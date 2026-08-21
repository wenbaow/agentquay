//! 连接 AgentQuay Bridge 的应用侧客户端（设计文档 §4.8）。
//!
//! 职责：注册 Tool、心跳、调用分发、确认流程、指数退避重连、token 持久化、
//! auto-spawn 内嵌 Bridge。

use std::any::Any;
use std::future::Future;
use std::panic::AssertUnwindSafe;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, AtomicU16, Ordering};
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::Duration;

use futures_util::{SinkExt, StreamExt};
use log::{debug, info, warn};
use serde_json::{json, Value};
use tokio::sync::{watch, Mutex as AsyncMutex};
use tokio_tungstenite::connect_async;
use tokio_tungstenite::tungstenite::Message;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::{MaybeTlsStream, WebSocketStream};

use crate::confirm::{default_handler, ConfirmationHandler};
use crate::error::AgentQuayError;
use crate::tool_call::ToolCallHandler;
use crate::protocol::{
    now_timestamp, ConfirmPayload, ConfirmResultPayload, Envelope, ErrorInfo, InvokePayload,
    LaunchInfo, ResultPayload, DISCONNECT_MIGRATE, DISCONNECT_NORMAL, DISCONNECT_REPLACED, MSG_CONFIRM,
    MSG_CONFIRM_RESULT, MSG_DISCONNECT, MSG_INVOKE, MSG_NOTIFICATION, MSG_PING, MSG_PONG,
    MSG_REGISTER, MSG_REGISTER_ACK, MSG_REGISTER_ERROR, MSG_RESULT,
};
use crate::spawn::{ensure_bridge, read_port_file};
use crate::token::TokenStore;
use crate::tool::{
    is_valid_app_id, is_valid_tool_name, FnToolProvider, IntoProviders, ToolError, ToolMetadata,
    ToolProvider,
};
use crate::PROTOCOL_VERSION;

/// 注册等待超时（秒）。
const REGISTER_TIMEOUT: Duration = Duration::from_secs(10);
/// WebSocket 连接超时。
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
/// 心跳判定：连续两个周期无消息即连接死亡。
const MAX_SILENT_CYCLES: u32 = 2;

type WsStream = WebSocketStream<MaybeTlsStream<tokio::net::TcpStream>>;
type WsSink = futures_util::stream::SplitSink<WsStream, Message>;

/// 已登记的 Tool（元数据 + 分发目标）。
type ToolEntry = (ToolMetadata, Arc<dyn ToolProvider>);

/// 连接 AgentQuay Bridge 的应用侧客户端。
pub struct AgentQuayClient {
    app_id: String,
    app_name: String,
    host: String,
    port: AtomicU16, // 0 = 尚未解析（从端口文件读取）
    auto_spawn_bridge: bool,
    version: String,
    protocol_version: String,
    heartbeat_interval: Duration,
    max_retry_interval: Duration,
    confirm_handler: Arc<dyn ConfirmationHandler>,
    /// 上报给 Bridge 的启动命令（§5.8，离线自动拉起用）。
    launch_info: Option<LaunchInfo>,
    /// 工具调用钩子：在业务方法执行前触发。
    tool_call_handler: Option<Arc<dyn ToolCallHandler>>,

    /// 已登记工具（注册顺序保持，供 tools/list 展示）。
    registry: Mutex<Vec<ToolEntry>>,
    /// 钉扎 token（内存副本；首次注册后从 register_ack 更新并持久化）。
    token: Mutex<Option<String>>,
    token_store: TokenStore,

    /// 停止信号（close() 置位）。
    stop_tx: watch::Sender<bool>,
    stop_rx: watch::Receiver<bool>,
    /// 本进程拉起的 Bridge 子进程（close() / Drop 时终止）。
    spawned: Mutex<Option<std::process::Child>>,
    /// 是否已执行过 close()（禁止再次 connect）。
    closed: AtomicBool,
}

impl AgentQuayClient {
    pub fn builder() -> ClientBuilder {
        ClientBuilder::new()
    }

    // ------------------------------------------------------------------
    // 工具注册
    // ------------------------------------------------------------------

    /// 登记 Tool（单个 ToolProvider 或元组）。
    ///
    /// 注意：需在 `connect()` 之前完成注册——协议每次连接只允许一次注册。
    pub fn register_tools<P: IntoProviders>(&self, providers: P) -> Result<(), AgentQuayError> {
        for provider in providers.into_providers() {
            for meta in provider.tools() {
                if !is_valid_tool_name(&meta.name) {
                    return Err(AgentQuayError::InvalidTool(format!(
                        "tool 名 {:?} 不符合规范 [a-zA-Z0-9_-]{{1,78}}",
                        meta.name
                    )));
                }
                let mut registry = self.registry.lock().unwrap();
                if registry.iter().any(|(m, _)| m.name == meta.name) {
                    return Err(AgentQuayError::InvalidTool(format!("tool 名重复: {}", meta.name)));
                }
                debug!("已登记 tool: {}", meta.name);
                registry.push((meta, provider.clone()));
            }
        }
        Ok(())
    }

