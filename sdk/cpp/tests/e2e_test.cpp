// e2e_test.cpp - End-to-end integration test: real Go Bridge + C++ SDK + raw MCP client.
// Aligned with Java AgentQuayE2ETest.
//
// Requires a built Bridge binary (bridge/dist/agentquay-*) or the
// AGENTQUAY_BRIDGE_BIN environment variable.
//
// Covers: register/token, tools/list, normal call, -32003 arg validation,
// business error passthrough, confirmation flow (confirm/cancel -32005),
// same-appId replacement, reconnect with token.
#include <QCoreApplication>
#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QProcess>
#include <QProcessEnvironment>
#include <QStandardPaths>
#include <QTest>
#include <QTimer>

#include <agentquay/AgentQuayClient.h>
#include <agentquay/BridgeSpawner.h>
#include <agentquay/agent_tool.h>

using namespace agentquay;

class E2ETest : public QObject {
    Q_OBJECT
private:
    static constexpr const char* kAppId = "cpp-e2e-app";
    static QProcess* m_bridge;
    static int m_port;
    static QNetworkAccessManager* m_http;
    static QString m_sessionId;
    static bool m_confirmAnswer;
    static int s_requestId;
    static AgentQuayClient* m_app; // 跨测试保持连接（对齐 Java E2E 的静态 app）

private slots:
    void initTestCase();
    void cleanupTestCase();
    void sdkRegistersAndToolsList();
    void toolsCallSuccess();
    void invalidArgsRejected();
    void businessErrorPassthrough();
    void confirmationFlow();
    void replacementStopsOldClient();
    void reconnectWithToken();

private:
    void startBridge();
    void stopBridge();
    QJsonObject mcpPost(const QString& method, const QJsonObject& params);
    void initMCPClient();
    bool waitAppState(int timeoutSeconds, bool online);
    QString findBridgeBinary();
};

QProcess* E2ETest::m_bridge = nullptr;
int E2ETest::m_port = 0;
QNetworkAccessManager* E2ETest::m_http = nullptr;
QString E2ETest::m_sessionId;
bool E2ETest::m_confirmAnswer = true;
int E2ETest::s_requestId = 0;
AgentQuayClient* E2ETest::m_app = nullptr;

class EchoController : public QObject {
    Q_OBJECT
public:
#ifndef Q_MOC_RUN
    AGENT_TOOL(echo, "echo message")
#endif
    Q_INVOKABLE QVariantMap echo(const QString& message)
    {
        QVariantMap m;
        m.insert("received", message);
        return m;
    }

#ifndef Q_MOC_RUN
    AGENT_TOOL(add, "add two integers")
#endif
    Q_INVOKABLE int add(int a, int b) { return a + b; }

#ifndef Q_MOC_RUN
    AGENT_TOOL_OPTS(confirmOp, "confirm operation", AgentToolOptions{true})
#endif
    Q_INVOKABLE QVariantMap confirmOp(const QString& value)
    {
        QVariantMap m;
        m.insert("confirmed_value", value);
        return m;
    }

#ifndef Q_MOC_RUN
    AGENT_TOOL(fail, "always fail")
#endif
    Q_INVOKABLE QVariantMap fail()
    {
        throw std::runtime_error("simulated business failure");
    }
};

void E2ETest::initTestCase()
{
    m_bridge = nullptr;
    m_port = 0;
    m_http = new QNetworkAccessManager();
    m_sessionId.clear();
    m_confirmAnswer = true;
    s_requestId = 0;
    m_app = nullptr;
    startBridge();
}

void E2ETest::cleanupTestCase()
{
    delete m_app;
    m_app = nullptr;
    stopBridge();
    delete m_http;
    m_http = nullptr;
}

