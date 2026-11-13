#pragma once

#include <QCheckBox>
#include <QComboBox>
#include <QDialog>
#include <QGroupBox>
#include <QJsonObject>
#include <QKeySequenceEdit>
#include <QLabel>
#include <QLineEdit>
#include <QPlainTextEdit>
#include <QPushButton>
#include <QSpinBox>
#include <QTableWidget>

namespace clashfx {

class DaemonClient;

class SubscriptionsDialog final : public QDialog {
    Q_OBJECT
public:
    explicit SubscriptionsDialog(DaemonClient* client, QWidget* parent = nullptr);
    void refreshFromSnapshot(const QJsonObject& snapshot);

private slots:
    void onAddOrUpdate();
    void onRefreshSelected();
    void onRefreshAll();
    void onDeleteSelected();
    void onSaveAutoUpdate();
    void onSelectionChanged();
    void onToggleUrlVisibility();

private:
    void retranslateUi();
    static QString formatBytes(qint64 bytes);
    static QString maskSubscriptionUrl(const QString& rawUrl);

    DaemonClient* client_ = nullptr;
    bool suppressAutoUpdateSignal_ = false;
    bool suppressSelectionFill_ = false;
    bool formDirty_ = false;
    bool urlVisible_ = false;
    QString selectedProfileName_;

    QCheckBox* autoUpdateCheck_ = nullptr;
    QLabel* everyLabel_ = nullptr;
    QSpinBox* intervalMinutesSpin_ = nullptr;
    QPushButton* saveIntervalBtn_ = nullptr;
    QTableWidget* table_ = nullptr;
    QGroupBox* editBox_ = nullptr;
    QLabel* nameFormLabel_ = nullptr;
    QLabel* urlFormLabel_ = nullptr;
    QLabel* uaFormLabel_ = nullptr;
    QLineEdit* nameEdit_ = nullptr;
    QLineEdit* urlEdit_ = nullptr;
    QPushButton* toggleUrlVisBtn_ = nullptr;
    QLineEdit* userAgentEdit_ = nullptr;
    QCheckBox* activateCheck_ = nullptr;
    QLabel* statusLabel_ = nullptr;
    QPushButton* saveSubBtn_ = nullptr;
    QPushButton* refreshSelBtn_ = nullptr;
    QPushButton* refreshAllBtn_ = nullptr;
    QPushButton* deleteBtn_ = nullptr;
    QPushButton* closeBtn_ = nullptr;
};

class SettingsDialog final : public QDialog {
    Q_OBJECT
public:
    explicit SettingsDialog(DaemonClient* client, QWidget* parent = nullptr);
    void loadFromSnapshot(const QJsonObject& snapshot);

private slots:
    void onBrowseCustomDir();
    void onResetCustomDir();
    void onResetBypass();
    void onAddCurrentNetwork();
    void onSaveClicked();

private:
    DaemonClient* client_ = nullptr;
    QJsonObject baseSettings_;
    QStringList detectedNetTokens_;

    // General tab
    QSpinBox* portOverrideSpin_ = nullptr;
    QSpinBox* apiPortSpin_ = nullptr;
    QLineEdit* apiSecretEdit_ = nullptr;
    QComboBox* logLevelCombo_ = nullptr;
    QLineEdit* customDirEdit_ = nullptr;
    QPlainTextEdit* bypassEdit_ = nullptr;
    QLineEdit* benchmarkUrlEdit_ = nullptr;
    QComboBox* updateChannelCombo_ = nullptr;

    // Enhanced TUN tab
    QLabel* serviceStatusLabel_ = nullptr;
    QComboBox* tunStackCombo_ = nullptr;
    QSpinBox* tunMtuSpin_ = nullptr;
    QLineEdit* tunInterfaceEdit_ = nullptr;
    QPlainTextEdit* tunExcludesEdit_ = nullptr;
    QCheckBox* bypassCnAppsCheck_ = nullptr;
    QCheckBox* customConfigAsIsCheck_ = nullptr;

    // Hotkeys & Auto-suspend tab
    QKeySequenceEdit* hotkeySysProxyEdit_ = nullptr;
    QKeySequenceEdit* hotkeyModeEdit_ = nullptr;
    QLabel* currentNetLabel_ = nullptr;
    QPlainTextEdit* suspendNetsEdit_ = nullptr;
    QPushButton* saveBtn_ = nullptr;
};

} // namespace clashfx
