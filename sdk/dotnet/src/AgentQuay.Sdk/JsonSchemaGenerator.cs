using System.Collections;
using System.Reflection;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace AgentQuay;

/// <summary>
/// 从方法签名与类型注解生成 JSON Schema（设计文档 §4.2 "System.Text.Json 生成 JSON Schema"）。
///
/// <para>支持：基本类型、string、char、Guid、DateTime、enum、数组、List/Set/集合、
/// IDictionary、可空类型（T? / Nullable&lt;T&gt;）、嵌套 POJO，以及
/// System.Text.Json 注解（[JsonPropertyName] 重命名 / [JsonIgnore] 忽略）。</para>
/// </summary>
public sealed class JsonSchemaGenerator
{
    /// <summary>
    /// 生成方法参数的 inputSchema：{"type":"object","properties":{...},"required":[...]}。
    /// </summary>
    /// <remarks>CancellationToken 参数不进入 schema（SDK 调用时自动注入 None）。</remarks>
    public JsonObject ParamSchema(MethodInfo method)
    {
        var properties = new JsonObject();
        var required = new List<string>();

        int index = 0;
        foreach (ParameterInfo param in method.GetParameters())
        {
            if (param.ParameterType == typeof(CancellationToken) ||
                param.ParameterType == typeof(System.Threading.CancellationToken?))
            {
                continue; // 基础设施参数：不入 schema
            }
            var ap = param.GetCustomAttribute<AgentParamAttribute>();
            bool markedRequired = ap?.Required ?? true;
            bool explicitOptional = IsNullableLike(param);

            string propName = param.Name ?? "arg" + index;
            // 编译器生成参数名 / 未保留时回退 "argN"（调用端按位置绑定）
            if (propName.StartsWith("arg", StringComparison.Ordinal))
            {
                propName = "arg" + index;
            }
            index++;

            var schema = TypeSchema(param.ParameterType);
            if (ap?.Description is { Length: > 0 } desc)
            {
                schema["description"] = desc;
            }
            // 值类型可空（Nullable<T>）已在 TypeSchema 合并；
            // 引用类型可空标注（string? 等）在此合并为 ["string","null"]
            if (explicitOptional && schema["type"] is JsonValue)
            {
                MarkNullable(schema);
            }
            properties[propName] = schema;

            if (markedRequired && !explicitOptional)
            {
                required.Add(propName);
            }
        }

        var root = new JsonObject { ["type"] = "object", ["properties"] = properties };
        if (required.Count > 0)
        {
            root["required"] = new JsonArray(required.Select(r => (JsonNode)r).ToArray());
        }
        return root;
    }

    /// <summary>类型 → JSON Schema（可空类型剥壳并标记可空）。</summary>
    public JsonObject TypeSchema(Type type) => TypeSchema(type, new HashSet<Type>());

    private JsonObject TypeSchema(Type type, HashSet<Type> seen)
    {
        // Nullable<T> → 剥壳 + 可空
        if (Nullable.GetUnderlyingType(type) is { } underlying)
        {
            return MarkNullable(TypeSchema(underlying, seen));
        }

        if (type == typeof(string) || type == typeof(char))
        {
            return Simple("string");
        }
        if (type == typeof(Guid))
        {
            return new JsonObject { ["type"] = "string", ["format"] = "uuid" };
        }
        if (type == typeof(DateTime) || type == typeof(DateTimeOffset))
        {
            return new JsonObject { ["type"] = "string", ["format"] = "date-time" };
        }
        if (type == typeof(byte[]))
        {
            return new JsonObject { ["type"] = "string", ["contentEncoding"] = "base64" };
        }
        if (IsIntegral(type))
        {
            return Simple("integer");
        }
        if (IsNumber(type))
        {
            return Simple("number");
        }
        if (type == typeof(bool))
        {
            return Simple("boolean");
        }
        if (type.IsEnum)
        {
            var values = new JsonArray(
                Enum.GetNames(type).Select(name => (JsonNode)name).ToArray());
            return new JsonObject { ["enum"] = values };
        }
        if (type.IsArray)
        {
            return new JsonObject
            {
                ["type"] = "array",
                ["items"] = TypeSchema(type.GetElementType()!, seen),
            };
        }
        if (IsDictionary(type))
        {
            var valueType = DictionaryValueType(type);
            return new JsonObject
            {
                ["type"] = "object",
                ["additionalProperties"] = valueType != null ? TypeSchema(valueType, seen) : new JsonObject(),
            };
        }
        if (IsCollection(type))
        {
            var itemType = CollectionItemType(type);
            var schema = new JsonObject
            {
                ["type"] = "array",
                ["items"] = itemType != null ? TypeSchema(itemType, seen) : new JsonObject(),
            };
            if (typeof(HashSet<>).IsAssignableFrom(GetRawType(type) ?? type)
                || type.Name.StartsWith("ISet", StringComparison.Ordinal)
                || type.Name.StartsWith("HashSet", StringComparison.Ordinal))
            {
                schema["uniqueItems"] = true;
            }
            return schema;
        }
        if (type == typeof(object) || type == typeof(JsonNode) || type == typeof(JsonValue))
        {
            return new JsonObject(); // 任意值
        }
        // 嵌套 POJO（含内置可枚举的类型兜底为 object）
        if (type.IsClass || type.IsValueType)
        {
            return BeanSchema(type, seen);
        }
        return new JsonObject();
    }