    /// 登记闭包 Tool（免过程宏；参数为原始 JSON，schema 为开放对象）。
    pub fn register_fn<F>(
        &self,
        name: impl Into<String>,
        description: impl Into<String>,
        f: F,
    ) -> Result<(), AgentQuayError>
    where
        F: Fn(Value) -> Result<Value, ToolError> + Send + Sync + 'static,
    {
        let provider = FnToolProvider::new(
            name,
            description,
            false,
            ToolMetadata::DEFAULT_TIMEOUT_SECONDS,
            ToolMetadata::DEFAULT_CONFIRM_TIMEOUT_SECONDS,
            f,
        );
        self.register_tools(provider)
    }

    /// 登记 async 闭包 Tool。
    pub fn register_fn_async<F>(
        &self,
        name: impl Into<String>,
        description: impl Into<String>,
        f: F,
    ) -> Result<(), AgentQuayError>
    where
        F: Fn(Value) -> Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send + 'static>>
            + Send
            + Sync
            + 'static,
    {
        struct AsyncFnTool {
            meta: ToolMetadata,
            f: Box<
                dyn Fn(
                        Value,
                    ) -> Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send + 'static>>
                    + Send
                    + Sync,
            >,
        }
        impl ToolProvider for AsyncFnTool {
            fn tools(&self) -> Vec<ToolMetadata> {
                vec![self.meta.clone()]
            }
            fn invoke(
                self: Arc<Self>,
                _tool: &str,
                args: Value,
            ) -> Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send + 'static>> {
                Box::pin(async move { (self.f)(args).await })
            }
        }
        let provider = AsyncFnTool {
            meta: ToolMetadata::new(
                name,
                description,
                json!({ "type": "object", "properties": {} }),
                false,
                ToolMetadata::DEFAULT_TIMEOUT_SECONDS,
                ToolMetadata::DEFAULT_CONFIRM_TIMEOUT_SECONDS,
            ),
            f: Box::new(f),
        };
        self.register_tools(provider)
    }

    /// 已登记的 tool 名列表。
    pub fn list_tools(&self) -> Vec<String> {
        self.registry
            .lock()
            .unwrap()
            .iter()
            .map(|(m, _)| m.name.clone())
            .collect()
    }

    /// 当前钉扎 token（本地持久化的值）。
    pub fn current_token(&self) -> Option<String> {
        self.token.lock().unwrap().clone()
    }

    // ------------------------------------------------------------------
    // 连接生命周期
    // ------------------------------------------------------------------

    /// 连接 Bridge 并保持（心跳 + 自动重连）。
    ///
    /// 阻塞直到 `close()`（返回 `Ok(())`）、连接被同 appId 的新实例替换
    /// （`Err(AgentQuayError::Replaced)`）或认证失败（`Err(AuthFailed)`）。
    pub async fn connect(&self) -> Result<(), AgentQuayError> {
        if self.closed.load(Ordering::Relaxed) {
            return Err(AgentQuayError::Closed);
        }

        // 首次连接：确保 Bridge 可用（含 auto-spawn），解析实际端口
        let (port, spawned) = ensure_bridge(&self.host, self.port(), self.auto_spawn_bridge).await?;
        self.set_port(port);
        if let Some(child) = spawned {
            info!("本进程已拉起内嵌 Bridge (pid {})", child.id());
            *self.spawned.lock().unwrap() = Some(child);
        }

        let mut stop_rx = self.stop_rx.clone();
        let mut backoff = Duration::from_secs(1);
        loop {
            if *stop_rx.borrow() {
                return Ok(());
            }
            match self.connect_once().await {
                Ok(()) => {
                    // 连接正常结束（close() 中断）
                    backoff = Duration::from_secs(1);
                }
                Err(e @ AgentQuayError::Replaced) => {
                    warn!("连接被同 appId 的新实例替换，停止重连");
                    return Err(e);
                }
                Err(e @ AgentQuayError::AuthFailed(_)) => {
                    // token 钉扎失败不重试：token 可能被轮换，需重新授权（与 Python SDK 一致）
                    warn!("认证失败，停止重连（token 可能已被轮换）");
                    return Err(e);
                }
                Err(e) => {
                    if *stop_rx.borrow() {
                        return Ok(());
                    }
                    // Bridge 可能已重启并发生端口漂移（autoPort）：重读权威端口文件
                    if let Some(read) = read_port_file() {
                        if read > 0 && read != self.port() {
                            info!("Bridge 端口变化 {} → {}", self.port(), read);
                            self.set_port(read);
                        }
                    }
                    warn!("连接失败: {e}（{}s 后重连）", backoff.as_secs());
                    tokio::select! {
                        _ = tokio::time::sleep(backoff) => {}
                        _ = stop_rx.changed() => return Ok(()),
                    }
                    backoff = (backoff * 2).min(self.max_retry_interval);
                }
            }
        }
    }

    /// 优雅关闭：停止重连、断开连接。内嵌 Bridge 生命周期由自己管理（空闲自回收），
    /// 宿主退出不再 kill——见 embedded-bridge-lifecycle.md 步骤 1。
    pub fn close(&self) {
        self.closed.store(true, Ordering::Relaxed);
        let _ = self.stop_tx.send(true);
        if self.spawned.lock().unwrap().take().is_some() {
            info!("内嵌 Bridge 交由空闲自回收退出");
        }
    }

    /// 当前实际端口（0 = 尚未解析）。
    pub fn port(&self) -> u16 {
        self.port.load(Ordering::Relaxed)
    }

    // ------------------------------------------------------------------
    // 内部实现
    // ------------------------------------------------------------------

    fn set_port(&self, port: u16) {
        self.port.store(port, Ordering::Relaxed);
    }

    fn token(&self) -> Option<String> {
        self.token.lock().unwrap().clone()
    }

    fn set_token(&self, token: Option<String>) {
        *self.token.lock().unwrap() = token;
    }

    /// 连接一次：连接 WS → 注册 → 消息循环（心跳 / 调用分发 / 确认）。
    async fn connect_once(&self) -> Result<(), AgentQuayError> {
        let port = self.port();
        if port == 0 {
            let read = read_port_file().unwrap_or(0);
            if read == 0 {
                return Err(AgentQuayError::BridgeUnavailable("端口文件缺失".into()));
            }
            self.set_port(read);
        }
        let url = format!("ws://{}:{}/ws", self.host, self.port());
        debug!("连接 Bridge: {url}");
        let request = url
            .clone()
            .into_client_request()
            .map_err(|e| AgentQuayError::Connection(format!("WebSocket 地址非法: {e}")))?;
        let (ws, _) = tokio::time::timeout(CONNECT_TIMEOUT, connect_async(request))
            .await
            .map_err(|_| AgentQuayError::Connection("WebSocket 连接超时（10s）".into()))?
            .map_err(|e| AgentQuayError::Connection(format!("WebSocket 连接失败: {e}")))?;
        let (sink, mut stream) = ws.split();
        let sink = Arc::new(AsyncMutex::new(sink));

        // 注册（tools 快照；token 首次留空，重连携带）
        let tools = self.snapshot_tools();
        let token = self.token();
        self.send_msg(
            &sink,
            MSG_REGISTER,
            Some(register_payload(
                &self.app_id,
                &self.app_name,
                &self.version,
                &self.protocol_version,
                token.as_deref().unwrap_or(""),
                &tools,
                self.launch_info.as_ref(),
            )),
        )
        .await?;

        // 等待 register_ack / register_error（10s）
        let ack = tokio::time::timeout(REGISTER_TIMEOUT, async {
            loop {
                match stream.next().await {
                    Some(Ok(Message::Text(text))) => {
                        let env = Envelope::parse(text.as_str()).map_err(|e| {
                            AgentQuayError::Protocol(format!("无法解析 Bridge 消息: {e}"))
                        })?;
                        match env.msg_type.as_str() {
                            MSG_REGISTER_ACK => {
                                let token = env
                                    .payload_object()
                                    .get("token")
                                    .and_then(|v| v.as_str())
                                    .map(str::to_owned)
                                    .filter(|t| !t.is_empty());
                                return Ok::<Option<String>, AgentQuayError>(token);
                            }
                            MSG_REGISTER_ERROR => {
                                let p = env.payload_object();
                                let code = p
                                    .get("code")
                                    .and_then(|v| v.as_str())
                                    .unwrap_or("UNKNOWN")
                                    .to_string();
                                let message = p
                                    .get("message")
                                    .and_then(|v| v.as_str())
                                    .unwrap_or("")
                                    .to_string();
                                let supported: Vec<String> = p
                                    .get("supportedVersions")
                                    .and_then(|v| v.as_array())
                                    .map(|arr| {
                                        arr.iter()
                                            .filter_map(|v| v.as_str().map(str::to_owned))
                                            .collect()
                                    })
                                    .unwrap_or_default();
                                if code == "AUTH_FAILED" {
                                    return Err(AgentQuayError::AuthFailed(message));
                                }
                                return Err(AgentQuayError::Registration {
                                    code,
                                    message,
                                    supported_versions: supported,
                                });
                            }
                            MSG_PING => {
                                self.send_msg(
                                    &sink,
                                    MSG_PONG,
                                    Some(json!({ "timestamp": now_timestamp() })),
                                )
                                .await?;
                            }
                            other => warn!("注册等待期间收到意外消息: {other}"),
                        }
                    }
                    Some(Ok(Message::Close(_))) => {
                        return Err(AgentQuayError::Connection("注册期间连接关闭".into()));
                    }
                    Some(Ok(_)) => {} // 二进制帧等，忽略
                    Some(Err(e)) => {
                        return Err(AgentQuayError::Connection(format!("连接断开: {e}")));
                    }
                    None => return Err(AgentQuayError::Connection("连接已关闭".into())),
                }
            }
        })
        .await
        .map_err(|_| AgentQuayError::Connection("注册超时（10s 未收到 register_ack）".into()))??;

        if let Some(new_token) = ack {
            self.set_token(Some(new_token.clone()));
            self.token_store.set(&new_token); // 持久化，重连自动携带
            debug!("已保存钉扎 token");
        }
        info!("注册成功: {} (tools={})", self.app_id, tools.len());

        // 稳态消息循环：心跳 + 调用分发
        let mut stop_rx = self.stop_rx.clone();
        let mut silent_cycles = 0u32;
        loop {
            tokio::select! {
                msg = stream.next() => {
                    match msg {
                        Some(Ok(Message::Text(text))) => {
                            silent_cycles = 0;
                            if let Err(e) = self.handle_message(&sink, text.as_str()).await {
                                return Err(e);
                            }
                        }
                        Some(Ok(Message::Ping(_))) => {} // tungstenite 协议层自动回 Pong
                        Some(Ok(Message::Close(_))) => {
                            return Err(AgentQuayError::Connection("连接已关闭".into()));
                        }
                        Some(Ok(_)) => {}
                        Some(Err(e)) => {
                            return Err(AgentQuayError::Connection(format!("连接断开: {e}")));
                        }
                        None => return Err(AgentQuayError::Connection("连接已关闭".into())),
                    }
                }
                _ = tokio::time::sleep(self.heartbeat_interval * 2) => {
                    // 心跳：发送 ping；连续两次超时判定连接死亡
                    silent_cycles += 1;
                    self.send_msg(&sink, MSG_PING, Some(json!({ "timestamp": now_timestamp() })))
                        .await?;
                    if silent_cycles >= MAX_SILENT_CYCLES {
                        return Err(AgentQuayError::Connection("心跳超时（无响应）".into()));
                    }
                }
                _ = stop_rx.changed() => return Ok(()),
            }
        }
    }

    /// 处理一条消息（invoke / confirm 分发给独立任务，不阻塞消息循环）。
    async fn handle_message(
        &self,
        sink: &Arc<AsyncMutex<WsSink>>,
        raw: &str,
    ) -> Result<(), AgentQuayError> {
        let env = match Envelope::parse(raw) {
            Ok(env) => env,
            Err(e) => {
                warn!("无法解析 Bridge 消息（忽略）: {e}");
                return Ok(());
            }
        };
        match env.msg_type.as_str() {
            MSG_INVOKE => {
                let payload: InvokePayload = match env
                    .payload
                    .as_ref()
                    .map(|p| serde_json::from_value(p.clone()))
                {
                    Some(Ok(p)) => p,
                    _ => {
                        warn!("invoke 消息 payload 非法（忽略）");
                        return Ok(());
                    }
                };
                self.spawn_invoke(sink.clone(), payload);
            }
            MSG_CONFIRM => {
                let payload: ConfirmPayload = match env
                    .payload
                    .as_ref()
                    .map(|p| serde_json::from_value(p.clone()))
                {
                    Some(Ok(p)) => p,
                    _ => {
                        warn!("confirm 消息 payload 非法（忽略）");
                        return Ok(());
                    }
                };
                self.spawn_confirm(sink.clone(), payload);
            }
            MSG_PING => {
                self.send_msg(sink, MSG_PONG, Some(json!({ "timestamp": now_timestamp() }))).await?;
            }
            MSG_PONG => {} // 静默计数已在消息循环重置
            MSG_DISCONNECT => {
                let reason = env
                    .payload_object()
                    .get("reason")
                    .and_then(|v| v.as_str())
                    .unwrap_or(DISCONNECT_NORMAL)
                    .to_string();
                if reason == DISCONNECT_REPLACED {
                    return Err(AgentQuayError::Replaced);
                }
                if reason == DISCONNECT_MIGRATE {
                    // Bridge 让位给更强的系统服务实例：按普通断线重连，重连会重读端口文件
                    log::info!("Bridge 让位，重连时将重读端口文件");
                }
                return Err(AgentQuayError::Connection(format!("Bridge 断开: {reason}")));
            }
            MSG_NOTIFICATION => {
                let payload = env.payload_object();
                let message = payload
                    .get("message")
                    .and_then(|v| v.as_str())
                    .unwrap_or("");
                info!("Bridge 通知: {message}");
            }
            other => warn!("未知消息类型: {other}"),
        }
        Ok(())
    }

    // ------------------------------------------------------------------
    // 调用分发
    // ------------------------------------------------------------------

    fn spawn_invoke(&self, sink: Arc<AsyncMutex<WsSink>>, payload: InvokePayload) {
        let entry = self.find_tool(&payload.tool);
        let Some((meta, provider)) = entry else {
            // tool 不存在：直接返回错误结果（与 Java SDK 一致）
            let error = ToolError::not_found(payload.tool.clone());
            tokio::spawn(async move {
                let resp = result_message(&payload.request_id, InvokeOutcome::ToolError(error));
                if let Err(e) = send_msg_raw(&sink, MSG_RESULT, &resp).await {
                    warn!("发送 result 失败: {e}");
                }
            });
            return;
        };

        // 触发工具调用钩子（在业务方法执行前）
        if let Some(handler) = &self.tool_call_handler {
            let tool_name = payload.tool.clone();
            let arguments = payload.arguments.clone();
            handler.on_tool_call(&tool_name, Some(&arguments));
        }

        let provider = provider.clone();
        let timeout_seconds = payload.timeout_seconds.unwrap_or(meta.timeout_seconds);
        debug!("执行 tool: {} args={}", payload.tool, payload.arguments);
        tokio::spawn(async move {
            let outcome =
                Self::dispatch_tool(provider, &payload.tool, payload.arguments, timeout_seconds).await;
            let resp = result_message(&payload.request_id, outcome);
            if let Err(e) = send_msg_raw(&sink, MSG_RESULT, &resp).await {
                warn!("发送 result 失败: {e}");
            }
        });
    }

    /// 执行 Tool：超时上限 = Bridge 侧执行超时 + 5s 余量
    /// （保证 Bridge 先超时，SDK 迟到的结果进孤儿处理，与 Java / Python 一致）。
    async fn dispatch_tool(
        provider: Arc<dyn ToolProvider>,
        tool: &str,
        args: Value,
        timeout_seconds: u32,
    ) -> InvokeOutcome {
        let fut = provider.invoke(tool, args);
        // panic 兜底：工具 panic 不崩进程，转为 EXECUTION_ERROR
        let guarded = CatchUnwind(AssertUnwindSafe(fut));
        let margin = timeout_seconds as u64 + 5;
        match tokio::time::timeout(Duration::from_secs(margin), guarded).await {
            Err(_elapsed) => InvokeOutcome::Timeout(timeout_seconds),
            Ok(Ok(Ok(value))) => InvokeOutcome::Success(value),
            Ok(Ok(Err(e))) => InvokeOutcome::ToolError(e),
            Ok(Err(panic)) => InvokeOutcome::Panic(panic_message(panic)),
        }
    }

    fn find_tool(&self, name: &str) -> Option<ToolEntry> {
        self.registry
            .lock()
            .unwrap()
            .iter()
            .find(|(m, _)| m.name == name)
            .cloned()
    }

    fn snapshot_tools(&self) -> Vec<ToolMetadata> {
        self.registry.lock().unwrap().iter().map(|(m, _)| m.clone()).collect()
    }

    // ------------------------------------------------------------------
    // 确认流程
    // ------------------------------------------------------------------

    fn spawn_confirm(&self, sink: Arc<AsyncMutex<WsSink>>, payload: ConfirmPayload) {
        let handler = self.confirm_handler.clone();
        let message = payload.message.unwrap_or_else(|| "确认执行操作？".into());
        let arguments = payload.arguments;
        let timeout_seconds = payload
            .timeout_seconds
            .unwrap_or(ToolMetadata::DEFAULT_CONFIRM_TIMEOUT_SECONDS);
        tokio::spawn(async move {
            // 确认回调可能阻塞（控制台输入 / 原生弹窗），放阻塞线程池执行
            let confirmed = tokio::task::spawn_blocking(move || {
                handler.confirm(&message, arguments.as_ref(), timeout_seconds)
            })
            .await
            .unwrap_or(false); // 线程池异常 → 默认拒绝（安全优先）
            let payload = ConfirmResultPayload { request_id: &payload.request_id, confirmed };
            let env = Envelope::new(MSG_CONFIRM_RESULT, serde_json::to_value(payload).ok());
            match env.encode() {
                Ok(raw) => {
                    if let Err(e) = send_msg_raw(&sink, MSG_CONFIRM_RESULT, &raw).await {
                        warn!("发送 confirm_result 失败: {e}");
                    }
                }
                Err(e) => warn!("confirm_result 序列化失败: {e}"),
            }
        });
    }

    // ------------------------------------------------------------------
    // 发送
    // ------------------------------------------------------------------

    async fn send_msg(
        &self,
        sink: &Arc<AsyncMutex<WsSink>>,
        msg_type: &str,
        payload: Option<Value>,
    ) -> Result<(), AgentQuayError> {
        let env = Envelope::new(msg_type, payload);
        let raw = env
            .encode()
            .map_err(|e| AgentQuayError::Protocol(format!("消息序列化失败: {e}")))?;
        send_msg_raw(sink, msg_type, &raw).await
    }
}

