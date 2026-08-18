package com.agentquay;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 端到端联调测试：真实 Go Bridge + Java SDK + raw MCP 客户端（设计文档 §3.2）。
 *
 * <p>需要先构建 Bridge 二进制（bridge/dist/ 下），或用环境变量
 * {@code AGENTQUAY_BRIDGE_BIN} 指定路径。
 *
 * <p>覆盖：注册/token、tools/list、正常调用、-32003 参数校验、业务错误透传、
 * 确认流程（确认/取消 -32005）、同 appId 替换、断线重连携带 token。
 */
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class AgentQuayE2ETest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String APP_ID = "java-e2e-app";

    private static Process bridge;
    private static int port;
    private static MCPClient rpc;

    // 被测应用（SDK 侧）
    private static AgentQuayClient app;
    private static CompletableFuture<Void> appTask;
    private static boolean confirmAnswer = true;

    static class Controller {
        @AgentTool("echo")
        public Map<String, Object> echo(String message) {
            return Map.of("received", message);
        }

        @AgentTool("add")
        public int add(int a, int b) {
            return a + b;
        }

        @AgentTool(value = "confirm_op", requiresConfirmation = true)
        public Map<String, Object> confirmOp(String value) {
            return Map.of("confirmed_value", value);
        }

        @AgentTool("fail")
        public Map<String, Object> fail() {
            throw new IllegalArgumentException("模拟业务失败");
        }
    }

    @BeforeAll
    static void startBridge() throws Exception {
        // 1. 定位并启动 Bridge（前台 serve）
        Path binary = findBridgeBinary();
        Path portFile = Paths.get(System.getProperty("user.home"), ".agentquay", "port");
        Files.deleteIfExists(portFile);

        ProcessBuilder pb = new ProcessBuilder(binary.toString(), "serve");
        pb.redirectOutput(ProcessBuilder.Redirect.DISCARD);
        pb.redirectError(ProcessBuilder.Redirect.DISCARD);
        pb.environment().put("AGENTQUAY_LOG_LEVEL", "debug");
        bridge = pb.start();

        // 2. 等待端口文件 + 健康检查
        long deadline = System.currentTimeMillis() + 15_000;
        while (System.currentTimeMillis() < deadline) {
            int p = BridgeSpawner.readPortFile();
            if (p > 0 && BridgeSpawner.probe("127.0.0.1", p, 500)) {
                port = p;
                break;
            }
            Thread.sleep(200);
        }
        assertTrue(port > 0, "Bridge 未在 15s 内就绪");

        rpc = new MCPClient(port);
        JsonNode init = rpc.post("initialize", Map.of(
                "protocolVersion", "2025-06-18",
                "capabilities", Map.of(),
                "clientInfo", Map.of("name", "java-e2e", "version", "1.0")));
        assertTrue(init.has("result"), "initialize 失败: " + init);
        assertNotNull(rpc.sessionId, "未建立 MCP 会话");
    }

    @AfterAll
    static void stopBridge() {
        if (app != null) {
            app.close();
        }
        if (bridge != null) {
            bridge.destroyForcibly();
        }
    }

    // ------------------------------------------------------------------
    // 测试
    // ------------------------------------------------------------------

    @Test
    @Order(1)
    void sdkRegistersAndToolsList() throws Exception {
        app = AgentQuayClient.builder()
                .appId(APP_ID)
                .appName("Java E2E")
                .port(0)
                .autoSpawnBridge(false) // 测试脚本管理 Bridge
                .heartbeatInterval(30)
                .maxRetryInterval(2)
                .confirmHandler((message, arguments, timeout) -> confirmAnswer)
                .build();
        app.registerTools(Controller.class);
        appTask = app.connectAsync();

        assertTrue(waitAppOnline(10), "应用未上线");

        JsonNode tools = rpc.post("tools/list", Map.of()).path("result").path("tools");
        List<String> names = new ArrayList<>();
        for (JsonNode t : tools) {
            names.add(t.path("name").asText());
        }
        assertTrue(names.contains("java-e2e-app_echo"), "缺少合成名 tool: " + names);
        assertTrue(names.contains("java-e2e-app_confirm_op"), "缺少 confirm_op: " + names);

        JsonNode echoTool = null;
        for (JsonNode t : tools) {
            if ("java-e2e-app_echo".equals(t.path("name").asText())) {
                echoTool = t;
            }
        }
        assertNotNull(echoTool);
        assertTrue(echoTool.path("description").asText().startsWith("[Java E2E]"), "描述缺应用名前缀");
        assertEquals(List.of("message"), jsonList(echoTool.path("inputSchema").path("required")));
    }

    @Test
    @Order(2)
    void toolsCallSuccess() throws Exception {
        JsonNode resp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_echo", "arguments", Map.of("message", "你好，Agent")));
        assertFalse(resp.path("result").path("isError").asBoolean(), "调用失败: " + resp);
        String text = resp.path("result").path("content").get(0).path("text").asText();
        assertEquals("{\"received\":\"你好，Agent\"}", text);

        JsonNode addResp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_add", "arguments", Map.of("a", 3, "b", 4)));
        assertEquals("7", addResp.path("result").path("content").get(0).path("text").asText());
    }

    @Test
    @Order(3)
    void invalidArgsRejected() throws Exception {
        // 缺必填参数 → -32003
        JsonNode resp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_echo", "arguments", Map.of()));
        assertEquals(-32003, errorCode(resp), "缺参应返回 -32003: " + resp);

        // 类型错误 → -32003
        resp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_echo", "arguments", Map.of("message", 123)));
        assertEquals(-32003, errorCode(resp), "类型错误应返回 -32003: " + resp);
    }

    @Test
    @Order(4)
    void businessErrorPassthrough() throws Exception {
        JsonNode resp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_fail", "arguments", Map.of()));
        assertTrue(resp.path("result").path("isError").asBoolean(), "业务错误应 isError: " + resp);
        String text = resp.path("result").path("content").get(0).path("text").asText();
        assertTrue(text.contains("EXECUTION_ERROR") || text.contains("模拟业务失败"), "错误信息未透传: " + text);
    }

    @Test
    @Order(5)
    void confirmationFlow() throws Exception {
        // 确认 → 成功
        confirmAnswer = true;
        JsonNode resp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_confirm_op", "arguments", Map.of("value", "v1")));
        assertFalse(resp.path("result").path("isError").asBoolean(), "确认后应成功: " + resp);

        // 取消 → -32005
        confirmAnswer = false;
        resp = rpc.post("tools/call", Map.of(
                "name", "java-e2e-app_confirm_op", "arguments", Map.of("value", "v2")));
        assertEquals(-32005, errorCode(resp), "取消应返回 -32005: " + resp);
    }

    @Test
    @Order(6)
    void replacementStopsOldClient() throws Exception {
        // 同 appId 新实例注册 → 旧连接被替换，connectAsync 以 ReplacedException 结束
        AgentQuayClient second = AgentQuayClient.builder()
                .appId(APP_ID)
                .appName("Java E2E")
                .port(port)
                .autoSpawnBridge(false)
                .confirmHandler((m, a, t) -> true)
                .build();
        second.registerTools(Controller.class);
        CompletableFuture<Void> secondTask = second.connectAsync();

        try {
            appTask.get(10, TimeUnit.SECONDS);
            assertTrue(false, "旧客户端应因替换而退出");
        } catch (java.util.concurrent.ExecutionException e) {
            assertTrue(e.getCause() instanceof AgentQuayException.ReplacedException,
                    "应为 ReplacedException: " + e.getCause());
        }
        app.close();
        app = second; // 新实例接管
        appTask = secondTask;
        assertTrue(waitAppOnline(8), "新实例应在线");
    }

    @Test
    @Order(7)
    void reconnectWithToken() throws Exception {
        // 关闭当前实例 → 新客户端用持久化 token 重新注册成功（不触发 AUTH_FAILED）
        app.close();
        assertTrue(waitAppOffline(8), "应用应下线");

        AgentQuayClient fresh = AgentQuayClient.builder()
                .appId(APP_ID)
                .appName("Java E2E")
                .port(port)
                .autoSpawnBridge(false)
                .confirmHandler((m, a, t) -> true)
                .build();
        fresh.registerTools(Controller.class);
        appTask = fresh.connectAsync();
        assertTrue(waitAppOnline(8), "重连（携带 token）应成功");
        app = fresh;
    }

    // ------------------------------------------------------------------
    // 工具
    // ------------------------------------------------------------------

    private static Path findBridgeBinary() throws Exception {
        String env = System.getenv("AGENTQUAY_BRIDGE_BIN");
        if (env != null && !env.isEmpty()) {
            return Paths.get(env);
        }
        // 相对仓库：sdk/java → ../../bridge/dist
        Path dist = Paths.get(System.getProperty("user.dir"))
                .getParent().getParent().resolve("bridge").resolve("dist");
        if (Files.isDirectory(dist)) {
            String os = System.getProperty("os.name", "").toLowerCase();
            String prefix = os.contains("win") ? "agentquay-windows-" : "agentquay-";
            try (var stream = Files.list(dist)) {
                for (java.util.Iterator<Path> it = stream.iterator(); it.hasNext(); ) {
                    Path p = it.next();
                    String name = p.getFileName().toString();
                    if (name.startsWith(prefix)) {
                        return p;
                    }
                }
            }
        }
        throw new IllegalStateException("未找到 Bridge 二进制。请先构建 bridge/dist 或设置 AGENTQUAY_BRIDGE_BIN");
    }

    private static boolean waitAppOnline(int timeoutSeconds) throws Exception {
        return waitAppState(timeoutSeconds, true);
    }

    private static boolean waitAppOffline(int timeoutSeconds) throws Exception {
        return waitAppState(timeoutSeconds, false);
    }

    private static boolean waitAppState(int timeoutSeconds, boolean online) throws Exception {
        long deadline = System.currentTimeMillis() + timeoutSeconds * 1000L;
        while (System.currentTimeMillis() < deadline) {
            HttpRequest req = HttpRequest.newBuilder()
                    .uri(URI.create("http://127.0.0.1:" + port + "/admin/status"))
                    .timeout(Duration.ofSeconds(2))
                    .GET().build();
            String body = HttpClient.newHttpClient().send(req, HttpResponse.BodyHandlers.ofString()).body();
            JsonNode apps = MAPPER.readTree(body).path("apps");
            boolean found = false;
            for (JsonNode a : apps) {
                if (APP_ID.equals(a.path("appId").asText())) {
                    found = true;
                    break;
                }
            }
            if (found == online) {
                return true;
            }
            Thread.sleep(200);
        }
        return false;
    }

    private static int errorCode(JsonNode resp) {
        JsonNode content = resp.path("result").path("content");
        if (content.isArray() && content.size() > 0) {
            String text = content.get(0).path("text").asText("");
            try {
                return MAPPER.readTree(text).path("code").asInt(0);
            } catch (Exception e) {
                return 0;
            }
        }
        return 0;
    }

    private static List<String> jsonList(JsonNode node) {
        List<String> out = new ArrayList<>();
        for (JsonNode n : node) {
            out.add(n.asText());
        }
        return out;
    }

    // ------------------------------------------------------------------
    // 最小 MCP Streamable HTTP 客户端
    // ------------------------------------------------------------------

    static class MCPClient {
        private final String url;
        private final HttpClient http = HttpClient.newHttpClient();
        String sessionId;
        private int id;

        MCPClient(int port) {
            this.url = "http://127.0.0.1:" + port + "/mcp";
        }

        JsonNode post(String method, Map<String, Object> params) throws Exception {
            id++;
            Map<String, Object> body = new java.util.LinkedHashMap<>();
            body.put("jsonrpc", "2.0");
            body.put("id", id);
            body.put("method", method);
            if (params != null) {
                body.put("params", params);
            }
            HttpRequest.Builder req = HttpRequest.newBuilder()
                    .uri(URI.create(url))
                    .timeout(Duration.ofSeconds(90))
                    .header("Content-Type", "application/json")
                    .header("Accept", "application/json, text/event-stream")
                    .POST(HttpRequest.BodyPublishers.ofString(MAPPER.writeValueAsString(body)));
            if (sessionId != null) {
                req.header("Mcp-Session-Id", sessionId);
            }
            HttpResponse<String> resp = http.send(req.build(), HttpResponse.BodyHandlers.ofString());
            String sid = resp.headers().firstValue("Mcp-Session-Id").orElse(null);
            if (sid != null) {
                sessionId = sid;
            }
            String ctype = resp.headers().firstValue("Content-Type").orElse("");
            String data = resp.body();
            if (ctype.contains("text/event-stream")) {
                data = lastSseData(data);
            }
            return MAPPER.readTree(data);
        }

        /** 从 SSE 流提取最后一个 data 负载（即目标 JSON-RPC 响应）。 */
        private static String lastSseData(String body) {
            String last = null;
            for (String event : body.split("\n\n")) {
                String payload = null;
                for (String line : event.split("\n")) {
                    if (line.startsWith("data:")) {
                        payload = line.substring(5).trim();
                    }
                }
                if (payload != null) {
                    last = payload;
                }
            }
            if (last == null) {
                throw new IllegalStateException("SSE 流中未找到 data: " + body);
            }
            return last;
        }
    }
}
