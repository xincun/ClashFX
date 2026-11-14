#include "RuleEditorDialog.h"

#include "DaemonClient.h"
#include "I18n.h"
#include "ModernWidgets.h"

#include <QAbstractItemView>
#include <QComboBox>
#include <QFrame>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QIcon>
#include <QJsonArray>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QMessageBox>
#include <QPlainTextEdit>
#include <QPointer>
#include <QPushButton>
#include <QSignalBlocker>
#include <QSplitter>
#include <QStackedWidget>
#include <QStyle>
#include <QTableWidget>
#include <QTableWidgetItem>
#include <QTimer>
#include <QVBoxLayout>

namespace clashfx {

namespace {

inline QString uiText(const char* zh, const char* en) {
    return I18n::instance().isChinese() ? QString::fromUtf8(zh) : QString::fromUtf8(en);
}

struct RuleTypeMeta {
    const char* code;
    const char* zhLabel;
    const char* enLabel;
    const char* placeholderZh;
    const char* placeholderEn;
};

constexpr RuleTypeMeta kRuleTypes[] = {
    {"DOMAIN-SUFFIX", "网站域名 (含子域名)", "Domain Suffix", "claude.ai / scholar.google.com", "claude.ai / example.com"},
    {"DOMAIN", "精准域名 (仅本域名)", "Exact Domain", "api.openai.com", "api.openai.com"},
    {"DOMAIN-KEYWORD", "域名关键词", "Domain Keyword", "openai", "openai"},
    {"DOMAIN-WILDCARD", "域名通配符", "Domain Wildcard", "*.google.com", "*.google.com"},
    {"DOMAIN-REGEX", "域名正则表达式", "Domain Regex", "^ai\\..+\\.com$", "^ai\\..+\\.com$"},
    {"PROCESS-NAME", "软件程序 (.exe / 进程)", "Process Name (.exe)", "steam.exe / claude.exe", "steam.exe / claude.exe"},
    {"PROCESS-PATH", "程序完整路径", "Process Path", "C:\\Program Files\\...", "C:\\Program Files\\..."},
    {"GEOSITE", "GeoSite 规则分组", "GeoSite Group", "cn / openai / google", "cn / openai / google"},
    {"IP-CIDR", "IPv4 网段 (CIDR)", "IPv4 CIDR", "192.168.1.0/24", "192.168.1.0/24"},
    {"IP-CIDR6", "IPv6 网段 (CIDR)", "IPv6 CIDR", "240e::/20", "240e::/20"},
    {"GEOIP", "GeoIP 国家地区", "GeoIP Country", "CN / US", "CN / US"},
    {"DST-PORT", "目标端口", "Dest Port", "443 / 8080", "443 / 8080"},
    {"SRC-IP-CIDR", "来源内网 IP", "Source IP CIDR", "10.0.0.0/8", "10.0.0.0/8"},
    {"RULE-SET", "外部规则集名称", "Rule-Set Name", "clashfx-ai-lock", "clashfx-ai-lock"},
    {"MATCH", "兜底默认规则", "Fallback MATCH", "<匹配所有剩余流量>", "<Matches remaining traffic>"},
};

QString payloadPlaceholderForType(const QString& typeCode) {
    const QString upper = typeCode.trimmed().toUpper();
    for (const auto& m : kRuleTypes) {
        if (upper == QLatin1String(m.code)) {
            return uiText(m.placeholderZh, m.placeholderEn);
        }
    }
    return QStringLiteral("example.com");
}

void styleTableDark(QTableWidget* table) {
    table->setSelectionBehavior(QAbstractItemView::SelectRows);
    table->setSelectionMode(QAbstractItemView::SingleSelection);
    table->setEditTriggers(QAbstractItemView::NoEditTriggers);
    table->setAlternatingRowColors(false);
    table->setShowGrid(false);
    table->setFocusPolicy(Qt::NoFocus);
    table->verticalHeader()->setVisible(false);
    table->horizontalHeader()->setHighlightSections(false);
    table->horizontalHeader()->setDefaultAlignment(Qt::AlignLeft | Qt::AlignVCenter);
    table->setStyleSheet(QStringLiteral(
        "QTableWidget { background: transparent; border: none; color: #e5e7eb;"
        "  selection-background-color: #192238; selection-color: #f9fafb; }"
        "QTableWidget::item { border-bottom: 1px solid #1f222d; padding: 4px 8px; }"
        "QTableWidget::item:selected { background: #192238; }"
        "QHeaderView::section { background: #14161e; color: #8b93a6; font-size: 11px; font-weight: 600;"
        "  border: none; border-bottom: 1px solid #262a36; padding: 8px 10px; }"));
}

PillTone sourcePillTone(const QString& source) {
    if (source == QLatin1String("system")) {
        return PillTone::Green;
    }
    if (source == QLatin1String("ai_lock")) {
        return PillTone::Violet;
    }
    if (source == QLatin1String("cn_apps") || source == QLatin1String("mixin")) {
        return PillTone::Blue;
    }
    return PillTone::Muted;
}

QString sourceDisplayLabel(const QString& source) {
    if (source == QLatin1String("system")) {
        return uiText("系统守护", "System");
    }
    if (source == QLatin1String("ai_lock")) {
        return uiText("AI 锁定", "AI Lock");
    }
    if (source == QLatin1String("cn_apps")) {
        return uiText("CN 直连", "CN Apps");
    }
    if (source == QLatin1String("mixin")) {
        return uiText("Mixin 补丁", "Mixin");
    }
    return uiText("订阅规则", "Profile");
}

}  // namespace

// ===========================================================================
// RuleEditorPage
// ===========================================================================

RuleEditorPage::RuleEditorPage(DaemonClient* client, QWidget* parent)
    : QWidget(parent)
    , client_(client) {
    auto* outerLay = new QVBoxLayout(this);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    // Header bar
    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(this);
    titleLabel_ = hdr.titleLabel;
    preservePill_ = hdr.primaryBadge;
    statusPill_ = hdr.secondaryBadge;
    subtitleLabel_ = hdr.subtitleLabel;

    tabSwitch_ = new SegmentedControl(true, this);
    tabSwitch_->setCompact(true);
    tabSwitch_->setItems({QStringLiteral("1"), QStringLiteral("2"), QStringLiteral("3")});

    reloadBtn_ = new QPushButton(this);
    reloadBtn_->setProperty("ghost", true);
    reloadBtn_->setFixedSize(34, 32);
    reloadBtn_->setStyleSheet(QStringLiteral("QPushButton { padding: 0px; }"));
    reloadBtn_->setIcon(
        QIcon(renderVectorIcon(VectorIcon::Refresh, 15, QColor(QStringLiteral("#9ca3af")), devicePixelRatioF())));
    reloadBtn_->setIconSize(QSize(15, 15));
    saveBtn_ = new QPushButton(this);
    saveBtn_->setProperty("primary", true);

    hdr.actionsLayout->addWidget(tabSwitch_);
    hdr.actionsLayout->addWidget(reloadBtn_);
    hdr.actionsLayout->addWidget(saveBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(this);
    auto* root = new QVBoxLayout(body);
    root->setContentsMargins(24, 16, 24, 18);
    root->setSpacing(14);
    outerLay->addWidget(body, 1);

    subStack_ = new QStackedWidget(body);
    root->addWidget(subStack_, 1);

    // -----------------------------------------------------------------------
    // Sub-tab 0: Visual Mixin Rules
    // -----------------------------------------------------------------------
    auto* visualPage = new QWidget(subStack_);
    auto* visualLay = new QVBoxLayout(visualPage);
    visualLay->setContentsMargins(0, 0, 0, 0);
    visualLay->setSpacing(14);

    auto* quickCardsRow = new QHBoxLayout();
    quickCardsRow->setSpacing(14);

    auto makeQuickCard = [&](QLabel*& titleLbl, BadgePill*& recPill, QLabel*& descLbl, ToggleSwitch*& sw) {
        auto* card = createCardFrame(visualPage);
        auto* h = new QHBoxLayout(card);
        h->setContentsMargins(16, 14, 16, 14);
        h->setSpacing(14);
        auto* col = new QVBoxLayout();
        col->setSpacing(6);
        auto* top = new QHBoxLayout();
        top->setSpacing(8);
        titleLbl = new QLabel(card);
        titleLbl->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
        recPill = new BadgePill(card, true);
        top->addWidget(titleLbl);
        top->addWidget(recPill);
        top->addStretch(1);

        descLbl = new QLabel(card);
        descLbl->setWordWrap(true);
        descLbl->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; line-height: 1.4;"));

        col->addLayout(top);
        col->addWidget(descLbl);
        sw = new ToggleSwitch(ToggleSwitch::Accent::Emerald, card);
        h->addLayout(col, 1);
        h->addWidget(sw, 0, Qt::AlignVCenter);
        return card;
    };

    quickCardsRow->addWidget(makeQuickCard(cnCardTitle_, cnRecPill_, cnCardDesc_, cnSwitch_), 1);
    quickCardsRow->addWidget(makeQuickCard(lanCardTitle_, lanRecPill_, lanCardDesc_, lanSwitch_), 1);
    visualLay->addLayout(quickCardsRow);

    // Custom Rules Card
    auto* customCard = createCardFrame(visualPage);
    auto* customLay = new QVBoxLayout(customCard);
    customLay->setContentsMargins(16, 14, 16, 14);
    customLay->setSpacing(12);

    auto* customTop = new QHBoxLayout();
    customTop->setSpacing(12);
    auto* customTextCol = new QVBoxLayout();
    customTextCol->setSpacing(3);
    customTitle_ = new QLabel(customCard);
    customTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
    customHint_ = new QLabel(customCard);
    customHint_->setStyleSheet(QStringLiteral("color: #8b93a6; font-size: 11px;"));
    customTextCol->addWidget(customTitle_);
    customTextCol->addWidget(customHint_);
    customTop->addLayout(customTextCol, 1);

    addRuleBtn_ = new QPushButton(customCard);
    customTop->addWidget(addRuleBtn_, 0, Qt::AlignTop);
    customLay->addLayout(customTop);

    customTable_ = new QTableWidget(0, 4, customCard);
    styleTableDark(customTable_);
    customTable_->verticalHeader()->setDefaultSectionSize(48);
    QHeaderView* ch = customTable_->horizontalHeader();
    ch->setSectionResizeMode(0, QHeaderView::Fixed);
    customTable_->setColumnWidth(0, 184);
    ch->setSectionResizeMode(1, QHeaderView::Stretch);
    ch->setSectionResizeMode(2, QHeaderView::Fixed);
    customTable_->setColumnWidth(2, 212);
    ch->setSectionResizeMode(3, QHeaderView::Fixed);
    customTable_->setColumnWidth(3, 168);  // >= 162px to prevent action button wrapping
    customLay->addWidget(customTable_, 1);

    visualLay->addWidget(customCard, 1);
    subStack_->addWidget(visualPage);

    // -----------------------------------------------------------------------
    // Sub-tab 1: YAML Patch Source
    // -----------------------------------------------------------------------
    auto* yamlPage = new QWidget(subStack_);
    auto* yamlLay = new QVBoxLayout(yamlPage);
    yamlLay->setContentsMargins(0, 0, 0, 0);
    yamlLay->setSpacing(12);

    extraKeysBanner_ = createCardFrame(yamlPage, QStringLiteral("ExtraKeysCard"));
    extraKeysBanner_->setStyleSheet(QStringLiteral(
        "QFrame#ExtraKeysCard { background: #231e12; border: 1px solid #634918; border-radius: 10px; }"));
    auto* exLay = new QHBoxLayout(extraKeysBanner_);
    exLay->setContentsMargins(14, 10, 14, 10);
    extraKeysLbl_ = new QLabel(extraKeysBanner_);
    extraKeysLbl_->setWordWrap(true);
    extraKeysLbl_->setStyleSheet(QStringLiteral("color: #fbbf24; font-size: 12px;"));
    exLay->addWidget(extraKeysLbl_);
    extraKeysBanner_->setVisible(false);
    yamlLay->addWidget(extraKeysBanner_);

    auto* yamlCard = createCardFrame(yamlPage);
    auto* ycLay = new QVBoxLayout(yamlCard);
    ycLay->setContentsMargins(16, 14, 16, 14);
    ycLay->setSpacing(10);
    yamlHintLbl_ = new QLabel(yamlCard);
    yamlHintLbl_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px;"));
    yamlHintLbl_->setWordWrap(true);

    yamlEdit_ = new QPlainTextEdit(yamlCard);
    yamlEdit_->setStyleSheet(QStringLiteral(
        "QPlainTextEdit { background: #101218; border: 1px solid #262a36; border-radius: 8px;"
        "  padding: 12px; color: #e5e7eb; font-family: Consolas, 'Cascadia Mono', monospace; font-size: 12px; }"));
    ycLay->addWidget(yamlHintLbl_);
    ycLay->addWidget(yamlEdit_, 1);
    yamlLay->addWidget(yamlCard, 1);
    subStack_->addWidget(yamlPage);

    // -----------------------------------------------------------------------
    // Sub-tab 2: Effective Rules & Hot-Reload .list Providers
    // -----------------------------------------------------------------------
    auto* effPage = new QWidget(subStack_);
    auto* effLay = new QVBoxLayout(effPage);
    effLay->setContentsMargins(0, 0, 0, 0);
    effLay->setSpacing(12);

    effSplitter_ = new QSplitter(Qt::Vertical, effPage);
    effSplitter_->setChildrenCollapsible(false);
    effSplitter_->setHandleWidth(8);
    effSplitter_->setStyleSheet(QStringLiteral(
        "QSplitter::handle:vertical { background: #252938; border-radius: 3px; margin: 2px 160px; }"
        "QSplitter::handle:vertical:hover { background: #3b82f6; }"));

    // Top half: Active top-level rules table
    auto* topCard = createCardFrame(effSplitter_);
    topCard->setMinimumHeight(185);
    auto* topLay = new QVBoxLayout(topCard);
    topLay->setContentsMargins(16, 12, 16, 12);
    topLay->setSpacing(10);

    auto* effFilterRow = new QHBoxLayout();
    effFilterRow->setSpacing(10);
    effSearchEdit_ = new QLineEdit(topCard);
    effSearchEdit_->setClearButtonEnabled(true);
    effSearchEdit_->setMinimumWidth(240);
    effSourceCombo_ = new QComboBox(topCard);
    effSourceCombo_->setMinimumWidth(148);
    effCountPill_ = new BadgePill(topCard, true);
    effRefreshBtn_ = new QPushButton(topCard);
    effFilterRow->addWidget(effSearchEdit_, 1);
    effFilterRow->addWidget(effSourceCombo_);
    effFilterRow->addWidget(effCountPill_);
    effFilterRow->addWidget(effRefreshBtn_);
    topLay->addLayout(effFilterRow);

    effTable_ = new QTableWidget(0, 7, topCard);
    styleTableDark(effTable_);
    effTable_->verticalHeader()->setDefaultSectionSize(38);
    effTable_->setItemDelegateForColumn(1, new PillItemDelegate(8, effTable_));
    QHeaderView* eh = effTable_->horizontalHeader();
    eh->setSectionResizeMode(0, QHeaderView::Fixed);
    effTable_->setColumnWidth(0, 58);
    eh->setSectionResizeMode(1, QHeaderView::Fixed);
    effTable_->setColumnWidth(1, 106);
    eh->setSectionResizeMode(2, QHeaderView::Fixed);
    effTable_->setColumnWidth(2, 118);
    eh->setSectionResizeMode(3, QHeaderView::Stretch);
    eh->setSectionResizeMode(4, QHeaderView::Fixed);
    effTable_->setColumnWidth(4, 144);
    eh->setSectionResizeMode(5, QHeaderView::Fixed);
    effTable_->setColumnWidth(5, 84);
    eh->setSectionResizeMode(6, QHeaderView::Fixed);
    effTable_->setColumnWidth(6, 108);
    topLay->addWidget(effTable_, 1);
    effSplitter_->addWidget(topCard);

    // Bottom half: Editable local .list rule-providers hot update
    rsCard_ = createCardFrame(effSplitter_, QStringLiteral("RulesetEditorCard"));
    rsCard_->setMinimumHeight(235);
    auto* rsLay = new QVBoxLayout(rsCard_);
    rsLay->setContentsMargins(16, 12, 16, 12);
    rsLay->setSpacing(10);

    auto* rsTop = new QHBoxLayout();
    rsTop->setSpacing(10);
    rsTitleLbl_ = new QLabel(rsCard_);
    rsTitleLbl_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
    rsProviderCombo_ = new QComboBox(rsCard_);
    rsProviderCombo_->setMinimumWidth(200);
    rsBehaviorPill_ = new BadgePill(rsCard_, true);
    rsCountPill_ = new BadgePill(rsCard_, true);
    rsPathLbl_ = new QLabel(rsCard_);
    rsPathLbl_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px; font-family: Consolas, monospace;"));
    rsResetBtn_ = new QPushButton(rsCard_);
    rsSaveBtn_ = new QPushButton(rsCard_);
    rsSaveBtn_->setProperty("primary", true);

    rsTop->addWidget(rsTitleLbl_);
    rsTop->addWidget(rsProviderCombo_);
    rsTop->addWidget(rsBehaviorPill_);
    rsTop->addWidget(rsCountPill_);
    rsTop->addWidget(rsPathLbl_, 1);
    rsTop->addWidget(rsResetBtn_);
    rsTop->addWidget(rsSaveBtn_);
    rsLay->addLayout(rsTop);

    rsEdit_ = new QPlainTextEdit(rsCard_);
    rsEdit_->setMinimumHeight(145);
    rsEdit_->setStyleSheet(QStringLiteral(
        "QPlainTextEdit { background: #101218; border: 1px solid #262a36; border-radius: 8px;"
        "  padding: 10px; color: #e5e7eb; font-family: Consolas, 'Cascadia Mono', monospace; font-size: 12px; }"));
    rsLay->addWidget(rsEdit_, 1);
    effSplitter_->addWidget(rsCard_);
    effSplitter_->setStretchFactor(0, 1);
    effSplitter_->setStretchFactor(1, 1);
    effSplitter_->setSizes({300, 290});
    effLay->addWidget(effSplitter_, 1);

    subStack_->addWidget(effPage);

    // Connections
    connect(tabSwitch_, &SegmentedControl::currentIndexChanged, this, &RuleEditorPage::syncSubTabSwitch);
    connect(reloadBtn_, &QPushButton::clicked, this, [this]() {
        if (currentTab_ == 2) {
            loadEffectiveRules();
        } else {
            loadProfileMixin();
        }
    });
    connect(saveBtn_, &QPushButton::clicked, this, &RuleEditorPage::saveMixin);
    connect(addRuleBtn_, &QPushButton::clicked, this, [this]() {
        addCustomRuleRow({QStringLiteral("DOMAIN-SUFFIX"), QString(), QStringLiteral("PROXY")}, 0);
        setDirty(true);
    });
    connect(cnSwitch_, &ToggleSwitch::toggled, this, [this](bool) {
        if (!loading_) {
            setDirty(true);
        }
    });
    connect(lanSwitch_, &ToggleSwitch::toggled, this, [this](bool) {
        if (!loading_) {
            setDirty(true);
        }
    });
    connect(yamlEdit_, &QPlainTextEdit::textChanged, this, [this]() {
        if (!loading_) {
            setDirty(true);
        }
    });

    connect(effSearchEdit_, &QLineEdit::textChanged, this, [this](const QString&) { renderEffectiveRulesTable(); });
    connect(effSourceCombo_, &QComboBox::currentIndexChanged, this, [this](int) { renderEffectiveRulesTable(); });
    connect(effRefreshBtn_, &QPushButton::clicked, this, [this]() { loadEffectiveRules(); });

    connect(effTable_, &QTableWidget::itemChanged, this, [this](QTableWidgetItem* item) {
        if (togglingRule_ || loading_ || !item || item->column() != 0) {
            return;
        }
        const QString source = item->data(Qt::UserRole + 2).toString();
        const QString payload = item->data(Qt::UserRole + 3).toString();
        if (source == QLatin1String("system") || payload == QLatin1String("clashfx-internal-direct")) {
            togglingRule_ = true;
            item->setCheckState(Qt::Checked);
            togglingRule_ = false;
            ToastOverlay::show(window(),
                               uiText("系统防环回守护规则 (clashfx-internal-direct) 不可禁用",
                                      "Loop-prevention rule (clashfx-internal-direct) cannot be disabled"),
                               PillTone::Amber);
            return;
        }

        bool okIdx = false;
        const int ruleIdx = item->data(Qt::UserRole + 1).toInt(&okIdx);
        if (!okIdx || ruleIdx < 0 || !client_) {
            return;
        }
        const bool wantDisabled = (item->checkState() != Qt::Checked);

        QJsonArray indices;
        indices.append(ruleIdx);
        QJsonObject params;
        params.insert(QStringLiteral("indices"), indices);
        params.insert(QStringLiteral("disabled"), wantDisabled);

        togglingRule_ = true;
        client_->call(QStringLiteral("set_rule_disabled"), params,
                      [this, source, wantDisabled](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                          togglingRule_ = false;
                          if (!ok) {
                              ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                              loadEffectiveRules();
                              return;
                          }
                          effectiveRulesView_ = data.toObject();
                          renderEffectiveRulesTable();
                          if (source == QLatin1String("ai_lock")) {
                              ToastOverlay::show(
                                  window(),
                                  uiText("已联动切换双条 AI 锁定规则状态并零断流生效",
                                         "Synced both AI-lock rules live without disconnect"),
                                  PillTone::Violet);
                          } else {
                              ToastOverlay::show(
                                  window(),
                                  wantDisabled ? uiText("已动态禁用该顶层规则", "Rule disabled live")
                                               : uiText("已动态启用该顶层规则", "Rule enabled live"),
                                  PillTone::Green);
                          }
                      });
    });

    connect(effTable_, &QTableWidget::cellClicked, this, [this](int row, int col) {
        if (col == 0) {
            return;
        }
        QTableWidgetItem* chk = effTable_->item(row, 0);
        if (!chk) {
            return;
        }
        const QString type = chk->data(Qt::UserRole + 4).toString();
        const QString payload = chk->data(Qt::UserRole + 3).toString();
        if (type.compare(QLatin1String("RuleSet"), Qt::CaseInsensitive) == 0 &&
            ruleProvidersMap_.contains(payload) && !rulesetDirty_) {
            selectProviderFromTable(payload, false);
        }
    });

    connect(effTable_, &QTableWidget::cellDoubleClicked, this, [this](int row, int col) {
        if (col == 0) {
            return;
        }
        QTableWidgetItem* chk = effTable_->item(row, 0);
        if (!chk) {
            return;
        }
        const QString source = chk->data(Qt::UserRole + 2).toString();
        const QString payload = chk->data(Qt::UserRole + 3).toString();
        const QString type = chk->data(Qt::UserRole + 4).toString();
        if (type.compare(QLatin1String("RuleSet"), Qt::CaseInsensitive) == 0 &&
            ruleProvidersMap_.contains(payload)) {
            selectProviderFromTable(payload, true);
        } else if (source == QLatin1String("mixin")) {
            tabSwitch_->setCurrentIndex(0, true);
        }
    });

    connect(rsProviderCombo_, &QComboBox::currentTextChanged, this, [this](const QString& name) {
        if (!loading_ && !name.isEmpty()) {
            currentProviderName_ = name;
            loadSelectedProviderIntoEditor();
        }
    });
    connect(rsEdit_, &QPlainTextEdit::textChanged, this, [this]() {
        if (!loading_) {
            setRulesetDirty(true);
        }
    });
    connect(rsSaveBtn_, &QPushButton::clicked, this, [this]() { saveSelectedRuleProvider(); });
    connect(rsResetBtn_, &QPushButton::clicked, this, [this]() { resetSelectedRuleProvider(); });
    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });

    retranslateUi();
    if (client_) {
        updateSnapshot(client_->snapshot());
    }
}

void RuleEditorPage::onPageActivated(int subTab) {
    if (subTab >= 0 && subTab <= 2 && subTab != currentTab_) {
        tabSwitch_->setCurrentIndex(subTab, false);
        syncSubTabSwitch(subTab);
    }
    if (!dirty_ && !rulesetDirty_) {
        loadProfileMixin();
        if (currentTab_ == 2) {
            loadEffectiveRules();
        }
    }
    if (currentTab_ == 2) {
        QTimer::singleShot(0, this, &RuleEditorPage::ensureRulesetEditorVisible);
    }
}

void RuleEditorPage::setDirty(bool dirty) {
    dirty_ = dirty;
    if (dirty_) {
        statusPill_->setPill(uiText("有未保存改动", "Unsaved Changes"), PillTone::Amber);
    } else if (hasExtraKeys_) {
        statusPill_->setPill(uiText("含自定义 YAML 附加键", "Extra YAML Keys Present"), PillTone::Violet);
    } else {
        statusPill_->setPill(QString(), PillTone::Muted);
    }
    const bool showMixinSave = (currentTab_ != 2) || dirty_;
    const bool connected = client_ && client_->isConnected();
    saveBtn_->setVisible(showMixinSave);
    saveBtn_->setEnabled(connected && showMixinSave);
}

