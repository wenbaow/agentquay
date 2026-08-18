"""decorators.py 单测：装饰器元数据挂载与注册扫描。"""

import sys
import unittest

sys.path.insert(0, "..")

from agentquay import AgentQuayClient, agent_tool
from agentquay.decorators import TOOL_ATTR, get_tool_spec


class Music:
    @agent_tool("search", description="搜索音乐")
    def search(self, keyword: str) -> list:  # noqa: ANN401
        return []

    @agent_tool(description="默认名回退函数名")
    def play(self, song_id: str) -> None: ...

    @agent_tool("delete", description="删除", requires_confirmation=True, timeout_seconds=15)
    def delete(self, song_id: str) -> None: ...

    def plain(self) -> None: ...  # 未装饰，不应被扫描


class TestDecorator(unittest.TestCase):
    def test_spec_attached(self):
        spec = get_tool_spec(Music.search)
        self.assertIsNotNone(spec)
        assert spec is not None
        self.assertEqual(spec.name, "search")
        self.assertEqual(spec.description, "搜索音乐")
        self.assertFalse(spec.requires_confirmation)

    def test_name_fallback(self):
        spec = get_tool_spec(Music.play)
        assert spec is not None
        self.assertEqual(spec.name, "play")

    def test_confirmation_flags(self):
        spec = get_tool_spec(Music.delete)
        assert spec is not None
        self.assertTrue(spec.requires_confirmation)
        self.assertEqual(spec.timeout_seconds, 15)
        self.assertEqual(spec.confirm_timeout_seconds, 120)

    def test_register_tools_scan(self):
        client = AgentQuayClient(app_id="music-app", app_name="Music Player",
                                 auto_spawn_bridge=False)
        client.register_tools(Music())
        self.assertEqual(sorted(client.list_tools()), ["delete", "play", "search"])
        # ToolBinding 携带 schema
        binding = client._tools["search"]
        self.assertEqual(binding.input_schema["required"], ["keyword"])
        self.assertTrue(callable(binding.func))

    def test_invalid_tool_name_rejected(self):
        with self.assertRaises(ValueError):
            @agent_tool("bad name")
            def f(): ...

    def test_app_id_validation(self):
        with self.assertRaises(ValueError):
            AgentQuayClient(app_id="music_app", app_name="x")  # 下划线非法
        with self.assertRaises(ValueError):
            AgentQuayClient(app_id="Music.App", app_name="x")  # 大写/点非法
        AgentQuayClient(app_id="music-app", app_name="x")      # 合法


if __name__ == "__main__":
    unittest.main()
