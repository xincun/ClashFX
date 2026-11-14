#pragma once

#include <QJsonObject>
#include <QWidget>

class QComboBox;
class QFrame;
class QLabel;
class QLineEdit;
class QPlainTextEdit;
class QPushButton;
class QSpinBox;
class QStackedWidget;
class QTableWidget;

namespace clashfx {

class BadgePill;
class DaemonClient;
class SegmentedControl;
class ToggleSwitch;

// ---------------------------------------------------------------------------
// Page 2: Managed Subscriptions & Profiles
// ---------------------------------------------------------------------------
class SubscriptionsPage final : public QWidget {
    Q_OBJECT
public:
    explicit SubscriptionsPage(DaemonClient* client, QWidget* parent = nullptr);

    void updateSnapshot(const QJsonObject& snapshot);
    void retranslateUi();

private:
    void upsertSubscription();
    void refreshSubscription(const QString& name);
    void refreshAllSubscriptions();
    void activateProfile(const QString& name);
    void removeSubscription(const QString& name);
    void clearForm();
    void saveAutoUpdatePolicy();
    void setFormDirty(bool dirty);

    DaemonClient* client_ = nullptr;
    QJsonObject lastSnapshot_;
    QStringList subNamesOrder_;
    bool formDirty_ = false;
    bool refreshing_ = false;
    bool busy_ = false;

    // Header
    QLabel* titleLabel_ = nullptr;
    BadgePill* countPill_ = nullptr;
    BadgePill* formPill_ = nullptr;
    QLabel* subtitleLabel_ = nullptr;
    QPushButton* refreshAllBtn_ = nullptr;
    QPushButton* saveFormBtn_ = nullptr;

    // Import / Edit Form Card
    QLabel* nameLbl_ = nullptr;
    QLineEdit* nameEdit_ = nullptr;
    QLabel* uaLbl_ = nullptr;
    QLineEdit* uaEdit_ = nullptr;
    QLabel* selectAfterLbl_ = nullptr;
    ToggleSwitch* selectAfterSwitch_ = nullptr;
    QLabel* urlLbl_ = nullptr;
    QLineEdit* urlEdit_ = nullptr;
    QPushButton* clearFormBtn_ = nullptr;

    QLabel* autoUpdateLbl_ = nullptr;
    ToggleSwitch* autoUpdateSwitch_ = nullptr;
    QComboBox* autoIntervalCombo_ = nullptr;

    // Subscriptions Table
    QTableWidget* table_ = nullptr;
};

// ---------------------------------------------------------------------------
// Page 7: Preferences & Settings
// ---------------------------------------------------------------------------
class SettingsPage final : public QWidget {
    Q_OBJECT
public:
    explicit SettingsPage(DaemonClient* client, QWidget* parent = nullptr);

    void updateSnapshot(const QJsonObject& snapshot);
    void retranslateUi();
    bool isDirty() const { return dirty_; }

private:
    void saveSettings(bool allowUac = false);
    void setDirty(bool dirty);
    void updateActiveNetworkBanner(const QJsonObject& snapshot);

    DaemonClient* client_ = nullptr;
    QJsonObject lastSnapshot_;
    QString configDir_;
    QString logsDir_;
    QStringList curNetProfiles_;
    QStringList curNetGateways_;
    bool dirty_ = false;
    bool refreshing_ = false;

    // Header
    QLabel* titleLabel_ = nullptr;
    BadgePill* servicePill_ = nullptr;
    BadgePill* dirtyPill_ = nullptr;
    QLabel* subtitleLabel_ = nullptr;
    SegmentedControl* tabSwitch_ = nullptr;
    QPushButton* saveBtn_ = nullptr;

    QStackedWidget* subStack_ = nullptr;

