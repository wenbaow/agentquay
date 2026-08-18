# 贡献指南

欢迎参与 AgentQuay！无论是对 Bridge（Go）还是六种语言 SDK 的改进、文档补充、
Bug 修复或新功能，都请先阅读本文。

## 提交 Issue

- **Bug 报告**：说明环境（OS / 语言 / SDK 版本 / Agent 客户端）、复现步骤、
  期望行为与实际行为，附上相关日志（`agentquay logs` 或 SDK 侧日志）。
- **功能建议**：说明使用场景与期望的行为，越具体越好。

## 本地开发

各模块的最低要求：

| 模块 | 工具链 |
|------|--------|
| `bridge/` | Go 1.25+ |
| `sdk/python` | Python 3.10+ |
| `sdk/typescript` | Node 18+，npm |
| `sdk/java` | JDK 11+，Maven |
| `sdk/dotnet` | .NET 8+ |
| `sdk/cpp` | CMake + Ninja，Qt 6.5+ |
| `sdk/rust` | Rust（edition 2021） |

构建与测试命令请参考根目录 [`README.md`](README.md) 第 9 节「验证」，修改任一语言
后请**运行对应模块的全部测试**（单测 + e2e），改动 Bridge 协议或行为时需保证六个
SDK 的 e2e 均通过。

## 代码风格

- **Go（Bridge）**：一律 `gofmt`；改动后运行 `go vet ./...`。
- **Rust**：`rustfmt` + `cargo clippy`（无警告）。
- **TypeScript**：`npm run build`（`tsc` strict）通过。
- **C#**：build 无 warning（项目已 `Nullable enable` + `LangVersion latest`）。
- **Python**：遵循 PEP 8，保持现有 docstring 风格。
- **提交说明**：推荐 Conventional Commits（`feat:` / `fix:` / `docs:` / `refactor:` /
  `test:` / `chore:`），便于生成 changelog。

## 提交与 PR

1. 从 `main` 拉出分支，命名如 `fix/xxx`、`feat/xxx`。
2. 提交前跑一遍改动模块的测试。
3. PR 描述说明改动动机、影响面与验证方式；涉及协议/行为变更请在 PR 中注明。

## 许可

本项目（含 Bridge 与各语言 SDK）以 **Apache-2.0** 授权（见 [`LICENSE`](LICENSE) /
[`NOTICE`](NOTICE)）。提交贡献即表示你同意你的贡献按同样许可发布。
