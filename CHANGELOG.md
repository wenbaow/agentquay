# Changelog

本项目采用语义化版本（SemVer）。各 SDK 与 Bridge 的包版本以对应清单文件为准。

## [Unreleased]

### Changed

- **npm 发布迁移到 Trusted Publishing（OIDC）**：`publish-npm` 不再使用 `NPM_TOKEN`
  （可在下次发布成功后从仓库删除该 secret），改由 GitHub Actions OIDC 自动鉴权并
  自动生成 provenance 证明；Node 版本 20 → 24（trusted publishing 要求 Node ≥ 22.14 /
  npm CLI ≥ 11.5.1）。npm 侧需先在 `@agentquay/sdk` 的 Settings → Trusted Publisher
  绑定仓库与工作流（见 CONTRIBUTING.md「发布流程」）。
- 首次发布中 0.3.0–0.3.3 为各平台覆盖不全的中间版本，已按平台清理：PyPI yank
  0.3.0–0.3.3、npm unpublish 0.3.2/0.3.3、NuGet unlist 0.3.1–0.3.3；Maven Central
  产物不可删除（0.3.3 保留）。**0.3.4 是首个五平台齐全的版本。**

## [0.3.4] - 2026-09-24

### Fixed

- **GitHub Release 的校验和步骤**：`sha256sum *` 在 `release-assets/` 含子目录
  （`bridge-binaries/`）时报 `Is a directory` 而失败，导致 Release 创建与
  crates.io 发布被连带跳过。改为 `find` 递归收集文件生成校验和，并设为
  `continue-on-error`——校验和失败不再阻断 Release 与 crates.io。

### Changed

- 全项目版本号升至 **0.3.4**（0.3.3 已完成 PyPI / npm / NuGet / Maven Central
  四平台发布，本版补齐 GitHub Release 与 crates.io 首发）；四平台 Bridge 二进制
  以当前源码重建并同步。

## [0.3.3] - 2026-09-24

### Fixed

- **Maven Central 发布（最后一个平台）**：`central-publishing-maven-plugin:0.5.0`
  在 Portal API 响应新增 `warnings` 字段后反序列化失败
  （`UnrecognizedPropertyException: Unrecognized field "warnings"`），导致
  `mvn deploy` 在产物已上传、签名已完成之后仍报错。升级插件到 **0.11.0**。
  （此前的失败点依次为：GPG 批处理口令、Java e2e 平台匹配，均已在本轮修复。）

### Changed

- 全项目版本号升至 **0.3.3**；四平台 Bridge 二进制以当前源码重建并同步。
- 发布工作流：Maven 步骤增加失败诊断，把 `[ERROR]` 行输出为 GitHub 注解
  （注解可无认证读取，便于排障）。

## [0.3.2] - 2026-09-24

### Fixed

- **跨平台可执行位**（CI 在 Linux 上运行 Java e2e 时暴露）：内嵌 Bridge 二进制在
  打包/解压链路上可能丢失 Unix 可执行位，导致 Linux/macOS 上 auto-spawn 报
  `Permission denied`。修复分三层：
  - 仓库内 18 个 unix 二进制以 `100755` 权限位记录（git），发布工作流在拷贝 CI
    产物后显式 `chmod +x`，保证 wheel / npm tarball / nupkg / jar 携带可执行位；
  - **Java SDK**：从 jar 解压内嵌二进制后显式 `setExecutable(true)`（jar 资源
    不携带 Unix 权限位）；
  - **Python / TypeScript SDK**：运行内嵌二进制前兜底 `chmod`（只读安装目录下
    忽略失败，交由 spawn 报错提示 `AGENTQUAY_BRIDGE_BIN`）。
- **Java e2e 测试的平台匹配**：在 `bridge/dist` 查找二进制时仅用 `agentquay-`
  前缀，非 Windows 平台会选中 macOS 产物（CI 上导致 Maven 发布在测试阶段失败）；
  改为按当前 OS + 架构精确匹配。

### Changed

- 全项目版本号升至 **0.3.2**；四平台 Bridge 二进制（darwin-amd64 / darwin-arm64 /
  linux-amd64 / windows-amd64）以当前源码重建并同步到各 SDK 的 `bridge_bin/`。

## [0.3.1] - 2026-09-24

### Fixed

