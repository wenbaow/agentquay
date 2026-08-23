"""页面智能路由单测（方案 §9 Phase 1 验收，与 C# PageRouterTests 对齐）：
惰性注册元数据立即可见、单飞去重、弱引用重建、激活超时、无工厂报错、
激活钩子执行一次、显式注销、工厂注册注解校验。"""

from __future__ import annotations

import asyncio
import sys
import unittest

sys.path.insert(0, "..")

from agentquay import AgentQuayClient, agent_tool
from agentquay.client import ToolBinding
from agentquay.decorators import TOOL_ATTR, get_tool_spec
from agentquay.errors import (
    PageActivationError,
    PageActivationTimeoutError,
    PageNotFoundError,
)

CREATED = []


class SearchPage:
    def __init__(self) -> None:
        self.seq = len(CREATED)
        CREATED.append(self)

    @agent_tool("search", description="搜索音乐")
    async def search(self, keyword: str) -> dict:
        return {"page": self.seq, "keyword": keyword}

    @agent_tool("ping", description="同步探针")
    def ping(self) -> int:
        return self.seq

    def plain(self) -> None:  # 未装饰，不应被扫描
        pass


def new_client(**kw) -> AgentQuayClient:
    return AgentQuayClient(app_id="music-app", app_name="Music Player",
                           auto_spawn_bridge=False, **kw)


class TestLazyRegistration(unittest.TestCase):
    def setUp(self) -> None:
        CREATED.clear()

    def test_class_with_page_key_registers_metadata_immediately(self):
        client = new_client()
        client.register_tools(SearchPage, page_key="SearchPage")
        self.assertEqual(sorted(client.list_tools()), ["ping", "search"])
        self.assertEqual(len(CREATED), 0)  # 未创建实例
        binding = client._tools["search"]
        self.assertEqual(binding.page_key, "SearchPage")
        self.assertIsNone(binding.instance)
        self.assertIsNone(binding.live)
        self.assertEqual(binding.input_schema["required"], ["keyword"])

    def test_class_without_page_key_instantiates_immediately(self):
        client = new_client()
        client.register_tools(SearchPage)
        self.assertEqual(len(CREATED), 1)  # 立即实例化（RegisterTools<T>() 语义）
        binding = client._tools["search"]
        self.assertIs(binding.instance, CREATED[0])

    def test_factory_registration_requires_return_annotation(self):
        client = new_client()
        with self.assertRaises(ValueError):
            client.register_tools_factory(lambda: SearchPage(), "SearchPage")  # 无注解
        with self.assertRaises(ValueError):
            client.register_tools_factory(lambda: None, "")  # 空 page_key

    def test_duplicate_name_across_pages_rejected(self):
        client = new_client()
        client.register_tools(SearchPage, page_key="SearchPage")
        with self.assertRaises(ValueError) as ctx:
            client.register_tools(SearchPage, page_key="SearchPageB")
        self.assertIn("跨页面也须全局唯一", str(ctx.exception))

    def test_register_payload_includes_page_key(self):
        client = new_client()
        client.register_tools(SearchPage, page_key="SearchPage")
        payload = client._register_payload()
        tools = payload["tools"]
        self.assertEqual({t["name"]: t.get("pageKey") for t in tools},
                         {"search": "SearchPage", "ping": "SearchPage"})


