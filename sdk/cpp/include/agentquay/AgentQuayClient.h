// agentquay/AgentQuayClient.h — 连接 AgentQuay Bridge 的应用侧客户端（设计文档 §4.7）。
//
// 职责：工具注册（Qt 反射 / addTool fallback）、心跳、调用分发、确认流程、
// 指数退避重连、token 持久化、auto-spawn 内嵌 Bridge。
//
// 用法：
//   AgentQuayClient client("music-app", "Music Player");
//   client.setPort(0);                // 0 = 从 ~/.agentquay/port 自动读取
//   client.setAutoSpawnBridge(true);  // 未检测到服务时自动拉起内嵌 Bridge
//   client.registerTools<MusicController>();
//   client.connect();                 // 异步连接，与 Qt 事件循环共存
//   return app.exec();
//
// 说明：
//   - 客户端为 QObject，必须在主事件循环中运行（WebSocket/心跳/分发均走事件循环）；
//   - Tool 方法在 QThreadPool 工作线程上执行（与 Java/C# 的 executor 对齐），
//     方法需线程安全；结果通过队列回投主线程后再发送；
//   - 确认回调（confirmHandler）在主线程同步执行（默认 QMessageBox / 控制台）。
#pragma once

#include "agentquay/ConfirmationHandler.h"
#include "agentquay/Protocol.h"

#include <QHash>
#include <QJsonObject>
#include <QJsonValue>
#include <QList>
#include <QMetaObject>
#include <QObject>
#include <QSharedPointer>
#include <QString>
#include <QStringList>
#include <QVariant>
#include <QVariantMap>
#include <functional>
#include <type_traits>
#include <utility>
#include <vector>

class QWebSocket;
class QTimer;

namespace agentquay {

class BridgeSpawner;
class TokenStore;

/**
 * 一个已注册 Tool 的元数据（注册后生成，含调用绑定）。
 *
 * 绑定方式二选一：
 *   - Qt 反射绑定（AGENT_TOOL + Q_INVOKABLE）：metaObject + methodIndex + target
 *   - addTool fallback：handler（std::function）
 */
struct ToolInfo {
    QString name;
    QString description;
    QJsonObject inputSchema;
    bool requiresConfirmation = false;
    int timeoutSeconds = 30;
    int confirmTimeoutSeconds = 120;

    // Qt 反射绑定
    QObject* target = nullptr;               // 控制器实例（非拥有指针；存活周期见 targetGuard）
    QSharedPointer<QObject> targetGuard;     // owned 注册时的共享所有权：在途调用期间保持控制器存活
    const QMetaObject* metaObject = nullptr; // 注册时的类元对象
    int methodIndex = -1;                    // 方法在 metaObject 中的索引

    // fallback 绑定
    std::function<QVariant(const QVariantMap&)> handler;

    bool isReflected() const { return metaObject && methodIndex >= 0; }
};

class AgentQuayClient : public QObject {
    Q_OBJECT
public:
    /**
     * @param appId   应用 ID（[a-z0-9-]{1,48}，禁止 _ 和 .；如 music-app）
     * @param appName 应用显示名
     */
    AgentQuayClient(QString appId, QString appName, QObject* parent = nullptr);
    ~AgentQuayClient() override;

    // ------------------------------------------------------------------
    // 配置（须在 connect() 之前设置）
    // ------------------------------------------------------------------
    void setHost(const QString& host);                  // 默认 127.0.0.1
    void setPort(int port);                             // 0 = 从端口文件自动读取（默认）
    void setAutoSpawnBridge(bool enabled);              // 默认 true
    void setVersion(const QString& version);            // 默认 "1.0.0"
    void setProtocolVersion(const QString& protocolVersion); // 默认 "1.0"
    void setHeartbeatInterval(int seconds);             // 默认 30
    void setMaxRetryInterval(int seconds);              // 默认 30
    void setConfirmHandler(ConfirmHandler handler);     // 默认 QMessageBox / 控制台
    void setLaunchInfo(LaunchInfo launchInfo);          // 启动命令（§5.8）；缺省自动探测
    void setAutoReportLaunch(bool enabled);             // 是否随注册上报启动命令（默认 true）
    using ToolCallHandler = std::function<void(const QString&, const QVariantMap&)>;
    void setToolCallHandler(ToolCallHandler handler);   // 工具调用钩子（默认无）

    // ------------------------------------------------------------------
    // 工具注册
    // ------------------------------------------------------------------

    /**
     * 注册控制器类（Qt 反射路线）：构造 T(args...) 并共享所有权——在途 Tool 执行期间
     * 保持控制器存活（引用归零后回到其所属线程释放）。扫描 QMetaObject 中与 AGENT_TOOL
     * 匹配的 Q_INVOKABLE 方法作为 Tool。T 必须派生自 QObject。
     */
    template <typename T, typename... Args>
    void registerTools(Args&&... args)
    {
        static_assert(std::is_base_of<QObject, T>::value,
                      "AgentQuayClient::registerTools<T> 要求 T 派生自 QObject");
        registerOwned(new T(std::forward<Args>(args)...));
    }

    /** 注册控制器实例（Qt 反射路线；不接管所有权，客户端生命周期内实例须存活）。 */
    void registerTools(QObject* instance);