- **首次多平台发布的三个 CI 问题**（v0.3.0 的工作流在 npm / NuGet / Maven 三平台失败，
  本版修复；0.3.0 仅 PyPI 发布成功，其余平台由 0.3.1 承接）：
  - **Maven Central**：`maven-gpg-plugin` 默认 `useAgent=true`，依赖 gpg-agent 交互式
    输入口令，CI 无人值守环境签名失败（`gpg: signing failed: Bad passphrase`）。
    改为官方推荐的无人值守配置：`useAgent=false` + `MAVEN_GPG_PASSPHRASE` 环境变量
    批处理模式（本地已实测签名成功）。
  - **NuGet**：PowerShell 不对原生命令展开通配符，`dotnet nuget push artifacts/*.nupkg`
    报 `error: File does not exist (artifacts/*.nupkg)`；改为 bash 循环逐个推送。
  - **npm**：granular access token 需启用 Bypass 2FA，并同时授予包与组织（agentquay）
    写权限（凭据侧修正，工作流无需改动）。

### Changed

- 全项目版本号升至 **0.3.1**；四平台 Bridge 二进制（darwin-amd64 / darwin-arm64 /
  linux-amd64 / windows-amd64）全部以当前源码重建并同步到各 SDK 的 `bridge_bin/`。

## [0.3.0] - 2026-09-24

### Added

- **发布自动化**：新增 `.github/workflows/release.yml`——打 `vX.Y.Z` tag 触发，
  交叉编译四平台 Bridge 二进制、组装并推送 PyPI（Trusted Publishing）/ npm /
  crates.io / NuGet / Maven Central（Central Portal + GPG 签名）五个平台，并创建
  GitHub Release（附二进制与各包产物 + SHA256 校验和）。新增
  `scripts/check-version.py`（版本一致性校验，发布前哨兵）与
  `scripts/release-notes.py`（Release 说明取自 CHANGELOG）。
- 各 SDK 补齐包管理器发布元数据：Python（PEP 639 SPDX license + license-files）、
  npm（author/repository/publishConfig）、.NET（RepositoryUrl）、Java
  （developers/scm/sources/javadoc/GPG 签名/Central Portal 插件、可复现构建
  outputTimestamp）。

### Changed

- **Rust SDK 内嵌 Bridge 二进制机制**：crates.io 单包上限 10MiB，无法携带
  四平台二进制（共 ~37MB）。`bridge_bin/` 从发布的 crate 中排除，改为构建期由
  `build.rs` 准备：本地开发用仓库内提交的二进制（离线可用），从 crates.io 安装时
  按 `CARGO_PKG_VERSION` 从 GitHub Releases 下载对应平台二进制并缓存（首次构建需
  联网，下载失败报错并提示关闭 `embedded-bridge` 特性）。`agentquay-macros`
  依赖版本改为继承 workspace，发布顺序 macros → 主 crate。
- 全项目版本号升至 **0.3.0**；四平台 Bridge 二进制（darwin-amd64 / darwin-arm64 /
  linux-amd64 / windows-amd64）全部以当前源码重建并同步到各 SDK 的 `bridge_bin/`。
- **Java SDK 发布坐标**：Maven Central groupId 采用 `io.github.wenbaow`（GitHub
  账号命名空间验证），即 `io.github.wenbaow:agentquay-sdk`。

## [0.2.0] - 2026-08-23

### Added

- **页面智能路由**（页面智能路由方案）：页面工具注册时全量可见、实例绑定惰性化——
  页面未打开时工具始终在 `tools/list` 中，Agent 调用时：实例存活直接调（UI 线程、
  异步让出不阻塞）；无实例有工厂则单飞去创建（导航 + 等待就绪 + 15s 激活超时）；
  无实例无工厂返回明确错误（`PAGE_NOT_FOUND`）。页面创建/导航并发单飞、弱引用
  GC 后可重建、跨页面工具名全局唯一。
  - **C#（Phase 1）**：`RegisterTools<T>(pageKey)` / `RegisterTools(Func<object>, pageKey)`
    惰性注册、`SetPageActivator`（导航 + 异步就绪等待）、`UnregisterPage`、
    `SetUIThreadDispatcher`（核心包只定义接口）、`PageActivationTimeoutSeconds`；
    错误码 `PAGE_NOT_FOUND` / `PAGE_ACTIVATION_FAILED` / `PAGE_ACTIVATION_TIMEOUT`。
  - **WPF 扩展包 `AgentQuay.Sdk.Wpf`（Phase 2）**：`WpfDispatcher`（BeginInvoke + TCS，
    UI 线程执行且 async 让出，动画不冻结）+ `PageLoadedAsync` 事件驱动就绪等待。
  - **Python / TypeScript / Java SDK（Phase 6）** 同构跟进：类型级惰性注册
    （`register_tools(cls, page_key)` / `registerTools(cls, { pageKey })` /
    `registerTools(Class, pageKey)`）、工厂注册、`set_page_activator` /
    `setPageActivator` 激活钩子、单飞路由、`unregister_page` / `unregisterPage`、
    显式注销后工厂路径自动重建。
  - **Bridge（Phase 3）**：`ToolMetadata` 新增可选字段 `pageKey`（协议版本不升、
    旧 SDK 完全兼容，注册消息零破坏）；`tools/list` 描述前缀 `[AppName|PageKey]`
    （离线变体 `[未运行] [AppName|PageKey]`）；危险工具确认消息注明"将在应用中打开
    页面 {pageKey}"；跨页面工具名重复复用 `INVALID_TOOL` 拒绝注册。
