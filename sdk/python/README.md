# agentquay-sdk — AgentQuay Python SDK

让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。加个装饰器，其余全部由框架处理。

```
pip install agentquay-sdk        # 依赖：websockets（keyring/pydantic 可选）
```

## 用法

```python
import asyncio
from agentquay import AgentQuayClient, agent_tool

class MusicController:
    @agent_tool("search", description="搜索音乐库中的歌曲")
    def search(self, keyword: str, limit: int = 10) -> list[dict]:
        """搜索音乐库。"""
        ...

    @agent_tool("play", description="播放指定歌曲")
    async def play(self, song_id: str) -> dict:
        """异步方法同样支持。"""
        ...

    @agent_tool("delete", description="删除歌曲", requires_confirmation=True)
    def delete(self, song_id: str) -> dict:
        """危险操作：Bridge 会先向应用弹确认框，用户确认后才执行。"""
        ...

async def main():
    client = AgentQuayClient(
        app_id="music-app",        # [a-z0-9-]{1,48}，禁止 _ 和 .
        app_name="Music Player",
        port=0,                    # 0 = 从 ~/.agentquay/port 自动读取
        auto_spawn_bridge=True,    # 未检测到 Bridge 时自动拉起内嵌二进制
    )
    client.register_tools(MusicController())
    await client.connect()         # 保持连接：心跳 + 断线指数退避重连

asyncio.run(main())
```

## 特性

- **`@agent_tool` 装饰器**：`name` 缺省回退函数名；描述缺省回退 docstring 首行；
  同步函数经 `asyncio.to_thread` 执行，async 函数直接 await
- **类型注解 → JSON Schema**：`str/int/float/bool`、`list[T]`、`dict[str, T]`、
  `Optional[T]`、`Literal`、`enum.Enum` 自动生成；Pydantic v2 模型可选增强
- **确认弹窗**：tkinter OS 原生对话框（三平台标准库）；无显示环境回退控制台；
  可用 `on_confirm` 回调自定义
- **token 持久化**：keyring（Windows Credential Manager / Keychain / libsecret），
  不可用时回退 `~/.agentquay/tokens.json`（0600）
- **心跳 + 重连**：30s 应用层 ping/pong；断线指数退避重连（1s → 30s），自动携带 token；
  同 appId 新实例注册时旧连接收到 `disconnect(replaced)` 并停止重连
- **auto-spawn**：本地无 Bridge 时自动拉起内嵌二进制（`serve --embedded`，
  日志写应用缓存目录），SDK 退出时自动终止；拉取失败给出明确出路提示

## 确认流程

`requires_confirmation=True` 的 tool 被调用时，Bridge 向应用发送 `confirm` 消息：

```python
client = AgentQuayClient(..., on_confirm=my_confirm_cb)

async def my_confirm_cb(message: str, arguments: dict | None) -> bool:
    """返回 True=确认 / False=取消。支持同步与异步回调。"""
    print(f"⚠️ {message} {arguments}")
    return input("确认执行？[y/N] ").strip().lower() == "y"
```

## 自定义确认弹窗示例（tkinter）

```python
def tk_confirm(message: str, arguments: dict | None) -> bool:
    import tkinter as tk
    from tkinter import messagebox
    root = tk.Tk(); root.withdraw()
    return messagebox.askyesno("AgentQuay 确认", f"{message}\n{arguments}")
```

## 开发

```bash
pip install websockets
python -m unittest discover -s tests        # 18 项单测
python tests/e2e_test.py                    # 26 项端到端联调（需先构建 Bridge）
python examples/music_app.py                # 示例应用
```

## 协议

- 注册：`register`（含 protocolVersion、authToken、tools 元数据）
- 调用：Bridge → `invoke` → 应用执行 → `result`
- 确认：Bridge → `confirm` → 应用弹窗 → `confirm_result`
- 心跳：`ping` / `pong`（30s）

详见 [`README.md`](../../README.md) §2.1（Bridge↔应用协议）与 [`sdk-tutorial.md`](../../sdk-tutorial.md) §5.1（Python SDK 用法）。
