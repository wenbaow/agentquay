using System.Collections.Concurrent;
using System.Reflection;

namespace AgentQuay;

/// <summary>
/// UI 线程调度抽象（页面智能路由 §5.2）：核心包只定义接口、不引用任何 UI 框架。
/// 无 UI / 控制台场景不设置调度器，惰性创建与调用直接执行（线程池）。
/// WPF / WinUI / Avalonia 各提供扩展包实现（如 <c>AgentQuay.Sdk.Wpf</c> 的 WpfDispatcher）。
/// 实现必须：发布到 UI 线程执行；异步期间让出线程，不得阻塞 UI 线程。
/// </summary>
public interface IUIThreadDispatcher
{
    /// <summary>在 UI 线程上执行 <paramref name="action"/> 并返回结果（async 期间让出线程）。</summary>
    Task<T> DispatchAsync<T>(Func<Task<T>> action);
}

/// <summary>页面激活钩子：可选的自定义"导航 + 等待就绪"，只在首次调用、绑定之后执行一次。</summary>
internal sealed class PageActivator
{
    public Action<object>? Navigate { get; }
    public Func<object, Task>? AwaitReady { get; }

    public PageActivator(Action<object>? navigate, Func<object, Task>? awaitReady)
    {
        Navigate = navigate;
        AwaitReady = awaitReady;
    }
}

/// <summary>
/// 一个工具绑定的完整信息（页面智能路由 §4.1）：元数据静态上报、实例绑定惰性化。
/// 替代原 <see cref="ToolMetadata.Target"/> 直接存实例的方式——实例弱引用，GC 后回到可重建状态。
/// </summary>
internal sealed class ToolBinding
{
    /// <summary>元数据（name/schema/confirm/timeout，注册上报用；不依赖页面存活）。</summary>
    public ToolMetadata Metadata { get; }

    /// <summary>可选分组标签（仅 SDK 内部路由用，不进协议）。</summary>
    public string? PageKey { get; }

    /// <summary>立即绑定（已开页面，原行为；强引用，页面生命周期由应用侧管理）。</summary>
    public object? Instance { get; }

    /// <summary>惰性工厂（页面未打开时首次调用创建）。</summary>
    public Func<object>? Factory { get; }

    /// <summary>惰性激活后的弱引用实例。</summary>
    public WeakReference<object>? Live { get; set; }

    public MethodInfo Method => Metadata.Method;

    public string Name => Metadata.Name;

    public ToolBinding(ToolMetadata metadata, string? pageKey, object? instance, Func<object>? factory)
    {
        Metadata = metadata;
        PageKey = pageKey;
        Instance = instance;
        Factory = factory;
    }

    /// <summary>当前可用目标：立即绑定实例，或弱引用存活实例（均无则 null）。</summary>
    public object? LiveTarget() => Live != null && Live.TryGetTarget(out var t) ? t : null;
}

/// <summary>
/// 页面路由表（页面智能路由 §4.2）：pageKey → 创建中的任务（单飞去重）。
/// 并发调用合并等待同一个创建任务，不会建出两个页面；创建/导航/等待整体带超时，
/// 任务完成（成功或失败）后回 NotLoaded，允许下次调用重建。
///
/// 并发保障：每 key 一把锁（<see cref="_gates"/>），"查-建-换"与"完成移除"都在锁内，
/// 保证同一时刻每个 key 至多一个运行中的创建任务。
/// </summary>
internal sealed class PageRouter
{
    private readonly ConcurrentDictionary<string, object> _gates = new();
    private readonly ConcurrentDictionary<string, Task<object>> _inflight = new();
    private readonly ConcurrentDictionary<string, PageActivator> _activators = new();

    /// <summary>UI 线程调度器（可运行期设置；null = 直接执行，无 UI 场景）。</summary>
    public IUIThreadDispatcher? Dispatcher { get; set; }

    /// <summary>注册激活钩子（navigate 在 UI 线程执行；awaitReady 必须异步等待，禁止阻塞）。</summary>
    public void SetActivator(string pageKey, Action<object>? navigate, Func<object, Task>? awaitReady)
    {
        _activators[pageKey] = new PageActivator(navigate, awaitReady);
    }

