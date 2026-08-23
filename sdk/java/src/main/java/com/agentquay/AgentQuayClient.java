package com.agentquay;

import com.agentquay.internal.Protocol;
import com.fasterxml.jackson.databind.JsonNode;

import java.lang.ref.WeakReference;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.lang.reflect.Modifier;
import java.lang.reflect.Parameter;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.WebSocket;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CompletionException;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.Consumer;
import java.util.function.Supplier;
import java.util.logging.Level;
import java.util.logging.Logger;

/**
 * 连接 AgentQuay Bridge 的应用侧客户端（设计文档 §4.4）。
 *
 * <p>职责：注册 Tool、心跳、调用分发、确认流程、指数退避重连、token 持久化、
 * auto-spawn 内嵌 Bridge。
 *
 * <pre>{@code
 * AgentQuayClient client = AgentQuayClient.builder()
 *     .appId("music-app")
 *     .appName("Music Player")
 *     .port(0)                 // 从 ~/.agentquay/port 自动读取
 *     .autoSpawnBridge(true)   // 未检测到服务时自动拉起内嵌 Bridge
 *     .build();
 * client.registerTools(MusicController.class);
 * client.connect();           // 阻塞保持连接；Swing 应用可用 connectAsync()
 * }</pre>
 */
public class AgentQuayClient implements AutoCloseable {

    private static final Logger LOG = Logger.getLogger(AgentQuayClient.class.getName());

    /** 队列哨兵：连接关闭/出错。 */
    private static final String CLOSED = "\u0000__CLOSED__";

    private final String appId;
    private final String appName;
    private final String host;
    private volatile int port;
    private final boolean autoSpawnBridge;
    private final String version;
    private final String protocolVersion;
    private final int heartbeatInterval;
    private final int maxRetryInterval;
    private final ConfirmationHandler confirmHandler;
    /** 工具调用钩子：在业务方法执行前触发（允许 UI 层拦截并响应）。 */
    private final ToolCallHandler toolCallHandler;
    /** 上报给 Bridge 的启动命令（§5.8，离线自动拉起用）。 */
    private final LaunchInfo launchInfo;

    private final List<ToolBinding> tools = new ArrayList<>();
    /** 页面路由（页面智能路由 §4.2）：pageKey → 创建中的 CompletableFuture（单飞去重）。 */
    private final ConcurrentHashMap<String, CompletableFuture<Object>> inflight = new ConcurrentHashMap<>();
    /** 页面路由锁：每 key 一把（"查-建-换"与超时让出互斥，防重复创建）。 */
    private final ConcurrentHashMap<String, Object> gates = new ConcurrentHashMap<>();
    /** pageKey → 激活钩子（导航 / 就绪等待）。 */
    private final ConcurrentHashMap<String, PageActivator> activators = new ConcurrentHashMap<>();
    private volatile UIThreadDispatcher uiDispatcher;
    /** 页面激活超时秒数（创建/导航/等待整体计时，默认 15s，方案 §2.3）。 */
    private volatile int pageActivationTimeoutSeconds = 15;
    private final JsonSchemaGenerator schemaGen = new JsonSchemaGenerator();
    private final TokenStore tokenStore;
    private final BridgeSpawner spawner;
    private final ExecutorService executor;
    private final BlockingQueue<String> messageQueue = new LinkedBlockingQueue<>();

    private volatile WebSocket ws;
    private volatile String token;
    private volatile boolean stopRequested;
    private volatile int silentCycles;
    private final AtomicBoolean connected = new AtomicBoolean(false);

    private AgentQuayClient(Builder b) {
        this.appId = b.appId;
        this.appName = b.appName;
        this.host = b.host;
        this.port = b.port;
        this.autoSpawnBridge = b.autoSpawnBridge;
        this.version = b.version;
        this.protocolVersion = b.protocolVersion;
        this.heartbeatInterval = b.heartbeatInterval;
        this.maxRetryInterval = b.maxRetryInterval;
        this.confirmHandler = b.confirmHandler != null ? b.confirmHandler : ConfirmDialog::ask;
        this.toolCallHandler = b.toolCallHandler;
        this.launchInfo = b.launchInfo != null ? b.launchInfo : (b.autoReportLaunch ? LaunchInfo.detect() : null);
        this.tokenStore = new TokenStore(appId);
        this.token = tokenStore.get();
        this.spawner = new BridgeSpawner(host);
        this.executor = Executors.newCachedThreadPool(daemonThreadFactory("agentquay-dispatch"));
    }

    public static Builder builder() {
        return new Builder();
    }