void RuleEditorPage::setRulesetDirty(bool dirty) {
    rulesetDirty_ = dirty;
}

void RuleEditorPage::retranslateUi() {
    titleLabel_->setText(uiText("分流规则与全局配置补丁 (Profile Mixin)", "Routing Rules & Profile Mixin"));
    preservePill_->setPill(uiText("切换订阅自动保留", "Preserved Across Profiles"), PillTone::Blue);
    subtitleLabel_->setText(uiText(
        "可视化配置常用直连与自定义分流，或实时开关顶层规则、零断流热更新本地 .list 规则集",
        "Manage CN/LAN bypass, top-priority custom rules, raw Mixin YAML, and live hot-reload .list rulesets"));

    const int effCount = effectiveRulesView_.value(QStringLiteral("total_rules")).toInt(0);
    tabSwitch_->setItemText(0, uiText("可视化分流", "Visual Rules"));
    tabSwitch_->setItemText(1, uiText("YAML 补丁源码", "YAML Source"));
    tabSwitch_->setItemText(2, effCount > 0 ? (uiText("当前生效与热更新 (", "Live Rules (") +
                                               QString::number(effCount) + QStringLiteral(")"))
                                            : uiText("当前生效与热更新", "Live Rules"));

    reloadBtn_->setToolTip(uiText("重新读取配置", "Reload Configuration"));
    saveBtn_->setText(uiText("保存并立即生效", "Save & Apply"));

    cnCardTitle_->setText(uiText("🇨🇳 国内网站与国内 IP 自动直连", "🇨🇳 Bypass CN Websites & IPs"));
    cnRecPill_->setPill(uiText("推荐", "Recommended"), PillTone::Green);
    cnCardDesc_->setText(uiText(
        "百度、淘宝、B站、知乎等 11.8 万+ 国内域名与 CN IP 直接连接，不绕海外代理、不弹安全验证 (GEOSITE,cn + GEOIP,CN)",
        "Route Chinese domains and CN CIDRs directly without proxy detour (GEOSITE,cn + GEOIP,CN)"));

    lanCardTitle_->setText(uiText("🏠 局域网与私有内网直连", "🏠 Bypass LAN & Private Networks"));
    lanRecPill_->setPill(uiText("推荐", "Recommended"), PillTone::Green);
    lanCardDesc_->setText(uiText(
        "路由器后台、NAS、打印机及 192.168.x.x / 10.x.x.x / .local 域名直接连接 (GEOSITE,private + GEOIP,LAN)",
        "Keep router, NAS, printer & 192.168/10.x/.local traffic direct (GEOSITE,private + GEOIP,LAN)"));

    customTitle_->setText(uiText("自定义网站与客户端软件分流规则（按从上到下优先于订阅规则匹配）",
                                 "Custom Domain, App Process & IP Rules (Evaluated Before Profile Rules)"));
    customHint_->setText(uiText(
        "直接粘贴完整网址也会自动提取域名；进程名规则在增强模式 (TUN) 下自动生效",
        "Full URLs are automatically stripped to domain hosts; PROCESS-NAME rules work automatically under Enhanced TUN"));
    addRuleBtn_->setText(uiText("+ 添加一条规则", "+ Add Rule"));

    customTable_->setHorizontalHeaderLabels({
        uiText("匹配方式", "Match Type"),
        uiText("网址域名 / 软件进程 (.exe) / IP 网段", "Domain / Process (.exe) / IP CIDR"),
        uiText("走哪个出口", "Target Outbound"),
        uiText("优先级与操作", "Order & Remove"),
    });

    extraKeysLbl_->setText(uiText(
        "检测到 Mixin 中还包含额外高级配置键（如 dns / proxy-groups / tun 覆盖），可视化编辑保存后将原样保留这些扩展键。",
        "Additional top-level Mixin keys (e.g. dns / proxy-groups) exist and will be preserved intact on Visual save."));
    yamlHintLbl_->setText(uiText(
        "高级用户可直接编辑 `.profile_mixin.yaml` 完整源码；切回“可视化分流”时会自动校验并解析常用规则。",
        "Edit the raw `.profile_mixin.yaml` document directly; switching back to Visual Rules parses and validates YAML."));

    effSearchEdit_->setPlaceholderText(uiText("搜索生效规则（类型 / 域名 / 策略 / 来源）...",
                                              "Filter active rules (type / payload / proxy / source)..."));
    {
        QSignalBlocker sb(effSourceCombo_);
        const int prevIdx = effSourceCombo_->currentIndex();
        effSourceCombo_->clear();
        effSourceCombo_->addItem(uiText("全部规则来源", "All Sources"), QStringLiteral("all"));
        effSourceCombo_->addItem(uiText("Mixin 补丁", "Mixin Patch"), QStringLiteral("mixin"));
        effSourceCombo_->addItem(uiText("AI 专用锁定", "AI Lock"), QStringLiteral("ai_lock"));
        effSourceCombo_->addItem(uiText("CN 应用直连", "CN Apps"), QStringLiteral("cn_apps"));
        effSourceCombo_->addItem(uiText("订阅原规则", "Profile Rules"), QStringLiteral("profile"));
        effSourceCombo_->addItem(uiText("系统守护", "System Guard"), QStringLiteral("system"));
        if (prevIdx >= 0 && prevIdx < effSourceCombo_->count()) {
            effSourceCombo_->setCurrentIndex(prevIdx);
        }
    }
    effRefreshBtn_->setText(uiText("刷新生效规则", "Refresh Rules"));
    effTable_->setHorizontalHeaderLabels({
        uiText("启用", "On"),
        uiText("规则来源", "Source"),
        uiText("规则类型", "Type"),
        uiText("匹配内容 (Payload)", "Payload"),
        uiText("命中策略组", "Target"),
        uiText("命中/未中", "Hits / Miss"),
        uiText("操作", "Action"),
    });

    rsTitleLbl_->setText(uiText("本地规则集热更新 (.list)", "Live .list Ruleset Editor"));
    rsResetBtn_->setText(uiText("恢复内置默认", "Reset Default"));
    rsSaveBtn_->setText(uiText("保存并零断流热更新", "Save & Hot-Reload"));
    rsEdit_->setPlaceholderText(uiText(
        "每行输入一条规则，例如 DOMAIN-SUFFIX,openai.com 或 PROCESS-NAME,cursor.exe ...",
        "One rule per line, e.g. DOMAIN-SUFFIX,openai.com or PROCESS-NAME,cursor.exe ..."));

    setDirty(dirty_);
    if (!effectiveRulesView_.isEmpty()) {
        renderEffectiveRulesTable();
    }
}

