using System.Diagnostics;
using System.Net.Sockets;
using System.Runtime.InteropServices;
using System.Text.Json;

namespace AgentQuay;

/// <summary>
/// auto-spawn：检测本地 Bridge，未运行时自动拉起内嵌二进制（设计文档 §4.1）。
///
/// <para>二进制查找顺序：环境变量 AGENTQUAY_BRIDGE_BIN / AGENTQUAY_BRIDGE_DIR →
/// 输出目录/仓库树中的 bridge_bin/agentquay[.exe] → PATH 中的 agentquay。
/// 拉起方式：{binary} serve --embedded，日志写入 ~/.agentquay/logs（AGENTQUAY_LOG_DIR）。</para>
/// </summary>
public sealed class BridgeSpawner
{
    private static readonly string HomeDir =
        Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);

    /// <summary>~/.agentquay/port（Bridge 写入的实际端口）。</summary>
    public static string PortFilePath => Path.Combine(HomeDir, ".agentquay", "port");

    /// <summary>本进程拉起的 Bridge 子进程。生命周期由 Bridge 自己管理（空闲自回收），
    /// 进程退出不再主动 Kill——见 embedded-bridge-lifecycle.md 步骤 1。</summary>
    private static volatile Process? _spawned;

    private readonly string _host;

    public BridgeSpawner(string host) => _host = host;

    /// <summary>读取 ~/.agentquay/port（Bridge 写入的实际端口）。</summary>
    public static int ReadPortFile()
    {
        try
        {
            if (int.TryParse(File.ReadAllText(PortFilePath).Trim(), out int port)
                && port > 0 && port < 65536)
            {
                return port;
            }
        }
        catch (Exception)
        {
            // 无端口文件 / 解析失败 → 0
        }
        return 0;
    }

    /// <summary>TCP 探测端口是否可连接。</summary>
    public static bool Probe(string host, int port, int timeoutMillis)
    {
        try
        {
            using var client = new TcpClient();
            var task = client.ConnectAsync(host, port);
            if (!task.Wait(timeoutMillis))
            {
                return false;
            }
            return client.Connected;
        }
        catch (Exception)
        {
            return false;
        }
    }

    // ------------------------------------------------------------------
    // spawn 原子锁（步骤 3）：多应用同时首启时保证只有一个去拉起 Bridge。
    // 锁文件内容为 {pid, startedAt}，与其他语言 SDK 的锁互认。

    private static readonly string LockPath = Path.Combine(HomeDir, ".agentquay", "spawn.lock");
    private const long LockTtlMs = 15_000;
    private const long SpawnWaitMs = 12_000;

    /// <summary>独占创建锁文件；已有锁返回 false。</summary>
    private static bool WriteLockFile()
    {
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(LockPath)!);
            using var fs = new FileStream(LockPath, FileMode.CreateNew, FileAccess.Write);
            using var w = new StreamWriter(fs);
            w.Write($"{{\"pid\":{Environment.ProcessId},\"startedAt\":{Environment.TickCount64}}}");
            return true;
        }
        catch (IOException)
        {
            return false; // 已有锁
        }
        catch (UnauthorizedAccessException)
        {
            return false;
        }
    }

    private static bool LockExpired()
    {
        try
        {
            using var doc = JsonDocument.Parse(File.ReadAllText(LockPath));
            long startedAt = 0;
            if (doc.RootElement.TryGetProperty("startedAt", out var s) && s.TryGetInt64(out long v))
            {
                startedAt = v;
            }
            return Environment.TickCount64 - startedAt > LockTtlMs;
        }
        catch
        {
            return true; // 缺失/损坏一律视为已失效
        }
    }

    private static bool AcquireSpawnLock()
    {
        if (WriteLockFile())
        {
            return true;
        }
        if (!LockExpired())
        {
            return false; // 未过期：另有实例正在拉起
        }
        try
        {
            File.Delete(LockPath); // 过期（崩溃残留）：打破后重试一次
        }
        catch { }
        return WriteLockFile();
    }

    private static void ReleaseSpawnLock()
    {
        try
        {
            File.Delete(LockPath);
        }
        catch { }
    }

    /// <summary>未抢到锁时等待别的应用拉起的 Bridge 端口文件就绪，直接复用。</summary>
    private int WaitForAnyBridge(int knownPort)
    {
        long deadline = Environment.TickCount64 + SpawnWaitMs;
        while (Environment.TickCount64 < deadline)
        {
            if (knownPort > 0 && Probe(_host, knownPort, 200))
            {
                return knownPort;
            }
            int read = ReadPortFile();
            if (read > 0 && Probe(_host, read, 200))
            {
                return read;
            }
            Thread.Sleep(200);
        }
        throw new BridgeSpawnException(
            "其他进程正在拉起 Bridge，但等待超时未就绪。可检查 ~/.agentquay/spawn.lock 是否残留，或手动运行 `agentquay start --daemon`");
    }

    /// <summary>
    /// 确保本地 Bridge 可用。
    /// </summary>
    /// <param name="port">期望端口（0 = 从端口文件读取）。</param>
    /// <param name="autoSpawn">未检测到服务时自动拉起内嵌 Bridge。</param>
    /// <returns>实际端口（0 表示不可用）。</returns>
    public int EnsureBridge(int port, bool autoSpawn)
    {
        int actual = port;
        if (actual == 0)
        {
            actual = ReadPortFile();
        }
        if (actual > 0 && Probe(_host, actual, 300))
        {
            return actual;
        }
        if (autoSpawn)
        {
            string? binary = FindBinary();
            if (binary != null)
            {
                if (AcquireSpawnLock())
                {
                    try
                    {
                        // 拿到锁后复查：竞态窗口内可能有别的实例已拉起
                        if (actual > 0 && Probe(_host, actual, 200))
                        {
                            return actual;
                        }
                        int existing = ReadPortFile();
                        if (existing > 0 && Probe(_host, existing, 200))
                        {
                            return existing;
                        }
                        Console.WriteLine($"[AgentQuay] 本地无 Bridge 服务，拉起内嵌 Bridge: {binary}");
                        try
                        {
                            SpawnEmbedded(binary);
                        }
                        catch (Exception e)
                        {
                            throw new BridgeSpawnException($"拉起内嵌 Bridge 失败: {e.Message}", e);
                        }
                        long deadline = Environment.TickCount64 + 12_000;
                        while (Environment.TickCount64 < deadline)
                        {
                            if (actual > 0 && Probe(_host, actual, 200))
                            {
                                return actual;
                            }
                            int read = ReadPortFile();
                            if (read > 0 && Probe(_host, read, 200))
                            {
                                return read;
                            }
                            Thread.Sleep(200);
                        }
                        throw new BridgeSpawnException(
                            "内嵌 Bridge 启动超时。可尝试: 1) 手动运行 `agentquay start --daemon`; " +
                            "2) 检查端口占用与日志");
                    }
                    finally
                    {
                        ReleaseSpawnLock();
                    }
                }
                // 未抢到锁：别的应用正在拉起，等待其端口文件就绪后直接复用
                return WaitForAnyBridge(actual);
            }
            throw new BridgeUnavailableException(
                "本地未检测到 AgentQuay Bridge，且未找到 agentquay 二进制。" +
                "两种出路: 1) 安装系统服务（agentquay start --daemon）; " +
                "2) 将 agentquay 放入 PATH 或随 SDK 分发内嵌二进制");
        }
        throw new BridgeUnavailableException(
            $"本地端口 {(actual > 0 ? actual.ToString() : "(未找到端口文件)")} 无 AgentQuay Bridge 服务，" +
            "且 autoSpawnBridge=false。请先运行 `agentquay start --daemon` 或开启 autoSpawn");
    }

    // ------------------------------------------------------------------

    /// <summary>当前平台的候选二进制名：主名 agentquay-&lt;os&gt;-&lt;arch&gt;[.exe]，随后兼容旧命名。</summary>
    private static IEnumerable<string> BinaryCandidates()
    {
        string osName = OperatingSystem.IsWindows() ? "windows"
            : OperatingSystem.IsMacOS() ? "darwin" : "linux";
        string archName = RuntimeInformation.OSArchitecture == Architecture.Arm64 ? "arm64" : "amd64";
        string ext = osName == "windows" ? ".exe" : "";
        yield return $"agentquay-{osName}-{archName}{ext}";
        yield return osName == "windows" ? "agentquay.exe" : "agentquay";
    }

    private static string? FindBinary()
    {
        // 1. 环境变量显式指定
        string? env = Environment.GetEnvironmentVariable("AGENTQUAY_BRIDGE_BIN");
        if (!string.IsNullOrEmpty(env) && File.Exists(env))
        {
            return env;
        }
        env = Environment.GetEnvironmentVariable("AGENTQUAY_BRIDGE_DIR");
        if (!string.IsNullOrEmpty(env))
        {
            foreach (string name in BinaryCandidates())
            {
                string candidate = Path.Combine(env, name);
                if (File.Exists(candidate))
                {
                    return candidate;
                }
            }
        }

        // 2. 输出目录 bridge_bin（NuGet contentFiles / CopyToOutputDirectory 分发）
        string baseDir = AppContext.BaseDirectory;
        foreach (string name in BinaryCandidates())
        {
            string? direct = JoinIfExists(baseDir, "bridge_bin", name);
            if (direct != null)
            {
                return direct;
            }
        }

        // 3. 仓库开发布局：向上回溯查找 bridge_bin（tests/bin/Debug/net8.0 → 仓库根）
        var cursor = new DirectoryInfo(baseDir);
        for (int i = 0; i < 8 && cursor != null; i++)
        {
            foreach (string name in BinaryCandidates())
            {
                string? found = JoinIfExists(cursor.FullName, "bridge_bin", name);
                if (found != null)
                {
                    return found;
                }
            }
            cursor = cursor.Parent;
        }

        // 4. PATH 中的 agentquay
        string? pathEnv = Environment.GetEnvironmentVariable("PATH");
        if (!string.IsNullOrEmpty(pathEnv))
        {
            foreach (string dir in pathEnv.Split(Path.PathSeparator, StringSplitOptions.RemoveEmptyEntries))
            {
                foreach (string name in BinaryCandidates())
                {
                    string candidate = Path.Combine(dir, name);
                    if (File.Exists(candidate))
                    {
                        return candidate;
                    }
                }
            }
        }
        return null;
    }

    private static string? JoinIfExists(string baseDir, string sub, string file)
    {
        try
        {
            string candidate = Path.Combine(baseDir, sub, file);
            return File.Exists(candidate) ? candidate : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    private static void SpawnEmbedded(string binary)
    {
        string logDir = Path.Combine(HomeDir, ".agentquay", "logs");
        Directory.CreateDirectory(logDir);
        string logFile = Path.Combine(logDir, "agentquay.log");

        var psi = new ProcessStartInfo
        {
            FileName = binary,
            UseShellExecute = false,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        psi.ArgumentList.Add("serve");
        psi.ArgumentList.Add("--embedded");
        psi.Environment["AGENTQUAY_LOG_DIR"] = logDir;

        var process = Process.Start(psi) ?? throw new IOException("无法启动 Bridge 进程: " + binary);
        // 后台排空 stdout/stderr 到日志文件（避免子进程管道缓冲区阻塞；进程退出后结束）
        _ = Task.Run(() =>
        {
            try { File.AppendAllText(logFile, "\n[bridge stdout]\n" + (process.StandardOutput.ReadToEnd() ?? "")); } catch { }
        });
        _ = Task.Run(() =>
        {
            try { File.AppendAllText(logFile, "\n[bridge stderr]\n" + (process.StandardError.ReadToEnd() ?? "")); } catch { }
        });
        _spawned = process;
    }
}