//! 端到端联调测试：真实 Go Bridge + Rust SDK + raw MCP 客户端（设计文档 §3.2）。
//!
//! 需要 Bridge 二进制：环境变量 `AGENTQUAY_BRIDGE_BIN`，或仓库内 `bridge/dist/`
//! 下与当前平台匹配的预编译二进制。
//!
//! 覆盖：注册/token、tools/list、正常调用（同步/异步）、-32003 参数校验、
//! 业务错误透传（ToolError code / EXECUTION_ERROR / panic）、确认流程（确认/取消 -32005）、
//! 同 appId 替换（Replaced）、断线重连携带 token。
//!
//! ```bash
//! cd sdk/rust && cargo test -p agentquay --test e2e_test
//! ```

use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::Arc;
use std::time::Duration;

use agentquay::confirm::AutoConfirmationHandler;
use agentquay::{agent_tool, AgentQuayClient, ToolError};
use serde_json::{json, Value};

const APP_ID: &str = "rust-e2e-app";

// ---------------------------------------------------------------------------
// 被测应用（SDK 侧，宏生成）
// ---------------------------------------------------------------------------

struct Controller;

#[agent_tool]
impl Controller {
    #[agent_tool(name = "echo", description = "回声")]
    fn echo(&self, message: String) -> Value {
        json!({ "received": message })
    }

    #[agent_tool(name = "add", description = "加法")]
    fn add(&self, a: i32, b: i32) -> i32 {
        a + b
    }

    #[agent_tool(name = "async_greet", description = "异步问候")]
    async fn async_greet(&self, name: String) -> String {
        tokio::time::sleep(Duration::from_millis(50)).await;
        format!("你好，{name}")
    }

    #[agent_tool(name = "fail", description = "普通业务失败")]
    fn fail(&self) -> Result<String, String> {
        Err("模拟业务失败".into())
    }

    #[agent_tool(name = "business_error", description = "自定义业务错误")]
    fn business_error(&self) -> Result<String, ToolError> {
        Err(ToolError::business("SEARCH_FAILED", "音乐库服务暂时不可用", None))
    }

    #[agent_tool(name = "panic_tool", description = "panic 测试")]
    fn panic_tool(&self) -> String {
        panic!("模拟 panic")
    }

    #[agent_tool(name = "confirm_op", description = "确认操作", requires_confirmation = true)]
    fn confirm_op(&self, value: String) -> Value {
        json!({ "confirmed_value": value })
    }
}

// ---------------------------------------------------------------------------
// 最小 MCP Streamable HTTP 客户端（与 Java e2e 同款流程）
// ---------------------------------------------------------------------------

struct McpClient {
    url: String,
    client: reqwest::Client,
    session_id: Option<String>,
    id: u32,
}

impl McpClient {
    fn new(port: u16) -> Self {
        Self {
            url: format!("http://127.0.0.1:{port}/mcp"),
            client: reqwest::Client::new(),
            session_id: None,
            id: 0,
        }
    }

    async fn post(&mut self, method: &str, params: Value) -> Value {
        self.id += 1;
        let mut body = json!({ "jsonrpc": "2.0", "id": self.id, "method": method });
        if params.is_object() && !params.as_object().unwrap().is_empty() {
            body["params"] = params;
        }
        let mut req = self
            .client
            .post(&self.url)
            .header("Content-Type", "application/json")
            .header("Accept", "application/json, text/event-stream")
            .body(body.to_string());
        if let Some(sid) = &self.session_id {
            req = req.header("Mcp-Session-Id", sid);
        }
        let resp = req.send().await.expect("MCP 请求失败");
        if let Some(sid) = resp.headers().get("mcp-session-id").and_then(|v| v.to_str().ok()) {
            self.session_id = Some(sid.to_string());
        }
        let ctype = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
            .to_string();
        let text = resp.text().await.expect("读取 MCP 响应失败");
        let data = if ctype.contains("text/event-stream") {
            last_sse_data(&text)
        } else {
            text
        };
        serde_json::from_str(&data).expect("解析 MCP 响应失败")
    }
}