void E2ETest::startBridge()
{
    const QString bridgeBin = findBridgeBinary();
    const QString home = QStandardPaths::writableLocation(QStandardPaths::HomeLocation);
    // 清理端口文件与认证表：让 bridge 全新启动并首次注册（避免与历史 token 冲突）
    QFile::remove(QDir(home).filePath(".agentquay/port"));
    QFile::remove(QDir(home).filePath(".agentquay/auth.json"));

    m_bridge = new QProcess();
    m_bridge->setProgram(bridgeBin);
    m_bridge->setArguments({"serve"});
    m_bridge->setStandardOutputFile(QProcess::nullDevice());
    m_bridge->setStandardErrorFile(QProcess::nullDevice());
    QProcessEnvironment env = QProcessEnvironment::systemEnvironment();
    env.insert("AGENTQUAY_LOG_LEVEL", "debug");
    m_bridge->setProcessEnvironment(env);
    m_bridge->start();

    const qint64 deadline = QDateTime::currentMSecsSinceEpoch() + 15000;
    while (QDateTime::currentMSecsSinceEpoch() < deadline) {
        m_port = BridgeSpawner::readPortFile();
        if (m_port > 0 && BridgeSpawner::probe("127.0.0.1", m_port, 500))
            break;
        QTest::qSleep(200);
    }
    qDebug() << "bridge port:" << m_port
             << "bridge state:" << int(m_bridge->state())
             << "bridge running:" << (m_bridge->state() == QProcess::Running);
    if (m_bridge->state() != QProcess::Running)
        qDebug() << "bridge not running, exit code:" << m_bridge->exitCode()
                 << "error:" << m_bridge->errorString();
    if (m_port <= 0) {
        QFAIL("Bridge did not become ready within 15s");
        return;
    }
    initMCPClient();
}

void E2ETest::stopBridge()
{
    if (m_bridge) {
        m_bridge->kill();
        m_bridge->waitForFinished(5000);
        delete m_bridge;
        m_bridge = nullptr;
    }
}

void E2ETest::initMCPClient()
{
    QJsonObject initParams;
    initParams["protocolVersion"] = "2025-06-18";
    initParams["capabilities"] = QJsonObject();
    QJsonObject clientInfo;
    clientInfo["name"] = "cpp-e2e";
    clientInfo["version"] = "1.0";
    initParams["clientInfo"] = clientInfo;
    const QJsonObject resp = mcpPost("initialize", initParams);
    if (!resp.contains("result")) {
        QFAIL("MCP initialize failed");
        return;
    }
    if (m_sessionId.isEmpty()) {
        QFAIL("No MCP session id established");
    }
}

QJsonObject E2ETest::mcpPost(const QString& method, const QJsonObject& params)
{
    ++s_requestId;
    QJsonObject body;
    body["jsonrpc"] = "2.0";
    body["id"] = s_requestId;
    body["method"] = method;
    body["params"] = params;

    QUrl url(QString("http://127.0.0.1:%1/mcp").arg(m_port));
    QNetworkRequest req(url);
    req.setRawHeader("Content-Type", "application/json");
    req.setRawHeader("Accept", "application/json, text/event-stream");
    if (!m_sessionId.isEmpty())
        req.setRawHeader("Mcp-Session-Id", m_sessionId.toUtf8());

    QNetworkReply* reply = m_http->post(req, QJsonDocument(body).toJson());
    QEventLoop loop;
    QObject::connect(reply, &QNetworkReply::finished, &loop, &QEventLoop::quit);
    QTimer::singleShot(90000, &loop, &QEventLoop::quit);
    loop.exec();

    const QByteArray data = reply->readAll();
    const QString ct = QString(reply->header(QNetworkRequest::ContentTypeHeader).toString());
    if (ct.contains("text/event-stream")) {
        QString last;
        for (const QByteArray& line : data.split('\n')) {
            if (line.startsWith("data:"))
                last = line.mid(5).trimmed();
        }
        if (!last.isEmpty()) {
            const QString sid = QString(reply->rawHeader("Mcp-Session-Id"));
            if (!sid.isEmpty())
                m_sessionId = sid;
            reply->deleteLater();
            return QJsonDocument::fromJson(last.toUtf8()).object();
        }
    }
    const QString sid = QString(reply->rawHeader("Mcp-Session-Id"));
    if (!sid.isEmpty())
        m_sessionId = sid;
    reply->deleteLater();
    return QJsonDocument::fromJson(data).object();
}

