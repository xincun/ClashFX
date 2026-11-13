#include "RuleEditorDialog.h"
#include "DaemonClient.h"
#include "I18n.h"
#include "TrayIconGenerator.h"

#include <QAbstractItemView>
#include <QCloseEvent>
#include <QDesktopServices>
#include <QFileInfo>
#include <QFontDatabase>
#include <QFrame>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QMessageBox>
#include <QSplitter>
#include <QTimer>
#include <QUrl>
#include <QVBoxLayout>

namespace clashfx {

namespace {

struct SimpleRuleTypeOption {
    const char* code;
    QString label;
};

QVector<SimpleRuleTypeOption> primaryRuleTypeOptions() {
    return {
        {"DOMAIN-SUFFIX", L("网站域名 (含子域名)", "Domain Suffix (incl. subdomains)")},
        {"DOMAIN", L("完整域名 (精确匹配)", "Exact Domain")},
        {"DOMAIN-KEYWORD", L("网址关键词", "Domain Keyword")},
        {"PROCESS-NAME", L("软件程序 (.exe)", "App Process (.exe)")},
        {"IP-CIDR", L("IP 地址或网段", "IP / CIDR Range")},
    };
}

QString friendlyTypeLabelForAdvanced(const QString& code) {
    const QString upper = code.trimmed().toUpper();
    if (upper == QStringLiteral("IP-CIDR6")) return L("IPv6 地址或网段 (IP-CIDR6)", "IPv6 CIDR");
    if (upper == QStringLiteral("PROCESS-PATH")) return L("软件完整路径 (PROCESS-PATH)", "Process Path");
    if (upper == QStringLiteral("GEOSITE")) return L("站点分类库 (GEOSITE)", "GeoSite Category");
    if (upper == QStringLiteral("GEOIP")) return L("国家/地区 IP (GEOIP)", "GeoIP Country");
    if (upper == QStringLiteral("DST-PORT")) return L("目标端口 (DST-PORT)", "Destination Port");
    if (upper == QStringLiteral("SRC-IP-CIDR")) return L("来源局域网 IP (SRC-IP-CIDR)", "Source IP CIDR");
    if (upper == QStringLiteral("DOMAIN-REGEX")) return L("域名正则 (DOMAIN-REGEX)", "Domain Regex");
    return upper;
}

QString sourceDisplayLabel(const QString& src) {
    if (src == QStringLiteral("system")) return L("系统守护", "System");
    if (src == QStringLiteral("ai_lock")) return L("AI 锁定", "AI Lock");
    if (src == QStringLiteral("cn_apps")) return L("国内桌面应用", "CN Apps");
    if (src == QStringLiteral("mixin")) return L("配置补丁", "Config Patch");
    return L("订阅规则", "Subscription");
}

QColor sourceBadgeColor(const QString& src) {
    if (src == QStringLiteral("ai_lock")) return QColor(QStringLiteral("#7c3aed"));
    if (src == QStringLiteral("mixin")) return QColor(QStringLiteral("#2563eb"));
    if (src == QStringLiteral("cn_apps")) return QColor(QStringLiteral("#059669"));
    if (src == QStringLiteral("system")) return QColor(QStringLiteral("#64748b"));
    return QColor(QStringLiteral("#475569"));
}

int variantIntOr(const QVariant& v, int fallback = -1) {
    bool ok = false;
    const int val = v.toInt(&ok);
    return ok ? val : fallback;
}

} // namespace

RuleEditorDialog::RuleEditorDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent), client_(client) {
    setWindowTitle(L("配置补丁与分流规则 (Profile Mixin)", "Config Patch & Routing Rules (Profile Mixin)"));
    setWindowIcon(TrayIconGenerator::appIcon());
    resize(940, 680);
    setMinimumSize(800, 540);

    auto* rootLayout = new QVBoxLayout(this);
    rootLayout->setContentsMargins(16, 14, 16, 14);
    rootLayout->setSpacing(10);

    headerDescLabel_ = new QLabel(this);
    headerDescLabel_->setWordWrap(true);
    headerDescLabel_->setStyleSheet(QStringLiteral(
        "background-color: #eff6ff; color: #1e3a8a; border: 1px solid #bfdbfe; "
        "border-radius: 6px; padding: 8px 12px; font-size: 12px;"));
    rootLayout->addWidget(headerDescLabel_);

    tabs_ = new QTabWidget(this);

    // ========================================================================
    // Tab 0: Visual Config Patch (Simple Switches + Inline 3-Column Table)
    // ========================================================================
    auto* visualPage = new QWidget(tabs_);
    auto* visualLayout = new QVBoxLayout(visualPage);
    visualLayout->setContentsMargins(10, 10, 10, 10);
    visualLayout->setSpacing(12);

    auto* presetBox = new QGroupBox(
        L("1. 常用直连开关（勾选后对所有订阅自动生效，更新订阅不会丢失）",
          "1. One-Click Direct Switches (Active across all subscriptions)"),
        visualPage);
    auto* presetLayout = new QVBoxLayout(presetBox);
    presetLayout->setSpacing(8);

    bypassCNWebCheck_ = new QCheckBox(
        L("国内网站与国内 IP 直连（推荐）", "Bypass Chinese Websites & CN IPs via DIRECT (Recommended)"),
        presetBox);
    bypassCNWebCheck_->setStyleSheet(QStringLiteral("font-weight: 600; font-size: 13px;"));
    auto* cnHint = new QLabel(
        L("让百度、淘宝、B站、知乎、微信网页等国内网站直接连接，不走海外代理、不触发安全验证码（自动启用 GEOSITE,cn + GEOIP,CN）",
          "Routes 118,000+ domestic domains and Chinese IPs directly without going through overseas proxies (GEOSITE,cn + GEOIP,CN)."),
        presetBox);
    cnHint->setWordWrap(true);
    cnHint->setStyleSheet(QStringLiteral("color: #64748b; font-size: 12px; margin-left: 22px;"));

    bypassLANCheck_ = new QCheckBox(
        L("局域网与内网地址直连（推荐）", "Bypass Local Network & Private IPs via DIRECT (Recommended)"),
        presetBox);
    bypassLANCheck_->setStyleSheet(QStringLiteral("font-weight: 600; font-size: 13px;"));
    auto* lanHint = new QLabel(
        L("让路由器后台、NAS、打印机及私有内网段 (192.168.x.x / 10.x.x.x / .local) 直接连接（自动启用 GEOSITE,private + GEOIP,LAN）",
          "Routes localhost, .local domains, and private LAN subnets directly (GEOSITE,private + GEOIP,LAN)."),
        presetBox);
    lanHint->setWordWrap(true);
    lanHint->setStyleSheet(QStringLiteral("color: #64748b; font-size: 12px; margin-left: 22px;"));

    presetLayout->addWidget(bypassCNWebCheck_);
    presetLayout->addWidget(cnHint);
    presetLayout->addSpacing(2);
    presetLayout->addWidget(bypassLANCheck_);
    presetLayout->addWidget(lanHint);
    visualLayout->addWidget(presetBox, 0);

    auto* rulesBox = new QGroupBox(
        L("2. 单独指定某个网站或软件怎么走（按从上到下顺序优先匹配）",
          "2. Custom Website / App Rules (Matched top-to-bottom ahead of subscription rules)"),
        visualPage);
    auto* rulesBoxLayout = new QVBoxLayout(rulesBox);
    rulesBoxLayout->setSpacing(8);

    emptyHintLabel_ = new QLabel(
        L("还没有单独添加的规则。上方两个开关已覆盖日常绝大多数分流；如果你还想单独指定某个网站或 .exe 软件走直连/代理，点击下方「+ 添加一条规则」即可。",
          "No custom rules yet. Click '+ Add Rule' below if you want a specific website or .exe app to go Direct, Proxy, or Reject."),
        rulesBox);
    emptyHintLabel_->setWordWrap(true);
    emptyHintLabel_->setStyleSheet(QStringLiteral(
        "background-color: #f8fafc; color: #475569; border: 1px dashed #cbd5e1; "
        "border-radius: 6px; padding: 14px; font-size: 12px;"));
    rulesBoxLayout->addWidget(emptyHintLabel_);

    customTable_ = new QTableWidget(0, 4, rulesBox);
    customTable_->setHorizontalHeaderLabels({
        L("匹配方式", "Match By"),
        L("网址 / 软件名 / IP", "Domain / App (.exe) / IP"),
        L("怎么走 (出口)", "Action / Outbound"),
        L("排序与删除", "Order / Remove"),
    });
    customTable_->setSelectionMode(QAbstractItemView::NoSelection);
    customTable_->verticalHeader()->setVisible(false);
    customTable_->verticalHeader()->setDefaultSectionSize(36);
    customTable_->horizontalHeader()->setSectionResizeMode(0, QHeaderView::Fixed);
    customTable_->setColumnWidth(0, 195);
    customTable_->horizontalHeader()->setSectionResizeMode(1, QHeaderView::Stretch);
    customTable_->horizontalHeader()->setSectionResizeMode(2, QHeaderView::Fixed);
    customTable_->setColumnWidth(2, 230);
    customTable_->horizontalHeader()->setSectionResizeMode(3, QHeaderView::Fixed);
    customTable_->setColumnWidth(3, 128);
    rulesBoxLayout->addWidget(customTable_, 1);

    auto* tableToolRow = new QHBoxLayout();
    addRowBtn_ = new QPushButton(L("+ 添加一条规则", "+ Add Rule"), rulesBox);
    addRowBtn_->setAutoDefault(false);
    addRowBtn_->setStyleSheet(QStringLiteral(
        "QPushButton { font-weight: 600; padding: 5px 14px; }"));
    extraKeysBadge_ = new QLabel(rulesBox);
    extraKeysBadge_->setStyleSheet(QStringLiteral("color: #64748b; font-size: 12px;"));
    tableToolRow->addWidget(addRowBtn_);
    tableToolRow->addStretch(1);
    tableToolRow->addWidget(extraKeysBadge_);
    rulesBoxLayout->addLayout(tableToolRow);

    visualLayout->addWidget(rulesBox, 1);
    tabs_->addTab(visualPage, L("可视化分流", "Visual Rules"));

    // ========================================================================
    // Tab 1: Raw YAML Editor (.profile_mixin.yaml)
    // ========================================================================
    auto* yamlPage = new QWidget(tabs_);
    auto* yamlLayout = new QVBoxLayout(yamlPage);
    yamlLayout->setContentsMargins(10, 10, 10, 10);
    yamlLayout->setSpacing(8);

    auto* yamlTopRow = new QHBoxLayout();
    yamlPathLabel_ = new QLabel(yamlPage);
    yamlPathLabel_->setStyleSheet(QStringLiteral("color: #475569; font-size: 12px;"));
    auto* openFolderBtn = new QPushButton(L("打开配置文件夹", "Open Config Folder"), yamlPage);
    openFolderBtn->setAutoDefault(false);
    yamlTopRow->addWidget(yamlPathLabel_, 1);
    yamlTopRow->addWidget(openFolderBtn);
    yamlLayout->addLayout(yamlTopRow);

    yamlEdit_ = new QPlainTextEdit(yamlPage);
    QFont mono = QFontDatabase::systemFont(QFontDatabase::FixedFont);
    mono.setPointSize(10);
    yamlEdit_->setFont(mono);
    yamlEdit_->setTabStopDistance(16);
    yamlLayout->addWidget(yamlEdit_, 1);

    tabs_->addTab(yamlPage, L("YAML 源码 (高级)", "YAML Source"));

    // ========================================================================
    // Tab 2: Live Effective Rules Inspector & Dynamic Ruleset Editor
    // ========================================================================
    auto* effPage = new QWidget(tabs_);
    auto* effLayout = new QVBoxLayout(effPage);
    effLayout->setContentsMargins(10, 10, 10, 10);
    effLayout->setSpacing(8);

    auto* effTipLabel = new QLabel(
        L("提示：① 勾选首列「启用」可实时临时开关任意顶层规则（内核重载后恢复）；"
          "② 点击内置规则集（<b>clashfx-ai-lock</b> / <b>clashfx-cn-apps-direct</b>）可在下方直接编辑本地 .list 清单并<b>零断流热更新</b>。",
          "Tip: 1) Toggle 'On / #' to dynamically enable/disable top-level rules in memory; "
          "2) Select built-in rule-sets (clashfx-ai-lock / clashfx-cn-apps-direct) to edit local .list files and hot-reload in 0ms."),
        effPage);
    effTipLabel->setWordWrap(true);
    effTipLabel->setStyleSheet(QStringLiteral(
        "background-color: #f8fafc; color: #334155; border: 1px solid #e2e8f0; "
        "border-radius: 5px; padding: 6px 10px; font-size: 12px;"));
    effLayout->addWidget(effTipLabel);

    auto* filterBar = new QHBoxLayout();
    searchEdit_ = new QLineEdit(effPage);
    searchEdit_->setPlaceholderText(L("搜索规则类型 / 域名 / 进程 / 规则集名称…",
                                      "Filter by type, domain, process, or rule-set…"));
    searchEdit_->setClearButtonEnabled(true);
    effSummaryLabel_ = new QLabel(effPage);
    refreshEffBtn_ = new QPushButton(L("刷新", "Refresh"), effPage);
    refreshEffBtn_->setAutoDefault(false);
    filterBar->addWidget(searchEdit_, 1);
    filterBar->addWidget(effSummaryLabel_);
    filterBar->addWidget(refreshEffBtn_);
    effLayout->addLayout(filterBar);

    auto* splitter = new QSplitter(Qt::Vertical, effPage);
    effTable_ = new QTableWidget(0, 6, splitter);
    effTable_->setHorizontalHeaderLabels({
        L("启用 / #", "On / #"),
        L("来源", "Source"),
        L("类型", "Type"),
        L("匹配条件 / 规则集", "Payload / Rule-Set"),
        L("出口", "Outbound"),
        L("子规则 / 命中", "Size / Hits"),
    });
    effTable_->setSelectionBehavior(QAbstractItemView::SelectRows);
    effTable_->setSelectionMode(QAbstractItemView::SingleSelection);
    effTable_->setEditTriggers(QAbstractItemView::NoEditTriggers);
    effTable_->verticalHeader()->setVisible(false);
    effTable_->horizontalHeader()->setSectionResizeMode(0, QHeaderView::ResizeToContents);
    effTable_->horizontalHeader()->setSectionResizeMode(1, QHeaderView::ResizeToContents);
    effTable_->horizontalHeader()->setSectionResizeMode(2, QHeaderView::ResizeToContents);
    effTable_->horizontalHeader()->setSectionResizeMode(3, QHeaderView::Stretch);
    effTable_->horizontalHeader()->setSectionResizeMode(4, QHeaderView::ResizeToContents);
    effTable_->horizontalHeader()->setSectionResizeMode(5, QHeaderView::ResizeToContents);

    providerDetailBox_ = new QGroupBox(
        L("规则详情与内置规则集在线编辑", "Rule Inspector & Live Ruleset Editor"),
        splitter);
    auto* detailLayout = new QVBoxLayout(providerDetailBox_);
    detailLayout->setSpacing(6);

    auto* providerToolRow = new QHBoxLayout();
    providerMetaLabel_ = new QLabel(providerDetailBox_);
    providerMetaLabel_->setWordWrap(true);
    providerMetaLabel_->setStyleSheet(QStringLiteral("color: #475569; font-size: 12px;"));

    openRulesetFolderBtn_ = new QPushButton(L("打开所在目录", "Show in Folder"), providerDetailBox_);
    openRulesetFolderBtn_->setAutoDefault(false);

    resetRulesetBtn_ = new QPushButton(L("恢复默认清单", "Reset to Default"), providerDetailBox_);
    resetRulesetBtn_->setAutoDefault(false);

    saveRulesetBtn_ = new QPushButton(L("保存并热更新规则集", "Save & Hot-Update Ruleset"), providerDetailBox_);
    saveRulesetBtn_->setAutoDefault(false);
    saveRulesetBtn_->setStyleSheet(QStringLiteral(
        "QPushButton { background-color: #059669; color: #ffffff; font-weight: 600; "
        "border: 1px solid #047857; border-radius: 5px; padding: 5px 14px; }"
        "QPushButton:hover { background-color: #047857; }"
        "QPushButton:disabled { background-color: #a7f3d0; border-color: #a7f3d0; }"));

    providerToolRow->addWidget(providerMetaLabel_, 1);
    providerToolRow->addWidget(openRulesetFolderBtn_);
    providerToolRow->addWidget(resetRulesetBtn_);
    providerToolRow->addWidget(saveRulesetBtn_);
    detailLayout->addLayout(providerToolRow);

    providerDetailEdit_ = new QPlainTextEdit(providerDetailBox_);
    providerDetailEdit_->setReadOnly(true);
    providerDetailEdit_->setFont(mono);
    providerDetailEdit_->setPlaceholderText(
        L("点击上方表格中的 clashfx-ai-lock 或 clashfx-cn-apps-direct 可直接编辑对应的本地 .list 规则集文件并热更新。",
          "Select clashfx-ai-lock or clashfx-cn-apps-direct above to edit its .list file and hot-update."));
    detailLayout->addWidget(providerDetailEdit_, 1);

    openRulesetFolderBtn_->setVisible(false);
    resetRulesetBtn_->setVisible(false);
    saveRulesetBtn_->setVisible(false);

    splitter->addWidget(effTable_);
    splitter->addWidget(providerDetailBox_);
    splitter->setStretchFactor(0, 5);
    splitter->setStretchFactor(1, 4);
    effLayout->addWidget(splitter, 1);

    tabs_->addTab(effPage, L("当前生效总览", "Active Rules"));
    rootLayout->addWidget(tabs_, 1);

    // ========================================================================
    // Shared Persistent Footer
    // ========================================================================
    auto* footer = new QHBoxLayout();
    statusLabel_ = new QLabel(this);
    saveApplyBtn_ = new QPushButton(L("保存并立即生效", "Save & Apply Now"), this);
    saveApplyBtn_->setAutoDefault(false);
    saveApplyBtn_->setDefault(false);
    saveApplyBtn_->setStyleSheet(QStringLiteral(
        "QPushButton { background-color: #2563eb; color: #ffffff; font-weight: 600; "
        "border: 1px solid #1d4ed8; border-radius: 6px; padding: 7px 20px; font-size: 13px; }"
        "QPushButton:hover { background-color: #1d4ed8; }"
        "QPushButton:disabled { background-color: #93c5fd; border-color: #93c5fd; }"));
    auto* closeBtn = new QPushButton(L("关闭", "Close"), this);
    closeBtn->setAutoDefault(false);
    closeBtn->setStyleSheet(QStringLiteral("padding: 7px 16px;"));

    footer->addWidget(statusLabel_, 1);
    footer->addWidget(saveApplyBtn_);
    footer->addWidget(closeBtn);
    rootLayout->addLayout(footer);

    // Signals
    connect(bypassCNWebCheck_, &QCheckBox::toggled, this, [this](bool) {
        if (suppressSignals_) return;
        visualModifiedSinceSync_ = true;
        setDirty(true);
    });
    connect(bypassLANCheck_, &QCheckBox::toggled, this, [this](bool) {
        if (suppressSignals_) return;
        visualModifiedSinceSync_ = true;
        setDirty(true);
    });
    connect(addRowBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onAddRuleRow);
    connect(openFolderBtn, &QPushButton::clicked, this, &RuleEditorDialog::onOpenMixinFolder);
    connect(yamlEdit_, &QPlainTextEdit::textChanged, this, [this]() {
        if (suppressSignals_) return;
        yamlModifiedSinceSync_ = true;
        setDirty(true);
    });
    connect(tabs_, &QTabWidget::currentChanged, this, &RuleEditorDialog::onTabChanged);
    connect(saveApplyBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onSaveAndApply);
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::close);

    connect(refreshEffBtn_, &QPushButton::clicked, this, &RuleEditorDialog::refreshEffectiveRules);
    connect(searchEdit_, &QLineEdit::textChanged, this, &RuleEditorDialog::onFilterEffectiveRules);
    connect(effTable_, &QTableWidget::itemSelectionChanged, this, &RuleEditorDialog::onEffectiveSelectionChanged);
    connect(effTable_, &QTableWidget::itemChanged, this, &RuleEditorDialog::onEffectiveItemChanged);

    connect(providerDetailEdit_, &QPlainTextEdit::textChanged, this, [this]() {
        if (suppressRulesetSignals_ || activeEditableProvider_.isEmpty()) return;
        setRulesetDirty(true);
    });
    connect(saveRulesetBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onSaveRulesetProvider);
    connect(resetRulesetBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onResetRulesetProvider);
    connect(openRulesetFolderBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onOpenRulesetFolder);

    setDirty(false);
    updateFooterButtonsForTab();

    if (client_) {
        connect(client_, &DaemonClient::stateChanged, this, &RuleEditorDialog::refreshFromSnapshot);
        refreshFromSnapshot(client_->snapshot());
        reloadMixinFromDaemon();
    }
}

