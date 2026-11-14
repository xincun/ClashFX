#pragma once

#include <QByteArray>
#include <QJsonObject>
#include <QMainWindow>
#include <QPointer>

class QComboBox;
class QFrame;
class QHBoxLayout;
class QLabel;
class QNetworkAccessManager;
class QNetworkReply;
class QProgressBar;
class QPushButton;
class QStackedWidget;

namespace clashfx {

class AiLockPage;
class BadgePill;
class DaemonClient;
class MultiNodePage;
class NodeDiagnosticPage;
class RuleEditorPage;
class SegmentedControl;
class SettingsPage;
class SidebarNavButton;
class SidebarQuickCard;
class SubscriptionsPage;
class WebView2Widget;

enum class ConsolePage {
    Dashboard = 0,
    Subscriptions = 1,
    Rules = 2,
    AiLock = 3,
    MultiNode = 4,
    Diagnostics = 5,
    Settings = 6,
};

class ConsoleWindow final : public QMainWindow {
    Q_OBJECT
public:
    explicit ConsoleWindow(DaemonClient* client, QWidget* parent = nullptr);

    void setAllowClose(bool allow) { allowClose_ = allow; }
    void applySnapshot(bool connected, const QJsonObject& snapshot);
    void navigateTo(ConsolePage page, int subTab = -1, const QString& diagNode = QString());
    ConsolePage currentPage() const { return currentPage_; }
    void presentWindow();

protected:
    void showEvent(QShowEvent* event) override;
    void hideEvent(QHideEvent* event) override;
    void closeEvent(QCloseEvent* event) override;

private:
    QWidget* buildSidebar(QWidget* parent);
    QWidget* buildDashboardPage(QWidget* parent);
    void updateWindowTitle();
    void updateSidebarFromSnapshot(bool connected, const QJsonObject& snapshot);
    void updateDashboardHeader(bool connected, const QJsonObject& snapshot);
    void retranslateUi();

    void switchOutboundMode(int index);
    void toggleSystemProxy(bool nextChecked);
    void toggleEnhancedMode(bool nextChecked, bool allowUac = false);
    void selectActiveProfile(const QString& profileName);
    void refreshActiveProfile();
    void copyTerminalProxyCommand();
    void runBenchmarkAll();

    void syncTrafficStream();
    void stopTrafficStream();
    static QString formatSpeedRate(qint64 bytesPerSec);

    DaemonClient* client_ = nullptr;
    bool allowClose_ = false;
    bool lastConnected_ = false;
    bool updatingUi_ = false;
    QJsonObject lastSnapshot_;
    ConsolePage currentPage_ = ConsolePage::Dashboard;

    // Live /traffic NDJSON state
    QNetworkAccessManager* netMgr_ = nullptr;
    QPointer<QNetworkReply> trafficReply_;
    QByteArray trafficBuf_;
    int trafficPort_ = 0;
    QString trafficSecret_;

    // Sidebar widgets
    QFrame* sidebar_ = nullptr;
    QLabel* brandIconLabel_ = nullptr;
    QLabel* brandSubLabel_ = nullptr;
    BadgePill* sidebarAiPill_ = nullptr;
    SegmentedControl* modeSwitch_ = nullptr;
    SidebarQuickCard* sysProxyCard_ = nullptr;
    SidebarQuickCard* tunCard_ = nullptr;

    QComboBox* profileCombo_ = nullptr;
    BadgePill* profileActivePill_ = nullptr;
    QPushButton* profileRefreshBtn_ = nullptr;
    QProgressBar* quotaBar_ = nullptr;
    QLabel* quotaTextLabel_ = nullptr;

    QLabel* navSectionLabel_ = nullptr;
    SidebarNavButton* navDashboard_ = nullptr;
    SidebarNavButton* navSubscriptions_ = nullptr;
    SidebarNavButton* navRules_ = nullptr;
    SidebarNavButton* navAiLock_ = nullptr;
    SidebarNavButton* navMultiNode_ = nullptr;
    SidebarNavButton* navDiagnostics_ = nullptr;
    SidebarNavButton* navSettings_ = nullptr;

    QLabel* upTitleLabel_ = nullptr;
    QLabel* upSpeedLabel_ = nullptr;
    QLabel* downTitleLabel_ = nullptr;
    QLabel* downSpeedLabel_ = nullptr;
    QLabel* mixedPortLabel_ = nullptr;
    QLabel* apiPortLabel_ = nullptr;

    // Right workspace stack & pages
    QStackedWidget* rightStack_ = nullptr;

    QLabel* dashTitleLabel_ = nullptr;
    BadgePill* dashStatusPill_ = nullptr;
    BadgePill* dashAiPill_ = nullptr;
    QLabel* dashSubLabel_ = nullptr;
    QPushButton* benchAllBtn_ = nullptr;
    QPushButton* copyTermBtn_ = nullptr;
    QPushButton* reloadDashBtn_ = nullptr;

    QStackedWidget* dashboardStack_ = nullptr;
    WebView2Widget* webView_ = nullptr;
    QWidget* fallbackPage_ = nullptr;
    QLabel* fallbackTitleLabel_ = nullptr;
    QLabel* fallbackReasonLabel_ = nullptr;
    QPushButton* downloadWebViewBtn_ = nullptr;
    QPushButton* retryWebViewBtn_ = nullptr;

    SubscriptionsPage* subsPage_ = nullptr;
    RuleEditorPage* rulesPage_ = nullptr;
    AiLockPage* aiLockPage_ = nullptr;
    MultiNodePage* multiNodePage_ = nullptr;
    NodeDiagnosticPage* diagPage_ = nullptr;
    SettingsPage* settingsPage_ = nullptr;
};

}  // namespace clashfx
