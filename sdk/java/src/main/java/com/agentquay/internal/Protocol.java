package com.agentquay.internal;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.util.List;
import java.util.Map;

/**
 * Bridge ↔ 应用的 WebSocket 协议（设计文档 §3.1、附录 A）。
 * 消息外壳：{ "type": "...", "payload": {...} }。
 */
public final class Protocol {

    // 消息类型
    public static final String MSG_REGISTER = "register";
    public static final String MSG_REGISTER_ACK = "register_ack";
    public static final String MSG_REGISTER_ERROR = "register_error";
    public static final String MSG_INVOKE = "invoke";
    public static final String MSG_RESULT = "result";
    public static final String MSG_CONFIRM = "confirm";
    public static final String MSG_CONFIRM_RESULT = "confirm_result";
    public static final String MSG_PING = "ping";
    public static final String MSG_PONG = "pong";
    public static final String MSG_DISCONNECT = "disconnect";
    public static final String MSG_NOTIFICATION = "notification";

    // 断开原因
    public static final String DISCONNECT_NORMAL = "normal";
    public static final String DISCONNECT_REPLACED = "replaced";
    public static final String DISCONNECT_SHUTDOWN = "shutdown";
    public static final String DISCONNECT_MIGRATE = "migrate"; // 让位：应用重连到端口文件指向的更强实例

    public static final ObjectMapper MAPPER = new ObjectMapper()
            .configure(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES, false)
            .setSerializationInclusion(JsonInclude.Include.NON_NULL);

    private static final TypeReference<Map<String, Object>> MAP_TYPE =
            new TypeReference<Map<String, Object>>() {
            };

    private Protocol() {
    }

    /** 注册消息 payload（§5.8：可选 launch 启动命令字段）。 */
    public static ObjectNode registerPayload(String appId, String appName, String version,
                                             String protocolVersion, String authToken,
                                             List<Map<String, Object>> tools,
                                             ObjectNode launch) {
        ObjectNode payload = MAPPER.createObjectNode();
        payload.put("appId", appId);
        payload.put("appName", appName);
        payload.put("version", version);
        payload.put("protocolVersion", protocolVersion);
        payload.put("authToken", authToken == null ? "" : authToken);
        payload.set("tools", MAPPER.valueToTree(tools));
        if (launch != null) {
            payload.set("launch", launch);
        }
        return payload;
    }

    /** 兼容旧签名（无 launch，由调用方判定后使用新签名）。 */
    public static ObjectNode registerPayload(String appId, String appName, String version,
                                             String protocolVersion, String authToken,
                                             List<Map<String, Object>> tools) {
        return registerPayload(appId, appName, version, protocolVersion, authToken, tools, null);
    }

    /** 解析外壳消息，返回 {"type": ..., "payload": {...}}。 */
    public static Map<String, Object> parse(String raw) throws Exception {
        Map<String, Object> env = MAPPER.readValue(raw, MAP_TYPE);
        if (!env.containsKey("type")) {
            throw new IllegalArgumentException("消息缺少 type 字段: " + raw);
        }
        return env;
    }

    /** 序列化外壳消息。 */
    public static String encode(String type, Object payload) throws Exception {
        ObjectNode env = MAPPER.createObjectNode();
        env.put("type", type);
        if (payload != null) {
            env.set("payload", MAPPER.valueToTree(payload));
        }
        return MAPPER.writeValueAsString(env);
    }

    /** 取 payload 子节点（无则返回空 object）。 */
    public static JsonNode payload(Map<String, Object> env) {
        Object payload = env.get("payload");
        return payload == null ? MAPPER.createObjectNode() : MAPPER.valueToTree(payload);
    }
}