- 页面智能路由单测：C# `PageRouterTests`（9 项）、Python `test_page_router`（12 项）、
  TypeScript `page_router.test.ts`（9 项）、Java `PageRouterTest`（10 项），覆盖单飞
  并发、GC/注销重建、激活超时、无工厂报错、激活钩子只执行一次；Bridge 侧新增
  pageKey 注册/跨页重名拒绝/描述前缀端到端测试。

### Changed

- 全项目版本号升至 **0.2.0**；四平台 Bridge 二进制（darwin-amd64 / darwin-arm64 /
  linux-amd64 / windows-amd64）全部以当前源码重建并同步到各 SDK 的 `bridge_bin/`。

## [0.1.1] - 2026-08-21

### Fixed

- **确认流程**：确认阶段应用断连/被替换时立即返回 `-32001`/`-32007`，不再等满确认超时
  （此前 Agent 会白等 120s）——Bridge `mcpbridge.go`。
- **跨语言 spawn 锁单位统一**：`spawn.lock` 的 `startedAt` 统一为 epoch 毫秒（Python 原写
  epoch 秒、C# 原写系统启动起算的 `TickCount64`，与 TS/Java/C++/Rust 不互认，混合语言
  并发首启会双拉起 Bridge）。
- **启动弹终端窗口**：Python / Rust / C# 拉起内嵌 Bridge 时在 Windows 上设置
  `CREATE_NO_WINDOW`（此前桌面 GUI 应用启动会闪现控制台窗口）。
- **TypeScript**：`close()` 在重连退避期间调用不再导致 `connect()` 永久挂起（此前
  `clearTimeout` 清掉退避定时器后 resolve 永不触发）。
- **C++**：控制器改为共享所有权（QSharedPointer + 跨线程安全释放 + 工作线程 QPointer
  守卫），修复 `stop()`/析构时在途 Tool（>3s）导致控制器 use-after-free；同时修复
  `QVariant(QMetaType, void*)` 拷贝语义下枚举/回退分支的临时对象泄漏。
- **Java**：工具调用钩子在工具存在性检查之后触发（与其它语言一致）；为新增的
  `toolCallHandler` 补齐 XML 文档。
- **C#**：修复 `toolCallHandler` 调用处参数类型不匹配导致的编译错误。

### Added

- 各语言 SDK 新增**工具调用钩子** `on_tool_call` / `onToolCall` / `toolCallHandler` /
  `setToolCallHandler`：在业务方法执行前触发，供 UI 层拦截并响应（见 sdk-tutorial §6.7）。

### Changed

- 全项目版本号升至 **0.1.1**；四平台 Bridge 二进制（darwin-amd64 / darwin-arm64 /
  linux-amd64 / windows-amd64）全部以当前源码重建并同步到各 SDK 的 `bridge_bin/`。

## [0.1.0] - 2026-08-18

应用启动注册表、OS 级发现与内嵌 Bridge 生命周期完整方案落地。

### Added

- **应用启动注册表与 OS 级发现**：`agentquay apps list/add/remove/search/launch/
  set-auto-launch` 与内置工具 `app_list` / `app_launch` / `app_search` / `app_adopt`；
  离线应用调用时自动拉起再执行。
- **内嵌 Bridge 生命周期三步方案**：空闲自回收（`embeddedIdleTimeoutSeconds`）、
  实例分级让位与权威端口归位（`service` > `embedded`，`disconnect(reason=migrate)`）、
  spawn 原子锁（`~/.agentquay/spawn.lock`）消除并发首启竞态。
- **六种语言 SDK**：Python / TypeScript / Java / .NET / C++ / Rust，均通过单测与
  e2e（真实 Bridge 联调）。
- **双模式部署**：SDK 内嵌（默认）与系统服务（LaunchAgent / Windows Service /
  systemd user service）。

### Changed

- 项目统一为 **Apache-2.0** 许可（原计划 SDK=MIT + Bridge=AGPL-3.0 的双许可）。
- `go.mod` 的 `go` 指令固定到主次版本（`go 1.25`）。

### Security

- token 钉扎认证（`aq_`+64hex，存系统凭证库，可 `rotate-token` 轮换）。
- 危险操作确认流程（`RequiresConfirmation`，OS 原生确认框，确认/执行超时分离）。
- 离线拉起只接受已登记/系统发现候选，杜绝任意路径注入。