    // ------------------------------------------------------------------
    // 工具注册
    // ------------------------------------------------------------------

    /** 注册控制器类（无参构造实例化；仅静态方法时无需实例）。 */
    public AgentQuayClient registerTools(Class<?> controllerClass) {
        Object instance = instantiate(controllerClass);
        boolean hasInstanceMethods = hasAnnotatedInstanceMethods(controllerClass);
        if (instance == null && hasInstanceMethods) {
            throw new IllegalArgumentException(
                    "控制器含实例方法但无法实例化（需要可访问的无参构造）: "
                            + controllerClass.getName());
        }
        return registerTools(instance, controllerClass);
    }

    /** 注册控制器实例。 */
    public AgentQuayClient registerTools(Object instance) {
        return registerTools(instance, instance.getClass());
    }

    /**
     * 惰性注册控制器类（页面智能路由 §3.2）：页面未打开工具也可见，首次调用才创建实例
     * （需无参构造）。pageKey 只是 SDK 内部的分组标签，不进协议、Agent 无感知。
     */
    public AgentQuayClient registerTools(Class<?> controllerClass, String pageKey) {
        requirePageKey(pageKey);
        if (hasAnnotatedInstanceMethods(controllerClass) && !hasParameterlessCtor(controllerClass)) {
            throw new IllegalArgumentException(
                    "控制器含实例方法但无法实例化（需要可访问的无参构造）: "
                            + controllerClass.getName());
        }
        return registerBindings(controllerClass, pageKey, null, () -> instantiate(controllerClass));
    }

    /**
     * 惰性注册（显式工厂，DI 场景，页面智能路由）：首次调用经工厂创建实例。
     * 与 C# {@code RegisterTools(Func<object>, pageKey)} / Python {@code register_tools_factory} 同构。
     *
     * @param controllerClass 控制器类（注解扫描与 schema 生成用）
     * @param factory         创建页面实例的工厂（首次调用才执行）
     * @param pageKey         页面分组标签
     */
    public AgentQuayClient registerToolsFactory(Class<?> controllerClass,
                                                Supplier<Object> factory, String pageKey) {
        requirePageKey(pageKey);
        if (factory == null) {
            throw new NullPointerException("factory");
        }
        return registerBindings(controllerClass, pageKey, null, factory);
    }

    /**
     * 注册 pageKey 的激活钩子（页面智能路由 §3.2）：首次惰性创建后执行一次。
     * navigate 在 UI 线程执行；awaitReady 返回 {@link CompletableFuture}（如 Loaded 事件），
     * SDK 会等待其完成，实现必须异步等待、禁止阻塞。
     */
    public AgentQuayClient setPageActivator(String pageKey, Consumer<Object> navigate,
                                            java.util.function.Function<Object, CompletableFuture<?>> awaitReady) {
        requirePageKey(pageKey);
        activators.put(pageKey, new PageActivator(navigate, awaitReady));
        return this;
    }

    /** 页面关闭时显式注销：清除弱引用与激活钩子，工厂路径下次调用自动重建。
     *  不调也行——弱引用 GC 后自动失效。 */
    public AgentQuayClient unregisterPage(String pageKey) {
        for (ToolBinding b : tools) {
            if (pageKey.equals(b.pageKey)) {
                b.live = null;
            }
        }
        activators.remove(pageKey);
        return this;
    }

    /** 注入 UI 线程调度器（页面智能路由 §5.2）：页面工具创建与调用在 UI 线程执行
     * （Swing 场景可用 invokeAndWait 实现；null = 直接执行，无 UI 场景）。 */
    public AgentQuayClient setUIThreadDispatcher(UIThreadDispatcher dispatcher) {
        this.uiDispatcher = dispatcher;
        return this;
    }

    /** 设置页面激活超时秒数（创建/导航/等待整体计时，默认 15s）。 */
    public AgentQuayClient setPageActivationTimeoutSeconds(int seconds) {
        if (seconds <= 0) {
            throw new IllegalArgumentException("激活超时必须为正数");
        }
        this.pageActivationTimeoutSeconds = seconds;
        return this;
    }

    private static void requirePageKey(String pageKey) {
        if (pageKey == null || pageKey.isEmpty()) {
            throw new IllegalArgumentException("pageKey 不能为空");
        }
    }

    private static Object instantiate(Class<?> clazz) {
        try {
            java.lang.reflect.Constructor<?> ctor = clazz.getDeclaredConstructor();
            try {
                ctor.setAccessible(true); // 包私有嵌套类等
            } catch (RuntimeException ignored) {
                // 模块系统限制时保持原访问性
            }
            return ctor.newInstance();
        } catch (ReflectiveOperationException e) {
            return null;
        }
    }

