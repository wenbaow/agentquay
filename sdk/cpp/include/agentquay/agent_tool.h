// agentquay/agent_tool.h — AGENT_TOOL / AGENT_TOOL_OPTS 宏：编译期元数据登记
// （设计文档 §4.7 "Qt 路线"，Qt 6.5+）。
//
// C++ 无原生注解/反射机制。SDK 提供两条实现路径：
//   1. Qt 路线（主）：QObject + Q_INVOKABLE 方法 + AGENT_TOOL 元数据宏。
//      运行时由 QMetaObject 反射查找并调用（QMetaMethod::invoke，参数经 QVariant 转换）。
//   2. 通用 fallback：AgentQuayClient::addTool(...) 传入 std::function 处理器，
//      无需 Q_OBJECT / Q_INVOKABLE（见 AgentQuayClient.h）。
//
// 用法（宏放在方法声明之前，需配合 Q_INVOKABLE）：
//
//   class MusicController : public QObject {
//       Q_OBJECT
//   public:
//       AGENT_TOOL(search, "搜索音乐库中的歌曲")
//       Q_INVOKABLE QVariantList search(const QString& keyword);
//
//       // 危险操作：需要用户确认（第三个参数为 AgentToolOptions{...}）
//       AGENT_TOOL_OPTS(deleteSong, "删除歌曲（危险操作）", agentquay::AgentToolOptions{true})
//       Q_INVOKABLE QVariantMap deleteSong(const QString& songId);
//   };
//
// 说明：
//   - 宏第一参数为方法名（同时是 tool 名），第二参数为描述（字符串字面量）；
//   - AGENT_TOOL_OPTS 的第三参数为 agentquay::AgentToolOptions{...}：
//     C++17 按成员顺序（requiresConfirmation, timeoutSeconds, confirmTimeoutSeconds），
//     C++20 可用 designated initializer：{ .requiresConfirmation = true }；
//   - 宏展开为类内 static inline 注册器成员，在静态初始化阶段把方法名与
//     元数据登记到进程级注册表；registerTools<T>() 时按方法名与 QMetaObject 匹配绑定；
//   - 若你使用的老版本 moc 无法解析 `AGENT_TOOL(name, "desc")` 形式，
//     将宏调用行用 `#ifndef Q_MOC_RUN` / `#endif` 包裹即可（Qt 6 默认可直接使用）。
#pragma once

#include <QString>
#include <mutex>
#include <vector>

namespace agentquay {

/** AGENT_TOOL_OPTS 的第三参数：危险确认 / 超时等选项。 */
struct AgentToolOptions {
    bool requiresConfirmation = false;   // 危险操作：调用前弹确认框
    int timeoutSeconds = 30;             // 执行超时（Bridge 侧独立计时）
    int confirmTimeoutSeconds = 120;     // 确认超时（独立于执行超时）
};

/** 编译期元数据（AGENT_TOOL 展开时登记）。 */
struct AgentToolMeta {
    const char* name = nullptr;
    const char* description = nullptr;
    AgentToolOptions options;
};

/** 进程级工具元数据注册表（静态初始化阶段写入，registerTools 时读取）。 */
class AgentToolRegistry {
public:
    /** 登记一条元数据（AGENT_TOOL 展开时调用）。 */
    static void add(const AgentToolMeta& meta);

    /** 读取全部已登记元数据（registerTools<T>() 时匹配方法名）。 */
    static std::vector<AgentToolMeta> all();
};

/** 类内 static inline 注册器：构造时将元数据登记到进程级注册表。 */
class ToolRegistrar {
public:
    explicit ToolRegistrar(const AgentToolMeta& meta) { AgentToolRegistry::add(meta); }
};

} // namespace agentquay

// AGENT_TOOL(methodName, description)                     —— 两参形式，选项取默认值
// AGENT_TOOL_OPTS(methodName, description, options)       —— 三参形式，options 为
//                                                            agentquay::AgentToolOptions{...}
// 两种形式均须放在 Q_INVOKABLE 方法声明之前。
#define AGENT_TOOL(methodName, description) \
    static inline const agentquay::ToolRegistrar agentQuay_toolReg_##methodName{ \
        agentquay::AgentToolMeta{ #methodName, description, agentquay::AgentToolOptions{} } \
    };

#define AGENT_TOOL_OPTS(methodName, description, options) \
    static inline const agentquay::ToolRegistrar agentQuay_toolReg_##methodName{ \
        agentquay::AgentToolMeta{ #methodName, description, options } \
    };