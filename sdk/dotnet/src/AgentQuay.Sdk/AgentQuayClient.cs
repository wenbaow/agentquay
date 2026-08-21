using System.Collections.Concurrent;
using System.Diagnostics;
using System.Diagnostics.CodeAnalysis;
using System.Net.WebSockets;
using System.Reflection;
using System.Runtime.ExceptionServices;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading.Channels;
using AgentQuay.Internal;

namespace AgentQuay;

/// <summary>
/// 连接 AgentQuay Bridge 的应用侧客户端（设计文档 §4.2）。
///
/// <para>职责：注册 Tool、心跳、调用分发、确认流程、指数退避重连、token 持久化、
/// auto-spawn 内嵌 Bridge。</para>
///
/// <example>
/// <code>
/// var client = await AgentQuayClient.ConnectAsync(
///     appId: "music-app",
///     appName: "Music Player",
///     host: "localhost",
///     port: 0,                    // 从 ~/.agentquay/port 自动读取实际端口
///     autoSpawnBridge: true);     // 未检测到服务时自动拉起内嵌 Bridge
/// client.RegisterTools&lt;MusicController&gt;();
/// await client.StartAsync();      // 保持连接，监听调用
/// </code>
/// </example>
/// </summary>
public sealed class AgentQuayClient : IAsyncDisposable
{
    /// <summary>队列哨兵：连接关闭/出错。</summary>
    private const string ClosedSentinel = "\u0000__CLOSED__";

    private const string DefaultVersion = "1.0.0";
    private const string DefaultProtocolVersion = "1.0";

    private readonly string _appId;
    private readonly string _appName;
    private readonly string _host;
    private volatile int _port;
    private readonly bool _autoSpawnBridge;
    private readonly string _version;
    private readonly string _protocolVersion;
    private readonly int _heartbeatIntervalSeconds;
    private readonly int _maxRetryIntervalSeconds;
    private readonly ConfirmationHandler _confirmationHandler;
    /// <summary>工具调用钩子：在业务方法执行前触发（允许 UI 层拦截并响应）。</summary>
    private readonly ToolCallHandler? _toolCallHandler;
    /// <summary>上报给 Bridge 的启动命令（§5.8，离线自动拉起用）。</summary>
    private readonly LaunchInfo? _launchInfo;

    private readonly List<ToolMetadata> _tools = new();
    private readonly JsonSchemaGenerator _schemaGen = new();
    private readonly TokenStore _tokenStore;
    private readonly BridgeSpawner _spawner;
    private readonly Channel<string> _messages = Channel.CreateUnbounded<string>();
    private readonly SemaphoreSlim _sendLock = new(1, 1);
    private readonly byte[] _receiveBuffer = new byte[64 * 1024];

    private volatile ClientWebSocket? _ws;
    private volatile string? _token;
    private volatile bool _stopRequested;

    private AgentQuayClient(string appId, string appName, string host, int port, bool autoSpawnBridge,
                            string version, string protocolVersion,
                            int heartbeatIntervalSeconds, int maxRetryIntervalSeconds,
                            ConfirmationHandler? confirmationHandler, LaunchInfo? launchInfo,
                            ToolCallHandler? toolCallHandler = null)
    {
        _appId = appId;
        _appName = appName;
        _host = host;
        _port = port;
        _autoSpawnBridge = autoSpawnBridge;
        _version = version;
        _protocolVersion = protocolVersion;
        _heartbeatIntervalSeconds = heartbeatIntervalSeconds;
        _maxRetryIntervalSeconds = maxRetryIntervalSeconds;
        _confirmationHandler = confirmationHandler ?? ConfirmDialog.Ask;
        _toolCallHandler = toolCallHandler;
        _launchInfo = launchInfo;
        _tokenStore = new TokenStore(appId);
        _token = _tokenStore.Get();
        _spawner = new BridgeSpawner(host);
    }