    /**
     * 通用 fallback 注册：直接登记处理器，无需 Q_OBJECT / Q_INVOKABLE。
     * 处理器签名：QVariant(const QVariantMap& args)；抛异常即视为业务错误。
     * @param inputSchema 可选显式 schema（缺省按 {type:object,properties:{}} 生成）
     */
    AgentQuayClient& addTool(const QString& name,
                             std::function<QVariant(const QVariantMap&)> handler,
                             const QString& description = QString(),
                             bool requiresConfirmation = false,
                             int timeoutSeconds = 30,
                             int confirmTimeoutSeconds = 120,
                             const QJsonObject& inputSchema = QJsonObject());

    /** 已登记的 tool 名列表。 */
    QStringList listTools() const;

    // ------------------------------------------------------------------
    // 连接生命周期
    // ------------------------------------------------------------------

    /**
     * 连接 Bridge 并保持（异步，不阻塞；与 Qt 事件循环共存）。
     * 首次调用会解析端口并视需要 auto-spawn；端口不可用且无法拉起时抛异常。
     * 返回 false 表示已经启动过（重复调用忽略）。
     */
    bool connect();

    /** 阻塞等待首次注册成功（最多 timeoutMs 毫秒），供测试/启动阶段使用。 */
    bool waitForConnected(int timeoutMs = 15000);

    /** 主动断开并停止重连（同时终止本进程拉起的 Bridge 子进程）。 */
    void stop();

    /** 是否已注册成功（连接建立且收到 register_ack）。 */
    bool isConnected() const { return m_registered; }

    /** 当前持有的 token（首次注册后由 Bridge 分配并持久化）。 */
    QString token() const { return m_token; }

signals:
    /** 注册成功，可以接收调用。 */
    void connected();

    /** 与 Bridge 的连接终止（reason 为普通断开/错误信息）。 */
    void disconnectedWithReason(const QString& reason);

    /** 本连接被同 appId 的新实例替换（不再重连）。 */
    void replaced();

    /** Bridge 广播通知。 */
    void bridgeNotification(const QString& message);

    /** 不可恢复错误（如 AUTH_FAILED），客户端已停止。 */
    void fatalError(const QString& message);

private:
    struct PendingInvoke {
        QString toolName;
        int timeoutSeconds = 30;
        qint64 deadlineMs = 0;
    };

    struct InvokeResult {
        bool success = false;
        QJsonValue data;
        QString errorCode;
        QString errorMessage;
        QJsonObject error() const {
            if (errorCode.isEmpty()) return QJsonObject();
            QJsonObject e;
            e.insert(QStringLiteral("code"), errorCode);
            e.insert(QStringLiteral("message"), errorMessage);
            return e;
        }
    };

    void registerReflected(QObject* instance, const QSharedPointer<QObject>& guard = {});
    void registerOwned(QObject* instance);
    void addToolInternal(ToolInfo info);

    // 连接流程
    void startConnection();
    void openSocket();
    void sendRegister();
    void scheduleReconnect();
    void forceClose();

    // 消息处理（主线程）
    void handleMessage(const QString& raw);
    void handleRegisterAck(const QJsonObject& payload);
    void handleRegisterError(const QJsonObject& payload);
    void handleInvoke(const QJsonObject& payload);
    void handleConfirm(const QJsonObject& payload);
    void handleDisconnect(const QString& reason);
    void onWatchdogTick();

    // 调用执行（static：不读取客户端成员状态，故在途工作线程在客户端析构后仍可安全调用）
    static InvokeResult dispatchInvoke(const ToolInfo& tool, const QJsonObject& args);
    static QVariant jsonToParam(const QJsonValue& value, int metaTypeId);
    static QVariant invokeReflected(const ToolInfo& tool, const QJsonObject& args);
    static QVariant invokeHandler(const ToolInfo& tool, const QJsonObject& args);

    // 发送
    void sendMessage(const QByteArray& type, const QJsonObject& payload);
    void sendResult(const QString& requestId, bool success,
                    const QJsonValue& data, const QJsonObject& error);

    ToolInfo* findTool(const QString& name);
    const ToolInfo* findTool(const QString& name) const;
    bool isAppIdValid(const QString& appId) const;
    bool isToolNameValid(const QString& name) const;

    // 成员
    QString m_appId;
    QString m_appName;
    QString m_host = QStringLiteral("127.0.0.1");
    int m_port = 0;
    bool m_autoSpawnBridge = true;
    QString m_version = QStringLiteral("1.0.0");
    QString m_protocolVersion = QStringLiteral("1.0");
    int m_heartbeatInterval = 30;
    int m_maxRetryInterval = 30;
    ConfirmHandler m_confirmHandler;
    LaunchInfo m_launchInfo;          // 随注册上报的启动命令（§5.8）
    bool m_autoReportLaunch = true;
    ToolCallHandler m_toolCallHandler;  // 工具调用钩子

    std::vector<ToolInfo> m_tools;
    // owned 注册的控制器共享所有权：客户端至少持有一份引用，在途工作线程各持一份
    QList<QSharedPointer<QObject>> m_ownedControllers;

    QWebSocket* m_socket = nullptr;
    QTimer* m_pingTimer = nullptr;
    QTimer* m_registerTimeout = nullptr;
    QTimer* m_watchdog = nullptr;

    BridgeSpawner* m_spawner = nullptr;
    TokenStore* m_tokenStore = nullptr;

    QString m_token;
    bool m_registered = false;
    bool m_started = false;
    bool m_stopRequested = false;
    bool m_reconnectScheduled = false;
    int m_backoffMs = 1000;
    qint64 m_lastMessageMs = 0;
    bool m_waitingForRegister = false;

    QHash<QString, PendingInvoke> m_pending;
};

} // namespace agentquay