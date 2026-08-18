# AgentQuay C++ SDK

让 AI Agent 通过标准 MCP 协议发现并调用你的 Qt 桌面应用方法。加个宏，其余全部由框架处理（设计文档 §4.7）。

## 依赖

- **Qt 6.5+**（Core / Network / WebSockets 必需；Widgets 可选，默认确认框使用原生 QMessageBox）
  - 注意：Qt 6.5 起 WebSockets 为独立模块，aqt 安装时需显式带上：
    `aqt install-qt -O D:\Qt windows desktop 6.8.3 win64_msvc2022_64 -m qtwebsockets`
- **CMake 3.16+**
- **C++17** 编译器（MSVC / GCC / Clang 均可）
  - MSVC 需 `/utf-8`（CMake 自动添加）以正确解析源码中的 UTF-8 中文注释

## 在本机 Windows 11 上构建与测试（已验证）

```powershell
# 环境：VS 2022 (MSVC 14.51) + Qt 6.8.3 (msvc2022_64 + qtwebsockets) + Ninja
powershell -ExecutionPolicy Bypass -File sdk/cpp/_build.ps1
```

验证结果（2026-08-17，Windows 11）：

```
Step 2 Build  exit: 0    # agentquay.lib + music_app.exe + schema_tests.exe + e2e_tests.exe
Step 3 Schema  exit: 0    # 12 项单测全过（类型映射 / 协议编解码 / token 回环 / 端口文件）
Step 4 E2E     exit: 0    # 9 项端到端全过（真实 Go Bridge：注册 / tools/list / 调用 /
                          #   -32003 校验 / 业务错误 / 确认取消 -32005 / 替换 / token 重连）
```

手工运行测试：

```bash
cd sdk/cpp/build/tests
./schema_tests.exe -o result.txt        # QTest 输出重定向到文件（Windows 控制台默认吞输出）
AGENTQUAY_BRIDGE_BIN=../../bridge/dist/agentquay-windows-amd64.exe ./e2e_tests.exe -o e2e.txt
```

## 用法

```cpp
#include <QObject>
#include <agentquay/agent_tool.h>
#include <agentquay/AgentQuayClient.h>

class MusicController : public QObject {
    Q_OBJECT
public:
    AGENT_TOOL(search, "搜索音乐库中的歌曲")
    Q_INVOKABLE QVariantList search(const QString& keyword);

    AGENT_TOOL(play, "播放指定歌曲")
    Q_INVOKABLE QVariantMap play(const QString& songId);

    AGENT_TOOL_OPTS(deleteSong, "删除歌曲（危险操作）", agentquay::AgentToolOptions{true})
    Q_INVOKABLE QVariantMap deleteSong(const QString& songId);
};
```

```cpp
int main(int argc, char* argv[])
{
    QCoreApplication app(argc, argv);
    AgentQuayClient client("music-app", "Music Player");
    client.setPort(0);                // 从 ~/.agentquay/port 自动读取
    client.setAutoSpawnBridge(true);  // 未检测到服务时自动拉起内嵌 Bridge
    client.registerTools<MusicController>();
    client.connect();                 // 异步连接，与 Qt 事件循环共存
    return app.exec();
}
```

Agent 配置 MCP 端点 `http://127.0.0.1:19846/mcp` 即可发现 `music-app_search` / `music-app_play` / `music-app_delete` 等工具。

## 特性

| 能力 | 说明 |
|------|------|
| 反射注册 | `AGENT_TOOL(methodName, description)` 宏 + `Q_INVOKABLE` 方法；运行时 `QMetaObject::invoke` 按声明顺序传参 |
| JSON Schema | 基于 `QMetaMethod::parameterMetaTypes()` 生成：基本类型、`QString`/`QByteArray`/`QUrl`/日期时间、数值族、`bool`、`QVariantMap`/`QJsonArray`/`QStringList`、已注册枚举 |
| 通用 fallback | `addTool(name, handler, ...)` 无需 Q_OBJECT / Q_INVOKABLE；任意可调用对象均可注册 |
| 确认流程 | 默认 QMessageBox（QApplication 环境）/ 控制台输入（headless）；可用 `setConfirmHandler(...)` 自定义 |
| token 持久化 | `~/.agentquay/tokens.json`（与 Java/TS/Python SDK 共享同一份，后续可扩展 qtkeychain） |
| 心跳 + 重连 | 30s 双向 ping/pong；断线指数退避重连（1s → 30s），自动携带 token；被同 appId 新实例替换时触发 `replaced()` 信号 |
| auto-spawn | 本地无 Bridge 时自动拉起内嵌二进制（`serve --embedded`），进程退出时随应用停止 |
| 看门狗 | 本地执行超时 = Bridge 执行超时 + 5s；迟到结果进入孤儿缓冲（由 Bridge 处理，SDK 侧丢弃本地 pending） |

