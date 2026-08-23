using System.Windows;

namespace AgentQuay;

/// <summary>
/// WPF 页面就绪等待辅助（页面智能路由 §5.3）：事件驱动 + 可选的超时兜底，
/// 禁止轮询/阻塞。用于 <see cref="AgentQuayClient.SetPageActivator"/> 的 awaitReady 参数：
///
/// <example>
/// <code>
/// client.SetPageActivator("SearchPage",
///     navigate: page => MainWindow.NavigateTo((SearchPage)page),
///     awaitReady: async page => await PageLoadedAsync((FrameworkElement)page));
/// </code>
/// </example>
/// </summary>
public static class PageLoadedAsyncHelper
{
    /// <summary>
    /// 等待元素触发 Loaded 事件（已 Loaded 立即完成）。
    /// 未在 <paramref name="timeoutSeconds"/> 内 Loaded 时抛 <see cref="TimeoutException"/>。
    /// 默认 30s 大于 SDK 的页面激活超时（默认 15s）：挂到 SetPageActivator 的 awaitReady
    /// 时由 SDK 激活超时先兜底（PAGE_ACTIVATION_TIMEOUT），本超时仅作独立使用时的护栏。
    /// </summary>
    public static Task PageLoadedAsync(FrameworkElement element, int timeoutSeconds = 30)
    {
        if (element.IsLoaded)
        {
            return Task.CompletedTask;
        }
        var tcs = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        RoutedEventHandler handler = null!;
        handler = (_, _) =>
        {
            element.Loaded -= handler;
            tcs.TrySetResult();
        };
        element.Loaded += handler;
        if (timeoutSeconds > 0)
        {
            // 超时兜底：事件没来也释放订阅，避免泄漏；结果由 SDK 侧激活超时统一兜底
            _ = Task.Delay(TimeSpan.FromSeconds(timeoutSeconds)).ContinueWith(_ =>
            {
                element.Loaded -= handler;
                tcs.TrySetException(new TimeoutException(
                    $"页面在 {timeoutSeconds}s 内未触发 Loaded（pageKey 激活等待超时）"));
            }, TaskScheduler.Default);
        }
        return tcs.Task;
    }
}