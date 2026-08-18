// agentquay/AgentQuayClient.cpp — AgentQuay 应用侧客户端实现
// （设计文档 §3.1 / §4.7，与 Java/C# SDK 行为对齐）。
#include "agentquay/AgentQuayClient.h"

#include "agentquay/AgentQuayException.h"
#include "agentquay/BridgeSpawner.h"
#include "agentquay/JsonSchemaGenerator.h"
#include "agentquay/Protocol.h"
#include "agentquay/TokenStore.h"
#include "agentquay/agent_tool.h"

#include <QAbstractSocket>
#include <QDateTime>
#include <QEventLoop>
#include <QJsonArray>
#include <QJsonDocument>
#include <QMetaEnum>
#include <QMetaMethod>
#include <QMetaType>
#include <QThreadPool>
#include <QTimer>
#include <QUrl>
#include <QWebSocket>

#include <algorithm>

namespace agentquay {

namespace {

constexpr int kMaxInvokeArgs = 10;      // QMetaMethod::invoke 最多支持的动态参数个数
constexpr int kRegisterTimeoutMs = 10000;
constexpr qint64 kWatchdogIntervalMs = 500;
constexpr int kHeartbeatMissIntervals = 4; // 心跳失联判定（无消息间隔数）

qint64 nowMs()
{
    return QDateTime::currentMSecsSinceEpoch();
}

qint64 nowSec()
{
    return QDateTime::currentSecsSinceEpoch();
}

// JSON 值 → 目标 QMetaType 的 QVariant（常见 Qt 容器走显式映射，避免 QVariant 转换歧义）。
// 转换失败返回无效 QVariant，由调用方回退为目标类型默认值。
QVariant jsonToParamImpl(const QJsonValue& value, int metaTypeId)
{
    switch (metaTypeId) {
    case QMetaType::QString:
    case QMetaType::QChar:
        return value.toString();
    case QMetaType::QByteArray:
        return value.toString().toUtf8();
    case QMetaType::Int:
        return value.toInt();
    case QMetaType::UInt:
        return static_cast<uint>(qMax<qint64>(0, static_cast<qint64>(value.toVariant().toDouble())));
    case QMetaType::Short:
        return static_cast<short>(value.toInt());
    case QMetaType::UShort:
        return static_cast<ushort>(qMax(0, value.toInt()));
    case QMetaType::LongLong:
        return value.toVariant().toLongLong();
    case QMetaType::ULongLong:
        return value.toVariant().toULongLong();
    case QMetaType::Double:
        return value.toDouble();
    case QMetaType::Float:
        return static_cast<float>(value.toDouble());
    case QMetaType::Bool:
        return value.toBool();
    case QMetaType::QVariantMap:
        return value.toObject().toVariantMap();
    case QMetaType::QVariantList:
        return value.toArray().toVariantList();
    case QMetaType::QStringList: {
        QStringList list;
        const QJsonArray arr = value.toArray();
        list.reserve(arr.size());
        for (const QJsonValue& item : arr)
            list.append(item.toString());
        return list;
    }
    case QMetaType::QJsonObject:
        return value.toObject();
    case QMetaType::QJsonArray:
        return value.toArray();
    default: {
        // 注册的枚举：QString → QMetaEnum 键名；其余自定义类型尝试 QVariant 转换
        if (const QMetaObject* mo = QMetaType::metaObjectForType(metaTypeId)) {
            for (int i = 0; i < mo->enumeratorCount(); ++i) {
                const QMetaEnum me = mo->enumerator(i);
                if (!me.isValid() || me.keyCount() == 0)
                    continue;
                const int n = me.keysToValue(value.toString().toUtf8());
                if (n >= 0) {
                    // Qt 6.8: QVariant(int, void*) 被移除，改用 QMetaType::create + move
                    void* tmp = QMetaType::create(metaTypeId, &n);
                    if (tmp) {
                        QVariant v(QMetaType(metaTypeId), tmp);
                        return v;
                    }
                }
                return QVariant(); // 未识别键名 → 无效，调用方回退默认值
            }
            return QVariant();
        }
        QVariant v = value.toVariant();
        if (v.metaType().id() != metaTypeId)
            v.convert(metaTypeId);
        return v.metaType().id() == metaTypeId ? v : QVariant();
    }
    }
}

} // namespace

// ---------------------------------------------------------------------------
// 构造 / 析构
// ---------------------------------------------------------------------------

AgentQuayClient::AgentQuayClient(QString appId, QString appName, QObject* parent)
    : QObject(parent)
    , m_appId(std::move(appId))
    , m_appName(std::move(appName))
    , m_confirmHandler(defaultConfirmHandler())
    , m_launchInfo(detectLaunchInfo()) // 缺省自动探测（§5.8），可用 setLaunchInfo 覆盖
{
    if (!isAppIdValid(m_appId))
        throw AgentQuayException(
            QStringLiteral("appId 不符合规范 [a-z0-9-]{1,48}（禁止 _ 和 .）: %1").arg(m_appId));
    if (m_appName.isEmpty())
        throw AgentQuayException(QStringLiteral("appName 不能为空"));

    m_tokenStore = new TokenStore(m_appId);
    m_token = m_tokenStore->get();
    // 注意：BridgeSpawner 在 connect() 中创建，以获取 setHost 之后的最终配置

    // 心跳：每 heartbeatInterval 秒主动 ping（Bridge 也会向应用 ping，双向心跳）
    m_pingTimer = new QTimer(this);
    m_pingTimer->setInterval(m_heartbeatInterval * 1000);
    QObject::connect(m_pingTimer, &QTimer::timeout, this, [this] {
        if (m_registered && m_socket
            && m_socket->state() == QAbstractSocket::ConnectedState) {
            sendMessage(kMsgPing, pingPayload(nowSec()));
        }
    });
    m_pingTimer->start();

    // 注册超时
    m_registerTimeout = new QTimer(this);
    m_registerTimeout->setSingleShot(true);
    QObject::connect(m_registerTimeout, &QTimer::timeout, this, [this] {
        if (m_waitingForRegister) {
            qWarning() << "[AgentQuay] 注册超时" << kRegisterTimeoutMs / 1000 << "s 未收到 register_ack";
            m_waitingForRegister = false;
            forceClose();
            scheduleReconnect();
        }
    });

    // 看门狗：心跳失联检测 + 本地执行超时扫描
    m_watchdog = new QTimer(this);
    m_watchdog->setInterval(kWatchdogIntervalMs);
    QObject::connect(m_watchdog, &QTimer::timeout, this, [this] { onWatchdogTick(); });
    m_watchdog->start();
}

AgentQuayClient::~AgentQuayClient()
{
    stop();
}

// ---------------------------------------------------------------------------
// 配置
// ---------------------------------------------------------------------------

void AgentQuayClient::setHost(const QString& host) { m_host = host; }
void AgentQuayClient::setPort(int port) { m_port = port; }
void AgentQuayClient::setAutoSpawnBridge(bool enabled) { m_autoSpawnBridge = enabled; }
void AgentQuayClient::setVersion(const QString& version) { m_version = version; }
void AgentQuayClient::setProtocolVersion(const QString& protocolVersion) { m_protocolVersion = protocolVersion; }
void AgentQuayClient::setHeartbeatInterval(int seconds)
{
    m_heartbeatInterval = seconds > 0 ? seconds : 30;
    if (m_pingTimer)
        m_pingTimer->setInterval(m_heartbeatInterval * 1000);
}
void AgentQuayClient::setMaxRetryInterval(int seconds) { m_maxRetryInterval = seconds > 0 ? seconds : 30; }
void AgentQuayClient::setConfirmHandler(ConfirmHandler handler)
{
    m_confirmHandler = handler ? std::move(handler) : defaultConfirmHandler();
}
void AgentQuayClient::setLaunchInfo(LaunchInfo launchInfo)
{
    m_launchInfo = std::move(launchInfo);
}
void AgentQuayClient::setAutoReportLaunch(bool enabled)
{
    m_autoReportLaunch = enabled;
}

// ---------------------------------------------------------------------------
// 工具注册
// ---------------------------------------------------------------------------

void AgentQuayClient::registerTools(QObject* instance)
{
    if (!instance)
        throw AgentQuayException(QStringLiteral("registerTools 收到空实例"));
    registerReflected(instance);
}

void AgentQuayClient::registerReflected(QObject* instance)
{
    const std::vector<AgentToolMeta> metas = AgentToolRegistry::all();
    const QMetaObject* mo = instance->metaObject();
    for (int i = mo->methodOffset(); i < mo->methodCount(); ++i) {
        const QMetaMethod m = mo->method(i);
        if (m.methodType() != QMetaMethod::Method) // Q_INVOKABLE 方法
            continue;
        const QByteArray name = m.name();
        const AgentToolMeta* meta = nullptr;
        for (const AgentToolMeta& mt : metas) {
            if (mt.name && name == mt.name) {
                meta = &mt;
                break;
            }
        }
        if (!meta)
            continue; // 未标记 AGENT_TOOL

        ToolInfo info;
        info.name = QString::fromUtf8(meta->name);
        info.description = QString::fromUtf8(meta->description);
        info.requiresConfirmation = meta->options.requiresConfirmation;
        info.timeoutSeconds = meta->options.timeoutSeconds;
        info.confirmTimeoutSeconds = meta->options.confirmTimeoutSeconds;
        info.inputSchema = JsonSchemaGenerator::paramSchema(m);
        info.target = instance;
        info.metaObject = mo;
        info.methodIndex = m.methodIndex();
        addToolInternal(std::move(info));
    }
}

AgentQuayClient& AgentQuayClient::addTool(const QString& name,
                                          std::function<QVariant(const QVariantMap&)> handler,
                                          const QString& description,
                                          bool requiresConfirmation,
                                          int timeoutSeconds,
                                          int confirmTimeoutSeconds,
                                          const QJsonObject& inputSchema)
{
    ToolInfo info;
    info.name = name;
    info.description = description;
    info.requiresConfirmation = requiresConfirmation;
    info.timeoutSeconds = timeoutSeconds;
    info.confirmTimeoutSeconds = confirmTimeoutSeconds;
    if (inputSchema.isEmpty()) {
        QJsonObject root;
        root.insert(QStringLiteral("type"), QStringLiteral("object"));
        root.insert(QStringLiteral("properties"), QJsonObject());
        info.inputSchema = root;
    } else {
        info.inputSchema = inputSchema;
    }
    info.handler = std::move(handler);
    addToolInternal(std::move(info));
    return *this;
}

void AgentQuayClient::addToolInternal(ToolInfo info)
{
    if (!isToolNameValid(info.name))
        throw AgentQuayException(
            QStringLiteral("tool 名不符合规范 [a-zA-Z0-9_-]{1,78}: %1").arg(info.name));
    for (const ToolInfo& t : m_tools)
        if (t.name == info.name)
            throw AgentQuayException(QStringLiteral("tool 名重复: %1").arg(info.name));
    if (!info.isReflected() && !info.handler)
        throw AgentQuayException(QStringLiteral("tool %1 无绑定（既非反射方法也无 handler）").arg(info.name));
    m_tools.push_back(std::move(info));
}

QStringList AgentQuayClient::listTools() const
{
    QStringList names;
    names.reserve(int(m_tools.size()));
    for (const ToolInfo& t : m_tools)
        names.append(t.name);
    return names;
}

// ---------------------------------------------------------------------------
// 连接生命周期
// ---------------------------------------------------------------------------

bool AgentQuayClient::connect()
{
    if (m_started)
        return false;
    m_started = true;
    m_stopRequested = false;
    m_backoffMs = 1000;

    // 首次连接：确保 Bridge 可用（含 auto-spawn），解析实际端口
    // （spawner 在此创建，确保拿到 setHost 之后的最终 host）
    m_spawner = new BridgeSpawner(m_host, this);
    m_port = m_spawner->ensureBridge(m_port, m_autoSpawnBridge);

    startConnection();
    return true;
}

bool AgentQuayClient::waitForConnected(int timeoutMs)
{
    if (m_registered)
        return true;
    QEventLoop loop;
    const QMetaObject::Connection c1 =
        QObject::connect(this, &AgentQuayClient::connected, &loop, &QEventLoop::quit, Qt::QueuedConnection);
    const QMetaObject::Connection c2 =
        QObject::connect(this, &AgentQuayClient::fatalError, &loop, &QEventLoop::quit, Qt::QueuedConnection);
    QTimer::singleShot(timeoutMs, &loop, &QEventLoop::quit);
    loop.exec();
    disconnect(c1);
    disconnect(c2);
    return m_registered;
}

void AgentQuayClient::stop()
{
    m_stopRequested = true;
    m_registered = false;
    m_waitingForRegister = false;
    m_pending.clear();
    m_registerTimeout->stop();
    if (m_socket)
        m_socket->abort();
    // 本进程拉起的 Bridge 交由空闲自回收退出（embedded-bridge-lifecycle.md 步骤 1）
    // 注意：m_spawner 仅在 connect() 中创建——未调用 connect() 直接析构时为空
    if (m_spawner)
        m_spawner->shutdown();
    // 等待执行中的 Tool 结束（避免析构后工作线程仍访问控制器）
    QThreadPool::globalInstance()->waitForDone(3000);
}

// ---------------------------------------------------------------------------
// 连接流程
// ---------------------------------------------------------------------------

void AgentQuayClient::startConnection()
{
    if (m_stopRequested)
        return;
    openSocket();
}

void AgentQuayClient::openSocket()
{
    if (m_socket) {
        m_socket->deleteLater();
        m_socket = nullptr;
    }
    auto* socket = new QWebSocket(QString(), QWebSocketProtocol::VersionLatest, this);
    m_socket = socket;

    QObject::connect(socket, &QWebSocket::connected, this, [this] {
        qInfo() << "[AgentQuay] WebSocket 已连接" << m_host << m_port;
        m_registered = false;
        m_waitingForRegister = true;
        m_lastMessageMs = nowMs();
        sendRegister();
        m_registerTimeout->start(kRegisterTimeoutMs);
    });
    QObject::connect(socket, &QWebSocket::textMessageReceived, this,
            &AgentQuayClient::handleMessage);
    QObject::connect(socket, &QWebSocket::disconnected, this, [this] {
        if (m_socket) {
            m_socket->deleteLater();
            m_socket = nullptr;
        }
        const bool stopped = m_stopRequested;
        m_registered = false;
        m_waitingForRegister = false;
        m_registerTimeout->stop();
        m_pending.clear();
        if (stopped) {
            emit disconnectedWithReason(QStringLiteral("stopped"));
            return;
        }
        emit disconnectedWithReason(QStringLiteral("connection lost"));
        scheduleReconnect();
    });
    QObject::connect(socket, &QWebSocket::errorOccurred, this, [this, socket](QAbstractSocket::SocketError) {
        qWarning() << "[AgentQuay] WebSocket 错误:" << socket->errorString();
        if (m_stopRequested)
            return;
        forceClose();
        scheduleReconnect();
    });

    const QUrl url(QStringLiteral("ws://%1:%2/ws").arg(m_host).arg(m_port));
    qInfo() << "[AgentQuay] 连接 Bridge" << url.toString();
    socket->open(url);
}

void AgentQuayClient::sendRegister()
{
    if (!m_socket || m_socket->state() != QAbstractSocket::ConnectedState)
        return;

    QJsonArray toolsArr;
    for (const ToolInfo& t : m_tools) {
        QJsonObject tool;
        tool.insert(QStringLiteral("name"), t.name);
        tool.insert(QStringLiteral("description"), t.description);
        tool.insert(QStringLiteral("inputSchema"), t.inputSchema);
        tool.insert(QStringLiteral("requiresConfirmation"), t.requiresConfirmation);
        tool.insert(QStringLiteral("timeoutSeconds"), t.timeoutSeconds);
        tool.insert(QStringLiteral("confirmTimeoutSeconds"), t.confirmTimeoutSeconds);
        toolsArr.append(tool);
    }

    const QJsonObject payload = registerPayload(m_appId, m_appName, m_version,
                                                m_protocolVersion, m_token, toolsArr,
                                                m_autoReportLaunch ? m_launchInfo.toJson() : QJsonObject());
    m_socket->sendTextMessage(
        QString::fromUtf8(encode(QString::fromLatin1(kMsgRegister), payload)));
}

void AgentQuayClient::scheduleReconnect()
{
    if (m_stopRequested || m_reconnectScheduled)
        return;
    m_reconnectScheduled = true;
    const int delayMs = m_backoffMs;
    m_backoffMs = qMin(m_backoffMs * 2, m_maxRetryInterval * 1000);
    qInfo() << "[AgentQuay] 连接失败，" << delayMs / 1000 << "s 后重连";
    QTimer::singleShot(delayMs, this, [this] {
        m_reconnectScheduled = false;
        if (m_stopRequested)
            return;
        // Bridge 可能已重启并发生端口漂移：重读权威端口文件
        const int read = BridgeSpawner::readPortFile();
        if (read > 0 && read != m_port) {
            qInfo() << "[AgentQuay] Bridge 端口变化" << m_port << "→" << read;
            m_port = read;
        }
        startConnection();
    });
}

void AgentQuayClient::forceClose()
{
    if (m_socket)
        m_socket->abort();
}

// ---------------------------------------------------------------------------
// 消息处理（主线程）
// ---------------------------------------------------------------------------

void AgentQuayClient::handleMessage(const QString& raw)
{
    m_lastMessageMs = nowMs();

    QString type;
    QJsonValue payloadValue;
    if (!parse(raw.toUtf8(), &type, &payloadValue)) {
        qWarning() << "[AgentQuay] 无法解析消息:" << raw.left(200);
        return;
    }
    const QJsonObject payload = payloadValue.toObject();

    if (type == QLatin1String(kMsgRegisterAck)) {
        handleRegisterAck(payload);
    } else if (type == QLatin1String(kMsgRegisterError)) {
        handleRegisterError(payload);
    } else if (type == QLatin1String(kMsgInvoke)) {
        handleInvoke(payload);
    } else if (type == QLatin1String(kMsgConfirm)) {
        handleConfirm(payload);
    } else if (type == QLatin1String(kMsgPing)) {
        // 回 pong（回显时间戳）
        sendMessage(kMsgPong, pingPayload(payload.value(QStringLiteral("timestamp"))
                                              .toVariant().toLongLong()));
    } else if (type == QLatin1String(kMsgPong)) {
        // 静默：消息到达即重置失联计数
    } else if (type == QLatin1String(kMsgDisconnect)) {
        handleDisconnect(payload.value(QStringLiteral("reason"))
                             .toString(QString::fromLatin1(kDisconnectNormal)));
    } else if (type == QLatin1String(kMsgNotification)) {
        const QString message = payload.value(QStringLiteral("message")).toString();
        qInfo() << "[AgentQuay] Bridge 通知:" << message;
        emit bridgeNotification(message);
    } else {
        qWarning() << "[AgentQuay] 未知消息类型:" << type;
    }
}

void AgentQuayClient::handleRegisterAck(const QJsonObject& payload)
{
    const QString newToken = payload.value(QStringLiteral("token")).toString();
    if (!newToken.isEmpty()) {
        m_token = newToken;
        m_tokenStore->set(newToken); // 持久化，重连自动携带
    }
    m_registered = true;
    m_waitingForRegister = false;
    m_backoffMs = 1000;
    m_registerTimeout->stop();
    qInfo() << "[AgentQuay] 注册成功:" << m_appId << "(tools=" << m_tools.size() << ")";
    emit connected();
}

void AgentQuayClient::handleRegisterError(const QJsonObject& payload)
{
    const QString code = payload.value(QStringLiteral("code")).toString();
    const QString message = payload.value(QStringLiteral("message")).toString();
    m_registerTimeout->stop();
    m_waitingForRegister = false;

    // 持久性注册错误重连无意义，直接停止并通知
    m_stopRequested = true;
    forceClose();
    if (code == QLatin1String(kErrCodeAuthFailed)) {
        emit fatalError(QStringLiteral("认证失败（authToken 与 Bridge 钉扎的 token 不匹配）: %1").arg(message));
    } else {
        emit fatalError(QStringLiteral("注册被拒绝 [%1]: %2").arg(code, message));
    }
}

void AgentQuayClient::handleInvoke(const QJsonObject& payload)
{
    const QString requestId = payload.value(QStringLiteral("requestId")).toString();
    const QString toolName = payload.value(QStringLiteral("tool")).toString();
    const int timeoutSeconds = payload.value(QStringLiteral("timeoutSeconds")).toInt(30);
    const QJsonObject args = payload.value(QStringLiteral("arguments")).toObject();

    const ToolInfo* tool = findTool(toolName);
    if (!tool) {
        sendResult(requestId, false, QJsonValue(),
                   errorObject(QString::fromLatin1(kInvokeErrCodeToolNotFound),
                               QStringLiteral("tool 不存在: %1").arg(toolName)));
        return;
    }

    // 挂起请求 + 看门狗计时（本地超时 = Bridge 执行超时 + 5s 余量，保证先于 Bridge 超时）
    PendingInvoke pending;
    pending.toolName = toolName;
    pending.timeoutSeconds = timeoutSeconds;
    pending.deadlineMs = nowMs() + qint64(timeoutSeconds + 5) * 1000;
    m_pending.insert(requestId, pending);

    // 工作线程执行（Tool 方法需线程安全），完成后再回投主线程发送结果
    const ToolInfo copy = *tool;
    QThreadPool::globalInstance()->start([this, copy, args, requestId] {
        const InvokeResult res = dispatchInvoke(copy, args);
        QMetaObject::invokeMethod(this, [this, requestId, res] {
            auto it = m_pending.find(requestId);
            if (it == m_pending.end())
                return; // 已超时 → 孤儿结果，丢弃（对齐 Bridge 孤儿处理）
            m_pending.erase(it);
            sendResult(requestId, res.success, res.data, res.error());
        }, Qt::QueuedConnection);
    });
}

void AgentQuayClient::handleConfirm(const QJsonObject& payload)
{
    const QString requestId = payload.value(QStringLiteral("requestId")).toString();
    const QString message = payload.value(QStringLiteral("message")).toString();
    const int timeoutSeconds = payload.value(QStringLiteral("timeoutSeconds")).toInt(120);
    const QVariantMap arguments = payload.value(QStringLiteral("arguments")).toObject().toVariantMap();

    // 确认回调在主线程同步执行（默认 QMessageBox / 控制台；异常视为拒绝）
    bool confirmed = false;
    try {
        confirmed = m_confirmHandler(message, arguments, timeoutSeconds);
    } catch (const std::exception& e) {
        qWarning() << "[AgentQuay] 确认流程异常，默认拒绝:" << e.what();
        confirmed = false;
    } catch (...) {
        confirmed = false;
    }
    sendMessage(kMsgConfirmResult, confirmResultPayload(requestId, confirmed));
}

void AgentQuayClient::handleDisconnect(const QString& reason)
{
    if (reason == QLatin1String(kDisconnectReplaced)) {
        // 被同 appId 的新实例替换：停止重连（对齐 Java ReplacedException）
        qWarning() << "[AgentQuay] 被同 appId 的新实例替换";
        m_stopRequested = true;
        forceClose();
        emit replaced();
        return;
    }
    if (reason == QLatin1String(kDisconnectMigrate)) {
        // Bridge 让位给更强的系统服务实例：按普通断线重连，重连会重读端口文件
        qInfo() << "[AgentQuay] Bridge 让位，重连时将重读端口文件";
        forceClose();
        scheduleReconnect();
        return;
    }
    // normal / shutdown：Bridge 可能重启，随后重连
    qInfo() << "[AgentQuay] Bridge 断开:" << reason;
    forceClose();
    scheduleReconnect();
}

void AgentQuayClient::onWatchdogTick()
{
    const qint64 now = nowMs();

    // 心跳失联：注册成功后长时间无任何消息 → 强制重连
    if (m_registered && (now - m_lastMessageMs > qint64(m_heartbeatInterval) * kHeartbeatMissIntervals * 1000)) {
        qWarning() << "[AgentQuay] 心跳超时（" << m_heartbeatInterval * kHeartbeatMissIntervals
                   << "s 无消息），强制重连";
        m_registered = false;
        forceClose();
        return;
    }

    // 本地执行超时扫描
    for (auto it = m_pending.begin(); it != m_pending.end();) {
        if (now >= it.value().deadlineMs) {
            const QString requestId = it.key();
            const PendingInvoke pending = it.value();
            qWarning() << "[AgentQuay] 本地执行超时" << requestId << pending.toolName;
            it = m_pending.erase(it);
            sendResult(requestId, false, QJsonValue(),
                       errorObject(QString::fromLatin1(kInvokeErrCodeExecutionTimeout),
                                   QStringLiteral("执行超时（>%1s），结果可能迟到")
                                       .arg(pending.timeoutSeconds)));
        } else {
            ++it;
        }
    }
}

// ---------------------------------------------------------------------------
// 调用执行（工作线程）
// ---------------------------------------------------------------------------

AgentQuayClient::InvokeResult AgentQuayClient::dispatchInvoke(const ToolInfo& tool,
                                                              const QJsonObject& args)
{
    InvokeResult result;
    try {
        const QVariant value = tool.isReflected() ? invokeReflected(tool, args)
                                                  : invokeHandler(tool, args);
        result.success = true;
        if (value.isValid())
            result.data = QJsonValue::fromVariant(value);
    } catch (const ToolCallException& e) {
        result.errorCode = e.code();
        result.errorMessage = QString::fromUtf8(e.what());
    } catch (const std::exception& e) {
        result.errorCode = QString::fromLatin1(kInvokeErrCodeExecutionError);
        result.errorMessage = QString::fromUtf8(e.what());
    } catch (...) {
        result.errorCode = QString::fromLatin1(kInvokeErrCodeExecutionError);
        result.errorMessage = QStringLiteral("未知异常");
    }
    return result;
}

QVariant AgentQuayClient::invokeHandler(const ToolInfo& tool, const QJsonObject& args)
{
    if (!tool.handler)
        throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                QStringLiteral("Tool 处理器为空: %1").arg(tool.name));
    return tool.handler(args.toVariantMap());
}

