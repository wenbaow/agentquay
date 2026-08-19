/**
 * auto-spawn：检测本地 Bridge，未运行时自动拉起内嵌二进制（设计文档 §4.1）。
 *
 * 流程：读 ~/.agentquay/port → TCP 探测 → 未运行且 autoSpawnBridge=true →
 * 查找内嵌二进制（随包分发 bridge_bin/）或 PATH 中的 agentquay →
 * 以 `serve --embedded` 拉起 → 等待端口就绪。
 */
import { spawn, type ChildProcess } from "node:child_process";
import * as fs from "node:fs";
import * as net from "node:net";
import * as os from "node:os";
import * as path from "node:path";
import { BridgeSpawnError, BridgeUnavailableError } from "./errors";

const PORT_FILE = path.join(os.homedir(), ".agentquay", "port");

/** 内嵌二进制目录（npm 包根 bridge_bin/；源码运行时为 sdk/typescript/bridge_bin/）。 */
const BRIDGE_BIN_DIRS = [
  path.join(__dirname, "..", "bridge_bin"),
  path.join(__dirname, "bridge_bin"),
];

/** 架构映射（node os.arch() → 产物架构后缀）。 */
const ARCH_MAP: Record<string, string> = { x64: "amd64", arm64: "arm64", ia32: "386" };

/**
 * 当前平台/架构下的内嵌二进制候选名，按优先级排列：
 * 主名 agentquay-<os>-<arch>[.exe]，随后兼容无架构后缀的旧命名。
 */
function platformBinNames(): string[] {
  const osName = process.platform === "win32" ? "windows" : process.platform; // darwin / linux
  const arch = ARCH_MAP[process.arch] ?? process.arch;
  const names = [`agentquay-${osName}-${arch}${osName === "windows" ? ".exe" : ""}`];
  if (osName === "windows") names.push("agentquay.exe");
  else names.push(`agentquay-${osName}`, "agentquay");
  return names;
}

/**
 * 本进程拉起的 Bridge 子进程。生命周期由 Bridge 自己管理（空闲自回收），
 * 宿主退出不再主动 kill——见 embedded-bridge-lifecycle.md 步骤 1。
 */
let spawned: ChildProcess | null = null;

/** 读取 ~/.agentquay/port（Bridge 写入的实际端口）。 */
export function readPortFile(): number | null {
  try {
    const text = fs.readFileSync(PORT_FILE, "utf8").trim();
    const port = Number(text);
    return Number.isInteger(port) && port > 0 && port < 65536 ? port : null;
  } catch {
    return null;
  }
}

/** TCP 探测端口是否可连接。 */
export function probe(host: string, port: number, timeoutMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = net.connect({ host, port });
    const done = (ok: boolean) => {
      socket.destroy();
      resolve(ok);
    };
    socket.setTimeout(timeoutMs);
    socket.once("connect", () => done(true));
    socket.once("timeout", () => done(false));
    socket.once("error", () => done(false));
  });
}

/** 查找可用的 Bridge 二进制：环境变量覆盖 → 内嵌 → PATH。 */
export function findBridgeBinary(): string | null {
  const env = process.env.AGENTQUAY_BRIDGE_BIN;
  if (env && fs.existsSync(env)) {
    return env;
  }
  const candidates = platformBinNames();
  for (const name of candidates) {
    for (const dir of BRIDGE_BIN_DIRS) {
      const p = path.join(dir, name);
      if (fs.existsSync(p)) {
        return p;
      }
    }
  }
  for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
    if (!dir) {
      continue;
    }
    for (const name of ["agentquay", "agentquay.exe", "agentquay.cmd"]) {
      const p = path.join(dir, name);
      if (fs.existsSync(p)) {
        return p;
      }
    }
  }
  return null;
}

/**
 * 以 `serve --embedded` 模式拉起 Bridge，日志写入 ~/.agentquay/logs/agentquay.log。
 * 进程退出时由模块级 hook 终止（随应用退出而停止）。
 */
export function spawnEmbedded(binary: string): ChildProcess {
  const logDir = path.join(os.homedir(), ".agentquay", "logs");
  fs.mkdirSync(logDir, { recursive: true });
  const logFd = fs.openSync(path.join(logDir, "agentquay.log"), "a");
  const proc = spawn(binary, ["serve", "--embedded"], {
    stdio: ["ignore", logFd, logFd],
    env: { ...process.env, AGENTQUAY_LOG_DIR: logDir },
    windowsHide: true, // Windows 上不弹出控制台窗口
  });
  proc.on("exit", () => {
    try {
      fs.closeSync(logFd);
    } catch {
      // 已关闭
    }
    if (spawned === proc) {
      spawned = null;
    }
  });
  proc.on("error", (err) => {
    console.warn(`[agentquay] 内嵌 Bridge 启动失败: ${err.message}`);
  });
  spawned = proc;
  return proc;
}

/**
 * 确保本地 Bridge 可用，返回 (实际端口, 已拉起的子进程或 null)。
 *
 * @param host 连接地址
 * @param port 期望端口（0 = 从端口文件读取）
 * @param autoSpawn 未检测到服务时自动拉起内嵌 Bridge
 * @throws BridgeUnavailableError 无法获得可用 Bridge
 */