impl Drop for AgentQuayClient {
    fn drop(&mut self) {
        self.close();
    }
}

/// 注册消息 payload（§5.8：可选 launch 启动命令字段）。
fn register_payload(
    app_id: &str,
    app_name: &str,
    version: &str,
    protocol_version: &str,
    auth_token: &str,
    tools: &[ToolMetadata],
    launch: Option<&LaunchInfo>,
) -> Value {
    let mut payload = json!({
        "appId": app_id,
        "appName": app_name,
        "version": version,
        "protocolVersion": protocol_version,
        "authToken": auth_token,
        "tools": tools,
    });
    if let Some(l) = launch {
        payload["launch"] = serde_json::to_value(l).unwrap_or_default();
    }
    payload
}

/// 发送一条消息（写锁串行化；dispatch / confirm 任务共用）。
async fn send_msg_raw(
    sink: &Arc<AsyncMutex<WsSink>>,
    msg_type: &str,
    raw: &str,
) -> Result<(), AgentQuayError> {
    sink.lock()
        .await
        .send(Message::text(raw.to_string()))
        .await
        .map_err(|e| AgentQuayError::Connection(format!("发送 {msg_type} 失败: {e}")))
}

/// 一次 Tool 调用的执行结果（用于构造 result 消息）。
enum InvokeOutcome {
    Success(Value),
    ToolError(ToolError),
    /// Bridge 侧已超时；SDK 迟到的结果由 Bridge 进孤儿缓冲。
    Timeout(u32),
    Panic(String),
}

