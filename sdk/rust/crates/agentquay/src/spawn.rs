//! auto-spawn：检测本地 Bridge，未运行时自动拉起内嵌二进制（设计文档 §4.1）。
//!
//! 流程：读 `~/.agentquay/port` → TCP 探测 → 未运行且 `auto_spawn_bridge=true` →
//! 查找内嵌二进制（由 build.rs 按 target 条件准备，路径经编译期常量注入）或 PATH
//! 中的 `agentquay` → 以 `serve --embedded` 拉起 → 等待端口就绪。

use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::Duration;

use log::{info, warn};

use crate::error::AgentQuayError;
use crate::home::home_dir;

/// 端口文件（Bridge 写入实际监听端口，SDK 读取）。
pub fn port_file() -> PathBuf {
    home_dir().unwrap_or_else(|| PathBuf::from(".")).join(".agentquay").join("port")
}

/// 读取 `~/.agentquay/port`（Bridge 写入的实际端口）。
pub fn read_port_file() -> Option<u16> {
    let data = std::fs::read_to_string(port_file()).ok()?;
    let port: u16 = data.trim().parse().ok()?;
    if port == 0 {
        return None;
    }
    Some(port)
}

/// TCP 探测端口是否可连接。
pub async fn probe(host: &str, port: u16) -> bool {
    tokio::time::timeout(Duration::from_millis(300), tokio::net::TcpStream::connect((host, port)))
        .await
        .is_ok_and(|r| r.is_ok())
}

/// 查找可用的 Bridge 二进制：内嵌（crate 资源，解压到缓存目录）优先，其次 PATH。
pub fn find_bridge_binary() -> Option<PathBuf> {
    if let Some(bundled) = embedded_binary() {
        info!("使用内嵌 Bridge 二进制: {}", bundled.display());
        return Some(bundled);
    }
    let exe = if cfg!(windows) { "agentquay.exe" } else { "agentquay" };
    std::env::var_os("PATH").and_then(|paths| {
        std::env::split_paths(&paths)
            .map(|dir| dir.join(exe))
            .find(|p| p.is_file())
    })
}

/// 以 `serve --embedded` 模式拉起 Bridge，日志写入应用缓存目录
/// （`~/.agentquay/logs/agentquay.log`，通过 `AGENTQUAY_LOG_DIR` 指定）。
pub fn spawn_embedded(binary: &Path) -> std::io::Result<Child> {
    let home = home_dir().unwrap_or_else(|| PathBuf::from("."));
    let log_dir = home.join(".agentquay").join("logs");
    std::fs::create_dir_all(&log_dir)?;
    let log_file = log_dir.join("agentquay.log");
    let stdout = std::fs::OpenOptions::new().create(true).append(true).open(&log_file)?;
    let stderr = stdout.try_clone()?;

    let mut cmd = Command::new(binary);
    cmd.arg("serve")
        .arg("--embedded")
        .env("AGENTQUAY_LOG_DIR", &log_dir)
        .stdout(Stdio::from(stdout))
        .stderr(Stdio::from(stderr));
    // Windows 上脱离控制台并隐藏新建的控制台窗口（桌面 GUI 应用拉起嵌入式桥时
    // 不得闪现终端窗口）；CREATE_NEW_PROCESS_GROUP 与 CREATE_NO_WINDOW 可叠加
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW);
    }
    cmd.spawn()
}

// ---------------------------------------------------------------------------
// spawn 原子锁（步骤 3）：多应用同时首启时保证只有一个去拉起 Bridge。
// 锁文件 ~/.agentquay/spawn.lock，内容 {pid, startedAt}，跨语言互认。

const SPAWN_LOCK_TTL_SECS: u64 = 15;
const SPAWN_WAIT_SECS: u64 = 12;

fn spawn_lock_path() -> PathBuf {
    home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".agentquay")
        .join("spawn.lock")
}

fn now_millis() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

/// 独占创建锁文件（create_new == O_EXCL）；已有锁返回 false。
fn write_lock_file() -> bool {
    let path = spawn_lock_path();
    if let Some(parent) = path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let content = format!("{{\"pid\":{},\"startedAt\":{}}}", std::process::id(), now_millis());
    match std::fs::OpenOptions::new().write(true).create_new(true).open(&path) {
        Ok(mut f) => {
            use std::io::Write;
            let _ = f.write_all(content.as_bytes());
            true
        }
        Err(_) => false,
    }
}