export async function ensureBridge(
  host: string,
  port: number,
  autoSpawn: boolean,
): Promise<{ port: number; proc: ChildProcess | null }> {
  let actual = port;
  if (actual === 0) {
    actual = readPortFile() ?? 0;
  }

  // 1. 已有端口 → 直接探测
  if (actual > 0 && (await probe(host, actual, 300))) {
    return { port: actual, proc: null };
  }

  // 2. 自动拉起（带 spawn 原子锁：同一时刻只有一个进程真正去拉起，其余等待复用）
  if (autoSpawn) {
    const binary = findBridgeBinary();
    if (binary) {
      if (acquireSpawnLock()) {
        try {
          // 拿到锁后复查：竞态窗口内可能有别的实例已拉起
          if (actual > 0 && (await probe(host, actual, 200))) {
            return { port: actual, proc: null };
          }
          const existing = readPortFile();
          if (existing && (await probe(host, existing, 200))) {
            return { port: existing, proc: null };
          }
          const proc = spawnEmbedded(binary);
          // 等待端口文件出现（Bridge 启动后写入实际端口）
          const deadline = Date.now() + 12_000;
          while (Date.now() < deadline) {
            if (actual > 0 && (await probe(host, actual, 200))) {
              return { port: actual, proc };
            }
            const read = readPortFile();
            if (read && (await probe(host, read, 200))) {
              return { port: read, proc };
            }
            await sleep(200);
          }
          try {
            proc.kill();
          } catch {
            // 已退出
          }
          throw new BridgeSpawnError(
            "内嵌 Bridge 启动超时。可尝试: 1) 手动运行 `agentquay start --daemon` 安装系统服务; " +
              "2) 检查端口占用与日志",
          );
        } finally {
          releaseSpawnLock();
        }
      }
      // 未抢到锁：别的应用正在拉起，等待其端口文件就绪后直接复用
      return waitForAnyBridge(host, actual);
    }
    throw new BridgeUnavailableError(
      "本地未检测到 AgentQuay Bridge，且未找到 agentquay 二进制。" +
        "两种出路: 1) 安装系统服务（agentquay start --daemon）; " +
        "2) 将 agentquay 放入 PATH 或随 SDK 分发内嵌二进制",
    );
  }

  throw new BridgeUnavailableError(
    `本地端口 ${actual || "(未找到端口文件)"} 无 AgentQuay Bridge 服务，` +
      "且 autoSpawnBridge=false。请先运行 `agentquay start --daemon` 或开启 autoSpawn",
  );
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ---------------------------------------------------------------------------
// spawn 原子锁（步骤 3）：多应用同时首启时保证只有一个去拉起 Bridge。

const SPAWN_LOCK_PATH = path.join(os.homedir(), ".agentquay", "spawn.lock");
const SPAWN_LOCK_TTL_MS = 15_000;
const SPAWN_WAIT_MS = 12_000;

function lockExpired(): boolean {
  try {
    const data = JSON.parse(fs.readFileSync(SPAWN_LOCK_PATH, "utf8"));
    return Date.now() - Number(data.startedAt ?? 0) > SPAWN_LOCK_TTL_MS;
  } catch {
    return true; // 缺失/损坏一律视为已失效
  }
}

function acquireSpawnLock(): boolean {
  fs.mkdirSync(path.dirname(SPAWN_LOCK_PATH), { recursive: true });
  const write = (): boolean => {
    try {
      const fd = fs.openSync(SPAWN_LOCK_PATH, "wx"); // O_EXCL 独占创建
      try {
        fs.writeFileSync(fd, JSON.stringify({ pid: process.pid, startedAt: Date.now() }));
      } finally {
        fs.closeSync(fd);
      }
      return true;
    } catch {
      return false; // 已有锁
    }
  };
  if (write()) return true;
  if (!lockExpired()) return false; // 未过期：另有实例正在拉起
  // 过期（崩溃残留）：打破后重试一次
  try {
    fs.unlinkSync(SPAWN_LOCK_PATH);
  } catch {
    /* 已被移除 */
  }
  return write();
}

function releaseSpawnLock(): void {
  try {
    fs.unlinkSync(SPAWN_LOCK_PATH);
  } catch {
    /* 已被移除 */
  }
}

async function waitForAnyBridge(
  host: string,
  knownPort: number,
): Promise<{ port: number; proc: null }> {
  const deadline = Date.now() + SPAWN_WAIT_MS;
  while (Date.now() < deadline) {
    if (knownPort > 0 && (await probe(host, knownPort, 200))) {
      return { port: knownPort, proc: null };
    }
    const read = readPortFile();
    if (read && (await probe(host, read, 200))) {
      return { port: read, proc: null };
    }
    await sleep(200);
  }
  throw new BridgeSpawnError(
    "其他进程正在拉起 Bridge，但等待超时未就绪。可检查 ~/.agentquay/spawn.lock 是否残留，或手动运行 `agentquay start --daemon`",
  );
}
