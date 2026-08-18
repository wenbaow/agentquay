package com.agentquay;

import java.util.Map;

/**
 * 确认流程回调（设计文档 §3.3）：危险操作（requiresConfirmation=true）被调用时，
 * Bridge 向应用发送 confirm 消息，SDK 调用此回调决定是否放行。
 *
 * <p>默认实现为 Swing 原生对话框（{@link ConfirmDialog}）；无图形环境时回退控制台输入。
 */
@FunctionalInterface
public interface ConfirmationHandler {

    /**
     * 询问用户是否确认执行。
     *
     * @param message        确认文案
     * @param arguments      调用参数（展示用，可能为 null）
     * @param timeoutSeconds 确认超时秒数（超时视为取消；Bridge 侧同样计时）
     * @return true=确认 / false=取消
     */
    boolean confirm(String message, Map<String, Object> arguments, int timeoutSeconds);
}