QVariant AgentQuayClient::invokeReflected(const ToolInfo& tool, const QJsonObject& args) const
{
    if (!tool.target || !tool.metaObject)
        throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                QStringLiteral("Tool 绑定无效（目标实例已销毁）: %1").arg(tool.name));

    const QMetaMethod method = tool.metaObject->method(tool.methodIndex);
    if (!method.isValid())
        throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                QStringLiteral("方法索引无效: %1").arg(tool.name));

    const QList<QByteArray> paramTypes = method.parameterTypes();
    const QList<QByteArray> paramNames = method.parameterNames();

    if (paramTypes.size() != paramNames.size())
        throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                QStringLiteral("方法参数元信息不一致: %1").arg(tool.name));
    if (paramTypes.size() > kMaxInvokeArgs)
        throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                QStringLiteral("参数超过 %1 个，无法动态调用: %2")
                                    .arg(kMaxInvokeArgs).arg(tool.name));

    // 按声明顺序绑定参数（QMetaMethod::invoke 需要类型名精确匹配 moc 记录）
    QVariant values[kMaxInvokeArgs];
    QGenericArgument genericArgs[kMaxInvokeArgs];
    for (int i = 0; i < paramTypes.size(); ++i) {
        // Qt 6.8.3: parameterMetaType(int) 返回 QMetaType；通过 QMetaType::fromName 反向查找 id
        const QByteArray& paramType = paramTypes.value(i);
        const QMetaType mt = QMetaType::fromName(paramType);
        const int typeId = mt.id();
        if (typeId == QMetaType::UnknownType || !QMetaType::isRegistered(typeId))
            throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                    QStringLiteral("参数类型未注册为 Qt 元类型（Q_DECLARE_METATYPE）: %1")
                                        .arg(QString::fromLatin1(paramTypes.value(i))));

        QByteArray name = paramNames.value(i);
        if (name.isEmpty())
            name = "arg" + QByteArray::number(i);
        const QJsonValue jsonValue = args.value(QString::fromUtf8(name));
        QVariant value = jsonToParamImpl(jsonValue, typeId);
        // 转换失败时回退目标类型默认值，避免 QGenericArgument 数据类型不匹配
        if (!value.isValid() || value.metaType().id() != typeId) {
            // Qt 6.8: QVariant(int, void*) 被移除，改用 QMetaType::create + move
            void* tmp = QMetaType::create(typeId);
            if (tmp) {
                value = QVariant(mt, tmp);
            } else {
                value = QVariant();
            }
        }
        if (!value.isValid())
            value = QVariant();
        values[i] = value;
        genericArgs[i] = QGenericArgument(paramTypes.value(i).constData(), values[i].constData());
    }
    for (int i = paramTypes.size(); i < kMaxInvokeArgs; ++i)
        genericArgs[i] = QGenericArgument();

    // 返回值捕获（类型已注册时）
    const int retTypeId = method.returnMetaType().id();
    const QByteArray retTypeName = method.typeName();
    void* retData = nullptr;
    QGenericReturnArgument returnArg;
    if (retTypeId != QMetaType::Void && retTypeId != QMetaType::UnknownType
        && QMetaType::isRegistered(retTypeId) && !retTypeName.isEmpty()) {
        retData = QMetaType::create(retTypeId);
        if (retData)
            returnArg = QGenericReturnArgument(retTypeName.constData(), retData);
    }

    // 注意：tool.target 的销毁由客户端生命周期保证（stop() 会等待工作线程结束）
    const bool ok = method.invoke(tool.target, Qt::DirectConnection, returnArg,
                                  genericArgs[0], genericArgs[1], genericArgs[2], genericArgs[3],
                                  genericArgs[4], genericArgs[5], genericArgs[6], genericArgs[7],
                                  genericArgs[8], genericArgs[9]);

    QVariant result;
    if (retData) {
        if (ok)
            result = QVariant(QMetaType(retTypeId), retData);
        QMetaType(retTypeId).destroy(retData);
    }
    if (!ok)
        throw ToolCallException(QString::fromLatin1(kInvokeErrCodeExecutionError),
                                QStringLiteral("反射调用失败: %1").arg(tool.name));
    return result;
}

