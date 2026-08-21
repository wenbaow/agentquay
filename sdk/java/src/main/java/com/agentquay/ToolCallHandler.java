package com.agentquay;

/**
 * 工具调用钩子接口：在业务方法执行前触发，允许 UI 层拦截并响应。
 *
 * <p>典型用法：
 * <pre>{@code
 * client.builder()
 *     .toolCallHandler((toolName, arguments) -> {
 *         if ("navigate_page".equals(toolName)) {
 *             String category = (String) arguments.get("category");
 *             Platform.runLater(() -> ui.navigate(category, arguments));
 *         }
 *     })
 *     .build();
 * }</pre>
 */
@FunctionalInterface
public interface ToolCallHandler {
    /**
     * 工具调用前的回调。
     *
     * @param toolName   工具名
     * @param arguments  调用参数（可为 null）
     */
    void onToolCall(String toolName, java.util.Map<String, Object> arguments);
}
