namespace AgentQuay;

/// <summary>
/// 确认流程回调（设计文档 §3.3）：危险操作（[AgentTool(RequiresConfirmation = true)]）被调用时，
/// Bridge 向应用发送 confirm 消息，SDK 调用此回调决定是否放行。
/// </summary>
/// <param name="message">确认文案。</param>
/// <param name="arguments">调用参数（展示用，可能为 null）。</param>
/// <param name="timeoutSeconds">确认超时秒数（超时视为取消；Bridge 侧同样计时）。</param>
/// <returns>true=确认 / false=取消。</returns>
public delegate bool ConfirmationHandler(
    string message,
    IReadOnlyDictionary<string, object?>? arguments,
    int timeoutSeconds);