QString RuleEditorDialog::placeholderForRuleType(const QString& ruleType) {
    const QString upper = ruleType.trimmed().toUpper();
    if (upper == QStringLiteral("DOMAIN-SUFFIX")) {
        return L("输入主域名，如 baidu.com 或 scholar.org（可直接粘贴网址）",
                 "e.g. baidu.com or scholar.org (URLs auto-cleaned)");
    }
    if (upper == QStringLiteral("DOMAIN")) {
        return L("输入完整域名，如 api.example.com", "e.g. api.example.com");
    }
    if (upper == QStringLiteral("DOMAIN-KEYWORD")) {
        return L("输入网址关键词，如 github", "e.g. github");
    }
    if (upper == QStringLiteral("PROCESS-NAME") || upper == QStringLiteral("PROCESS-PATH")) {
        return L("输入软件名，如 steam.exe（需开启增强模式 TUN）", "e.g. steam.exe (requires TUN mode)");
    }
    if (upper == QStringLiteral("IP-CIDR") || upper == QStringLiteral("IP-CIDR6")) {
        return L("输入 IP 或网段，如 192.168.50.1 或 10.0.0.0/8", "e.g. 192.168.50.1 or 10.0.0.0/8");
    }
    return L("输入匹配内容", "Enter match value");
}

