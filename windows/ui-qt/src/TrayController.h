#pragma once

#include <QByteArray>
#include <QJsonObject>
#include <QLocalServer>
#include <QMenu>
#include <QObject>
#include <QPointer>
#include <QSystemTrayIcon>
#include <functional>

namespace clashfx {

class ConsoleWindow;
class DaemonClient;
class HotkeyManager;

class TrayController final : public QObject {
    Q_OBJECT
public:
    explicit TrayController(DaemonClient* client,
                            const QString& uiServerName,
                            bool showConsoleInitially,
                            QObject* parent = nullptr);
    ~TrayController() override;

    bool startSingleInstanceServer();
    void handleExternalCommand(const QString& message);
    ConsoleWindow* consoleWindow() const { return consoleWindow_; }

public slots:
    void openConsole();
    void openAiLockDialog();
    void openMultiNodeDialog();
    void openRuleEditorDialog();
    void openActiveRulesDialog();
    void openSubscriptionsDialog();
    void openDiagnosticsDialog(const QString& preselectNode = QString());
    void openSettingsDialog();
    void quitAll(bool forceRestoreProxy = false);

private slots:
    void onConnectionChanged(bool connected);
    void onStateChanged(const QJsonObject& snapshot);
    void onTrayActivated(QSystemTrayIcon::ActivationReason reason);
    void onToggleSystemProxyHotkey();
    void onSwitchModeHotkey();

private:
    void rebuildTrayVisualsAndMenu(bool forceMenu = false);
    void rebuildMenuContents();
    bool isMenuTreeVisible() const;
    QByteArray buildMenuSignature() const;
    void handleConfigMutationResult(const QString& actionTitle,
                                    const std::function<void()>& retryAction,
                                    bool ok,
                                    const QString& err);
    void mutateSettingBool(const QString& key, bool value);
    void mutateSettingString(const QString& key, const QString& value);
    void importSchemeUri(const QString& uri);
    void copyDiagnosticInfo();
    void checkForUpdates();
    void showAboutBox();
    QWidget* modalDialogParent() const;

    DaemonClient* client_ = nullptr;
    QString uiServerName_;
    QLocalServer* uiServer_ = nullptr;
    HotkeyManager* hotkeys_ = nullptr;
    QSystemTrayIcon* trayIcon_ = nullptr;
    QMenu* trayMenu_ = nullptr;
    ConsoleWindow* consoleWindow_ = nullptr;

    bool connected_ = false;
    bool menuDirty_ = true;
    bool daemonQuitSent_ = false;
    bool forceRestoreOnQuit_ = false;
    QString lastIconKey_;
    QString lastTooltip_;
    QByteArray lastMenuSig_;
    QJsonObject snapshot_;
};

} // namespace clashfx
