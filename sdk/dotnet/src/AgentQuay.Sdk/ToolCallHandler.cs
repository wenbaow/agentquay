namespace AgentQuay;

/// <summary>
/// 工具调用钩子：在业务方法执行前触发，允许 UI 层拦截并响应。
/// </summary>
/// <param name="toolName">工具名</param>
/// <param name="arguments">调用参数（可为 null）</param>
public delegate void ToolCallHandler(string toolName, IReadOnlyDictionary<string, object?>? arguments);