void RuleEditorDialog::refreshFromSnapshot(const QJsonObject& snapshot) {
    if (snapshot.isEmpty()) return;

    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    selectedProfile_ = settings.value(QStringLiteral("selected_profile")).toString(QStringLiteral("config"));
    outboundMode_ = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toUpper();

    const QString prevDef = defaultTarget_;
    defaultTarget_ = snapshot.value(QStringLiteral("default_rule_target")).toString(QStringLiteral("DIRECT"));

    QStringList newGroups;
    for (const QJsonValue& gv : snapshot.value(QStringLiteral("proxy_groups")).toArray()) {
        const QString gName = gv.toObject().value(QStringLiteral("name")).toString().trimmed();
        if (!gName.isEmpty() && gName.compare(QStringLiteral("GLOBAL"), Qt::CaseInsensitive) != 0) {
            newGroups.append(gName);
        }
    }
    QStringList newNodes;
    for (const QJsonValue& nv : snapshot.value(QStringLiteral("ai_lock_candidates")).toArray()) {
        const QString nName = nv.toObject().value(QStringLiteral("name")).toString().trimmed();
        if (!nName.isEmpty()) {
            newNodes.append(nName);
        }
    }

    const bool targetsChanged = (prevDef != defaultTarget_) || (groupTargets_ != newGroups) || (nodeTargets_ != newNodes);
    groupTargets_ = newGroups;
    nodeTargets_ = newNodes;

    QString header = L("这里编辑的是全局<b>配置补丁 (Profile Mixin)</b>：保存后自动合并进当前及后续切换的所有订阅配置（当前订阅：<b>%1</b>，主代理组：<b>%2</b>）。",
                       "Editing global <b>Config Patch (Profile Mixin)</b>: automatically merged into all profiles at runtime (Active: <b>%1</b>, Primary Group: <b>%2</b>).")
                         .arg(selectedProfile_.toHtmlEscaped(), defaultTarget_.toHtmlEscaped());
    if (outboundMode_ != QStringLiteral("RULE")) {
        header += L("  <span style='color:#b45309;'>[注意：当前出站模式为 %1，切回 RULE 规则模式后分流才会生效]</span>",
                    "  <span style='color:#b45309;'>[Note: Outbound mode is %1; switch to RULE mode for split routing]</span>")
                      .arg(outboundMode_);
    }
    headerDescLabel_->setText(header);

    if (targetsChanged && !workingRules_.isEmpty()) {
        rebuildCustomTable();
    }
}

