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

## 发布流程（发包到各包管理器平台）

发布由 GitHub Actions 自动化：打 `vX.Y.Z` tag 即触发 `.github/workflows/release.yml`，
依次完成 Bridge 四平台交叉编译、各 SDK 打包、推送 PyPI / npm / crates.io / NuGet /
Maven Central，并创建 GitHub Release（附四平台二进制与各包产物）。**不存在可用的
CI（如首次发版）时，请先按本流程把基础设施跑通。**

### 发版步骤（常规版本）

1. **改版本号**：全项目统一升到新版本（现有约定：逐清单手工同步）——
   `sdk/python/pyproject.toml`、`sdk/typescript/package.json`、
   `sdk/rust/Cargo.toml`（workspace，两个 crate 自动继承）、两个 `.csproj`、
   `sdk/java/pom.xml`、`sdk/cpp/CMakeLists.txt`、`bridge/cmd/agentquay/main.go`。
   `python scripts/check-version.py <新版本>` 可校验一致性（发布工作流第一步也会校验）。
2. **更新 `CHANGELOG.md`**：把 `[Unreleased]` 内容整理为新版本小节，补充日期与
   Bridge 二进制重建说明。Release 说明自动取自该小节。
3. **重建并提交内嵌 Bridge 二进制**（现有约定）：本地交叉编译四平台产物（见
   README §10 或工作流 `bridge-build` 步骤），覆盖 6 个 SDK 的 `bridge_bin/` 后提交。
4. **合入 main 后打 tag**：`git tag vX.Y.Z && git push origin vX.Y.Z`——工作流自动
   执行后续全部发布动作。tag 必须指向 main 上的提交（工作流会校验）。
5. **验证发布结果**：检查 GitHub Release 附件完整；用已发布包各跑一遍最小连接
   用例（如 `pip install agentquay-sdk` 后运行 `tests/lifecycle_e2e.py`）。

### 一次性准备（首次发版前）

| 平台 | 账号/命名空间 | GitHub 仓库配置 |
|------|--------------|----------------|
| PyPI | 注册并创建项目 `agentquay-sdk`，配置 **Trusted Publishing**（OIDC 绑定本仓库、workflow 名 `release.yml`，无需 token） | 无 |
| npm | 注册并创建组织 `agentquay`（包名 `@agentquay/sdk`）；在**包的 Settings → Trusted Publisher** 绑定：Organization `wenbaow`、Repository `agentquay`、Workflow `release.yml`、Environment `npm`，并勾选允许直接 `npm publish`（**OIDC，无需 NPM_TOKEN**；首次发布前包不存在、无法配置，故首个版本曾用 granular token） | environment `npm` 无需 secret（OIDC），仅作发布审批门 |
| crates.io | 注册，包名 `agentquay` / `agentquay-macros` | environment `crates-io` 的 secret `CARGO_REGISTRY_TOKEN`（scope 限定两包） |
| NuGet | 注册，包名 `AgentQuay.Sdk` / `AgentQuay.Sdk.Wpf`；在 nuget.org → Account → Trusted Publishing 创建策略绑定本仓库 `release.yml` workflow + `nuget` 环境（**OIDC，无需 API key**） | environment `nuget` 无需 secret（OIDC），如需审批门可勾 Required reviewers |
| Maven Central | central.sonatype.com 注册，**连接 GitHub 账号完成 `io.github.wenbaow` 命名空间验证**（agentquay.com 域名已被注册，故不用 `com.agentquay`；GitHub 验证免费即时）；生成 GPG 签名密钥对并上传公钥 | environment `maven-central` 的 secrets：`OSSRH_USERNAME` / `OSSRH_PASSWORD`（Portal token）、`GPG_PRIVATE_KEY`（ASCII-armored 私钥）、`GPG_PASSPHRASE` |

发布前先在目标平台核查包名是否被占用；任一被占用需改名（如 Python 换
`agentquay-sdk-python`、Maven 换 `com.github.<user>` 用 GitHub 验证）。

### 失败处理

各平台均不允许同一版本号重复发布。任一 job 失败：
修复问题后 **bump 到新版本再发**（重新走发版步骤）；Maven Central 若已进入
staging 可先在 Portal 中 drop 再同版本重发。`github-release` job 失败不影响已发布
的包（幂等重建即可）。crates.io 的 job 依赖 GitHub Release 先创建（Rust 的
`build.rs` 要从 Release 附件按版本下载内嵌 Bridge，crates.io 单包 10MiB 上限无法
内嵌）；可直接下载本仓库源码构建的场景不受影响（走仓库内 `bridge_bin/`）。

## 许可

本项目（含 Bridge 与各语言 SDK）以 **Apache-2.0** 授权（见 [`LICENSE`](LICENSE) /
[`NOTICE`](NOTICE)）。提交贡献即表示你同意你的贡献按同样许可发布。
