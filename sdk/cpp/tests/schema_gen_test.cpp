// schema_gen_test.cpp - JSON Schema generation unit tests.
// Covers: primitive type mapping, arrays/maps, registered enums, empty params,
// protocol encode/decode. Does not require a real Bridge.
#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QJsonArray>
#include <QJsonObject>
#include <QMetaMethod>
#include <QStandardPaths>
#include <QTest>

#include <agentquay/BridgeSpawner.h>
#include <agentquay/JsonSchemaGenerator.h>
#include <agentquay/Protocol.h>
#include <agentquay/TokenStore.h>
#include <agentquay/agent_tool.h>

using namespace agentquay;


class TestController : public QObject {
    Q_OBJECT
public:
#ifndef Q_MOC_RUN
    AGENT_TOOL(stringMethod, "string method")
#endif
    Q_INVOKABLE QVariant stringMethod(const QString& input);

#ifndef Q_MOC_RUN
    AGENT_TOOL(numMethod, "numeric method")
#endif
    Q_INVOKABLE int numMethod(int x, double y);

#ifndef Q_MOC_RUN
    AGENT_TOOL(boolMethod, "boolean method")
#endif
    Q_INVOKABLE bool boolMethod(bool flag);

#ifndef Q_MOC_RUN
    AGENT_TOOL(emptyMethod, "empty params")
#endif
    Q_INVOKABLE QVariantMap emptyMethod();

#ifndef Q_MOC_RUN
    AGENT_TOOL(listMethod, "list method")
#endif
    Q_INVOKABLE QStringList listMethod(const QStringList& tags);

#ifndef Q_MOC_RUN
    AGENT_TOOL(mapMethod, "map method")
#endif
    Q_INVOKABLE QVariantMap mapMethod(const QVariantMap& config);

};

// 实现（moc 生成的 qt_static_metacall 需要这些符号；仅用于 schema 测试，不做业务）
QVariant TestController::stringMethod(const QString& input) { Q_UNUSED(input); return QVariant(); }
int TestController::numMethod(int x, double y) { Q_UNUSED(x); Q_UNUSED(y); return 0; }
bool TestController::boolMethod(bool flag) { Q_UNUSED(flag); return false; }
QVariantMap TestController::emptyMethod() { return QVariantMap(); }
QStringList TestController::listMethod(const QStringList& tags) { Q_UNUSED(tags); return QStringList(); }
QVariantMap TestController::mapMethod(const QVariantMap& config) { Q_UNUSED(config); return QVariantMap(); }

class SchemaGenTest : public QObject {
    Q_OBJECT
private slots:
    void stringType();
    void numericTypes();
    void booleanType();
    void stringList();
    void stringMap();
    void emptyParams();
    void protocolEncodeDecode();
    void protocolRegisterPayload();
    void launchInfoSerialization();
    void tokenStoreRoundTrip();
    void bridgeSpawnerPortFile();
};

void SchemaGenTest::stringType()
{
    const TestController ctrl;
    const QMetaObject* mo = ctrl.metaObject();
    const QMetaMethod m = mo->method(mo->indexOfMethod("stringMethod(QString)"));
    const QJsonObject schema = JsonSchemaGenerator::paramSchema(m);
    const QJsonObject props = schema.value("properties").toObject();
    QCOMPARE(props.value("input").toObject().value("type").toString(), QStringLiteral("string"));
    QVERIFY(schema.contains("required"));
}

void SchemaGenTest::numericTypes()
{
    const TestController ctrl;
    const QMetaObject* mo = ctrl.metaObject();
    const QMetaMethod m = mo->method(mo->indexOfMethod("numMethod(int,double)"));
    const QJsonObject schema = JsonSchemaGenerator::paramSchema(m);
    const QJsonObject props = schema.value("properties").toObject();
    QCOMPARE(props.value("x").toObject().value("type").toString(), QStringLiteral("integer"));
    QCOMPARE(props.value("y").toObject().value("type").toString(), QStringLiteral("number"));
}

void SchemaGenTest::booleanType()
{
    const TestController ctrl;
    const QMetaObject* mo = ctrl.metaObject();
    const QMetaMethod m = mo->method(mo->indexOfMethod("boolMethod(bool)"));
    const QJsonObject schema = JsonSchemaGenerator::paramSchema(m);
    const QJsonObject props = schema.value("properties").toObject();
    QCOMPARE(props.value("flag").toObject().value("type").toString(), QStringLiteral("boolean"));
}

void SchemaGenTest::stringList()
{
    const TestController ctrl;
    const QMetaObject* mo = ctrl.metaObject();
    const QMetaMethod m = mo->method(mo->indexOfMethod("listMethod(QStringList)"));
    const QJsonObject schema = JsonSchemaGenerator::paramSchema(m);
    const QJsonObject props = schema.value("properties").toObject();
    QCOMPARE(props.value("tags").toObject().value("type").toString(), QStringLiteral("array"));
}

