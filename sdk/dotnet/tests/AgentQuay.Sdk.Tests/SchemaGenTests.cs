using System.Reflection;
using System.Text.Json.Nodes;
using AgentQuay;
using Xunit;

namespace AgentQuay.Tests;

/// <summary>
/// JSON Schema 生成单测：基本类型、enum、集合、可空、POJO（设计文档 §4.2 "System.Text.Json 生成"）。
/// </summary>
public class SchemaGenTests
{
    private static readonly JsonSchemaGenerator Gen = new();

    public enum TestEnum { Alpha, Beta, Gamma }

    public sealed class Song
    {
        public string? Id { get; set; }
        public string? Title { get; set; }
        public int Duration { get; set; }
    }

    public sealed class Controller
    {
        public void Plain(string keyword, int count, double ratio, bool enable) { }

        public void WithEnums(TestEnum mode) { }

        public void WithCollections(List<string> names, HashSet<int> ids, Dictionary<string, int> counts) { }

        public void WithNullable(int? maybe, string? maybeText, [AgentParam(Required = false)] string? opt) { }

        public void WithPoco(Song nested) { }

        public void WithCancellation(CancellationToken ct, string keyword) { }
    }

    private JsonObject SchemaFor(string methodName)
    {
        var method = typeof(Controller).GetMethod(methodName)
                     ?? throw new InvalidOperationException("方法不存在: " + methodName);
        return Gen.ParamSchema(method);
    }

    [Fact]
    public void BasicTypes()
    {
        var schema = SchemaFor(nameof(Controller.Plain));
        Assert.Equal("object", schema["type"]!.GetValue<string>());
        Assert.Equal("string", TypeOf(schema, "keyword"));
        Assert.Equal("integer", TypeOf(schema, "count"));
        Assert.Equal("number", TypeOf(schema, "ratio"));
        Assert.Equal("boolean", TypeOf(schema, "enable"));
        var required = Required(schema);
        Assert.Equal(new[] { "count", "enable", "keyword", "ratio" }, required.OrderBy(x => x));
    }

    [Fact]
    public void EnumBecomesEnumValues()
    {
        var schema = SchemaFor(nameof(Controller.WithEnums));
        var values = schema["properties"]!["mode"]!["enum"]!.AsArray()
            .Select(n => n!.GetValue<string>()).ToList();
        Assert.Equal(new[] { "Alpha", "Beta", "Gamma" }, values);
    }

    [Fact]
    public void Collections()
    {
        var schema = SchemaFor(nameof(Controller.WithCollections));
        Assert.Equal("array", TypeOf(schema, "names"));
        Assert.Equal("string", schema["properties"]!["names"]!["items"]!["type"]!.GetValue<string>());
        Assert.True(schema["properties"]!["ids"]!["uniqueItems"]!.GetValue<bool>(), "HashSet 应标记 uniqueItems");
        Assert.Equal("object", TypeOf(schema, "counts"));
        Assert.Equal("integer", schema["properties"]!["counts"]!["additionalProperties"]!["type"]!.GetValue<string>());
    }

    [Fact]
    public void NullableTypesAreOptionalAndMergedNull()
    {
        var schema = SchemaFor(nameof(Controller.WithNullable));
        Assert.Equal(new[] { "integer", "null" },
            schema["properties"]!["maybe"]!["type"]!.AsArray().Select(n => n!.GetValue<string>()));
        // T? / string? / [AgentParam(Required=false)] 均不进 required
        Assert.Equal(Array.Empty<string>(), Required(schema));
        string?[] maybeTextTypes = schema["properties"]!["maybeText"]!["type"]!.AsArray()
            .Select(n => n!.GetValue<string>()).ToArray();
        Assert.Equal(new[] { "string", "null" }, maybeTextTypes);
        string?[] optTypes = schema["properties"]!["opt"]!["type"]!.AsArray()
            .Select(n => n!.GetValue<string>()).ToArray();
        Assert.Equal(new[] { "string", "null" }, optTypes);
    }

    [Fact]
    public void PocoBecomesObjectWithCamelCaseProps()
    {
        var schema = SchemaFor(nameof(Controller.WithPoco));
        var props = schema["properties"]!["nested"]!["properties"]!.AsObject();
        Assert.Equal("object", schema["properties"]!["nested"]!["type"]!.GetValue<string>());
        Assert.Equal("string", props["id"]!["type"]!.GetValue<string>());
        Assert.Equal("string", props["title"]!["type"]!.GetValue<string>());
        Assert.Equal("integer", props["duration"]!["type"]!.GetValue<string>());
    }

    [Fact]
    public void CancellationTokenIsSkipped()
    {
        var schema = SchemaFor(nameof(Controller.WithCancellation));
        var props = schema["properties"]!.AsObject();
        Assert.False(props.ContainsKey("ct"), "CancellationToken 不应进入 schema");
        Assert.True(props.ContainsKey("keyword"));
        Assert.Equal(new[] { "keyword" }, Required(schema));
    }

    [Fact]
    public void DescriptionFromAgentParamIsOptionalByDefault()
    {
        // 未标注 [AgentParam] 的参数：无 description，属性默认存在
        var schema = SchemaFor(nameof(Controller.Plain));
        Assert.NotNull(schema["properties"]!["keyword"]);
    }

    // ------------------------------------------------------------------

    private static string TypeOf(JsonObject schema, string prop)
        => schema["properties"]![prop]!["type"]!.GetValue<string>();

    private static List<string> Required(JsonObject schema)
        => schema["required"]?.AsArray().Select(n => n!.GetValue<string>()).ToList() ?? new List<string>();
}