fn lock_expired() -> bool {
    let content = match std::fs::read_to_string(spawn_lock_path()) {
        Ok(c) => c,
        Err(_) => return true, // 缺失视为已失效
    };
    let started: u64 = content
        .split("\"startedAt\":")
        .nth(1)
        .and_then(|s| s.split('}').next())
        .and_then(|s| s.parse().ok())
        .unwrap_or(0);
    now_millis().saturating_sub(started) > SPAWN_LOCK_TTL_SECS * 1000
}

fn acquire_spawn_lock() -> bool {
    if write_lock_file() {
        return true;
    }
    if !lock_expired() {
        return false; // 未过期：另有实例正在拉起
    }
    let _ = std::fs::remove_file(spawn_lock_path()); // 过期（崩溃残留）：打破后重试一次
    write_lock_file()
}

fn release_spawn_lock() {
    let _ = std::fs::remove_file(spawn_lock_path());
}

/// 未抢到锁时等待别的应用拉起的 Bridge 端口文件就绪，直接复用。
async fn wait_for_any_bridge(
    host: &str,
    known_port: u16,
) -> Result<(u16, Option<Child>), AgentQuayError> {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(SPAWN_WAIT_SECS);
    loop {
        if known_port > 0 && probe(host, known_port).await {
            return Ok((known_port, None));
        }
        if let Some(port) = read_port_file() {
            if port > 0 && probe(host, port).await {
                return Ok((port, None));
            }
        }
        if tokio::time::Instant::now() >= deadline {
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    Err(AgentQuayError::BridgeSpawn(
        "其他进程正在拉起 Bridge，但等待超时未就绪。可检查 ~/.agentquay/spawn.lock 是否残留，或手动运行 `agentquay start --daemon`".into(),
    ))
}

/// 确保本地 Bridge 可用，返回 `(实际端口, 本进程拉起的子进程或 None)`。
///
/// 流程与 Java / Python SDK 一致：端口文件 → 探测 → auto-spawn → 等待就绪（12s）。
pub async fn ensure_bridge(
    host: &str,
    port: u16,
    auto_spawn: bool,
) -> Result<(u16, Option<Child>), AgentQuayError> {
    let mut actual = port;
    if actual == 0 {
        actual = read_port_file().unwrap_or(0);
    }

    // 1. 已有端口 → 直接探测
    if actual > 0 && probe(host, actual).await {
        return Ok((actual, None));
    }

    // 2. 自动拉起（带 spawn 原子锁：同一时刻只有一个进程真正去拉起，其余等待复用）
    if auto_spawn {
        match find_bridge_binary() {
            Some(binary) => {
                if acquire_spawn_lock() {
                    // 拿到锁后复查：竞态窗口内可能有别的实例已拉起
                    if actual > 0 && probe(host, actual).await {
                        release_spawn_lock();
                        return Ok((actual, None));
                    }
                    if let Some(p) = read_port_file() {
                        if p > 0 && probe(host, p).await {
                            release_spawn_lock();
                            return Ok((p, None));
                        }
                    }
                    let outcome = async {
                        info!("本地无 Bridge 服务，拉起内嵌 Bridge: {}", binary.display());
                        let child = spawn_embedded(&binary).map_err(|e| {
                            AgentQuayError::BridgeSpawn(format!("拉起内嵌 Bridge 失败: {e}"))
                        })?;
                        // 等待端口文件出现（Bridge 启动后写入实际端口）
                        let deadline = tokio::time::Instant::now() + Duration::from_secs(12);
                        loop {
                            if actual > 0 && probe(host, actual).await {
                                return Ok::<_, AgentQuayError>((actual, Some(child)));
                            }
                            if let Some(port) = read_port_file() {
                                if port > 0 && probe(host, port).await {
                                    return Ok((port, Some(child)));
                                }
                            }
                            if tokio::time::Instant::now() >= deadline {
                                break;
                            }
                            tokio::time::sleep(Duration::from_millis(200)).await;
                        }
                        warn!("内嵌 Bridge 启动超时，终止子进程");
                        kill_child(child);
                        Err(AgentQuayError::BridgeSpawn(
                            "内嵌 Bridge 启动超时（12s）。可尝试: 1) 手动运行 `agentquay start --daemon` \
                             安装系统服务; 2) 检查端口占用与 ~/.agentquay/logs/agentquay.log"
                                .into(),
                        ))
                    }
                    .await;
                    release_spawn_lock();
                    return outcome;
                }
                // 未抢到锁：别的应用正在拉起，等待其端口文件就绪后直接复用
                return wait_for_any_bridge(host, actual).await;
            }
            None => Err(AgentQuayError::BridgeUnavailable(
                "本地未检测到 AgentQuay Bridge，且未找到 agentquay 二进制。\
                 两种出路: 1) 安装系统服务（agentquay start --daemon）; \
                 2) 将 agentquay 放入 PATH 或启用 SDK 的 embedded-bridge 特性"
                    .into(),
            )),
        }
    } else {
        Err(AgentQuayError::BridgeUnavailable(format!(
            "本地端口 {} 无 AgentQuay Bridge 服务，且 autoSpawnBridge=false。\
             请先运行 `agentquay start --daemon` 或开启 autoSpawnBridge",
            if actual > 0 { actual.to_string() } else { "(未找到端口文件)".into() }
        )))
    }
}

/// 终止子进程（std 的 kill 在 Windows 上即强制终止；SDK 退出清理用）。
pub fn kill_child(mut child: Child) {
    let _ = child.kill();
    let _ = child.wait();
}

// ---------------------------------------------------------------------------
// 内嵌二进制（构建期由 build.rs 准备：仓库内 bridge_bin/ 或 GitHub Releases
// 按版本下载；路径经 AGENTQUAY_EMBEDDED_BRIDGE 编译期常量注入）
// ---------------------------------------------------------------------------

fn cache_dir() -> PathBuf {
    dirs::cache_dir()
        .map(|d| d.join("agentquay"))
        .unwrap_or_else(|| home_dir().unwrap_or_else(|| PathBuf::from(".")).join(".agentquay").join("cache"))
}

fn embedded_binary() -> Option<PathBuf> {
    let source = PathBuf::from(option_env!("AGENTQUAY_EMBEDDED_BRIDGE")?);
    if !source.is_file() {
        return None;
    }
    let bytes_len = std::fs::metadata(&source).ok()?.len();
    let name = source.file_name()?.to_str()?;
    let dir = cache_dir().join("bridge_bin");
    std::fs::create_dir_all(&dir).ok()?;
    let path = dir.join(name);
    // 内容不一致时重新拷贝（版本升级覆盖旧文件，并避开只读源码树）
    let need_copy = !path.is_file()
        || std::fs::metadata(&path).map(|m| m.len() != bytes_len).unwrap_or(true);
    if need_copy {
        if std::fs::copy(&source, &path).is_err() {
            return None;
        }
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755));
    }
    Some(path)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn port_file_read_invalid_returns_none() {
        let _guard = crate::TEST_ENV_LOCK.lock().unwrap();
        // 隔离 HOME
        let sandbox = std::env::temp_dir().join(format!("agentquay-port-test-{}", std::process::id()));
        std::env::set_var("HOME", &sandbox);
        #[cfg(windows)]
        std::env::set_var("USERPROFILE", &sandbox);
        assert!(read_port_file().is_none());
        std::fs::create_dir_all(port_file().parent().unwrap()).unwrap();
        std::fs::write(port_file(), "19846").unwrap();
        assert_eq!(read_port_file(), Some(19846));
        std::fs::write(port_file(), "abc").unwrap();
        assert!(read_port_file().is_none());
        let _ = std::fs::remove_dir_all(&sandbox);
    }

    #[test]
    fn embedded_binary_exists_on_current_target() {
        let _guard = crate::TEST_ENV_LOCK.lock().unwrap();
        // 隔离 HOME（cache_dir 回退时避免写入真实用户目录）
        let sandbox = std::env::temp_dir().join(format!(
            "agentquay-embed-test-{}",
            std::process::id()
        ));
        std::env::set_var("HOME", &sandbox);
        #[cfg(windows)]
        std::env::set_var("USERPROFILE", &sandbox);
        // 当前平台（windows-amd64）应有内嵌二进制且可解压
        let bin = embedded_binary();
        assert!(bin.is_some(), "当前平台缺少内嵌 Bridge 二进制（bridge_bin/）");
        assert!(bin.unwrap().is_file());
        let _ = std::fs::remove_dir_all(&sandbox);
    }
}
