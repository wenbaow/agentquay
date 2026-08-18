package com.agentquay;

import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * 方法参数的补充元数据（设计文档 §4.4）。
 */
@Retention(RetentionPolicy.RUNTIME)
@Target(ElementType.PARAMETER)
public @interface AgentParam {

    /** 参数描述（写入 JSON Schema 的 property description）。 */
    String description() default "";

    /** 是否必填（默认 true；Optional 类型始终视为可选）。 */
    boolean required() default true;
}
