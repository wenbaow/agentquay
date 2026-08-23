"""端到端联调测试：真实 Bridge + Python SDK + MCP 客户端全链路。

覆盖场景：
  1. Bridge 启动（serve 前台）与 health 探测
  2. SDK 注册（token 钉扎）→ tools/list 可见（合成名 + 应用名前缀描述）
  3. tools/call 正常调用、参数校验 -32003、未知 tool、业务错误透传
  4. 确认流程：确认成功 / 用户取消 -32005
  5. 执行超时 -32004 + 迟到结果进孤儿缓冲
  6. 同 appId 替换 -32007（ReplacedError）
  7. 断线重连自动携带 token（不触发 AUTH_FAILED）
  8. rotate-token：旧 token 被拒 AUTH_FAILED，新 token 恢复
  9. （尽力而为）SSE 上的 notifications/tools/list_changed

用法: python tests/e2e_test.py
"""

from __future__ import annotations

import asyncio
import json
import logging
import queue
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from agentquay import AgentQuayClient, agent_tool  # noqa: E402
from agentquay.errors import AuthFailedError, ReplacedError  # noqa: E402
from agentquay.tokens import TokenStore  # noqa: E402

logging.basicConfig(level=logging.WARNING, format="%(levelname)s %(name)s: %(message)s")

REPO_ROOT = Path(__file__).resolve().parent.parent.parent.parent
BRIDGE_BIN = next((REPO_ROOT / "bridge" / "dist").glob("agentquay-windows-*.exe"), None)
if BRIDGE_BIN is None:
    BRIDGE_BIN = next((REPO_ROOT / "bridge" / "dist").glob("agentquay-*"), None)
assert BRIDGE_BIN is not None, "未找到 Bridge 二进制，请先构建: cd bridge && go build -o dist/ ./cmd/agentquay"

PASS, FAIL = 0, 0


def check(name: str, cond: bool, detail: str = "") -> None:
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  ✅ {name}")
    else:
        FAIL += 1
        print(f"  ❌ {name} {detail}")


class MCPRawClient:
    """最小 MCP Streamable HTTP 客户端（无第三方依赖，同步内核 + 异步外壳）。"""

    def __init__(self, port: int):
        self.url = f"http://127.0.0.1:{port}/mcp"
        self.session_id: str | None = None
        self._id = 0

    async def _post(self, method: str, params: dict | None = None) -> dict:
        # 同步 HTTP 在线程中执行，避免阻塞共享事件循环（SDK 应用同进程运行）
        return await asyncio.to_thread(self._post_sync, method, params)

    def _post_sync(self, method: str, params: dict | None) -> dict:
        self._id += 1
        body = {"jsonrpc": "2.0", "id": self._id, "method": method}
        if params is not None:
            body["params"] = params
        req = urllib.request.Request(
            self.url,
            data=json.dumps(body).encode(),
            headers={
                "Content-Type": "application/json",
                "Accept": "application/json, text/event-stream",
            },
        )
        if self.session_id:
            req.add_header("Mcp-Session-Id", self.session_id)
        with urllib.request.urlopen(req, timeout=90) as resp:
            sid = resp.headers.get("Mcp-Session-Id")
            if sid:
                self.session_id = sid
            data = resp.read()
            ctype = resp.headers.get("Content-Type", "")
        # Streamable HTTP 规范：会话有挂起通知（如 list_changed）时，
        # 服务端以 SSE 流响应（通知在前，目标响应在最后）——真实 MCP 客户端同样解析
        if "text/event-stream" in ctype:
            return self._parse_sse(data)
        return json.loads(data)

    @staticmethod
    def _parse_sse(data: bytes) -> dict:
        """从 SSE 流提取最后一个 data 负载（即目标 JSON-RPC 响应）。"""
        last: str | None = None
        for event in data.decode(errors="replace").split("\n\n"):
            payload: str | None = None
            for line in event.splitlines():
                if line.startswith("data:"):
                    payload = line[5:].strip()
            if payload:
                last = payload
        if last is None:
            raise ValueError(f"SSE 流中未找到 data 负载: {data[:200]!r}")
        return json.loads(last)

    async def initialize(self) -> dict:
        return await self._post("initialize", {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "e2e-client", "version": "1.0"},
        })

    async def tools_list(self) -> dict:
        return await self._post("tools/list", {})

    async def tools_call(self, name: str, arguments: dict, _meta: dict | None = None) -> dict:
        params: dict = {"name": name, "arguments": arguments}
        if _meta:
            params["_meta"] = _meta
        return await self._post("tools/call", params)


