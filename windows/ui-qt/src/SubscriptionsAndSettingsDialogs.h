#pragma once

#include <QCheckBox>
#include <QComboBox>
#include <QDialog>
#include <QJsonObject>
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

private:
    static QString formatBytes(qint64 bytes);

    DaemonClient* client_ = nullptr;
    QCheckBox* autoUpdateCheck_ = nullptr;
    QSpinBox* intervalMinutesSpin_ = nullptr;
    QTableWidget* table_ = nullptr;
    QLineEdit* nameEdit_ = nullptr;
    QLineEdit* urlEdit_ = nullptr;
    QCheckBox* activateCheck_ = nullptr;
    QPushButton* saveSubBtn_ = nullptr;
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
    QLineEdit* hotkeySysProxyEdit_ = nullptr;
    QLineEdit* hotkeyModeEdit_ = nullptr;
    QLabel* currentNetLabel_ = nullptr;
    QPlainTextEdit* suspendNetsEdit_ = nullptr;
    QPushButton* saveBtn_ = nullptr;
};

} // namespace clashfx