class TestRouting(unittest.TestCase):
    def setUp(self) -> None:
        CREATED.clear()

    async def _first_call(self, client: AgentQuayClient) -> dict:
        binding = client._tools["search"]
        return await client._call(binding, {"keyword": "七里香"}, 30)

    def test_first_call_creates_then_reuses(self):
        client = new_client()
        client.register_tools(SearchPage, page_key="SearchPage")
        binding = client._tools["search"]
        asyncio.run(self._first_call(client))
        self.assertEqual(len(CREATED), 1)
        self.assertIsNotNone(binding.live)
        # 第二次调用：弱引用存活 → 复用同一实例
        asyncio.run(self._first_call(client))
        self.assertEqual(len(CREATED), 1)

    def test_concurrent_calls_single_flight(self):
        client = new_client()
        client.register_tools(SearchPage, page_key="SearchPage")

        async def run() -> list:
            binding = client._tools["search"]
            return await asyncio.gather(*[
                client._call(binding, {"keyword": f"k{i}"}, 30) for i in range(10)
            ])

        results = asyncio.run(run())
        self.assertEqual(len({r["page"] for r in results}), 1)  # 只创建一个实例
        self.assertEqual(len(CREATED), 1)
        self.assertEqual(results[0]["keyword"], "k0")

    def test_unregister_then_recreate(self):
        client = new_client()
        client.register_tools(SearchPage, page_key="SearchPage")
        binding = client._tools["search"]
        asyncio.run(self._first_call(client))
        self.assertEqual(len(CREATED), 1)
        client.unregister_page("SearchPage")
        self.assertIsNone(binding.live)
        asyncio.run(self._first_call(client))
        self.assertEqual(len(CREATED), 2)  # 重建

    def test_no_factory_raises_page_not_found(self):
        client = new_client()
        # 直接构造"无实例无工厂"的绑定（页面未打开且未配工厂）
        spec = get_tool_spec(SearchPage.search)
        assert spec is not None
        binding = ToolBinding(name=spec.name, description=spec.description,
                              input_schema=spec.func and {}, requires_confirmation=False,
                              timeout_seconds=30, confirm_timeout_seconds=120,
                              func=spec.func or SearchPage.search, is_async=True,
                              page_key="SearchPage")
        client._tools[spec.name] = binding

        with self.assertRaises(PageNotFoundError):
            asyncio.run(client._call(binding, {"keyword": "x"}, 30))

    def test_activation_timeout(self):
        client = new_client(page_activation_timeout=0.2)
        client.register_tools(SearchPage, page_key="SearchPage")
        client.set_page_activator(
            "SearchPage",
            await_ready=lambda page: asyncio.sleep(60),  # 永不就绪
        )
        binding = client._tools["search"]
        with self.assertRaises(PageActivationTimeoutError):
            asyncio.run(client._call(binding, {"keyword": "x"}, 30))

    def test_activation_failure_then_rebuild(self):
        client = new_client()

        def build_flaky() -> _FlakyPage:
            return _FlakyPage()

        client.register_tools_factory(build_flaky, "FlakyPage")

        async def run() -> int:
            binding = client._tools["f"]
            with self.assertRaises(PageActivationError):
                await client._call(binding, {}, 30)
            # 失败后回 NotLoaded：下次调用重建成功
            return await client._call(binding, {}, 30)

        result = asyncio.run(run())
        self.assertEqual(result, 0)

    def test_activator_runs_once_under_concurrent_calls(self):
        client = new_client()
        navigated = {"n": 0, "ready": 0}
        client.register_tools(SearchPage, page_key="SearchPage")
        client.set_page_activator(
            "SearchPage",
            navigate=lambda page: navigated.__setitem__("n", navigated["n"] + 1),
            await_ready=lambda page: asyncio.sleep(0.01),
        )
        binding = client._tools["search"]

        async def run() -> None:
            await asyncio.gather(*[
                client._call(binding, {"keyword": "x"}, 30) for _ in range(10)
            ])

        asyncio.run(run())
        self.assertEqual(navigated["n"], 1)  # 导航只执行一次
        self.assertEqual(len(CREATED), 1)


class _FlakyPage:
    """模块级（工厂返回注解经 get_type_hints 解析需要能全局查找到）。"""

    def __init__(self) -> None:
        if _FLAKY["on"]:
            _FLAKY["on"] = False
            raise RuntimeError("DI 容器不可用")
        self.seq = len(CREATED)
        CREATED.append(self)

    @agent_tool("f", description="f")
    def f(self) -> int:
        return self.seq


_FLAKY = {"on": True}


if __name__ == "__main__":
    unittest.main()