def tool_error_text(resp: dict) -> str:
    """从 IsError 结果提取 content 文本。"""
    return resp.get("result", {}).get("content", [{}])[0].get("text", "")


def tool_error_code(resp: dict) -> int | None:
    """从 IsError 结果提取 agentquay 错误码。"""
    text = tool_error_text(resp)
    try:
        return json.loads(text).get("code")
    except (json.JSONDecodeError, AttributeError):
        return None


# ---------------------------------------------------------------------------
# 被测应用（SDK 侧）
# ---------------------------------------------------------------------------

class E2EController:
    @agent_tool("echo", description="回显消息")
    def echo(self, message: str) -> dict:
        return {"received": message}

    @agent_tool("add", description="整数加法")
    def add(self, a: int, b: int) -> int:
        return a + b

    @agent_tool("confirm_op", description="需要用户确认的操作", requires_confirmation=True)
    def confirm_op(self, value: str) -> dict:
        return {"confirmed_value": value}

    @agent_tool("fail", description="总是失败（业务错误透传）")
    def fail(self) -> dict:
        raise ValueError("模拟业务失败")

    @agent_tool("slow", description="慢操作（测试执行超时）")
    def slow(self, seconds: float = 32.0) -> dict:
        time.sleep(seconds)
        return {"elapsed": seconds}


class SongPage:
    """页面控制器（页面智能路由）：惰性注册，首次调用才创建实例。"""

    creations = 0  # 实例创建计数（跨测试实例共享，用于断言"只创建一次"）

    def __init__(self) -> None:
        type(self).creations += 1
        self.hits = 0

    @agent_tool("queue", description="查看播放队列（页面工具）")
    def queue(self) -> dict:
        self.hits += 1
        return {"page": type(self).creations, "hits": self.hits}


class E2EApp:
    def __init__(self, app_id: str = "e2e-app", confirm_answer: bool = True):
        self.client = AgentQuayClient(
            app_id=app_id,
            app_name="E2E App",
            port=0,
            auto_spawn_bridge=False,  # 测试脚本自己管理 Bridge
            heartbeat_interval=30,
            max_retry_interval=2,
            on_confirm=lambda message, arguments: confirm_answer,
        )
        self.client.register_tools(E2EController())
        # 页面工具：惰性注册（页面未打开工具也可见，首次调用才创建）
        self.client.register_tools(SongPage, page_key="SongPage")

    async def run(self) -> None:
        await self.client.connect()


def admin_status(port: int) -> dict:
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/admin/status", timeout=3) as resp:
        return json.loads(resp.read())


def admin_rotate(port: int, app_id: str) -> str:
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/admin/rotate-token",
        data=json.dumps({"appId": app_id}).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=3) as resp:
        return json.loads(resp.read())["token"]


