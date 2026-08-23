package com.agentquay;

import com.agentquay.internal.Protocol;
import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.Collectors;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 页面智能路由单测（方案 §9 Phase 1 验收，对齐 C# PageRouterTests / Python test_page_router.py）：
 * 惰性注册元数据立即可见、单飞去重、弱引用重建、激活超时、无工厂报错、
 * 激活钩子执行一次、UI 线程调度。
 */
public class PageRouterTest {

    /** 测试页面：记录创建次数（静态共享，测试前重置），实例带唯一 seq。 */
    static class Page {
        static final AtomicInteger CREATED = new AtomicInteger();
        final int seq;

        Page() {
            this.seq = CREATED.incrementAndGet();
        }

        @AgentTool(value = "search", description = "搜索音乐")
        public CompletableFuture<Map<String, Object>> search(String keyword) {
            return CompletableFuture.completedFuture(Map.of("page", seq, "keyword", keyword));
        }

        @AgentTool(value = "ping", description = "同步探针")
        public int ping() {
            return seq;
        }
    }

    private static JsonNode argsObject(String key, String value) {
        return Protocol.MAPPER.createObjectNode().put(key, value);
    }

    private static AgentQuayClient newClient() {
        return AgentQuayClient.builder()
                .appId("music-app")
                .appName("Music Player")
                .autoSpawnBridge(false)
                .build();
    }

    @Test
    void lazyRegistration_metadataVisible_instanceNotCreated() {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        client.registerTools(Page.class, "SearchPage");
        assertEquals(List.of("ping", "search"),
                client.listTools().stream().sorted().collect(Collectors.toList()));
        assertEquals(0, Page.CREATED.get()); // 未创建实例
        ToolBinding binding = client.findTool("search");
        assertEquals("SearchPage", binding.pageKey);
        assertNull(binding.instance);
        assertNull(binding.live);
        assertNotNull(binding.factory);
    }

    @Test
    void firstCallCreates_thenReuses() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        client.registerTools(Page.class, "SearchPage");
        ToolBinding binding = client.findTool("search");

        Object r1 = client.invokeSync(binding, argsObject("keyword", "x"));
        assertEquals(1, Page.CREATED.get());
        assertNotNull(binding.liveTarget());
        int page1 = ((Number) ((Map<?, ?>) r1).get("page")).intValue();

