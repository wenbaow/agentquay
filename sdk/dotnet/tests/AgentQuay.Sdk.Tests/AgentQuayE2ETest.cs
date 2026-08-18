using System.Diagnostics;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using AgentQuay;
using Xunit;

namespace AgentQuay.Tests;

/// <summary>
/// 端到端联调测试：真实 Go Bridge + C# SDK + raw MCP 客户端（设计文档 §3.2）。
///
/// <para>需要先构建 Bridge 二进制（bridge/dist/ 下），或用环境变量
/// <see cref="AgentQuayE2ETest.BridgeFixture.BridgeBinaryEnv"/> 指定路径。</para>
///
/// <para>覆盖：注册/token、tools/list、正常调用、-32003 参数校验、业务错误透传、
/// 确认流程（确认/取消 -32005）、同 appId 替换、断线重连携带 token。</para>
/// </summary>
public class AgentQuayE2ETest : IClassFixture<AgentQuayE2ETest.BridgeFixture>
{
    private const string AppId = "dotnet-e2e-app";
    private const string AppName = "CSharp E2E";

    private readonly BridgeFixture _fixture;

    public AgentQuayE2ETest(BridgeFixture fixture) => _fixture = fixture;

    // 被测应用（SDK 侧）
    private static bool _confirmAnswer = true;

    public sealed class Song
    {
        public string? Id { get; set; }
        public string? Title { get; set; }
        public string? Artist { get; set; }
    }

    public sealed class Controller
    {
        [AgentTool("echo")]
        public Dictionary<string, string> Echo([AgentParam(Description = "消息")] string message)
            => new() { ["received"] = message };

        [AgentTool("add")]
        public int Add(int a, int b) => a + b;

        [AgentTool("confirm_op", RequiresConfirmation = true)]
        public Dictionary<string, string> ConfirmOp(string value)
            => new() { ["confirmed_value"] = value };

        [AgentTool("fail")]
        public string Fail() => throw new InvalidOperationException("模拟业务失败");

        [AgentTool("async_echo")]
        public async Task<Dictionary<string, string>> AsyncEcho(string message)
        {
            await Task.Yield();
            return new Dictionary<string, string> { ["received"] = message };
        }
    }

    // ------------------------------------------------------------------
    // 端到端全流程（单个测试方法保证步骤顺序；共享 BridgeFixture）
    // ------------------------------------------------------------------

    [Fact]
    public async Task E2e_FullFlow()
    {
        await _fixture.InitializeAsync();
        var rpc = _fixture.Rpc!;
        int port = _fixture.Port;

        // ---- Step1: SDK 注册 + tools/list ---------------------------------
        AgentQuayClient app = await AgentQuayClient.ConnectAsync(
            appId: AppId,
            appName: AppName,
            port: 0,                     // 从 ~/.agentquay/port 自动读取
            autoSpawnBridge: false,      // 测试脚本管理 Bridge
            heartbeatIntervalSeconds: 30,
            maxRetryIntervalSeconds: 2,
            confirmationHandler: (m, a, t) => _confirmAnswer);
        app.RegisterTools<Controller>();
        var appTask = app.StartAsync();

        Assert.True(await WaitAppState(port, true, 10), "应用未上线");

        var tools = rpc.Post("tools/list", new JsonObject())["result"]!["tools"]!.AsArray();
        var names = tools.Select(t => t!["name"]!.GetValue<string>()).ToList();
        Assert.Contains($"{AppId}_echo", names);
        Assert.Contains($"{AppId}_confirm_op", names);
        Assert.Contains($"{AppId}_async_echo", names);

        var echoTool = tools.First(t => t!["name"]!.GetValue<string>() == $"{AppId}_echo")!;
        Assert.StartsWith("[CSharp E2E]", echoTool["description"]!.GetValue<string>(), StringComparison.Ordinal);
        var required = echoTool["inputSchema"]!["required"]!.AsArray()
            .Select(n => n!.GetValue<string>()).ToList();
        Assert.Contains("message", required);

        // ---- Step2: 正常调用 ----------------------------------------------
        var resp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_echo", ["arguments"] = new JsonObject { ["message"] = "你好，Agent" } });
        Assert.True(resp["result"] is not null, "tools/call 响应缺 result: " + resp.ToJsonString());
        Assert.False(IsError(resp), "调用失败: " + resp);
        Assert.Equal("{\"received\":\"你好，Agent\"}", ContentText(resp));

        var addResp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_add", ["arguments"] = new JsonObject { ["a"] = 3, ["b"] = 4 } });
        Assert.Equal("7", ContentText(addResp));

        // async unwrap
        var asyncResp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_async_echo", ["arguments"] = new JsonObject { ["message"] = "async" } });
        Assert.False(IsError(asyncResp), "async 调用失败: " + asyncResp);
        Assert.Contains("async", ContentText(asyncResp));

        // ---- Step3: 参数校验 -32003 ----------------------------------------
        resp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_echo", ["arguments"] = new JsonObject() });
        Assert.Equal(-32003, ErrorCode(resp));
        resp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_echo", ["arguments"] = new JsonObject { ["message"] = 123 } });
        Assert.Equal(-32003, ErrorCode(resp));

        // ---- Step4: 业务错误透传 -------------------------------------------
        resp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_fail", ["arguments"] = new JsonObject() });
        Assert.True(IsError(resp), "业务错误应 isError: " + resp);
        string text = ContentText(resp);
        Assert.True(text.Contains("EXECUTION_ERROR", StringComparison.Ordinal) || text.Contains("模拟业务失败", StringComparison.Ordinal),
            "错误信息未透传: " + text);

        // ---- Step5: 确认流程 ------------------------------------------------
        _confirmAnswer = true;
        resp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_confirm_op", ["arguments"] = new JsonObject { ["value"] = "v1" } });
        Assert.False(IsError(resp), "确认后应成功: " + resp);

        _confirmAnswer = false;
        resp = rpc.Post("tools/call", new JsonObject { ["name"] = $"{AppId}_confirm_op", ["arguments"] = new JsonObject { ["value"] = "v2" } });
        Assert.Equal(-32005, ErrorCode(resp));

        // ---- Step6: 同 appId 替换 → 旧客户端 ReplacedException --------------
        AgentQuayClient second = await AgentQuayClient.ConnectAsync(
            appId: AppId, appName: AppName, port: port, autoSpawnBridge: false,
            confirmationHandler: (m, a, t) => true);
        second.RegisterTools<Controller>();
        var secondTask = second.StartAsync();

        var winner = await Task.WhenAny(appTask, Task.Delay(15_000));
        Assert.Same(appTask, winner);
        var replacedEx = await Assert.ThrowsAsync<ReplacedException>(() => appTask);
        Assert.NotNull(replacedEx);
        await app.DisposeAsync();
        app = second;
        appTask = secondTask;
        Assert.True(await WaitAppState(port, true, 8), "新实例应在线");

        // ---- Step7: 断线重连携带 token --------------------------------------
        await app.DisposeAsync();
        Assert.True(await WaitAppState(port, false, 8), "应用应下线");

        AgentQuayClient fresh = await AgentQuayClient.ConnectAsync(
            appId: AppId, appName: AppName, port: port, autoSpawnBridge: false,
            confirmationHandler: (m, a, t) => true);
        fresh.RegisterTools<Controller>();
        appTask = fresh.StartAsync();
        Assert.True(await WaitAppState(port, true, 8), "重连（携带 token）应成功");
        await fresh.DisposeAsync();
    }

