// agentquay/ConfirmationHandler.cpp
#include "agentquay/ConfirmationHandler.h"

#include <QApplication>
#include <QCoreApplication>
#include <QGuiApplication>
#include <QKeyEvent>
#include <QMessageBox>
#include <QAbstractButton>
#include <QTextStream>
#include <QTimer>

namespace agentquay {

namespace {

QString formatMessage(const QString& message, const QVariantMap& arguments)
{
    if (arguments.isEmpty())
        return message;
    QString text = message;
    text += QStringLiteral("\n\nargs:\n");
    for (auto it = arguments.cbegin(); it != arguments.cend(); ++it) {
        text += QStringLiteral("  %1 = %2\n").arg(it.key(), it->toString());
    }
    return text;
}

bool consoleAsk(const QString& message)
{
    QTextStream in(stdin);
    QTextStream out(stdout);
    out << message << Qt::endl;
    while (true) {
        out << "confirm? [y/N]: " << Qt::flush;
        const QString line = in.readLine().trimmed().toLower();
        if (line == QLatin1String("y") || line == QLatin1String("yes"))
            return true;
        if (line.isEmpty() || line == QLatin1String("n") || line == QLatin1String("no"))
            return false;
        out << "please enter y or n" << Qt::endl;
    }
}

#ifdef AGENTQUAY_WITH_WIDGETS
bool widgetAsk(const QString& message, int timeoutSeconds)
{
    QMessageBox box;
    box.setWindowTitle(QStringLiteral("AgentQuay Confirm"));
    box.setIcon(QMessageBox::Question);
    box.setText(message);
    box.setStandardButtons(QMessageBox::Yes | QMessageBox::No);
    box.setDefaultButton(QMessageBox::No);
    if (timeoutSeconds > 0) {
        QTimer timer;
        timer.setSingleShot(true);
        int timeoutMs = timeoutSeconds * 1000;
        QMetaObject::Connection tc;
        tc = QObject::connect(&timer, &QTimer::timeout, [&box, timeoutMs]() {
            QKeyEvent keyPress(QEvent::KeyRelease, Qt::Key_No, Qt::NoModifier);
            QAbstractButton* btn = box.button(QMessageBox::No);
            if (btn) QCoreApplication::sendEvent(btn, &keyPress);
        });
        timer.start(timeoutMs);
        Q_UNUSED(tc)
    }
    return box.exec() == QMessageBox::Yes;
}
#endif

} // namespace

ConfirmHandler defaultConfirmHandler()
{
#ifdef AGENTQUAY_WITH_WIDGETS
    if (qobject_cast<QGuiApplication*>(QCoreApplication::instance())) {
        return [](const QString& message, const QVariantMap& arguments, int timeoutSeconds) {
            return widgetAsk(formatMessage(message, arguments), timeoutSeconds);
        };
    }
#endif
    return [](const QString& message, const QVariantMap& arguments, int) {
        return consoleAsk(formatMessage(message, arguments));
    };
}

} // namespace agentquay