    // Sub-tab 0: General & Ports
    QLabel* cardPortsTitle_ = nullptr;
    QLabel* portOverrideTitle_ = nullptr;
    QLabel* portOverrideSub_ = nullptr;
    QSpinBox* portOverrideSpin_ = nullptr;
    QLabel* apiPortTitle_ = nullptr;
    QLabel* apiPortSub_ = nullptr;
    QSpinBox* apiPortSpin_ = nullptr;
    QLabel* apiSecretTitle_ = nullptr;
    QLabel* apiSecretSub_ = nullptr;
    QLineEdit* apiSecretEdit_ = nullptr;
    QLabel* allowLanTitle_ = nullptr;
    QLabel* allowLanSub_ = nullptr;
    ToggleSwitch* allowLanSwitch_ = nullptr;
    QLabel* ipv6Title_ = nullptr;
    QLabel* ipv6Sub_ = nullptr;
    ToggleSwitch* ipv6Switch_ = nullptr;

    QLabel* cardSystemTitle_ = nullptr;
    QLabel* launchTitle_ = nullptr;
    QLabel* launchSub_ = nullptr;
    ToggleSwitch* launchSwitch_ = nullptr;
    QLabel* bypassCnAppsTitle_ = nullptr;
    QLabel* bypassCnAppsSub_ = nullptr;
    ToggleSwitch* bypassCnAppsSwitch_ = nullptr;
    QLabel* customAsIsTitle_ = nullptr;
    QLabel* customAsIsSub_ = nullptr;
    ToggleSwitch* customAsIsSwitch_ = nullptr;
    QLabel* uiLangTitle_ = nullptr;
    QLabel* uiLangSub_ = nullptr;
    QComboBox* langCombo_ = nullptr;
    QComboBox* trayCombo_ = nullptr;
    QComboBox* logLevelCombo_ = nullptr;
    QLabel* benchUrlTitle_ = nullptr;
    QLabel* benchUrlSub_ = nullptr;
    QLineEdit* benchUrlEdit_ = nullptr;
    QLabel* bypassListTitle_ = nullptr;
    QLabel* bypassListSub_ = nullptr;
    QLineEdit* bypassListEdit_ = nullptr;
    QLabel* customDirTitle_ = nullptr;
    QLabel* customDirSub_ = nullptr;
    QLineEdit* customDirEdit_ = nullptr;
    QPushButton* browseDirBtn_ = nullptr;

    // Sub-tab 1: Enhanced TUN & Directories
    QLabel* tunServiceTitle_ = nullptr;
    QLabel* tunServiceSub_ = nullptr;
    QPushButton* openCfgDirBtn_ = nullptr;
    QPushButton* openLogDirBtn_ = nullptr;
    QLabel* tunCardTitle_ = nullptr;
    QLabel* tunStackTitle_ = nullptr;
    QLabel* tunStackSub_ = nullptr;
    QComboBox* tunStackCombo_ = nullptr;
    QLabel* tunMtuTitle_ = nullptr;
    QLabel* tunMtuSub_ = nullptr;
    QSpinBox* tunMtuSpin_ = nullptr;
    QLabel* tunIfaceTitle_ = nullptr;
    QLabel* tunIfaceSub_ = nullptr;
    QLineEdit* tunIfaceEdit_ = nullptr;
    QLabel* tunExcludesTitle_ = nullptr;
    QLabel* tunExcludesSub_ = nullptr;
    QPlainTextEdit* tunExcludesEdit_ = nullptr;

    // Sub-tab 2: Global Hotkeys & Auto-Suspend Networks
    QLabel* hkCardTitle_ = nullptr;
    QLabel* hkProxyTitle_ = nullptr;
    QLabel* hkProxySub_ = nullptr;
    QLineEdit* hkProxyEdit_ = nullptr;
    QLabel* hkModeTitle_ = nullptr;
    QLabel* hkModeSub_ = nullptr;
    QLineEdit* hkModeEdit_ = nullptr;

    QLabel* suspendCardTitle_ = nullptr;
    QLabel* activeNetTitle_ = nullptr;
    QLabel* activeNetInfo_ = nullptr;
    QPushButton* addNetProfileBtn_ = nullptr;
    QPushButton* addNetGwBtn_ = nullptr;
    QLabel* suspendRulesTitle_ = nullptr;
    QLabel* suspendRulesSub_ = nullptr;
    QPlainTextEdit* suspendRulesEdit_ = nullptr;
};

}  // namespace clashfx
