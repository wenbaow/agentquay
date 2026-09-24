#!/usr/bin/env python3
"""从 CHANGELOG.md 提取指定版本的小节，作为 GitHub Release 说明。

用法: python scripts/release-notes.py <版本号，可带 v 前缀>
未找到对应小节时输出兜底文本并退出 0（不阻塞发布，runbook 要求发布前更新 CHANGELOG）。
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def main() -> int:
    if len(sys.argv) != 2:
        print(f"用法: {sys.argv[0]} <版本号>（例如 0.3.0）")
        return 2
    version = sys.argv[1].lstrip("v")

    text = (ROOT / "CHANGELOG.md").read_text(encoding="utf-8")
    sections = re.split(r"^##\s+", text, flags=re.MULTILINE)
    for section in sections[1:]:
        title = section.splitlines()[0].strip()
        if title.startswith(f"[{version}]"):
            print(f"## {title}")
            print("".join(section.splitlines(keepends=True)[1:]).rstrip())
            return 0

    print(f"AgentQuay v{version}（CHANGELOG.md 中未找到 [{version}] 小节，发布前请补充）")
    return 0


if __name__ == "__main__":
    sys.exit(main())