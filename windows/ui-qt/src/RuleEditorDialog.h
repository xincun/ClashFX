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
#include <QTabWidget>
#include <QTableWidget>
#include <QVector>

namespace clashfx {

class DaemonClient;

struct MixinRuleRow {
    QString type = QStringLiteral("DOMAIN-SUFFIX");
    QString payload;
    QString target = QStringLiteral("DIRECT");
};

class RuleEditorDialog final : public QDialog {
    Q_OBJECT
public:
    explicit RuleEditorDialog(DaemonClient* client, QWidget* parent = nullptr);
    void refreshFromSnapshot(const QJsonObject& snapshot);
    void reloadMixinFromDaemon();
    void refreshEffectiveRules();
    void selectTab(int index);

protected:
    void closeEvent(QCloseEvent* event) override;

private slots:
    void onAddRuleRow();
    void onSaveAndApply();
    void onTabChanged(int index);
    void onOpenMixinFolder();
    void onEffectiveSelectionChanged();
    void onEffectiveItemChanged(QTableWidgetItem* item);
    void onFilterEffectiveRules(const QString& keyword);
    void onSaveRulesetProvider();
    void onResetRulesetProvider();
    void onOpenRulesetFolder();

private:
    void applyMixinDocument(const QJsonObject& doc, bool markClean);
    void applyEffectiveRulesData(const QJsonObject& obj, int preferredRuleIndex = -1);
    void rebuildCustomTable(int focusRow = -1);
    void populateTargetCombo(QComboBox* combo, const QString& currentTarget) const;
    QJsonArray serializeVisualRulesJson(bool skipBlankPayload) const;
    void setDirty(bool dirty, const QString& savedHint = QString());
    void setRulesetDirty(bool dirty, const QString& savedHint = QString());
    void updateFooterButtonsForTab();
    void renderEffectiveTable(int preferredRuleIndex = -1);
    void loadDetailForRuleObject(const QJsonObject& r, bool forceReloadEditor = false);
    static QString placeholderForRuleType(const QString& ruleType);

    DaemonClient* client_ = nullptr;
    bool suppressSignals_ = false;
    bool suppressEffectiveItemSignals_ = false;
    bool suppressRulesetSignals_ = false;
    bool dirty_ = false;
    bool rulesetDirty_ = false;
    bool visualModifiedSinceSync_ = false;
    bool yamlModifiedSinceSync_ = false;
    int lastTabIndex_ = 0;

    QString mixinFilePath_;
    QString lastSyncedYaml_;
    bool hasExtraYamlKeys_ = false;

    QString selectedProfile_;
    QString outboundMode_;
    QString defaultTarget_ = QStringLiteral("DIRECT");
    QStringList groupTargets_;
    QStringList nodeTargets_;
    QVector<MixinRuleRow> workingRules_;

    QJsonArray effectiveRules_;
    QJsonObject ruleProviders_;
    QString activeEditableProvider_;
    QString activeRulesetFilePath_;
    int lastSelectedEffOrigIdx_ = -1;

    QLabel* headerDescLabel_ = nullptr;
    QTabWidget* tabs_ = nullptr;

    // Tab 0: Visual Config Patch
    QCheckBox* bypassCNWebCheck_ = nullptr;
    QCheckBox* bypassLANCheck_ = nullptr;
    QLabel* emptyHintLabel_ = nullptr;
    QTableWidget* customTable_ = nullptr;
    QPushButton* addRowBtn_ = nullptr;
    QLabel* extraKeysBadge_ = nullptr;

    // Tab 1: Raw YAML (.profile_mixin.yaml)
    QLabel* yamlPathLabel_ = nullptr;
    QPlainTextEdit* yamlEdit_ = nullptr;

    // Tab 2: Live Effective Rules Inspector & Dynamic Ruleset Editor
    QLineEdit* searchEdit_ = nullptr;
    QLabel* effSummaryLabel_ = nullptr;
    QPushButton* refreshEffBtn_ = nullptr;
    QTableWidget* effTable_ = nullptr;
    QGroupBox* providerDetailBox_ = nullptr;
    QLabel* providerMetaLabel_ = nullptr;
    QPushButton* openRulesetFolderBtn_ = nullptr;
    QPushButton* resetRulesetBtn_ = nullptr;
    QPushButton* saveRulesetBtn_ = nullptr;
    QPlainTextEdit* providerDetailEdit_ = nullptr;

    // Shared Footer
    QLabel* statusLabel_ = nullptr;
    QPushButton* saveApplyBtn_ = nullptr;
};

} // namespace clashfx