void RuleEditorPage::updateSnapshot(const QJsonObject& snapshot) {
    lastSnapshot_ = snapshot;
    const QString def = snapshot.value(QStringLiteral("default_rule_target")).toString().trimmed();
    if (!def.isEmpty()) {
        defaultTarget_ = def;
    }
    refreshProxyTargetsFromSnapshot(snapshot);
    const bool connected = client_ ? client_->isConnected() : false;
    reloadBtn_->setEnabled(connected);
    const bool showMixinSave = (currentTab_ != 2) || dirty_;
    saveBtn_->setVisible(showMixinSave);
    saveBtn_->setEnabled(connected && showMixinSave);
    effRefreshBtn_->setEnabled(connected && !loadingEffective_);
    const QJsonObject curPv = ruleProvidersMap_.value(currentProviderName_).toObject();
    rsSaveBtn_->setEnabled(connected && curPv.value(QStringLiteral("editable")).toBool(false));
    rsResetBtn_->setEnabled(connected && curPv.value(QStringLiteral("can_reset")).toBool(false));
    if (connected) {
        if (!mixinLoaded_ && !loadingMixin_ && !dirty_) {
            loadProfileMixin();
        }
        if (currentTab_ == 2 && effectiveRulesView_.isEmpty() && !loadingEffective_) {
            loadEffectiveRules();
        }
    }
}

