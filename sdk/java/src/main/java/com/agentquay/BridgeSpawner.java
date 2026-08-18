package com.agentquay;

import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileAlreadyExistsException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardCopyOption;
import java.nio.file.StandardOpenOption;
import java.util.ArrayList;
import java.util.List;
import java.util.logging.Level;
import java.util.logging.Logger;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * auto-spawn：检测本地 Bridge，未运行时自动拉起内嵌二进制（设计文档 §4.1）。
 *
 * <p>二进制查找顺序：Maven 包资源 {@code bridge_bin/agentquay[.exe]}（解压到
 * 用户缓存目录）→ PATH 中的 {@code agentquay}。拉起方式：
 * {@code agentquay serve --embedded}，日志写入应用缓存目录。
 */
public final class BridgeSpawner {

    private static final Logger LOG = Logger.getLogger(BridgeSpawner.class.getName());

    private static final Path PORT_FILE = Paths.get(
            System.getProperty("user.home"), ".agentquay", "port");

    /** 本进程拉起的 Bridge 子进程。生命周期由 Bridge 自己管理（空闲自回收），
     *  JVM 退出不再主动 destroy——见 embedded-bridge-lifecycle.md 步骤 1。 */
    private static volatile Process spawned;

    private final String host;

    public BridgeSpawner(String host) {
        this.host = host;
    }

    /** 读取 ~/.agentquay/port（Bridge 写入的实际端口）。 */
    public static int readPortFile() {
        try {
            String text = new String(Files.readAllBytes(PORT_FILE)).trim();
            int port = Integer.parseInt(text);
            return port > 0 && port < 65536 ? port : 0;
        } catch (Exception e) {
            return 0;
        }
    }

    /** TCP 探测端口是否可连接。 */
    public static boolean probe(String host, int port, int timeoutMillis) {
        try (Socket socket = new Socket()) {
            socket.connect(new InetSocketAddress(host, port), timeoutMillis);
            return true;
        } catch (IOException e) {
            return false;
        }
    }

    /**
     * 确保本地 Bridge 可用。
     *
     * @param port      期望端口（0 = 从端口文件读取）
     * @param autoSpawn 未检测到服务时自动拉起内嵌 Bridge
     * @return 实际端口（0 表示不可用）
     */
    public int ensureBridge(int port, boolean autoSpawn) {
        int actual = port;
        if (actual == 0) {
            actual = readPortFile();
        }
        if (actual > 0 && probe(host, actual, 300)) {
            return actual;
        }
        if (autoSpawn) {
            Path binary = findBinary();
            if (binary != null) {
                if (acquireSpawnLock()) {
                    try {
                        // 拿到锁后复查：竞态窗口内可能有别的实例已拉起
                        if (actual > 0 && probe(host, actual, 200)) {
                            return actual;
                        }
                        int existing = readPortFile();
                        if (existing > 0 && probe(host, existing, 200)) {
                            return existing;
                        }
                        LOG.info("本地无 Bridge 服务，拉起内嵌 Bridge: " + binary);
                        try {
                            spawnEmbedded(binary);
                        } catch (IOException e) {
                            throw new AgentQuayException.BridgeSpawnException(
                                    "拉起内嵌 Bridge 失败: " + e.getMessage(), e);
                        }
                        long deadline = System.currentTimeMillis() + 12_000;
                        while (System.currentTimeMillis() < deadline) {
                            if (actual > 0 && probe(host, actual, 200)) {
                                return actual;
                            }
                            int read = readPortFile();
                            if (read > 0 && probe(host, read, 200)) {
                                return read;
                            }
                            sleep(200);
                        }
                        throw new AgentQuayException.BridgeSpawnException(
                                "内嵌 Bridge 启动超时。可尝试: 1) 手动运行 `agentquay start --daemon`; "
                                        + "2) 检查端口占用与日志");
                    } finally {
                        releaseSpawnLock();
                    }
                }
                // 未抢到锁：别的应用正在拉起，等待其端口文件就绪后直接复用
                return waitForAnyBridge(actual);
            }
            throw new AgentQuayException.BridgeUnavailableException(
                    "本地未检测到 AgentQuay Bridge，且未找到 agentquay 二进制。"
                            + "两种出路: 1) 安装系统服务（agentquay start --daemon）; "
                            + "2) 将 agentquay 放入 PATH 或随 SDK 分发内嵌二进制");
        }
        throw new AgentQuayException.BridgeUnavailableException(
                "本地端口 " + (actual > 0 ? actual : "(未找到端口文件)")
                        + " 无 AgentQuay Bridge 服务，且 autoSpawnBridge=false。"
                        + "请先运行 `agentquay start --daemon` 或开启 autoSpawn");
    }

    // ------------------------------------------------------------------

