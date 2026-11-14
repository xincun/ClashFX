#pragma once

#include <QHash>
#include <QJsonObject>
#include <QWidget>

class QButtonGroup;
class QComboBox;
class QFrame;
class QHBoxLayout;
class QLabel;
class QLineEdit;
class QPlainTextEdit;
class QPushButton;
class QScrollArea;
class QSpinBox;
class QTableWidget;

namespace clashfx {

class BadgePill;
class DaemonClient;
class SegmentedControl;
class ToggleSwitch;

// ---------------------------------------------------------------------------
// Page 4: AI Dedicated Node Lock (Fail-Closed Protection)
// ---------------------------------------------------------------------------
class AiLockPage final : public QWidget {
    Q_OBJECT
public:
    explicit AiLockPage(DaemonClient* client, QWidget* parent = nullptr);

    void updateSnapshot(const QJsonObject& snapshot);
    void retranslateUi();
    void focusSearch();

private:
    void rebuildTable();
    void updateTopologyCard(const QJsonObject& snapshot);
    void refreshSelectionColumn();
    void applyLock(bool enable, bool allowUac = false);

    DaemonClient* client_ = nullptr;
    QJsonObject lastSnapshot_;
    QString selectedTarget_;
    QString lockedTarget_;
    bool lockEnabled_ = false;
    bool busy_ = false;

    // Header
    QLabel* titleLabel_ = nullptr;
    BadgePill* statusPill_ = nullptr;
    BadgePill* relayPill_ = nullptr;
    QLabel* subtitleLabel_ = nullptr;
    QPushButton* disableBtn_ = nullptr;
    QPushButton* applyBtn_ = nullptr;

    // Topology card
    QFrame* topoCard_ = nullptr;
    QLabel* topoTitleLabel_ = nullptr;
    QLabel* topoDescLabel_ = nullptr;
    BadgePill* topoHealthPill_ = nullptr;
    QLabel* boxAppsTitle_ = nullptr;
    QLabel* boxAppsVal_ = nullptr;
    QLabel* boxRelayTitle_ = nullptr;
    QLabel* boxRelayVal_ = nullptr;
    QFrame* boxTargetFrame_ = nullptr;
    QLabel* boxTargetTitle_ = nullptr;
    QLabel* boxTargetVal_ = nullptr;
    QFrame* boxFallbackFrame_ = nullptr;
    QLabel* boxFallbackTitle_ = nullptr;
    QLabel* boxFallbackVal_ = nullptr;

    // Candidates table card
    QLabel* tableTitleLabel_ = nullptr;
    QLineEdit* searchEdit_ = nullptr;
    QTableWidget* table_ = nullptr;

    // Cached node latency lookup from proxy_groups
    QHash<QString, QPair<int, bool>> delayCache_;
};

// ---------------------------------------------------------------------------
// Page 5: Multi-Node Fixed Ports & 204 Health Watchdog
// ---------------------------------------------------------------------------
class MultiNodePage final : public QWidget {
    Q_OBJECT
public:
    explicit MultiNodePage(DaemonClient* client, QWidget* parent = nullptr);

    void updateSnapshot(const QJsonObject& snapshot);
    void retranslateUi();
    bool hasUnsavedEdits() const { return userEditedPending_; }

private:
    void rebuildOrRefreshTable(const QJsonObject& nodeServices,
                               const QString& aiLockedNode,
                               const QHash<QString, QString>& nodeTypeMap);
    void updateKpiCards(const QJsonObject& nodeServices);
    void saveNodeServices();
    void toggleAllNodes(bool enableAll);
    void showChromeLaunchHelper();

    DaemonClient* client_ = nullptr;
    QJsonObject lastSnapshot_;
    bool userEditedPending_ = false;
    bool refreshingFromSnapshot_ = false;
    bool advancedExpanded_ = false;

    // Local pending edits (nodeName -> enabled / port)
    QHash<QString, bool> pendingEnabled_;
    QHash<QString, int> pendingPorts_;
    QStringList orderedNodeNames_;

