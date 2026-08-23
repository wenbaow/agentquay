using System.Reflection;
using System.Text.Json.Nodes;
using AgentQuay;
using Xunit;

namespace AgentQuay.Tests;

/// <summary>
/// 页面智能路由单测（方案 §9 Phase 1 验收）：单飞去重、GC/注销后重建、激活超时、
/// 无工厂报错、激活钩子执行一次、UI 线程调度、静态方法免实例。
/// </summary>
public class PageRouterTests
{
    /// <summary>测试页面：记录创建次数，实例带唯一 Id（判定"同一个"）。</summary>
    private sealed class Page
    {
        public static int CreatedCount;
        public Guid Id { get; } = Guid.NewGuid();

        public Page() => Interlocked.Increment(ref CreatedCount);

        [AgentTool("probe", Description = "探针")]
        public string Probe() => "ok";
    }

    private static ToolBinding MakeBinding(string? pageKey, Func<object>? factory,
                                           object? instance = null, MethodInfo? method = null)
    {
        method ??= typeof(Page).GetMethod(nameof(Page.Probe))!;
        var metadata = new ToolMetadata("probe", "probe", new JsonObject(),
            false, 30, 120, null, method);
        return new ToolBinding(metadata, pageKey, instance, factory);
    }

    /// <summary>重置共享创建计数（xUnit 同类内测试顺序执行，静态状态跨测试残留）。</summary>
    private static void ResetCount() => Page.CreatedCount = 0;

    /// <summary>不经 ConnectAsync 直接构造客户端（反射私有构造；页面路由单测不需要真实连接）。</summary>
    private static AgentQuayClient NewClient()
    {
        var ctor = typeof(AgentQuayClient).GetConstructor(
            BindingFlags.Instance | BindingFlags.NonPublic, null,
            new[]
            {
                typeof(string), typeof(string), typeof(string), typeof(int), typeof(bool),
                typeof(string), typeof(string), typeof(int), typeof(int),
                typeof(ConfirmationHandler), typeof(LaunchInfo), typeof(ToolCallHandler),
            }, null)!;
        return (AgentQuayClient)ctor.Invoke(new object?[]
        {
            "test-app", "Test App", "127.0.0.1", 0, false,
            "1.0.0", "1.0", 30, 30, null, null, null,
        });
    }

    /// <summary>完全可控的 UI 线程调度器：把动作投递到独立线程执行并等待完成。</summary>
    private sealed class FakeDispatcher : IUIThreadDispatcher
    {
        public int Dispatched;

        public Task<T> DispatchAsync<T>(Func<Task<T>> action)
        {
            Interlocked.Increment(ref Dispatched);
            var tcs = new TaskCompletionSource<T>(TaskCreationOptions.RunContinuationsAsynchronously);
            _ = Task.Run(async () =>
            {
                try
                {
                    tcs.SetResult(await action());
                }
                catch (Exception e)
                {
                    tcs.SetException(e);
                }
            });
            return tcs.Task;
        }
    }

    // ------------------------------------------------------------------
    // 单飞：并发调用合并等待同一个创建任务，只创建一个实例
    // ------------------------------------------------------------------

    [Fact]
    public async Task ConcurrentActivations_ShareSingleInstance()
    {
        ResetCount();
        var router = new PageRouter();
        // 工厂带闸门：建模真实的"创建/导航窗口"，保证并发调用落在同一创建任务上。
        // 并发发起必须并行（Task.Run），串行循环会让后一个调用落在已完成任务上（属正常的重建语义）
        var gate = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var binding = MakeBinding("SearchPage", () =>
        {
            gate.Task.Wait(); // 只阻塞 threadpool 线程；真实创建窗口由 Task.Run 扇出保证并发
            return new Page();
        });

        var tasks = Enumerable.Range(0, 10)
            .Select(_ => Task.Run(() => router.GetOrCreateAsync(binding, 15, CancellationToken.None)));
        await Task.Delay(150); // 等待全部调用进入合并等待
        gate.SetResult(true);
        var pages = await Task.WhenAll(tasks);

        Assert.Single(pages.DistinctBy(p => ((Page)p).Id));
        Assert.Equal(1, Page.CreatedCount);
    }

    // 客户端分发层：页面创建完成后弱引用存活，后续并发调用走"实例存活直接调"，不重复创建
    [Fact]
    public async Task ConcurrentDispatchAfterCreate_ReusesLiveInstance()
    {
        ResetCount();
        await using var client = NewClient();
        client.RegisterTools<Page>(pageKey: "SearchPage");
        var binding = client._tools.Single(t => t.PageKey == "SearchPage");

        // 第 1 次：创建页面。应用侧持有页面强引用（模拟页面在导航栈中）——
        // 弱引用只用于"页面已关闭"的追踪，不承载存活
        await client.DispatchAsync(binding, null, CancellationToken.None);
        var page1 = binding.LiveTarget()!;
        Assert.Equal(1, Page.CreatedCount);
        Assert.NotNull(page1);

        // 随后并发调用：Live 存活 → 全部命中同一实例，不触发工厂
        var pages = await Task.WhenAll(Enumerable.Range(0, 10)
            .Select(_ => client.DispatchAsync(binding, null, CancellationToken.None)));
        Assert.All(pages, p => Assert.Equal("ok", p));
        Assert.Same(page1, binding.LiveTarget());
        GC.KeepAlive(page1);
        Assert.Equal(1, Page.CreatedCount);
    }

    // ------------------------------------------------------------------
    // 弱引用重建：Live 失效（UnregisterPage / GC）后工厂再次创建
    // ------------------------------------------------------------------

