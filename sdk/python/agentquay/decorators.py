"""agent_tool 装饰器：标记方法为 Agent Tool（设计文档 §4.6）。

装饰器把 Tool 元数据挂在函数属性 ``__agentquay_tool__`` 上，
``AgentQuayClient.register_tools(instance)`` 时扫描实例方法并绑定。
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Callable, Optional

# 函数属性名（挂载 ToolSpec）
TOOL_ATTR = "__agentquay_tool__"

# tool 名规范（与 Bridge 一致）：[a-zA-Z0-9_-]{1,78}
TOOL_NAME_PATTERN = r"^[a-zA-Z0-9_-]{1,78}$"


@dataclass
class ToolSpec:
    """单个 Tool 的元数据。"""

    name: str
    description: str
    requires_confirmation: bool = False
    timeout_seconds: int = 30
    confirm_timeout_seconds: int = 120
    func: Optional[Callable] = field(default=None, repr=False)


def agent_tool(
    name: str | None = None,
    description: str = "",
    requires_confirmation: bool = False,
    timeout_seconds: int = 30,
    confirm_timeout_seconds: int = 120,
) -> Callable[[Callable], Callable]:
    """标记方法为 Agent Tool。

    :param name: Tool 名（缺省回退到函数名）
    :param description: 描述（缺省回退到函数 docstring 首行）
    :param requires_confirmation: 危险操作需要用户确认（Bridge 触发确认流程）
    :param timeout_seconds: 执行超时（默认 30s，Bridge 侧独立计时）
    :param confirm_timeout_seconds: 确认超时（默认 120s，独立于执行超时）
    """

    def decorator(func: Callable) -> Callable:
        tool_name = name or func.__name__
        if not re.match(TOOL_NAME_PATTERN, tool_name):
            raise ValueError(
                f"tool 名 {tool_name!r} 不符合规范 [a-zA-Z0-9_-]{{1,78}}"
            )
        # 描述缺省回退到函数 docstring 首行
        desc = description
        if not desc:
            doc = (func.__doc__ or "").strip()
            desc = doc.splitlines()[0] if doc else ""
        setattr(func, TOOL_ATTR, ToolSpec(
            name=tool_name,
            description=desc,
            requires_confirmation=requires_confirmation,
            timeout_seconds=timeout_seconds,
            confirm_timeout_seconds=confirm_timeout_seconds,
            func=func,
        ))
        return func

    return decorator


def get_tool_spec(func: Callable) -> ToolSpec | None:
    """取函数上挂载的 ToolSpec（无则返回 None）。"""
    return getattr(func, TOOL_ATTR, None)