void RuleEditorDialog::reloadMixinFromDaemon() {
    if (!client_) return;
    client_->call(QStringLiteral("get_profile_mixin"), QJsonObject(),
                  [this](bool ok, const QJsonValue& data, const QString&, const QString&) {
                      if (!ok || !data.isObject()) return;
                      applyMixinDocument(data.toObject(), true);
                  });
}

void RuleEditorDialog::applyMixinDocument(const QJsonObject& doc, bool markClean) {
    suppressSignals_ = true;
    if (doc.contains(QStringLiteral("path"))) {
        mixinFilePath_ = doc.value(QStringLiteral("path")).toString();
        yamlPathLabel_->setText(L("补丁文件：", "File: ") + mixinFilePath_);
    }
    const QString defTgt = doc.value(QStringLiteral("default_target")).toString();
    if (!defTgt.isEmpty()) {
        defaultTarget_ = defTgt;
    }

    lastSyncedYaml_ = doc.value(QStringLiteral("yaml")).toString();
    yamlEdit_->setPlainText(lastSyncedYaml_);

    bypassCNWebCheck_->setChecked(doc.value(QStringLiteral("bypass_cn_web")).toBool(false));
    bypassLANCheck_->setChecked(doc.value(QStringLiteral("bypass_lan")).toBool(false));
    hasExtraYamlKeys_ = doc.value(QStringLiteral("has_extra_keys")).toBool(false);
    extraKeysBadge_->setText(hasExtraYamlKeys_
                                 ? L("已包含其他 YAML 补丁字段（可视化保存时自动保留）",
                                     "Contains additional YAML keys (preserved on visual save)")
                                 : QString());

    workingRules_.clear();
    for (const QJsonValue& rv : doc.value(QStringLiteral("rules")).toArray()) {
        const QJsonObject ro = rv.toObject();
        MixinRuleRow row;
        row.type = ro.value(QStringLiteral("type")).toString(QStringLiteral("DOMAIN-SUFFIX"));
        row.payload = ro.value(QStringLiteral("payload")).toString();
        row.target = ro.value(QStringLiteral("target")).toString(QStringLiteral("DIRECT"));
        workingRules_.push_back(row);
    }
    suppressSignals_ = false;

    rebuildCustomTable();

    visualModifiedSinceSync_ = false;
    yamlModifiedSinceSync_ = false;
    if (markClean) {
        setDirty(false);
    }
}

void RuleEditorDialog::populateTargetCombo(QComboBox* combo, const QString& currentTarget) const {
    combo->clear();
    combo->addItem(L("直连 (DIRECT)", "Direct (DIRECT)"), QStringLiteral("DIRECT"));
    combo->addItem(L("走代理 (跟随主策略组: %1)", "Proxy (Primary Group: %1)").arg(defaultTarget_),
                   QStringLiteral("PROXY"));
    combo->addItem(L("拦截阻止 (REJECT)", "Block (REJECT)"), QStringLiteral("REJECT"));

    if (!groupTargets_.isEmpty() || !nodeTargets_.isEmpty()) {
        combo->insertSeparator(combo->count());
    }
    for (const QString& g : groupTargets_) {
        combo->addItem(L("策略组: ", "Group: ") + g, g);
    }
    for (const QString& n : nodeTargets_) {
        combo->addItem(L("节点: ", "Node: ") + n, n);
    }

    QString norm = currentTarget.trimmed();
    if (norm.compare(QStringLiteral("PROXY"), Qt::CaseInsensitive) == 0 ||
        norm.compare(QStringLiteral("@DEFAULT"), Qt::CaseInsensitive) == 0) {
        norm = QStringLiteral("PROXY");
    } else if (norm.compare(QStringLiteral("DIRECT"), Qt::CaseInsensitive) == 0) {
        norm = QStringLiteral("DIRECT");
    } else if (norm.compare(QStringLiteral("REJECT"), Qt::CaseInsensitive) == 0) {
        norm = QStringLiteral("REJECT");
    }

    int idx = combo->findData(norm);
    if (idx < 0 && !norm.isEmpty()) {
        combo->addItem(norm, norm);
        idx = combo->count() - 1;
    }
    if (idx >= 0) {
        combo->setCurrentIndex(idx);
    }
}