    private JsonObject BeanSchema(Type beanClass, HashSet<Type> seen)
    {
        var schema = new JsonObject { ["type"] = "object" };
        var properties = new JsonObject();

        if (seen.Contains(beanClass))
        {
            schema["properties"] = properties;
            return schema; // 循环引用：截断
        }
        seen.Add(beanClass);
        try
        {
            foreach (PropertyInfo prop in beanClass.GetProperties(
                         BindingFlags.Public | BindingFlags.Instance))
            {
                if (prop.GetIndexParameters().Length > 0 || !prop.CanRead)
                {
                    continue;
                }
                var ignore = prop.GetCustomAttribute<JsonIgnoreAttribute>();
                if (ignore != null && ignore.Condition == JsonIgnoreCondition.Always)
                {
                    continue;
                }
                string name = prop.GetCustomAttribute<JsonPropertyNameAttribute>()?.Name
                              ?? ToCamelCase(prop.Name);
                properties[name] = TypeSchema(prop.PropertyType, seen);
            }
        }
        finally
        {
            seen.Remove(beanClass);
        }
        schema["properties"] = properties;
        return schema;
    }

    // ------------------------------------------------------------------
    // 工具方法
    // ------------------------------------------------------------------

    private static JsonObject MarkNullable(JsonObject inner)
    {
        if (inner["type"] is JsonValue t && t.TryGetValue<string>(out var typeName))
        {
            inner["type"] = new JsonArray(typeName, "null");
        }
        else if (inner["type"] is null)
        {
            inner["nullable"] = true;
        }
        return inner;
    }

    private static bool IsNullableLike(ParameterInfo param)
    {
        // 1) Nullable<T>（值类型可空）
        if (Nullable.GetUnderlyingType(param.ParameterType) != null)
        {
            return true;
        }
        // 2) 引用类型显式可空标注（string? 等，.NET 6+ NullabilityInfoContext）
        try
        {
            var info = new NullabilityInfoContext().Create(param);
            return info.ReadState == NullabilityState.Nullable;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private static bool IsIntegral(Type type) => type == typeof(byte) || type == typeof(sbyte)
        || type == typeof(short) || type == typeof(ushort) || type == typeof(int)
        || type == typeof(uint) || type == typeof(long) || type == typeof(ulong);

    private static bool IsNumber(Type type) => type == typeof(float) || type == typeof(double)
        || type == typeof(decimal);

    private static bool IsDictionary(Type type)
    {
        if (!type.IsGenericType)
        {
            return false;
        }
        foreach (Type iface in type.GetInterfaces())
        {
            if (iface.IsGenericType &&
                (iface.GetGenericTypeDefinition() == typeof(IDictionary<,>)
                 || iface.GetGenericTypeDefinition() == typeof(IReadOnlyDictionary<,>)))
            {
                return true;
            }
        }
        var def = type.GetGenericTypeDefinition();
        return def == typeof(IDictionary<,>) || def == typeof(IReadOnlyDictionary<,>)
               || def == typeof(Dictionary<,>);
    }

    private static bool IsCollection(Type type)
    {
        if (!type.IsGenericType)
        {
            return false;
        }
        if (IsDictionary(type))
        {
            return false;
        }
        foreach (Type iface in type.GetInterfaces())
        {
            // 注意：开放泛型接口（IEnumerable<>）对 List<string> 等封闭类型
            // IsAssignableFrom 返回 false，必须扫描已实现接口
            if (iface.IsGenericType && iface.GetGenericTypeDefinition() == typeof(IEnumerable<>))
            {
                return true;
            }
        }
        // 参数本身就是 IEnumerable<T> 接口
        return type.GetGenericTypeDefinition() == typeof(IEnumerable<>);
    }

    private static Type? DictionaryValueType(Type type)
    {
        foreach (Type iface in type.GetInterfaces())
        {
            if (iface.IsGenericType &&
                (iface.GetGenericTypeDefinition() == typeof(IDictionary<,>)
                 || iface.GetGenericTypeDefinition() == typeof(IReadOnlyDictionary<,>)))
            {
                return iface.GetGenericArguments()[1];
            }
        }
        if (type.IsGenericType)
        {
            var def = type.GetGenericTypeDefinition();
            if (def == typeof(Dictionary<,>) || def == typeof(IDictionary<,>)
                || def == typeof(IReadOnlyDictionary<,>))
            {
                return type.GetGenericArguments()[1];
            }
        }
        return null;
    }

    private static Type? CollectionItemType(Type type)
    {
        foreach (Type iface in type.GetInterfaces())
        {
            if (iface.IsGenericType && iface.GetGenericTypeDefinition() == typeof(IEnumerable<>))
            {
                return iface.GetGenericArguments()[0];
            }
        }
        if (type.IsGenericType && type.GetGenericTypeDefinition() is { } def
                                && def != typeof(IDictionary<,>)
                                && def != typeof(IReadOnlyDictionary<,>))
        {
            return type.GetGenericArguments()[0];
        }
        return null;
    }

    private static Type? GetRawType(Type type) => type.IsGenericType ? type.GetGenericTypeDefinition() : null;

    private static JsonObject Simple(string type) => new() { ["type"] = type };

    private static string ToCamelCase(string name)
        => name.Length > 1 && char.IsUpper(name[0])
            ? char.ToLowerInvariant(name[0]) + name[1..]
            : name;
}