#include "ConsoleWindow.h"
#include "DaemonClient.h"
#include "I18n.h"
#include "ModernWidgets.h"
#include "TrayController.h"
#include "TrayIconGenerator.h"

#include <QApplication>
#include <QColor>
#include <QCryptographicHash>
#include <QFont>
#include <QLocalSocket>
#include <QPalette>
#include <QStyleFactory>
#include <QThread>
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

    QFont appFont(QStringLiteral("Microsoft YaHei UI"));
    appFont.setFamilies({QStringLiteral("Microsoft YaHei UI"), QStringLiteral("Segoe UI"), QStringLiteral("PingFang SC")});
    appFont.setPointSize(9);
    app.setFont(appFont);

    QPalette pal;
    pal.setColor(QPalette::Window, QColor(0x14, 0x15, 0x1a));
    pal.setColor(QPalette::WindowText, QColor(0xf4, 0xf4, 0xf5));
    pal.setColor(QPalette::Base, QColor(0x18, 0x1a, 0x22));
    pal.setColor(QPalette::AlternateBase, QColor(0x1c, 0x1f, 0x29));
    pal.setColor(QPalette::ToolTipBase, QColor(0x1e, 0x20, 0x2a));
    pal.setColor(QPalette::ToolTipText, QColor(0xf4, 0xf4, 0xf5));
    pal.setColor(QPalette::Text, QColor(0xf1, 0xf5, 0xf9));
    pal.setColor(QPalette::Button, QColor(0x21, 0x23, 0x2d));
    pal.setColor(QPalette::ButtonText, QColor(0xe2, 0xe8, 0xf0));
    pal.setColor(QPalette::BrightText, QColor(0xff, 0xff, 0xff));
    pal.setColor(QPalette::Link, QColor(0x60, 0xa5, 0xfa));
    pal.setColor(QPalette::Highlight, QColor(0x25, 0x63, 0xeb));
    pal.setColor(QPalette::HighlightedText, QColor(0xff, 0xff, 0xff));
    pal.setColor(QPalette::PlaceholderText, QColor(0x64, 0x74, 0x8b));
    pal.setColor(QPalette::Disabled, QPalette::WindowText, QColor(0x64, 0x74, 0x8b));
    pal.setColor(QPalette::Disabled, QPalette::Text, QColor(0x64, 0x74, 0x8b));
    pal.setColor(QPalette::Disabled, QPalette::ButtonText, QColor(0x64, 0x74, 0x8b));
    app.setPalette(pal);

    app.setStyleSheet(QStringLiteral(
        "QDialog, QMainWindow { background-color: #14151a; color: #f4f4f5; }"
        "QMessageBox { background-color: #181a22; color: #f4f4f5; }"
        "QScrollArea { background-color: transparent; border: none; }"
        "QScrollArea > QWidget > QWidget { background-color: transparent; }"
        "QGroupBox { background-color: #1a1c24; border: 1px solid #272a36; border-radius: 10px; "
        "margin-top: 12px; padding: 14px 12px 12px 12px; font-weight: 600; color: #e2e8f0; }"
        "QGroupBox::title { subcontrol-origin: margin; left: 12px; padding: 0 6px; color: #f1f5f9; }"
        "QLineEdit, QPlainTextEdit, QSpinBox, QComboBox, QKeySequenceEdit { "
        "background-color: #14161d; color: #f1f5f9; border: 1px solid #303444; border-radius: 6px; "
        "padding: 5px 10px; selection-background-color: #2563eb; }"
        "QLineEdit:focus, QPlainTextEdit:focus, QSpinBox:focus, QComboBox:focus, QKeySequenceEdit:focus { "
        "border-color: #3b82f6; }"
        "QComboBox::drop-down { border: none; width: 22px; }"
        "QComboBox QAbstractItemView { background-color: #1a1c24; color: #f1f5f9; "
        "selection-background-color: #2563eb; border: 1px solid #303444; outline: none; }"
        "QSpinBox::up-button, QSpinBox::down-button { width: 16px; border: none; background: transparent; }"
        "QSpinBox::up-button:hover, QSpinBox::down-button:hover { background-color: #252836; }"
        "QPushButton { background-color: #21232d; color: #e2e8f0; border: 1px solid #323544; "
        "border-radius: 7px; padding: 6px 13px; font-weight: 600; }"
        "QPushButton:hover { background-color: #2a2d3a; border-color: #42475a; }"
        "QPushButton:pressed { background-color: #323646; }"
        "QPushButton:default, QPushButton[primary=\"true\"] { "
        "background-color: #2563eb; border-color: #3b82f6; color: #ffffff; }"
        "QPushButton:default:hover, QPushButton[primary=\"true\"]:hover { background-color: #1d4ed8; }"
        "QPushButton[danger=\"true\"] { "
        "background-color: rgba(239,68,68,24); border-color: rgba(248,113,113,85); color: #f87171; }"
        "QPushButton[danger=\"true\"]:hover { background-color: rgba(239,68,68,48); }"
        "QPushButton[ghost=\"true\"] { "
        "background-color: transparent; border-color: #2b2e3b; color: #94a3b8; }"
        "QPushButton[ghost=\"true\"]:hover { background-color: #20222c; color: #e2e8f0; }"
        "QPushButton:disabled { background-color: #191b22; color: #52525b; border-color: #232530; }"
        "QTableView, QTableWidget { background-color: #181a22; alternate-background-color: #1c1e28; "
        "color: #e2e8f0; gridline-color: #21242f; border: 1px solid #272a36; border-radius: 10px; "
        "selection-background-color: rgba(37,99,235,40); selection-color: #ffffff; outline: none; }"
        "QTableView::item, QTableWidget::item { padding: 4px 10px; border-bottom: 1px solid #21242f; }"
        "QHeaderView::section { background-color: #1e202a; color: #94a3b8; border: none; "
        "border-bottom: 1px solid #272a36; padding: 8px 12px; font-size: 11px; font-weight: 600; }"
        "QScrollBar:vertical { background: transparent; width: 8px; margin: 2px; }"
        "QScrollBar::handle:vertical { background: #2e3244; border-radius: 4px; min-height: 28px; }"
        "QScrollBar::handle:vertical:hover { background: #3f445c; }"
        "QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical { height: 0px; }"
        "QScrollBar::add-page:vertical, QScrollBar::sub-page:vertical { background: transparent; }"
        "QScrollBar:horizontal { background: transparent; height: 8px; margin: 2px; }"
        "QScrollBar::handle:horizontal { background: #2e3244; border-radius: 4px; min-width: 28px; }"
        "QScrollBar::handle:horizontal:hover { background: #3f445c; }"
        "QScrollBar::add-line:horizontal, QScrollBar::sub-line:horizontal { width: 0px; }"
        "QScrollBar::add-page:horizontal, QScrollBar::sub-page:horizontal { background: transparent; }"
        "QMenu { background-color: #1a1c24; color: #f1f5f9; border: 1px solid #303444; border-radius: 8px; padding: 5px; }"
        "QMenu::item { padding: 6px 24px; border-radius: 5px; }"
        "QMenu::item:selected:enabled { background-color: #2563eb; color: #ffffff; }"
        "QMenu::item:disabled { color: #64748b; }"
        "QMenu::separator { height: 1px; background: #272a36; margin: 4px 8px; }"
        "QToolTip { background-color: #1e202a; color: #f1f5f9; border: 1px solid #303444; border-radius: 6px; padding: 5px 8px; }"));
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
    // Wait until the primary instance finishes handleExternalCommand (including SetForegroundWindow)
    // and disconnects the socket before this process exits, keeping the Win32 foreground grant alive.
    if (sock.state() == QLocalSocket::ConnectedState) {
        sock.waitForDisconnected(600);
    }
    return true;
}

} // namespace

