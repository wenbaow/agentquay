#!/usr/bin/env python3
"""校验全项目各清单文件的版本号与给定版本一致（发布工作流第一步）。

用法: python scripts/check-version.py <版本号>

检查范围（AgentQuay 所有包/Bridge 的版本唯一来源，约定手工同步）：
  - sdk/python/pyproject.toml            [project] version
  - sdk/typescript/package.json          "version"
  - sdk/rust/Cargo.toml                  [workspace.package] version
  - sdk/dotnet/.../AgentQuay.Sdk.csproj  <Version>
  - sdk/dotnet/.../AgentQuay.Sdk.Wpf.csproj <Version>
  - sdk/java/pom.xml                     <version>（根项目）
  - sdk/cpp/CMakeLists.txt               project(... VERSION ...)
  - bridge/cmd/agentquay/main.go         Version = "..."

任一清单缺失或不一致即退出码 1，并列出差异。
"""

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

CHECKS = [
    (
        "Python SDK (agentquay-sdk)",
        ROOT / "sdk/python/pyproject.toml",
        r'^version\s*=\s*"([^"]+)"',
    ),
    (
        "TypeScript SDK (@agentquay/sdk)",
        ROOT / "sdk/typescript/package.json",
        None,  # JSON 字段
    ),
    (
        "Rust workspace (agentquay / agentquay-macros)",
        ROOT / "sdk/rust/Cargo.toml",
        r'^version\s*=\s*"([^"]+)"',
    ),
    (
        "Rust 跨 crate 依赖 (agentquay-macros)",
        ROOT / "sdk/rust/Cargo.toml",
        r'^agentquay-macros\s*=\s*\{[^}]*version\s*=\s*"([^"]+)"',
    ),
    (
        ".NET SDK (AgentQuay.Sdk)",
        ROOT / "sdk/dotnet/src/AgentQuay.Sdk/AgentQuay.Sdk.csproj",
        r"<Version>([^<]+)</Version>",
    ),
    (
        ".NET WPF (AgentQuay.Sdk.Wpf)",
        ROOT / "sdk/dotnet/src/AgentQuay.Sdk.Wpf/AgentQuay.Sdk.Wpf.csproj",
        r"<Version>([^<]+)</Version>",
    ),
    (
        "Java SDK (io.github.wenbaow:agentquay-sdk)",
        ROOT / "sdk/java/pom.xml",
        r"<version>([^<]+)</version>",
    ),
    (
        "C++ SDK (agentquay-cpp)",
        ROOT / "sdk/cpp/CMakeLists.txt",
        r"project\([^)]*VERSION ([0-9][^ )]+)",
    ),
    (
        "Bridge (Go)",
        ROOT / "bridge/cmd/agentquay/main.go",
        r'Version\s*=\s*"([^"]+)"',
    ),
]


def extract(path: Path, pattern: str | None) -> str | None:
    text = path.read_text(encoding="utf-8")
    if pattern is not None:
        match = re.search(pattern, text, re.MULTILINE)
        return match.group(1) if match else None
    # package.json：取顶层的 "version"
    return json.loads(text).get("version")


def main() -> int:
    if len(sys.argv) != 2:
        print(f"用法: {sys.argv[0]} <版本号>（例如 0.3.0）")
        return 2
    expected = sys.argv[1].lstrip("v")  # 容忍 v 前缀

    failures = []
    for name, path, pattern in CHECKS:
        if not path.is_file():
            failures.append((name, path, "<文件不存在>"))
            continue
        actual = extract(path, pattern)
        if actual != expected:
            failures.append((name, path, actual or "<未找到版本号>"))

    if failures:
        print(f"版本一致性校验失败（期望 {expected}）：")
        for name, path, actual in failures:
            print(f"  ✗ {name}: {path.relative_to(ROOT)} → {actual}")
        return 1

    print(f"✓ 全部 {len(CHECKS)} 处清单版本一致: {expected}")
    return 0


if __name__ == "__main__":
    sys.exit(main())