fn result_message(request_id: &str, outcome: InvokeOutcome) -> String {
    let payload = match outcome {
        InvokeOutcome::Success(data) => ResultPayload {
            request_id,
            success: true,
            data: Some(data),
            error: None,
        },
        InvokeOutcome::ToolError(e) => ResultPayload {
            request_id,
            success: false,
            data: None,
            error: Some(ErrorInfo { code: e.code, message: e.message, details: e.details }),
        },
        InvokeOutcome::Timeout(seconds) => ResultPayload {
            request_id,
            success: false,
            data: None,
            error: Some(ErrorInfo {
                code: "EXECUTION_TIMEOUT".into(),
                message: format!("执行超时（>{seconds}s）"),
                details: None,
            }),
        },
        InvokeOutcome::Panic(message) => ResultPayload {
            request_id,
            success: false,
            data: None,
            error: Some(ErrorInfo {
                code: "EXECUTION_ERROR".into(),
                message: format!("tool 执行 panic: {message}"),
                details: None,
            }),
        },
    };
    Envelope::new(MSG_RESULT, Some(serde_json::to_value(payload).unwrap()))
        .encode()
        .unwrap()
}

/// panic payload → 可读消息。
fn panic_message(payload: Box<dyn Any + Send>) -> String {
    if let Some(s) = payload.downcast_ref::<&str>() {
        (*s).to_string()
    } else if let Some(s) = payload.downcast_ref::<String>() {
        s.clone()
    } else {
        "未知 panic".to_string()
    }
}