    [Fact]
    public async Task AfterLiveLost_FactoryRecreates()
    {
        ResetCount();
        var router = new PageRouter();
        var binding = MakeBinding("SearchPage", () => new Page());

        var first = (Page)await router.GetOrCreateAsync(binding, 15, CancellationToken.None);
        Assert.Equal(1, Page.CreatedCount);
        Assert.Same(first, binding.LiveTarget());

        binding.Live = null; // 模拟 UnregisterPage / 页面关闭后弱引用失效
        var second = (Page)await router.GetOrCreateAsync(binding, 15, CancellationToken.None);

        Assert.NotSame(first, second);
        Assert.Equal(2, Page.CreatedCount);
        Assert.Same(second, binding.LiveTarget());
    }

    // ------------------------------------------------------------------
    // 激活超时：创建/导航/等待整体计时，超时抛 PageActivationTimeoutException
    // ------------------------------------------------------------------

    [Fact]
    public async Task ActivationTimeout_ThrowsPageActivationTimeout()
    {
        ResetCount();
        var router = new PageRouter();
        // awaitReady 永不完成（模拟 Loaded 事件迟迟不来）
        router.SetActivator("SearchPage",
            navigate: null,
            awaitReady: _ => new TaskCompletionSource<bool>().Task);
        var binding = MakeBinding("SearchPage", () => new Page());

        await Assert.ThrowsAsync<PageActivationTimeoutException>(() =>
            router.GetOrCreateAsync(binding, activationTimeoutSeconds: 1, CancellationToken.None));
    }

    // ------------------------------------------------------------------
    // 无工厂：激活报 PageActivationException（工具仍在表内，错误明确透传）
    // ------------------------------------------------------------------

    [Fact]
    public async Task NoFactory_ActivationFails()
    {
        ResetCount();
        var router = new PageRouter();
        var binding = MakeBinding("SearchPage", factory: null);

        var ex = await Assert.ThrowsAsync<PageActivationException>(() =>
            router.GetOrCreateAsync(binding, 15, CancellationToken.None));
        Assert.Contains("无惰性工厂", ex.Message);
    }

    // ------------------------------------------------------------------
    // 激活钩子：单飞并发激活时钩子只执行一次（"激活只在首次调用执行一次"由
    // 客户端 DispatchAsync 的实例存活检查保证，路由层保证并发合并）
    // ------------------------------------------------------------------

    [Fact]
    public async Task ActivatorRuns_OnceUnderConcurrentCalls()
    {
        ResetCount();
        var router = new PageRouter();
        int navigated = 0;
        int ready = 0;
        router.SetActivator("SearchPage",
            page => Interlocked.Increment(ref navigated),
            async _ =>
            {
                Interlocked.Increment(ref ready);
                await Task.Yield();
            });
        var gate = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var binding = MakeBinding("SearchPage", () =>
        {
            gate.Task.Wait();
            return new Page();
        });

        var tasks = Enumerable.Range(0, 10)
            .Select(_ => Task.Run(() => router.GetOrCreateAsync(binding, 15, CancellationToken.None)));
        await Task.Delay(150);
        gate.SetResult(true);
        var pages = await Task.WhenAll(tasks);

        Assert.Equal(1, navigated);
        Assert.Equal(1, ready);
        Assert.Single(pages.DistinctBy(p => ((Page)p).Id));
        Assert.Equal(1, Page.CreatedCount);
    }

    // ------------------------------------------------------------------
    // UI 线程调度：激活在调度器上执行（WpfDispatcher 的替身验证）
    // ------------------------------------------------------------------

    [Fact]
    public async Task Activation_RunsOnDispatcher()
    {
        ResetCount();
        var dispatcher = new FakeDispatcher();
        var router = new PageRouter { Dispatcher = dispatcher };
        var binding = MakeBinding("SearchPage", () => new Page());

        await router.GetOrCreateAsync(binding, 15, CancellationToken.None);

        Assert.Equal(1, dispatcher.Dispatched);
    }

    // ------------------------------------------------------------------
    // 激活失败（工厂抛异常）→ PageActivationException（PAGE_ACTIVATION_FAILED）
    // ------------------------------------------------------------------

    [Fact]
    public async Task FactoryThrows_WrappedAsPageActivation()
    {
        ResetCount();
        var router = new PageRouter();
        var binding = MakeBinding("SearchPage",
            () => throw new InvalidOperationException("DI 容器不可用"));

        var ex = await Assert.ThrowsAsync<PageActivationException>(() =>
            router.GetOrCreateAsync(binding, 15, CancellationToken.None));
        Assert.Contains("DI 容器不可用", ex.Message);
    }

    // ------------------------------------------------------------------
    // 失败后允许重建：激活失败的任务已移除，下次调用重新创建
    // ------------------------------------------------------------------

    [Fact]
    public async Task AfterFailure_NextCallRecreates()
    {
        ResetCount();
        var router = new PageRouter();
        bool flaky = true;
        var binding = MakeBinding("SearchPage", () =>
        {
            if (flaky)
            {
                flaky = false;
                throw new InvalidOperationException("一次失败");
            }
            return new Page();
        });

        await Assert.ThrowsAsync<PageActivationException>(() =>
            router.GetOrCreateAsync(binding, 15, CancellationToken.None));

        var page = (Page)await router.GetOrCreateAsync(binding, 15, CancellationToken.None);
        Assert.NotNull(page);
        Assert.Same(page, binding.LiveTarget());
    }
}