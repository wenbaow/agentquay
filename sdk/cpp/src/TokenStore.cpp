// agentquay/TokenStore.cpp — ~/.agentquay/tokens.json 的读写实现。
#include "agentquay/TokenStore.h"

#include <QDebug>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSaveFile>
#include <QStandardPaths>
#include <utility>

namespace agentquay {

namespace {
// 兼容 Java/TS/Python SDK：~/.agentquay 目录下共享同一份 token 映射文件
QString agentQuayHome()
{
    return QDir(QStandardPaths::writableLocation(QStandardPaths::HomeLocation))
        .filePath(QStringLiteral(".agentquay"));
}
} // namespace

TokenStore::TokenStore(QString appId)
    : m_appId(std::move(appId))
{
}

QString TokenStore::filePath()
{
    return QDir(agentQuayHome()).filePath(QStringLiteral("tokens.json"));
}

QString TokenStore::get() const
{
    QFile file(filePath());
    if (!file.open(QIODevice::ReadOnly))
        return QString();
    const QJsonDocument doc = QJsonDocument::fromJson(file.readAll());
    if (!doc.isObject())
        return QString();
    return doc.object().value(m_appId).toString();
}

void TokenStore::set(const QString& token) const
{
    QDir().mkpath(agentQuayHome());

    // 读取现有映射（文件损坏则重建），保留其他应用的 token
    QJsonObject map;
    QFile file(filePath());
    if (file.exists() && file.open(QIODevice::ReadOnly)) {
        const QJsonDocument doc = QJsonDocument::fromJson(file.readAll());
        if (doc.isObject())
            map = doc.object();
    }
    map.insert(m_appId, token);

    // 原子写入（QSaveFile），避免写一半崩溃损坏文件；失败仅记录，不抛异常
    QSaveFile out(filePath());
    if (!out.open(QIODevice::WriteOnly)) {
        qWarning() << "[AgentQuay] 写入 token 失败（open）:" << out.errorString();
        return;
    }
    out.write(QJsonDocument(map).toJson(QJsonDocument::Indented));
    if (!out.commit())
        qWarning() << "[AgentQuay] 写入 token 失败（commit）:" << out.errorString();
}

} // namespace agentquay