/// 在每次 poll 时用 `catch_unwind` 包裹内部 Future 的包装器：
/// 工具（async 方法）panic 时不会终止任务，而是转为 Err(panic)。
struct CatchUnwind<F>(F);

impl<F: Future> Future for CatchUnwind<F> {
    type Output = Result<F::Output, Box<dyn Any + Send>>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        // SAFETY: 外层 future 被 pin，内部 future 的数据位置随之稳定
        let this = unsafe { self.get_unchecked_mut() };
        let mut inner = unsafe { Pin::new_unchecked(&mut this.0) };
        match std::panic::catch_unwind(AssertUnwindSafe(|| inner.as_mut().poll(cx))) {
            Ok(poll) => poll.map(Ok),
            Err(payload) => Poll::Ready(Err(payload)),
        }
    }
}

// ---------------------------------------------------------------------------
// Builder
// ---------------------------------------------------------------------------

/// `AgentQuayClient` 构造器。
pub struct ClientBuilder {
    app_id: String,
    app_name: String,
    host: String,
    port: u16,
    auto_spawn_bridge: bool,
    version: String,
    protocol_version: String,
    heartbeat_interval: u64,
    max_retry_interval: u64,
    confirm_handler: Option<Arc<dyn ConfirmationHandler>>,
    launch_info: Option<LaunchInfo>,
    auto_report_launch: bool,
    tool_call_handler: Option<Arc<dyn ToolCallHandler>>,
}