void RuleEditorDialog::rebuildCustomTable(int focusRow) {
    const bool prevSuppress = suppressSignals_;
    suppressSignals_ = true;

    const int n = workingRules_.size();
    emptyHintLabel_->setVisible(n == 0);
    customTable_->setVisible(n > 0);
    customTable_->setRowCount(n);

    QLineEdit* focusEdit = nullptr;
    const auto typeOptions = primaryRuleTypeOptions();

    for (int i = 0; i < n; ++i) {
        const MixinRuleRow& r = workingRules_[i];

        auto* typeCombo = new QComboBox(customTable_);
        for (const auto& opt : typeOptions) {
            typeCombo->addItem(opt.label, QString::fromLatin1(opt.code));
        }
        const QString upperType = r.type.trimmed().toUpper();
        int tIdx = typeCombo->findData(upperType);
        if (tIdx < 0 && !upperType.isEmpty()) {
            typeCombo->addItem(friendlyTypeLabelForAdvanced(upperType), upperType);
            tIdx = typeCombo->count() - 1;
        }
        if (tIdx >= 0) {
            typeCombo->setCurrentIndex(tIdx);
        }

        auto* payloadEdit = new QLineEdit(r.payload, customTable_);
        payloadEdit->setPlaceholderText(placeholderForRuleType(upperType));
        payloadEdit->setClearButtonEnabled(true);
        if (i == focusRow) {
            focusEdit = payloadEdit;
        }

        auto* targetCombo = new QComboBox(customTable_);
        populateTargetCombo(targetCombo, r.target);

        auto* actionWrap = new QWidget(customTable_);
        auto* actionRow = new QHBoxLayout(actionWrap);
        actionRow->setContentsMargins(4, 2, 4, 2);
        actionRow->setSpacing(4);

        auto* upBtn = new QPushButton(QStringLiteral("↑"), actionWrap);
        upBtn->setFixedWidth(26);
        upBtn->setAutoDefault(false);
        upBtn->setEnabled(i > 0);
        upBtn->setToolTip(L("上移（更高优先级）", "Move up"));

        auto* downBtn = new QPushButton(QStringLiteral("↓"), actionWrap);
        downBtn->setFixedWidth(26);
        downBtn->setAutoDefault(false);
        downBtn->setEnabled(i + 1 < n);
        downBtn->setToolTip(L("下移", "Move down"));

        auto* delBtn = new QPushButton(L("删除", "Del"), actionWrap);
        delBtn->setAutoDefault(false);
        delBtn->setStyleSheet(QStringLiteral("color: #dc2626; padding: 2px 6px;"));

        actionRow->addWidget(upBtn);
        actionRow->addWidget(downBtn);
        actionRow->addWidget(delBtn);

        customTable_->setCellWidget(i, 0, typeCombo);
        customTable_->setCellWidget(i, 1, payloadEdit);
        customTable_->setCellWidget(i, 2, targetCombo);
        customTable_->setCellWidget(i, 3, actionWrap);

        connect(typeCombo, qOverload<int>(&QComboBox::currentIndexChanged), this,
                [this, i, typeCombo, payloadEdit](int) {
                    if (suppressSignals_ || i < 0 || i >= workingRules_.size()) return;
                    const QString code = typeCombo->currentData().toString();
                    workingRules_[i].type = code;
                    payloadEdit->setPlaceholderText(placeholderForRuleType(code));
                    visualModifiedSinceSync_ = true;
                    setDirty(true);
                });

        connect(payloadEdit, &QLineEdit::textEdited, this, [this, i](const QString& text) {
            if (suppressSignals_ || i < 0 || i >= workingRules_.size()) return;
            workingRules_[i].payload = text;
            visualModifiedSinceSync_ = true;
            setDirty(true);
        });

        connect(targetCombo, qOverload<int>(&QComboBox::currentIndexChanged), this,
                [this, i, targetCombo](int) {
                    if (suppressSignals_ || i < 0 || i >= workingRules_.size()) return;
                    workingRules_[i].target = targetCombo->currentData().toString();
                    visualModifiedSinceSync_ = true;
                    setDirty(true);
                });

        connect(upBtn, &QPushButton::clicked, this, [this, i]() {
            if (i <= 0 || i >= workingRules_.size()) return;
            workingRules_.swapItemsAt(i, i - 1);
            visualModifiedSinceSync_ = true;
            rebuildCustomTable(i - 1);
            setDirty(true);
        });

        connect(downBtn, &QPushButton::clicked, this, [this, i]() {
            if (i < 0 || i + 1 >= workingRules_.size()) return;
            workingRules_.swapItemsAt(i, i + 1);
            visualModifiedSinceSync_ = true;
            rebuildCustomTable(i + 1);
            setDirty(true);
        });

        connect(delBtn, &QPushButton::clicked, this, [this, i]() {
            if (i < 0 || i >= workingRules_.size()) return;
            workingRules_.removeAt(i);
            visualModifiedSinceSync_ = true;
            rebuildCustomTable();
            setDirty(true);
        });
    }

    suppressSignals_ = prevSuppress;
    if (focusEdit) {
        QTimer::singleShot(0, focusEdit, [focusEdit]() { focusEdit->setFocus(); });
    }
}

void RuleEditorDialog::onAddRuleRow() {
    MixinRuleRow row;
    row.type = QStringLiteral("DOMAIN-SUFFIX");
    row.target = bypassCNWebCheck_->isChecked() ? QStringLiteral("PROXY") : QStringLiteral("DIRECT");
    workingRules_.push_back(row);
    visualModifiedSinceSync_ = true;
    rebuildCustomTable(workingRules_.size() - 1);
    setDirty(true);
}

QJsonArray RuleEditorDialog::serializeVisualRulesJson(bool skipBlankPayload) const {
    QJsonArray arr;
    for (const MixinRuleRow& r : workingRules_) {
        const QString tp = r.type.trimmed().toUpper();
        const QString pl = r.payload.trimmed();
        if (skipBlankPayload && tp != QStringLiteral("MATCH") && pl.isEmpty()) {
            continue;
        }
        QJsonObject obj;
        obj.insert(QStringLiteral("type"), tp);
        obj.insert(QStringLiteral("payload"), pl);
        obj.insert(QStringLiteral("target"), r.target.trimmed());
        arr.append(obj);
    }
    return arr;
}

void RuleEditorDialog::updateFooterButtonsForTab() {
    if (!tabs_ || !saveApplyBtn_) return;
    if (tabs_->currentIndex() == 2) {
        saveApplyBtn_->setVisible(dirty_);
        saveApplyBtn_->setText(L("保存补丁修改", "Save Patch Changes"));
    } else {
        saveApplyBtn_->setVisible(true);
        saveApplyBtn_->setText(L("保存并立即生效", "Save & Apply Now"));
    }
}

void RuleEditorDialog::onTabChanged(int index) {
    updateFooterButtonsForTab();
    if (!client_) {
        lastTabIndex_ = index;
        return;
    }

    if (lastTabIndex_ == 0 && index == 1 && visualModifiedSinceSync_) {
        QJsonObject params;
        params.insert(QStringLiteral("direction"), QStringLiteral("to_yaml"));
        params.insert(QStringLiteral("yaml"), lastSyncedYaml_);
        params.insert(QStringLiteral("bypass_cn_web"), bypassCNWebCheck_->isChecked());
        params.insert(QStringLiteral("bypass_lan"), bypassLANCheck_->isChecked());
        params.insert(QStringLiteral("rules"), serializeVisualRulesJson(true));
        client_->call(QStringLiteral("convert_profile_mixin"), params,
                      [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                          if (!ok) {
                              QMessageBox::warning(this, windowTitle(), err);
                              return;
                          }
                          const QString built = data.toObject().value(QStringLiteral("yaml")).toString();
                          suppressSignals_ = true;
                          yamlEdit_->setPlainText(built);
                          suppressSignals_ = false;
                          lastSyncedYaml_ = built;
                          visualModifiedSinceSync_ = false;
                          yamlModifiedSinceSync_ = false;
                      });
    } else if (lastTabIndex_ == 1 && index == 0 && yamlModifiedSinceSync_) {
        QJsonObject params;
        params.insert(QStringLiteral("direction"), QStringLiteral("from_yaml"));
        params.insert(QStringLiteral("yaml"), yamlEdit_->toPlainText());
        client_->call(QStringLiteral("convert_profile_mixin"), params,
                      [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                          if (!ok) {
                              QMessageBox::warning(this, L("YAML 语法有误", "YAML Syntax Error"), err);
                              suppressSignals_ = true;
                              tabs_->setCurrentIndex(1);
                              suppressSignals_ = false;
                              lastTabIndex_ = 1;
                              updateFooterButtonsForTab();
                              return;
                          }
                          applyMixinDocument(data.toObject(), false);
                      });
    } else if (index == 2) {
        refreshEffectiveRules();
    }

    lastTabIndex_ = index;
}

void RuleEditorDialog::setDirty(bool dirty, const QString& savedHint) {
    dirty_ = dirty;
    updateFooterButtonsForTab();
    if (dirty) {
        statusLabel_->setStyleSheet(QStringLiteral("color: #d97706; font-weight: 600;"));
        statusLabel_->setText(L("● 配置补丁有未保存修改，点击右侧「保存并立即生效」即可应用",
                                "● Unsaved Config Patch changes — click 'Save & Apply Now'"));
    } else if (!savedHint.isEmpty()) {
        statusLabel_->setStyleSheet(QStringLiteral("color: #059669; font-weight: 600;"));
        statusLabel_->setText(savedHint);
    } else if (!rulesetDirty_) {
        statusLabel_->clear();
    }
}

void RuleEditorDialog::setRulesetDirty(bool dirty, const QString& savedHint) {
    rulesetDirty_ = dirty;
    if (dirty) {
        statusLabel_->setStyleSheet(QStringLiteral("color: #d97706; font-weight: 600;"));
        statusLabel_->setText(L("● 规则集 %1 已修改，点击「保存并热更新规则集」立即生效（无需重启内核）",
                                "● Ruleset %1 modified — click 'Save & Hot-Update Ruleset' to apply in 0ms")
                                  .arg(activeEditableProvider_));
    } else if (!savedHint.isEmpty()) {
        statusLabel_->setStyleSheet(QStringLiteral("color: #059669; font-weight: 600;"));
        statusLabel_->setText(savedHint);
    } else if (!dirty_) {
        statusLabel_->clear();
    }
}

