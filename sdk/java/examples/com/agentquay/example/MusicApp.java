package com.agentquay.example;

import com.agentquay.AgentParam;
import com.agentquay.AgentQuayClient;
import com.agentquay.AgentTool;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;

/**
 * AgentQuay Java SDK 示例：音乐应用（设计文档 §4.4）。
 *
 * <p>运行前确保 Bridge 可用（SDK 会自动拉起内嵌 Bridge，或先运行
 * {@code agentquay start --daemon}）：
 *
 * <pre>
 * mvn -q compile exec:java -Dexec.mainClass=com.agentquay.example.MusicApp
 * </pre>
 *
 * 然后用 MCP 客户端连接 http://127.0.0.1:19846/mcp，
 * 即可发现并调用 music-app_search / music-app_play / music-app_delete（需确认）。
 */
public final class MusicApp {

    /** 业务模型。 */
    public static final class Song {
        public String id;
        public String title;
        public String artist;
        public int duration;
    }

    /** 业务控制器：加 @AgentTool 注解暴露方法给 Agent。 */
    public static class MusicController {

        private final List<Song> songs = new ArrayList<>();

        public MusicController() {
            songs.add(song("1", "七里香", "周杰伦", 243));
            songs.add(song("2", "晴天", "周杰伦", 269));
            songs.add(song("3", "海阔天空", "Beyond", 326));
            songs.add(song("4", "平凡之路", "朴树", 302));
        }

        @AgentTool(value = "search", description = "搜索音乐库中的歌曲")
        public List<Song> search(@AgentParam(description = "搜索关键词") String keyword) {
            List<Song> results = new ArrayList<>();
            for (Song s : songs) {
                if (s.title.contains(keyword) || s.artist.contains(keyword)) {
                    results.add(s);
                }
            }
            return results;
        }

        @AgentTool(value = "play", description = "播放指定歌曲")
        public Map<String, Object> play(@AgentParam(description = "歌曲ID") String songId) {
            for (Song s : songs) {
                if (s.id.equals(songId)) {
                    return Map.of("status", "playing", "song", s);
                }
            }
            throw new IllegalArgumentException("歌曲不存在: " + songId);
        }

        @AgentTool(value = "delete", description = "删除歌曲（危险操作，需要用户确认）",
                requiresConfirmation = true)
        public Map<String, Object> delete(@AgentParam(description = "歌曲ID") String songId) {
            for (int i = 0; i < songs.size(); i++) {
                if (songs.get(i).id.equals(songId)) {
                    Song removed = songs.remove(i);
                    return Map.of("status", "deleted", "song", removed);
                }
            }
            throw new IllegalArgumentException("歌曲不存在: " + songId);
        }

        /** 异步方法：返回 CompletableFuture，SDK 自动 unwrap。 */
        @AgentTool(value = "async_echo", description = "异步回显（演示 CompletableFuture unwrap）")
        public CompletableFuture<Map<String, String>> asyncEcho(
                @AgentParam(description = "消息") String message) {
            return CompletableFuture.completedFuture(Map.of("received", message));
        }

        private static Song song(String id, String title, String artist, int duration) {
            Song s = new Song();
            s.id = id;
            s.title = title;
            s.artist = artist;
            s.duration = duration;
            return s;
        }
    }

    public static void main(String[] args) throws Exception {
        AgentQuayClient client = AgentQuayClient.builder()
                .appId("music-app")
                .appName("Music Player")
                .host("localhost")
                .port(0)                 // 从 ~/.agentquay/port 自动读取
                .autoSpawnBridge(true)   // 未检测到服务时自动拉起内嵌 Bridge
                .build();
        client.registerTools(MusicController.class);

        System.out.println("已注册 tools: " + client.listTools());
        System.out.println("等待 Agent 调用…（Ctrl+C 退出）");
        client.connect(); // 阻塞保持连接；Swing 应用可用 connectAsync()
    }
}
