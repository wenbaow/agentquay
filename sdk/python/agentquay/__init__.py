"""AgentQuay Python SDK.

让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。

快速开始::

    from agentquay import AgentQuayClient, agent_tool

    class MusicController:
        @agent_tool("search", description="搜索音乐库")
        def search(self, keyword: str) -> list[dict]:
            return [{"id": "1", "title": "七里香", "artist": "周杰伦"}]

    async def main():
        client = AgentQuayClient(app_id="music-app", app_name="Music Player")
        client.register_tools(MusicController())
        await client.connect()
"""

from .client import AgentQuayClient, LaunchInfo, default_launch_info
from .decorators import agent_tool
from .errors import (
    AgentQuayError,
    AuthFailedError,
    BridgeConnectionError,
    BridgeSpawnError,
    BridgeUnavailableError,
    ConfirmationError,
    InvokeTimeoutError,
    ProtocolError,
    RegistrationError,
    ReplacedError,
    ToolCallError,
)

__version__ = "0.3.3"

__all__ = [
    "AgentQuayClient",
    "agent_tool",
    "LaunchInfo",
    "default_launch_info",
    "AgentQuayError",
    "BridgeConnectionError",
    "BridgeUnavailableError",
    "BridgeSpawnError",
    "RegistrationError",
    "AuthFailedError",
    "ProtocolError",
    "ReplacedError",
    "ConfirmationError",
    "InvokeTimeoutError",
    "ToolCallError",
    "__version__",
]
