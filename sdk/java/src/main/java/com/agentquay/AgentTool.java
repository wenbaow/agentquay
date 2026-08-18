package com.agentquay;

import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * 标记方法为 Agent Tool（设计文档 §4.4）。
 *
 * <pre>{@code
 * @AgentTool(value = "search", description = "搜索音乐库")
 * public List<Song> search(@AgentParam(description = "搜索关键词") String keyword) { ... }
 * }</pre>
 */
@Retention(RetentionPolicy.RUNTIME)
@Target(ElementType.METHOD)
public @interface AgentTool {

    /** Tool 名（缺省回退到方法名）。 */
    String value() default "";

    /** 描述（Agent 侧展示）。 */
    String description() default "";

    /** 危险操作：调用时先弹确认框，用户确认后才执行。 */
    boolean requiresConfirmation() default false;

    /** 执行超时秒数（默认 30，Bridge 侧独立计时）。 */
    int timeoutSeconds() default 30;

    /** 确认超时秒数（默认 120，独立于执行超时）。 */
    int confirmTimeoutSeconds() default 120;
}