void RuleEditorDialog::onSaveAndApply() {
    if (!client_) return;

    QJsonObject params;
    const bool saveAsRawYaml = (tabs_->currentIndex() == 1 && yamlModifiedSinceSync_) ||
                               (yamlModifiedSinceSync_ && !visualModifiedSinceSync_);
    if (saveAsRawYaml) {
        params.insert(QStringLiteral("mode"), QStringLiteral("yaml"));
        params.insert(QStringLiteral("yaml"), yamlEdit_->toPlainText());
    } else {
        params.insert(QStringLiteral("mode"), QStringLiteral("visual"));
        params.insert(QStringLiteral("base_yaml"), lastSyncedYaml_);
        params.insert(QStringLiteral("bypass_cn_web"), bypassCNWebCheck_->isChecked());
        params.insert(QStringLiteral("bypass_lan"), bypassLANCheck_->isChecked());
        params.insert(QStringLiteral("rules"), serializeVisualRulesJson(true));
    }

    saveApplyBtn_->setEnabled(false);
    saveApplyBtn_->setText(L("正在保存并热重载…", "Saving & Reloading…"));

    client_->call(QStringLiteral("save_profile_mixin"), params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      saveApplyBtn_->setEnabled(true);
                      updateFooterButtonsForTab();
                      if (!ok) {
                          QMessageBox::warning(this, L("保存配置补丁失败", "Failed to Save Config Patch"), err);
                          return;
                      }
                      if (data.isObject()) {
                          applyMixinDocument(data.toObject(), true);
                      }
                      setDirty(false, L("✓ 已写入配置补丁 (.profile_mixin.yaml) 并立即生效",
                                        "✓ Saved to Profile Mixin and hot-reloaded"));
                      refreshEffectiveRules();
                  });
}

void RuleEditorDialog::onOpenMixinFolder() {
    if (mixinFilePath_.isEmpty()) return;
    const QFileInfo fi(mixinFilePath_);
    QDesktopServices::openUrl(QUrl::fromLocalFile(fi.absolutePath()));
}

void RuleEditorDialog::applyEffectiveRulesData(const QJsonObject& obj, int preferredRuleIndex) {
    effectiveRules_ = obj.value(QStringLiteral("rules")).toArray();
    ruleProviders_ = obj.value(QStringLiteral("rule_providers")).toObject();
    renderEffectiveTable(preferredRuleIndex);
}

void RuleEditorDialog::refreshEffectiveRules() {
    if (!client_) return;
    int keepRuleIdx = -1;
    if (effTable_->currentRow() >= 0) {
        if (const auto* it = effTable_->item(effTable_->currentRow(), 0)) {
            const int orig = variantIntOr(it->data(Qt::UserRole), -1);
            if (orig >= 0 && orig < effectiveRules_.size()) {
                keepRuleIdx = effectiveRules_.at(orig).toObject().value(QStringLiteral("index")).toInt(-1);
            }
        }
    }
    refreshEffBtn_->setEnabled(false);
    client_->call(QStringLiteral("get_effective_rules"), QJsonObject(),
                  [this, keepRuleIdx](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      refreshEffBtn_->setEnabled(true);
                      if (!ok || !data.isObject()) {
                          effSummaryLabel_->setText(err);
                          return;
                      }
                      applyEffectiveRulesData(data.toObject(), keepRuleIdx);
                  });
}

void RuleEditorDialog::onFilterEffectiveRules(const QString&) {
    renderEffectiveTable();
}

void RuleEditorDialog::renderEffectiveTable(int preferredRuleIndex) {
    const QString kw = searchEdit_->text().trimmed().toLower();
    suppressEffectiveItemSignals_ = true;
    effTable_->setRowCount(0);

    int shown = 0;
    int disabledCount = 0;
    int rowToSelect = -1;

    for (int i = 0; i < effectiveRules_.size(); ++i) {
        const QJsonObject r = effectiveRules_.at(i).toObject();
        const int idx = r.value(QStringLiteral("index")).toInt(i);
        const QString tp = r.value(QStringLiteral("type")).toString();
        const QString payload = r.value(QStringLiteral("payload")).toString();
        const QString proxy = r.value(QStringLiteral("proxy")).toString();
        const int size = r.value(QStringLiteral("size")).toInt(-1);
        const bool disabled = r.value(QStringLiteral("disabled")).toBool(false);
        const QString src = r.value(QStringLiteral("source")).toString();
        const QString srcLabel = sourceDisplayLabel(src);
        const qint64 hitCount = static_cast<qint64>(r.value(QStringLiteral("hit_count")).toDouble(0));

        if (disabled) {
            ++disabledCount;
        }

        if (!kw.isEmpty()) {
            const QString hay = (tp + QLatin1Char(' ') + payload + QLatin1Char(' ') +
                                 proxy + QLatin1Char(' ') + srcLabel)
                                    .toLower();
            bool providerHit = false;
            if (ruleProviders_.contains(payload)) {
                const QJsonArray pArr = ruleProviders_.value(payload).toObject().value(QStringLiteral("payload")).toArray();
                for (const QJsonValue& pv : pArr) {
                    if (pv.toString().toLower().contains(kw)) {
                        providerHit = true;
                        break;
                    }
                }
            }
            if (!hay.contains(kw) && !providerHit) {
                continue;
            }
        }

        const int row = effTable_->rowCount();
        effTable_->insertRow(row);

        // Column 0: Live Enable/Disable checkbox + 1-based rule index
        auto* idxItem = new QTableWidgetItem(QString::number(idx + 1));
        idxItem->setData(Qt::UserRole, i);
        idxItem->setData(Qt::UserRole + 1, idx);
        idxItem->setCheckState(disabled ? Qt::Unchecked : Qt::Checked);
        if (src == QStringLiteral("system") || payload == QStringLiteral("clashfx-internal-direct")) {
            idxItem->setFlags(Qt::ItemIsSelectable | Qt::ItemIsEnabled);
            idxItem->setToolTip(L("系统防环回守护规则固定开启，防止内核流量死循环",
                                  "System loop-prevention guard is permanently enabled"));
        } else {
            idxItem->setFlags(Qt::ItemIsSelectable | Qt::ItemIsEnabled | Qt::ItemIsUserCheckable);
            if (payload == QStringLiteral("clashfx-ai-lock")) {
                idxItem->setToolTip(L("动态开关 AI 锁定顶层规则（配对的防泄漏 REJECT 规则会自动同步切换；内核重载后恢复）",
                                      "Dynamically toggle AI Lock rule pair in memory (restored on reload)"));
            } else {
                idxItem->setToolTip(L("勾选/取消勾选可实时启用或停用此规则（内核重载后恢复）",
                                      "Toggle live rule evaluation in Mihomo memory"));
            }
        }
        effTable_->setItem(row, 0, idxItem);

        auto* srcItem = new QTableWidgetItem(srcLabel);
        srcItem->setForeground(disabled ? QColor(QStringLiteral("#94a3b8")) : sourceBadgeColor(src));
        QFont f = srcItem->font();
        f.setBold(true);
        f.setStrikeOut(disabled);
        srcItem->setFont(f);
        effTable_->setItem(row, 1, srcItem);

        const QJsonObject provObj = ruleProviders_.value(payload).toObject();
        const bool isEditableProv = provObj.value(QStringLiteral("editable")).toBool(false);
        QString displayPayload = payload.isEmpty() ? QStringLiteral("*") : payload;
        if (isEditableProv) {
            displayPayload += L("  [可编辑 .list]", "  [Editable .list]");
        }

        auto* tpItem = new QTableWidgetItem(tp);
        auto* plItem = new QTableWidgetItem(displayPayload);
        auto* pxItem = new QTableWidgetItem(proxy);

        QString statText;
        if (disabled) {
            statText = L("已临时停用", "Disabled");
        }
        if (ruleProviders_.contains(payload)) {
            const int cnt = provObj.value(QStringLiteral("rule_count")).toInt(size);
            if (!statText.isEmpty()) statText += QStringLiteral(" | ");
            statText += L("%1 条子规则", "%1 sub-rules").arg(cnt);
        } else if (size > 0) {
            if (!statText.isEmpty()) statText += QStringLiteral(" | ");
            statText += QString::number(size);
        }
        if (hitCount > 0) {
            if (!statText.isEmpty()) statText += QStringLiteral(" | ");
            statText += L("命中 %1", "Hits %1").arg(hitCount);
        }
        auto* stItem = new QTableWidgetItem(statText);

        for (QTableWidgetItem* cell : {tpItem, plItem, pxItem, stItem}) {
            if (disabled) {
                QFont cf = cell->font();
                cf.setStrikeOut(true);
                cell->setFont(cf);
                cell->setForeground(QColor(QStringLiteral("#94a3b8")));
            }
        }
        if (isEditableProv && !disabled) {
            plItem->setForeground(QColor(QStringLiteral("#047857")));
            QFont pf = plItem->font();
            pf.setBold(true);
            plItem->setFont(pf);
        }

        effTable_->setItem(row, 2, tpItem);
        effTable_->setItem(row, 3, plItem);
        effTable_->setItem(row, 4, pxItem);
        effTable_->setItem(row, 5, stItem);

        if (preferredRuleIndex >= 0 && idx == preferredRuleIndex) {
            rowToSelect = row;
        }
        ++shown;
    }

    suppressEffectiveItemSignals_ = false;

    QString summary = L("共 %1 条顶层规则（显示 %2 条）", "%1 top-level rules (%2 shown)")
                          .arg(effectiveRules_.size())
                          .arg(shown);
    if (disabledCount > 0) {
        summary += L(" · 已临时停用 %1 条", " · %1 temporarily disabled").arg(disabledCount);
    }
    effSummaryLabel_->setText(summary);

    if (effTable_->rowCount() > 0) {
        effTable_->selectRow(rowToSelect >= 0 ? rowToSelect : 0);
    } else if (!rulesetDirty_) {
        activeEditableProvider_.clear();
        activeRulesetFilePath_.clear();
        openRulesetFolderBtn_->setVisible(false);
        resetRulesetBtn_->setVisible(false);
        saveRulesetBtn_->setVisible(false);
        providerDetailEdit_->setReadOnly(true);
        providerDetailEdit_->clear();
    }
}

