// agentquay/ConfirmationHandler.h — 确认流程回调（设计文档 §3.3、§6.3）。
//
// 危险操作（requiresConfirmation=true）被 Agent 调用时，Bridge 向应用发送
// confirm 消息；SDK 调用确认回调决定是否放行，再通过 confirm_result 上报。
//
// 默认实现：QtWidgets 可用时弹 OS 原生观感的 QMessageBox（超时自动取消）；
// 无图形环境（QCoreApplication / headless）回退控制台输入。
#pragma once

#include <QString>
#include <QVariantMap>
#include <functional>

namespace agentquay {

/**
 * 询问用户是否确认执行。
 *
 * @param message        确认文案
 * @param arguments      调用参数（展示用，可能为空）
 * @param timeoutSeconds 确认超时秒数（超时视为取消；Bridge 侧同样计时）
 * @return true=确认 / false=取消
 */
using ConfirmHandler = std::function<bool(const QString& message,
                                          const QVariantMap& arguments,
                                          int timeoutSeconds)>;

/** 默认确认回调（应用可覆盖 AgentQuayClient::setConfirmHandler）。 */
ConfirmHandler defaultConfirmHandler();

} // namespace agentquay