int main(int argc, char* argv[]) {
    QApplication app(argc, argv);
    QApplication::setApplicationName(QStringLiteral("ClashFX"));
    QApplication::setOrganizationName(QStringLiteral("ClashFX"));
    QApplication::setApplicationVersion(QStringLiteral("1.19.24"));
    QApplication::setQuitOnLastWindowClosed(false);
    QApplication::setWindowIcon(clashfx::TrayIconGenerator::appIcon());

    applyUnifiedDarkTheme(app);
    clashfx::Win11Dwm::installGlobalDarkTitleBarFilter(app);

    // Touch singleton so QTranslator is installed on qApp immediately
    (void)clashfx::I18n::instance();

    const QStringList args = QCoreApplication::arguments();
    QString customDaemonPipe;
    QString customUiPipe;
    QString customDataDir;
    QString startupUri;
    QString startupCmd;
    int smokeTestMs = 0;
    QString smokeReportPath;
    bool backgroundStart = false;

    for (int i = 1; i < args.size(); ++i) {
        const QString& a = args.at(i);
        if (a == QStringLiteral("--pipe") && i + 1 < args.size()) {
            customDaemonPipe = args.at(++i);
        } else if (a == QStringLiteral("--ui-pipe") && i + 1 < args.size()) {
            customUiPipe = args.at(++i);
        } else if (a == QStringLiteral("--data-dir") && i + 1 < args.size()) {
            customDataDir = args.at(++i);
        } else if (a == QStringLiteral("--smoke-test-ms") && i + 1 < args.size()) {
            smokeTestMs = args.at(++i).toInt();
        } else if (a == QStringLiteral("--smoke-report") && i + 1 < args.size()) {
            smokeReportPath = args.at(++i);
        } else if (a == QStringLiteral("--background") || a == QStringLiteral("--minimized")) {
            backgroundStart = true;
        } else if (a.startsWith(QStringLiteral("clash://"), Qt::CaseInsensitive) ||
                   a.startsWith(QStringLiteral("clashfx://"), Qt::CaseInsensitive) ||
                   a.startsWith(QStringLiteral("clash-verge://"), Qt::CaseInsensitive)) {
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

    QString uiServerName = customUiPipe.trimmed();
    if (uiServerName.isEmpty()) {
        if (!customDaemonPipe.trimmed().isEmpty()) {
            const QByteArray pipeHash = QCryptographicHash::hash(
                customDaemonPipe.trimmed().toUtf8(), QCryptographicHash::Sha256).toHex().left(12);
            uiServerName = QStringLiteral("ClashFXUI-%1-%2")
                               .arg(clashfx::DaemonClient::sanitizedUsername(),
                                    QString::fromLatin1(pipeHash));
        } else {
            uiServerName = QStringLiteral("ClashFXUI-") + clashfx::DaemonClient::sanitizedUsername();
        }
        if (smokeTestMs > 0) {
            uiServerName += QStringLiteral("-smoke-%1").arg(QCoreApplication::applicationPid());
        }
    }

    QString forwardMsg = QStringLiteral("ACTIVATE");
    if (!startupUri.isEmpty()) {
        forwardMsg = QStringLiteral("URI:") + startupUri;
    } else if (!startupCmd.isEmpty()) {
        forwardMsg = startupCmd;
    }

    const bool isQuitCmd = (startupCmd == QStringLiteral("CMD:--quit") ||
                            startupCmd == QStringLiteral("CMD:--quit-uninstall"));

#ifdef Q_OS_WIN
    struct InstanceMutexGuard {
        HANDLE handle = nullptr;
        ~InstanceMutexGuard() {
            if (handle) {
                CloseHandle(handle);
            }
        }
    } instanceMutex;

    if (smokeTestMs <= 0 && !isQuitCmd) {
        const std::wstring mutexName = (QStringLiteral("Local\\ClashFXInstance-") + uiServerName).toStdWString();
        instanceMutex.handle = CreateMutexW(nullptr, FALSE, mutexName.c_str());
        if (instanceMutex.handle && GetLastError() == ERROR_ALREADY_EXISTS) {
            for (int attempt = 0; attempt < 8; ++attempt) {
                if (forwardToExistingInstance(uiServerName, forwardMsg)) {
                    return 0;
                }
                QThread::msleep(150);
            }
            return 0;
        }
    }
#endif

    if (smokeTestMs <= 0 && forwardToExistingInstance(uiServerName, forwardMsg)) {
        return 0;
    }
    if (isQuitCmd) {
        clashfx::DaemonClient tempClient(customDaemonPipe, customDataDir);
        tempClient.sendQuitDaemonSync(startupCmd == QStringLiteral("CMD:--quit-uninstall"), 600);
        return 0;
    }

    const bool showInitially = (!backgroundStart || smokeTestMs > 0);
    clashfx::DaemonClient client(customDaemonPipe, customDataDir);
    clashfx::TrayController controller(&client, uiServerName, showInitially, smokeTestMs > 0);
    if (!controller.startSingleInstanceServer() && smokeTestMs <= 0) {
        if (forwardToExistingInstance(uiServerName, forwardMsg)) {
            return 0;
        }
    }
    client.start();

    if (smokeTestMs > 0 && controller.consoleWindow()) {
        controller.consoleWindow()->configureSmokeTest(smokeTestMs, smokeReportPath);
    }

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
