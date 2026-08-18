//! 用户主目录解析：优先环境变量（Windows: `USERPROFILE` / 其他: `HOME`），
//! 与 Bridge（Go `os.UserHomeDir`）行为一致，便于测试隔离与端口/令牌路径统一。

use std::path::PathBuf;

/// 返回用户主目录。
///
/// 优先环境变量（与 Go `os.UserHomeDir` 一致），失败时回退 `dirs::home_dir()`
/// （注意：dirs 在 Windows 上走 Win32 API，不受环境变量影响，因此必须先用 env）。
pub fn home_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    let from_env = std::env::var_os("USERPROFILE");
    #[cfg(not(windows))]
    let from_env = std::env::var_os("HOME");
    if let Some(h) = from_env {
        if !h.is_empty() {
            return Some(PathBuf::from(h));
        }
    }
    dirs::home_dir()
}

/// `~/.agentquay` 配置目录（Bridge 同款路径）。
#[allow(dead_code)] // 预留：config 目录访问
pub fn agentquay_dir() -> PathBuf {
    home_dir().unwrap_or_else(|| PathBuf::from(".")).join(".agentquay")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn home_dir_prefers_env() {
        let _guard = crate::TEST_ENV_LOCK.lock().unwrap();
        let sandbox = std::env::temp_dir().join(format!("agentquay-home-test-{}", std::process::id()));
        std::env::set_var("HOME", &sandbox);
        #[cfg(windows)]
        std::env::set_var("USERPROFILE", &sandbox);
        assert_eq!(home_dir().as_deref(), Some(sandbox.as_path()));
    }
}