impl Default for ClientBuilder {
    fn default() -> Self {
        Self::new()
    }
}

impl ClientBuilder {
    pub fn new() -> Self {
        Self {
            app_id: String::new(),
            app_name: String::new(),
            host: "127.0.0.1".into(),
            port: 0,
            auto_spawn_bridge: true,
            version: "1.0.0".into(),
            protocol_version: PROTOCOL_VERSION.into(),
            heartbeat_interval: 30,
            max_retry_interval: 30,
            confirm_handler: None,
            launch_info: None,
            auto_report_launch: true,
            tool_call_handler: None,
        }
    }

    /// 应用 ID（[a-z0-9-]{1,48}，禁止 `_` 和 `.`；如 music-app）。
    pub fn app_id(mut self, app_id: impl Into<String>) -> Self {
        self.app_id = app_id.into();
        self
    }

    /// 应用显示名（Agent 侧 tools/list 描述带 `[应用名]` 前缀）。
    pub fn app_name(mut self, app_name: impl Into<String>) -> Self {
        self.app_name = app_name.into();
        self
    }

    /// Bridge 地址（默认 127.0.0.1）。
    pub fn host(mut self, host: impl Into<String>) -> Self {
        self.host = host.into();
        self
    }

    /// Bridge 端口（0 = 从 ~/.agentquay/port 自动读取）。
    pub fn port(mut self, port: u16) -> Self {
        self.port = port;
        self
    }

