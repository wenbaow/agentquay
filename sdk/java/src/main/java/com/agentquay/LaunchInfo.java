package com.agentquay;

import com.agentquay.internal.Protocol;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.io.File;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/**
 * 应用的启动命令（随 register 上报，供 Bridge 离线自动拉起，设计文档 §5.8）。
 *
 * <p>JVM 无法可靠反推"该启动哪个进程"，因此 {@link #detect()} 为尽力而为：
 * <ol>
 *   <li>jpackage / 安装器生成的应用：{@link ProcessHandle} 给出真实的可执行文件（优先）；</li>
 *   <li>普通 {@code java -jar app.jar}：java 可执行文件 + {@code -jar &lt;jar&gt;}；</li>
 *   <li>兜底：java 可执行文件本身。</li>
 * </ol>
 * 推荐生产环境用 {@link Builder} 显式指定（如安装目录里的 wrapper exe）。
 */
public final class LaunchInfo {

    private final String execPath;
    private final List<String> args;
    private final String cwd;
    private final boolean singleInstance;
    private final int launchTimeoutSeconds;

    private LaunchInfo(Builder b) {
        this.execPath = b.execPath;
        this.args = b.args;
        this.cwd = b.cwd;
        this.singleInstance = b.singleInstance;
        this.launchTimeoutSeconds = b.launchTimeoutSeconds;
    }

    public static Builder builder() {
        return new Builder();
    }

    /**
     * 尽力探测本进程的启动命令；失败返回 {@code null}（不阻塞注册，可后续手工 apps add）。
     */
    public static LaunchInfo detect() {
        // 1. jpackage / 安装器生成的应用：ProcessHandle 直接给出真实可执行文件
        try {
            String cmd = ProcessHandle.current().info().command().orElse(null);
            if (cmd != null && !cmd.isEmpty() && Files.isExecutable(Path.of(cmd))) {
                return builder().execPath(cmd).build();
            }
        } catch (Throwable ignored) {
            // 继续走 jar 探测
        }

        // 2. 普通 java -jar / classpath 启动
        try {
            String javaHome = System.getProperty("java.home");
            Path java = Path.of(javaHome, "bin", "java" + (isWindows() ? ".exe" : ""));
            String jar = findMainJar();
            if (jar != null) {
                return builder().execPath(java.toString()).args(Arrays.asList("-jar", jar)).build();
            }
            return builder().execPath(java.toString()).build();
        } catch (Throwable ignored) {
            return null;
        }
    }

    /**
     * 从 classpath 中找出"应用主 jar"：优先唯一 jar；否则按 {@code sun.java.command}
     * 中的主类名在 jar 内查找对应 class。
     */
    private static String findMainJar() {
        try {
            String classPath = System.getProperty("java.class.path", "");
            List<String> entries = new ArrayList<>();
            for (String e : classPath.split(File.pathSeparator)) {
                if (!e.isEmpty()) {
                    entries.add(e);
                }
            }
            if (entries.isEmpty()) {
                return null;
            }
            // 唯一 jar：直接返回
            List<String> jars = new ArrayList<>();
            for (String e : entries) {
                if (e.toLowerCase().endsWith(".jar") && Files.isRegularFile(Path.of(e))) {
                    jars.add(e);
                }
            }
            if (jars.size() == 1) {
                return jars.get(0);
            }
            // 多 jar：按主类名匹配
            String command = System.getProperty("sun.java.command", "");
            String mainClass = command.isBlank() ? "" : command.split("\\s+")[0].replace('.', '/');
            for (String jar : jars) {
                try (java.util.jar.JarFile jf = new java.util.jar.JarFile(jar)) {
                    if (mainClass.endsWith(".class")) {
                        if (jf.getJarEntry(mainClass) != null) {
                            return jar;
                        }
                    }
                } catch (Exception ignored) {
                    // 损坏 jar 跳过
                }
            }
            // 找不到主公 jar：取未被依赖目录包含的第一个 jar
            return jars.get(0);
        } catch (Throwable ignored) {
            return null;
        }
    }

    private static boolean isWindows() {
        return System.getProperty("os.name", "").toLowerCase().contains("win");
    }

    /** 序列化为注册 payload 的 launch 字段。 */
    public ObjectNode toJson() {
        ObjectNode node = Protocol.MAPPER.createObjectNode();
        node.put("execPath", execPath);
        if (args != null && !args.isEmpty()) {
            ArrayNode arr = node.putArray("args");
            args.forEach(arr::add);
        }
        if (cwd != null && !cwd.isEmpty()) {
            node.put("cwd", cwd);
        }
        node.put("singleInstance", singleInstance);
        if (launchTimeoutSeconds > 0) {
            node.put("launchTimeoutSeconds", launchTimeoutSeconds);
        }
        return node;
    }

    /** 构造器。 */
    public static final class Builder {
        private String execPath = "";
        private List<String> args = new ArrayList<>();
        private String cwd = "";
        private boolean singleInstance = false;
        private int launchTimeoutSeconds = 0;

        /** 可执行文件绝对路径（必填）。 */
        public Builder execPath(String execPath) {
            this.execPath = execPath;
            return this;
        }

        /** 启动参数（argv 数组，禁止 shell 字符串）。 */
        public Builder args(List<String> args) {
            this.args = args == null ? new ArrayList<>() : new ArrayList<>(args);
            return this;
        }

        /** 工作目录（可空）。 */
        public Builder cwd(String cwd) {
            this.cwd = cwd;
            return this;
        }

        /** 单实例应用（已在运行时不再重复拉起）。 */
        public Builder singleInstance(boolean singleInstance) {
            this.singleInstance = singleInstance;
            return this;
        }

        /** 启动等待超时秒数（0 = 用 Bridge 全局默认）。 */
        public Builder launchTimeoutSeconds(int launchTimeoutSeconds) {
            this.launchTimeoutSeconds = launchTimeoutSeconds;
            return this;
        }

        public LaunchInfo build() {
            if (execPath == null || execPath.isEmpty()) {
                throw new IllegalArgumentException("execPath 不能为空");
            }
            Set<String> dedup = new LinkedHashSet<>(args);
            return new LaunchInfo(new Builder()
                    .execPath(execPath)
                    .args(new ArrayList<>(dedup))
                    .cwd(cwd)
                    .singleInstance(singleInstance)
                    .launchTimeoutSeconds(launchTimeoutSeconds));
        }
    }
}