void RuleEditorDialog::onEffectiveItemChanged(QTableWidgetItem* item) {
    if (suppressEffectiveItemSignals_ || !item || item->column() != 0 || !client_) return;

    const int ruleIndex = variantIntOr(item->data(Qt::UserRole + 1), -1);
    if (ruleIndex < 0) return;

    const bool wantDisabled = (item->checkState() != Qt::Checked);

    QJsonObject params;
    QJsonArray indices;
    indices.append(ruleIndex);
    params.insert(QStringLiteral("indices"), indices);
    params.insert(QStringLiteral("disabled"), wantDisabled);

    effTable_->setEnabled(false);
    client_->call(QStringLiteral("set_rule_disabled"), params,
                  [this, ruleIndex, wantDisabled](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      effTable_->setEnabled(true);
                      if (!ok) {
                          QMessageBox::warning(this, L("动态切换规则失败", "Failed to Toggle Rule"), err);
                          refreshEffectiveRules();
                          return;
                      }
                      if (data.isObject()) {
                          applyEffectiveRulesData(data.toObject(), ruleIndex);
                      }
                      statusLabel_->setStyleSheet(QStringLiteral("color: #059669; font-weight: 600;"));
                      statusLabel_->setText(
                          wantDisabled
                              ? L("✓ 已实时停用顶层规则 #%1（内存即时生效，重载配置后恢复启用）",
                                  "✓ Rule #%1 disabled live in memory (restored on reload)")
                                    .arg(ruleIndex + 1)
                              : L("✓ 已恢复启用顶层规则 #%1",
                                  "✓ Rule #%1 re-enabled")
                                    .arg(ruleIndex + 1));
                  });
}

void RuleEditorDialog::onEffectiveSelectionChanged() {
    const int row = effTable_->currentRow();
    if (row < 0) return;

    const QTableWidgetItem* idxItem = effTable_->item(row, 0);
    if (!idxItem) return;
    bool convOk = false;
    const int origIdx = idxItem->data(Qt::UserRole).toInt(&convOk);
    if (!convOk || origIdx < 0 || origIdx >= effectiveRules_.size()) return;

    const QJsonObject r = effectiveRules_.at(origIdx).toObject();
    const QString nextPayload = r.value(QStringLiteral("payload")).toString();

    // Keep unsaved .list edits intact if switching between paired clashfx-ai-lock rows;
    // prompt before discarding if switching away from an edited ruleset!
    if (rulesetDirty_ && !activeEditableProvider_.isEmpty() && nextPayload != activeEditableProvider_) {
        const auto ans = QMessageBox::question(
            this,
            windowTitle(),
            L("本地规则集 %1 有未保存的修改，确定放弃这些修改并切换查看其他规则吗？",
              "Ruleset %1 has unsaved changes. Discard them and switch rows?")
                .arg(activeEditableProvider_),
            QMessageBox::Yes | QMessageBox::No,
            QMessageBox::No);
        if (ans == QMessageBox::No) {
            if (lastSelectedEffOrigIdx_ >= 0) {
                for (int rIdx = 0; rIdx < effTable_->rowCount(); ++rIdx) {
                    if (effTable_->item(rIdx, 0) &&
                        variantIntOr(effTable_->item(rIdx, 0)->data(Qt::UserRole), -1) == lastSelectedEffOrigIdx_) {
                        effTable_->blockSignals(true);
                        effTable_->selectRow(rIdx);
                        effTable_->blockSignals(false);
                        break;
                    }
                }
            }
            return;
        }
        setRulesetDirty(false);
    }

    lastSelectedEffOrigIdx_ = origIdx;
    loadDetailForRuleObject(r, false);
}

