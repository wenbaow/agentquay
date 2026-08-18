// MusicController.h — AgentQuay C++ SDK 示例控制器（设计文档 §4.7 Qt 路线）。
//
// AGENT_TOOL / AGENT_TOOL_OPTS 宏放在 Q_INVOKABLE 方法声明之前，展开为
// 类内 static inline 注册器，在静态初始化阶段把方法名与元数据登记到进程级注册表。
// registerTools<T>() 时由 QMetaObject 反射扫描 Q_INVOKABLE 方法并按方法名匹配绑定。
//
// 说明：宏调用行用 #ifndef Q_MOC_RUN 包裹，moc 预处理时将其剥离，避免老版本
// moc 无法解析带字符串字面量的宏调用（Qt 6 默认可直接使用，包裹更稳妥）。
#pragma once

#include <QObject>
#include <QVariantMap>
#include <QVariantList>

#include <agentquay/agent_tool.h>

class MusicController : public QObject {
    Q_OBJECT
public:
    explicit MusicController(QObject* parent = nullptr);

    // 搜索音乐库中的歌曲（返回 JSON-friendly 的 QVariantList）
#ifndef Q_MOC_RUN
    AGENT_TOOL(search, "搜索音乐库中的歌曲")
#endif
    Q_INVOKABLE QVariantList search(const QString& keyword);

    // 播放指定歌曲
#ifndef Q_MOC_RUN
    AGENT_TOOL(play, "播放指定歌曲")
#endif
    Q_INVOKABLE QVariantMap play(const QString& songId);

    // 危险操作：requiresConfirmation=true，Agent 调用前 Bridge 会向应用弹确认框
#ifndef Q_MOC_RUN
    AGENT_TOOL_OPTS(deleteSong, "删除歌曲（危险操作，需要用户确认）", agentquay::AgentToolOptions{true})
#endif
    Q_INVOKABLE QVariantMap deleteSong(const QString& songId);

    // 列出全部歌曲
#ifndef Q_MOC_RUN
    AGENT_TOOL(listAll, "列出全部歌曲")
#endif
    Q_INVOKABLE QVariantList listAll();

private:
    struct Song {
        QString id;
        QString title;
        QString artist;
        int duration;
    };

    QVariantMap songToVariant(const Song& s) const;

    QList<Song> m_songs;
};