void RuleEditorPage::refreshProxyTargetsFromSnapshot(const QJsonObject& snapshot) {
    QStringList newGroups;
    const QJsonArray groups = snapshot.value(QStringLiteral("proxy_groups")).toArray();
    for (const QJsonValue& gVal : groups) {
        const QString name = gVal.toObject().value(QStringLiteral("name")).toString().trimmed();
        if (!name.isEmpty() && !newGroups.contains(name)) {
            newGroups.push_back(name);
        }
    }
    if (newGroups == proxyGroupChoices_) {
        return;
    }
    proxyGroupChoices_ = newGroups;

    for (int r = 0; r < customTable_->rowCount(); ++r) {
        if (auto* combo = qobject_cast<QComboBox*>(customTable_->cellWidget(r, 2))) {
            const QString curVal = combo->currentData().toString();
            QSignalBlocker sb(combo);
            combo->clear();
            combo->addItem(uiText("🚀 走代理 (跟随主策略组)", "🚀 Proxy (Primary Group)"), QStringLiteral("PROXY"));
            combo->addItem(uiText("🏠 直连 (DIRECT)", "🏠 DIRECT"), QStringLiteral("DIRECT"));
            combo->addItem(uiText("⛔ 拦截阻止 (REJECT)", "⛔ REJECT"), QStringLiteral("REJECT"));
            for (const QString& g : proxyGroupChoices_) {
                combo->addItem(uiText("🎯 策略组: ", "🎯 Group: ") + g, g);
            }
            int idx = combo->findData(curVal);
            if (idx < 0 && !curVal.isEmpty()) {
                combo->addItem(curVal, curVal);
                idx = combo->count() - 1;
            }
            combo->setCurrentIndex(idx >= 0 ? idx : 0);
            updateTargetComboStyle(combo);
        }
    }
}

