// agentquay/agent_tool.cpp — AGENT_TOOL 宏背后的进程级元数据注册表实现。
#include "agentquay/agent_tool.h"

namespace agentquay {

namespace {
// 注意：注册表必须是共享的单一实例（函数内 static 在 add/all 各自作用域会
// 变成两个独立变量，导致 all() 永远读不到 add() 写入的数据）。
std::vector<AgentToolMeta>& toolRegistry()
{
    static std::vector<AgentToolMeta> registry;
    return registry;
}

std::mutex& toolRegistryMutex()
{
    static std::mutex mutex;
    return mutex;
}
} // namespace

void AgentToolRegistry::add(const AgentToolMeta& meta)
{
    std::lock_guard<std::mutex> lock(toolRegistryMutex());
    toolRegistry().push_back(meta);
}

std::vector<AgentToolMeta> AgentToolRegistry::all()
{
    std::lock_guard<std::mutex> lock(toolRegistryMutex());
    return toolRegistry();
}

} // namespace agentquay