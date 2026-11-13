#include "DaemonClient.h"
#include "I18n.h"
#include "TrayController.h"
#include "TrayIconGenerator.h"

#include <QApplication>
#include <QColor>
#include <QLocalSocket>
#include <QPalette>
#include <QStyleFactory>
#include <QTimer>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#endif

namespace {

void applyUnifiedDarkTheme(QApplication& app) {
    if (QStyleFactory::keys().contains(QStringLiteral("Fusion"), Qt::CaseInsensitive)) {
        QApplication::setStyle(QStringLiteral("Fusion"));
    }

    QPalette pal;
    pal.setColor(QPalette::Window, QColor(0x18, 0x18, 0x1b));
    pal.setColor(QPalette::WindowText, QColor(0xf4, 0xf4, 0xf5));
    pal.setColor(QPalette::Base, QColor(0x1f, 0x1f, 0x23));
    pal.setColor(QPalette::AlternateBase, QColor(0x27, 0x27, 0x2a));
    pal.setColor(QPalette::ToolTipBase, QColor(0x27, 0x27, 0x2a));
    pal.setColor(QPalette::ToolTipText, QColor(0xf4, 0xf4, 0xf5));
    pal.setColor(QPalette::Text, QColor(0xf4, 0xf4, 0xf5));
    pal.setColor(QPalette::Button, QColor(0x27, 0x27, 0x2a));
    pal.setColor(QPalette::ButtonText, QColor(0xf4, 0xf4, 0xf5));
    pal.setColor(QPalette::BrightText, QColor(0xff, 0xff, 0xff));
    pal.setColor(QPalette::Link, QColor(0x60, 0xa5, 0xfa));
    pal.setColor(QPalette::Highlight, QColor(0x25, 0x63, 0xeb));
    pal.setColor(QPalette::HighlightedText, QColor(0xff, 0xff, 0xff));
    pal.setColor(QPalette::PlaceholderText, QColor(0x71, 0x71, 0x7a));
    pal.setColor(QPalette::Disabled, QPalette::WindowText, QColor(0x71, 0x71, 0x7a));
    pal.setColor(QPalette::Disabled, QPalette::Text, QColor(0x71, 0x71, 0x7a));
    pal.setColor(QPalette::Disabled, QPalette::ButtonText, QColor(0x71, 0x71, 0x7a));
    app.setPalette(pal);

    app.setStyleSheet(QStringLiteral(
        "QDialog, QMainWindow { background-color: #18181b; color: #f4f4f5; }"
        "QGroupBox { border: 1px solid #2e2e33; border-radius: 6px; margin-top: 10px; padding-top: 8px; font-weight: 600; color: #e4e4e7; }"
        "QGroupBox::title { subcontrol-origin: margin; left: 10px; padding: 0 4px; }"
        "QTabWidget::pane { border: 1px solid #2e2e33; border-radius: 6px; background-color: #18181b; }"
        "QTabBar::tab { background-color: #222226; color: #a1a1aa; border: 1px solid #2e2e33; border-bottom: none; "
        "border-top-left-radius: 6px; border-top-right-radius: 6px; padding: 6px 14px; margin-right: 2px; }"
        "QTabBar::tab:selected { background-color: #27272a; color: #f4f4f5; font-weight: 600; }"
        "QTabBar::tab:hover:!selected { background-color: #27272a; color: #e4e4e7; }"
        "QLineEdit, QPlainTextEdit, QSpinBox, QComboBox, QKeySequenceEdit { "
        "background-color: #222226; color: #f4f4f5; border: 1px solid #3f3f46; border-radius: 5px; padding: 4px 8px; selection-background-color: #2563eb; }"
        "QLineEdit:focus, QPlainTextEdit:focus, QSpinBox:focus, QComboBox:focus, QKeySequenceEdit:focus { border-color: #3b82f6; }"
        "QComboBox QAbstractItemView { background-color: #222226; color: #f4f4f5; selection-background-color: #2563eb; border: 1px solid #3f3f46; }"
        "QPushButton { background-color: #27272a; color: #f4f4f5; border: 1px solid #3f3f46; border-radius: 6px; padding: 5px 12px; }"
        "QPushButton:hover { background-color: #323238; border-color: #52525b; }"
        "QPushButton:pressed { background-color: #3f3f46; }"
        "QPushButton:default { background-color: #2563eb; border-color: #1d4ed8; color: #ffffff; font-weight: 600; }"
        "QPushButton:default:hover { background-color: #1d4ed8; }"
        "QPushButton:disabled { background-color: #202024; color: #52525b; border-color: #27272a; }"
        "QTableView, QTableWidget { background-color: #1c1c20; alternate-background-color: #222227; color: #f4f4f5; "
        "gridline-color: #2e2e33; border: 1px solid #2e2e33; border-radius: 6px; selection-background-color: #1d4ed8; selection-color: #ffffff; }"
        "QHeaderView::section { background-color: #27272a; color: #d4d4d8; border: none; border-right: 1px solid #323238; border-bottom: 1px solid #323238; padding: 5px 8px; font-weight: 600; }"
        "QMenu { background-color: #1f1f23; color: #f4f4f5; border: 1px solid #3f3f46; padding: 4px; }"
        "QMenu::item { padding: 5px 24px 5px 24px; border-radius: 4px; }"
        "QMenu::item:selected:enabled { background-color: #2563eb; color: #ffffff; }"
        "QMenu::item:disabled { color: #71717a; }"
        "QMenu::separator { height: 1px; background: #2e2e33; margin: 4px 6px; }"
        "QToolTip { background-color: #27272a; color: #f4f4f5; border: 1px solid #3f3f46; padding: 4px 6px; }"));
}

bool forwardToExistingInstance(const QString& serverName, const QString& payload) {
    QLocalSocket sock;
    sock.connectToServer(serverName);
    if (!sock.waitForConnected(450)) {
        return false;
    }
#ifdef Q_OS_WIN
    // Grant foreground activation permission so the running instance can raise its window above other apps.
    AllowSetForegroundWindow(ASFW_ANY);
#endif
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

    applyUnifiedDarkTheme(app);

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
                   a == QStringLiteral("--quit") ||
                   a == QStringLiteral("--quit-uninstall")) {
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
    if (startupCmd == QStringLiteral("CMD:--quit") ||
        startupCmd == QStringLiteral("CMD:--quit-uninstall")) {
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
