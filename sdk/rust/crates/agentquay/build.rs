//! 按当前目标平台准备内嵌 Bridge 二进制（`embedded-bridge` 特性，默认开启）。
//!
//! 优先使用仓库内 git 提交的 `bridge_bin/`（本地开发离线可用）；crates.io 发布的
//! 包不含该目录（`[package] exclude`，4 × ~10MB 超出 crates.io 每包 10MiB 上限），
//! 回退到从 GitHub Releases 按 `v{CARGO_PKG_VERSION}` 下载对应平台二进制并缓存到
//! `OUT_DIR`。成功后经 `cargo:rustc-env=AGENTQUAY_EMBEDDED_BRIDGE` 把路径注入编译
//! 期常量，spawn.rs 运行时解压到缓存目录（与旧版 `include_bytes!` 语义一致）。
//!
//! 下载失败视为编译错误（与旧版缺少 `bridge_bin/` 文件时 `include_bytes!` 的报错
//! 行为一致）；不支持的平台或关闭特性时不报错，auto-spawn 回退到 PATH 查找。

use std::env;
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::Duration;

const RELEASE_BASE: &str = "https://github.com/wenbaow/agentquay/releases/download";

/// 当前平台对应的桥二进制文件名；不支持的平台返回 None（回退 PATH）。
fn bridge_file_name(target_os: &str, target_arch: &str) -> Option<String> {
    let name = match (target_os, target_arch) {
        ("windows", "x86_64") => "agentquay-windows-amd64.exe",
        ("linux", "x86_64") => "agentquay-linux-amd64",
        ("macos", "aarch64") => "agentquay-darwin-arm64",
        ("macos", "x86_64") => "agentquay-darwin-amd64",
        _ => return None,
    };
    Some(name.to_string())
}

fn main() {
    // 特性关闭时不准备内嵌二进制（用户自带 Bridge / 走 PATH）
    if env::var_os("CARGO_FEATURE_EMBEDDED_BRIDGE").is_none() {
        return;
    }
    let target_os = env::var("CARGO_CFG_TARGET_OS").unwrap();
    let target_arch = env::var("CARGO_CFG_TARGET_ARCH").unwrap();
    let Some(file_name) = bridge_file_name(&target_os, &target_arch) else {
        println!("cargo:warning=当前平台无内嵌 Bridge 二进制，auto-spawn 将回退到 PATH 查找");
        return;
    };

    let manifest_dir = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap());

    // 1) 仓库内提交的 bridge_bin/（本地开发离线可用）
    let local = manifest_dir.join("bridge_bin").join(&file_name);
    if local.is_file() {
        emit_path(&local);
        return;
    }

    // 2) crates.io 发布的包：按版本从 GitHub Releases 下载到 OUT_DIR（已有缓存则跳过）
    let out_dir = PathBuf::from(env::var("OUT_DIR").unwrap());
    let dest = out_dir.join("bridge_bin").join(&file_name);
    if !download_bridge(&dest, &file_name) {
        panic!(
            "无法获取内嵌 Bridge 二进制 {file_name}（GitHub Releases v{} 下载失败）。\
             请检查网络，或改用 default-features = false 并自行提供 Bridge（PATH / AGENTQUAY_BRIDGE_BIN）",
            env::var("CARGO_PKG_VERSION").unwrap()
        );
    }
    emit_path(&dest);
}

fn emit_path(path: &Path) {
    println!("cargo:rustc-env=AGENTQUAY_EMBEDDED_BRIDGE={}", path.display());
}

fn download_bridge(dest: &Path, file_name: &str) -> bool {
    // 已有非空缓存（同一 OUT_DIR 重复构建）直接复用
    if dest.is_file() && dest.metadata().map(|m| m.len() > 0).unwrap_or(false) {
        return true;
    }
    let version = env::var("CARGO_PKG_VERSION").unwrap();
    let url = format!("{RELEASE_BASE}/v{version}/{file_name}");
    println!("cargo:warning=下载 AgentQuay Bridge v{version}（{file_name}）: {url}");
    let response = match ureq::get(&url).timeout(Duration::from_secs(120)).call() {
        Ok(r) => r,
        Err(e) => {
            println!("cargo:warning=下载失败: {e}");
            return false;
        }
    };
    let mut body: Vec<u8> = Vec::new();
    if response.into_reader().read_to_end(&mut body).is_err() || body.is_empty() {
        println!("cargo:warning=下载内容为空: {url}");
        return false;
    }
    if let Some(parent) = dest.parent() {
        let _ = fs::create_dir_all(parent);
    }
    if fs::write(dest, &body).is_err() {
        return false;
    }
    // OUT_DIR 内缓存由本脚本管理，直接置为可执行
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(dest, fs::Permissions::from_mode(0o755));
    }
    true
}