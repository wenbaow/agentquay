/**
 * 可选依赖 keytar（peerDependenciesMeta optional）的短路声明。
 * 用户安装 keytar 后 token 持久化到系统凭证库（Windows Credential Manager /
 * macOS Keychain / libsecret）；未安装时回退 ~/.agentquay/tokens.json。
 * 安装了 keytar 时以包自带类型为准（此处仅在不解析时兜底为 any）。
 */
declare module "keytar";