    // Header
    QLabel* titleLabel_ = nullptr;
    BadgePill* statusPill_ = nullptr;
    BadgePill* dirtyPill_ = nullptr;
    QLabel* subtitleLabel_ = nullptr;
    QPushButton* checkBtn_ = nullptr;
    QPushButton* speedBtn_ = nullptr;
    QPushButton* saveBtn_ = nullptr;

    // 4 KPI Cards
    QLabel* kpi1Title_ = nullptr;
    ToggleSwitch* portsSwitch_ = nullptr;
    QLabel* kpi1Value_ = nullptr;
    QLabel* kpi1Sub_ = nullptr;

    QLabel* kpi2Title_ = nullptr;
    ToggleSwitch* recoverySwitch_ = nullptr;
    QLabel* kpi2Value_ = nullptr;
    QLabel* kpi2Sub_ = nullptr;

    QLabel* kpi3Title_ = nullptr;
    QLabel* kpi3Value_ = nullptr;
    QLabel* kpi3Sub_ = nullptr;

    QLabel* kpi4Title_ = nullptr;
    QPushButton* chromeGuideBtn_ = nullptr;
    QPushButton* enableAllBtn_ = nullptr;
    QPushButton* advToggleBtn_ = nullptr;
    QLabel* kpi4Sub_ = nullptr;

    // Collapsible advanced parameter drawer
    QFrame* advDrawer_ = nullptr;
    QLabel* firstPortLbl_ = nullptr;
    QSpinBox* firstPortSpin_ = nullptr;
    QLabel* intervalLbl_ = nullptr;
    QSpinBox* intervalSpin_ = nullptr;
    QLabel* thresholdLbl_ = nullptr;
    QSpinBox* thresholdSpin_ = nullptr;
    QLabel* cooldownLbl_ = nullptr;
    QSpinBox* cooldownSpin_ = nullptr;
    QPushButton* disableAllBtn_ = nullptr;
    QPushButton* refreshDirectBtn_ = nullptr;

    // Nodes table
    QTableWidget* table_ = nullptr;
};

// ---------------------------------------------------------------------------
// Page 6: Single-Node Latency Probe & Fault Diagnostics
// ---------------------------------------------------------------------------
class NodeDiagnosticPage final : public QWidget {
    Q_OBJECT
public:
    explicit NodeDiagnosticPage(DaemonClient* client, QWidget* parent = nullptr);

    void updateSnapshot(const QJsonObject& snapshot);
    void retranslateUi();
    void startDiagnosisForNode(const QString& nodeName);

private:
    void populateNodeChoices(const QJsonObject& snapshot);
    void runProbe();
    void copyReport();

    DaemonClient* client_ = nullptr;
    QJsonObject lastSnapshot_;
    QJsonObject lastReport_;
    bool probing_ = false;
    QHash<QString, QString> nodeProviderMap_;

    // Header
    QLabel* titleLabel_ = nullptr;
    BadgePill* statusPill_ = nullptr;
    QLabel* subtitleLabel_ = nullptr;
    QPushButton* copyBtn_ = nullptr;
    QPushButton* runBtn_ = nullptr;

    // Controls card
    QLabel* targetLbl_ = nullptr;
    QComboBox* nodeCombo_ = nullptr;
    QLabel* providerTag_ = nullptr;
    QLabel* urlLbl_ = nullptr;
    QComboBox* urlCombo_ = nullptr;

    // 3 KPI result cards
    QLabel* kpiVerdictTitle_ = nullptr;
    QLabel* kpiVerdictVal_ = nullptr;
    BadgePill* kpiVerdictCode_ = nullptr;

    QLabel* kpiSummaryTitle_ = nullptr;
    QLabel* kpiSummaryVal_ = nullptr;
    QLabel* kpiRawMsg_ = nullptr;

    QLabel* kpiAdviceTitle_ = nullptr;
    QLabel* kpiAdviceVal_ = nullptr;

    // Full formatted diagnostic output
    QLabel* consoleTitle_ = nullptr;
    QPlainTextEdit* outputEdit_ = nullptr;
};

}  // namespace clashfx
