"""token 持久化：优先系统凭证库（keyring，可选依赖），不可用时回退到本地文件。

存储位置：keyring service "agentquay" / 或 ~/.agentquay/tokens.json（0600 权限）。
"""

from __future__ import annotations

import json
import logging
import os
from pathlib import Path

logger = logging.getLogger("agentquay")

try:  # 可选依赖：keyring（macOS Keychain / Windows Credential Manager / libsecret）
    import keyring as _keyring
    import keyring.errors as _keyring_errors
except ImportError:  # pragma: no cover
    _keyring = None  # type: ignore[assignment]
    _keyring_errors = None  # type: ignore[assignment]

KEYRING_SERVICE = "agentquay"

# 回退文件路径：~/.agentquay/tokens.json
def _fallback_file() -> Path:
    return Path.home() / ".agentquay" / "tokens.json"


class TokenStore:
    """appId → token 的持久化存储。"""

    def __init__(self, app_id: str, use_keyring: bool = True):
        self.app_id = app_id
        self.use_keyring = use_keyring and _keyring is not None

    def get(self) -> str | None:
        if self.use_keyring:
            try:
                token = _keyring.get_password(KEYRING_SERVICE, self.app_id)
                if token:
                    return token
            except Exception as exc:  # keyring 后端异常 → 回退文件
                logger.warning("keyring 读取失败，回退文件存储: %s", exc)
        return self._read_file()

    def set(self, token: str) -> None:
        if self.use_keyring:
            try:
                _keyring.set_password(KEYRING_SERVICE, self.app_id, token)
                return
            except Exception as exc:
                logger.warning("keyring 写入失败，回退文件存储: %s", exc)
        self._write_file(token)

    def _read_file(self) -> str | None:
        path = _fallback_file()
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
            return data.get(self.app_id)
        except (FileNotFoundError, json.JSONDecodeError):
            return None

    def _write_file(self, token: str) -> None:
        path = _fallback_file()
        path.parent.mkdir(parents=True, exist_ok=True)
        try:
            data = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
        except json.JSONDecodeError:
            data = {}
        data[self.app_id] = token
        path.write_text(json.dumps(data, indent=2), encoding="utf-8")
        try:
            os.chmod(path, 0o600)
        except OSError:
            pass  # Windows 无 chmod 语义
