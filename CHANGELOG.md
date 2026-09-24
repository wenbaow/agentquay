# Changelog

本项目采用语义化版本（SemVer）。各 SDK 与 Bridge 的包版本以对应清单文件为准。

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
