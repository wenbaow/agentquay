"""auto-spawn：检测本地 Bridge，未运行时自动拉起内嵌二进制（设计文档 §4.1）。

流程：读 ~/.agentquay/port → TCP 探测 → 未运行且 auto_spawn_bridge=True →
查找内嵌二进制（随包分发）或 PATH 中的 agentquay → 以 --embedded 拉起 →
等待端口就绪。
"""

from __future__ import annotations

import json
import logging
import os
import platform
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path

from .errors import BridgeSpawnError, BridgeUnavailableError

logger = logging.getLogger("agentquay")

PORT_FILE = Path.home() / ".agentquay" / "port"

# spawn 原子锁：~/.agentquay/spawn.lock（步骤 3：多应用同时首启时保证只有一个去拉起 Bridge）
# startedAt 统一为 epoch 毫秒（与 TS/Rust/Java/C++ 互认；C# 同用 epoch 毫秒）
SPAWN_LOCK = Path.home() / ".agentquay" / "spawn.lock"
SPAWN_LOCK_TTL_MS = 15_000.0  # 崩溃残留锁的过期时间，超过可抢占
SPAWN_WAIT_SECONDS = 12.0


def read_port_file() -> int | None:
    """读取 ~/.agentquay/port（Bridge 写入的实际端口）。"""
    try:
        text = PORT_FILE.read_text(encoding="utf-8").strip()
        port = int(text)
        return port if 0 < port < 65536 else None
    except (FileNotFoundError, ValueError):
        return None


def probe(host: str, port: int, timeout: float = 0.3) -> bool:
    """TCP 探测端口是否可连接。"""
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def _bundled_binary() -> Path | None:
    """随包分发的 Bridge 二进制：agentquay/bridge_bin/agentquay-<os>-<arch>[.exe]。"""
    system = "windows" if os.name == "nt" else sys.platform  # darwin / linux
    arch = {
        "x86_64": "amd64",
        "amd64": "amd64",
        "aarch64": "arm64",
        "arm64": "arm64",
    }.get(platform.machine().lower(), "amd64")
    names = [f"agentquay-{system}-{arch}" + (".exe" if system == "windows" else "")]
    # 兼容无架构后缀的旧命名（新包不再分发，仅为旧安装兼容）
    names.append({
        "windows": "agentquay.exe",
        "darwin": "agentquay-darwin",
        "linux": "agentquay-linux",
    }.get(system, ""))
    for name in names:
        if not name:
            continue
        candidate = Path(__file__).resolve().parent / "bridge_bin" / name
        if candidate.exists():
            _ensure_executable(candidate)
            return candidate
    return None


def _ensure_executable(path: Path) -> None:
    """确保内嵌二进制可执行：wheel/npm 等打包链路可能丢失 Unix 权限位。

    只读安装（系统级 site-packages）下 chmod 会失败，忽略即可——此时交给
    spawn 报错并提示 AGENTQUAY_BRIDGE_BIN 兜底。
    """
    if os.name == "nt":
        return
    try:
        path.chmod(path.stat().st_mode | 0o111)
    except OSError:
        pass


def find_bridge_binary() -> Path | None:
    """查找可用的 Bridge 二进制：内嵌优先，其次 PATH。"""
    bundled = _bundled_binary()
    if bundled:
        return bundled
    path = shutil.which("agentquay")
    return Path(path) if path else None


def spawn_embedded(binary: Path, log_dir: Path | None = None) -> subprocess.Popen:
    """以 --embedded 模式拉起 Bridge，日志写入应用缓存目录。

    :param binary: Bridge 可执行文件路径
    :param log_dir: 日志目录（缺省 ~/.agentquay/logs）
    """
    log_dir = log_dir or (Path.home() / ".agentquay" / "logs")
    log_dir.mkdir(parents=True, exist_ok=True)

    env = dict(os.environ)
    env["AGENTQUAY_LOG_DIR"] = str(log_dir)

    log_file = open(log_dir / "agentquay.log", "ab")
    # Windows 上脱离控制台并隐藏新建的控制台窗口（桌面 GUI 应用拉起嵌入式桥时
    # 不得弹出终端窗口），否则子进程是控制台子系统会闪现黑窗口
    creationflags = 0
    if os.name == "nt":
        creationflags = subprocess.CREATE_NEW_PROCESS_GROUP | getattr(subprocess, "CREATE_NO_WINDOW", 0)
    try:
        proc = subprocess.Popen(
            [str(binary), "serve", "--embedded"],
            stdout=log_file,
            stderr=log_file,
            env=env,
            creationflags=creationflags,
        )
    except OSError as exc:
        log_file.close()
        raise BridgeSpawnError(f"拉起内嵌 Bridge 失败: {exc}") from exc
    return proc


def wait_for_port(host: str, port: int, timeout: float = 10.0) -> bool:
    """等待端口就绪。"""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if probe(host, port):
            return True
        time.sleep(0.2)
    return False