    // ------------------------------------------------------------------
    // 工具
    // ------------------------------------------------------------------

    private static async Task<bool> WaitAppState(int port, bool online, int timeoutSeconds)
    {
        long deadline = Environment.TickCount64 + timeoutSeconds * 1000L;
        while (Environment.TickCount64 < deadline)
        {
            try
            {
                using var http = new HttpClient { Timeout = TimeSpan.FromSeconds(2) };
                string body = await http.GetStringAsync($"http://127.0.0.1:{port}/admin/status");
                using var doc = JsonDocument.Parse(body);
                bool found = doc.RootElement.TryGetProperty("apps", out var apps)
                             && apps.EnumerateArray().Any(a =>
                                 a.TryGetProperty("appId", out var id) && id.GetString() == AppId);
                if (found == online)
                {
                    return true;
                }
            }
            catch (Exception)
            {
                // Bridge 重启中 / 尚未就绪
            }
            await Task.Delay(200);
        }
        return false;
    }

    private static bool IsError(JsonNode resp) => resp["result"]?["isError"]?.GetValue<bool>() ?? false;

    private static string ContentText(JsonNode resp)
        => resp["result"]!["content"]![0]!["text"]!.GetValue<string>();

    private static int ErrorCode(JsonNode resp)
    {
        var content = resp["result"]?["content"];
        if (content is JsonArray { Count: > 0 })
        {
            string text = content[0]!["text"]!.GetValue<string>();
            try
            {
                var node = JsonNode.Parse(text);
                return node?["code"]?.GetValue<int>() ?? 0;
            }
            catch (Exception)
            {
                return 0;
            }
        }
        return 0;
    }

    // ------------------------------------------------------------------
    // Bridge fixture：启动真实 Go Bridge 并等待就绪
    // ------------------------------------------------------------------

    public sealed class BridgeFixture : IDisposable
    {
        public const string BridgeBinaryEnv = "AGENTQUAY_BRIDGE_BIN";

        private static readonly string HomeDir =
            Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);

        public int Port { get; private set; }
        public MCPClient? Rpc { get; private set; }
        public Process? Bridge { get; private set; }

        /// <summary>xUnit 的 IClassFixture：同一测试类实例复用本 fixture；首次使用时初始化。</summary>
        public Task InitializeAsync()
        {
            if (Rpc == null)
            {
                StartBridge();
            }
            return Task.CompletedTask;
        }