async def wait_app_online(port: int, app_id: str, timeout: float = 8.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if any(a["appId"] == app_id for a in (await asyncio.to_thread(admin_status, port)).get("apps", [])):
            return True
        await asyncio.sleep(0.2)
    return False


async def wait_app_offline(port: int, app_id: str, timeout: float = 8.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if all(a["appId"] != app_id for a in (await asyncio.to_thread(admin_status, port)).get("apps", [])):
            return True
        await asyncio.sleep(0.2)
    return False


def wait_list_changed(port: int, session_id: str, timeout: float = 8.0) -> bool:
    """尽力而为：SSE 流上等待 list_changed 通知（非致命检查）。"""
    q: queue.Queue = queue.Queue()

    def _read() -> None:
        try:
            req = urllib.request.Request(
                f"http://127.0.0.1:{port}/mcp",
                headers={"Accept": "text/event-stream", "Mcp-Session-Id": session_id},
            )
            with urllib.request.urlopen(req, timeout=timeout + 2) as resp:
                buffer = ""
                while True:
                    chunk = resp.read(64).decode(errors="replace")
                    if not chunk:
                        break
                    buffer += chunk
                    while "\n\n" in buffer:
                        event, buffer = buffer.split("\n\n", 1)
                        if "list_changed" in event:
                            q.put(True)
                            return
        except Exception:
            q.put(False)

    t = threading.Thread(target=_read, daemon=True)
    t.start()
    try:
        return q.get(timeout=timeout) is True
    except queue.Empty:
        return False


# ---------------------------------------------------------------------------
# 主测试流程
# ---------------------------------------------------------------------------

async def main() -> int:
    global PASS, FAIL
    print(f"=== AgentQuay 端到端联调测试 (bridge={BRIDGE_BIN.name}) ===")

    # 1. 启动 Bridge（前台 serve，由本脚本管理生命周期）
    #    先删除端口文件与残留进程，保证端口状态确定
    port_file = Path.home() / ".agentquay" / "port"
    port_file.unlink(missing_ok=True)
    import os

    os.environ["AGENTQUAY_LOG_LEVEL"] = "debug"
    bridge = subprocess.Popen(
        [str(BRIDGE_BIN), "serve"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        env=dict(os.environ),
    )
    try:
        # 等待端口文件出现（新 Bridge 写入）且健康
        port = 0
        for _ in range(60):
            time.sleep(0.2)
            if not port_file.exists():
                continue
            try:
                port = int(port_file.read_text().strip())
            except ValueError:
                continue
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/admin/health", timeout=1):
                    break
            except Exception:
                port = 0
        check("Bridge 启动并健康", port > 0, f"port={port}")
        if not port:
            return 1

        # 2. MCP 握手 + 会话
        rpc = MCPRawClient(port)
        init = await rpc.initialize()
        check("MCP initialize 握手", "result" in init, str(init)[:200])
        check("会话已建立", rpc.session_id is not None)

        # 3. SDK 应用注册
        app = E2EApp()
        connect_task = asyncio.create_task(app.run())
        check("SDK 应用注册上线", await wait_app_online(port, "e2e-app"))

        # 4. tools/list
        tools = (await rpc.tools_list())["result"]["tools"]
        names = {t["name"] for t in tools}
        check("tools/list 返回合成名 e2e-app_echo", "e2e-app_echo" in names)
        echo_tool = next(t for t in tools if t["name"] == "e2e-app_echo")
        check("描述带应用名前缀 [E2E App]", echo_tool["description"].startswith("[E2E App]"))
        check("inputSchema 含 required", echo_tool["inputSchema"].get("required") == ["message"])

        # 4.5 页面智能路由端到端（方案 §9 Phase 4 验收：未开页面自动创建 + 前缀 + 实例复用）
        page_tool = next(t for t in tools if t["name"] == "e2e-app_queue")
        check("页面工具在 tools/list 中（页面未打开也可见）",
              page_tool["description"] == "[E2E App|SongPage] 查看播放队列（页面工具）",
              page_tool["description"])
        check("页面尚未创建（工厂未执行）", SongPage.creations == 0)
        resp = await rpc.tools_call("e2e-app_queue", {})
        ok = "result" in resp and not resp["result"].get("isError", False)
        check("调用未打开页面 → 自动创建并返回", ok, str(resp)[:200])
        if ok:
            t1 = json.loads(resp["result"]["content"][0]["text"])
            resp = await rpc.tools_call("e2e-app_queue", {})
            t2 = json.loads(resp["result"]["content"][0]["text"])
            check("首次调用创建页面（page=1, hits=1）", t1 == {"page": 1, "hits": 1}, str(t1))
            check("再次调用复用同一实例（page=1, hits=2）", t2 == {"page": 1, "hits": 2}, str(t2))
            check("全程只创建一次实例", SongPage.creations == 1)

        # 5. 正常调用
        resp = await rpc.tools_call("e2e-app_echo", {"message": "你好，Agent"})
        ok = "result" in resp and not resp["result"].get("isError", False)
        check("tools/call 正常调用", ok, str(resp)[:200])
        if ok:
            text = json.loads(resp["result"]["content"][0]["text"])
            check("结果内容正确", text == {"received": "你好，Agent"}, str(text))

        # 6. 参数校验（-32003）
        resp = await rpc.tools_call("e2e-app_echo", {})  # 缺必填
        check("缺必填参数 → -32003", tool_error_code(resp) == -32003, tool_error_text(resp)[:120])
        resp = await rpc.tools_call("e2e-app_echo", {"message": 123})  # 类型错误
        check("类型错误 → -32003", tool_error_code(resp) == -32003, tool_error_text(resp)[:120])

        # 7. 未知 tool / 未知应用
        resp = await rpc.tools_call("e2e-app_no_such_tool", {})
        check("未知 tool → 错误响应", "error" in resp or tool_error_code(resp) is not None, str(resp)[:150])
        resp = await rpc.tools_call("no-such-app_echo", {"message": "x"})
        check("未知应用 → 错误响应", "error" in resp or tool_error_code(resp) is not None, str(resp)[:150])

        # 8. 业务错误透传
        resp = await rpc.tools_call("e2e-app_fail", {})
        check("业务错误透传 isError", resp.get("result", {}).get("isError", False), str(resp)[:200])

        # 9. 确认流程：确认成功
        resp = await rpc.tools_call("e2e-app_confirm_op", {"value": "v1"})
        ok = "result" in resp and not resp["result"].get("isError", False)
        check("确认流程（确认）→ 成功", ok, str(resp)[:200])

        # 10. 确认流程：用户取消 → -32005
        app2 = E2EApp(confirm_answer=False)
        # 直接复用现有注册：用一个新 on_confirm 实例替换？不行——替换会顶掉注册。
        # 改为：临时切换 app 的确认回调
        old_cb = app.client.on_confirm
        app.client.on_confirm = lambda message, arguments: False
        resp = await rpc.tools_call("e2e-app_confirm_op", {"value": "v2"})
        app.client.on_confirm = old_cb
        check("确认流程（取消）→ -32005", tool_error_code(resp) == -32005, tool_error_text(resp)[:120])

        # 11. 执行超时 → -32004 + 孤儿结果
        t0 = time.monotonic()
        resp = await rpc.tools_call("e2e-app_slow", {"seconds": 32.0})
        elapsed = time.monotonic() - t0
        check("执行超时 → -32004", tool_error_code(resp) == -32004 and 28 < elapsed < 40,
              f"code={tool_error_code(resp)} elapsed={elapsed:.1f}s")
        # 等待迟到结果进入孤儿缓冲（32s sleep 结束）
        orphan_seen = False
        for _ in range(40):
            time.sleep(0.5)
            if (await asyncio.to_thread(admin_status, port)).get("orphanResults", 0) >= 1:
                orphan_seen = True
                break
        check("迟到结果进孤儿缓冲", orphan_seen)

        # 12. 同 appId 替换 → ReplacedError（-32007 语义）
        # 先挂上 SSE 监听：替换过程触发两次 list_changed（旧连接注销 + 新注册）
        loop = asyncio.get_running_loop()
        sse_fut = loop.run_in_executor(None, wait_list_changed, port, rpc.session_id or "", 8.0)
        app_new = E2EApp()
        new_task = asyncio.create_task(app_new.run())
        try:
            await asyncio.wait_for(connect_task, timeout=8)
            replaced = False
        except ReplacedError:
            replaced = True
        except Exception as exc:
            replaced = isinstance(exc, ReplacedError)
        check("同 appId 新实例替换旧连接", replaced)
        check("新实例接管注册", await wait_app_online(port, "e2e-app"))
        sse_ok = await asyncio.wait_for(sse_fut, timeout=12)
        check("SSE list_changed 通知", sse_ok, "（尽力而为检查）")

        # 13. 断线重连 + token（关闭新实例，旧 token 的客户端重连成功）
        await app_new.client.close()
        check("新实例下线", await wait_app_offline(port, "e2e-app"))
        app3 = E2EApp()
        t3 = asyncio.create_task(app3.run())
        check("重连自动携带 token 成功", await wait_app_online(port, "e2e-app"))

        # 14. rotate-token：旧 token 被拒，新 token 恢复
        new_token = await asyncio.to_thread(admin_rotate, port, "e2e-app")
        await app3.client.close()
        await asyncio.sleep(0.5)
        app_old_token = E2EApp()
        try:
            t_old = asyncio.create_task(app_old_token.run())
            await asyncio.wait_for(t_old, timeout=6)
            auth_rejected = False
        except AuthFailedError:
            auth_rejected = True
        except Exception as exc:
            auth_rejected = isinstance(exc, AuthFailedError)
        check("轮换后旧 token 被拒 (AUTH_FAILED)", auth_rejected)

        TokenStore("e2e-app").set(new_token)
        app_new_token = E2EApp()
        t_new = asyncio.create_task(app_new_token.run())
        check("新 token 恢复注册", await wait_app_online(port, "e2e-app"))

        # 清理：关闭客户端并收敛未 await 的任务，避免退出噪音
        await app_new_token.client.close()
        await asyncio.sleep(0.5)
        for t in (connect_task, new_task, t3, t_old, t_new):
            if not t.done():
                t.cancel()
        await asyncio.gather(*[t for t in (connect_task, new_task, t3, t_old, t_new)
                              if not t.done()], return_exceptions=True)

    finally:
        bridge.terminate()
        try:
            bridge.wait(timeout=5)
        except subprocess.TimeoutExpired:
            bridge.kill()

    print(f"\n=== 结果: {PASS} 通过, {FAIL} 失败 ===")
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