    /// <summary>
    /// 准备客户端并确保本地 Bridge 可用（自动拉起内嵌 Bridge、解析实际端口）。
    /// </summary>
    /// <param name="appId">应用 ID（[a-z0-9-]{1,48}，禁止 _ 和 .；如 music-app）。</param>
    /// <param name="appName">应用显示名。</param>
    /// <param name="host">Bridge 地址（默认 127.0.0.1）。</param>
    /// <param name="port">Bridge 端口（0 = 从 ~/.agentquay/port 自动读取）。</param>
    /// <param name="autoSpawnBridge">未检测到服务时自动拉起内嵌 Bridge（默认 true）。</param>
    /// <param name="version">应用版本号（随注册上报，默认 1.0.0）。</param>
    /// <param name="protocolVersion">协议版本（默认 "1.0"）。</param>
    /// <param name="heartbeatIntervalSeconds">心跳间隔秒数（默认 30）。</param>
    /// <param name="maxRetryIntervalSeconds">重连退避上限秒数（默认 30）。</param>
    /// <param name="confirmationHandler">自定义确认回调（默认弹确认框，无图形环境回退控制台）。</param>
    /// <param name="launchInfo">启动命令（§5.8）：显式指定随 register 上报供 Bridge 离线自动拉起；
    ///     缺省自动探测（<see cref="LaunchInfo.Detect"/>），null + autoReportLaunch=false 则不上报。</param>
    /// <param name="autoReportLaunch">是否在注册时自动上报启动命令（默认 true）。</param>
    /// <param name="toolCallHandler">工具调用钩子：在业务方法执行前触发（允许 UI 层拦截并响应）。</param>
    /// <param name="cancellationToken">取消令牌。</param>
    public static async Task<AgentQuayClient> ConnectAsync(
        string appId,
        string appName,
        string host = "127.0.0.1",
        int port = 0,
        bool autoSpawnBridge = true,
        string? version = null,
        string? protocolVersion = null,
        int heartbeatIntervalSeconds = 30,
        int maxRetryIntervalSeconds = 30,
        ConfirmationHandler? confirmationHandler = null,
        LaunchInfo? launchInfo = null,
        bool autoReportLaunch = true,
        ToolCallHandler? toolCallHandler = null,
        CancellationToken cancellationToken = default)
    {
        if (!ToolMetadata.IsValidAppId(appId))
        {
            throw new ArgumentException("appId 不符合规范 [a-z0-9-]{1,48}（禁止 _ 和 .）: " + appId, nameof(appId));
        }
        if (string.IsNullOrEmpty(appName))
        {
            throw new ArgumentException("appName 不能为空", nameof(appName));
        }

        var client = new AgentQuayClient(
            appId, appName, host, port, autoSpawnBridge,
            version ?? DefaultVersion, protocolVersion ?? DefaultProtocolVersion,
            heartbeatIntervalSeconds, maxRetryIntervalSeconds, confirmationHandler,
            launchInfo ?? (autoReportLaunch ? LaunchInfo.Detect() : null),
            toolCallHandler);

        // 首次连接：确保 Bridge 可用（含 auto-spawn），解析实际端口
        int resolved = await Task.Run(() => client._spawner.EnsureBridge(port, autoSpawnBridge), cancellationToken)
            .ConfigureAwait(false);
        client._port = resolved;
        return client;
    }

    // ------------------------------------------------------------------
    // 工具注册
    // ------------------------------------------------------------------

    /// <summary>注册控制器（Type 扫描；含实例方法时尝试实例化，仅静态方法时无需实例）。</summary>
    public AgentQuayClient RegisterTools<T>() => RegisterTools(typeof(T));

    /// <summary>注册控制器类型（自动实例化：无参构造，含私有；仅静态方法时无需实例）。</summary>
    public AgentQuayClient RegisterTools(Type controllerType) => RegisterTools(controllerType, Instantiate(controllerType));

    /// <summary>注册控制器实例（推荐：控制器持有状态时用实例注册）。</summary>
    public AgentQuayClient RegisterTools(object instance) => RegisterTools(instance.GetType(), instance);

