"""AgentQuay Python SDK 示例：音乐应用。

运行前确保 Bridge 可用（SDK 会自动拉起内嵌 Bridge，或先运行 `agentquay start --daemon`）:

    python examples/music_app.py

然后用 MCP 客户端（opencode / codex / zcode / Claude Desktop 或任何 MCP 客户端）
连接 http://127.0.0.1:19846/mcp，即可发现并调用:
    music-app_search / music-app_play / music-app_delete（需用户确认）
"""

import asyncio
import logging
import random
import time

from agentquay import AgentQuayClient, agent_tool

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")

SONGS = [
    {"id": "1", "title": "七里香", "artist": "周杰伦", "duration": 243},
    {"id": "2", "title": "晴天", "artist": "周杰伦", "duration": 269},
    {"id": "3", "title": "海阔天空", "artist": "Beyond", "duration": 326},
    {"id": "4", "title": "平凡之路", "artist": "朴树", "duration": 302},
]

currently_playing: dict | None = None


class MusicController:
    @agent_tool("search", description="搜索音乐库中的歌曲")
    def search(self, keyword: str, limit: int = 10) -> list[dict]:
        """搜索音乐库。"""
        kw = keyword.lower()
        results = [s for s in SONGS if kw in s["title"].lower() or kw in s["artist"].lower()]
        return results[:limit]

    @agent_tool("play", description="播放指定歌曲")
    def play(self, song_id: str) -> dict:
        """播放歌曲。"""
        global currently_playing
        song = next((s for s in SONGS if s["id"] == song_id), None)
        if song is None:
            raise ValueError(f"歌曲不存在: {song_id}")
        currently_playing = song
        return {"status": "playing", "song": song}

    @agent_tool("now_playing", description="查看当前播放的歌曲")
    def now_playing(self) -> dict:
        """当前播放。"""
        return {"song": currently_playing}

    @agent_tool("stop", description="停止播放")
    def stop(self) -> dict:
        """停止播放。"""
        global currently_playing
        currently_playing = None
        return {"status": "stopped"}

    @agent_tool(
        "delete",
        description="从音乐库删除歌曲（危险操作，需要用户确认）",
        requires_confirmation=True,
    )
    def delete(self, song_id: str) -> dict:
        """删除歌曲——危险操作，Bridge 会先弹确认框。"""
        global SONGS
        song = next((s for s in SONGS if s["id"] == song_id), None)
        if song is None:
            raise ValueError(f"歌曲不存在: {song_id}")
        SONGS = [s for s in SONGS if s["id"] != song_id]
        return {"status": "deleted", "song": song}

    @agent_tool("simulate_slow", description="模拟慢操作（测试执行超时 -32004）")
    def simulate_slow(self, seconds: float = 35.0) -> dict:
        """慢操作：超过默认 30s 执行超时。"""
        time.sleep(seconds)
        return {"elapsed": seconds}

    @agent_tool("simulate_failure", description="模拟业务失败（错误透传）")
    def simulate_failure(self) -> dict:
        """业务错误。"""
        raise RuntimeError("音乐库服务暂时不可用")


async def main() -> None:
    client = AgentQuayClient(
        app_id="music-app",
        app_name="Music Player",
        host="localhost",
        port=0,                 # 从 ~/.agentquay/port 自动读取
        auto_spawn_bridge=True, # 未检测到服务时自动拉起内嵌 Bridge
    )
    client.register_tools(MusicController())
    print(f"已注册 tools: {client.list_tools()}")
    print("等待 Agent 调用…（Ctrl+C 退出）")
    await client.connect()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\n已退出")
