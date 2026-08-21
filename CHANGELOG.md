# Changelog

本项目采用语义化版本（SemVer）。各 SDK 与 Bridge 的包版本以对应清单文件为准。

## [Unreleased]

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
