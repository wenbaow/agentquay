package com.agentquay;

import javax.swing.JDialog;
import javax.swing.JOptionPane;
import javax.swing.JTextArea;
import javax.swing.SwingUtilities;
import javax.swing.Timer;
import java.awt.GraphicsEnvironment;
import java.util.Map;
import java.util.Scanner;
import java.util.concurrent.atomic.AtomicReference;
import java.util.logging.Logger;

/**
 * 默认确认对话框：优先 Swing（OS 原生观感，三平台可用），带超时自动取消；
 * 无图形环境（headless）时回退控制台输入。
 */
public final class ConfirmDialog {

    private static final Logger LOG = Logger.getLogger(ConfirmDialog.class.getName());

    private ConfirmDialog() {
    }

    /** 询问用户（阻塞当前线程直到决策或超时）。 */
    public static boolean ask(String message, Map<String, Object> arguments, int timeoutSeconds) {
        String full = format(message, arguments);

        if (GraphicsEnvironment.isHeadless()) {
            LOG.info("无图形环境，使用控制台确认: " + full);
            return consoleAsk(full);
        }

        AtomicReference<Boolean> result = new AtomicReference<>(null);
        try {
            SwingUtilities.invokeAndWait(() -> result.set(swingAsk(full, timeoutSeconds)));
        } catch (Exception e) {
            LOG.warning("弹窗失败（" + e + "），使用控制台确认");
            return consoleAsk(full);
        }
        Boolean value = result.get();
        return value != null && value;
    }

    // ------------------------------------------------------------------

    private static String format(String message, Map<String, Object> arguments) {
        if (arguments == null || arguments.isEmpty()) {
            return message;
        }
        return message + "\n\n参数:\n" + arguments;
    }

    private static boolean swingAsk(String message, int timeoutSeconds) {
        JTextArea text = new JTextArea(message);
        text.setEditable(false);
        text.setLineWrap(true);
        text.setWrapStyleWord(true);
        text.setColumns(40);
        text.setRows(8);

        JOptionPane pane = new JOptionPane(text, JOptionPane.QUESTION_MESSAGE,
                JOptionPane.YES_NO_OPTION);
        JDialog dialog = pane.createDialog(null, "AgentQuay 确认");
        dialog.setModal(true);

        if (timeoutSeconds > 0) {
            Timer timer = new Timer(timeoutSeconds * 1000, e -> {
                pane.setValue(JOptionPane.NO_OPTION); // 超时视为取消
                dialog.setVisible(false);
            });
            timer.setRepeats(false);
            timer.start();
            dialog.setVisible(true);
            timer.stop();
        } else {
            dialog.setVisible(true);
        }
        dialog.dispose();

        Object value = pane.getValue();
        return value != null && ((Integer) value) == JOptionPane.YES_OPTION;
    }

    private static boolean consoleAsk(String message) {
        System.out.println(message);
        Scanner scanner = new Scanner(System.in);
        while (true) {
            System.out.print("确认执行？[y/N]: ");
            if (!scanner.hasNextLine()) {
                return false;
            }
            String line = scanner.nextLine().trim().toLowerCase();
            if (line.equals("y") || line.equals("yes")) {
                return true;
            }
            if (line.isEmpty() || line.equals("n") || line.equals("no")) {
                return false;
            }
            System.out.println("请输入 y 或 n");
        }
    }
}