    /// <summary>已登记的 tool 名列表。</summary>
    public IReadOnlyList<string> ListTools() => _tools.Select(t => t.Name).ToList();

    private AgentQuayClient RegisterTools(Type controllerType, object? instance)
    {
        if (instance == null && HasAnnotatedInstanceMethods(controllerType))
        {
            throw new InvalidOperationException(
                $"控制器含实例方法但无法实例化（需要可访问的无参构造）: {controllerType.FullName}");
        }

        foreach (MethodInfo method in controllerType.GetMethods(
                     BindingFlags.Public | BindingFlags.Instance | BindingFlags.Static))
        {
            var at = method.GetCustomAttribute<AgentToolAttribute>();
            if (at == null)
            {
                continue;
            }
            string name = string.IsNullOrEmpty(at.Name) ? method.Name : at.Name!;
            if (!ToolMetadata.IsValidToolName(name))
            {
                throw new ArgumentException($"tool 名 {name} 不符合规范 [a-zA-Z0-9_-]{{1,78}}");
            }
            if (_tools.Any(t => t.Name == name))
            {
                throw new ArgumentException("tool 名重复: " + name);
            }
            var schema = _schemaGen.ParamSchema(method);
            object? target = method.IsStatic ? null : instance;
            _tools.Add(new ToolMetadata(name, at.Description ?? "", schema,
                at.RequiresConfirmation, at.TimeoutSeconds, at.ConfirmTimeoutSeconds,
                target, method));
        }
        return this;
    }

    private static object? Instantiate(Type type)
    {
        try
        {
            return Activator.CreateInstance(type, nonPublic: true);
        }
        catch (Exception)
        {
            return null;
        }
    }

    private static bool HasAnnotatedInstanceMethods(Type type)
    {
        foreach (MethodInfo m in type.GetMethods(BindingFlags.Public | BindingFlags.Instance))
        {
            if (m.GetCustomAttribute<AgentToolAttribute>() != null)
            {
                return true;
            }
        }
        return false;
    }

    // ------------------------------------------------------------------
    // 连接生命周期
    // ------------------------------------------------------------------

    /// <summary>
    /// 打开到 Bridge 的 WebSocket、注册工具并保持连接，监听 Agent 调用。
    /// 阻塞直到 <see cref="DisposeAsync"/>、连接被同 appId 的新实例替换（抛
    /// <see cref="ReplacedException"/>）、或 <paramref name="cancellationToken"/> 取消。
    /// 断线自动指数退避重连并携带持久化 token。
    /// </summary>
    public async Task StartAsync(CancellationToken cancellationToken = default)
    {
        if (_stopRequested)
        {
            throw new InvalidOperationException("客户端已关闭，无法再次启动");
        }
        await RunLoopAsync(cancellationToken).ConfigureAwait(false);
    }

