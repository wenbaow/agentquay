// agentquay/Protocol.h — Bridge ↔ 桌面应用的 WebSocket 消息格式
// （设计文档 §3.1、附录 A）。消息外壳：{ "type": "...", "payload": {...} }。
#pragma once

#include <QByteArray>
#include <QJsonArray>
#include <QJsonObject>
#include <QJsonValue>
#include <QString>
#include <QStringList>

namespace agentquay {

// 消息类型（附录 A）。
inline constexpr char kMsgRegister[]       = "register";        // 应用 → Bridge：注册应用及 Tool 列表
inline constexpr char kMsgRegisterAck[]    = "register_ack";    // Bridge → 应用：注册成功（首次注册含分配的 token）
inline constexpr char kMsgRegisterError[]  = "register_error";  // Bridge → 应用：注册失败
inline constexpr char kMsgInvoke[]         = "invoke";          // Bridge → 应用：调用指定 Tool
inline constexpr char kMsgResult[]         = "result";          // 应用 → Bridge：Tool 执行结果
inline constexpr char kMsgConfirm[]        = "confirm";         // Bridge → 应用：请求用户确认（危险操作）
inline constexpr char kMsgConfirmResult[]  = "confirm_result";  // 应用 → Bridge：用户确认/取消结果
inline constexpr char kMsgPing[]           = "ping";            // 双向心跳
inline constexpr char kMsgPong[]           = "pong";            // 双向心跳响应
inline constexpr char kMsgDisconnect[]     = "disconnect";      // 双向优雅断开通知
inline constexpr char kMsgNotification[]   = "notification";    // Bridge → 应用：广播通知

// 断开原因（disconnect payload.reason）。
inline constexpr char kDisconnectNormal[]   = "normal";     // 正常关闭
inline constexpr char kDisconnectReplaced[] = "replaced";   // 被同 appId 的新实例替换
inline constexpr char kDisconnectShutdown[] = "shutdown";   // Bridge 即将停止
inline constexpr char kDisconnectMigrate[]  = "migrate";    // 让位：应用重连到端口文件指向的更强实例

// 注册错误码（register_error payload.code）。
inline constexpr char kErrCodeBadRequest[]         = "BAD_REQUEST";
inline constexpr char kErrCodeAuthFailed[]         = "AUTH_FAILED";
inline constexpr char kErrCodeUnsupportedVersion[] = "UNSUPPORTED_VERSION";
inline constexpr char kErrCodeInvalidAppId[]       = "INVALID_APP_ID";
inline constexpr char kErrCodeInvalidTool[]        = "INVALID_TOOL";

// 工具执行错误码（SDK 侧产生，随 result.error.code 上报）。
inline constexpr char kInvokeErrCodeToolNotFound[]    = "TOOL_NOT_FOUND";
inline constexpr char kInvokeErrCodeExecutionTimeout[] = "EXECUTION_TIMEOUT";
inline constexpr char kInvokeErrCodeExecutionError[]  = "EXECUTION_ERROR";

/**
 * 序列化外壳消息：{ "type": type, "payload": payload }。
 * payload 为 null/undefined 时省略 payload 字段（对齐 Java 的 NON_NULL 策略）。
 */
QByteArray encode(const QString& type, const QJsonValue& payload = QJsonValue());

/** 解析外壳消息。返回 false 表示 JSON 非法或缺少 type 字段。 */
bool parse(const QByteArray& raw, QString* type, QJsonValue* payload);

/** 注册消息 payload（§3.1、§5.8）。tools 为 Tool 元数据数组；launch 可空（离线自动拉起用）。 */
QJsonObject registerPayload(const QString& appId, const QString& appName,
                            const QString& version, const QString& protocolVersion,
                            const QString& authToken, const QJsonArray& tools,
                            const QJsonObject& launch = QJsonObject());

/** 应用启动命令信息（§5.8），随 register 上报供 Bridge 离线自动拉起。 */
struct LaunchInfo {
    QString execPath;               // 可执行文件绝对路径（必填）
    QStringList args;               // 启动参数（argv 数组，禁止 shell 字符串）
    QString cwd;                    // 工作目录（可空）
    bool singleInstance = false;    // 单实例（已在运行时不再重复拉起）
    int launchTimeoutSeconds = 0;   // 覆盖全局启动等待超时（0 = 用全局默认）

    /** 序列化为注册 payload 的 launch 字段。 */
    QJsonObject toJson() const;
};

/**
 * 尽力探测本进程的启动命令（§5.8）：Qt 应用直接用 QCoreApplication::applicationFilePath()。
 * 打包/自定义启动器场景建议用 setLaunchInfo 显式指定。
 */
LaunchInfo detectLaunchInfo();

/** 心跳 payload（§3.1）。 */
QJsonObject pingPayload(qint64 timestampSeconds);

/** 执行结果 payload（应用 → Bridge）：{ requestId, success, data | error }。 */
QJsonObject resultPayload(const QString& requestId, bool success,
                          const QJsonValue& data, const QJsonObject& error);

/** 确认结果 payload（应用 → Bridge）：{ requestId, confirmed }。 */
QJsonObject confirmResultPayload(const QString& requestId, bool confirmed);

/** 构造业务错误对象：{ code, message }。 */
QJsonObject errorObject(const QString& code, const QString& message);

} // namespace agentquay