## 参数类型 → Schema 映射

| C++ 类型 | JSON Schema |
|---------|-------------|
| `QString` / `QByteArray` / `QChar` / `QUrl` / `QDate` / `QTime` / `QDateTime` | `{ "type": "string" }` |
| `int` / `uint` / `short` / `ushort` / `long long` / `ulong long` / `char` / `uchar` | `{ "type": "integer" }` |
| `float` / `double` | `{ "type": "number" }` |
| `bool` | `{ "type": "boolean" }` |
| `QVariantList` / `QStringList` / `QJsonArray` | `{ "type": "array", "items": {} }` |
| `QVariantMap` / `QJsonObject` | `{ "type": "object" }` |
| 已注册枚举（`Q_ENUM` / `Q_DECLARE_METATYPE`） | `{ "enum": [key1, key2, ...] }` |
| 其他未识别类型 | `{}`（不加约束） |

参数名从 `QMetaMethod::parameterNames()` 读取（moc 保留的声明顺序），缺省回退 `argN`。

## 构建与安装

```bash
# 构建
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j

# 测试（需先构建 bridge/dist/agentquay）
cmake --build build --target test

# 安装
cmake --install build --prefix /usr/local
```

### 消费方使用（find_package）

```cmake
find_package(Qt6 6.5 REQUIRED COMPONENTS Core Network WebSockets)
find_package(agentquay REQUIRED)

target_link_libraries(my_app PRIVATE agentquay::agentquay)
```

### vcpkg / Conan

v1 阶段随源码分发，v2 计划推至包管理器。

## 确认流程自定义

```cpp
client.setConfirmHandler([](const QString& message,
                            const QVariantMap& arguments,
                            int timeoutSeconds) {
    qInfo() << "⚠️ 确认:" << message << arguments;
    // 返回 true = 允许执行，false = 拒绝
    return false;
});
```

## 非 Qt 场景

若应用不依赖 Qt（或不想用 `Q_INVOKABLE`），使用通用 fallback：

```cpp
auto controller = std::make_shared<MyController>();
client.addTool("search", [controller](const QVariantMap& args) -> QVariant {
    const QString keyword = args.value("keyword").toString();
    return controller->search(keyword);
}, "搜索音乐库");
```

## 信号一览

```
connected()                  — 注册成功
disconnectedWithReason(reason) — 连接断开
replaced()                   — 被同 appId 新实例替换
bridgeNotification(message)  — Bridge 广播通知
fatalError(message)          — 不可恢复错误（客户端已停止）
```

## 端到端覆盖

`tests/e2e_test.cpp`（真实 Go Bridge，`e2e_tests` 可执行文件）：
注册/token、tools/list 合成名与描述前缀、正常调用、`-32003` 参数校验、业务错误透传、
确认流程（确认/取消 `-32005`）、同 appId 替换（`replaced()` 信号）、
断线重连携带 token。

> 测试通过环境变量 `AGENTQUAY_BRIDGE_BIN` 或自动查找 `bridge/dist/` 定位 Bridge 二进制。
> 测试启动时会清理 `~/.agentquay/auth.json`（全新注册，避免历史 token 冲突）。

单测（`tests/schema_gen_test.cpp`，`schema_tests` 可执行文件，不依赖 Bridge）：
参数类型 → JSON Schema 映射（string/integer/number/boolean/array/object）、空参数方法、
协议编解码、register payload、TokenStore 回环、BridgeSpawner 端口文件读取。