        Object r2 = client.invokeSync(binding, argsObject("keyword", "y"));
        assertEquals(1, Page.CREATED.get()); // 弱引用存活 → 复用
        assertEquals(page1, ((Number) ((Map<?, ?>) r2).get("page")).intValue());
    }

    @Test
    void unregisterPage_thenRecreate() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        client.registerTools(Page.class, "SearchPage");
        ToolBinding binding = client.findTool("search");

        client.invokeSync(binding, argsObject("keyword", "x"));
        assertEquals(1, Page.CREATED.get());

        client.unregisterPage("SearchPage");
        assertNull(binding.live);
        client.invokeSync(binding, argsObject("keyword", "y"));
        assertEquals(2, Page.CREATED.get()); // 重建
    }

    @Test
    void concurrentCalls_singleFlight() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        client.registerTools(Page.class, "SearchPage");
        ToolBinding binding = client.findTool("search");

        // 激活窗口：awaitReady 挂起在闸门上，并发调用合并等待同一任务
        CountDownLatch release = new CountDownLatch(1);
        client.setPageActivator("SearchPage", null, page -> {
            try {
                return release.await(30, TimeUnit.SECONDS)
                        ? CompletableFuture.completedFuture(null)
                        : CompletableFuture.failedFuture(new RuntimeException("超时"));
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return CompletableFuture.failedFuture(e);
            }
        });

        int n = 10;
        CountDownLatch start = new CountDownLatch(n);
        List<Throwable> errors = new ArrayList<>();
        AtomicInteger ok = new AtomicInteger();
        List<Integer> pages = java.util.Collections.synchronizedList(new ArrayList<>());
        for (int i = 0; i < n; i++) {
            Thread t = new Thread(() -> {
                start.countDown();
                try {
                    start.await();
                    Object r = client.invokeSync(binding, argsObject("keyword", "k"));
                    pages.add(((Number) ((Map<?, ?>) r).get("page")).intValue());
                    ok.incrementAndGet();
                } catch (Throwable e) {
                    errors.add(e);
                }
            });
            t.setDaemon(true);
            t.start();
        }
        assertTrue(start.await(30, TimeUnit.SECONDS), "并发线程未就绪");
        Thread.sleep(200); // 等待全部进入合并等待
        release.countDown();
        for (int i = 0; i < n; i++) {
            Thread.sleep(100);
        }
        assertTrue(errors.isEmpty(), "并发调用异常: " + errors);
        assertEquals(n, ok.get());
        assertEquals(1, new java.util.HashSet<>(pages).size()); // 只创建一个实例
        assertEquals(1, Page.CREATED.get());
    }

    @Test
    void activationTimeout_throwsPageActivationTimeout() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        client.setPageActivationTimeoutSeconds(1);
        client.registerTools(Page.class, "SearchPage");
        ToolBinding binding = client.findTool("search");
        // awaitReady 永不完成
        client.setPageActivator("SearchPage", null,
                page -> new CompletableFuture<>());

        assertThrows(AgentQuayException.PageActivationTimeoutException.class,
                () -> client.invokeSync(binding, argsObject("keyword", "x")));
    }

    @Test
    void activationFailure_thenRebuild() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        final boolean[] flaky = {true};
        client.registerToolsFactory(Page.class, () -> {
            if (flaky[0]) {
                flaky[0] = false;
                throw new IllegalStateException("DI 容器不可用");
            }
            return new Page();
        }, "SearchPage");
        ToolBinding binding = client.findTool("search");

        assertThrows(AgentQuayException.PageActivationException.class,
                () -> client.invokeSync(binding, argsObject("keyword", "x")));

        Object r = client.invokeSync(binding, argsObject("keyword", "y")); // 失败后可重建
        assertEquals(1, ((Number) ((Map<?, ?>) r).get("page")).intValue());
    }

    @Test
    void noFactory_raisesPageNotFound() throws Exception {
        AgentQuayClient client = newClient();
        // 直接构造"无实例无工厂"的绑定（页面未打开且未配工厂）
        ToolMetadata metadata = new ToolMetadata("search", "搜索音乐",
                java.util.Collections.emptyMap(), false, 30, 120, null,
                Page.class.getMethods()[0]); // 仅用于路由测试，方法本身不执行
        ToolBinding binding = new ToolBinding(metadata, "SearchPage", null, null);
        client.findTool("search"); // no-op 保引用
        assertNull(binding.factory);

        // 注入到客户端工具表：tools 私有，直接调 invokeSync 验证错误语义
        AgentQuayException.PageNotFoundException ex = assertThrows(
                AgentQuayException.PageNotFoundException.class,
                () -> client.invokeSync(binding, argsObject("keyword", "x")));
        assertTrue(ex.getMessage().contains("SearchPage"));
    }

    @Test
    void activatorRuns_onceUnderConcurrentCalls() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        AtomicInteger navigated = new AtomicInteger();
        AtomicInteger ready = new AtomicInteger();
        client.registerTools(Page.class, "SearchPage");
        // 并发窗口：awaitReady 挂闸门
        CountDownLatch release = new CountDownLatch(1);
        client.setPageActivator("SearchPage",
                page -> navigated.incrementAndGet(),
                page -> {
                    ready.incrementAndGet();
                    try {
                        return release.await(30, TimeUnit.SECONDS)
                                ? CompletableFuture.completedFuture(null)
                                : CompletableFuture.failedFuture(new RuntimeException());
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                        return CompletableFuture.failedFuture(e);
                    }
                });
        ToolBinding binding = client.findTool("search");

        int n = 8;
        CountDownLatch start = new CountDownLatch(n);
        List<Throwable> errors = java.util.Collections.synchronizedList(new ArrayList<>());
        for (int i = 0; i < n; i++) {
            Thread t = new Thread(() -> {
                start.countDown();
                try {
                    start.await();
                    client.invokeSync(binding, argsObject("keyword", "k"));
                } catch (Throwable e) {
                    errors.add(e);
                }
            });
            t.setDaemon(true);
            t.start();
        }
        assertTrue(start.await(30, TimeUnit.SECONDS), "并发线程未就绪");
        Thread.sleep(200);
        release.countDown();
        for (int i = 0; i < n; i++) {
            Thread.sleep(100);
        }
        assertTrue(errors.isEmpty(), "并发调用异常: " + errors);
        assertEquals(1, navigated.get()); // 导航只执行一次
        assertEquals(1, ready.get());
        assertEquals(1, Page.CREATED.get());
    }

    @Test
    void uiDispatcher_receivesPageActivations() throws Exception {
        Page.CREATED.set(0);
        AgentQuayClient client = newClient();
        long[] uiThreadId = {0};
        // 泛型 SAM 接口不能用 lambda 实现，用匿名类
        client.setUIThreadDispatcher(new UIThreadDispatcher() {
            @Override
            public <T> T dispatch(java.util.concurrent.Callable<T> action) throws Exception {
                uiThreadId[0] = Thread.currentThread().getId();
                return action.call();
            }
        });
        client.registerTools(Page.class, "SearchPage");
        ToolBinding binding = client.findTool("search");

        client.invokeSync(binding, argsObject("keyword", "x"));

        assertTrue(uiThreadId[0] != 0, "页面激活应经 UI 调度器执行");
    }

    @Test
    void registerPayload_includesPageKey() {
        AgentQuayClient client = newClient();
        client.registerTools(Page.class, "SearchPage");
        // 注册消息由 register() 组装（需真实连接），此处验证注册路径的 pageKey 不丢：
        // 工具表内绑定携带 pageKey（协议消息由 e2e 覆盖）
        assertNotNull(client.findTool("search").pageKey);
        assertFalse(client.findTool("ping").pageKey.isEmpty());
    }
}