    /// <summary>取 pageKey 对应的激活钩子（无则 null）。</summary>
    public PageActivator? Activator(string pageKey)
        => _activators.TryGetValue(pageKey, out var act) ? act : null;

    /// <summary>移除 pageKey 的激活钩子（UnregisterPage 时清理）。</summary>
    public void RemoveActivator(string pageKey)
    {
        _activators.TryRemove(pageKey, out _);
    }

    /// <summary>
    /// 获取或创建 pageKey（或工具名）对应的页面实例：单飞去重 + 激活超时。
    /// 运行中的任务合并等待（不会建出两个页面）；任务已完成（成功或失败）时替换为新任务
    /// ——即"回 NotLoaded，下次调用可重建"（惰性替换，无后台移除，无竞态）。
    /// 超时抛 <see cref="PageActivationTimeoutException"/> 并让出单飞槽位：底层激活任务
    /// 继续执行（与"页面已导航但调用超时"的孤儿机制一致），下次调用可重建。
    /// </summary>
    public async Task<object> GetOrCreateAsync(ToolBinding binding, int activationTimeoutSeconds, CancellationToken ct)
    {
        var key = binding.PageKey ?? binding.Name;
        var gate = _gates.GetOrAdd(key, static _ => new object());

        Task<object>? task;
        lock (gate)
        {
            if (!_inflight.TryGetValue(key, out task) || task.IsCompleted)
            {
                task = ActivateCoreAsync(binding, ct);
                _inflight[key] = task;
            }
        }

        using var timeoutCts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeoutCts.CancelAfter(TimeSpan.FromSeconds(Math.Max(1, activationTimeoutSeconds)));
        try
        {
            return await task.WaitAsync(timeoutCts.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (timeoutCts.IsCancellationRequested && !ct.IsCancellationRequested)
        {
            lock (gate)
            {
                if (_inflight.TryGetValue(key, out var current) && ReferenceEquals(current, task))
                {
                    _inflight.TryRemove(key, out _);
                }
            }
            throw new PageActivationTimeoutException(
                $"页面激活超时（>{activationTimeoutSeconds}s，pageKey={key}）");
        }
    }

    /// <summary>激活：工厂创建 → 可选导航 → 等待就绪。UI 线程执行，全程异步等待让出。
    /// 成功后先设置弱引用再返回（调用方拿到实例时 Live 必已就绪）。
    /// 首步 Task.Yield：确保任务以 pending 状态进入路由表后锁才释放——若激活在锁内同步
    /// 完成，后续并发调用都会落在"已完成"条目上逐个重建，单飞退化为串行重建。</summary>
    private async Task<object> ActivateCoreAsync(ToolBinding binding, CancellationToken ct)
    {
        await Task.Yield(); // YieldAwaitable 无 ConfigureAwait，语义一致
        var activate = async () =>
        {
            var factory = binding.Factory
                ?? throw new PageActivationException($"页面 '{binding.PageKey}' 无惰性工厂，无法激活");
            var instance = factory();
            if (instance == null)
            {
                throw new PageActivationException($"页面工厂返回 null（pageKey={binding.PageKey}）");
            }
            if (binding.PageKey != null && _activators.TryGetValue(binding.PageKey, out var act))
            {
                act.Navigate?.Invoke(instance);
                if (act.AwaitReady != null)
                {
                    await act.AwaitReady(instance).ConfigureAwait(false);
                }
            }
            return instance;
        };

        try
        {
            var page = Dispatcher != null
                ? await Dispatcher.DispatchAsync(activate).ConfigureAwait(false)
                : await activate().ConfigureAwait(false);
            binding.Live = new WeakReference<object>(page);
            return page;
        }
        catch (OperationCanceledException) when (ct.IsCancellationRequested)
        {
            throw;
        }
        catch (PageActivationException)
        {
            throw;
        }
        catch (Exception e)
        {
            throw new PageActivationException(
                $"页面激活失败（pageKey={binding.PageKey}）: {RootMessage(e).Message}", e);
        }
    }

    private static Exception RootMessage(Exception e)
    {
        Exception cause = e;
        while (cause.InnerException != null && cause.InnerException != cause)
        {
            cause = cause.InnerException;
        }
        return cause;
    }
}