void RuleEditorDialog::loadDetailForRuleObject(const QJsonObject& r, bool forceReloadEditor) {
    const QString tp = r.value(QStringLiteral("type")).toString();
    const QString payload = r.value(QStringLiteral("payload")).toString();
    const QString proxy = r.value(QStringLiteral("proxy")).toString();
    const QString src = sourceDisplayLabel(r.value(QStringLiteral("source")).toString());

    const QJsonObject rp = ruleProviders_.value(payload).toObject();
    const bool editable = rp.value(QStringLiteral("editable")).toBool(false);

    if (editable) {
        const int ruleCount = rp.value(QStringLiteral("rule_count")).toInt(0);
        const QString filePath = rp.value(QStringLiteral("file_path")).toString();
        const QString rawText = rp.value(QStringLiteral("raw_text")).toString();

        openRulesetFolderBtn_->setVisible(true);
        resetRulesetBtn_->setVisible(rp.value(QStringLiteral("can_reset")).toBool(true));
        saveRulesetBtn_->setVisible(true);

        providerMetaLabel_->setStyleSheet(QStringLiteral("color: #065f46; font-weight: 600; font-size: 12px;"));
        providerMetaLabel_->setText(
            L("本地可编辑规则集：%1（当前生效 %2 条）— 每行一条 TYPE,VALUE，无需写出口目标",
              "Editable local ruleset: %1 (%2 active rules) — one TYPE,VALUE per line, no target needed")
                .arg(payload)
                .arg(ruleCount));

        providerDetailEdit_->setReadOnly(false);
        providerDetailEdit_->setStyleSheet(QStringLiteral(
            "QPlainTextEdit { background-color: #ffffff; border: 1px solid #10b981; border-radius: 4px; }"));

        if (forceReloadEditor || activeEditableProvider_ != payload || !rulesetDirty_) {
            activeEditableProvider_ = payload;
            activeRulesetFilePath_ = filePath;
            suppressRulesetSignals_ = true;
            providerDetailEdit_->setPlainText(rawText);
            suppressRulesetSignals_ = false;
            setRulesetDirty(false);
        }
        return;
    }

    activeEditableProvider_.clear();
    activeRulesetFilePath_ = rp.value(QStringLiteral("file_path")).toString();
    openRulesetFolderBtn_->setVisible(!activeRulesetFilePath_.isEmpty());
    resetRulesetBtn_->setVisible(false);
    saveRulesetBtn_->setVisible(false);

    providerMetaLabel_->setStyleSheet(QStringLiteral("color: #64748b; font-size: 12px;"));
    if (payload == QStringLiteral("clashfx-internal-direct")) {
        providerMetaLabel_->setText(
            L("只读预览：ClashFX 内核自身防环回守护规则集（固定直连，不可修改或禁用，防止 TUN 流量死循环）",
              "Read-only: ClashFX internal self-loop guard (permanently DIRECT to prevent TUN deadlocks)"));
    } else {
        providerMetaLabel_->setText(
            L("只读预览（如需长期增删网站或软件分流，请切至「可视化分流」标签页；上方首列勾选框可临时开关此规则）",
              "Read-only preview (use 'Visual Rules' tab to add persistent rules, or toggle 'On / #' above)"));
    }

    providerDetailEdit_->setReadOnly(true);
    providerDetailEdit_->setStyleSheet(QStringLiteral(
        "QPlainTextEdit { background-color: #f8fafc; border: 1px solid #cbd5e1; border-radius: 4px; }"));

    QStringList lines;
    lines << QStringLiteral("[%1]  %2,%3%4")
                 .arg(src, tp, payload.isEmpty() ? QString() : (payload + QStringLiteral(",")), proxy);

    if (ruleProviders_.contains(payload)) {
        const QString behavior = rp.value(QStringLiteral("behavior")).toString();
        const QString vehicle = rp.value(QStringLiteral("vehicle_type")).toString();
        const QJsonArray inner = rp.value(QStringLiteral("payload")).toArray();
        const int ruleCount = rp.value(QStringLiteral("rule_count")).toInt(inner.size());

        lines << QString();
        lines << L("=== 规则集 %1（类型: %2 / %3，共 %4 条子规则）===",
                   "=== Rule-Set %1 (%2 / %3, %4 entries) ===")
                     .arg(payload, vehicle, behavior)
                     .arg(ruleCount);
        for (int k = 0; k < inner.size(); ++k) {
            lines << QStringLiteral("  %1. %2").arg(k + 1, 2).arg(inner.at(k).toString());
        }
    }

    suppressRulesetSignals_ = true;
    providerDetailEdit_->setPlainText(lines.join(QLatin1Char('\n')));
    suppressRulesetSignals_ = false;
}

void RuleEditorDialog::onSaveRulesetProvider() {
    if (!client_ || activeEditableProvider_.isEmpty()) return;

    const QString providerName = activeEditableProvider_;
    QJsonObject params;
    params.insert(QStringLiteral("provider_name"), providerName);
    params.insert(QStringLiteral("content"), providerDetailEdit_->toPlainText());

    int keepRuleIdx = -1;
    if (effTable_->currentRow() >= 0 && effTable_->item(effTable_->currentRow(), 0)) {
        keepRuleIdx = variantIntOr(effTable_->item(effTable_->currentRow(), 0)->data(Qt::UserRole + 1), -1);
    }

    saveRulesetBtn_->setEnabled(false);
    saveRulesetBtn_->setText(L("正在热更新…", "Hot-Updating…"));

    client_->call(QStringLiteral("save_rule_provider"), params,
                  [this, providerName, keepRuleIdx](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      saveRulesetBtn_->setEnabled(true);
                      saveRulesetBtn_->setText(L("保存并热更新规则集", "Save & Hot-Update Ruleset"));
                      if (!ok) {
                          QMessageBox::warning(this, L("保存规则集失败", "Failed to Save Ruleset"), err);
                          return;
                      }
                      setRulesetDirty(false);
                      if (data.isObject()) {
                          applyEffectiveRulesData(data.toObject(), keepRuleIdx);
                      }
                      const int newCnt = ruleProviders_.value(providerName)
                                             .toObject()
                                             .value(QStringLiteral("rule_count"))
                                             .toInt(0);
                      setRulesetDirty(false,
                                      L("✓ 已保存 %1（共 %2 条子规则）并完成零断流热更新！",
                                        "✓ Saved %1 (%2 rules) and hot-updated in 0ms!")
                                          .arg(providerName)
                                          .arg(newCnt));
                  });
}

void RuleEditorDialog::onResetRulesetProvider() {
    if (!client_ || activeEditableProvider_.isEmpty()) return;

    const QString providerName = activeEditableProvider_;
    const auto ans = QMessageBox::question(
        this,
        windowTitle(),
        L("确定将规则集 %1 恢复为 ClashFX 默认清单并立即热更新吗？（你自定义的增删条目将被覆盖）",
          "Reset ruleset %1 to ClashFX defaults and hot-update immediately?")
            .arg(providerName),
        QMessageBox::Yes | QMessageBox::No,
        QMessageBox::No);
    if (ans != QMessageBox::Yes) return;

    int keepRuleIdx = -1;
    if (effTable_->currentRow() >= 0 && effTable_->item(effTable_->currentRow(), 0)) {
        keepRuleIdx = variantIntOr(effTable_->item(effTable_->currentRow(), 0)->data(Qt::UserRole + 1), -1);
    }

    QJsonObject params;
    params.insert(QStringLiteral("provider_name"), providerName);

    resetRulesetBtn_->setEnabled(false);
    client_->call(QStringLiteral("reset_rule_provider"), params,
                  [this, providerName, keepRuleIdx](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      resetRulesetBtn_->setEnabled(true);
                      if (!ok) {
                          QMessageBox::warning(this, L("恢复默认清单失败", "Failed to Reset Ruleset"), err);
                          return;
                      }
                      setRulesetDirty(false);
                      if (data.isObject()) {
                          applyEffectiveRulesData(data.toObject(), keepRuleIdx);
                          if (effTable_->currentRow() >= 0 && effTable_->item(effTable_->currentRow(), 0)) {
                              const int orig = variantIntOr(effTable_->item(effTable_->currentRow(), 0)->data(Qt::UserRole), -1);
                              if (orig >= 0 && orig < effectiveRules_.size()) {
                                  loadDetailForRuleObject(effectiveRules_.at(orig).toObject(), true);
                              }
                          }
                      }
                      setRulesetDirty(false,
                                      L("✓ 已将 %1 恢复为默认清单并热更新生效",
                                        "✓ Reset %1 to default list and hot-updated")
                                          .arg(providerName));
                  });
}

void RuleEditorDialog::onOpenRulesetFolder() {
    if (activeRulesetFilePath_.isEmpty()) return;
    const QFileInfo fi(activeRulesetFilePath_);
    QDesktopServices::openUrl(QUrl::fromLocalFile(fi.absolutePath()));
}

void RuleEditorDialog::selectTab(int index) {
    if (!tabs_ || index < 0 || index >= tabs_->count()) return;
    tabs_->setCurrentIndex(index);
}

void RuleEditorDialog::closeEvent(QCloseEvent* event) {
    if (!dirty_ && !rulesetDirty_) {
        event->accept();
        return;
    }
    const auto ans = QMessageBox::question(
        this,
        windowTitle(),
        L("当前有未保存的配置补丁或规则集修改，确定放弃并关闭吗？",
          "You have unsaved Config Patch or Ruleset changes. Discard and close?"),
        QMessageBox::Yes | QMessageBox::No,
        QMessageBox::No);
    if (ans == QMessageBox::Yes) {
        event->accept();
    } else {
        event->ignore();
    }
}

} // namespace clashfx
