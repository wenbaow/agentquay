"""从 Python 类型注解生成 JSON Schema（设计文档 §4.6）。

支持：str/int/float/bool/None、list[T]、dict[str, T]、Optional[T]、Union、
Literal、enum.Enum，以及可选的 Pydantic v2 模型（model_json_schema）。
未知类型回退为 ``{}``（任意值）。
"""

from __future__ import annotations

import enum
import inspect
import json
import typing
from typing import Any, get_args, get_origin, get_type_hints


def _safe_get_type_hints(func: Any) -> dict[str, Any]:
    """获取类型注解；注解无法解析时回退为空（不阻断注册）。"""
    try:
        return get_type_hints(func)
    except Exception:
        return {}


def _type_to_schema(tp: Any) -> dict[str, Any]:
    """单个类型 → JSON Schema 片段。"""
    if tp is inspect.Parameter.empty or tp is None:
        if tp is None:
            return {"type": "null"}
        return {}

    # typing 泛型（list[int]、Optional[str] 等）
    origin = get_origin(tp)
    if origin is not None:
        args = get_args(tp)
        if origin is typing.Union:
            # Optional[X] == Union[X, None] → nullable
            non_null = [a for a in args if a is not type(None)]
            if len(non_null) == 1:
                inner = _type_to_schema(non_null[0])
                if args != non_null:  # 含 None → 可空
                    if "type" in inner:
                        inner = {**inner, "type": [inner["type"], "null"]}
                    else:
                        inner = {**inner, "nullable": True}
                return inner
            return {"anyOf": [_type_to_schema(a) for a in args]}
        if origin in (list, typing.List):
            return {"type": "array", "items": _type_to_schema(args[0]) if args else {}}
        if origin in (dict, typing.Dict):
            value_schema = _type_to_schema(args[1]) if len(args) > 1 else {}
            return {"type": "object", "additionalProperties": value_schema}
        if origin in (tuple, typing.Tuple):
            return {"type": "array", "items": [_type_to_schema(a) for a in args]}
        if origin is typing.Literal:
            return {"enum": list(args)}
        if origin is typing.Annotated:
            return _type_to_schema(args[0])
        return {}

    if tp is str:
        return {"type": "string"}
    if tp is int:
        return {"type": "integer"}
    if tp is float:
        return {"type": "number"}
    if tp is bool:
        return {"type": "boolean"}
    if tp is type(None):
        return {"type": "null"}

    # enum.Enum → enum
    if isinstance(tp, type) and issubclass(tp, enum.Enum):
        return {"enum": [m.value for m in tp]}

    # Pydantic v2 模型（可选增强）
    if isinstance(tp, type):
        try:
            from pydantic import BaseModel

            if issubclass(tp, BaseModel):
                return typing.cast(dict, tp.model_json_schema())
        except ImportError:
            pass

    return {}


def param_schema(func: Any) -> dict[str, Any]:
    """从函数签名生成 inputSchema（type=object, properties, required）。

    规则：
    - 跳过 self/cls 与 *args/**kwargs
    - 有默认值且默认值非 None → 可选（不在 required 中），default 写入 schema
    - 有默认值 None → 可选
    - 无默认值 → 必填
    """
    hints = _safe_get_type_hints(func)
    sig = inspect.signature(func)
    properties: dict[str, Any] = {}
    required: list[str] = []

    for pname, param in sig.parameters.items():
        if pname in ("self", "cls"):
            continue
        if param.kind in (inspect.Parameter.VAR_POSITIONAL, inspect.Parameter.VAR_KEYWORD):
            continue

        tp = hints.get(pname, inspect.Parameter.empty)
        prop = _type_to_schema(tp)

        has_default = param.default is not inspect.Parameter.empty
        if has_default:
            default = param.default
            if default is not None:
                try:
                    json.dumps(default)  # 仅保留可 JSON 序列化的默认值
                    prop["default"] = default
                except (TypeError, ValueError):
                    pass
            # 有默认值（含 None）→ 可选
        else:
            required.append(pname)

        properties[pname] = prop

    schema: dict[str, Any] = {"type": "object", "properties": properties}
    if required:
        schema["required"] = required
    return schema
