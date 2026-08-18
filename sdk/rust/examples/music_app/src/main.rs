//! MusicApp 示例（设计文档 §4.8）：被 AI Agent 控制的音乐应用。
//!
//! 运行前需要 Bridge：本地没有时会自动拉起内嵌二进制（auto_spawn_bridge 默认开启）。
//! 之后任何支持 MCP Streamable HTTP 的 Agent 配置 `http://127.0.0.1:19846/mcp`
//! 即可看到 `music-app_search` / `music-app_play` / `music-app_delete` 等工具。
//!
//! ```bash
//! cargo run -p music_app
//! ```

use agentquay::{agent_tool, AgentQuayClient, ToolError};
use serde::{Deserialize, Serialize};

/// 歌曲（返回值需要 Serialize；参数类型需要 Deserialize + JsonSchema）。
#[derive(Debug, Clone, Serialize, Deserialize, agentquay::JsonSchema)]
struct Song {
    id: String,
    title: String,
    artist: String,
}

struct MusicController {
    library: Vec<Song>,
}

impl MusicController {
    fn new() -> Self {
        Self {
            library: vec![
                Song { id: "1".into(), title: "七里香".into(), artist: "周杰伦".into() },
                Song { id: "2".into(), title: "晴天".into(), artist: "周杰伦".into() },
                Song { id: "3".into(), title: "光年之外".into(), artist: "邓紫棋".into() },
            ],
        }
    }
}

#[agent_tool]
impl MusicController {
    /// 搜索音乐库中的歌曲（description 缺省回退到 doc 注释首行）
    #[agent_tool(name = "search")]
    fn search(&self, keyword: String) -> Vec<Song> {
        self.library
            .iter()
            .filter(|s| s.title.contains(&keyword) || s.artist.contains(&keyword))
            .cloned()
            .collect()
    }

    /// 播放指定歌曲
    #[agent_tool(name = "play", description = "播放指定歌曲")]
    fn play(&self, song_id: String) -> Result<String, String> {
        if self.library.iter().any(|s| s.id == song_id) {
            Ok(format!("正在播放歌曲 {song_id}"))
        } else {
            Err(format!("歌曲不存在: {song_id}"))
        }
    }

    /// 异步 Tool 示例
    #[agent_tool(name = "stats", description = "统计音乐库")]
    async fn stats(&self) -> Result<agentquay::serde_json::Value, ToolError> {
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        Ok(agentquay::serde_json::json!({
            "total": self.library.len(),
            "artists": 2,
        }))
    }

    /// 删除歌曲（危险操作：Agent 调用时 Bridge 会先向应用弹确认）
    #[agent_tool(name = "delete", description = "删除歌曲", requires_confirmation = true)]
    fn delete(&self, song_id: String) -> Result<(), ToolError> {
        if self.library.iter().any(|s| s.id == song_id) {
            Ok(())
        } else {
            Err(ToolError::business("SONG_NOT_FOUND", format!("歌曲不存在: {song_id}"), None))
        }
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info")).init();

    let client = AgentQuayClient::builder()
        .app_id("music-app")
        .app_name("Music Player")
        .build()?;
    client.register_tools(MusicController::new())?;
    println!("已登记 tools: {:?}", client.list_tools());
    println!("连接 Bridge（Ctrl+C 退出）...");

    // 阻塞保持连接，监听 Agent 调用（被同 appId 新实例替换时返回 Err(Replaced)）
    client.connect().await?;
    Ok(())
}