    private static boolean hasParameterlessCtor(Class<?> clazz) {
        try {
            clazz.getDeclaredConstructor();
            return true;
        } catch (NoSuchMethodException e) {
            return false;
        }
    }

    private static boolean hasAnnotatedInstanceMethods(Class<?> clazz) {
        for (Method m : clazz.getMethods()) {
            if (m.getAnnotation(AgentTool.class) != null && !Modifier.isStatic(m.getModifiers())) {
                return true;
            }
        }
        return false;
    }

    /** 立即绑定（原行为）：pageKey 为 null，target 直接持有。 */
    private AgentQuayClient registerTools(Object target, Class<?> clazz) {
        return registerBindings(clazz, null, target, null);
    }

    /** 惰性注册：实例只在首次调用时经工厂创建，元数据立即入表。 */
    private AgentQuayClient registerBindings(Class<?> clazz, String pageKey,
                                             Object target, Supplier<Object> factory) {
        for (Method method : clazz.getMethods()) {
            AgentTool at = method.getAnnotation(AgentTool.class);
            if (at == null) {
                continue;
            }
            String name = at.value().isEmpty() ? method.getName() : at.value();
            if (!ToolMetadata.isValidToolName(name)) {
                throw new IllegalArgumentException(
                        "tool 名 " + name + " 不符合规范 [a-zA-Z0-9_-]{1,78}");
            }
            for (ToolBinding t : tools) {
                if (t.metadata.getName().equals(name)) {
                    // 同一 appId 内工具名跨页面全局唯一（页面智能路由 §2.2）
                    throw new IllegalArgumentException("tool 名重复（跨页面也须全局唯一）: " + name);
                }
            }
            Map<String, Object> schema = schemaGen.paramSchema(method);
            boolean isStatic = Modifier.isStatic(method.getModifiers());
            Object invokeTarget = isStatic ? null : target;
            tools.add(new ToolBinding(
                    new ToolMetadata(name, at.description(), schema,
                            at.requiresConfirmation(), at.timeoutSeconds(), at.confirmTimeoutSeconds(),
                            invokeTarget, method),
                    pageKey, isStatic ? null : target, isStatic ? null : factory));
            LOG.fine("已登记 tool: " + name);
        }
        return this;
    }

    /** 已登记的 tool 名列表。 */
    public List<String> listTools() {
        List<String> names = new ArrayList<>();
        for (ToolBinding t : tools) {
            names.add(t.metadata.getName());
        }
        return names;
    }

    // ------------------------------------------------------------------
    // 连接生命周期
    // ------------------------------------------------------------------

    /** 连接 Bridge 并保持（阻塞直到 close() 或连接被替换）。 */
    public void connect() throws AgentQuayException {
        try {
            connectAsync().join();
        } catch (CompletionException e) {
            Throwable cause = e.getCause();
            if (cause instanceof AgentQuayException) {
                throw (AgentQuayException) cause;
            }
            if (cause instanceof RuntimeException) {
                throw (RuntimeException) cause;
            }
            throw new AgentQuayException("连接失败: " + cause, cause);
        }
    }

    /** 异步连接（适合 Swing 等 GUI 应用）。 */
    public CompletableFuture<Void> connectAsync() {
        if (stopRequested) {
            CompletableFuture<Void> failed = new CompletableFuture<>();
            failed.completeExceptionally(new IllegalStateException("客户端已关闭，无法再次 connect"));
            return failed;
        }
        return CompletableFuture.runAsync(() -> {
            // 首次连接：确保 Bridge 可用（含 auto-spawn），解析实际端口
            int resolved = spawner.ensureBridge(port, autoSpawnBridge);
            port = resolved;
            runLoop();
        }, executor);
    }

    @Override
    public void close() {
        stopRequested = true;
        WebSocket current = ws;
        if (current != null) {
            try {
                current.sendClose(WebSocket.NORMAL_CLOSURE, "bye");
            } catch (Exception ignored) {
            }
        }
        executor.shutdownNow();
    }

    // ------------------------------------------------------------------
    // 内部实现
    // ------------------------------------------------------------------

