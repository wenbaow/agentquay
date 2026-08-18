// agentquay/BridgeSpawner.cpp — Bridge 探测、内嵌二进制查找与拉起。
#include "agentquay/BridgeSpawner.h"

#include "agentquay/AgentQuayException.h"

#include <QCoreApplication>
#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QProcess>
#include <QProcessEnvironment>
#include <QStandardPaths>
#include <QTcpSocket>
#include <QThread>
#include <QTimer>
#include <utility>

namespace agentquay {

namespace {

QString binaryName()
{
#ifdef Q_OS_WIN
    return QStringLiteral("agentquay.exe");
#else
    return QStringLiteral("agentquay");
#endif
}

// 平台后缀名列表（对应 bridge/dist 现有产物与未来 CI 产物）
QStringList suffixCandidates()
{
    QStringList names;
    names << binaryName(); // 无后缀
#ifdef Q_OS_WIN
    names << QStringLiteral("agentquay-windows-amd64.exe")
          << QStringLiteral("agentquay-windows-arm64.exe");
#elif defined(Q_OS_MACOS)
    names << QStringLiteral("agentquay-darwin-arm64")
          << QStringLiteral("agentquay-darwin-amd64");
#else
    names << QStringLiteral("agentquay-linux-amd64")
          << QStringLiteral("agentquay-linux-arm64");
#endif
    return names;
}

bool fileExists(const QString& path)
{
    return !path.isEmpty() && QFileInfo::exists(path);
}

// 在目录下查找任一候选二进制名，返回完整路径或空串
QString findInDir(const QString& dir)
{
    if (dir.isEmpty() || !QFileInfo::exists(dir))
        return QString();
    const QStringList candidates = suffixCandidates();
    for (const QString& name : candidates) {
        const QString path = QDir(dir).filePath(name);
        if (fileExists(path))
            return path;
    }
    return QString();
}

QString homeDir()
{
    return QStandardPaths::writableLocation(QStandardPaths::HomeLocation);
}

QString portFilePath()
{
    return QDir(homeDir()).filePath(QStringLiteral(".agentquay/port"));
}

} // namespace

BridgeSpawner::BridgeSpawner(QString host, QObject* parent)
    : QObject(parent)
    , m_host(std::move(host))
{
}

BridgeSpawner::~BridgeSpawner()
{
    shutdown();
}

int BridgeSpawner::readPortFile()
{
    QFile file(portFilePath());
    if (!file.open(QIODevice::ReadOnly))
        return 0;
    bool ok = false;
    const int port = QString::fromUtf8(file.readAll()).trimmed().toInt(&ok);
    return ok && port > 0 && port < 65536 ? port : 0;
}

bool BridgeSpawner::probe(const QString& host, int port, int timeoutMs)
{
    QTcpSocket socket;
    socket.connectToHost(host, port);
    return socket.waitForConnected(timeoutMs);
}

int BridgeSpawner::ensureBridge(int port, bool autoSpawn)
{
    int actual = port;
    if (actual == 0)
        actual = readPortFile();
    if (actual > 0 && probe(m_host, actual, 300))
        return actual;

    if (autoSpawn) {
        const QString binary = findBinary();
        if (!binary.isEmpty()) {
            if (acquireSpawnLock()) {
                try {
                    // 拿到锁后复查：竞态窗口内可能有别的实例已拉起
                    if (actual > 0 && probe(m_host, actual, 200))
                        return actual;
                    const int existing = readPortFile();
                    if (existing > 0 && probe(m_host, existing, 200))
                        return existing;
                    qInfo() << "[AgentQuay] 本地无 Bridge 服务，拉起内嵌 Bridge:" << binary;
                    spawnEmbedded(binary);
                    const qint64 deadline = QDateTime::currentMSecsSinceEpoch() + 12'000;
                    while (QDateTime::currentMSecsSinceEpoch() < deadline) {
                        if (actual > 0 && probe(m_host, actual, 200))
                            return actual;
                        const int read = readPortFile();
                        if (read > 0 && probe(m_host, read, 200))
                            return read;
                        QThread::msleep(200);
                    }
                    throw BridgeSpawnException(
                        QStringLiteral("内嵌 Bridge 启动超时。可尝试: 1) 手动运行 `agentquay start --daemon`; "
                                       "2) 检查端口占用与日志"));
                } catch (...) {
                    releaseSpawnLock();
                    throw;
                }
                releaseSpawnLock();
            }
            // 未抢到锁：别的应用正在拉起，等待其端口文件就绪后直接复用
            return waitForAnyBridge(actual);
        }
        throw BridgeUnavailableException(
            QStringLiteral("本地未检测到 AgentQuay Bridge，且未找到 agentquay 二进制。"
                           "两种出路: 1) 安装系统服务（agentquay start --daemon）; "
                           "2) 将 agentquay 放入 PATH 或随 SDK 分发内嵌二进制"));
    }
    throw BridgeUnavailableException(
        QStringLiteral("本地端口 %1 无 AgentQuay Bridge 服务，且 autoSpawnBridge=false。"
                       "请先运行 `agentquay start --daemon` 或开启 autoSpawn")
            .arg(actual > 0 ? QString::number(actual) : QStringLiteral("(未找到端口文件)")));
}

// ---------------------------------------------------------------------------
// spawn 原子锁（步骤 3）：锁文件 ~/.agentquay/spawn.lock，内容 {pid, startedAt}，跨语言互认。

QString BridgeSpawner::lockFilePath()
{
    return QDir::homePath() + QStringLiteral("/.agentquay/spawn.lock");
}

bool BridgeSpawner::writeLockFile()
{
    QDir().mkpath(QFileInfo(lockFilePath()).absolutePath());
    QFile file(lockFilePath());
    // NewOnly == O_EXCL：文件已存在则打开失败
    if (!file.open(QIODevice::NewOnly | QIODevice::WriteOnly))
        return false;
    file.write(QJsonDocument(QJsonObject{
        { QStringLiteral("pid"), QCoreApplication::applicationPid() },
        { QStringLiteral("startedAt"), QDateTime::currentMSecsSinceEpoch() },
    }).toJson(QJsonDocument::Compact));
    file.close();
    return true;
}

bool BridgeSpawner::lockExpired()
{
    QFile file(lockFilePath());
    if (!file.open(QIODevice::ReadOnly))
        return true; // 缺失视为已失效
    const QJsonObject obj = QJsonDocument::fromJson(file.readAll()).object();
    const qint64 startedAt = obj.value(QStringLiteral("startedAt")).toVariant().toLongLong();
    return QDateTime::currentMSecsSinceEpoch() - startedAt > 15'000;
}

bool BridgeSpawner::acquireSpawnLock()
{
    if (writeLockFile())
        return true;
    if (!lockExpired())
        return false; // 未过期：另有实例正在拉起
    QFile::remove(lockFilePath()); // 过期（崩溃残留）：打破后重试一次
    return writeLockFile();
}

void BridgeSpawner::releaseSpawnLock()
{
    QFile::remove(lockFilePath());
}

int BridgeSpawner::waitForAnyBridge(int knownPort)
{
    const qint64 deadline = QDateTime::currentMSecsSinceEpoch() + 12'000;
    while (QDateTime::currentMSecsSinceEpoch() < deadline) {
        if (knownPort > 0 && probe(m_host, knownPort, 200))
            return knownPort;
        const int read = readPortFile();
        if (read > 0 && probe(m_host, read, 200))
            return read;
        QThread::msleep(200);
    }
    throw BridgeSpawnException(
        QStringLiteral("其他进程正在拉起 Bridge，但等待超时未就绪。可检查 ~/.agentquay/spawn.lock 是否残留，"
                       "或手动运行 `agentquay start --daemon`"));
}

void BridgeSpawner::shutdown()
{
    if (!m_process)
        return;
    // 生命周期由 Bridge 自己管理（空闲自回收）：宿主退出不再 terminate/kill 子进程
    // ——见 embedded-bridge-lifecycle.md 步骤 1（QProcess 析构不会终止子进程）。
    delete m_process;
    m_process = nullptr;
}

QString BridgeSpawner::findBinary() const
{
    // 1. 环境变量显式指定
    const QString envBin = qEnvironmentVariable("AGENTQUAY_BRIDGE_BIN");
    if (fileExists(envBin))
        return envBin;
    const QString envDir = qEnvironmentVariable("AGENTQUAY_BRIDGE_DIR");
    if (!envDir.isEmpty()) {
        const QString inDir = findInDir(envDir);
        if (!inDir.isEmpty())
            return inDir;
    }

    // 2. 可执行文件目录下的 bridge_bin/（随包分发）
    const QString appDir = QCoreApplication::applicationDirPath();
    {
        const QString inBridgeBin = findInDir(QDir(appDir).filePath(QStringLiteral("bridge_bin")));
        if (!inBridgeBin.isEmpty())
            return inBridgeBin;
    }

    // 3. 向上回溯仓库布局：sdk/cpp/build/... → bridge/dist 或 bridge_bin（开发期）
    QDir cursor(appDir);
    for (int i = 0; i < 6 && !cursor.isRoot(); ++i) {
        const QString bridgeDist = cursor.filePath(QStringLiteral("bridge/dist"));
        const QString inDist = findInDir(bridgeDist);
        if (!inDist.isEmpty())
            return inDist;
        const QString bridgeBin = cursor.filePath(QStringLiteral("bridge_bin"));
        const QString inBin = findInDir(bridgeBin);
        if (!inBin.isEmpty())
            return inBin;
        if (!cursor.cdUp())
            break;
    }

    // 4. PATH 中的 agentquay
    const QString pathEnv = qEnvironmentVariable("PATH");
    const QStringList dirs = pathEnv.split(QDir::listSeparator(), Qt::SkipEmptyParts);
    for (const QString& dir : dirs) {
        const QString inPath = findInDir(dir);
        if (!inPath.isEmpty())
            return inPath;
    }
    return QString();
}

void BridgeSpawner::spawnEmbedded(const QString& binary)
{
    const QString logDir = QDir(homeDir()).filePath(QStringLiteral(".agentquay/logs"));
    QDir().mkpath(logDir);
    const QString logFile = QDir(logDir).filePath(QStringLiteral("agentquay.log"));

    QProcess* process = new QProcess(this);
    process->setProgram(binary);
    process->setArguments({QStringLiteral("serve"), QStringLiteral("--embedded")});
    process->setStandardOutputFile(logFile);
    process->setStandardErrorFile(logFile);
    QProcessEnvironment env = QProcessEnvironment::systemEnvironment();
    env.insert(QStringLiteral("AGENTQUAY_LOG_DIR"), logDir);
    process->setProcessEnvironment(env);
    process->start();
    if (!process->waitForStarted(5000)) {
        const QString err = process->errorString();
        delete process;
        throw BridgeSpawnException(QStringLiteral("拉起内嵌 Bridge 失败: %1").arg(err));
    }
    m_process = process;
}

} // namespace agentquay