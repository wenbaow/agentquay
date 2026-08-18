//! token 持久化（设计文档 §6.2）：优先系统钥匙串（keyring，可选特性），
//! 不可用时回退 `~/.agentquay/tokens.json`（与 Java / Python SDK 同格式，可互相读取）。

use std::path::PathBuf;

use log::{debug, warn};

use crate::home::home_dir;

/// keyring 服务名（Java SDK 一致）。
#[cfg(feature = "keyring")]
const KEYRING_SERVICE: &str = "agentquay";

/// token 文件回退路径。
pub fn tokens_file() -> PathBuf {
    home_dir().unwrap_or_else(|| PathBuf::from(".")).join(".agentquay").join("tokens.json")
}

/// 按 appId 读写钉扎 token。
pub struct TokenStore {
    app_id: String,
}

impl TokenStore {
    pub fn new(app_id: impl Into<String>) -> Self {
        Self { app_id: app_id.into() }
    }

    /// 读取已持久化的 token（无则返回 None）。
    pub fn get(&self) -> Option<String> {
        #[cfg(feature = "keyring")]
        if let Some(token) = keyring_get(&self.app_id) {
            return Some(token);
        }
        self.read_file()
    }

    /// 持久化 token（后续重连自动携带）。
    pub fn set(&self, token: &str) {
        #[cfg(feature = "keyring")]
        if keyring_set(&self.app_id, token) {
            return;
        }
        self.write_file(token);
    }

    // ------------------------------------------------------------------
    // 文件回退：~/.agentquay/tokens.json（{appId: token}）
    // ------------------------------------------------------------------

    fn read_file(&self) -> Option<String> {
        let path = tokens_file();
        let data = match std::fs::read_to_string(&path) {
            Ok(d) => d,
            Err(_) => return None,
        };
        let map: serde_json::Map<String, serde_json::Value> = match serde_json::from_str(&data) {
            Ok(m) => m,
            Err(e) => {
                debug!("token 文件解析失败（忽略）: {e}");
                return None;
            }
        };
        map.get(&self.app_id).and_then(|v| v.as_str()).map(str::to_owned)
    }

    fn write_file(&self, token: &str) {
        let path = tokens_file();
        let mut map = serde_json::Map::new();
        if let Ok(data) = std::fs::read_to_string(&path) {
            if let Ok(existing) = serde_json::from_str::<serde_json::Map<String, serde_json::Value>>(&data) {
                map = existing;
            }
        }
        map.insert(self.app_id.clone(), serde_json::Value::String(token.to_owned()));
        let body = serde_json::to_string_pretty(&map).unwrap_or_else(|_| format!("{{}}"));
        if let Some(parent) = path.parent() {
            if std::fs::create_dir_all(parent).is_err() {
                warn!("创建 {} 失败", parent.display());
                return;
            }
        }
        if std::fs::write(&path, body).is_err() {
            warn!("写入 token 文件失败: {}", path.display());
            return;
        }
        // 仅本用户可读写（Windows 无 POSIX 权限，忽略）
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600));
        }
    }
}

// ---------------------------------------------------------------------------
// keyring（可选特性，失败自动回退文件）
// ---------------------------------------------------------------------------

#[cfg(feature = "keyring")]
fn keyring_get(app_id: &str) -> Option<String> {
    let entry = keyring::Entry::new(KEYRING_SERVICE, app_id).ok()?;
    match entry.get_password() {
        Ok(token) if !token.is_empty() => Some(token),
        Ok(_) => None,
        Err(e) => {
            debug!("keyring 读取失败（回退文件存储）: {e}");
            None
        }
    }
}

#[cfg(feature = "keyring")]
fn keyring_set(app_id: &str, token: &str) -> bool {
    match keyring::Entry::new(KEYRING_SERVICE, app_id) {
        Ok(entry) => match entry.set_password(token) {
            Ok(()) => true,
            Err(e) => {
                debug!("keyring 写入失败（回退文件存储）: {e}");
                false
            }
        },
        Err(e) => {
            debug!("keyring 初始化失败（回退文件存储）: {e}");
            false
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn file_store_roundtrip() {
        let _guard = crate::TEST_ENV_LOCK.lock().unwrap();
        // 隔离 HOME（dirs::home_dir 读取环境变量），不触碰真实用户目录
        let sandbox = std::env::temp_dir().join(format!(
            "agentquay-token-test-{}",
            std::process::id()
        ));
        std::env::set_var("HOME", &sandbox);
        #[cfg(windows)]
        std::env::set_var("USERPROFILE", &sandbox);
        let store = TokenStore::new("test-app");
        assert!(store.get().is_none());
        store.set("aq_abcdef1234");
        assert_eq!(store.get().as_deref(), Some("aq_abcdef1234"));

        // 另一个实例可读取（模拟重连）
        let store2 = TokenStore::new("test-app");
        assert_eq!(store2.get().as_deref(), Some("aq_abcdef1234"));

        // 多 appId 共存
        let store3 = TokenStore::new("other-app");
        store3.set("aq_xyz");
        assert_eq!(store2.get().as_deref(), Some("aq_abcdef1234"));
        assert_eq!(store3.get().as_deref(), Some("aq_xyz"));

        let _ = std::fs::remove_dir_all(&sandbox);
    }
}
