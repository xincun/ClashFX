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
#include <QStandardItemModel>
#include <QTableView>

namespace clashfx {

class DaemonClient;

class AiLockDialog final : public QDialog {
    Q_OBJECT
public:
    explicit AiLockDialog(DaemonClient* client, QWidget* parent = nullptr);
    void refreshFromSnapshot(const QJsonObject& snapshot);

private slots:
    void onEnableClicked();
    void onDisableClicked();

private:
    DaemonClient* client_ = nullptr;
    QLabel* statusLabel_ = nullptr;
    QLabel* relayLabel_ = nullptr;
    QComboBox* targetCombo_ = nullptr;
    QPushButton* enableBtn_ = nullptr;
    QPushButton* disableBtn_ = nullptr;
};

class MultiNodeDialog final : public QDialog {
    Q_OBJECT
public:
    explicit MultiNodeDialog(DaemonClient* client, QWidget* parent = nullptr);
    void refreshFromSnapshot(const QJsonObject& snapshot);

private slots:
    void onSelectAll();
    void onDisableAll();
    void onSaveAndApply();
    void onCheckConnectivity();
    void onDirectRefresh();
    void onToggleSpeedTest();
    void onCopyHttp();
    void onCopySocks();
    void onShowChromeGuide();

private:
    int selectedRowPort(QString* outNodeName = nullptr) const;
    static QString formatHealth(const QJsonObject& healthObj, bool portEnabled, bool portsGlobalEnabled);
    static QString formatSpeed(const QJsonObject& speedObj, bool portEnabled, bool portsGlobalEnabled);

    DaemonClient* client_ = nullptr;
    bool updatingModel_ = false;
    bool userEditedPending_ = false;
    bool isSpeedTesting_ = false;

    QCheckBox* portsEnabledCheck_ = nullptr;
    QCheckBox* recoveryEnabledCheck_ = nullptr;
    QSpinBox* firstPortSpin_ = nullptr;
    QSpinBox* checkIntervalSpin_ = nullptr;
    QSpinBox* failureThresholdSpin_ = nullptr;
    QSpinBox* refreshCooldownSpin_ = nullptr;
    QLabel* statusBanner_ = nullptr;

    QTableView* tableView_ = nullptr;
    QStandardItemModel* model_ = nullptr;

    QPushButton* saveBtn_ = nullptr;
    QPushButton* checkBtn_ = nullptr;
    QPushButton* directRefreshBtn_ = nullptr;
    QPushButton* speedTestBtn_ = nullptr;
};

class NodeDiagnosticDialog final : public QDialog {
    Q_OBJECT
public:
    explicit NodeDiagnosticDialog(DaemonClient* client, QWidget* parent = nullptr);
    void refreshFromSnapshot(const QJsonObject& snapshot);
    void preselectNode(const QString& nodeName);

private slots:
    void onRunProbe();
    void onCopyReport();

private:
    DaemonClient* client_ = nullptr;
    QComboBox* nodeCombo_ = nullptr;
    QLineEdit* providerEdit_ = nullptr;
    QLineEdit* urlEdit_ = nullptr;
    QPushButton* runBtn_ = nullptr;
    QPlainTextEdit* outputEdit_ = nullptr;
};

} // namespace clashfx
