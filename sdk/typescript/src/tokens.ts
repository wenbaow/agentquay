/**
 * token 持久化（设计文档 §6.2）：优先系统凭证库（keytar，可选依赖），
 * 不可用时回退 ~/.agentquay/tokens.json（0600 权限）。
 */
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const KEYRING_SERVICE = "agentquay";

/** 回退文件路径：~/.agentquay/tokens.json（与 Python / Java SDK 一致）。 */
const FALLBACK_FILE = path.join(os.homedir(), ".agentquay", "tokens.json");

let keytarModule: {
  getPassword: (service: string, account: string) => Promise<string | null>;
  setPassword: (service: string, account: string, password: string) => Promise<void>;
} | null = null;
let keytarChecked = false;

/** 懒加载可选依赖 keytar（未安装时返回 null，回退文件存储）。 */
async function loadKeytar(): Promise<typeof keytarModule> {
  if (!keytarChecked) {
    keytarChecked = true;
    try {
      const mod = (await import("keytar")) as typeof keytarModule & {
        default?: typeof keytarModule;
      };
      keytarModule = (mod.default ?? mod) as typeof keytarModule;
    } catch {
      keytarModule = null; // 未安装 → 文件回退
    }
  }
  return keytarModule;
}

/** appId → token 的持久化存储。 */
export class TokenStore {
  constructor(private readonly appId: string) {}

  /** 读取已持久化的 token（无则返回 null）。 */
  async get(): Promise<string | null> {
    const keytar = await loadKeytar();
    if (keytar) {
      try {
        const token = await keytar.getPassword(KEYRING_SERVICE, this.appId);
        if (token) {
          return token;
        }
      } catch {
        // keytar 后端异常 → 回退文件存储
      }
    }
    return this.readFile();
  }

  /** 持久化 token（重连自动携带）。 */
  async set(token: string): Promise<void> {
    const keytar = await loadKeytar();
    if (keytar) {
      try {
        await keytar.setPassword(KEYRING_SERVICE, this.appId, token);
        return;
      } catch {
        // keytar 写入失败 → 回退文件存储
      }
    }
    await this.writeFile(token);
  }

  private async readFile(): Promise<string | null> {
    try {
      const data = JSON.parse(await fs.promises.readFile(FALLBACK_FILE, "utf8")) as Record<
        string,
        unknown
      >;
      const token = data[this.appId];
      return typeof token === "string" && token.length > 0 ? token : null;
    } catch {
      return null;
    }
  }

  private async writeFile(token: string): Promise<void> {
    try {
      await fs.promises.mkdir(path.dirname(FALLBACK_FILE), { recursive: true });
      let data: Record<string, unknown> = {};
      try {
        data = JSON.parse(await fs.promises.readFile(FALLBACK_FILE, "utf8")) as Record<
          string,
          unknown
        >;
      } catch {
        // 文件不存在或损坏 → 重建
      }
      data[this.appId] = token;
      await fs.promises.writeFile(FALLBACK_FILE, JSON.stringify(data, null, 2), {
        mode: 0o600,
      });
      try {
        await fs.promises.chmod(FALLBACK_FILE, 0o600);
      } catch {
        // Windows 无 POSIX 权限语义
      }
    } catch {
      // 写入失败不阻断注册（token 仅影响重连）
    }
  }
}
