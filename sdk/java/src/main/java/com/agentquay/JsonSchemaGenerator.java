package com.agentquay;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.databind.BeanDescription;
import com.fasterxml.jackson.databind.JavaType;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.introspect.BeanPropertyDefinition;

import java.lang.reflect.Method;
import java.lang.reflect.Parameter;
import java.lang.reflect.ParameterizedType;
import java.lang.reflect.Type;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

/**
 * 从方法签名与类型注解生成 JSON Schema（设计文档 §4.4 "Jackson 生成 JSON Schema"）。
 *
 * <p>基于 Jackson 的类型系统（JavaType + BeanDescription），支持：
 * 基本类型、String、enum、List/Set/数组、Map、Optional、嵌套 POJO，
 * 以及 Jackson 注解（@JsonProperty 重命名 / @JsonIgnore 忽略）。
 * 方法参数名依赖编译期 {@code -parameters} 保留。
 */
public final class JsonSchemaGenerator {

    private final ObjectMapper mapper;

    public JsonSchemaGenerator() {
        this.mapper = new ObjectMapper();
    }

    /**
     * 生成方法参数的 inputSchema：{@code {"type":"object","properties":{...},"required":[...]}}。
     */
    public Map<String, Object> paramSchema(Method method) {
        Map<String, Object> properties = new LinkedHashMap<>();
        List<String> required = new ArrayList<>();

        int index = 0;
        for (Parameter param : method.getParameters()) {
            AgentParam ap = param.getAnnotation(AgentParam.class);
            boolean markedRequired = ap == null || ap.required();
            boolean optionalType = isOptional(param.getParameterizedType());

            String propName = param.getName();
            // 编译器未保留参数名时回退 "argN" 并警告（pom 已开启 -parameters）
            if (propName == null || propName.startsWith("arg")) {
                propName = "arg" + index;
            }
            index++;

            Map<String, Object> schema = typeSchema(param.getParameterizedType());
            if (ap != null && !ap.description().isEmpty()) {
                schema.put("description", ap.description());
            }
            properties.put(propName, schema);

            if (markedRequired && !optionalType) {
                required.add(propName);
            }
        }

        Map<String, Object> root = new LinkedHashMap<>();
        root.put("type", "object");
        root.put("properties", properties);
        if (!required.isEmpty()) {
            root.put("required", required);
        }
        return root;
    }

    /** 类型 → JSON Schema（Optional 类型剥壳并标记可空）。 */
    public Map<String, Object> typeSchema(Type type) {
        return typeSchema(type, new HashSet<>());
    }

    private Map<String, Object> typeSchema(Type type, Set<Class<?>> seen) {
        if (type instanceof ParameterizedType) {
            ParameterizedType pt = (ParameterizedType) type;
            Class<?> raw = (Class<?>) pt.getRawType();
            Type[] args = pt.getActualTypeArguments();

            if (raw == Optional.class) {
                Map<String, Object> inner = typeSchema(args[0], seen);
                // Optional → 可空
                if (inner.containsKey("type")) {
                    Object t = inner.get("type");
                    if (t instanceof String) {
                        inner.put("type", java.util.Arrays.asList(t, "null"));
                    }
                } else {
                    inner.put("nullable", true);
                }
                return inner;
            }
            if (List.class.isAssignableFrom(raw) || Set.class.isAssignableFrom(raw)
                    || Iterable.class.isAssignableFrom(raw)) {
                Map<String, Object> schema = new LinkedHashMap<>();
                schema.put("type", "array");
                if (Set.class.isAssignableFrom(raw)) {
                    schema.put("uniqueItems", true);
                }
                schema.put("items", args.length > 0 ? typeSchema(args[0], seen) : emptySchema());
                return schema;
            }
            if (Map.class.isAssignableFrom(raw)) {
                Map<String, Object> schema = new LinkedHashMap<>();
                schema.put("type", "object");
                schema.put("additionalProperties",
                        args.length > 1 ? typeSchema(args[1], seen) : emptySchema());
                return schema;
            }
        }

        Class<?> raw = rawClass(type);
        if (raw == null) {
            return emptySchema();
        }
        if (raw == String.class || raw == Character.class || raw == char.class) {
            return mapOf("type", "string");
        }
        if (raw == Integer.class || raw == int.class || raw == Long.class || raw == long.class
                || raw == Short.class || raw == short.class || raw == Byte.class || raw == byte.class) {
            return mapOf("type", "integer");
        }
        if (raw == Double.class || raw == double.class || raw == Float.class || raw == float.class) {
            return mapOf("type", "number");
        }
        if (raw == Boolean.class || raw == boolean.class) {
            return mapOf("type", "boolean");
        }
        if (raw.isEnum()) {
            List<Object> values = new ArrayList<>();
            for (Object c : raw.getEnumConstants()) {
                values.add(c instanceof Enum ? ((Enum<?>) c).name() : String.valueOf(c));
            }
            Map<String, Object> schema = new LinkedHashMap<>();
            schema.put("enum", values);
            return schema;
        }
        if (raw.isArray()) {
            Map<String, Object> schema = new LinkedHashMap<>();
            schema.put("type", "array");
            schema.put("items", typeSchema(raw.getComponentType(), seen));
            return schema;
        }
        // 嵌套 POJO
        return beanSchema(raw, seen);
    }