    private void runLoop() {
        long backoffMillis = 1_000;
        while (!stopRequested) {
            try {
                connectOnce();
                backoffMillis = 1_000;
            } catch (AgentQuayException.ReplacedException e) {
                LOG.warning("连接被同 appId 的新实例替换，停止重连");
                throw e;
            } catch (Exception e) {
                if (stopRequested) {
                    break;
                }
                // Bridge 可能已重启并发生端口漂移（autoPort）：重读权威端口文件
                int read = BridgeSpawner.readPortFile();
                if (read > 0 && read != port) {
                    LOG.info("Bridge 端口变化 " + port + " → " + read);
                    port = read;
                }
                LOG.log(Level.WARNING, "连接失败: " + e + "（" + backoffMillis / 1000 + "s 后重连）");
                sleep(backoffMillis);
                backoffMillis = Math.min(backoffMillis * 2, maxRetryInterval * 1000L);
            }
        }
    }

    private void connectOnce() throws Exception {
        messageQueue.clear();
        silentCycles = 0;

        WebSocket socket = HttpClient.newHttpClient()
                .newWebSocketBuilder()
                .connectTimeout(Duration.ofSeconds(10))
                .buildAsync(URI.create("ws://" + host + ":" + port + "/ws"), new Listener())
                .join();
        this.ws = socket;

        try {
            register(socket);
            LOG.info("注册成功: " + appId + " (tools=" + tools.size() + ")");
            connected.set(true);

            // 消息循环：poll 带超时实现心跳检测
            while (!stopRequested) {
                String raw = messageQueue.poll(heartbeatInterval * 2L, TimeUnit.SECONDS);
                if (raw == null) {
                    silentCycles++;
                    try {
                        socket.sendText(Protocol.encode(Protocol.MSG_PING,
                                Map.of("timestamp", System.currentTimeMillis() / 1000)), true).join();
                    } catch (Exception e) {
                        throw new AgentQuayException.ConnectionException("心跳发送失败: " + e);
                    }
                    if (silentCycles >= 2) {
                        throw new AgentQuayException.ConnectionException("心跳超时（无响应）");
                    }
                    continue;
                }
                if (raw == CLOSED) {
                    throw new AgentQuayException.ConnectionException("连接已关闭");
                }
                silentCycles = 0;
                handleMessage(socket, raw);
            }
        } finally {
            connected.set(false);
            this.ws = null;
            try {
                socket.abort();
            } catch (Exception ignored) {
            }
        }
    }

    private void register(WebSocket socket) throws Exception {
        List<Map<String, Object>> toolsMeta = new ArrayList<>();
        for (ToolBinding b : tools) {
            ToolMetadata t = b.metadata;
            // Map.of 不允许 null：pageKey 可选，用 LinkedHashMap 拼接
            Map<String, Object> meta = new LinkedHashMap<>();
            meta.put("name", t.getName());
            meta.put("description", t.getDescription());
            meta.put("inputSchema", t.getInputSchema());
            meta.put("requiresConfirmation", t.isRequiresConfirmation());
            meta.put("timeoutSeconds", t.getTimeoutSeconds());
            meta.put("confirmTimeoutSeconds", t.getConfirmTimeoutSeconds());
            if (b.pageKey != null) {
                meta.put("pageKey", b.pageKey); // 可选分组标签（页面智能路由，旧 SDK 不传即空）
            }
            toolsMeta.add(meta);
        }
        socket.sendText(Protocol.encode(Protocol.MSG_REGISTER, Protocol.registerPayload(
                appId, appName, version, protocolVersion, token, toolsMeta,
                launchInfo != null ? launchInfo.toJson() : null)), true).join();

        // 等待 register_ack / register_error（10s）
        long deadline = System.currentTimeMillis() + 10_000;
        while (System.currentTimeMillis() < deadline) {
            String raw = messageQueue.poll(deadline - System.currentTimeMillis(), TimeUnit.MILLISECONDS);
            if (raw == null) {
                throw new AgentQuayException.ConnectionException("注册超时（10s 未收到 register_ack）");
            }
            if (raw == CLOSED) {
                throw new AgentQuayException.ConnectionException("注册期间连接关闭");
            }
            Map<String, Object> env = Protocol.parse(raw);
            String type = (String) env.get("type");
            if (Protocol.MSG_REGISTER_ACK.equals(type)) {
                JsonNode ack = Protocol.payload(env);
                String newToken = ack.path("token").asText();
                if (!newToken.isEmpty()) {
                    token = newToken;
                    tokenStore.set(newToken); // 持久化，重连自动携带
                }
                return;
            }
            if (Protocol.MSG_REGISTER_ERROR.equals(type)) {
                JsonNode err = Protocol.payload(env);
                String code = err.path("code").asText("UNKNOWN");
                String message = err.path("message").asText("");
                if ("AUTH_FAILED".equals(code)) {
                    throw new AgentQuayException.AuthFailedException(message);
                }
                throw new AgentQuayException.RegistrationException(code, message);
            }
        }
    }

