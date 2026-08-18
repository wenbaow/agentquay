// agentquay/Protocol.cpp — Bridge ↔ 桌面应用 WebSocket 消息编解码（设计文档 §3.1、附录 A）。
#include "agentquay/Protocol.h"

#include <QCoreApplication>
#include <QJsonDocument>

namespace agentquay {

QByteArray encode(const QString& type, const QJsonValue& payload)
{
    QJsonObject env;
    env.insert(QStringLiteral("type"), type);
    if (!payload.isUndefined() && !payload.isNull())
        env.insert(QStringLiteral("payload"), payload);
    return QJsonDocument(env).toJson(QJsonDocument::Compact);
}

bool parse(const QByteArray& raw, QString* type, QJsonValue* payload)
{
    QJsonParseError err;
    const QJsonDocument doc = QJsonDocument::fromJson(raw, &err);
    if (err.error != QJsonParseError::NoError || !doc.isObject())
        return false;
    const QJsonObject env = doc.object();
    if (!env.contains(QStringLiteral("type")))
        return false;
    if (type)
        *type = env.value(QStringLiteral("type")).toString();
    if (payload)
        *payload = env.value(QStringLiteral("payload"));
    return true;
}

QJsonObject registerPayload(const QString& appId, const QString& appName,
                            const QString& version, const QString& protocolVersion,
                            const QString& authToken, const QJsonArray& tools,
                            const QJsonObject& launch)
{
    QJsonObject p;
    p.insert(QStringLiteral("appId"), appId);
    p.insert(QStringLiteral("appName"), appName);
    p.insert(QStringLiteral("version"), version);
    p.insert(QStringLiteral("protocolVersion"), protocolVersion);
    p.insert(QStringLiteral("authToken"), authToken); // 首次注册可为空；重连必须携带
    p.insert(QStringLiteral("tools"), tools);
    if (!launch.isEmpty())
        p.insert(QStringLiteral("launch"), launch);
    return p;
}

QJsonObject LaunchInfo::toJson() const
{
    QJsonObject p;
    p.insert(QStringLiteral("execPath"), execPath);
    if (!args.isEmpty()) {
        QJsonArray arr;
        for (const QString& a : args)
            arr.append(a);
        p.insert(QStringLiteral("args"), arr);
    }
    if (!cwd.isEmpty())
        p.insert(QStringLiteral("cwd"), cwd);
    p.insert(QStringLiteral("singleInstance"), singleInstance);
    if (launchTimeoutSeconds > 0)
        p.insert(QStringLiteral("launchTimeoutSeconds"), launchTimeoutSeconds);
    return p;
}

LaunchInfo detectLaunchInfo()
{
    LaunchInfo info;
    // Qt 应用的可执行文件路径；无 QCoreApplication 实例时为空串
#if QT_VERSION >= QT_VERSION_CHECK(5, 8, 0)
    info.execPath = qApp ? qApp->applicationFilePath() : QString();
#else
    info.execPath = qApp ? QCoreApplication::applicationFilePath() : QString();
#endif
    return info;
}

QJsonObject pingPayload(qint64 timestampSeconds)
{
    QJsonObject p;
    p.insert(QStringLiteral("timestamp"), timestampSeconds);
    return p;
}

QJsonObject resultPayload(const QString& requestId, bool success,
                          const QJsonValue& data, const QJsonObject& error)
{
    QJsonObject p;
    p.insert(QStringLiteral("requestId"), requestId);
    p.insert(QStringLiteral("success"), success);
    if (success) {
        if (!data.isNull() && !data.isUndefined())
            p.insert(QStringLiteral("data"), data);
    } else if (!error.isEmpty()) {
        p.insert(QStringLiteral("error"), error);
    }
    return p;
}

QJsonObject confirmResultPayload(const QString& requestId, bool confirmed)
{
    QJsonObject p;
    p.insert(QStringLiteral("requestId"), requestId);
    p.insert(QStringLiteral("confirmed"), confirmed);
    return p;
}

QJsonObject errorObject(const QString& code, const QString& message)
{
    QJsonObject e;
    e.insert(QStringLiteral("code"), code);
    e.insert(QStringLiteral("message"), message);
    return e;
}

} // namespace agentquay