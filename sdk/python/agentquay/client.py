"""AgentQuayClient — 连接 Bridge 的应用侧客户端（设计文档 §4.6）。

职责：注册 Tool、心跳、调用分发、确认流程、指数退避重连、token 持久化、
auto-spawn 内嵌 Bridge。
"""

from __future__ import annotations

import asyncio
import atexit
import json
import logging
import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

import websockets

from .confirm import ask_confirmation
from .decorators import TOOL_ATTR, ToolSpec
from .errors import (
    AuthFailedError,
    BridgeConnectionError,
    BridgeUnavailableError,
    ConfirmationError,
    ProtocolError,
    RegistrationError,
    ReplacedError,
)
from .schema import param_schema
from .spawn import ensure_bridge
from .tokens import TokenStore

logger = logging.getLogger("agentquay")

APP_ID_PATTERN = re.compile(r"^[a-z0-9-]{1,48}$")

# 协议消息类型（与 Bridge 一致）
_MSG_REGISTER = "register"
_MSG_REGISTER_ACK = "register_ack"
_MSG_REGISTER_ERROR = "register_error"
_MSG_INVOKE = "invoke"
_MSG_RESULT = "result"
_MSG_CONFIRM = "confirm"
_MSG_CONFIRM_RESULT = "confirm_result"
_MSG_PING = "ping"
_MSG_PONG = "pong"
_MSG_DISCONNECT = "disconnect"
_MSG_NOTIFICATION = "notification"


@dataclass
class ToolBinding:
    """已绑定实例方法的 Tool。"""

    name: str
    description: str
    input_schema: dict[str, Any]
    requires_confirmation: bool
    timeout_seconds: int
    confirm_timeout_seconds: int
    func: Callable[..., Any]  # 已绑定方法
    is_async: bool = field(default=False)


@dataclass
class LaunchInfo:
    """应用的启动命令（随 register 上报，供 Bridge 离线自动拉起，§5.8）。

    不提供时 SDK 自动探测：打包程序（frozen）用 sys.executable；
    Python 脚本用 [解释器, 主脚本]；均可通过 launch_info 参数覆盖。
    """

    exec_path: str = ""
    args: list[str] = field(default_factory=list)
    cwd: str = ""
    single_instance: bool = False
    launch_timeout_seconds: int = 30

    def to_payload(self) -> dict[str, Any]:
        return {
            "execPath": self.exec_path or "",
            "args": self.args or [],
            "cwd": self.cwd or "",
            "singleInstance": self.single_instance,
            "launchTimeoutSeconds": self.launch_timeout_seconds,
        }


def default_launch_info() -> LaunchInfo | None:
    """自动探测本进程的启动命令（尽力而为，可被 launch_info 参数覆盖）。"""
    exe = getattr(sys, "executable", None)
    if not exe:
        return None
    if getattr(sys, "frozen", False):  # PyInstaller / Nuitka 等打包程序
        return LaunchInfo(exec_path=exe)
    script = ""
    try:
        candidate = Path(sys.argv[0]).resolve()
        if os.path.isfile(candidate):
            script = str(candidate)
    except Exception:
        script = ""
    return LaunchInfo(exec_path=exe, args=[script] if script else [])