        private void StartBridge()
        {
            string binary = FindBridgeBinary();
            string portFile = Path.Combine(HomeDir, ".agentquay", "port");
            try { File.Delete(portFile); } catch { }

            var psi = new ProcessStartInfo
            {
                FileName = binary,
                UseShellExecute = false,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
            };
            psi.ArgumentList.Add("serve");
            psi.Environment["AGENTQUAY_LOG_LEVEL"] = "debug";
            Bridge = Process.Start(psi) ?? throw new InvalidOperationException("无法启动 Bridge: " + binary);

            long deadline = Environment.TickCount64 + 15_000;
            while (Environment.TickCount64 < deadline)
            {
                int p = BridgeSpawner.ReadPortFile();
                if (p > 0 && BridgeSpawner.Probe("127.0.0.1", p, 500))
                {
                    Port = p;
                    break;
                }
                Thread.Sleep(200);
            }
            Assert.True(Port > 0, "Bridge 未在 15s 内就绪");

            Rpc = new MCPClient(Port);
            var init = Rpc.Post("initialize", new JsonObject
            {
                ["protocolVersion"] = "2025-06-18",
                ["capabilities"] = new JsonObject(),
                ["clientInfo"] = new JsonObject { ["name"] = "dotnet-e2e", ["version"] = "1.0" },
            });
            Assert.True(init["result"] != null, "initialize 失败: " + init);
            Assert.NotNull(Rpc.SessionId);
        }

        public void Dispose()
        {
            if (Bridge != null)
            {
                try { Bridge.Kill(entireProcessTree: true); } catch { }
                Bridge.Dispose();
                Bridge = null;
            }
        }

        private static string FindBridgeBinary()
        {
            string? env = Environment.GetEnvironmentVariable(BridgeBinaryEnv);
            if (!string.IsNullOrEmpty(env) && File.Exists(env))
            {
                return env;
            }
            // 仓库开发布局：从输出目录向上回溯到 bridge/dist/agentquay-<os>-<arch>
            var cursor = new DirectoryInfo(AppContext.BaseDirectory);
            for (int i = 0; i < 8 && cursor != null; i++)
            {
                string dist = Path.Combine(cursor.FullName, "bridge", "dist");
                if (Directory.Exists(dist))
                {
                    string prefix = OperatingSystem.IsWindows() ? "agentquay-windows-" : "agentquay-";
                    string? found = Directory.EnumerateFiles(dist)
                        .FirstOrDefault(f => Path.GetFileName(f).StartsWith(prefix, StringComparison.Ordinal));
                    if (found != null)
                    {
                        return found;
                    }
                }
                cursor = cursor.Parent;
            }
            throw new InvalidOperationException(
                "未找到 Bridge 二进制。请先构建 bridge/dist 或设置 " + BridgeBinaryEnv);
        }
    }

    // ------------------------------------------------------------------
    // 最小 MCP Streamable HTTP 客户端
    // ------------------------------------------------------------------

    public sealed class MCPClient
    {
        private static readonly JsonSerializerOptions Json = new() { PropertyNamingPolicy = JsonNamingPolicy.CamelCase };

        private readonly string _url;
        private readonly HttpClient _http = new() { Timeout = TimeSpan.FromSeconds(90) };
        private int _id;

        public string? SessionId { get; private set; }

        public MCPClient(int port) => _url = $"http://127.0.0.1:{port}/mcp";

        public JsonNode Post(string method, JsonObject? parameters)
        {
            _id++;
            var body = new JsonObject
            {
                ["jsonrpc"] = "2.0",
                ["id"] = _id,
                ["method"] = method,
            };
            if (parameters != null)
            {
                body["params"] = parameters;
            }

            var request = new HttpRequestMessage(HttpMethod.Post, _url)
            {
                Content = new StringContent(body.ToJsonString(), Encoding.UTF8, "application/json"),
            };
            request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
            request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("text/event-stream"));
            if (SessionId != null)
            {
                request.Headers.Add("Mcp-Session-Id", SessionId);
            }

            using var response = _http.Send(request);
            if (response.Headers.TryGetValues("Mcp-Session-Id", out var values))
            {
                SessionId = values.FirstOrDefault();
            }
            string data = response.Content.ReadAsStringAsync().GetAwaiter().GetResult();
            if (response.Content.Headers.ContentType?.MediaType?.Contains("text/event-stream", StringComparison.OrdinalIgnoreCase) == true)
            {
                data = LastSseData(data);
            }
            return JsonNode.Parse(data) ?? throw new InvalidOperationException("MCP 响应为空: " + data);
        }

        /// <summary>从 SSE 流提取最后一个 data 负载（即目标 JSON-RPC 响应）。</summary>
        private static string LastSseData(string body)
        {
            string? last = null;
            foreach (string eventBlock in body.Split("\n\n"))
            {
                string? payload = null;
                foreach (string line in eventBlock.Split('\n'))
                {
                    if (line.StartsWith("data:", StringComparison.Ordinal))
                    {
                        payload = line["data:".Length..].Trim();
                    }
                }
                if (payload != null)
                {
                    last = payload;
                }
            }
            if (last == null)
            {
                throw new InvalidOperationException("SSE 流中未找到 data: " + body);
            }
            return last;
        }
    }
}