    private void handleMessage(WebSocket socket, String raw) throws Exception {
        Map<String, Object> env = Protocol.parse(raw);
        String type = (String) env.get("type");
        JsonNode payload = Protocol.payload(env);
        LOG.fine("收到消息: " + type);

        switch (type == null ? "" : type) {
            case Protocol.MSG_INVOKE:
                executor.submit(() -> handleInvoke(socket, payload));
                break;
            case Protocol.MSG_CONFIRM:
                executor.submit(() -> handleConfirm(socket, payload));
                break;
            case Protocol.MSG_PING:
                socket.sendText(Protocol.encode(Protocol.MSG_PONG,
                        Map.of("timestamp", System.currentTimeMillis() / 1000)), true).join();
                break;
            case Protocol.MSG_PONG:
                break; // 静默计数已在消息循环重置
            case Protocol.MSG_DISCONNECT:
                String reason = payload.path("reason").asText(Protocol.DISCONNECT_NORMAL);
                if (Protocol.DISCONNECT_REPLACED.equals(reason)) {
                    throw new AgentQuayException.ReplacedException();
                }
                if (Protocol.DISCONNECT_MIGRATE.equals(reason)) {
                    // Bridge 让位给更强的系统服务实例：按普通断线重连，重连会重读端口文件
                    LOG.info("Bridge 让位，重连时将重读端口文件");
                }
                throw new AgentQuayException.ConnectionException("Bridge 断开: " + reason);
            case Protocol.MSG_NOTIFICATION:
                LOG.info("Bridge 通知: " + payload.path("message").asText());
                break;
            default:
                LOG.warning("未知消息类型: " + type);
        }
    }

    // ------------------------------------------------------------------
    // 调用分发
    // ------------------------------------------------------------------

    private void handleInvoke(WebSocket socket, JsonNode payload) {
        String requestId = payload.path("requestId").asText("");
        String toolName = payload.path("tool").asText("");
        int timeoutSeconds = payload.path("timeoutSeconds").asInt(30);
        JsonNode args = payload.path("arguments");

        ToolBinding tool = findTool(toolName);
        if (tool == null) {
            sendResult(socket, requestId, false, null,
                    Map.of("code", "TOOL_NOT_FOUND", "message", "tool 不存在: " + toolName));
            return;
        }

        // 触发工具调用钩子（在业务方法执行前；工具存在性检查之后，与其它语言一致）
        if (toolCallHandler != null) {
            try {
                Map<String, Object> argMap = args.isObject()
                        ? Protocol.MAPPER.convertValue(args,
                        new com.fasterxml.jackson.core.type.TypeReference<Map<String, Object>>() {})
                        : null;
                toolCallHandler.onToolCall(toolName, argMap);
            } catch (Exception e) {
                LOG.log(Level.WARNING, "toolCallHandler 异常: " + e);
            }
        }

        LOG.fine("执行 tool: " + toolName + " args=" + args);

        try {
            Object result = invokeWithTimeout(tool, args, timeoutSeconds);
            sendResult(socket, requestId, true, result, null);
        } catch (TimeoutException e) {
            sendResult(socket, requestId, false, null,
                    Map.of("code", "EXECUTION_TIMEOUT", "message", "执行超时（>" + timeoutSeconds + "s）"));
        } catch (Throwable e) {
            Throwable cause = unwrap(e);
            // 页面智能路由错误语义（方案 §6.4）：激活类错误复用 isError 结构化 JSON
            if (cause instanceof AgentQuayException.PageActivationTimeoutException) {
                sendResult(socket, requestId, false, null,
                        Map.of("code", "PAGE_ACTIVATION_TIMEOUT", "message", String.valueOf(cause.getMessage())));
            } else if (cause instanceof AgentQuayException.PageNotFoundException) {
                sendResult(socket, requestId, false, null,
                        Map.of("code", "PAGE_NOT_FOUND", "message", String.valueOf(cause.getMessage())));
            } else if (cause instanceof AgentQuayException.PageActivationException) {
                sendResult(socket, requestId, false, null,
                        Map.of("code", "PAGE_ACTIVATION_FAILED", "message", String.valueOf(cause.getMessage())));
            } else {
                LOG.log(Level.WARNING, "tool 执行异常: " + toolName, e);
                sendResult(socket, requestId, false, null,
                        Map.of("code", "EXECUTION_ERROR", "message", String.valueOf(rootMessage(e))));
            }
        }
    }

