"""schema.py 单测：类型注解 → JSON Schema 生成。"""

import enum
import sys
import unittest
from typing import Any, Literal, Optional

sys.path.insert(0, "..")  # 允许从 sdk/python 目录直接运行

from agentquay.schema import param_schema, _type_to_schema


class Color(enum.Enum):
    RED = "red"
    GREEN = "green"


class TestTypeToSchema(unittest.TestCase):
    def test_primitives(self):
        self.assertEqual(_type_to_schema(str), {"type": "string"})
        self.assertEqual(_type_to_schema(int), {"type": "integer"})
        self.assertEqual(_type_to_schema(float), {"type": "number"})
        self.assertEqual(_type_to_schema(bool), {"type": "boolean"})
        self.assertEqual(_type_to_schema(type(None)), {"type": "null"})

    def test_list(self):
        self.assertEqual(_type_to_schema(list[str]),
                         {"type": "array", "items": {"type": "string"}})

    def test_dict(self):
        self.assertEqual(_type_to_schema(dict[str, int]),
                         {"type": "object", "additionalProperties": {"type": "integer"}})

    def test_optional(self):
        self.assertEqual(_type_to_schema(Optional[str]),
                         {"type": ["string", "null"]})

    def test_literal(self):
        self.assertEqual(_type_to_schema(Literal["a", "b"]), {"enum": ["a", "b"]})

    def test_enum(self):
        self.assertEqual(_type_to_schema(Color), {"enum": ["red", "green"]})

    def test_unknown(self):
        self.assertEqual(_type_to_schema(Any), {})


class TestParamSchema(unittest.TestCase):
    def test_required_and_optional(self):
        def f(keyword: str, limit: int = 10) -> None: ...

        schema = param_schema(f)
        self.assertEqual(schema["type"], "object")
        self.assertEqual(schema["required"], ["keyword"])
        self.assertEqual(schema["properties"]["keyword"], {"type": "string"})
        self.assertEqual(schema["properties"]["limit"]["type"], "integer")
        self.assertEqual(schema["properties"]["limit"]["default"], 10)

    def test_default_none_is_optional(self):
        def f(tag: Optional[str] = None) -> None: ...

        schema = param_schema(f)
        self.assertNotIn("required", schema)
        self.assertEqual(schema["properties"]["tag"]["type"], ["string", "null"])

    def test_self_skipped(self):
        class C:
            def method(self, x: int) -> None: ...

        schema = param_schema(C().method)
        self.assertEqual(schema["required"], ["x"])

    def test_varargs_skipped(self):
        def f(a: str, *args: int, **kwargs: Any) -> None: ...

        schema = param_schema(f)
        self.assertEqual(schema["required"], ["a"])

    def test_nested(self):
        def f(items: list[dict[str, int]]) -> None: ...

        schema = param_schema(f)
        self.assertEqual(schema["properties"]["items"],
                         {"type": "array", "items": {"type": "object",
                                                      "additionalProperties": {"type": "integer"}}})


if __name__ == "__main__":
    unittest.main()
