package com.agentquay;

import java.lang.ref.WeakReference;
import java.lang.reflect.Method;
import java.util.concurrent.CompletableFuture;
import java.util.function.Consumer;
import java.util.function.Supplier;

/**
 * 一个工具绑定的完整信息（页面智能路由 §4.1）：元数据静态上报、实例绑定惰性化。
 * 替代原 {@link ToolMetadata#getTarget()} 直接存实例的方式——实例弱引用，GC 后回到可重建状态。
 */
final class ToolBinding {

    /** 元数据（name/schema/confirm/timeout，注册上报用；不依赖页面存活）。 */
    final ToolMetadata metadata;

    /** 页面分组标签（页面智能路由：仅 SDK 内部路由用，不进协议）。 */
    final String pageKey;

    /** 立即绑定实例（已开页面，原行为；强引用，页面生命周期由应用侧管理）。 */
    final Object instance;

    /** 惰性工厂（页面未打开时首次调用创建）。 */
    final Supplier<Object> factory;

    /** 惰性激活后的弱引用实例。 */
    volatile WeakReference<Object> live;

    ToolBinding(ToolMetadata metadata, String pageKey, Object instance, Supplier<Object> factory) {
        this.metadata = metadata;
        this.pageKey = pageKey;
        this.instance = instance;
        this.factory = factory;
    }

    String name() {
        return metadata.getName();
    }

    Method method() {
        return metadata.getMethod();
    }

    /** 当前可用目标：立即绑定实例，或弱引用存活实例（均无则 null）。 */
    Object liveTarget() {
        WeakReference<Object> ref = live;
        return ref == null ? null : ref.get();
    }
}

/**
 * UI 线程调度抽象（页面智能路由 §5.2）：核心包只定义接口、不引用任何 UI 框架。
 * Swing 场景典型实现：把 action 经 SwingUtilities.invokeAndWait 发布到 EDT 并返回结果。
 */
interface UIThreadDispatcher {
    /** 在 UI 线程上执行 action 并返回结果（阻塞等待；实现须确保不冻结 UI 线程）。 */
    <T> T dispatch(java.util.concurrent.Callable<T> action) throws Exception;
}

/**
 * 页面激活钩子（页面智能路由 §3.2）：可选的自定义"导航 + 等待就绪"，只在首次调用执行一次。
 */
final class PageActivator {
    final Consumer<Object> navigate;
    final java.util.function.Function<Object, CompletableFuture<?>> awaitReady;

    PageActivator(Consumer<Object> navigate,
                  java.util.function.Function<Object, CompletableFuture<?>> awaitReady) {
        this.navigate = navigate;
        this.awaitReady = awaitReady;
    }
}