    private Object invokeWithTimeout(ToolBinding binding, JsonNode args, int timeoutSeconds)
            throws Exception {
        // 超时上限 = Bridge 侧执行超时 + 5s 余量（保证 Bridge 先超时，迟到结果进孤儿处理）
        return executor.submit(() -> {
            try {
                return invokeSync(binding, args);
            } catch (Throwable e) {
                throw new CompletionException(e);
            }
        }).get(timeoutSeconds + 5L, TimeUnit.SECONDS);
    }

    /**
     * 调用分发核心（页面智能路由 §4.2）：实例存活直接调；无实例有工厂则单飞创建
     * （UI 线程、带激活超时）；无实例无工厂抛 PageNotFoundException（工具仍在表内）。
     */
    // 包可见（同 C# internal DispatchAsync）：单测直接驱动路由核心
    Object invokeSync(ToolBinding binding, JsonNode args) throws Exception {
        Method method = binding.method();
        Object target;
        if (Modifier.isStatic(method.getModifiers())) {
            target = null; // 静态方法无需实例
        } else {
            target = binding.instance != null ? binding.instance : binding.liveTarget();
            if (target == null && binding.factory != null) {
                target = getOrCreate(binding);
            }
            if (target == null) {
                throw new AgentQuayException.PageNotFoundException(
                        "页面 '" + binding.pageKey + "' 未打开且无工厂，无法调用");
            }
        }
        // 页面工具且有 UI 调度器 → UI 线程执行（Swing 场景）；否则线程池执行（原行为）
        final Object invokeTarget = target; // lambda 捕获要求 effectively final
        if (uiDispatcher != null && binding.pageKey != null) {
            return uiDispatcher.dispatch(() -> invokeMethod(binding, invokeTarget, args));
        }
        return invokeMethod(binding, invokeTarget, args);
    }

    private Object invokeMethod(ToolBinding binding, Object target, JsonNode args) throws Exception {
        Method method = binding.method();
        try {
            method.setAccessible(true);
        } catch (Exception ignored) {
            // 模块系统限制时保持原访问性
        }
        Object[] bound = bindArguments(method, args);
        Object result = method.invoke(target, bound);
        // 异步方法 unwrap：CompletableFuture → 等待结果
        if (result instanceof CompletableFuture) {
            return ((CompletableFuture<?>) result).join();
        }
        return result;
    }

    /**
     * 单飞去重（页面智能路由 §4.2）：并发调用合并等待同一个创建任务，不会建出两个页面；
     * 任务完成（成功或失败）后回 NotLoaded，下次调用重建；超时让出槽位（底层激活继续，
     * 与"页面已导航但调用超时"的孤儿机制一致）。
     */
    private Object getOrCreate(ToolBinding binding) throws Exception {
        String key = binding.pageKey != null ? binding.pageKey : binding.name();
        Object gate = gates.computeIfAbsent(key, k -> new Object());
        CompletableFuture<Object> task;
        synchronized (gate) {
            CompletableFuture<Object> existing = inflight.get(key);
            if (existing == null || existing.isDone()) {
                task = new CompletableFuture<>();
                inflight.put(key, task);
                executor.submit(() -> runActivation(binding, key, task));
            } else {
                task = existing;
            }
        }
        try {
            return task.get(pageActivationTimeoutSeconds, TimeUnit.SECONDS);
        } catch (java.util.concurrent.ExecutionException e) {
            // 激活异常原样透传（去掉 CompletableFuture 的 ExecutionException 外壳）
            Throwable cause = e.getCause();
            if (cause instanceof RuntimeException) {
                throw (RuntimeException) cause;
            }
            throw new AgentQuayException.PageActivationException(
                    "页面激活失败: " + cause, cause);
        } catch (TimeoutException e) {
            synchronized (gate) {
                if (inflight.get(key) == task) {
                    inflight.remove(key);
                }
            }
            throw new AgentQuayException.PageActivationTimeoutException(
                    "页面激活超时（>" + pageActivationTimeoutSeconds + "s，pageKey=" + key + "）");
        }
    }

    private void runActivation(ToolBinding binding, String key, CompletableFuture<Object> task) {
        try {
            task.complete(activate(binding));
        } catch (Throwable t) {
            task.completeExceptionally(t);
        }
    }

