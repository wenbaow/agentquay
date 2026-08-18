using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace AgentQuay.Internal;

/// <summary>
/// Bridge ↔ 应用的 WebSocket 协议（设计文档 §3.1、附录 A）。
/// 消息外壳：{ "type": "...", "payload": {...} }。
/// </summary>
public static class Protocol
{
    // 消息类型（附录 A）
    public const string MsgRegister = "register";         // 应用 → Bridge：注册应用及 Tool 列表
    public const string MsgRegisterAck = "register_ack"; // Bridge → 应用：注册成功（首次注册含分配的 token）
    public const string MsgRegisterError = "register_error"; // Bridge → 应用：注册失败
    public const string MsgInvoke = "invoke";             // Bridge → 应用：调用指定 Tool
    public const string MsgResult = "result";             // 应用 → Bridge：Tool 执行结果
    public const string MsgConfirm = "confirm";           // Bridge → 应用：请求用户确认（危险操作）
    public const string MsgConfirmResult = "confirm_result"; // 应用 → Bridge：用户确认/取消结果
    public const string MsgPing = "ping";                 // 双向心跳
    public const string MsgPong = "pong";                 // 双向心跳响应
    public const string MsgDisconnect = "disconnect";     // 双向优雅断开通知
    public const string MsgNotification = "notification"; // Bridge → 应用：广播通知

    // 断开原因
    public const string DisconnectNormal = "normal";      // 正常关闭
    public const string DisconnectReplaced = "replaced";  // 被同 appId 的新实例替换
    public const string DisconnectShutdown = "shutdown";  // Bridge 即将停止
    public const string DisconnectMigrate = "migrate";    // 让位：应用重连到端口文件指向的更强实例

    /// <summary>
    /// 全局 JSON 选项：camelCase 属性命名、null 字段省略（对齐 Java Protocol.MAPPER 的 NON_NULL）。
    /// </summary>
    public static readonly JsonSerializerOptions Json = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        PropertyNameCaseInsensitive = true,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    /// <summary>注册消息 payload（§5.8：可选 launch 启动命令字段）。</summary>
    public static JsonObject RegisterPayload(string appId, string appName, string version,
                                             string protocolVersion, string? authToken,
                                             JsonArray tools, JsonObject? launch = null)
    {
        var payload = new JsonObject
        {
            ["appId"] = appId,
            ["appName"] = appName,
            ["version"] = version,
            ["protocolVersion"] = protocolVersion,
            ["authToken"] = authToken ?? "",
            ["tools"] = tools,
        };
        if (launch != null)
        {
            payload["launch"] = launch;
        }
        return payload;
    }

    /// <summary>解析外壳消息，返回 {"type": ..., "payload": ...}。</summary>
    public static JsonObject Parse(string raw)
    {
        var env = JsonNode.Parse(raw) as JsonObject;
        if (env == null || !env.ContainsKey("type"))
        {
            throw new ProtocolException("消息缺少 type 字段: " + raw);
        }
        return env;
    }

    /// <summary>序列化外壳消息。</summary>
    public static string Encode(string type, object? payload)
    {
        var env = new JsonObject { ["type"] = type };
        if (payload != null)
        {
            env["payload"] = payload switch
            {
                JsonNode node => node,
                _ => JsonSerializer.SerializeToNode(payload, Json),
            };
        }
        return env.ToJsonString(Json);
    }

    /// <summary>取 payload 子节点（无则返回空 object）。</summary>
    public static JsonObject Payload(JsonObject env)
    {
        if (env["payload"] is JsonObject obj)
        {
            return obj;
        }
        return new JsonObject();
    }
}