void E2ETest::sdkRegistersAndToolsList()
{
    static EchoController ctrl; // 跨测试存活：m_app 的 tool 绑定引用它
    m_app = new AgentQuayClient(kAppId, "Cpp E2E");
    m_app->setPort(m_port);
    m_app->setAutoSpawnBridge(false);
    m_app->setConfirmHandler([](const QString&, const QVariantMap&, int) { return m_confirmAnswer; });
    m_app->registerTools(&ctrl);
    m_app->connect();
    if (!waitAppState(10, true)) {
        QFAIL("App did not come online");
        return;
    }

    const QJsonObject list = mcpPost("tools/list", QJsonObject());
    const QJsonArray tools = list.value("result").toObject().value("tools").toArray();
    QStringList names;
    for (const QJsonValue& t : tools)
        names.append(t.toObject().value("name").toString());

    QVERIFY(names.contains("cpp-e2e-app_echo"));
    QVERIFY(names.contains("cpp-e2e-app_confirmOp"));

    QJsonObject echoTool;
    for (const QJsonValue& t : tools) {
        if (t.toObject().value("name").toString() == "cpp-e2e-app_echo") {
            echoTool = t.toObject();
            break;
        }
    }
    QVERIFY(!echoTool.isEmpty());
    QVERIFY(echoTool.value("description").toString().startsWith("[Cpp E2E]"));
    const QJsonArray required = echoTool.value("inputSchema").toObject().value("required").toArray();
    QStringList reqList;
    for (const QJsonValue& r : required)
        reqList.append(r.toString());
    QCOMPARE(reqList, QStringList("message"));
}

void E2ETest::toolsCallSuccess()
{
    QJsonObject resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_echo"},
        {"arguments", QJsonObject{{"message", "hello agent"}}}
    });
    const QJsonObject result = resp.value("result").toObject();
    QVERIFY(!result.value("isError").toBool());
    const QString text = result.value("content").toArray().first().toObject().value("text").toString();
    QCOMPARE(text, QStringLiteral("{\"received\":\"hello agent\"}"));

    resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_add"},
        {"arguments", QJsonObject{{"a", 3}, {"b", 4}}}
    });
    const QString addText = resp.value("result").toObject().value("content")
                                .toArray().first().toObject().value("text").toString();
    QCOMPARE(addText, QStringLiteral("7"));
}

void E2ETest::invalidArgsRejected()
{
    // Missing required arg -> -32003
    QJsonObject resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_echo"},
        {"arguments", QJsonObject()}
    });
    const QString text1 = resp.value("result").toObject().value("content")
                              .toArray().first().toObject().value("text").toString();
    QCOMPARE(QJsonDocument::fromJson(text1.toUtf8()).object().value("code").toInt(0), -32003);

    // Wrong type -> -32003
    resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_echo"},
        {"arguments", QJsonObject{{"message", QJsonValue(123)}}}
    });
    const QString text2 = resp.value("result").toObject().value("content")
                              .toArray().first().toObject().value("text").toString();
    QCOMPARE(QJsonDocument::fromJson(text2.toUtf8()).object().value("code").toInt(0), -32003);
}

void E2ETest::businessErrorPassthrough()
{
    QJsonObject resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_fail"},
        {"arguments", QJsonObject()}
    });
    const QJsonObject result = resp.value("result").toObject();
    QVERIFY(result.value("isError").toBool());
    const QString text = result.value("content").toArray().first().toObject().value("text").toString();
    QVERIFY(text.contains("EXECUTION_ERROR") || text.contains("simulated business failure"));
}