    // ------------------------------------------------------------------
    // spawn 原子锁（步骤 3）：多应用同时首启时保证只有一个去拉起 Bridge。
    // 锁文件内容为 {pid, startedAt}，与 Python/TS/.NET/Rust 的锁互认。

    private static final ObjectMapper LOCK_MAPPER = new ObjectMapper();
    private static final Path LOCK_FILE = Paths.get(
            System.getProperty("user.home"), ".agentquay", "spawn.lock");
    private static final long LOCK_TTL_MS = 15_000L;
    private static final long SPAWN_WAIT_MS = 12_000L;

    /** 独占创建锁文件；已有锁返回 false。 */
    private static boolean writeLockFile() {
        try {
            Files.createDirectories(LOCK_FILE.getParent());
            Files.write(LOCK_FILE,
                    ("{\"pid\":" + ProcessHandle.current().pid()
                            + ",\"startedAt\":" + System.currentTimeMillis() + "}")
                            .getBytes(StandardCharsets.UTF_8),
                    StandardOpenOption.CREATE_NEW, StandardOpenOption.WRITE);
            return true;
        } catch (FileAlreadyExistsException e) {
            return false;
        } catch (IOException e) {
            return false;
        }
    }

    private static boolean lockExpired() {
        try {
            if (!Files.exists(LOCK_FILE)) {
                return true; // 缺失视为已失效
            }
            JsonNode node = LOCK_MAPPER.readTree(LOCK_FILE.toFile());
            long startedAt = node.path("startedAt").asLong(0);
            return System.currentTimeMillis() - startedAt > LOCK_TTL_MS;
        } catch (Exception e) {
            return true; // 损坏/解析失败一律视为已失效
        }
    }

    private static boolean acquireSpawnLock() {
        if (writeLockFile()) {
            return true;
        }
        if (!lockExpired()) {
            return false; // 未过期：另有实例正在拉起
        }
        try {
            Files.deleteIfExists(LOCK_FILE); // 过期（崩溃残留）：打破后重试一次
        } catch (IOException ignored) { }
        return writeLockFile();
    }

    private static void releaseSpawnLock() {
        try {
            Files.deleteIfExists(LOCK_FILE);
        } catch (IOException ignored) { }
    }

    /** 未抢到锁时等待别的应用拉起的 Bridge 端口文件就绪，直接复用。 */
    private int waitForAnyBridge(int knownPort) {
        long deadline = System.currentTimeMillis() + SPAWN_WAIT_MS;
        while (System.currentTimeMillis() < deadline) {
            if (knownPort > 0 && probe(host, knownPort, 200)) {
                return knownPort;
            }
            int read = readPortFile();
            if (read > 0 && probe(host, read, 200)) {
                return read;
            }
            sleep(200);
        }
        throw new AgentQuayException.BridgeSpawnException(
                "其他进程正在拉起 Bridge，但等待超时未就绪。可检查 ~/.agentquay/spawn.lock 是否残留，"
                        + "或手动运行 `agentquay start --daemon`");
    }

    private static Path findBinary() {
        // 1. Maven 包资源 bridge_bin/agentquay[.exe]
        String name = System.getProperty("os.name", "").toLowerCase().contains("win")
                ? "agentquay.exe" : "agentquay";
        try (InputStream in = BridgeSpawner.class.getResourceAsStream("/bridge_bin/" + name)) {
            if (in != null) {
                Path target = Paths.get(System.getProperty("java.io.tmpdir"),
                        "agentquay", "bridge_bin", name);
                Files.createDirectories(target.getParent());
                Files.copy(in, target, StandardCopyOption.REPLACE_EXISTING);
                return target;
            }
        } catch (IOException e) {
            LOG.log(Level.WARNING, "解压内嵌 Bridge 失败", e);
        }
        // 2. PATH 中的 agentquay
        String pathEnv = System.getenv("PATH");
        if (pathEnv != null) {
            for (String dir : pathEnv.split(File.pathSeparator)) {
                File candidate = new File(dir, name);
                if (candidate.canExecute()) {
                    return candidate.toPath();
                }
            }
        }
        return null;
    }

    private static void spawnEmbedded(Path binary) throws IOException {
        Path logDir = Paths.get(System.getProperty("user.home"), ".agentquay", "logs");
        Files.createDirectories(logDir);
        Path logFile = logDir.resolve("agentquay.log");

        List<String> cmd = new ArrayList<>();
        cmd.add(binary.toString());
        cmd.add("serve");
        cmd.add("--embedded");

        ProcessBuilder pb = new ProcessBuilder(cmd);
        pb.redirectOutput(logFile.toFile());
        pb.redirectError(logFile.toFile());
        pb.environment().put("AGENTQUAY_LOG_DIR", logDir.toString());
        spawned = pb.start();
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
