// agentquay/TokenStore.h — token 持久化（设计文档 §4.7、§6.2）。
//
// v1 默认实现为文件存储：~/.agentquay/tokens.json（{ appId: token, ... }），
// 与 Java / C# SDK 的 file fallback 完全兼容。后续可扩展 qtkeychain
// （macOS Keychain / Windows Credential Manager / libsecret）作为首选存储，
// 文件作为回退——保持本类的接口不变即可。
#pragma once

#include <QString>

namespace agentquay {

class TokenStore {
public:
    explicit TokenStore(QString  appId);

    /** 读取已持久化的 token（无则返回空字符串）。 */
    QString get() const;

    /** 持久化 token（覆盖旧值）。 */
    void set(const QString& token) const;

    /** 凭证文件路径：~/.agentquay/tokens.json。 */
    static QString filePath();

private:
    QString m_appId;
};

} // namespace agentquay