/**
 * AgentQuay TypeScript SDK 示例：音乐应用。
 *
 * 运行前确保 Bridge 可用（SDK 会自动拉起内嵌 Bridge，或先运行 `agentquay start --daemon`）:
 *
 *     npm run example
 *
 * 然后用 MCP 客户端（opencode / codex / zcode / Claude Desktop 或任何 MCP 客户端）
 * 连接 http://127.0.0.1:19846/mcp，即可发现并调用:
 *     music-app_search / music-app_play / music-app_delete（需用户确认）
 */
import { AgentQuayClient, AgentTool } from "../src/index";

interface Song {
  id: string;
  title: string;
  artist: string;
  duration: number;
}

const SONGS: Song[] = [
  { id: "1", title: "七里香", artist: "周杰伦", duration: 243 },
  { id: "2", title: "晴天", artist: "周杰伦", duration: 269 },
  { id: "3", title: "海阔天空", artist: "Beyond", duration: 326 },
  { id: "4", title: "平凡之路", artist: "朴树", duration: 302 },
];

let currentlyPlaying: Song | null = null;

class MusicController {
  @AgentTool("search", { description: "搜索音乐库中的歌曲" })
  search(keyword: string, limit = 10): Song[] {
    const kw = keyword.toLowerCase();
    const results = SONGS.filter(
      (s) => s.title.toLowerCase().includes(kw) || s.artist.toLowerCase().includes(kw),
    );
    return results.slice(0, limit);
  }

  @AgentTool("play", { description: "播放指定歌曲" })
  play(songId: string): { status: string; song: Song } {
    const song = SONGS.find((s) => s.id === songId);
    if (!song) {
      throw new Error(`歌曲不存在: ${songId}`);
    }
    currentlyPlaying = song;
    return { status: "playing", song };
  }

  @AgentTool("now_playing", { description: "查看当前播放的歌曲" })
  nowPlaying(): { song: Song | null } {
    return { song: currentlyPlaying };
  }

  @AgentTool("stop", { description: "停止播放" })
  stop(): { status: string } {
    currentlyPlaying = null;
    return { status: "stopped" };
  }

  @AgentTool("delete", {
    description: "从音乐库删除歌曲（危险操作，需要用户确认）",
    requiresConfirmation: true,
  })
  delete(songId: string): { status: string; song: Song } {
    const idx = SONGS.findIndex((s) => s.id === songId);
    if (idx < 0) {
      throw new Error(`歌曲不存在: ${songId}`);
    }
    const [song] = SONGS.splice(idx, 1);
    return { status: "deleted", song };
  }

  @AgentTool("simulate_slow", { description: "模拟慢操作（测试执行超时 -32004）" })
  async simulateSlow(seconds = 35): Promise<{ elapsed: number }> {
    // 慢操作：超过默认 30s 执行超时（async 不阻塞事件循环）
    await new Promise((resolve) => setTimeout(resolve, seconds * 1000));
    return { elapsed: seconds };
  }

  @AgentTool("simulate_failure", { description: "模拟业务失败（错误透传）" })
  simulateFailure(): never {
    throw new Error("音乐库服务暂时不可用");
  }
}

async function main(): Promise<void> {
  const client = new AgentQuayClient({
    appId: "music-app",
    appName: "Music Player",
    host: "localhost",
    port: 0, // 从 ~/.agentquay/port 自动读取
    autoSpawnBridge: true, // 未检测到服务时自动拉起内嵌 Bridge
  });
  client.registerTools(MusicController);
  console.log(`已注册 tools: ${client.listTools().join(", ")}`);
  console.log("等待 Agent 调用…（Ctrl+C 退出）");
  await client.connect();
}

main().catch((e) => {
  console.error("示例应用退出:", e);
  process.exitCode = 1;
});