    /** 激活：工厂创建 → 可选导航 → 等待就绪。UI 线程执行；成功后先设置弱引用再返回。
     *  工厂/导航/就绪任一抛异常 → PageActivationException（PAGE_ACTIVATION_FAILED）。 */
    private Object activate(ToolBinding binding) throws Exception {
        try {
            java.util.concurrent.Callable<Object> act = () -> {
                Object instance = binding.factory.get();
                if (instance == null) {
                    throw new AgentQuayException.PageActivationException(
                            "页面工厂返回 null（pageKey=" + binding.pageKey + "）");
                }
                PageActivator pa = binding.pageKey != null ? activators.get(binding.pageKey) : null;
                if (pa != null) {
                    if (pa.navigate != null) {
                        pa.navigate.accept(instance);
                    }
                    if (pa.awaitReady != null) {
                        pa.awaitReady.apply(instance).join(); // 异步等待就绪，禁止阻塞 UI
                    }
                }
                return instance;
            };
            Object page = (uiDispatcher != null && binding.pageKey != null)
                    ? uiDispatcher.dispatch(act)
                    : act.call();
            binding.live = new WeakReference<>(page);
            return page;
        } catch (AgentQuayException.PageActivationException e) {
            throw e;
        } catch (Exception e) {
            throw new AgentQuayException.PageActivationException(
                    "页面激活失败（pageKey=" + binding.pageKey + "）: " + rootMessage(e), e);
        }
    }

    /** 剥离 Execution/Completion 包装，取原始异常（错误映射用）。 */
    private static Throwable unwrap(Throwable e) {
        Throwable cause = e;
        while ((cause instanceof java.util.concurrent.ExecutionException
                || cause instanceof CompletionException) && cause.getCause() != null) {
            cause = cause.getCause();
        }
        return cause;
    }

    /** 按参数名将 JSON arguments 绑定到方法参数（Jackson 做类型转换）。 */
    private Object[] bindArguments(Method method, JsonNode args) throws Exception {
        Parameter[] params = method.getParameters();
        Object[] values = new Object[params.length];
        for (int i = 0; i < params.length; i++) {
            String name = params[i].getName();
            JsonNode value = args.isObject() ? args.get(name) : null;
            // 编译器未保留参数名时（argN），按声明顺序取位置参数
            if (value == null && (name == null || name.startsWith("arg"))) {
                if (args.isArray() && i < args.size()) {
                    value = args.get(i);
                }
            }
            values[i] = value == null || value.isNull()
                    ? null
                    : Protocol.MAPPER.treeToValue(value,
                    Protocol.MAPPER.getTypeFactory().constructType(params[i].getParameterizedType()));
        }
        return values;
    }

    private void sendResult(WebSocket socket, String requestId, boolean success,
                            Object data, Map<String, Object> error) {
        try {
            // 注意：Map.of 不允许 null 值（成功时 error=null / 失败时 data=null），
            // 用 LinkedHashMap；null 字段由 Protocol.MAPPER 的 NON_NULL 策略省略
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("requestId", requestId);
            payload.put("success", success);
            payload.put("data", data);
            payload.put("error", error);
            socket.sendText(Protocol.encode(Protocol.MSG_RESULT, payload), true).join();
        } catch (Exception e) {
            LOG.log(Level.FINE, "发送 result 失败（连接可能已断开）: " + requestId, e);
        }
    }

    // ------------------------------------------------------------------
    // 确认流程
    // ------------------------------------------------------------------

    private void handleConfirm(WebSocket socket, JsonNode payload) {
        String requestId = payload.path("requestId").asText("");
        String message = payload.path("message").asText("确认执行操作？");
        JsonNode args = payload.path("arguments");
        int timeoutSeconds = payload.path("timeoutSeconds").asInt(120);

        Map<String, Object> argMap = args.isObject()
                ? Protocol.MAPPER.convertValue(args,
                new com.fasterxml.jackson.core.type.TypeReference<Map<String, Object>>() {
                })
                : null;

        boolean confirmed;
        try {
            confirmed = confirmHandler.confirm(message, argMap, timeoutSeconds);
        } catch (Exception e) {
            LOG.log(Level.WARNING, "确认流程异常，默认拒绝: " + e);
            confirmed = false;
        }
        try {
            socket.sendText(Protocol.encode(Protocol.MSG_CONFIRM_RESULT,
                    Map.of("requestId", requestId, "confirmed", confirmed)), true).join();
        } catch (Exception e) {
            LOG.log(Level.WARNING, "发送 confirm_result 失败: " + requestId, e);
        }
    }

    // 包可见：单测按名取绑定
    ToolBinding findTool(String name) {
        for (ToolBinding t : tools) {
            if (t.metadata.getName().equals(name)) {
                return t;
            }
        }
        return null;
    }

