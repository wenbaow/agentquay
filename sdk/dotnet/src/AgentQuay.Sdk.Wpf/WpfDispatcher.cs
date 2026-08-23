using System.Windows;
using System.Windows.Threading;

namespace AgentQuay;

/// <summary>
/// WPF 的 UI 线程调度器（页面智能路由 §5.3）：页面工具创建与调用全部发布到 UI 线程，
/// async 期间让出线程，UI 照常响应（动画不冻结）。
///
/// <para>实现要点：用 <c>BeginInvoke + TaskCompletionSource</c> 绕开
/// <see cref="Dispatcher"/> 对 <c>Func&lt;Task&gt;</c> 重载的差异——BeginInvoke 一律
/// 以 async void 形式处理，异常透传到 <see cref="TaskCompletionSource{TResult}"/>。</para>
///
/// <example>
/// <code>
/// client.SetUIThreadDispatcher(new WpfDispatcher());
/// </code>
/// </example>
/// </summary>
public sealed class WpfDispatcher : IUIThreadDispatcher
{
    /// <summary>WPF 应用的主线程调度器（无 Application 时回退到任意 Dispatcher.CurrentDispatcher）。</summary>
    private readonly Dispatcher _dispatcher;

    public WpfDispatcher()
        : this(Application.Current?.Dispatcher ?? Dispatcher.CurrentDispatcher)
    {
    }

    public WpfDispatcher(Dispatcher dispatcher)
    {
        _dispatcher = dispatcher ?? throw new ArgumentNullException(nameof(dispatcher));
    }

    public Task<T> DispatchAsync<T>(Func<Task<T>> action)
    {
        var tcs = new TaskCompletionSource<T>(TaskCreationOptions.RunContinuationsAsynchronously);
        _dispatcher.BeginInvoke(async () =>
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