    /// <summary>停止客户端：关闭当前连接并从 Bridge 注销（重连循环退出）。</summary>
    public async ValueTask DisposeAsync()
    {
        _stopRequested = true;
        var ws = _ws;
        if (ws != null)
        {
            try
            {
                await ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "bye", CancellationToken.None)
                    .ConfigureAwait(false);
            }
            catch (Exception)
            {
                // 已断开
            }
        }
        _messages.Writer.TryComplete();
        _sendLock.Dispose();
    }

    // ------------------------------------------------------------------
    // 内部实现
    // ------------------------------------------------------------------

    private async Task RunLoopAsync(CancellationToken ct)
    {
        long backoffMillis = 1_000;
        while (!_stopRequested && !ct.IsCancellationRequested)
        {
            try
            {
                await ConnectOnceAsync(ct).ConfigureAwait(false);
                backoffMillis = 1_000;
            }
            catch (ReplacedException)
            {
                throw; // 被同 appId 的新实例替换，停止重连
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested)
            {
                break;
            }
            catch (Exception e)
            {
                if (_stopRequested || ct.IsCancellationRequested)
                {
                    break;
                }
                // Bridge 可能已重启并发生端口漂移（autoPort）：重读权威端口文件
                int read = BridgeSpawner.ReadPortFile();
                if (read > 0 && read != _port)
                {
                    Console.WriteLine($"[AgentQuay] Bridge 端口变化 {_port} → {read}");
                    _port = read;
                }
                Console.WriteLine($"[AgentQuay] 连接失败: {e.Message}（{backoffMillis / 1000}s 后重连）");
                try
                {
                    await Task.Delay(TimeSpan.FromMilliseconds(backoffMillis), ct).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
                backoffMillis = Math.Min(backoffMillis * 2, _maxRetryIntervalSeconds * 1000L);
            }
        }
    }

    private async Task ConnectOnceAsync(CancellationToken ct)
    {
        // 清空上一轮的残留消息
        while (_messages.Reader.TryRead(out _))
        {
        }

        var ws = new ClientWebSocket();
        // 应用层 30s ping/pong 心跳已足够，禁用 WS 层 keep-alive 避免重复探测
        ws.Options.KeepAliveInterval = Timeout.InfiniteTimeSpan;
        try
        {
            await ws.ConnectAsync(new Uri($"ws://{_host}:{_port}/ws"), ct).ConfigureAwait(false);
        }
        catch (Exception e)
        {
            throw new ConnectionException("连接 Bridge 失败: " + e.Message, e);
        }
        _ws = ws;

        // 接收泵：独立任务把消息投递到队列（单读者，避免并发 ReceiveAsync）
        var pump = Task.Run(() => ReceivePumpAsync(ws), CancellationToken.None);

        try
        {
            await RegisterAsync(ws, ct).ConfigureAwait(false);
            Console.WriteLine($"[AgentQuay] 注册成功: {_appId} (tools={_tools.Count})");

            int silentCycles = 0;
            while (!_stopRequested && !ct.IsCancellationRequested)
            {
                string? raw = await ReadWithTimeoutAsync(
                    TimeSpan.FromSeconds(_heartbeatIntervalSeconds * 2L), ct).ConfigureAwait(false);

                if (raw == null)
                {
                    // 静默周期：主动发心跳探测
                    silentCycles++;
                    try
                    {
                        await SendAsync(ws, Protocol.MsgPing,
                            new { timestamp = DateTimeOffset.UtcNow.ToUnixTimeSeconds() }, ct).ConfigureAwait(false);
                    }
                    catch (Exception e)
                    {
                        throw new ConnectionException("心跳发送失败: " + e.Message, e);
                    }
                    if (silentCycles >= 2)
                    {
                        throw new ConnectionException("心跳超时（无响应）");
                    }
                    continue;
                }
                if (raw == ClosedSentinel)
                {
                    throw new ConnectionException("连接已关闭");
                }
                silentCycles = 0;
                await HandleMessageAsync(ws, raw, ct).ConfigureAwait(false);
            }
        }
        finally
        {
            _ws = null;
            try
            {
                ws.Abort();
            }
            catch (Exception)
            {
            }
            // 等接收泵退出（Abort 后 ReceiveAsync 立即失败）
            try
            {
                await pump.WaitAsync(TimeSpan.FromSeconds(2)).ConfigureAwait(false);
            }
            catch (Exception)
            {
            }
        }
    }

    private async Task RegisterAsync(ClientWebSocket ws, CancellationToken ct)
    {
        var toolsMeta = new JsonArray(_tools.Select(t => (JsonNode)new JsonObject
        {
            ["name"] = t.Name,
            ["description"] = t.Description,
            ["inputSchema"] = t.InputSchema,
            ["requiresConfirmation"] = t.RequiresConfirmation,
            ["timeoutSeconds"] = t.TimeoutSeconds,
            ["confirmTimeoutSeconds"] = t.ConfirmTimeoutSeconds,
        }).ToArray());

        await SendAsync(ws, Protocol.MsgRegister,
            Protocol.RegisterPayload(_appId, _appName, _version, _protocolVersion, _token, toolsMeta,
                _launchInfo?.ToJson()),
            ct).ConfigureAwait(false);

        // 等待 register_ack / register_error（10s）
        var deadline = DateTime.UtcNow.AddSeconds(10);
        while (DateTime.UtcNow < deadline)
        {
            var remaining = deadline - DateTime.UtcNow;
            string? raw = await ReadWithTimeoutAsync(remaining, ct).ConfigureAwait(false);
            if (raw == null)
            {
                throw new ConnectionException("注册超时（10s 未收到 register_ack）");
            }
            if (raw == ClosedSentinel)
            {
                throw new ConnectionException("注册期间连接关闭");
            }

            var env = Protocol.Parse(raw);
            string type = env["type"]?.GetValue<string>() ?? "";
            if (Protocol.MsgRegisterAck == type)
            {
                string newToken = Protocol.Payload(env)["token"]?.GetValue<string>() ?? "";
                if (!string.IsNullOrEmpty(newToken))
                {
                    _token = newToken;
                    _tokenStore.Set(newToken); // 持久化，重连自动携带
                }
                return;
            }
            if (Protocol.MsgRegisterError == type)
            {
                var err = Protocol.Payload(env);
                string code = err["code"]?.GetValue<string>() ?? "UNKNOWN";
                string message = err["message"]?.GetValue<string>() ?? "";
                if ("AUTH_FAILED" == code)
                {
                    throw new AuthFailedException(message);
                }
                throw new RegistrationException(code, message);
            }
        }
        throw new ConnectionException("注册超时（10s 未收到 register_ack）");
    }

    private async Task HandleMessageAsync(ClientWebSocket ws, string raw, CancellationToken ct)
    {
        var env = Protocol.Parse(raw);
        string type = env["type"]?.GetValue<string>() ?? "";
        var payload = Protocol.Payload(env);

        switch (type)
        {
            case Protocol.MsgInvoke:
                _ = Task.Run(() => SafeDispatchAsync(() => HandleInvokeAsync(ws, payload, ct)));
                break;
            case Protocol.MsgConfirm:
                _ = Task.Run(() => SafeDispatchAsync(() => HandleConfirmAsync(ws, payload)));
                break;
            case Protocol.MsgPing:
                await SendAsync(ws, Protocol.MsgPong,
                    new { timestamp = DateTimeOffset.UtcNow.ToUnixTimeSeconds() }, ct).ConfigureAwait(false);
                break;
            case Protocol.MsgPong:
                break; // 静默计数已在消息循环重置
            case Protocol.MsgDisconnect:
                string reason = payload["reason"]?.GetValue<string>() ?? Protocol.DisconnectNormal;
                if (Protocol.DisconnectReplaced == reason)
                {
                    throw new ReplacedException();
                }
                if (Protocol.DisconnectMigrate == reason)
                {
                    // Bridge 让位给更强的系统服务实例：按普通断线重连，重连会重读端口文件
                    Console.WriteLine("[AgentQuay] Bridge 让位，重连时将重读端口文件");
                }
                throw new ConnectionException("Bridge 断开: " + reason);
            case Protocol.MsgNotification:
                Console.WriteLine("[AgentQuay] Bridge 通知: " +
                    (payload["message"]?.GetValue<string>() ?? ""));
                break;
            default:
                Console.WriteLine($"[AgentQuay] 未知消息类型: {type}");
                break;
        }
    }

    private static async Task SafeDispatchAsync(Func<Task> action)
    {
        try
        {
            await action().ConfigureAwait(false);
        }
        catch (Exception e)
        {
            Console.WriteLine($"[AgentQuay] 消息处理异常: {e.Message}");
        }
    }

    // ------------------------------------------------------------------
    // 调用分发
    // ------------------------------------------------------------------

    private async Task HandleInvokeAsync(ClientWebSocket ws, JsonObject payload, CancellationToken ct)
    {
        string requestId = payload["requestId"]?.GetValue<string>() ?? "";
        string toolName = payload["tool"]?.GetValue<string>() ?? "";
        int timeoutSeconds = payload["timeoutSeconds"]?.GetValue<int>() ?? 30;
        var args = payload["arguments"];

        var tool = FindTool(toolName);
        if (tool == null)
        {
            await SendResultAsync(ws, requestId, false, null, Error("TOOL_NOT_FOUND", "tool 不存在: " + toolName), ct)
                .ConfigureAwait(false);
            return;
        }

        // 触发工具调用钩子（在业务方法执行前）
        var argDict = args != null && args.GetValueKind() == JsonValueKind.Object
            ? JsonToObject(args) as IReadOnlyDictionary<string, object?>
            : null;
        _toolCallHandler?.Invoke(toolName, argDict);

        try
        {
            object? result = await InvokeWithTimeoutAsync(tool, args, timeoutSeconds, ct).ConfigureAwait(false);
            await SendResultAsync(ws, requestId, true, result, null, ct).ConfigureAwait(false);
        }
        catch (TimeoutException)
        {
            await SendResultAsync(ws, requestId, false, null,
                Error("EXECUTION_TIMEOUT", $"执行超时（>{timeoutSeconds}s）"), ct).ConfigureAwait(false);
        }
        catch (Exception e)
        {
            await SendResultAsync(ws, requestId, false, null,
                Error("EXECUTION_ERROR", RootMessage(e).Message), ct).ConfigureAwait(false);
        }
    }

    private async Task<object?> InvokeWithTimeoutAsync(ToolMetadata tool, JsonNode? args, int timeoutSeconds, CancellationToken ct)
    {
        // 超时上限 = Bridge 侧执行超时 + 5s 余量（保证 Bridge 先超时，迟到结果进孤儿处理）
        using var timeoutCts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeoutCts.CancelAfter(TimeSpan.FromSeconds(timeoutSeconds + 5));
        try
        {
            var invocation = Task.Run(() => InvokeSync(tool, args), ct);
            return await invocation.WaitAsync(timeoutCts.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (timeoutCts.IsCancellationRequested && !ct.IsCancellationRequested)
        {
            throw new TimeoutException($"执行超时（>{timeoutSeconds}s）");
        }
    }

    private object? InvokeSync(ToolMetadata tool, JsonNode? args)
    {
        object?[] bound = BindArguments(tool.Method, args);
        try
        {
            return UnwrapResult(tool.Method.Invoke(tool.Target, bound));
        }
        catch (TargetInvocationException e) when (e.InnerException != null)
        {
            // 方法内部异常：unwrap 后原样抛出（与调用者直接抛出一致）
            ExceptionDispatchInfo.Capture(e.InnerException).Throw();
            throw; // 不可达
        }
    }

    /// <summary>按参数名将 JSON arguments 绑定到方法参数（System.Text.Json 做类型转换）。</summary>
    private static object?[] BindArguments(MethodInfo method, JsonNode? args)
    {
        var parameters = method.GetParameters();
        var values = new object?[parameters.Length];
        bool isObject = args is JsonObject;
        for (int i = 0; i < parameters.Length; i++)
        {
            var param = parameters[i];
            if (param.ParameterType == typeof(CancellationToken))
            {
                values[i] = CancellationToken.None;
                continue;
            }
            string? name = param.Name;
            JsonNode? value = isObject ? (name != null ? args![name] : null) : null;
            // 编译器生成参数名 / 未按名命中（argN 或大小写差异）时兜底：位置参数 / 忽略大小写
            if (value == null && (name == null || name.StartsWith("arg", StringComparison.Ordinal)))
            {
                if (!isObject && args is JsonArray array && i < array.Count)
                {
                    value = array[i];
                }
            }
            if (value == null && isObject)
            {
                value = FindIgnoreCase((JsonObject)args!, name);
            }
            if (value == null || value.GetValueKind() == JsonValueKind.Null)
            {
                values[i] = param.ParameterType.IsValueType && Nullable.GetUnderlyingType(param.ParameterType) == null
                    ? Activator.CreateInstance(param.ParameterType)
                    : null;
                continue;
            }
            values[i] = DeserializeNode(value, param.ParameterType);
        }
        return values;
    }

    private static JsonNode? FindIgnoreCase(JsonObject obj, string? name)
    {
        if (string.IsNullOrEmpty(name))
        {
            return null;
        }
        foreach (var kv in obj)
        {
            if (string.Equals(kv.Key, name, StringComparison.OrdinalIgnoreCase))
            {
                return kv.Value;
            }
        }
        return null;
    }

    private static object? DeserializeNode(JsonNode? node, Type targetType)
    {
        if (node == null)
        {
            return null;
        }
        if (targetType == typeof(object))
        {
            return JsonToObject(node);
        }
        try
        {
            return JsonSerializer.Deserialize(node.ToJsonString(), targetType, Protocol.Json);
        }
        catch (JsonException)
        {
            return null; // 转换失败：交由方法执行暴露（可能演变为业务错误）
        }
    }

    /// <summary>
    /// 异步方法 unwrap：Task / Task&lt;T&gt; / ValueTask / ValueTask&lt;T&gt; 取结果（设计文档 §4.2）。
    /// </summary>
    private static object? UnwrapResult(object? result)
    {
        if (result is Task task)
        {
            return UnwrapTask(task);
        }
        if (result is ValueTask valueTask)
        {
            return UnwrapTask(valueTask.AsTask());
        }
        if (result != null)
        {
            var type = result.GetType();
            if (type.IsGenericType && type.GetGenericTypeDefinition() == typeof(ValueTask<>))
            {
                var asTask = type.GetMethod("AsTask");
                if (asTask != null && asTask.Invoke(result, null) is Task t)
                {
                    return UnwrapTask(t);
                }
            }
        }
        return result;
    }

    private static object? UnwrapTask(Task task)
    {
        task.GetAwaiter().GetResult(); // 先等待完成并抛出原始异常（不包 AggregateException）
        var type = task.GetType();
        if (type.IsGenericType)
        {
            return type.GetProperty("Result")?.GetValue(task);
        }
        return null;
    }

    private async Task SendResultAsync(ClientWebSocket ws, string requestId, bool success,
                                       object? data, JsonObject? error, CancellationToken ct)
    {
        var payload = new JsonObject
        {
            ["requestId"] = requestId,
            ["success"] = success,
            ["data"] = data == null ? null : JsonSerializer.SerializeToNode(data, Protocol.Json),
            ["error"] = error,
        };
        try
        {
            await SendAsync(ws, Protocol.MsgResult, payload, ct).ConfigureAwait(false);
        }
        catch (Exception e)
        {
            Console.WriteLine($"[AgentQuay] 发送 result 失败（连接可能已断开）: {requestId}: {e.Message}");
        }
    }

    // ------------------------------------------------------------------
    // 确认流程
    // ------------------------------------------------------------------

    private async Task HandleConfirmAsync(ClientWebSocket ws, JsonObject payload)
    {
        string requestId = payload["requestId"]?.GetValue<string>() ?? "";
        string message = payload["message"]?.GetValue<string>() ?? "确认执行操作？";
        var args = payload["arguments"];
        int timeoutSeconds = payload["timeoutSeconds"]?.GetValue<int>() ?? 120;

        var argMap = args != null && args.GetValueKind() == JsonValueKind.Object
            ? JsonToObject(args) as IReadOnlyDictionary<string, object?>
            : null;

        bool confirmed;
        try
        {
            confirmed = _confirmationHandler(message, argMap, timeoutSeconds);
        }
        catch (Exception e)
        {
            Console.WriteLine($"[AgentQuay] 确认流程异常，默认拒绝: {e.Message}");
            confirmed = false;
        }
        try
        {
            await SendAsync(ws, Protocol.MsgConfirmResult,
                new { requestId, confirmed }, CancellationToken.None).ConfigureAwait(false);
        }
        catch (Exception e)
        {
            Console.WriteLine($"[AgentQuay] 发送 confirm_result 失败: {requestId}: {e.Message}");
        }
    }

    private ToolMetadata? FindTool(string name) => _tools.FirstOrDefault(t => t.Name == name);

    private static JsonObject Error(string code, string message) => new()
    {
        ["code"] = code,
        ["message"] = message,
    };

    private static Exception RootMessage(Exception e)
    {
        Exception cause = e;
        while (cause.InnerException != null && cause.InnerException != cause)
        {
            cause = cause.InnerException;
        }
        return cause;
    }

    // ------------------------------------------------------------------
    // WebSocket 收发
    // ------------------------------------------------------------------

    /// <summary>从队列读取一条消息，超时返回 null（触发心跳探测）。</summary>
    private async Task<string?> ReadWithTimeoutAsync(TimeSpan timeout, CancellationToken ct)
    {
        using var timeoutCts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeoutCts.CancelAfter(timeout);
        try
        {
            return await _messages.Reader.ReadAsync(timeoutCts.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (!ct.IsCancellationRequested)
        {
            return null; // 静默超时
        }
    }

    /// <summary>序列化并发送一条消息（ClientWebSocket 不支持并发发送，持锁串行化）。</summary>
    private async Task SendAsync(ClientWebSocket ws, string type, object? payload, CancellationToken ct)
    {
        string text = Protocol.Encode(type, payload);
        await _sendLock.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            var bytes = Encoding.UTF8.GetBytes(text);
            await ws.SendAsync(new ArraySegment<byte>(bytes), WebSocketMessageType.Text, true, ct)
                .ConfigureAwait(false);
        }
        finally
        {
            _sendLock.Release();
        }
    }

    /// <summary>接收泵：把到达的文本消息投递到队列；连接关闭/出错时投递哨兵。</summary>
    private async Task ReceivePumpAsync(ClientWebSocket ws)
    {
        var sb = new StringBuilder();
        try
        {
            while (ws.State == WebSocketState.Open)
            {
                WebSocketReceiveResult received;
                do
                {
                    received = await ws.ReceiveAsync(new ArraySegment<byte>(_receiveBuffer), CancellationToken.None).ConfigureAwait(false);
                    if (received.MessageType == WebSocketMessageType.Close)
                    {
                        try
                        {
                            await ws.CloseOutputAsync(WebSocketCloseStatus.NormalClosure, "closed", CancellationToken.None)
                                .ConfigureAwait(false);
                        }
                        catch (Exception)
                        {
                        }
                        goto done;
                    }
                    sb.Append(Encoding.UTF8.GetString(_receiveBuffer, 0, received.Count));
                }
                while (!received.EndOfMessage);

                if (sb.Length > 0)
                {
                    _messages.Writer.TryWrite(sb.ToString());
                    sb.Clear();
                }
            }
        }
        catch (Exception)
        {
            // 连接中止/对端关闭：由 finally 投递哨兵
        }
        done:
        try
        {
            _messages.Writer.TryWrite(ClosedSentinel);
        }
        catch (Exception)
        {
        }
    }

    // ------------------------------------------------------------------
    // 工具方法
    // ------------------------------------------------------------------

    /// <summary>JsonNode → 普通对象图（int/long/double/string/bool/list/dictionary），供确认展示等使用。</summary>
    private static object? JsonToObject(JsonNode? node)
    {
        switch (node)
        {
            case null:
                return null;
            case JsonArray array:
                return array.Select(JsonToObject).ToList();
            case JsonObject obj:
                return obj.ToDictionary(kv => kv.Key, kv => JsonToObject(kv.Value));
            case JsonValue value:
            {
                var kind = node.GetValueKind();
                switch (kind)
                {
                    case JsonValueKind.String:
                        return value.GetValue<string>();
                    case JsonValueKind.Number:
                        return value.TryGetValue<long>(out var l) ? (object)l
                            : value.TryGetValue<double>(out var d) ? d : null;
                    case JsonValueKind.True:
                        return true;
                    case JsonValueKind.False:
                        return false;
                    default:
                        return null;
                }
            }
            default:
                return null;
        }
    }
}