// agentquay/BridgeSpawner.h — auto-spawn：检测本地 Bridge，未运行时自动拉起内嵌二进制
// （设计文档 §4.1）。对齐 Java / C# SDK 的 BridgeSpawner。
//
// 二进制查找顺序：
//   1. 环境变量 AGENTQUAY_BRIDGE_BIN（完整路径）
//   2. 环境变量 AGENTQUAY_BRIDGE_DIR（目录，含 agentquay[.exe]
//      及平台后缀名 agentquay-windows-amd64.exe 等）
//   3. 可执行文件目录下的 bridge_bin/（SDK/应用随包分发的内嵌二进制）
//   4. 向上回溯仓库布局中的 bridge/dist/（开发期）
//   5. PATH 中的 agentquay
// 拉起方式：{binary} serve --embedded，日志写入 ~/.agentquay/logs（AGENTQUAY_LOG_DIR）。
// 本进程拉起的 Bridge 子进程生命周期由 Bridge 自己管理（空闲自回收），SDK 退出不再杀子进程
// （embedded-bridge-lifecycle.md 步骤 1）。
#pragma once

#include <QObject>
#include <QString>

class QProcess;
class QTcpSocket;

namespace agentquay {

class BridgeSpawner : public QObject {
public:
    explicit BridgeSpawner(QString host, QObject* parent = nullptr);
    ~BridgeSpawner() override;

    /** 读取 ~/.agentquay/port（Bridge 写入的实际端口）；无/非法返回 0。 */
    static int readPortFile();

    /** TCP 探测端口是否可连接。 */
    static bool probe(const QString& host, int port, int timeoutMs);

    /**
     * 确保本地 Bridge 可用。
     * @param port      期望端口（0 = 从端口文件读取）
     * @param autoSpawn 未检测到服务时自动拉起内嵌 Bridge
     * @return 实际端口（0 表示不可用）
     * @throws BridgeUnavailableException / BridgeSpawnException
     */
    int ensureBridge(int port, bool autoSpawn);

    /** 终止本进程拉起的 Bridge 子进程（如有）。 */
    void shutdown();

private:
    QString findBinary() const;
    void spawnEmbedded(const QString& binary);

    // spawn 原子锁（步骤 3）：多应用同时首启时保证只有一个去拉起 Bridge
    static QString lockFilePath();
    static bool writeLockFile();
    static bool lockExpired();
    static bool acquireSpawnLock();
    static void releaseSpawnLock();
    int waitForAnyBridge(int knownPort);

    QString m_host;
    QProcess* m_process = nullptr;
};

} // namespace agentquay