    private static Throwable rootMessage(Throwable e) {
        Throwable cause = e;
        while (cause.getCause() != null && cause.getCause() != cause) {
            cause = cause.getCause();
        }
        if (cause instanceof InvocationTargetException && cause.getCause() != null) {
            cause = cause.getCause();
        }
        return cause;
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private static ThreadFactory daemonThreadFactory(String prefix) {
        return runnable -> {
            Thread t = new Thread(runnable, prefix);
            t.setDaemon(true);
            return t;
        };
    }

    /** WebSocket 监听器：入队消息；关闭/出错入队哨兵。 */
    private final class Listener implements WebSocket.Listener {
        @Override
        public CompletionStage<?> onText(WebSocket webSocket, CharSequence data, boolean last) {
            messageQueue.offer(data.toString());
            webSocket.request(1);
            return null;
        }

        @Override
        public void onError(WebSocket webSocket, Throwable error) {
            messageQueue.offer(CLOSED);
        }

        @Override
        public CompletionStage<?> onClose(WebSocket webSocket, int statusCode, String reason) {
            messageQueue.offer(CLOSED);
            return null;
        }
    }

    // ------------------------------------------------------------------
    // Builder
    // ------------------------------------------------------------------

    public static class Builder {
        private String appId;
        private String appName;
        private String host = "127.0.0.1";
        private int port = 0;
        private boolean autoSpawnBridge = true;
        private String version = "1.0.0";
        private String protocolVersion = "1.0";
        private int heartbeatInterval = 30;
        private int maxRetryInterval = 30;
        private ConfirmationHandler confirmHandler;
        private ToolCallHandler toolCallHandler;
        private LaunchInfo launchInfo;
        private boolean autoReportLaunch = true;

        /** 应用 ID（[a-z0-9-]{1,48}，禁止 _ 和 .；如 music-app）。 */
        public Builder appId(String appId) {
            this.appId = appId;
            return this;
        }

        /** 应用显示名。 */
        public Builder appName(String appName) {
            this.appName = appName;
            return this;
        }

        /** Bridge 地址（默认 127.0.0.1）。 */
        public Builder host(String host) {
            this.host = host;
            return this;
        }

        /** Bridge 端口（0 = 从 ~/.agentquay/port 自动读取）。 */
        public Builder port(int port) {
            this.port = port;
            return this;
        }

        /** 未检测到服务时自动拉起内嵌 Bridge（默认 true）。 */
        public Builder autoSpawnBridge(boolean autoSpawnBridge) {
            this.autoSpawnBridge = autoSpawnBridge;
            return this;
        }

        /** 应用版本号（随注册上报，默认 1.0.0）。 */
        public Builder version(String version) {
            this.version = version;
            return this;
        }

        /** 协议版本（默认 "1.0"）。 */
        public Builder protocolVersion(String protocolVersion) {
            this.protocolVersion = protocolVersion;
            return this;
        }

        /** 心跳间隔秒数（默认 30）。 */
        public Builder heartbeatInterval(int heartbeatInterval) {
            this.heartbeatInterval = heartbeatInterval;
            return this;
        }

        /** 重连退避上限秒数（默认 30）。 */
        public Builder maxRetryInterval(int maxRetryInterval) {
            this.maxRetryInterval = maxRetryInterval;
            return this;
        }

        /** 自定义确认回调（默认 Swing 对话框；无图形环境回退控制台）。 */
        public Builder confirmHandler(ConfirmationHandler confirmHandler) {
            this.confirmHandler = confirmHandler;
            return this;
        }

        /** 工具调用钩子：在业务方法执行前触发，允许 UI 层拦截并响应。 */
        public Builder toolCallHandler(ToolCallHandler toolCallHandler) {
            this.toolCallHandler = toolCallHandler;
            return this;
        }

        /** 启动命令（§5.8）：显式指定，随 register 上报供 Bridge 离线自动拉起。
         *  缺省自动探测（{@link LaunchInfo#detect()}）。 */
        public Builder launchInfo(LaunchInfo launchInfo) {
            this.launchInfo = launchInfo;
            return this;
        }

        /** 是否在注册时自动上报启动命令（默认 true）。 */
        public Builder autoReportLaunch(boolean autoReportLaunch) {
            this.autoReportLaunch = autoReportLaunch;
            return this;
        }

        public AgentQuayClient build() {
            if (appId == null || !ToolMetadata.isValidAppId(appId)) {
                throw new IllegalArgumentException(
                        "appId 不符合规范 [a-z0-9-]{1,48}（禁止 _ 和 .）: " + appId);
            }
            if (appName == null || appName.isEmpty()) {
                throw new IllegalArgumentException("appName 不能为空");
            }
            return new AgentQuayClient(this);
        }
    }
}
