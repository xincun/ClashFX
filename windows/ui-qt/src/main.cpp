#include "DaemonClient.h"
#include "I18n.h"
#include "TrayController.h"
#include "TrayIconGenerator.h"

#include <QApplication>
#include <QLocalSocket>
#include <QStyleFactory>
#include <QTimer>

namespace {

bool forwardToExistingInstance(const QString& serverName, const QString& payload) {
    QLocalSocket sock;
    sock.connectToServer(serverName);
    if (!sock.waitForConnected(450)) {
        return false;
    }
    sock.write(payload.toUtf8());
    sock.flush();
    sock.waitForBytesWritten(350);
    sock.disconnectFromServer();
    return true;
}

} // namespace

int main(int argc, char* argv[]) {
    QApplication app(argc, argv);
    QApplication::setApplicationName(QStringLiteral("ClashFX"));
    QApplication::setOrganizationName(QStringLiteral("ClashFX"));
    QApplication::setApplicationVersion(QStringLiteral("1.19.24-win.1"));
    QApplication::setQuitOnLastWindowClosed(false);
    QApplication::setWindowIcon(clashfx::TrayIconGenerator::appIcon());

    if (QStyleFactory::keys().contains(QStringLiteral("windowsvista"), Qt::CaseInsensitive)) {
        QApplication::setStyle(QStringLiteral("windowsvista"));
    } else if (QStyleFactory::keys().contains(QStringLiteral("Fusion"), Qt::CaseInsensitive)) {
        QApplication::setStyle(QStringLiteral("Fusion"));
    }

    // Touch singleton so QTranslator is installed on qApp immediately
    (void)clashfx::I18n::instance();

    const QStringList args = QCoreApplication::arguments();
    QString customDaemonPipe;
    QString customUiPipe;
    QString customDataDir;
    QString startupUri;
    QString startupCmd;
    bool backgroundStart = false;

    for (int i = 1; i < args.size(); ++i) {
        const QString& a = args.at(i);
        if (a == QStringLiteral("--pipe") && i + 1 < args.size()) {
            customDaemonPipe = args.at(++i);
        } else if (a == QStringLiteral("--ui-pipe") && i + 1 < args.size()) {
            customUiPipe = args.at(++i);
        } else if (a == QStringLiteral("--data-dir") && i + 1 < args.size()) {
            customDataDir = args.at(++i);
        } else if (a == QStringLiteral("--background") || a == QStringLiteral("--minimized")) {
            backgroundStart = true;
        } else if (a.startsWith(QStringLiteral("clash://"), Qt::CaseInsensitive) ||
                   a.startsWith(QStringLiteral("clashfx://"), Qt::CaseInsensitive)) {
            startupUri = a;
        } else if (a == QStringLiteral("--open-console") ||
                   a == QStringLiteral("--open-ai-lock") ||
                   a == QStringLiteral("--open-multi-node") ||
                   a == QStringLiteral("--open-rules") ||
                   a == QStringLiteral("--open-active-rules") ||
                   a == QStringLiteral("--open-subscriptions") ||
                   a == QStringLiteral("--open-diagnostics") ||
                   a == QStringLiteral("--open-settings") ||
                   a == QStringLiteral("--quit")) {
            startupCmd = QStringLiteral("CMD:") + a;
        }
    }

    const QString uiServerName = customUiPipe.trimmed().isEmpty()
                                     ? (QStringLiteral("ClashFXUI-") + clashfx::DaemonClient::sanitizedUsername())
                                     : customUiPipe.trimmed();

    QString forwardMsg = QStringLiteral("ACTIVATE");
    if (!startupUri.isEmpty()) {
        forwardMsg = QStringLiteral("URI:") + startupUri;
    } else if (!startupCmd.isEmpty()) {
        forwardMsg = startupCmd;
    }

    if (forwardToExistingInstance(uiServerName, forwardMsg)) {
        return 0;
    }
    if (startupCmd == QStringLiteral("CMD:--quit")) {
        return 0;
    }

    auto* client = new clashfx::DaemonClient(customDaemonPipe, customDataDir, &app);
    clashfx::TrayController controller(client, uiServerName, !backgroundStart, &app);
    controller.startSingleInstanceServer();
    client->start();

    if (!startupUri.isEmpty()) {
        QTimer::singleShot(700, &controller, [&controller, startupUri]() {
            controller.handleExternalCommand(QStringLiteral("URI:") + startupUri);
        });
    }
    if (!startupCmd.isEmpty()) {
        QTimer::singleShot(500, &controller, [&controller, startupCmd]() {
            controller.handleExternalCommand(startupCmd);
        });
    }

    return app.exec();
}