QVariant AgentQuayClient::jsonToParam(const QJsonValue& value, int metaTypeId)
{
    return jsonToParamImpl(value, metaTypeId);
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

void AgentQuayClient::sendMessage(const QByteArray& type, const QJsonObject& payload)
{
    if (!m_socket || m_socket->state() != QAbstractSocket::ConnectedState)
        return; // 连接已断，静默丢弃（对齐 Java 的日志降级）
    m_socket->sendTextMessage(
        QString::fromUtf8(encode(QString::fromLatin1(type), payload)));
}

void AgentQuayClient::sendResult(const QString& requestId, bool success,
                                 const QJsonValue& data, const QJsonObject& error)
{
    sendMessage(QByteArray(kMsgResult),
                resultPayload(requestId, success, data, error));
}

// ---------------------------------------------------------------------------
// 工具查找与校验
// ---------------------------------------------------------------------------

ToolInfo* AgentQuayClient::findTool(const QString& name)
{
    for (ToolInfo& t : m_tools)
        if (t.name == name)
            return &t;
    return nullptr;
}

const ToolInfo* AgentQuayClient::findTool(const QString& name) const
{
    for (const ToolInfo& t : m_tools)
        if (t.name == name)
            return &t;
    return nullptr;
}

bool AgentQuayClient::isAppIdValid(const QString& appId) const
{
    if (appId.isEmpty() || appId.size() > 48)
        return false;
    for (const QChar c : appId) {
        const bool lower = c >= QLatin1Char('a') && c <= QLatin1Char('z');
        const bool digit = c >= QLatin1Char('0') && c <= QLatin1Char('9');
        const bool dash = c == QLatin1Char('-');
        if (!lower && !digit && !dash)
            return false;
    }
    return true;
}

bool AgentQuayClient::isToolNameValid(const QString& name) const
{
    if (name.isEmpty() || name.size() > 78)
        return false;
    for (const QChar c : name) {
        const bool lower = c >= QLatin1Char('a') && c <= QLatin1Char('z');
        const bool upper = c >= QLatin1Char('A') && c <= QLatin1Char('Z');
        const bool digit = c >= QLatin1Char('0') && c <= QLatin1Char('9');
        const bool underscore = c == QLatin1Char('_');
        const bool dash = c == QLatin1Char('-');
        if (!lower && !upper && !digit && !underscore && !dash)
            return false;
    }
    return true;
}

} // namespace agentquay