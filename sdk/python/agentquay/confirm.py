"""确认弹窗（设计文档 §3.3）：优先 OS 原生对话框（tkinter，三平台标准库），
无显示环境时回退控制台输入，再不可用时默认拒绝（安全优先）。

实现说明：tkinter 需在主线程驱动。采用 Toplevel 对话框 + 事件循环内周期
``root.update()`` 轮询的方式，弹窗期间 asyncio 事件循环（心跳、重连）不被阻塞，
且所有 Tk 调用都在同一线程，无跨线程竞态。
"""

from __future__ import annotations

import asyncio
import json
import logging
import os

logger = logging.getLogger("agentquay")


def _console_ask(message: str) -> bool:
    """回退：控制台输入 y/n。"""
    while True:
        answer = input(f"{message} [y/N]: ").strip().lower()
        if answer in ("y", "yes"):
            return True
        if answer in ("", "n", "no"):
            return False
        print("请输入 y 或 n")


def _format_message(message: str, arguments: dict | None) -> str:
    if not arguments:
        return message
    try:
        detail = json.dumps(arguments, ensure_ascii=False, indent=2)
    except (TypeError, ValueError):
        detail = str(arguments)
    return f"{message}\n\n参数:\n{detail}"


async def ask_confirmation(
    message: str,
    arguments: dict | None = None,
    timeout_seconds: int = 120,
) -> bool:
    """弹出确认对话框并等待用户决策。

    :param message: 确认文案
    :param arguments: 调用参数（展示用）
    :param timeout_seconds: 确认超时（超时视为取消；Bridge 侧同样计时）
    """
    full_message = _format_message(message, arguments)

    # 无图形环境（Unix 无 DISPLAY）→ 控制台
    if os.name != "nt" and not os.environ.get("DISPLAY"):
        logger.info("无图形环境，使用控制台确认: %s", full_message)
        return await asyncio.to_thread(_console_ask, full_message)

    try:
        import tkinter as tk
    except ImportError:
        logger.info("tkinter 不可用，使用控制台确认: %s", full_message)
        return await asyncio.to_thread(_console_ask, full_message)

    try:
        return await _ask_tk(full_message, timeout_seconds)
    except Exception as exc:
        logger.warning("弹窗失败（%s），使用控制台确认", exc)
        return await asyncio.to_thread(_console_ask, full_message)


async def _ask_tk(full_message: str, timeout_seconds: int) -> bool:
    import tkinter as tk

    loop = asyncio.get_running_loop()
    result: dict[str, bool | None] = {"value": None}
    deadline = loop.time() + max(timeout_seconds, 5)

    root = tk.Tk()
    root.withdraw()

    dialog = tk.Toplevel(root)
    dialog.title("AgentQuay 确认")
    dialog.resizable(False, False)
    try:
        dialog.attributes("-topmost", True)
    except tk.TclError:
        pass

    label = tk.Label(dialog, text=full_message, justify="left",
                     wraplength=420, padx=20, pady=16)
    label.pack()

    buttons = tk.Frame(dialog)
    buttons.pack(pady=(0, 12))

    def decide(choice: bool) -> None:
        result["value"] = choice

    tk.Button(buttons, text="取消", width=10, command=lambda: decide(False)).pack(side="left", padx=6)
    tk.Button(buttons, text="确认", width=10, command=lambda: decide(True)).pack(side="left", padx=6)

    dialog.protocol("WM_DELETE_WINDOW", lambda: decide(False))
    dialog.grab_set()

    def _poll() -> None:
        if result["value"] is not None or loop.time() >= deadline:
            try:
                dialog.destroy()
                root.destroy()
            except tk.TclError:
                pass
            return
        try:
            root.update()
        except tk.TclError:
            result["value"] = False
            return
        loop.call_later(0.05, _poll)

    loop.call_soon(_poll)
    # 等待用户决策或超时
    while result["value"] is None:
        if loop.time() >= deadline:
            break
        await asyncio.sleep(0.05)

    if result["value"] is None:
        logger.warning("确认超时（%ss），视为取消", timeout_seconds)
        return False
    return bool(result["value"])