# ---------------------------------------------------------------------------
# spawn 原子锁（步骤 3）：多应用同时探测到"无 Bridge"时的竞态消除。

def _write_lock() -> bool:
    """原子创建锁文件；已有锁返回 False。"""
    SPAWN_LOCK.parent.mkdir(parents=True, exist_ok=True)
    flags = os.O_CREAT | os.O_EXCL | os.O_WRONLY
    try:
        fd = os.open(SPAWN_LOCK, flags)
    except FileExistsError:
        return False
    with os.fdopen(fd, "w") as f:
        f.write(json.dumps({"pid": os.getpid(), "startedAt": time.time() * 1000}))
    return True


def _lock_expired() -> bool:
    """锁文件是否已超时（崩溃残留可被抢占）。"""
    try:
        data = json.loads(SPAWN_LOCK.read_text(encoding="utf-8"))
        return time.time() * 1000 - float(data.get("startedAt", 0)) > SPAWN_LOCK_TTL_MS
    except Exception:
        return True  # 缺失/损坏一律视为已失效


def _release_lock() -> None:
    try:
        SPAWN_LOCK.unlink()
    except FileNotFoundError:
        pass


def acquire_spawn_lock() -> bool:
    """抢占 spawn 锁；过期锁会被打破并重试一次。失败说明另有实例正在拉起。"""
    if _write_lock():
        return True
    if not _lock_expired():
        return False
    try:
        SPAWN_LOCK.unlink()
    except FileNotFoundError:
        pass
    return _write_lock()


def _wait_for_any_bridge(host: str, known_port: int, timeout: float) -> tuple[int, None]:
    """未抢到锁时等待其他实例拉起的 Bridge 端口文件就绪，直接复用。"""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if known_port > 0 and probe(host, known_port):
            return known_port, None
        read_port = read_port_file()
        if read_port and probe(host, read_port):
            return read_port, None
        time.sleep(0.2)
    raise BridgeSpawnError(
        "其他进程正在拉起 Bridge，但等待超时未就绪。可尝试: 1) 检查 ~/.agentquay/spawn.lock 是否残留; "
        "2) 手动运行 `agentquay start --daemon`"
    )


def ensure_bridge(host: str, port: int, auto_spawn: bool) -> tuple[int, subprocess.Popen | None]:
    """确保本地 Bridge 可用，返回 (实际端口, 已拉起的子进程或 None)。

    :param host: 连接地址
    :param port: 期望端口（0 = 从端口文件读取）
    :param auto_spawn: 未检测到服务时自动拉起内嵌 Bridge
    :raises BridgeUnavailableError: 无法获得可用 Bridge
    """
    actual_port = port
    if actual_port == 0:
        actual_port = read_port_file() or 0

    # 1. 已有端口 → 直接探测
    if actual_port > 0 and probe(host, actual_port):
        return actual_port, None

    # 2. 自动拉起（带 spawn 原子锁：同一时刻只有一个进程真正去拉起，其余等待复用）
    if auto_spawn:
        binary = find_bridge_binary()
        if binary:
            if acquire_spawn_lock():
                try:
                    # 拿到锁后复查：竞态窗口内可能有别的实例已拉起
                    if actual_port > 0 and probe(host, actual_port):
                        return actual_port, None
                    read_port = read_port_file()
                    if read_port and probe(host, read_port):
                        return read_port, None
                    logger.info("本地无 Bridge 服务，拉起内嵌 Bridge: %s", binary)
                    try:
                        proc = spawn_embedded(binary)
                    except BridgeSpawnError:
                        raise
                    # 等待端口文件出现（Bridge 启动后写入实际端口）
                    deadline = time.monotonic() + 12
                    while time.monotonic() < deadline:
                        if actual_port > 0 and probe(host, actual_port):
                            return actual_port, proc
                        read_port = read_port_file()
                        if read_port and probe(host, read_port):
                            return read_port, proc
                        time.sleep(0.2)
                    proc.terminate()
                    raise BridgeSpawnError(
                        "内嵌 Bridge 启动超时。可尝试: 1) 手动运行 `agentquay start --daemon` 安装系统服务; "
                        "2) 检查端口占用与日志"
                    )
                finally:
                    _release_lock()
            # 未抢到锁：别的应用正在拉起，等待其端口文件就绪后直接复用
            return _wait_for_any_bridge(host, actual_port, SPAWN_WAIT_SECONDS)
        raise BridgeUnavailableError(
            "本地未检测到 AgentQuay Bridge，且未找到 agentquay 二进制。"
            "两种出路: 1) 安装系统服务（agentquay start --daemon）; "
            "2) 将 agentquay 放入 PATH 或随 SDK 分发内嵌二进制"
        )

    raise BridgeUnavailableError(
        f"本地端口 {actual_port or '(未找到端口文件)'} 无 AgentQuay Bridge 服务，"
        "且 auto_spawn_bridge=False。请先运行 `agentquay start --daemon` 或开启 auto_spawn"
    )
