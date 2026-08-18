"""lifecycle_e2e 的子进程 harness：模拟一个接入 SDK 的桌面应用（embedded-bridge-lifecycle.md 场景测试用）。

用法: python _lifecycle_harness.py <appId> <appName>

行为:
- 以 auto_spawn_bridge=True 连接（若本地无桥则拉起内嵌 Bridge；首个连接的成为"宿主"）。
- 保持连接，监听 stdin：
    close   -> 优雅关闭（close()），进程退出
    其它      -> 忽略
"""

import asyncio
import sys

from agentquay import AgentQuayClient, agent_tool


class Ctl:
    @agent_tool("echo", description="回显（探活用）")
    def echo(self, msg: str = "") -> dict:
        return {"echo": msg}


async def main() -> None:
    app_id, app_name = sys.argv[1], sys.argv[2]
    client = AgentQuayClient(app_id=app_id, app_name=app_name, auto_spawn_bridge=True)
    client.register_tools(Ctl())

    stop = asyncio.Event()

    async def stdin_reader() -> None:
        loop = asyncio.get_running_loop()
        while True:
            line = await loop.run_in_executor(None, sys.stdin.readline)
            if not line:
                return
            if line.strip() == "close":
                stop.set()
                return

    reader = asyncio.create_task(stdin_reader())
    conn = asyncio.create_task(client.connect())
    print(f"READY {app_id}", flush=True)
    await stop.wait()
    print(f"CLOSING {app_id}", flush=True)
    await client.close()
    conn.cancel()
    try:
        await conn
    except asyncio.CancelledError:
        pass


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
