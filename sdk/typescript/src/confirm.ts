/**
 * 确认弹窗（设计文档 §3.3）：默认处理为终端交互确认（[y/N]），
 * 无交互终端时默认拒绝（安全优先），超时视为取消。
 * GUI 应用（Electron 等）应通过 AgentQuayClient 的 `onConfirm` 提供原生对话框。
 */
import * as readline from "node:readline";

function formatConfirmMessage(message: string, args: unknown): string {
  if (!args) {
    return message;
  }
  let detail: string;
  try {
    detail = JSON.stringify(args, null, 2);
  } catch {
    detail = String(args);
  }
  return `${message}\n\n参数:\n${detail}`;
}

/**
 * 请求用户确认并等待决策。
 *
 * @param message 确认文案
 * @param args 调用参数（展示用）
 * @param timeoutSeconds 确认超时（超时视为取消；Bridge 侧同样计时）
 */
export async function askConfirmation(
  message: string,
  args: unknown = null,
  timeoutSeconds = 120,
): Promise<boolean> {
  // 无交互终端（后台进程 / GUI 应用）→ 默认拒绝（安全优先）
  if (!process.stdin.isTTY) {
    return false;
  }
  const full = formatConfirmMessage(message, args);
  return new Promise<boolean>((resolve) => {
    const rl = readline.createInterface({ input: process.stdin, output: process.stderr });
    let timer: NodeJS.Timeout | null = null;
    const finish = (value: boolean) => {
      if (timer) {
        clearTimeout(timer);
      }
      rl.close();
      resolve(value);
    };
    // 超时视为取消（Bridge 侧同样计时，SDK 侧略长以免竞态）
    timer = setTimeout(() => {
      rl.write("\n确认超时，视为取消\n");
      finish(false);
    }, Math.max(timeoutSeconds, 5) * 1000);
    rl.on("SIGINT", () => finish(false));
    rl.question(`${full} [y/N]: `, (answer) => {
      finish(/^y(es)?$/i.test(answer.trim()));
    });
  });
}