void RuleEditorPage::syncSubTabSwitch(int nextIndex) {
    if (switchingTab_) {
        return;
    }
    if (nextIndex == currentTab_) {
        return;
    }

    auto setVisualTabOnly = [&](int idx) {
        currentTab_ = idx;
        subStack_->setCurrentIndex(idx);
        const bool showMixinSave = (idx != 2) || dirty_;
        saveBtn_->setVisible(showMixinSave);
        saveBtn_->setEnabled(client_ && client_->isConnected() && showMixinSave);
        if (idx == 2) {
            QTimer::singleShot(0, this, &RuleEditorPage::ensureRulesetEditorVisible);
        }
    };

    // Moving Visual (0) -> YAML (1): serialize visual form to YAML via daemon
    if (currentTab_ == 0 && nextIndex == 1 && client_ && client_->isConnected()) {
        QJsonArray arr;
        for (const auto& r : collectCustomRules()) {
            QJsonObject obj;
            obj.insert(QStringLiteral("type"), r.type);
            obj.insert(QStringLiteral("payload"), r.payload);
            obj.insert(QStringLiteral("target"), r.target);
            arr.append(obj);
        }
        QJsonObject params;
        params.insert(QStringLiteral("direction"), QStringLiteral("to_yaml"));
        params.insert(QStringLiteral("yaml"), baseYamlCache_);
        params.insert(QStringLiteral("bypass_cn_web"), cnSwitch_->isChecked());
        params.insert(QStringLiteral("bypass_lan"), lanSwitch_->isChecked());
        params.insert(QStringLiteral("rules"), arr);

        switchingTab_ = true;
        client_->call(QStringLiteral("convert_profile_mixin"), params,
                      [this, nextIndex, setVisualTabOnly](bool ok, const QJsonValue& data, const QString& err,
                                                          const QString&) {
                          switchingTab_ = false;
                          if (!ok) {
                              tabSwitch_->setCurrentIndex(0, false);
                              ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          const QJsonObject doc = data.toObject();
                          loading_ = true;
                          const QString yml = doc.value(QStringLiteral("yaml")).toString();
                          baseYamlCache_ = yml;
                          yamlEdit_->setPlainText(yml);
                          loading_ = false;
                          setVisualTabOnly(nextIndex);
                      });
        return;
    }

    // Moving YAML (1) -> Visual (0) or (2): parse YAML back to visual fields first
    if (currentTab_ == 1 && nextIndex == 0 && client_ && client_->isConnected()) {
        QJsonObject params;
        params.insert(QStringLiteral("direction"), QStringLiteral("from_yaml"));
        params.insert(QStringLiteral("yaml"), yamlEdit_->toPlainText());

        switchingTab_ = true;
        client_->call(QStringLiteral("convert_profile_mixin"), params,
                      [this, nextIndex, setVisualTabOnly](bool ok, const QJsonValue& data, const QString& err,
                                                          const QString&) {
                          switchingTab_ = false;
                          if (!ok) {
                              tabSwitch_->setCurrentIndex(1, false);
                              ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          const bool wasDirty = dirty_;
                          applyDocument(data.toObject(), true);
                          setDirty(wasDirty);
                          setVisualTabOnly(nextIndex);
                      });
        return;
    }

    setVisualTabOnly(nextIndex);
    if (nextIndex == 2) {
        loadEffectiveRules();
    }
}

void RuleEditorPage::loadProfileMixin() {
    if (!client_ || !client_->isConnected() || loadingMixin_) {
        return;
    }
    loadingMixin_ = true;
    client_->call(QStringLiteral("get_profile_mixin"), QJsonObject(),
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      loadingMixin_ = false;
                      if (!ok) {
                          ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      mixinLoaded_ = true;
                      applyDocument(data.toObject(), true);
                      setDirty(false);
                  });
}

void RuleEditorPage::applyDocument(const QJsonObject& doc, bool preserveActiveTab) {
    loading_ = true;
    baseYamlCache_ = doc.value(QStringLiteral("yaml")).toString();
    hasExtraKeys_ = doc.value(QStringLiteral("has_extra_keys")).toBool(false);
    extraKeysBanner_->setVisible(hasExtraKeys_);

    cnSwitch_->setCheckedSilent(doc.value(QStringLiteral("bypass_cn_web")).toBool(false));
    lanSwitch_->setCheckedSilent(doc.value(QStringLiteral("bypass_lan")).toBool(false));
    yamlEdit_->setPlainText(baseYamlCache_);

    QList<MixinRuleRow> rows;
    const QJsonArray arr = doc.value(QStringLiteral("rules")).toArray();
    rows.reserve(arr.size());
    for (const QJsonValue& v : arr) {
        const QJsonObject o = v.toObject();
        rows.push_back({
            o.value(QStringLiteral("type")).toString(),
            o.value(QStringLiteral("payload")).toString(),
            o.value(QStringLiteral("target")).toString(),
        });
    }
    populateCustomTable(rows);
    loading_ = false;

    if (!preserveActiveTab) {
        currentTab_ = 0;
        tabSwitch_->setCurrentIndex(0, false);
        subStack_->setCurrentIndex(0);
    }
}

void RuleEditorPage::populateCustomTable(const QList<MixinRuleRow>& rows) {
    QSignalBlocker sb(customTable_);
    customTable_->setRowCount(0);
    for (const auto& item : rows) {
        addCustomRuleRow(item, -1);
    }
}

void RuleEditorPage::addCustomRuleRow(const MixinRuleRow& item, int insertAt) {
    const int row = (insertAt >= 0 && insertAt <= customTable_->rowCount()) ? insertAt : customTable_->rowCount();
    customTable_->insertRow(row);

    // Col 0: Rule Type Combo
    auto* typeCombo = new QComboBox(customTable_);
    for (const auto& m : kRuleTypes) {
        typeCombo->addItem(uiText(m.zhLabel, m.enLabel), QString::fromLatin1(m.code));
    }
    const QString normType = item.type.trimmed().toUpper();
    int typeIdx = typeCombo->findData(normType);
    if (typeIdx < 0 && !normType.isEmpty()) {
        typeCombo->addItem(normType, normType);
        typeIdx = typeCombo->count() - 1;
    }
    typeCombo->setCurrentIndex(typeIdx >= 0 ? typeIdx : 0);
    customTable_->setCellWidget(row, 0, typeCombo);

    // Col 1: Payload Input
    auto* payloadEdit = new QLineEdit(item.payload, customTable_);
    const QString curType = typeCombo->currentData().toString();
    payloadEdit->setPlaceholderText(payloadPlaceholderForType(curType));
    payloadEdit->setEnabled(curType != QLatin1String("MATCH"));
    customTable_->setCellWidget(row, 1, payloadEdit);

    // Col 2: Target Outbound Combo
    auto* targetCombo = new QComboBox(customTable_);
    targetCombo->addItem(uiText("🚀 走代理 (跟随主策略组)", "🚀 Proxy (Primary Group)"), QStringLiteral("PROXY"));
    targetCombo->addItem(uiText("🏠 直连 (DIRECT)", "🏠 DIRECT"), QStringLiteral("DIRECT"));
    targetCombo->addItem(uiText("⛔ 拦截阻止 (REJECT)", "⛔ REJECT"), QStringLiteral("REJECT"));
    for (const QString& g : proxyGroupChoices_) {
        targetCombo->addItem(uiText("🎯 策略组: ", "🎯 Group: ") + g, g);
    }
    const QString normTarget = item.target.trimmed().isEmpty() ? QStringLiteral("PROXY") : item.target.trimmed();
    int tIdx = targetCombo->findData(normTarget);
    if (tIdx < 0) {
        targetCombo->addItem(normTarget, normTarget);
        tIdx = targetCombo->count() - 1;
    }
    targetCombo->setCurrentIndex(tIdx);
    updateTargetComboStyle(targetCombo);
    customTable_->setCellWidget(row, 2, targetCombo);

    // Col 3: Order & Remove Action Buttons
    auto* actWrap = new QWidget(customTable_);
    actWrap->setObjectName(QStringLiteral("RuleActWrap"));
    actWrap->setStyleSheet(QStringLiteral("QWidget#RuleActWrap { background: transparent; }"));
    auto* actLay = new QHBoxLayout(actWrap);
    actLay->setContentsMargins(6, 0, 6, 0);
    actLay->setSpacing(6);
    auto* upBtn = new QPushButton(QStringLiteral("↑"), actWrap);
    auto* downBtn = new QPushButton(QStringLiteral("↓"), actWrap);
    auto* delBtn = new QPushButton(uiText("删除", "Del"), actWrap);
    upBtn->setFixedSize(32, 28);
    downBtn->setFixedSize(32, 28);
    delBtn->setMinimumWidth(58);
    delBtn->setFixedHeight(28);
    delBtn->setProperty("danger", true);
    for (QPushButton* b : {upBtn, downBtn}) {
        b->setStyleSheet(QStringLiteral("padding: 0; font-size: 12px; border-radius: 6px;"));
    }
    delBtn->setStyleSheet(QStringLiteral(
        "QPushButton { background: #26161b; border: 1px solid #592632; color: #f87171;"
        "  border-radius: 6px; padding: 3px 10px; font-size: 11px; font-weight: 600; }"
        "QPushButton:hover { background: #361d25; }"));
    actLay->addWidget(upBtn);
    actLay->addWidget(downBtn);
    actLay->addWidget(delBtn);
    actLay->addStretch(1);
    customTable_->setCellWidget(row, 3, actWrap);

    auto findWidgetRow = [this](QWidget* w) -> int {
        for (int r = 0; r < customTable_->rowCount(); ++r) {
            for (int c = 0; c < customTable_->columnCount(); ++c) {
                if (customTable_->cellWidget(r, c) == w) {
                    return r;
                }
            }
        }
        return -1;
    };

    connect(typeCombo, &QComboBox::currentIndexChanged, this, [this, typeCombo, payloadEdit](int) {
        const QString tp = typeCombo->currentData().toString();
        payloadEdit->setPlaceholderText(payloadPlaceholderForType(tp));
        payloadEdit->setEnabled(tp != QLatin1String("MATCH"));
        if (!loading_) {
            setDirty(true);
        }
    });
    connect(payloadEdit, &QLineEdit::textChanged, this, [this](const QString&) {
        if (!loading_) {
            setDirty(true);
        }
    });
    connect(targetCombo, &QComboBox::currentIndexChanged, this, [this, targetCombo](int) {
        updateTargetComboStyle(targetCombo);
        if (!loading_) {
            setDirty(true);
        }
    });
    connect(upBtn, &QPushButton::clicked, this, [this, actWrap, findWidgetRow]() {
        const int r = findWidgetRow(actWrap);
        if (r >= 0) {
            moveCustomRow(r, -1);
        }
    });
    connect(downBtn, &QPushButton::clicked, this, [this, actWrap, findWidgetRow]() {
        const int r = findWidgetRow(actWrap);
        if (r >= 0) {
            moveCustomRow(r, +1);
        }
    });
    connect(delBtn, &QPushButton::clicked, this, [this, actWrap, findWidgetRow]() {
        const int r = findWidgetRow(actWrap);
        if (r >= 0) {
            removeCustomRow(r);
        }
    });
}

void RuleEditorPage::updateTargetComboStyle(QComboBox* combo) {
    const QString target = combo->currentData().toString();
    QString bg = QStringLiteral("#131d33");
    QString border = QStringLiteral("#254170");
    QString fg = QStringLiteral("#60a5fa");
    if (target == QLatin1String("DIRECT")) {
        bg = QStringLiteral("#13251f");
        border = QStringLiteral("#20523c");
        fg = QStringLiteral("#34d399");
    } else if (target == QLatin1String("REJECT")) {
        bg = QStringLiteral("#291519");
        border = QStringLiteral("#5c242d");
        fg = QStringLiteral("#f87171");
    }
    combo->setStyleSheet(QStringLiteral(
                             "QComboBox { background: %1; border: 1px solid %2; border-radius: 14px;"
                             "  color: %3; padding: 4px 12px; font-size: 12px; font-weight: 600; }"
                             "QComboBox::drop-down { border: none; width: 18px; }")
                             .arg(bg, border, fg));
}

QList<RuleEditorPage::MixinRuleRow> RuleEditorPage::collectCustomRules() const {
    QList<MixinRuleRow> list;
    list.reserve(customTable_->rowCount());
    for (int r = 0; r < customTable_->rowCount(); ++r) {
        auto* typeCombo = qobject_cast<QComboBox*>(customTable_->cellWidget(r, 0));
        auto* payloadEdit = qobject_cast<QLineEdit*>(customTable_->cellWidget(r, 1));
        auto* targetCombo = qobject_cast<QComboBox*>(customTable_->cellWidget(r, 2));
        if (!typeCombo || !payloadEdit || !targetCombo) {
            continue;
        }
        const QString tp = typeCombo->currentData().toString();
        const QString pay = payloadEdit->text().trimmed();
        if (tp != QLatin1String("MATCH") && pay.isEmpty()) {
            continue;
        }
        list.push_back({tp, pay, targetCombo->currentData().toString()});
    }
    return list;
}

void RuleEditorPage::moveCustomRow(int row, int delta) {
    const int dst = row + delta;
    if (row < 0 || dst < 0 || dst >= customTable_->rowCount()) {
        return;
    }
    auto readRow = [this](int r) -> MixinRuleRow {
        auto* tc = qobject_cast<QComboBox*>(customTable_->cellWidget(r, 0));
        auto* pe = qobject_cast<QLineEdit*>(customTable_->cellWidget(r, 1));
        auto* oc = qobject_cast<QComboBox*>(customTable_->cellWidget(r, 2));
        return {
            tc ? tc->currentData().toString() : QStringLiteral("DOMAIN-SUFFIX"),
            pe ? pe->text() : QString(),
            oc ? oc->currentData().toString() : QStringLiteral("PROXY"),
        };
    };
    const MixinRuleRow moved = readRow(row);
    loading_ = true;
    customTable_->removeRow(row);
    addCustomRuleRow(moved, dst);
    customTable_->selectRow(dst);
    loading_ = false;
    setDirty(true);
}

void RuleEditorPage::removeCustomRow(int row) {
    if (row < 0 || row >= customTable_->rowCount()) {
        return;
    }
    loading_ = true;
    customTable_->removeRow(row);
    loading_ = false;
    setDirty(true);
}

void RuleEditorPage::saveMixin() {
    if (!client_) {
        return;
    }
    QJsonObject params;
    if (currentTab_ == 1) {
        params.insert(QStringLiteral("mode"), QStringLiteral("yaml"));
        params.insert(QStringLiteral("yaml"), yamlEdit_->toPlainText());
    } else {
        QJsonArray arr;
        for (const auto& r : collectCustomRules()) {
            QJsonObject o;
            o.insert(QStringLiteral("type"), r.type);
            o.insert(QStringLiteral("payload"), r.payload);
            o.insert(QStringLiteral("target"), r.target);
            arr.append(o);
        }
        params.insert(QStringLiteral("mode"), QStringLiteral("visual"));
        params.insert(QStringLiteral("base_yaml"), baseYamlCache_);
        params.insert(QStringLiteral("bypass_cn_web"), cnSwitch_->isChecked());
        params.insert(QStringLiteral("bypass_lan"), lanSwitch_->isChecked());
        params.insert(QStringLiteral("rules"), arr);
    }

    saveBtn_->setEnabled(false);
    client_->call(QStringLiteral("save_profile_mixin"), params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      saveBtn_->setEnabled(client_ && client_->isConnected() && currentTab_ != 2);
                      if (!ok) {
                          QMessageBox::warning(window(), uiText("保存 Mixin 失败", "Save Mixin Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      applyDocument(data.toObject(), true);
                      setDirty(false);
                      ToastOverlay::show(
                          window(),
                          uiText("分流规则与全局补丁已保存并热重载生效", "Profile Mixin saved & applied"),
                          PillTone::Green);
                  });
}

void RuleEditorPage::loadEffectiveRules() {
    if (!client_ || !client_->isConnected() || loadingEffective_) {
        return;
    }
    loadingEffective_ = true;
    effRefreshBtn_->setEnabled(false);
    client_->call(QStringLiteral("get_effective_rules"), QJsonObject(),
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      loadingEffective_ = false;
                      effRefreshBtn_->setEnabled(client_ && client_->isConnected());
                      if (!ok) {
                          ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      effectiveRulesView_ = data.toObject();
                      ruleProvidersMap_ = effectiveRulesView_.value(QStringLiteral("rule_providers")).toObject();
                      retranslateUi();
                      populateRuleProviderSelector();
                  });
}

void RuleEditorPage::renderEffectiveRulesTable() {
    const QJsonArray rules = effectiveRulesView_.value(QStringLiteral("rules")).toArray();
    const QString filterText = effSearchEdit_->text().trimmed().toLower();
    const QString sourceFilter = effSourceCombo_->currentData().toString();

    struct EffRow {
        int index;
        QString source;
        QString type;
        QString payload;
        QString proxy;
        qint64 hitCount;
        qint64 missCount;
        bool disabled;
    };
    QList<EffRow> filtered;
    filtered.reserve(rules.size());

    for (const QJsonValue& v : rules) {
        const QJsonObject o = v.toObject();
        const QString src = o.value(QStringLiteral("source")).toString();
        const QString tp = o.value(QStringLiteral("type")).toString();
        const QString pay = o.value(QStringLiteral("payload")).toString();
        const QString prx = o.value(QStringLiteral("proxy")).toString();

        if (!sourceFilter.isEmpty() && sourceFilter != QLatin1String("all") && src != sourceFilter) {
            continue;
        }
        if (!filterText.isEmpty()) {
            const QString hay = (src + QLatin1Char(' ') + tp + QLatin1Char(' ') + pay + QLatin1Char(' ') + prx).toLower();
            if (!hay.contains(filterText)) {
                continue;
            }
        }
        filtered.push_back({
            o.value(QStringLiteral("index")).toInt(-1),
            src,
            tp,
            pay,
            prx,
            static_cast<qint64>(o.value(QStringLiteral("hit_count")).toDouble(0)),
            static_cast<qint64>(o.value(QStringLiteral("miss_count")).toDouble(0)),
            o.value(QStringLiteral("disabled")).toBool(false),
        });
    }

    effCountPill_->setPill(
        QString::number(filtered.size()) + QStringLiteral(" / ") + QString::number(rules.size()), PillTone::Blue);

    togglingRule_ = true;
    effTable_->setRowCount(0);
    effTable_->setRowCount(static_cast<int>(filtered.size()));
    for (int i = 0; i < filtered.size(); ++i) {
        const EffRow& r = filtered.at(i);
        const bool isSystemGuard = (r.source == QLatin1String("system") || r.payload == QLatin1String("clashfx-internal-direct"));

        auto* chk = new QTableWidgetItem();
        chk->setData(Qt::UserRole + 1, r.index);
        chk->setData(Qt::UserRole + 2, r.source);
        chk->setData(Qt::UserRole + 3, r.payload);
        chk->setData(Qt::UserRole + 4, r.type);
        if (isSystemGuard) {
            chk->setFlags(Qt::ItemIsSelectable);
            chk->setCheckState(Qt::Checked);
            chk->setToolTip(uiText("系统防环回守护规则不可禁用", "System loop-guard rule cannot be disabled"));
        } else {
            chk->setFlags(Qt::ItemIsEnabled | Qt::ItemIsSelectable | Qt::ItemIsUserCheckable);
            chk->setCheckState(r.disabled ? Qt::Unchecked : Qt::Checked);
        }
        effTable_->setItem(i, 0, chk);

        auto* srcItem = new QTableWidgetItem(sourceDisplayLabel(r.source));
        srcItem->setTextAlignment(Qt::AlignCenter);
        srcItem->setData(kPillToneRole, static_cast<int>(sourcePillTone(r.source)));
        effTable_->setItem(i, 1, srcItem);

        auto* typeItem = new QTableWidgetItem(r.type);
        typeItem->setForeground(QColor(r.disabled ? QStringLiteral("#6b7280") : QStringLiteral("#9ca3af")));
        effTable_->setItem(i, 2, typeItem);

        auto* payItem = new QTableWidgetItem(r.payload);
        payItem->setForeground(QColor(r.disabled ? QStringLiteral("#6b7280") : QStringLiteral("#f3f4f6")));
        effTable_->setItem(i, 3, payItem);

        auto* prxItem = new QTableWidgetItem(r.proxy);
        prxItem->setForeground(QColor(r.disabled ? QStringLiteral("#6b7280") : QStringLiteral("#93c5fd")));
        effTable_->setItem(i, 4, prxItem);

        auto* hitItem = new QTableWidgetItem(QStringLiteral("%1 / %2").arg(r.hitCount).arg(r.missCount));
        hitItem->setForeground(QColor(QStringLiteral("#9ca3af")));
        hitItem->setTextAlignment(Qt::AlignRight | Qt::AlignVCenter);
        effTable_->setItem(i, 5, hitItem);

        // Col 6: Action button / indicator
        const bool isRuleSetRow =
            (r.type.compare(QLatin1String("RuleSet"), Qt::CaseInsensitive) == 0 &&
             ruleProvidersMap_.contains(r.payload));
        if (isRuleSetRow || r.source == QLatin1String("mixin")) {
            auto* actWrap = new QWidget(effTable_);
            actWrap->setObjectName(QStringLiteral("EffActWrap"));
            actWrap->setStyleSheet(QStringLiteral("QWidget#EffActWrap { background: transparent; }"));
            auto* actLay = new QHBoxLayout(actWrap);
            actLay->setContentsMargins(6, 0, 6, 0);
            actLay->setAlignment(Qt::AlignCenter);

            auto* btn = new QPushButton(actWrap);
            if (isRuleSetRow) {
                const bool editable =
                    ruleProvidersMap_.value(r.payload).toObject().value(QStringLiteral("editable")).toBool(false);
                if (editable) {
                    btn->setText(uiText("编辑规则集", "Edit .list"));
                    btn->setStyleSheet(QStringLiteral(
                        "QPushButton { background: #16243e; border: 1px solid #29487d; color: #60a5fa;"
                        "  border-radius: 6px; padding: 3px 9px; font-size: 11px; font-weight: 600; min-height: 22px; }"
                        "QPushButton:hover { background: #1e3256; }"));
                    connect(btn, &QPushButton::clicked, this,
                            [this, pname = r.payload]() { selectProviderFromTable(pname, true); });
                } else {
                    btn->setText(uiText("查看只读", "Inspect"));
                    btn->setStyleSheet(QStringLiteral(
                        "QPushButton { background: #1b1e29; border: 1px solid #2f3445; color: #9ca3af;"
                        "  border-radius: 6px; padding: 3px 9px; font-size: 11px; font-weight: 600; min-height: 22px; }"
                        "QPushButton:hover { background: #252938; color: #e5e7eb; }"));
                    connect(btn, &QPushButton::clicked, this,
                            [this, pname = r.payload]() { selectProviderFromTable(pname, false); });
                }
            } else {
                btn->setText(uiText("去可视化改", "Edit Mixin"));
                btn->setStyleSheet(QStringLiteral(
                    "QPushButton { background: #1b1e29; border: 1px solid #2f3445; color: #cbd5e1;"
                    "  border-radius: 6px; padding: 3px 9px; font-size: 11px; font-weight: 600; min-height: 22px; }"
                    "QPushButton:hover { background: #252938; color: #ffffff; }"));
                connect(btn, &QPushButton::clicked, this, [this]() { tabSwitch_->setCurrentIndex(0, true); });
            }
            actLay->addWidget(btn);
            effTable_->setCellWidget(i, 6, actWrap);
        } else {
            auto* hintItem = new QTableWidgetItem(uiText("左侧可开关", "Toggleable"));
            hintItem->setForeground(QColor(QStringLiteral("#6b7280")));
            hintItem->setTextAlignment(Qt::AlignCenter);
            effTable_->setItem(i, 6, hintItem);
        }
    }
    togglingRule_ = false;
}

void RuleEditorPage::populateRuleProviderSelector() {
    QStringList editableNames;
    QStringList readonlyNames;
    for (auto it = ruleProvidersMap_.constBegin(); it != ruleProvidersMap_.constEnd(); ++it) {
        const QJsonObject pv = it.value().toObject();
        if (pv.value(QStringLiteral("editable")).toBool(false)) {
            editableNames.push_back(it.key());
        } else {
            readonlyNames.push_back(it.key());
        }
    }
    editableNames.sort();
    readonlyNames.sort();
    const QStringList allNames = editableNames + readonlyNames;

    loading_ = true;
    rsProviderCombo_->clear();
    for (const QString& n : allNames) {
        rsProviderCombo_->addItem(n, n);
    }
    if (!currentProviderName_.isEmpty() && allNames.contains(currentProviderName_)) {
        rsProviderCombo_->setCurrentText(currentProviderName_);
    } else if (!allNames.isEmpty()) {
        currentProviderName_ = allNames.first();
        rsProviderCombo_->setCurrentIndex(0);
    }
    loading_ = false;
    loadSelectedProviderIntoEditor();
}

void RuleEditorPage::loadSelectedProviderIntoEditor() {
    const QString name = rsProviderCombo_->currentText();
    currentProviderName_ = name;
    const QJsonObject pv = ruleProvidersMap_.value(name).toObject();
    const bool editable = pv.value(QStringLiteral("editable")).toBool(false);
    const bool canReset = pv.value(QStringLiteral("can_reset")).toBool(false);
    const QString behavior = pv.value(QStringLiteral("behavior")).toString();
    const int count = pv.value(QStringLiteral("rule_count")).toInt(0);
    const QString filePath = pv.value(QStringLiteral("file_path")).toString();

    const QString behText = behavior.isEmpty() ? QStringLiteral("RuleSet") : behavior;
    rsBehaviorPill_->setPill(editable ? behText : (behText + uiText(" · 只读", " · Read-Only")),
                             editable ? PillTone::Blue : PillTone::Muted);
    rsCountPill_->setPill(QString::number(count) + uiText(" 条规则", " rules"), PillTone::Green);
    rsPathLbl_->setText(rsPathLbl_->fontMetrics().elidedText(filePath, Qt::ElideMiddle, 220));
    rsPathLbl_->setToolTip(filePath);

    QString content = pv.value(QStringLiteral("raw_text")).toString();
    if (content.isEmpty()) {
        const QJsonArray pl = pv.value(QStringLiteral("payload")).toArray();
        QStringList lines;
        lines.reserve(pl.size());
        for (const QJsonValue& v : pl) {
            lines.push_back(v.toString());
        }
        content = lines.join(QLatin1Char('\n'));
    }

    loading_ = true;
    rsEdit_->setPlainText(content);
    rsEdit_->setReadOnly(!editable);
    loading_ = false;
    setRulesetDirty(false);

    const bool connected = client_ && client_->isConnected();
    rsSaveBtn_->setEnabled(connected && editable);
    rsResetBtn_->setEnabled(connected && canReset);
}

void RuleEditorPage::saveSelectedRuleProvider() {
    if (!client_ || currentProviderName_.isEmpty()) {
        return;
    }
    QJsonObject params;
    params.insert(QStringLiteral("provider_name"), currentProviderName_);
    params.insert(QStringLiteral("content"), rsEdit_->toPlainText());

    rsSaveBtn_->setEnabled(false);
    client_->call(QStringLiteral("save_rule_provider"), params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      rsSaveBtn_->setEnabled(client_ && client_->isConnected());
                      if (!ok) {
                          QMessageBox::warning(window(), uiText("热更新规则集失败", "Hot-Reload Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      effectiveRulesView_ = data.toObject();
                      ruleProvidersMap_ = effectiveRulesView_.value(QStringLiteral("rule_providers")).toObject();
                      setRulesetDirty(false);
                      renderEffectiveRulesTable();
                      loadSelectedProviderIntoEditor();
                      ToastOverlay::show(
                          window(),
                          uiText("已写入并零断流热更新规则集 ", "Saved & hot-reloaded ruleset ") +
                              currentProviderName_,
                          PillTone::Green);
                  });
}

void RuleEditorPage::resetSelectedRuleProvider() {
    if (!client_ || currentProviderName_.isEmpty()) {
        return;
    }
    const auto ans = QMessageBox::question(
        window(), uiText("恢复内置默认规则集", "Reset Ruleset to Default"),
        uiText("确认将内置规则集 ", "Reset ") + currentProviderName_ +
            uiText(" 恢复为默认内置规则并立即热更新吗？", " to built-in defaults and hot-reload now?"));
    if (ans != QMessageBox::Yes) {
        return;
    }

    QJsonObject params;
    params.insert(QStringLiteral("provider_name"), currentProviderName_);
    rsResetBtn_->setEnabled(false);
    client_->call(QStringLiteral("reset_rule_provider"), params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      rsResetBtn_->setEnabled(client_ && client_->isConnected());
                      if (!ok) {
                          QMessageBox::warning(window(), uiText("恢复默认失败", "Reset Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      effectiveRulesView_ = data.toObject();
                      ruleProvidersMap_ = effectiveRulesView_.value(QStringLiteral("rule_providers")).toObject();
                      setRulesetDirty(false);
                      renderEffectiveRulesTable();
                      loadSelectedProviderIntoEditor();
                      ToastOverlay::show(
                          window(),
                          uiText("已恢复默认规则集并零断流生效", "Reset ruleset to defaults & hot-reloaded"),
                          PillTone::Blue);
                  });
}

void RuleEditorPage::ensureRulesetEditorVisible() {
    if (!effSplitter_) {
        return;
    }
    const QList<int> sz = effSplitter_->sizes();
    const int total = sz.value(0) + sz.value(1);
    if (sz.value(1) < 235) {
        const int h = total > 420 ? total : 580;
        const int half = h * 50 / 100;
        effSplitter_->setSizes({half, h - half});
    }
}

void RuleEditorPage::selectProviderFromTable(const QString& providerName, bool focusEditor) {
    ensureRulesetEditorVisible();
    if (providerName.isEmpty() || !ruleProvidersMap_.contains(providerName)) {
        return;
    }

    if (providerName != currentProviderName_) {
        if (rulesetDirty_) {
            if (!focusEditor) {
                return;
            }
            const auto ans = QMessageBox::question(
                window(), uiText("未保存的规则集编辑", "Unsaved Ruleset Edits"),
                uiText("当前正在编辑 ", "Discard unsaved edits to ") + currentProviderName_ +
                    uiText(" 且尚未热更新，确定切换并放弃未保存改动吗？", " and switch ruleset?"));
            if (ans != QMessageBox::Yes) {
                return;
            }
        }
        const int idx = rsProviderCombo_->findData(providerName);
        if (idx >= 0) {
            QSignalBlocker sb(rsProviderCombo_);
            rsProviderCombo_->setCurrentIndex(idx);
        }
        currentProviderName_ = providerName;
        loadSelectedProviderIntoEditor();
    }

    if (focusEditor) {
        rsEdit_->setFocus();
        if (rsCard_) {
            QPointer<QFrame> cardPtr(rsCard_);
            rsCard_->setStyleSheet(QStringLiteral(
                "#RulesetEditorCard { background-color: #1a1c24; border: 1px solid #3b82f6; border-radius: 10px; }"));
            QTimer::singleShot(900, this, [cardPtr]() {
                if (cardPtr) {
                    cardPtr->setStyleSheet(QStringLiteral(
                        "#RulesetEditorCard { background-color: #1a1c24; border: 1px solid #272a36; border-radius: 10px; }"));
                }
            });
        }
        if (rsEdit_->isReadOnly()) {
            ToastOverlay::show(window(),
                               uiText("该规则集为系统内置只读保护项，仅支持预览", "This built-in guard ruleset is read-only"),
                               PillTone::Amber, 2400);
        } else {
            ToastOverlay::show(window(),
                               uiText("已在下方打开规则集编辑器：", "Opened ruleset editor below: ") + providerName,
                               PillTone::Blue, 2000);
        }
    }
}

}  // namespace clashfx
