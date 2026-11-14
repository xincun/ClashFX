#pragma once

#include <QJsonArray>
#include <QJsonObject>
#include <QWidget>

class QComboBox;
class QFrame;
class QLabel;
class QLineEdit;
class QPlainTextEdit;
class QPushButton;
class QStackedWidget;
class QTableWidget;

namespace clashfx {

class BadgePill;
class DaemonClient;
class SegmentedControl;
class ToggleSwitch;

// ---------------------------------------------------------------------------
// Page 3: Routing Rules, Profile Mixin & Live Effective Rules Hot-Update
// ---------------------------------------------------------------------------
class RuleEditorPage final : public QWidget {
    Q_OBJECT
public:
    explicit RuleEditorPage(DaemonClient* client, QWidget* parent = nullptr);

    void updateSnapshot(const QJsonObject& snapshot);
    void retranslateUi();
    void onPageActivated(int subTab = -1);
    bool isDirty() const { return dirty_ || rulesetDirty_; }

private:
    struct MixinRuleRow {
        QString type;
        QString payload;
        QString target;
    };

    void loadProfileMixin();
    void applyDocument(const QJsonObject& doc, bool preserveActiveTab);
    void populateCustomTable(const QList<MixinRuleRow>& rows);
    void addCustomRuleRow(const MixinRuleRow& item, int insertAt = -1);
    QList<MixinRuleRow> collectCustomRules() const;
    void moveCustomRow(int row, int delta);
    void removeCustomRow(int row);
    void updateTargetComboStyle(QComboBox* combo);
    void refreshProxyTargetsFromSnapshot(const QJsonObject& snapshot);
    void syncSubTabSwitch(int nextIndex);
    void saveMixin();

    void loadEffectiveRules();
    void renderEffectiveRulesTable();
    void populateRuleProviderSelector();
    void loadSelectedProviderIntoEditor();
    void saveSelectedRuleProvider();
    void resetSelectedRuleProvider();
    void setDirty(bool dirty);
    void setRulesetDirty(bool dirty);

    DaemonClient* client_ = nullptr;
    QJsonObject lastSnapshot_;
    QStringList proxyGroupChoices_;
    QString defaultTarget_ = QStringLiteral("PROXY");
    QString baseYamlCache_;
    bool hasExtraKeys_ = false;
    bool dirty_ = false;
    bool rulesetDirty_ = false;
    bool loading_ = false;
    bool switchingTab_ = false;
    bool togglingRule_ = false;
    int currentTab_ = 0;

    QJsonObject effectiveRulesView_;
    QJsonObject ruleProvidersMap_;
    QString currentProviderName_;

    // Header
    QLabel* titleLabel_ = nullptr;
    BadgePill* preservePill_ = nullptr;
    BadgePill* statusPill_ = nullptr;
    QLabel* subtitleLabel_ = nullptr;
    SegmentedControl* tabSwitch_ = nullptr;
    QPushButton* reloadBtn_ = nullptr;
    QPushButton* saveBtn_ = nullptr;

    QStackedWidget* subStack_ = nullptr;

    // Sub-tab 0: Visual Mixin Rules
    QLabel* cnCardTitle_ = nullptr;
    BadgePill* cnRecPill_ = nullptr;
    QLabel* cnCardDesc_ = nullptr;
    ToggleSwitch* cnSwitch_ = nullptr;

    QLabel* lanCardTitle_ = nullptr;
    BadgePill* lanRecPill_ = nullptr;
    QLabel* lanCardDesc_ = nullptr;
    ToggleSwitch* lanSwitch_ = nullptr;

    QLabel* customTitle_ = nullptr;
    QLabel* customHint_ = nullptr;
    QPushButton* addRuleBtn_ = nullptr;
    QTableWidget* customTable_ = nullptr;

    // Sub-tab 1: YAML Source
    QFrame* extraKeysBanner_ = nullptr;
    QLabel* extraKeysLbl_ = nullptr;
    QLabel* yamlHintLbl_ = nullptr;
    QPlainTextEdit* yamlEdit_ = nullptr;

    // Sub-tab 2: Effective Rules & Hot-Reload .list Providers
    QLineEdit* effSearchEdit_ = nullptr;
    QComboBox* effSourceCombo_ = nullptr;
    BadgePill* effCountPill_ = nullptr;
    QPushButton* effRefreshBtn_ = nullptr;
    QTableWidget* effTable_ = nullptr;

    QLabel* rsTitleLbl_ = nullptr;
    QComboBox* rsProviderCombo_ = nullptr;
    BadgePill* rsBehaviorPill_ = nullptr;
    BadgePill* rsCountPill_ = nullptr;
    QLabel* rsPathLbl_ = nullptr;
    QPlainTextEdit* rsEdit_ = nullptr;
    QPushButton* rsResetBtn_ = nullptr;
    QPushButton* rsSaveBtn_ = nullptr;
};

}  // namespace clashfx
