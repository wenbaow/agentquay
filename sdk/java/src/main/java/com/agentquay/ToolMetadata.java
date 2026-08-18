package com.agentquay;

import java.lang.reflect.Method;
import java.util.Map;

/**
 * 一个已注册 Tool 的元数据（注册后由扫描生成，含调用绑定）。
 */
public class ToolMetadata {

    /** tool 名规范：[a-zA-Z0-9_-]{1,78}。 */
    static final String TOOL_NAME_PATTERN = "^[a-zA-Z0-9_-]{1,78}$";

    /** appId 规范：[a-z0-9-]{1,48}，禁止 _ 和 .。 */
    static final String APP_ID_PATTERN = "^[a-z0-9-]{1,48}$";

    private final String name;
    private final String description;
    private final Map<String, Object> inputSchema;
    private final boolean requiresConfirmation;
    private final int timeoutSeconds;
    private final int confirmTimeoutSeconds;

    /** 调用绑定：目标实例（静态方法为 null）与方法。 */
    private final Object target;
    private final Method method;

    public ToolMetadata(String name, String description, Map<String, Object> inputSchema,
                        boolean requiresConfirmation, int timeoutSeconds, int confirmTimeoutSeconds,
                        Object target, Method method) {
        this.name = name;
        this.description = description;
        this.inputSchema = inputSchema;
        this.requiresConfirmation = requiresConfirmation;
        this.timeoutSeconds = timeoutSeconds;
        this.confirmTimeoutSeconds = confirmTimeoutSeconds;
        this.target = target;
        this.method = method;
    }

    public String getName() { return name; }

    public String getDescription() { return description; }

    public Map<String, Object> getInputSchema() { return inputSchema; }

    public boolean isRequiresConfirmation() { return requiresConfirmation; }

    public int getTimeoutSeconds() { return timeoutSeconds; }

    public int getConfirmTimeoutSeconds() { return confirmTimeoutSeconds; }

    public Object getTarget() { return target; }

    public Method getMethod() { return method; }

    /** 校验 appId 是否合法。 */
    public static boolean isValidAppId(String appId) {
        return appId != null && appId.matches(APP_ID_PATTERN);
    }

    /** 校验 tool 名是否合法。 */
    public static boolean isValidToolName(String name) {
        return name != null && name.matches(TOOL_NAME_PATTERN);
    }
}