void SchemaGenTest::stringMap()
{
    const TestController ctrl;
    const QMetaObject* mo = ctrl.metaObject();
    const QMetaMethod m = mo->method(mo->indexOfMethod("mapMethod(QVariantMap)"));
    const QJsonObject schema = JsonSchemaGenerator::paramSchema(m);
    const QJsonObject props = schema.value("properties").toObject();
    QCOMPARE(props.value("config").toObject().value("type").toString(), QStringLiteral("object"));
}

void SchemaGenTest::emptyParams()
{
    const TestController ctrl;
    const QMetaObject* mo = ctrl.metaObject();
    const QMetaMethod m = mo->method(mo->indexOfMethod("emptyMethod()"));
    const QJsonObject schema = JsonSchemaGenerator::paramSchema(m);
    QCOMPARE(schema.value("type").toString(), QStringLiteral("object"));
    QVERIFY(schema.value("properties").toObject().isEmpty());
    QVERIFY(!schema.contains("required"));
}

void SchemaGenTest::protocolEncodeDecode()
{
    const QByteArray raw = encode("ping", pingPayload(1700000000LL));
    QVERIFY(!raw.isEmpty());
    QString type;
    QJsonValue payload;
    QVERIFY(parse(raw, &type, &payload));
    QCOMPARE(type, QString("ping"));
    QCOMPARE(payload.toObject().value("timestamp").toVariant().toLongLong(), 1700000000LL);
}

void SchemaGenTest::protocolRegisterPayload()
{
    QJsonArray tools;
    QJsonObject t1;
    t1["name"] = "search";
    t1["description"] = "search music library";
    t1["inputSchema"] = QJsonObject({{"type", "object"}});
    t1["requiresConfirmation"] = false;
    t1["timeoutSeconds"] = 30;
    t1["confirmTimeoutSeconds"] = 120;
    tools.append(t1);

    const QJsonObject payload = registerPayload(
        "music-app", "Music Player", "1.0.0", "1.0", QString(), tools);
    QCOMPARE(payload.value("appId").toString(), QString("music-app"));
    QCOMPARE(payload.value("tools").toArray().size(), 1);
}

void SchemaGenTest::launchInfoSerialization()
{
    // 缺省 launch：不出现 launch 字段
    const QJsonObject noLaunch = registerPayload(
        "music-app", "Music Player", "1.0.0", "1.0", QString(), QJsonArray());
    QVERIFY(!noLaunch.contains(QStringLiteral("launch")));

    // 自定义启动命令：完整序列化
    LaunchInfo info;
    info.execPath = QStringLiteral("C:/apps/music-app.exe");
    info.args = QStringList{QStringLiteral("--quiet")};
    info.cwd = QStringLiteral("C:/apps");
    info.singleInstance = true;
    info.launchTimeoutSeconds = 20;
    const QJsonObject withLaunch = registerPayload(
        "music-app", "Music Player", "1.0.0", "1.0", QString(), QJsonArray(),
        info.toJson());
    QVERIFY(withLaunch.contains(QStringLiteral("launch")));
    const QJsonObject launch = withLaunch.value(QStringLiteral("launch")).toObject();
    QCOMPARE(launch.value(QStringLiteral("execPath")).toString(), QStringLiteral("C:/apps/music-app.exe"));
    QCOMPARE(launch.value(QStringLiteral("singleInstance")).toBool(), true);
    QCOMPARE(launch.value(QStringLiteral("launchTimeoutSeconds")).toInt(), 20);
    QCOMPARE(launch.value(QStringLiteral("args")).toArray().size(), 1);

    // 自动探测：当前进程存在可执行文件路径（QCoreApplication::applicationFilePath）
    const LaunchInfo detected = detectLaunchInfo();
    QVERIFY(!detected.execPath.isEmpty());
}

void SchemaGenTest::tokenStoreRoundTrip()
{
    // 用独特 appId，不删除共享的 tokens.json（避免影响其他 SDK/测试持久化的 token）
    TokenStore store("schema-token-test");
    store.set("aq_test_token_12345");
    const QString got = store.get();
    QCOMPARE(got, QString("aq_test_token_12345"));
}

void SchemaGenTest::bridgeSpawnerPortFile()
{
    using agentquay::BridgeSpawner;
    // Write a temporary port file and read it back
    const QString home = QStandardPaths::writableLocation(QStandardPaths::HomeLocation);
    const QString portPath = QDir(home).filePath(".agentquay/port");
    QFile::remove(portPath);
    QFile f(portPath);
    if (f.open(QIODevice::WriteOnly)) {
        f.write("19846\n");
        f.close();
    }
    QCOMPARE(BridgeSpawner::readPortFile(), 19846);
    QFile::remove(portPath);
}

#include "schema_gen_test.moc"

int main(int argc, char* argv[])
{
    QCoreApplication app(argc, argv);
    return QTest::qExec(new SchemaGenTest(), argc, argv);
}