    /** 用 Jackson BeanDescription 内省 POJO 属性。 */
    private Map<String, Object> beanSchema(Class<?> beanClass, Set<Class<?>> seen) {
        Map<String, Object> schema = new LinkedHashMap<>();
        schema.put("type", "object");
        Map<String, Object> properties = new LinkedHashMap<>();
        List<String> required = new ArrayList<>();

        if (seen.contains(beanClass)) {
            schema.put("properties", properties);
            return schema; // 循环引用：截断
        }
        seen.add(beanClass);
        try {
            JavaType javaType = mapper.getTypeFactory().constructType(beanClass);
            BeanDescription desc = mapper.getSerializationConfig().introspect(javaType);
            for (BeanPropertyDefinition prop : desc.findProperties()) {
                if (prop.getPrimaryMember() != null
                        && prop.getPrimaryMember().getAnnotation(JsonIgnore.class) != null) {
                    continue;
                }
                String name = prop.getName();
                if (prop.getPrimaryMember() != null) {
                    JsonProperty jp = prop.getPrimaryMember().getAnnotation(JsonProperty.class);
                    if (jp != null && !jp.value().isEmpty()) {
                        name = jp.value();
                    }
                }
                Map<String, Object> propSchema = typeSchema(reflectType(prop.getPrimaryType()), seen);
                if (prop.isRequired()) {
                    required.add(name);
                }
                properties.put(name, propSchema);
            }
        } finally {
            seen.remove(beanClass);
        }
        schema.put("properties", properties);
        if (!required.isEmpty()) {
            schema.put("required", required);
        }
        return schema;
    }

    private static boolean isOptional(Type type) {
        return type instanceof ParameterizedType
                && ((ParameterizedType) type).getRawType() == Optional.class;
    }

    /**
     * Jackson JavaType → java.lang.reflect.Type（尽力保留 List/Map/Optional 泛型）。
     * BeanDescription 的属性类型是 JavaType，而 typeSchema 基于 reflect 类型系统。
     */
    private static Type reflectType(JavaType jt) {
        if (jt == null) {
            return null;
        }
        Class<?> raw = jt.getRawClass();
        if (List.class.isAssignableFrom(raw) && jt.getContentType() != null) {
            return new ParameterizedTypeImpl(List.class,
                    new Type[]{reflectType(jt.getContentType())});
        }
        if (Map.class.isAssignableFrom(raw) && jt.getKeyType() != null
                && jt.getContentType() != null) {
            return new ParameterizedTypeImpl(Map.class,
                    new Type[]{reflectType(jt.getKeyType()), reflectType(jt.getContentType())});
        }
        if (Optional.class.isAssignableFrom(raw) && jt.getContentType() != null) {
            return new ParameterizedTypeImpl(Optional.class,
                    new Type[]{reflectType(jt.getContentType())});
        }
        return raw;
    }

    /** 最小 ParameterizedType 实现（JDK 无公开构造）。 */
    private static final class ParameterizedTypeImpl implements ParameterizedType {
        private final Class<?> raw;
        private final Type[] args;

        ParameterizedTypeImpl(Class<?> raw, Type[] args) {
            this.raw = raw;
            this.args = args;
        }

        @Override
        public Type[] getActualTypeArguments() {
            return args;
        }

        @Override
        public Type getRawType() {
            return raw;
        }

        @Override
        public Type getOwnerType() {
            return null;
        }
    }

    private static Class<?> rawClass(Type type) {
        if (type instanceof Class) {
            return (Class<?>) type;
        }
        if (type instanceof ParameterizedType) {
            Type raw = ((ParameterizedType) type).getRawType();
            return raw instanceof Class ? (Class<?>) raw : null;
        }
        return null;
    }

    private static Map<String, Object> mapOf(String k, Object v) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put(k, v);
        return m;
    }

    private static Map<String, Object> emptySchema() {
        return new LinkedHashMap<>();
    }
}
