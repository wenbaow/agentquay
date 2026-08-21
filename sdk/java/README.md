# agentquay-sdk — AgentQuay Java SDK

让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。加个注解，其余全部由框架处理
（设计文档 §4.4）。

## 依赖

```xml
<dependency>
    <groupId>com.agentquay</groupId>
    <artifactId>agentquay-sdk</artifactId>
    <version>0.1.1</version>
</dependency>
```

- 需要 **Java 11+**（WebSocket 用 JDK 内置 `java.net.http.WebSocket`，零第三方传输依赖）
- 编译时开启 `-parameters` 保留方法参数名（`maven-compiler-plugin` 配 `<parameters>true</parameters>`）
- 可选依赖：`com.github.javakeyring:java-keyring:1.0.4`（token 持久化到系统凭证库；
  不引入则回退 `~/.agentquay/tokens.json`）

## 用法

```java
import com.agentquay.AgentParam;
import com.agentquay.AgentQuayClient;
import com.agentquay.AgentTool;

public class MusicController {
    @AgentTool(value = "search", description = "搜索音乐库")
    public List<Song> search(@AgentParam(description = "搜索关键词") String keyword) {
        // 业务逻辑
    }

    @AgentTool(value = "play", description = "播放歌曲")
    public void play(@AgentParam(description = "歌曲ID") String songId) {
        // 业务逻辑
    }

    @AgentTool(value = "delete", description = "删除歌曲", requiresConfirmation = true)
    public void delete(@AgentParam(description = "歌曲ID") String songId) {
        // 危险操作：Bridge 会先向应用弹确认框
    }
}

public static void main(String[] args) throws Exception {
    AgentQuayClient client = AgentQuayClient.builder()
            .appId("music-app")          // [a-z0-9-]{1,48}，禁止 _ 和 .
            .appName("Music Player")
            .host("localhost")
            .port(0)                     // 0 = 从 ~/.agentquay/port 自动读取
            .autoSpawnBridge(true)       // 未检测到服务时自动拉起内嵌 Bridge
            .build();
    client.registerTools(MusicController.class);
    client.connect();                    // 阻塞保持连接；Swing 应用用 connectAsync()
}
```

## 特性

| 能力 | 说明 |
|------|------|
| 注解扫描 | `@AgentTool`（name 缺省回退方法名）+ `@AgentParam`（描述/必填）；支持继承的 public 方法、静态方法、`registerTools(Class)` 与 `registerTools(instance)` |
| JSON Schema | Jackson 类型系统生成：基本类型、String、enum、List/Set/数组、Map、`Optional`（可空）、嵌套 POJO、`@JsonProperty` 重命名 / `@JsonIgnore` |
| 异步方法 | 返回 `CompletableFuture` 自动 unwrap（设计文档 §4.4） |
| 确认流程 | 默认 Swing 对话框（超时自动取消，无图形环境回退控制台）；可用 `.confirmHandler(...)` 自定义 |
| token 持久化 | keyring-java（Windows Credential Manager / Keychain / libsecret，可选）→ `~/.agentquay/tokens.json` 回退 |
| 心跳 + 重连 | 30s 应用层 ping/pong；断线指数退避重连（1s → 30s），自动携带 token；被替换（同 appId 新实例）时 `connect()` 抛 `ReplacedException` |
| auto-spawn | 本地无 Bridge 时自动拉起内嵌二进制（`serve --embedded`，日志写应用缓存目录），JVM 退出自动终止 |

## 确认流程自定义

```java
AgentQuayClient client = AgentQuayClient.builder()
        .confirmHandler((message, arguments, timeoutSeconds) -> {
            System.out.println("⚠️ " + message + " " + arguments);
            return readLine().equalsIgnoreCase("y"); // true=确认 / false=取消
        })
        .build();
```

## 构建与测试

```bash
mvn compile                # 编译（含 examples 源码目录）
mvn test                   # 单测 11 项 + 端到端 7 项（需先构建 bridge/dist）
mvn -q compile exec:java -Dexec.mainClass=com.agentquay.example.MusicApp   # 示例应用
```

端到端覆盖（`AgentQuayE2ETest`，真实 Go Bridge）：注册/token、`tools/list` 合成名与描述前缀、
正常调用、`-32003` 参数校验、业务错误透传、确认流程（确认/取消 `-32005`）、
同 appId 替换（`ReplacedException`）、断线重连携带 token。

> 测试通过环境变量 `AGENTQUAY_BRIDGE_BIN` 或自动查找 `bridge/dist/` 定位 Bridge 二进制。
