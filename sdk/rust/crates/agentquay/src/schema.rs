//! 过程宏生成代码的 schema 运行时辅助（内部 API，经 `agentquay::__private` 暴露）。

use schemars::Schema;
use schemars::SchemaGenerator;
use serde_json::{Map, Value};

/// 创建一个 draft-07 的 SchemaGenerator（与 Bridge 的 JSON Schema 校验器
/// santhosh-tekuri/jsonschema v5 的默认草稿一致；`definitions` 关键字兼容）。
pub fn schema_generator() -> SchemaGenerator {
    schemars::generate::SchemaSettings::draft07().into_generator()
}

/// 单个参数的 schema 输入（`#[agent_tool]` 生成代码构造）。
pub struct Param<'a> {
    pub name: &'a str,
    pub schema: Schema,
    pub required: bool,
    pub description: Option<&'a str>,
}

/// 构造参数（`#[agent_tool]` 生成代码调用）。
pub fn param<'a>(
    name: &'a str,
    schema: Schema,
    required: bool,
    description: Option<&'a str>,
) -> Param<'a> {
    Param { name, schema, required, description }
}

/// 为类型生成 subschema（复杂类型进 definitions 并返回 `$ref`，与嵌套类型去重一致）。
pub fn subschema_for<T: schemars::JsonSchema + ?Sized>(gen: &mut SchemaGenerator) -> Schema {
    gen.subschema_for::<T>()
}

/// 将一组参数 schema 合并为 inputSchema：
/// `{ "type": "object", "properties": {...}, "required": [...], "definitions": {...} }`。
///
/// `definitions` 从同一个 SchemaGenerator 汇总（复杂类型以 `$ref` 引用并去重）。
pub fn param_schema(gen: &mut SchemaGenerator, params: &[Param]) -> Value {
    let mut properties = Map::new();
    let mut required = Vec::new();
    for p in params {
        let mut schema = schema_to_value(p.schema.clone());
        if let Some(desc) = p.description {
            schema = add_description(schema, desc);
        }
        properties.insert(p.name.to_owned(), schema);
        if p.required {
            required.push(p.name.to_owned());
        }
    }
    let mut root = serde_json::json!({ "type": "object", "properties": properties });
    if !required.is_empty() {
        root["required"] = Value::Array(required.into_iter().map(Value::String).collect());
    }
    let definitions = gen.definitions_mut();
    if !definitions.is_empty() {
        root["definitions"] = Value::Object(definitions.clone());
    }
    root
}

fn schema_to_value(schema: Schema) -> Value {
    serde_json::to_value(schema).expect("JSON Schema 序列化失败（schemars 内部错误）")
}

/// 为 schema 追加 description（非对象 schema（如 bool）用 allOf 包裹）。
fn add_description(schema: Value, description: &str) -> Value {
    if schema.is_object() && schema.get("description").is_none() {
        let mut out = schema;
        out["description"] = Value::String(description.to_owned());
        out
    } else {
        serde_json::json!({ "allOf": [schema], "description": description })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use schemars::JsonSchema;

    #[test]
    fn empty_params_schema() {
        let mut gen = schema_generator();
        let schema = param_schema(&mut gen, &[]);
        assert_eq!(schema["type"], "object");
        assert!(schema.get("required").is_none());
        assert!(schema.get("definitions").is_none());
    }

    #[test]
    fn params_schema_with_required_and_option() {
        let mut gen = schema_generator();
        let keyword = <String as JsonSchema>::json_schema(&mut gen);
        let limit = <Option<u32> as JsonSchema>::json_schema(&mut gen);
        let schema = param_schema(
            &mut gen,
            &[
                param("keyword", keyword, true, Some("搜索关键词")),
                param("limit", limit, false, None),
            ],
        );
        assert_eq!(schema["type"], "object");
        assert_eq!(schema["properties"]["keyword"]["type"], "string");
        assert_eq!(schema["properties"]["keyword"]["description"], "搜索关键词");
        assert_eq!(schema["required"], serde_json::json!(["keyword"]));
    }

    #[test]
    fn complex_type_definitions_merged() {
        let mut gen = schema_generator();
        #[derive(JsonSchema)]
        #[allow(dead_code)]
        struct Point {
            x: f64,
            y: f64,
        }
        let schema = <Vec<Point> as JsonSchema>::json_schema(&mut gen);
        let root = param_schema(&mut gen, &[param("points", schema, true, None)]);
        // 复杂类型以 $ref 引用，definitions 汇总在同一对象内
        assert!(root["properties"]["points"]["items"]["$ref"].is_string());
        assert!(root["definitions"]["Point"].is_object());
    }
}