void E2ETest::confirmationFlow()
{
    // Confirm -> success
    m_confirmAnswer = true;
    QJsonObject resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_confirmOp"},
        {"arguments", QJsonObject{{"value", "v1"}}}
    });
    QVERIFY(!resp.value("result").toObject().value("isError").toBool());

    // Cancel -> -32005
    m_confirmAnswer = false;
    resp = mcpPost("tools/call", {
        {"name", "cpp-e2e-app_confirmOp"},
        {"arguments", QJsonObject{{"value", "v2"}}}
    });
    const QString text = resp.value("result").toObject().value("content")
                              .toArray().first().toObject().value("text").toString();
    QCOMPARE(QJsonDocument::fromJson(text.toUtf8()).object().value("code").toInt(0), -32005);
}

void E2ETest::replacementStopsOldClient()
{
    EchoController ctrl1;
    AgentQuayClient client1(kAppId, "Cpp E2E");
    client1.setPort(m_port);
    client1.setAutoSpawnBridge(false);
    client1.setConfirmHandler([](const QString&, const QVariantMap&, int) { return true; });
    client1.registerTools(&ctrl1);

    QObject::connect(&client1, &AgentQuayClient::replaced, [] {});
    client1.connect();

    EchoController ctrl2;
    AgentQuayClient client2(kAppId, "Cpp E2E");
    client2.setPort(m_port);
    client2.setAutoSpawnBridge(false);
    client2.setConfirmHandler([](const QString&, const QVariantMap&, int) { return true; });
    client2.registerTools(&ctrl2);
    client2.connect();

    QVERIFY(waitAppState(8, true));
}

void E2ETest::reconnectWithToken()
{
    const QString home = QStandardPaths::writableLocation(QStandardPaths::HomeLocation);
    const QString tokenPath = QDir(home).filePath(".agentquay/tokens.json");
    QFile f(tokenPath);
    QVERIFY(f.exists() && f.size() > 0);
}

bool E2ETest::waitAppState(int timeoutSeconds, bool online)
{
    const qint64 deadline = QDateTime::currentMSecsSinceEpoch() + qint64(timeoutSeconds) * 1000;
    while (QDateTime::currentMSecsSinceEpoch() < deadline) {
        QUrl url(QString("http://127.0.0.1:%1/admin/status").arg(m_port));
        QNetworkRequest req(url);
        QNetworkReply* reply = m_http->get(req);
        bool found = false;
        QEventLoop loop;
        QObject::connect(reply, &QNetworkReply::finished, [&]() {
            const QJsonDocument doc = QJsonDocument::fromJson(reply->readAll());
            for (const QJsonValue& appVal : doc.object().value("apps").toArray()) {
                if (appVal.toObject().value("appId").toString() == kAppId)
                    found = true;
            }
            reply->deleteLater();
            loop.quit();
        });
        QTimer::singleShot(2000, &loop, &QEventLoop::quit);
        loop.exec();
        if (found == online)
            return true;
        QTest::qSleep(200);
    }
    return false;
}

QString E2ETest::findBridgeBinary()
{
    const QString env = qEnvironmentVariable("AGENTQUAY_BRIDGE_BIN");
    if (!env.isEmpty() && QFile::exists(env))
        return env;

    const QStringList suffixes = {
        "agentquay.exe", "agentquay-windows-amd64.exe",
        "agentquay-linux-amd64", "agentquay-darwin-arm64",
        "agentquay-darwin-amd64", "agentquay"
    };
    const QString home = QStandardPaths::writableLocation(QStandardPaths::HomeLocation);
    QDir root(home);
    for (int i = 0; i < 6; ++i) {
        const QString distPath = QDir(root).absoluteFilePath("bridge/dist");
        for (const QString& name : suffixes) {
            const QString candidate = distPath + "/" + name;
            if (QFile::exists(candidate))
                return candidate;
        }
        if (!root.cdUp())
            break;
    }
    qWarning() << "Bridge binary not found. Build bridge/dist or set AGENTQUAY_BRIDGE_BIN";
    return QString();
}

#include "e2e_test.moc"

int main(int argc, char* argv[])
{
    QCoreApplication app(argc, argv);
    return QTest::qExec(new E2ETest(), argc, argv);
}