    /// 未检测到服务时自动拉起内嵌 Bridge（默认 true）。
    pub fn auto_spawn_bridge(mut self, auto_spawn_bridge: bool) -> Self {
        self.auto_spawn_bridge = auto_spawn_bridge;
        self
    }

    /// 应用版本号（随注册上报，默认 1.0.0）。
    pub fn version(mut self, version: impl Into<String>) -> Self {
        self.version = version.into();
        self
    }

    /// 协议版本（默认 "1.0"）。
    pub fn protocol_version(mut self, protocol_version: impl Into<String>) -> Self {
        self.protocol_version = protocol_version.into();
        self
    }

    /// 心跳间隔秒数（默认 30）。
    pub fn heartbeat_interval(mut self, seconds: u64) -> Self {
        self.heartbeat_interval = seconds;
        self
    }

    /// 重连退避上限秒数（默认 30）。
    pub fn max_retry_interval(mut self, seconds: u64) -> Self {
        self.max_retry_interval = seconds;
        self
    }

    /// 自定义确认回调（默认：控制台 y/n；native-confirm 特性开启时为 OS 原生弹窗）。
    pub fn confirm_handler(mut self, handler: Arc<dyn ConfirmationHandler>) -> Self {
        self.confirm_handler = Some(handler);
        self
    }

    /// 工具调用钩子：在业务方法执行前触发，允许 UI 层拦截并响应。
    pub fn tool_call_handler(mut self, handler: Arc<dyn ToolCallHandler>) -> Self {
        self.tool_call_handler = Some(handler);
        self
    }

    /// 启动命令（§5.8）：显式指定，随 register 上报供 Bridge 离线自动拉起。
    /// 缺省自动探测当前可执行文件。
    pub fn launch_info(mut self, info: LaunchInfo) -> Self {
        self.launch_info = Some(info);
        self
    }