class AgentQuayClient:
    """连接 AgentQuay Bridge 的客户端。

    :param app_id: 应用 ID（[a-z0-9-]{1,48}，禁止 _ 和 .；如 music-app）
    :param app_name: 应用显示名
    :param host: Bridge 地址（默认 127.0.0.1）
    :param port: Bridge 端口（0 = 从 ~/.agentquay/port 自动读取）
    :param auto_spawn_bridge: 未检测到服务时自动拉起内嵌 Bridge（默认 True）
    :param version: 应用版本号（随注册上报）
    :param protocol_version: 协议版本（默认 "1.0"）
    :param heartbeat_interval: 心跳间隔秒数（默认 30）
    :param max_retry_interval: 重连退避上限秒数（默认 30）
    :param launch_info: 启动命令（推荐显式指定；缺省自动探测）
    :param auto_report_launch: 是否在注册时上报 launch 信息（默认 True）
    :param on_confirm: 自定义确认回调 async (message, arguments) -> bool，
        缺省使用 OS 原生弹窗
    """

    def __init__(
        self,
        app_id: str,
        app_name: str,
        host: str = "127.0.0.1",
        port: int = 0,
        auto_spawn_bridge: bool = True,
        version: str = "1.0.0",
        protocol_version: str = "1.0",
        heartbeat_interval: int = 30,
        max_retry_interval: int = 30,
        launch_info: LaunchInfo | None = None,
        auto_report_launch: bool = True,
        on_confirm: Callable[..., Any] | None = None,
    ) -> None:
        if not APP_ID_PATTERN.match(app_id):
            raise ValueError(
                f"appId {app_id!r} 不符合规范 [a-z0-9-]{{1,48}}（禁止 _ 和 .）"
            )
        self.app_id = app_id
        self.app_name = app_name
        self.host = host
        self.port = port
        self.auto_spawn_bridge = auto_spawn_bridge
        self.version = version
        self.protocol_version = protocol_version
        self.heartbeat_interval = heartbeat_interval
        self.max_retry_interval = max_retry_interval
        self.on_confirm = on_confirm
        self._launch_info = launch_info or (default_launch_info() if auto_report_launch else None)

        self._tools: dict[str, ToolBinding] = {}
        self._token = TokenStore(app_id).get()
        self._ws: websockets.ClientConnection | None = None
        self._send_lock = asyncio.Lock()
        self._stop_event = asyncio.Event()
        self._silent_cycles = 0
        self._spawned_proc: subprocess.Popen | None = None
        self._closed = False

        atexit.register(self._atexit_cleanup)

    # ------------------------------------------------------------------
    # 工具注册
    # ------------------------------------------------------------------

    def register_tools(self, instance: object) -> "AgentQuayClient":
        """扫描实例上被 @agent_tool 标记的方法并登记。

        :return: self（支持链式调用）
        """
        for member_name in dir(instance):
            if member_name.startswith("__"):
                continue
            attr = getattr(instance, member_name)
            spec: ToolSpec | None = getattr(attr, TOOL_ATTR, None)
            if spec is None:
                continue
            if not callable(attr):
                raise ValueError(f"{member_name} 被标记为 AgentTool 但不可调用")
            self._tools[spec.name] = ToolBinding(
                name=spec.name,
                description=spec.description,
                input_schema=param_schema(spec.func or attr),
                requires_confirmation=spec.requires_confirmation,
                timeout_seconds=spec.timeout_seconds,
                confirm_timeout_seconds=spec.confirm_timeout_seconds,
                func=attr,
                is_async=asyncio.iscoroutinefunction(attr),
            )
            logger.debug("已登记 tool: %s", spec.name)
        return self

    def list_tools(self) -> list[str]:
        """返回已登记的 tool 名列表。"""
        return list(self._tools)

    # ------------------------------------------------------------------
    # 连接生命周期
    # ------------------------------------------------------------------

    async def connect(self) -> None:
        """连接 Bridge 并保持（心跳 + 自动重连）。阻塞直到 close() 或连接被替换。"""
        if self._closed:
            raise RuntimeError("客户端已关闭，无法再次 connect")
        self._stop_event.clear()
        # 首次连接：确保 Bridge 可用（含 auto-spawn）
        port, proc = ensure_bridge(self.host, self.port, self.auto_spawn_bridge)
        self.port = port
        if proc is not None:
            self._spawned_proc = proc
        await self._run_forever()

    async def close(self) -> None:
        """优雅关闭：停止重连、断开连接。

        内嵌 Bridge 的生命周期由它自己管理（无应用注册且空闲超时后自退出），
        宿主关闭不再主动杀子进程（embedded-bridge-lifecycle.md 步骤 1）。
        """
        self._closed = True
        self._stop_event.set()
        ws = self._ws
        if ws is not None:
            try:
                await ws.close()
            except Exception:
                pass
            self._ws = None
        self._cleanup_spawned()

    async def __aenter__(self) -> "AgentQuayClient":
        await self.connect()
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    # ------------------------------------------------------------------
    # 内部实现
    # ------------------------------------------------------------------

    async def _run_forever(self) -> None:
        backoff = 1.0
        while not self._stop_event.is_set():
            try:
                await self._connect_once()
                backoff = 1.0
            except ReplacedError:
                logger.warning("连接被同 appId 的新实例替换，停止重连")
                raise
            except asyncio.CancelledError:
                raise
            except (BridgeConnectionError, OSError) as exc:
                if self._stop_event.is_set():
                    break
                # Bridge 可能已重启并发生端口漂移（autoPort）：重读权威端口文件
                if self.port != 0:
                    from .spawn import read_port_file

                    read = read_port_file()
                    if read and read != self.port:
                        logger.info("Bridge 端口变化 %s → %s", self.port, read)
                        self.port = read
                logger.warning("连接失败: %s（%ss 后重连）", exc, backoff)
                try:
                    await asyncio.wait_for(self._stop_event.wait(), timeout=backoff)
                except asyncio.TimeoutError:
                    pass
                backoff = min(backoff * 2, self.max_retry_interval)

    async def _connect_once(self) -> None:
        # 端口漂移处理：port 文件是权威来源
        if self.port == 0:
            from .spawn import read_port_file

            read = read_port_file()
            if read:
                self.port = read

        url = f"ws://{self.host}:{self.port}/ws"
        logger.info("连接 Bridge: %s", url)
        ws = await websockets.connect(
            url,
            ping_interval=None,  # 使用应用层 JSON 心跳（协议 §3.1）
            close_timeout=5,
            max_size=1 << 20,
        )
        self._ws = ws
        try:
            await self._register(ws)
            logger.info("注册成功: %s (tools=%d)", self.app_id, len(self._tools))
            await self._recv_loop(ws)
        except websockets.ConnectionClosed as exc:
            raise BridgeConnectionError(f"连接断开: {exc}") from exc
        finally:
            self._ws = None
            try:
                await ws.close()
            except Exception:
                pass

    async def _register(self, ws: websockets.ClientConnection) -> None:
        payload = {
            "appId": self.app_id,
            "appName": self.app_name,
            "version": self.version,
            "protocolVersion": self.protocol_version,
            "authToken": self._token or "",
            "tools": [
                {
                    "name": t.name,
                    "description": t.description,
                    "inputSchema": t.input_schema,
                    "requiresConfirmation": t.requires_confirmation,
                    "timeoutSeconds": t.timeout_seconds,
                    "confirmTimeoutSeconds": t.confirm_timeout_seconds,
                }
                for t in self._tools.values()
            ],
        }
        if self._launch_info is not None:
            payload["launch"] = self._launch_info.to_payload()
        await self._send(ws, _MSG_REGISTER, payload)

        try:
            raw = await asyncio.wait_for(ws.recv(), timeout=10)
        except asyncio.TimeoutError as exc:
            raise BridgeConnectionError("注册超时（10s 未收到 register_ack）") from exc

        env = _parse(raw)
        if env["type"] == _MSG_REGISTER_ACK:
            token = env.get("payload", {}).get("token")
            if token:
                self._token = token
                TokenStore(self.app_id).set(token)  # 持久化，重连自动携带
            return
        if env["type"] == _MSG_REGISTER_ERROR:
            code = env.get("payload", {}).get("code", "UNKNOWN")
            message = env.get("payload", {}).get("message", "")
            if code == "AUTH_FAILED":
                raise AuthFailedError(f"认证失败: {message}")
            raise RegistrationError(f"注册被拒绝 [{code}]: {message}")
        raise ProtocolError(f"注册等待期间收到意外消息: {env['type']}")

    async def _recv_loop(self, ws: websockets.ClientConnection) -> None:
        while not self._stop_event.is_set():
            try:
                raw = await asyncio.wait_for(ws.recv(), timeout=self.heartbeat_interval * 2)
            except asyncio.TimeoutError:
                # 心跳：发送 ping；连续两次超时判定连接死亡
                self._silent_cycles += 1
                try:
                    await self._send(ws, _MSG_PING, {"timestamp": int(time.time())})
                except Exception as exc:
                    raise BridgeConnectionError(f"心跳发送失败: {exc}") from exc
                if self._silent_cycles >= 2:
                    raise BridgeConnectionError("心跳超时（30s 无响应）")
                continue
            except websockets.ConnectionClosed as exc:
                raise BridgeConnectionError(f"连接断开: {exc}") from exc

            self._silent_cycles = 0
            try:
                env = _parse(raw)
            except ProtocolError:
                continue
            await self._handle_message(ws, env)

    async def _handle_message(self, ws: websockets.ClientConnection, env: dict[str, Any]) -> None:
        msg_type = env["type"]
        payload = env.get("payload", {})

        if msg_type == _MSG_INVOKE:
            asyncio.get_running_loop().create_task(self._handle_invoke(ws, payload))
        elif msg_type == _MSG_CONFIRM:
            asyncio.get_running_loop().create_task(self._handle_confirm(ws, payload))
        elif msg_type == _MSG_PING:
            await self._send(ws, _MSG_PONG, {"timestamp": int(time.time())})
        elif msg_type == _MSG_PONG:
            pass
        elif msg_type == _MSG_DISCONNECT:
            reason = payload.get("reason", "normal")
            if reason == "replaced":
                raise ReplacedError("本连接已被同 appId 的新实例替换")
            if reason == "migrate":
                # Bridge 让位给更强的系统服务实例：按普通断线重连，
                # 重连路径会重读端口文件并自动落到新实例（token 共享）。
                logger.info("Bridge 让位，重连时将重读端口文件")
                raise BridgeConnectionError("Bridge 让位迁移")
            raise BridgeConnectionError(f"Bridge 断开: {reason}")
        elif msg_type == _MSG_NOTIFICATION:
            logger.info("Bridge 通知: %s", payload.get("message"))
        else:
            logger.warning("未知消息类型: %s", msg_type)

    async def _handle_invoke(self, ws: websockets.ClientConnection, payload: dict[str, Any]) -> None:
        request_id = payload.get("requestId", "")
        tool_name = payload.get("tool", "")
        arguments = payload.get("arguments") or {}
        timeout_seconds = payload.get("timeoutSeconds") or 30

        binding = self._tools.get(tool_name)
        if binding is None:
            await self._send_result(ws, request_id, False, None,
                                    {"code": "TOOL_NOT_FOUND", "message": f"tool 不存在: {tool_name}"})
            return

        logger.debug("执行 tool: %s args=%s", tool_name, arguments)
        try:
            result = await self._call(binding, arguments, timeout_seconds)
            await self._send_result(ws, request_id, True, result, None)
        except asyncio.TimeoutError:
            await self._send_result(ws, request_id, False, None,
                                    {"code": "EXECUTION_TIMEOUT", "message": f"执行超时（>{timeout_seconds}s）"})
        except Exception as exc:
            logger.exception("tool 执行异常: %s", tool_name)
            await self._send_result(ws, request_id, False, None,
                                    {"code": "EXECUTION_ERROR", "message": str(exc)})

    async def _call(self, binding: ToolBinding, arguments: dict[str, Any], timeout_seconds: int) -> Any:
        kwargs = _bind_arguments(binding.func, arguments)
        # 超时上限 = Bridge 侧执行超时 + 5s 余量（保证 Bridge 先超时，SDK 迟到结果进孤儿处理）
        if binding.is_async:
            return await asyncio.wait_for(binding.func(**kwargs), timeout=timeout_seconds + 5)
        return await asyncio.wait_for(asyncio.to_thread(binding.func, **kwargs), timeout=timeout_seconds + 5)

    async def _handle_confirm(self, ws: websockets.ClientConnection, payload: dict[str, Any]) -> None:
        request_id = payload.get("requestId", "")
        message = payload.get("message", "确认执行操作？")
        arguments = payload.get("arguments")
        timeout_seconds = payload.get("timeoutSeconds") or 120

        try:
            if self.on_confirm is not None:
                # 支持同步与异步回调（await 非 awaitable 会抛 TypeError）
                result = self.on_confirm(message, arguments)
                if asyncio.iscoroutine(result):
                    confirmed = await result
                else:
                    confirmed = bool(result)
            else:
                confirmed = await ask_confirmation(message, arguments, timeout_seconds)
        except Exception as exc:
            logger.warning("确认流程异常，默认拒绝: %s", exc)
            confirmed = False
        try:
            await self._send(ws, _MSG_CONFIRM_RESULT,
                             {"requestId": request_id, "confirmed": bool(confirmed)})
        except Exception as exc:
            raise ConfirmationError(f"发送确认结果失败: {exc}") from exc

    async def _send_result(
        self,
        ws: websockets.ClientConnection,
        request_id: str,
        success: bool,
        data: Any,
        error: dict[str, Any] | None,
    ) -> None:
        payload: dict[str, Any] = {
            "requestId": request_id,
            "success": success,
            "data": data,
            "error": error,
        }
        await self._send(ws, _MSG_RESULT, payload)

    async def _send(self, ws: websockets.ClientConnection, msg_type: str, payload: Any) -> None:
        msg = json.dumps({"type": msg_type, "payload": payload}, ensure_ascii=False)
        async with self._send_lock:
            await ws.send(msg)

    # ------------------------------------------------------------------
    # 清理
    # ------------------------------------------------------------------

    def _cleanup_spawned(self) -> None:
        # 内嵌 Bridge 生命周期由自己管理（无应用注册且空闲超时后自退出），
        # 宿主退出不再主动杀子进程（embedded-bridge-lifecycle.md 步骤 1）。
        if self._spawned_proc is not None:
            logger.debug("内嵌 Bridge 交由空闲自回收退出 (pid %s)", self._spawned_proc.pid)
            self._spawned_proc = None

    def _atexit_cleanup(self) -> None:
        self._cleanup_spawned()


def _parse(raw: str | bytes) -> dict[str, Any]:
    try:
        env = json.loads(raw)
    except (json.JSONDecodeError, TypeError) as exc:
        raise ProtocolError(f"无法解析 Bridge 消息: {exc}") from exc
    if not isinstance(env, dict) or "type" not in env:
        raise ProtocolError(f"消息缺少 type 字段: {raw!r}")
    return env


def _bind_arguments(func: Callable[..., Any], arguments: dict[str, Any]) -> dict[str, Any]:
    """按函数签名绑定参数（忽略多余参数；缺失参数让调用方抛 TypeError 由上层捕获）。"""
    import inspect

    sig = inspect.signature(func)
    kwargs = {k: v for k, v in arguments.items() if k in sig.parameters}
    return kwargs
