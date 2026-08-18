//! 过程宏生成代码的集成测试（无需 Bridge）：验证 `#[agent_tool]` 生成的
//! 元数据（schemars schema）与调用分发（同步 / async / Result / 参数绑定）。

use std::sync::Arc;

use agentquay::{agent_tool, AgentQuayClient, ToolError, ToolProvider};

// ---------------------------------------------------------------------------
// impl 块模式
// ---------------------------------------------------------------------------

#[derive(agentquay::JsonSchema, agentquay::serde::Serialize, agentquay::serde::Deserialize)]
struct Point {
    x: f64,
    y: f64,
}

#[derive(agentquay::JsonSchema, agentquay::serde::Serialize, agentquay::serde::Deserialize)]
struct Song {
    id: String,
    title: String,
}

struct TestController;

#[agent_tool]
impl TestController {
    /// 搜索歌曲（description 回退 doc 首行）
    #[agent_tool(name = "search")]
    fn search(&self, keyword: String) -> Vec<Song> {
        vec![Song { id: "1".into(), title: keyword }]
    }

    #[agent_tool(name = "add", description = "加法")]
    fn add(&self, a: i32, b: i32) -> i32 {
        a + b
    }

    #[agent_tool(name = "point", description = "点")]
    fn point(&self, value: Point) -> Point {
        value
    }

    #[agent_tool(name = "optional_param", description = "可选参数")]
    #[allow(unused_variables)]
    fn optional_param(&self, keyword: String, #[param(description = "条数上限")] limit: Option<u32>) -> u32 {
        limit.unwrap_or(10)
    }

    #[agent_tool(name = "async_echo", description = "异步回声")]
    async fn async_echo(&self, message: String) -> String {
        tokio::time::sleep(std::time::Duration::from_millis(5)).await;
        format!("echo: {message}")
    }

    #[agent_tool(name = "tool_error", description = "ToolError 透传")]
    fn tool_error(&self, value: String) -> Result<String, ToolError> {
        if value == "ok" {
            Ok("fine".into())
        } else {
            Err(ToolError::business("SEARCH_FAILED", "音乐库服务暂时不可用", None))
        }
    }

    #[agent_tool(name = "plain_error", description = "普通错误包装")]
    fn plain_error(&self) -> Result<String, String> {
        Err("模拟业务失败".into())
    }
}

// ---------------------------------------------------------------------------
// 自由函数模式
// ---------------------------------------------------------------------------

/// 计算距离
#[agent_tool(name = "distance", description = "两点距离")]
fn distance(x1: f64, y1: f64, x2: f64, y2: f64) -> f64 {
    ((x2 - x1).powi(2) + (y2 - y1).powi(2)).sqrt()
}

#[tokio::test]
async fn tools_metadata_and_schema() {
    let provider = Arc::new(TestController) as Arc<dyn ToolProvider>;
    let tools = provider.tools();
    assert_eq!(tools.len(), 7);

    let search = tools.iter().find(|t| t.name == "search").unwrap();
    assert_eq!(search.description, "搜索歌曲（description 回退 doc 首行）");
    assert_eq!(search.requires_confirmation, false);
    assert_eq!(search.timeout_seconds, 30);
    assert_eq!(search.confirm_timeout_seconds, 120);
    // schema：必填 keyword（string）；返回值类型不进入参数 schema
    assert_eq!(search.input_schema["type"], "object");
    assert_eq!(search.input_schema["required"], serde_json::json!(["keyword"]));
    assert_eq!(search.input_schema["properties"]["keyword"]["type"], "string");
    assert!(search.input_schema.get("definitions").is_none(), "返回值类型不应进入参数 schema");

    // 可选参数：不进 required；#[param(description)] 生效
    let opt = tools.iter().find(|t| t.name == "optional_param").unwrap();
    assert_eq!(opt.input_schema["required"], serde_json::json!(["keyword"]));
    assert_eq!(
        opt.input_schema["properties"]["limit"]["description"],
        "条数上限"
    );

    // Point 复杂参数类型：subschema_for → $ref + definitions
    let point = tools.iter().find(|t| t.name == "point").unwrap();
    assert_eq!(
        point.input_schema["properties"]["value"]["$ref"],
        "#/definitions/Point"
    );
    assert!(point.input_schema["definitions"]["Point"].is_object());

    // 自由函数模式：生成 distance_tool 注册载体
    let free_tool = distance_tool.tools();
    assert_eq!(free_tool.len(), 1);
    assert_eq!(free_tool[0].name, "distance");
    assert_eq!(free_tool[0].input_schema["required"], serde_json::json!(["x1", "y1", "x2", "y2"]));
}

#[tokio::test]
async fn invoke_dispatch() {
    let provider = Arc::new(TestController) as Arc<dyn ToolProvider>;

    // 同步方法 + 返回值序列化
    let r = provider
        .clone()
        .invoke("add", serde_json::json!({ "a": 3, "b": 4 }))
        .await
        .unwrap();
    assert_eq!(r, serde_json::json!(7));

    // 复杂类型（Vec<Song>）
    let r = provider
        .clone()
        .invoke("search", serde_json::json!({ "keyword": "七里香" }))
        .await
        .unwrap();
    assert_eq!(r[0]["title"], "七里香");

    // async 方法
    let r = provider
        .clone()
        .invoke("async_echo", serde_json::json!({ "message": "hi" }))
        .await
        .unwrap();
    assert_eq!(r, serde_json::json!("echo: hi"));

    // Result<T, ToolError>：错误 code 透传
    let e = provider
        .clone()
        .invoke("tool_error", serde_json::json!({ "value": "bad" }))
        .await
        .unwrap_err();
    assert_eq!(e.code, "SEARCH_FAILED");
    assert_eq!(e.message, "音乐库服务暂时不可用");

    // Result<T, String>：包装为 EXECUTION_ERROR
    let e = provider.clone().invoke("plain_error", serde_json::json!({})).await.unwrap_err();
    assert_eq!(e.code, "EXECUTION_ERROR");
    assert_eq!(e.message, "模拟业务失败");

    // 缺必填参数 → INVALID_ARGUMENTS
    let e = provider.clone().invoke("add", serde_json::json!({ "a": 1 })).await.unwrap_err();
    assert_eq!(e.code, "INVALID_ARGUMENTS");

    // 未知 tool
    let e = provider.clone().invoke("nope", serde_json::json!({})).await.unwrap_err();
    assert_eq!(e.code, "TOOL_NOT_FOUND");

    // 自由函数模式
    let free = Arc::new(distance_tool) as Arc<dyn ToolProvider>;
    let r = free
        .invoke("distance", serde_json::json!({ "x1": 0.0, "y1": 0.0, "x2": 3.0, "y2": 4.0 }))
        .await
        .unwrap();
    assert_eq!(r, serde_json::json!(5.0));
}

#[tokio::test]
async fn register_tools_accepts_impl_and_free_fn() {
    let client = AgentQuayClient::builder().app_id("macro-test-app").app_name("T").build().unwrap();

    // impl 块模式按值注册
    client.register_tools(TestController).unwrap();
    assert!(client.list_tools().contains(&"search".to_string()));

    // 自由函数模式：注册载体 + 元组注册
    client.register_tools((distance_tool,)).unwrap();
    assert!(client.list_tools().contains(&"distance".to_string()));

    // 重复注册报错
    let err = client.register_tools(TestController).unwrap_err();
    assert!(matches!(err, agentquay::AgentQuayError::InvalidTool(_)));
}