/// 从 SSE 流提取最后一个 data 负载（即目标 JSON-RPC 响应）。
fn last_sse_data(body: &str) -> String {
    let mut last = None;
    for event in body.split("\n\n") {
        let mut payload = None;
        for line in event.lines() {
            if let Some(rest) = line.strip_prefix("data:") {
                payload = Some(rest.trim().to_string());
            }
        }
        if let Some(p) = payload {
            last = Some(p);
        }
    }
    last.expect("SSE 流中未找到 data")
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

/// 定位 Bridge 二进制：AGENTQUAY_BRIDGE_BIN 或仓库 bridge/dist。
fn find_bridge_binary() -> PathBuf {
    if let Ok(env) = std::env::var("AGENTQUAY_BRIDGE_BIN") {
        if !env.is_empty() {
            return PathBuf::from(env);
        }
    }
    // crate 根 → 仓库根（sdk/rust/crates/agentquay → 向上 4 级）
    let repo_root = Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(4)
        .expect("定位仓库根失败");
    let dist = repo_root.join("bridge").join("dist");
    let prefix = if cfg!(windows) {
        "agentquay-windows-"
    } else if cfg!(target_os = "macos") {
        "agentquay-darwin-"
    } else {
        "agentquay-linux-"
    };
    let exe = std::fs::read_dir(&dist)
        .unwrap_or_else(|e| panic!("bridge/dist 不存在（请先构建 Bridge）: {e}"))
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .find(|p| {
            p.file_name()
                .and_then(|n| n.to_str())
                .map(|n| n.starts_with(prefix))
                .unwrap_or(false)
        });
    exe.unwrap_or_else(|| panic!("bridge/dist 中未找到 {} 前缀的二进制", prefix))
}

async fn probe(port: u16) -> bool {
    tokio::time::timeout(
        Duration::from_millis(300),
        tokio::net::TcpStream::connect(("127.0.0.1", port)),
    )
    .await
    .is_ok_and(|r| r.is_ok())
}

fn read_port_file(dir: &Path) -> Option<u16> {
    let text = std::fs::read_to_string(dir.join(".agentquay").join("port")).ok()?;
    text.trim().parse().ok()
}

/// 轮询 admin/status 直到 appId 上线/下线。
async fn wait_app_state(port: u16, online: bool) -> bool {
    let client = reqwest::Client::new();
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    while tokio::time::Instant::now() < deadline {
        if let Ok(resp) = client
            .get(format!("http://127.0.0.1:{port}/admin/status"))
            .timeout(Duration::from_secs(2))
            .send()
            .await
        {
            if let Ok(body) = resp.text().await {
                if let Ok(v) = serde_json::from_str::<Value>(&body) {
                    let found = v
                        .get("apps")
                        .and_then(|a| a.as_array())
                        .map(|apps| apps.iter().any(|a| a.get("appId") == Some(&json!(APP_ID))))
                        .unwrap_or(false);
                    if found == online {
                        return true;
                    }
                }
            }
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    false
}

/// 从 MCP 响应的 content 中提取错误码（-32003 等）。
fn error_code(resp: &Value) -> i64 {
    resp.pointer("/result/content/0/text")
        .and_then(|t| t.as_str())
        .and_then(|t| serde_json::from_str::<Value>(t).ok())
        .and_then(|v| v.get("code").and_then(|c| c.as_i64()))
        .unwrap_or(0)
}

fn content_text(resp: &Value) -> String {
    resp.pointer("/result/content/0/text")
        .and_then(|t| t.as_str())
        .unwrap_or("")
        .to_string()
}

// ---------------------------------------------------------------------------
// 端到端测试（真实 Bridge）
// ---------------------------------------------------------------------------

/// Bridge 子进程守卫：测试结束强制终止。
struct BridgeGuard(Child);

impl Drop for BridgeGuard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn e2e_with_real_bridge() {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info")).init();

    // 1. 沙箱 HOME（token / 端口文件不触碰真实用户目录）
    let sandbox = std::env::temp_dir().join(format!("agentquay-e2e-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&sandbox);
    std::fs::create_dir_all(&sandbox).unwrap();
    std::env::set_var("HOME", &sandbox);
    #[cfg(windows)]
    std::env::set_var("USERPROFILE", &sandbox);

    // 2. 启动真实 Bridge（前台 serve）
    let binary = find_bridge_binary();
    let log_file = sandbox.join("bridge.log");
    let log_handle = std::fs::OpenOptions::new().create(true).append(true).open(&log_file).unwrap();
    let mut cmd = Command::new(&binary);
    cmd.arg("serve")
        .env("AGENTQUAY_LOG_LEVEL", "debug")
        .env("HOME", &sandbox)
        .stdout(Stdio::from(log_handle.try_clone().unwrap()))
        .stderr(Stdio::from(log_handle));
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
        cmd.creation_flags(CREATE_NEW_PROCESS_GROUP);
    }
    let child = cmd.spawn().expect("拉起 Bridge 失败");
    let _bridge = BridgeGuard(child);

    // 3. 等待端口文件 + 探测
    let mut port = 0u16;
    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    while tokio::time::Instant::now() < deadline {
        if let Some(p) = read_port_file(&sandbox) {
            if probe(p).await {
                port = p;
                break;
            }
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    assert!(port > 0, "Bridge 未在 15s 内就绪（日志: {}）", log_file.display());

    // 4. MCP initialize
    let mut rpc = McpClient::new(port);
    let init = rpc
        .post(
            "initialize",
            json!({
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": { "name": "rust-e2e", "version": "1.0" },
            }),
        )
        .await;
    assert!(init.get("result").is_some(), "initialize 失败: {init}");
    assert!(rpc.session_id.is_some(), "未建立 MCP 会话");

    // 5. SDK 客户端注册
    let confirm = Arc::new(AutoConfirmationHandler::new(true));
    let client = Arc::new(
        AgentQuayClient::builder()
            .app_id(APP_ID)
            .app_name("Rust E2E")
            .port(port)
            .auto_spawn_bridge(false)
            .heartbeat_interval(30)
            .max_retry_interval(2)
            .confirm_handler(confirm.clone())
            .build()
            .unwrap(),
    );
    client.register_tools(Controller).unwrap();
    let connect_task = tokio::spawn({
        let client = client.clone();
        async move { client.connect().await }
    });
    assert!(wait_app_state(port, true).await, "应用未上线");

    // 6. tools/list：合成名 + 描述前缀 + schema required
    let tools = rpc.post("tools/list", json!({})).await;
    let tools = tools.pointer("/result/tools").unwrap().as_array().unwrap().clone();
    let names: Vec<&str> = tools
        .iter()
        .map(|t| t.get("name").and_then(|n| n.as_str()).unwrap_or(""))
        .collect();
    assert!(names.contains(&"rust-e2e-app_echo"), "缺少 echo: {names:?}");
    assert!(names.contains(&"rust-e2e-app_confirm_op"), "缺少 confirm_op: {names:?}");

    let echo_tool = tools.iter().find(|t| {
        t.get("name").and_then(|n| n.as_str()) == Some("rust-e2e-app_echo")
    }).unwrap();
    assert!(
        echo_tool.get("description").and_then(|d| d.as_str()).unwrap_or("").starts_with("[Rust E2E]"),
        "描述缺应用名前缀"
    );
    assert_eq!(
        echo_tool.pointer("/inputSchema/required"),
        Some(&json!(["message"])),
        "echo schema required 不符: {echo_tool}"
    );

    // 7. 正常调用（同步 + 异步）
    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_echo", "arguments": { "message": "你好，Agent" } }))
        .await;
    // 成功响应没有 isError 字段（缺失 = 成功）
    assert!(!resp.pointer("/result/isError").and_then(|v| v.as_bool()).unwrap_or(false), "echo 失败: {resp}");
    assert_eq!(content_text(&resp), r#"{"received":"你好，Agent"}"#);

    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_add", "arguments": { "a": 3, "b": 4 } }))
        .await;
    assert_eq!(content_text(&resp), "7");

    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_async_greet", "arguments": { "name": "Agent" } }))
        .await;
    assert_eq!(content_text(&resp), r#""你好，Agent""#);

    // 8. 参数校验 → -32003（缺必填 / 类型错误，Bridge 不转发）
    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_echo", "arguments": {} }))
        .await;
    assert_eq!(error_code(&resp), -32003, "缺参应返回 -32003: {resp}");

    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_echo", "arguments": { "message": 123 } }))
        .await;
    assert_eq!(error_code(&resp), -32003, "类型错误应返回 -32003: {resp}");

    // 9. 业务错误透传
    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_fail", "arguments": {} }))
        .await;
    assert!(resp.pointer("/result/isError").and_then(|v| v.as_bool()).unwrap_or(false));
    assert!(content_text(&resp).contains("EXECUTION_ERROR"), "错误信息未透传: {}", content_text(&resp));

    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_business_error", "arguments": {} }))
        .await;
    assert!(resp.pointer("/result/isError").and_then(|v| v.as_bool()).unwrap_or(false));
    let text = content_text(&resp);
    assert!(text.contains("SEARCH_FAILED") && text.contains("音乐库服务暂时不可用"), "ToolError code 未透传: {text}");

    // 10. panic 兜底 → EXECUTION_ERROR（不崩进程）
    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_panic_tool", "arguments": {} }))
        .await;
    assert!(resp.pointer("/result/isError").and_then(|v| v.as_bool()).unwrap_or(false));
    assert!(content_text(&resp).contains("EXECUTION_ERROR"), "panic 未转为错误: {}", content_text(&resp));

    // 11. 确认流程：确认 → 成功；取消 → -32005
    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_confirm_op", "arguments": { "value": "v1" } }))
        .await;
    assert!(!resp.pointer("/result/isError").and_then(|v| v.as_bool()).unwrap_or(false), "确认后应成功: {resp}");

    confirm.set_answer(false);
    let resp = rpc
        .post("tools/call", json!({ "name": "rust-e2e-app_confirm_op", "arguments": { "value": "v2" } }))
        .await;
    assert_eq!(error_code(&resp), -32005, "取消应返回 -32005: {resp}");
    confirm.set_answer(true);

    // 12. 同 appId 替换 → 旧连接退出（Replaced），新实例在线
    let second = Arc::new(
        AgentQuayClient::builder()
            .app_id(APP_ID)
            .app_name("Rust E2E")
            .port(port)
            .auto_spawn_bridge(false)
            .confirm_handler(confirm.clone())
            .build()
            .unwrap(),
    );
    second.register_tools(Controller).unwrap();
    let second_task = tokio::spawn({
        let second = second.clone();
        async move { second.connect().await }
    });
    let replaced = tokio::time::timeout(Duration::from_secs(10), async {
        connect_task.await.expect("connect 任务 panic")
    })
    .await
    .expect("旧连接未在 10s 内退出");
    assert!(matches!(replaced, Err(agentquay::AgentQuayError::Replaced)), "应为 Replaced: {replaced:?}");
    assert!(wait_app_state(port, true).await, "新实例应在线");

    // 13. 断线重连携带 token：关闭新实例 → 新客户端用持久化 token 重新注册成功
    second.close();
    assert!(wait_app_state(port, false).await, "应用应下线");
    second_task.await.unwrap().unwrap(); // close() → connect 返回 Ok

    let fresh = Arc::new(
        AgentQuayClient::builder()
            .app_id(APP_ID)
            .app_name("Rust E2E")
            .port(port)
            .auto_spawn_bridge(false)
            .confirm_handler(confirm.clone())
            .build()
            .unwrap(),
    );
    fresh.register_tools(Controller).unwrap();
    let fresh_task = tokio::spawn({
        let fresh = fresh.clone();
        async move { fresh.connect().await }
    });
    assert!(wait_app_state(port, true).await, "重连（携带 token）应成功");

    // 清理
    fresh.close();
    fresh_task.await.unwrap().unwrap();
    let _ = std::fs::remove_dir_all(&sandbox);
    eprintln!("e2e 全部通过 ✅");
}