    /// 是否在注册时自动上报启动命令（默认 true）。
    pub fn auto_report_launch(mut self, enable: bool) -> Self {
        self.auto_report_launch = enable;
        self
    }

    pub fn build(self) -> Result<AgentQuayClient, AgentQuayError> {
        if !is_valid_app_id(&self.app_id) {
            return Err(AgentQuayError::InvalidConfig(format!(
                "appId {:?} 不符合规范 [a-z0-9-]{{1,48}}（禁止 _ 和 .）",
                self.app_id
            )));
        }
        if self.app_name.is_empty() {
            return Err(AgentQuayError::InvalidConfig("appName 不能为空".into()));
        }
        let launch_info = self
            .launch_info
            .or_else(|| self.auto_report_launch.then(detect_launch_info).flatten());
        let (stop_tx, stop_rx) = watch::channel(false);
        let token_store = TokenStore::new(&self.app_id);
        Ok(AgentQuayClient {
            app_id: self.app_id,
            app_name: self.app_name,
            host: self.host,
            port: AtomicU16::new(self.port),
            auto_spawn_bridge: self.auto_spawn_bridge,
            version: self.version,
            protocol_version: self.protocol_version,
            heartbeat_interval: Duration::from_secs(self.heartbeat_interval.max(1)),
            max_retry_interval: Duration::from_secs(self.max_retry_interval.max(1)),
            confirm_handler: self.confirm_handler.unwrap_or_else(default_handler),
            launch_info,
            tool_call_handler: self.tool_call_handler,
            registry: Mutex::new(Vec::new()),
            token: Mutex::new(token_store.get()),
            token_store,
            stop_tx,
            stop_rx,
            spawned: Mutex::new(None),
            closed: AtomicBool::new(false),
        })
    }
}

/// 自动探测当前进程的启动命令（§5.8）：二进制应用直接使用当前可执行文件路径。
/// 探测失败时返回 None（不阻塞注册）。
fn detect_launch_info() -> Option<LaunchInfo> {
    let exe = std::env::current_exe().ok()?;
    if exe.as_os_str().is_empty() {
        return None;
    }
    Some(LaunchInfo::new(exe.to_string_lossy().into_owned()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builder_validates_app_id() {
        assert!(ClientBuilder::new().app_id("music-app").app_name("Music").build().is_ok());
        assert!(ClientBuilder::new().app_id("music_app").app_name("Music").build().is_err());
        assert!(ClientBuilder::new().app_id("music-app").app_name("").build().is_err());
    }

    #[test]
    fn register_tools_validation() {
        let client = ClientBuilder::new().app_id("test-app").app_name("T").build().unwrap();
        let good = FnToolProvider::new("ok_tool", "desc", false, 30, 120, |_| Ok(Value::Null));
        assert!(client.register_tools(good).is_ok());

        let dup = FnToolProvider::new("ok_tool", "desc", false, 30, 120, |_| Ok(Value::Null));
        assert!(matches!(client.register_tools(dup), Err(AgentQuayError::InvalidTool(_))));

        let bad = FnToolProvider::new("bad.name", "desc", false, 30, 120, |_| Ok(Value::Null));
        assert!(matches!(client.register_tools(bad), Err(AgentQuayError::InvalidTool(_))));
    }

    #[test]
    fn register_payload_includes_launch() {
        let tools: [ToolMetadata; 0] = [];
        // 不传 launch：不出现 launch 字段
        let p1 = register_payload("app", "名", "1.0", "1.0", "", &tools, None);
        assert!(p1.get("launch").is_none());

        // 传入 launch：出现完整字段
        let li = LaunchInfo::new("/opt/app/bin/app")
            .with_args(vec!["--x".into()])
            .single_instance(true)
            .launch_timeout(20);
        let p2 = register_payload("app", "名", "1.0", "1.0", "", &tools, Some(&li));
        let launch = p2.get("launch").expect("应包含 launch");
        assert_eq!(launch["execPath"], "/opt/app/bin/app");
        assert_eq!(launch["args"][0], "--x");
        assert_eq!(launch["singleInstance"], true);
        assert_eq!(launch["launchTimeoutSeconds"], 20);

        // 默认探测：构建客户端时 laucnh_info 自动填充（当前测试进程的可执行文件存在）
        let client = AgentQuayClient::builder().app_id("test-app").app_name("T").build().unwrap();
        assert!(client.launch_info.is_some());

        // auto_report_launch(false)：不自动填充
        let client2 = AgentQuayClient::builder()
            .app_id("test-app")
            .app_name("T")
            .auto_report_launch(false)
            .build()
            .unwrap();
        assert!(client2.launch_info.is_none());
    }
}
