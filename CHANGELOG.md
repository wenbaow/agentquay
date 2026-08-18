# Changelog

本项目采用语义化版本（SemVer）。各 SDK 与 Bridge 的包版本以对应清单文件为准。

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
