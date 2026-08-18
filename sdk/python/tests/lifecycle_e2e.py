"""内嵌 Bridge 生命周期端到端测试（embedded-bridge-lifecycle.md 三个场景）。

在隔离的临时家目录（USERPROFILE/HOME）里，用真实 Bridge 二进制 + 真实 Python SDK
验证：
  Scenario 1：宿主 A 关闭后，B/C 不掉线；全部关闭后内嵌桥空闲自回收退出。
  Scenario 2：系统服务最后启动 → 内嵌桥让位、应用迁移到系统服务、服务归位回权威端口。
  Scenario 3：多个应用并发首启 → 只有一个内嵌桥被拉起（spawn 原子锁）。

用法:
  python tests/lifecycle_e2e.py
  AGENTQUAY_BRIDGE_BIN=<path> python tests/lifecycle_e2e.py
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]  # 仓库根（sdk/python/tests -> 上溯三层）
SDK_PY = REPO_ROOT / "sdk" / "python"
HERE = Path(__file__).resolve().parent
HARNESS = HERE / "_lifecycle_harness.py"

# 与现有 e2e 相同的二进制查找逻辑
_BRIDGE_BIN = None
if os.environ.get("AGENTQUAY_BRIDGE_BIN") and Path(os.environ["AGENTQUAY_BRIDGE_BIN"]).exists():
    _BRIDGE_BIN = Path(os.environ["AGENTQUAY_BRIDGE_BIN"])
if _BRIDGE_BIN is None:
    dist = REPO_ROOT / "bridge" / "dist"
    _BRIDGE_BIN = next(dist.glob("agentquay-windows-*.exe"), None)
assert _BRIDGE_BIN is not None, "未找到 Bridge 二进制，请先构建: cd bridge && go build -o dist/ ./cmd/agentquay"

BRIDGE_BIN = _BRIDGE_BIN


# ---------------------------------------------------------------------------
# 工具

def make_env(home: Path, extra: dict | None = None) -> dict:
    env = dict(os.environ)
    env["USERPROFILE"] = str(home)
    env["HOME"] = str(home)
    env["PYTHONPATH"] = str(SDK_PY)
    if extra:
        env.update(extra)
    return env


def write_config(home: Path, embedded_idle: int) -> None:
    d = home / ".agentquay"
    d.mkdir(parents=True, exist_ok=True)
    (d / "config.json").write_text(json.dumps({
        "port": 19846,
        "autoPort": True,
        "host": "127.0.0.1",
        "embeddedIdleTimeoutSeconds": embedded_idle,
    }), encoding="utf-8")


def read_port_file(home: Path) -> int | None:
    p = home / ".agentquay" / "port"
    try:
        return int(p.read_text(encoding="utf-8").strip())
    except (FileNotFoundError, ValueError):
        return None


def admin_status(home: Path) -> dict | None:
    port = read_port_file(home)
    if not port:
        return None
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/admin/status", timeout=1) as r:
            return json.loads(r.read().decode("utf-8"))
    except Exception:
        return None


def wait_until(cond, timeout: float, interval: float = 0.5, desc: str = "condition"):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = cond()
            if last:
                return last
        except Exception as e:  # noqa: BLE001
            last = e
        time.sleep(interval)
    raise AssertionError(f"等待超时: {desc} (last={last})")


def bridge_process_count() -> int:
    """统计正在运行的"嵌入模式" Bridge 进程数（按命令行 serve --embedded 过滤，
    兼容包内 agentquay.exe 与 dist 的 agentquay-*.exe 不同命名）。"""
    ps = (
        "$n=Get-CimInstance Win32_Process | Where-Object { "
        "($_.Name -like 'agentquay*.exe') -and ($_.CommandLine -like '*serve --embedded*') } | "
        "Measure-Object | Select-Object -ExpandProperty Count; Write-Output $n"
    )
    out = subprocess.run(
        ["powershell", "-NoProfile", "-Command", ps], capture_output=True,
    ).stdout.decode("gbk", errors="ignore").strip()
    try:
        return int(out)
    except ValueError:
        return 0


def kill_repo_agentquay() -> None:
    """清理由仓库内二进制启动的 Bridge 进程（防止失败运行残留占用权威端口 19846）。"""
    reposlash = str(REPO_ROOT).replace("/", "\\")
    ps = (
        "Get-CimInstance Win32_Process | Where-Object { "
        f"($_.Name -like 'agentquay*.exe') -and ($_.ExecutablePath -like '{reposlash}*') }} | "
        "ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }"
    )
    subprocess.run(["powershell", "-NoProfile", "-Command", ps], capture_output=True)


class Harness:
    """一个 SDK 应用子进程。"""

    def __init__(self, app_id: str, app_name: str, home: Path):
        self.app_id = app_id
        self.proc = subprocess.Popen(
            [sys.executable, str(HARNESS), app_id, app_name],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, bufsize=1, env=make_env(home), cwd=str(SDK_PY),
        )
        ready = wait_until(
            lambda: "READY" in (self.proc.stdout.readline() or ""),
            timeout=30, interval=0.3, desc=f"{app_id} ready",
        )

    def close(self) -> None:
        if self.proc.poll() is None:
            try:
                self.proc.stdin.write("close\n")
                self.proc.stdin.flush()
            except Exception:
                pass
        self.proc.wait(timeout=30)

    def alive(self) -> bool:
        return self.proc.poll() is None


def registered_apps(status: dict | None) -> set[str]:
    if not status:
        return set()
    return {a["appId"] for a in status.get("apps", [])}


def wait_apps(home: Path, want: set[str], timeout: float, desc: str) -> dict:
    """等待 /admin/status 恰好等于 want（含精确比对），返回 status。"""
    wait_until(lambda: registered_apps(admin_status(home)) == want, timeout=timeout, desc=desc)
    st = admin_status(home)
    assert st is not None and registered_apps(st) == want, (st, registered_apps(st))
    return st


def _scenario1() -> None:
    print("=== Scenario 1: 宿主 A 关闭后 B/C 不掉线；全关后空闲自回收 ===")
    kill_repo_agentquay()
    hs: list[Harness] = []
    with tempfile.TemporaryDirectory(prefix="aq-e2e-s1-", ignore_cleanup_errors=True) as td:
        home = Path(td)
        write_config(home, embedded_idle=8)
        try:
            a = Harness("app-a", "App A", home); hs.append(a)
            b = Harness("app-b", "App B", home); hs.append(b)
            c = Harness("app-c", "App C", home); hs.append(c)
            st = wait_apps(home, {"app-a", "app-b", "app-c"}, timeout=40, desc="三应用同时在线")
            owner_pid = st["pid"]

            # 宿主 A 关闭 -> B/C 不受影响，桥仍在
            a.close(); hs.remove(a)
            assert not a.alive()
            st = wait_apps(home, {"app-b", "app-c"}, timeout=20, desc="A 注销，B/C 仍在")
            assert st["pid"] == owner_pid, "内嵌桥不应随宿主 A 退出"

            # 关闭 B、C -> 桥空闲自回收退出
            b.close(); hs.remove(b)
            c.close(); hs.remove(c)
            assert not b.alive() and not c.alive()
            wait_until(lambda: admin_status(home) is None, timeout=40,
                       desc="内嵌桥空闲自回收（端口文件消失/进程退出）")
            wait_until(lambda: bridge_process_count() == 0, timeout=10,
                       desc="内嵌桥进程完全退出")
            print("  ✅ 场景 1 通过")
        finally:
            for h in hs:
                try:
                    h.close()
                except Exception:
                    pass
    print()


def _scenario2() -> None:
    print("=== Scenario 2: 系统服务最后启动 -> 让位迁移 + 归位权威端口 ===")
    kill_repo_agentquay()
    hs: list[Harness] = []
    with tempfile.TemporaryDirectory(prefix="aq-e2e-s2-", ignore_cleanup_errors=True) as td:
        home = Path(td)
        write_config(home, embedded_idle=600)
        svc = None
        try:
            a = Harness("app-a", "App A", home); hs.append(a)
            b = Harness("app-b", "App B", home); hs.append(b)
            wait_apps(home, {"app-a", "app-b"}, timeout=40, desc="embedded 桥上 a/b 在线")
            embedded_pid = admin_status(home)["pid"]

            # 启动系统服务（service 模式）— 19846 被占 -> 漂移，随后归位
            svc = subprocess.Popen(
                [str(BRIDGE_BIN), "serve"],
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                env=make_env(home), cwd=str(SDK_PY),
            )

            # 收敛：内嵌桥退出 + 服务归位 19846 + a/b 迁到服务
            def converged():
                port = read_port_file(home)
                if port != 19846:
                    return None
                st = admin_status(home)
                if st is None or st["mode"] != "service":
                    return None
                return st if registered_apps(st) == {"app-a", "app-b"} else None
            st = wait_until(converged, timeout=60, desc="应用迁移到系统服务并归位 19846")
            assert st["pid"] != embedded_pid, "应由新的系统服务实例接管"

            # 宿主 A 关闭 -> 无影响（A 不再持有任何桥）
            a.close(); hs.remove(a)
            assert not a.alive()
            wait_apps(home, {"app-b"}, timeout=20, desc="A 关闭后 B 仍在系统服务上")

            b.close(); hs.remove(b)
            assert not b.alive()
            wait_until(lambda: registered_apps(admin_status(home)) == set(), timeout=20,
                       desc="B 关闭后系统服务仍存活（空闲不回收 service）")
            st = admin_status(home)
            assert st is not None and st["mode"] == "service", "service 不应空闲自回收"
            print("  ✅ 场景 2 通过")
        finally:
            for h in hs:
                try:
                    h.close()
                except Exception:
                    pass
            if svc is not None:
                svc.terminate()
                try:
                    svc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    svc.kill()
    print()


def _scenario3() -> None:
    print("=== Scenario 3: 三应用并发首启 -> 只拉起一个内嵌桥（spawn 锁） ===")
    kill_repo_agentquay()
    hs: list[Harness] = []
    with tempfile.TemporaryDirectory(prefix="aq-e2e-s3-", ignore_cleanup_errors=True) as td:
        home = Path(td)
        write_config(home, embedded_idle=6)
        try:
            hs = [Harness(f"app-{c}", f"App {c}", home) for c in "abc"]
            st = wait_apps(home, {"app-a", "app-b", "app-c"}, timeout=40,
                           desc="三应用注册到同一桥")
            count = bridge_process_count()
            assert count == 1, f"应只有 1 个 Bridge 进程，实际 {count}"
            print(f"  ✅ 场景 3 通过（Bridge 进程数 = {count}，pid = {st['pid']}）")
        finally:
            for h in hs:
                try:
                    h.close()
                except Exception:
                    pass
            try:
                wait_until(lambda: admin_status(home) is None, timeout=30,
                           desc="场景 3 清理：桥空闲自回收")
            except AssertionError:
                pass
    print()


def main() -> None:
    print(f"Bridge 二进制: {BRIDGE_BIN}")
    try:
        _scenario1()
        _scenario2()
        _scenario3()
        print("=== lifecycle e2e: 全部通过 ===")
    finally:
        kill_repo_agentquay()


if __name__ == "__main__":
    main()
