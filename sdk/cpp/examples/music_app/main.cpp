// main.cpp — AgentQuay C++ SDK 示例：音乐应用（设计文档 §4.7）。
//
// 运行前确保 Bridge 可用（SDK 会自动拉起内嵌 Bridge，或先运行
// `agentquay start --daemon`）：
//
//   cmake -S . -B build && cmake --build build
//   ./build/examples/music_app/music_app
//
// 然后用 MCP 客户端连接 http://127.0.0.1:19846/mcp，
// 即可发现并调用 music-app_search / music-app_play / music-app_delete（需确认）
// / music-app_listAll。
#include <QCoreApplication>

#include <agentquay/AgentQuayClient.h>

#include "MusicController.h"

MusicController::MusicController(QObject* parent)
    : QObject(parent)
{
    m_songs.append({QStringLiteral("1"), QStringLiteral("七里香"), QStringLiteral("周杰伦"), 243});
    m_songs.append({QStringLiteral("2"), QStringLiteral("晴天"), QStringLiteral("周杰伦"), 269});
    m_songs.append({QStringLiteral("3"), QStringLiteral("海阔天空"), QStringLiteral("Beyond"), 326});
    m_songs.append({QStringLiteral("4"), QStringLiteral("平凡之路"), QStringLiteral("朴树"), 302});
}

QVariantMap MusicController::songToVariant(const Song& s) const
{
    QVariantMap m;
    m.insert(QStringLiteral("id"), s.id);
    m.insert(QStringLiteral("title"), s.title);
    m.insert(QStringLiteral("artist"), s.artist);
    m.insert(QStringLiteral("duration"), s.duration);
    return m;
}

QVariantList MusicController::search(const QString& keyword)
{
    QVariantList results;
    for (const Song& s : m_songs) {
        if (s.title.contains(keyword) || s.artist.contains(keyword))
            results.append(songToVariant(s));
    }
    return results;
}

QVariantMap MusicController::play(const QString& songId)
{
    for (const Song& s : m_songs) {
        if (s.id == songId) {
            QVariantMap result;
            result.insert(QStringLiteral("status"), QStringLiteral("playing"));
            result.insert(QStringLiteral("song"), songToVariant(s));
            return result;
        }
    }
    // 抛异常 = 业务错误，SDK 会透传给 Agent（EXECUTION_ERROR）
    throw std::runtime_error("歌曲不存在: " + songId.toStdString());
}

QVariantMap MusicController::deleteSong(const QString& songId)
{
    for (int i = 0; i < m_songs.size(); ++i) {
        if (m_songs.at(i).id == songId) {
            const Song removed = m_songs.takeAt(i);
            QVariantMap result;
            result.insert(QStringLiteral("status"), QStringLiteral("deleted"));
            result.insert(QStringLiteral("song"), songToVariant(removed));
            return result;
        }
    }
    throw std::runtime_error("歌曲不存在: " + songId.toStdString());
}

QVariantList MusicController::listAll()
{
    QVariantList results;
    for (const Song& s : m_songs)
        results.append(songToVariant(s));
    return results;
}

int main(int argc, char* argv[])
{
    QCoreApplication app(argc, argv);

    agentquay::AgentQuayClient client(QStringLiteral("music-app"),
                                      QStringLiteral("Music Player"));
    client.setPort(0);                // 0 = 从 ~/.agentquay/port 自动读取
    client.setAutoSpawnBridge(true);  // 未检测到服务时自动拉起内嵌 Bridge

    client.registerTools<MusicController>();

    qInfo() << "已注册 tools:" << client.listTools();
    qInfo() << "等待 Agent 调用…（Ctrl+C 退出）";

    client.connect(); // 异步连接，与 Qt 事件循环共存

    // 可选：自定义确认框（默认 QMessageBox / 控制台）
    // client.setConfirmHandler([](const QString& message,
    //                             const QVariantMap& args, int timeoutSeconds) {
    //     qInfo().noquote() << "⚠️ 确认请求:" << message << args;
    //     qInfo() << "超时秒数:" << timeoutSeconds << "（默认拒绝）";
    //     return false;
    // });

    return app.exec();
}