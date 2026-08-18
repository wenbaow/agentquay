//! 确认流程回调（设计文档 §3.3）：危险操作（requires_confirmation=true）被调用时，
//! Bridge 向应用发送 `confirm` 消息，SDK 调用确认回调决定是否放行。

use serde_json::Value;

/// 确认回调：返回 true=确认 / false=取消（异常、超时视为取消，安全优先）。
pub trait ConfirmationHandler: Send + Sync + 'static {
    fn confirm(&self, message: &str, arguments: Option<&Value>, timeout_seconds: u32) -> bool;
}

/// 拼接确认文案（含参数展示）。
fn format_message(message: &str, arguments: Option<&Value>) -> String {
    match arguments {
        Some(args) => format!("{message}\n\n参数:\n{}", serde_json::to_string_pretty(args).unwrap_or_default()),
        None => message.to_string(),
    }
}

/// 默认确认回调：控制台 y/n 输入。
///
/// 说明：阻塞式控制台输入无法按 `timeout_seconds` 计时——Bridge 侧独立计时，
/// 确认超时由 Bridge 判定为取消，SDK 迟到的 confirm_result 会进入孤儿处理。
pub struct ConsoleConfirmationHandler;

impl ConfirmationHandler for ConsoleConfirmationHandler {
    fn confirm(&self, message: &str, arguments: Option<&Value>, _timeout_seconds: u32) -> bool {
        eprintln!("\n[AgentQuay 确认] {}", format_message(message, arguments));
        loop {
            eprint!("确认执行？[y/N]: ");
            use std::io::Write;
            let _ = std::io::stdout().flush();
            let mut line = String::new();
            if std::io::stdin().read_line(&mut line).is_err() {
                return false;
            }
            match line.trim().to_ascii_lowercase().as_str() {
                "y" | "yes" => return true,
                "" | "n" | "no" => return false,
                _ => eprintln!("请输入 y 或 n"),
            }
        }
    }
}

/// 原生对话框确认回调（特性 `native-confirm`，基于 rfd；三平台 OS 原生弹窗）。
#[cfg(feature = "native-confirm")]
pub struct NativeConfirmationHandler;

#[cfg(feature = "native-confirm")]
impl ConfirmationHandler for NativeConfirmationHandler {
    fn confirm(&self, message: &str, arguments: Option<&Value>, _timeout_seconds: u32) -> bool {
        let dialog = rfd::MessageDialog::new()
            .set_title("AgentQuay 确认")
            .set_description(format_message(message, arguments))
            .set_buttons(rfd::MessageButtons::YesNo)
            .set_level(rfd::MessageLevel::Warning);
        matches!(dialog.show(), rfd::MessageDialogResult::Yes)
    }
}

/// 默认确认回调：native-confirm 特性开启时用原生弹窗，否则控制台。
pub(crate) fn default_handler() -> std::sync::Arc<dyn ConfirmationHandler> {
    #[cfg(feature = "native-confirm")]
    {
        std::sync::Arc::new(NativeConfirmationHandler)
    }
    #[cfg(not(feature = "native-confirm"))]
    {
        std::sync::Arc::new(ConsoleConfirmationHandler)
    }
}

/// 供测试 / 自动化使用：固定答案。
pub struct AutoConfirmationHandler {
    answer: std::sync::atomic::AtomicBool,
}

impl AutoConfirmationHandler {
    pub fn new(answer: bool) -> Self {
        Self { answer: std::sync::atomic::AtomicBool::new(answer) }
    }
    pub fn set_answer(&self, answer: bool) {
        self.answer.store(answer, std::sync::atomic::Ordering::Relaxed);
    }
}

impl ConfirmationHandler for AutoConfirmationHandler {
    fn confirm(&self, _message: &str, _arguments: Option<&Value>, _timeout_seconds: u32) -> bool {
        self.answer.load(std::sync::atomic::Ordering::Relaxed)
    }
}
