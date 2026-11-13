#pragma once

#include <QCheckBox>
#include <QComboBox>
#include <QDialog>
#include <QGroupBox>
#include <QJsonArray>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QPlainTextEdit>
#include <QPushButton>
#include <QSet>
#include <QTabWidget>
#include <QTableWidget>
#include <QVector>

namespace clashfx {

class DaemonClient;

struct CustomRuleEntry {
    bool enabled = true;
    QString type = QStringLiteral("DOMAIN-SUFFIX");
    QString payload;
    QString target = QStringLiteral("@DEFAULT");
    QString position = QStringLiteral("prepend");
    bool noResolve = false;
    QString note;
};

class RuleEditorDialog final : public QDialog {
    Q_OBJECT
public:
    explicit RuleEditorDialog(DaemonClient* client, QWidget* parent = nullptr);
    void refreshFromSnapshot(const QJsonObject& snapshot);
    void refreshEffectiveRules();

protected:
    void closeEvent(QCloseEvent* event) override;

private slots:
    void onRuleTypeChanged(int index);
    void onCustomSelectionChanged();
    void onCustomCellChanged(int row, int column);
    void onAddRule();
    void onUpdateSelectedRule();
    void onMoveUp();
    void onMoveDown();
    void onDeleteSelected();
    void onSaveAndApply();
    void onOpenMixinFile();
    void onEffectiveSelectionChanged();
    void onFilterEffectiveRules(const QString& keyword);

private:
    bool readFormIntoEntry(CustomRuleEntry* out, QString* errMsg) const;
    void rebuildCustomTable(int selectRow = -1);
    void rebuildTargetCombo(const QString& preserveTarget = QString());
    void setDirty(bool dirty);
    void renderEffectiveTable();
    QString formatTargetDisplay(const QString& target) const;
    static bool typeSupportsNoResolve(const QString& ruleType);

    DaemonClient* client_ = nullptr;
    bool updatingTable_ = false;
    bool dirty_ = false;

    QString selectedProfile_;
    QString outboundMode_;
    QString defaultTarget_ = QStringLiteral("DIRECT");
    QSet<QString> validTargetsInProfile_;
    QVector<CustomRuleEntry> workingRules_;

    QJsonArray effectiveRules_;
    QJsonObject ruleProviders_;

    QLabel* infoBanner_ = nullptr;
    QTabWidget* tabs_ = nullptr;

    // Tab 1: Custom Rules
    QTableWidget* customTable_ = nullptr;
    QComboBox* typeCombo_ = nullptr;
    QComboBox* targetCombo_ = nullptr;
    QComboBox* positionCombo_ = nullptr;
    QLineEdit* payloadEdit_ = nullptr;
    QCheckBox* noResolveCheck_ = nullptr;
    QLineEdit* noteEdit_ = nullptr;
    QPushButton* addBtn_ = nullptr;
    QPushButton* updateRowBtn_ = nullptr;
    QPushButton* moveUpBtn_ = nullptr;
    QPushButton* moveDownBtn_ = nullptr;
    QPushButton* deleteBtn_ = nullptr;
    QLabel* unsavedLabel_ = nullptr;
    QPushButton* saveApplyBtn_ = nullptr;

    // Tab 2: Effective Rules Inspector
    QLineEdit* searchEdit_ = nullptr;
    QLabel* effSummaryLabel_ = nullptr;
    QPushButton* refreshEffBtn_ = nullptr;
    QTableWidget* effTable_ = nullptr;
    QGroupBox* providerDetailBox_ = nullptr;
    QPlainTextEdit* providerDetailEdit_ = nullptr;
};

} // namespace clashfx
