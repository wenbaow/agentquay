// agentquay/AgentQuayException.h — AgentQuay SDK 异常类型（对齐 Java SDK AgentQuayException）。
#pragma once

#include <QString>
#include <stdexcept>

namespace agentquay {

/** AgentQuay SDK 异常基类。 */
class AgentQuayException : public std::runtime_error {
public:
    explicit AgentQuayException(const QString& message)
        : std::runtime_error(message.toStdString()) {}
};

/** 本地未检测到运行中的 Bridge，且无法自动拉起。 */
class BridgeUnavailableException : public AgentQuayException {
public:
    explicit BridgeUnavailableException(const QString& message)
        : AgentQuayException(message) {}
};

/** 自动拉起内嵌 Bridge 失败（端口被占、无权限、启动超时等）。 */
class BridgeSpawnException : public BridgeUnavailableException {
public:
    explicit BridgeSpawnException(const QString& message)
        : BridgeUnavailableException(message) {}
};

/** 与 Bridge 的 WebSocket 连接断开或失败。 */
class ConnectionException : public AgentQuayException {
public:
    explicit ConnectionException(const QString& message)
        : AgentQuayException(message) {}
};

/** 注册被 Bridge 拒绝（协议不兼容 / appId 非法 / tool 非法）。 */
class RegistrationException : public AgentQuayException {
public:
    RegistrationException(const QString& code, const QString& message)
        : AgentQuayException(QStringLiteral("注册被拒绝 [%1]: %2").arg(code, message))
        , m_code(code) {}

    QString code() const { return m_code; }

private:
    QString m_code;
};

/** 认证失败：authToken 与 Bridge 钉扎的 token 不匹配。 */
class AuthFailedException : public RegistrationException {
public:
    explicit AuthFailedException(const QString& message)
        : RegistrationException(QStringLiteral("AUTH_FAILED"), message) {}
};

/** 本连接被同 appId 的新实例替换（Bridge 关闭了本连接，不再重连）。 */
class ReplacedException : public AgentQuayException {
public:
    ReplacedException()
        : AgentQuayException(QStringLiteral("本连接已被同 appId 的新实例替换")) {}
};

/** 协议消息无法解析或非法。 */
class ProtocolException : public AgentQuayException {
public:
    explicit ProtocolException(const QString& message)
        : AgentQuayException(message) {}
};

/** Tool 调用返回业务错误（错误透传；what() = 纯消息，随 result.error.message 上报）。 */
class ToolCallException : public AgentQuayException {
public:
    ToolCallException(const QString& code, const QString& message)
        : AgentQuayException(message)
        , m_code(code) {}

    QString code() const { return m_code; }

private:
    QString m_code;
};

} // namespace agentquay