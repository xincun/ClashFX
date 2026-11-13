#include "RuleEditorDialog.h"
#include "DaemonClient.h"
#include "I18n.h"
#include "TrayIconGenerator.h"

#include <QAbstractItemView>
#include <QBrush>
#include <QCloseEvent>
#include <QColor>
#include <QDesktopServices>
#include <QFormLayout>
#include <QGridLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QMessageBox>
#include <QSplitter>
#include <QUrl>
#include <QVBoxLayout>

namespace clashfx {

namespace {

struct RuleTypeSpec {
    const char* code;
    QString label;
    QString placeholder;
};

QVector<RuleTypeSpec> buildRuleTypeSpecs() {
    return {
        {"DOMAIN-SUFFIX",
         L("域名后缀 (DOMAIN-SUFFIX)", "Domain Suffix (DOMAIN-SUFFIX)"),
         L("例如 scholar.google.com、github.com（匹配该域名及所有子域名）",
           "e.g. scholar.google.com, github.com (matches domain & subdomains)")},
        {"DOMAIN",
         L("精确域名 (DOMAIN)", "Exact Domain (DOMAIN)"),
         L("例如 api.example.com（仅精确匹配完整域名）",
           "e.g. api.example.com (exact hostname match)")},
        {"DOMAIN-KEYWORD",
         L("域名关键字 (DOMAIN-KEYWORD)", "Domain Keyword (DOMAIN-KEYWORD)"),
         L("例如 spotify（域名中包含该词即匹配）",
           "e.g. spotify (substring match in hostname)")},
        {"DOMAIN-REGEX",
         L("域名正则 (DOMAIN-REGEX)", "Domain Regex (DOMAIN-REGEX)"),
         L("例如 ^(.+\\.)?example\\.(com|org)$",
           "e.g. ^(.+\\.)?example\\.(com|org)$")},
        {"IP-CIDR",
         L("目标 IP 段 (IP-CIDR)", "Destination IP CIDR (IP-CIDR)"),
         L("例如 1.1.1.0/24 或 8.8.8.8/32",
           "e.g. 1.1.1.0/24 or 8.8.8.8/32")},
        {"GEOIP",
         L("国家/地区 IP (GEOIP)", "GeoIP Country/Tag (GEOIP)"),
         L("例如 CN、US、JP、TELEGRAM",
           "e.g. CN, US, JP, TELEGRAM")},
        {"GEOSITE",
         L("站点分类集 (GEOSITE)", "GeoSite Category (GEOSITE)"),
         L("例如 youtube、github、cn、geolocation-!cn",
           "e.g. youtube, github, cn, geolocation-!cn")},
        {"PROCESS-NAME",
         L("进程名称 (PROCESS-NAME)", "Process Name (PROCESS-NAME)"),
         L("例如 Telegram.exe、chrome.exe（需开启 TUN 增强模式）",
           "e.g. Telegram.exe, chrome.exe (requires Enhanced TUN)")},
        {"PROCESS-PATH",
         L("进程完整路径 (PROCESS-PATH)", "Process Path (PROCESS-PATH)"),
         L("例如 C:\\Program Files\\App\\app.exe（需开启 TUN 增强模式）",
           "e.g. C:\\Program Files\\App\\app.exe (requires Enhanced TUN)")},
        {"DST-PORT",
         L("目标端口 (DST-PORT)", "Destination Port (DST-PORT)"),
         L("例如 443 或 8000-8080",
           "e.g. 443 or 8000-8080")},
        {"SRC-IP-CIDR",
         L("源 IP 段 (SRC-IP-CIDR)", "Source IP CIDR (SRC-IP-CIDR)"),
         L("例如 192.168.1.50/32（局域网指定设备分流）",
           "e.g. 192.168.1.50/32 (LAN client routing)")},
        {"NETWORK",
         L("传输层协议 (NETWORK)", "Transport Protocol (NETWORK)"),
         L("填写 udp 或 tcp（例如屏蔽 QUIC：NETWORK + udp + REJECT）",
           "udp or tcp (e.g. block QUIC with NETWORK + udp + REJECT)")},
    };
}

QString formatSourceBadge(const QString& source) {
    if (source == QStringLiteral("system")) {
        return L("系统防环", "System Guard");
    }
    if (source == QStringLiteral("ai_lock")) {
        return L("AI 专用锁定", "AI Lock");
    }
    if (source == QStringLiteral("custom")) {
        return L("自定义规则", "Custom");
    }
    if (source == QStringLiteral("cn_apps")) {
        return L("国内应用直连", "CN Apps");
    }
    return L("订阅配置", "Profile");
}

} // namespace

RuleEditorDialog::RuleEditorDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setWindowTitle(L("分流规则编辑器", "Routing Rules Editor"));
    resize(960, 660);

    auto* rootLayout = new QVBoxLayout(this);
    rootLayout->setSpacing(10);

    // Top banner + Advanced Mixin shortcut
    auto* topBar = new QHBoxLayout();
    infoBanner_ = new QLabel(this);
    infoBanner_->setWordWrap(true);
    infoBanner_->setStyleSheet(QStringLiteral(
        "background-color: #f4f4f5; color: #18181b; border: 1px solid #d4d4d8; "
        "border-radius: 6px; padding: 6px 10px;"));
    auto* mixinBtn = new QPushButton(L("高级 Profile Mixin (YAML)…", "Advanced YAML Mixin…"), this);
    topBar->addWidget(infoBanner_, 1);
    topBar->addWidget(mixinBtn, 0);
    rootLayout->addLayout(topBar);

    tabs_ = new QTabWidget(this);

    // ========================================================================
    // Tab 1: Custom Rules Editor
    // ========================================================================
    auto* customPage = new QWidget(tabs_);
    auto* customLayout = new QVBoxLayout(customPage);
    customLayout->setSpacing(10);

    customTable_ = new QTableWidget(0, 6, customPage);
    customTable_->setHorizontalHeaderLabels({
        L("启用", "On"),
        L("规则类型", "Type"),
        L("匹配内容", "Match Payload"),
        L("出口策略", "Outbound"),
        L("优先级位置", "Position"),
        L("备注", "Note"),
    });
    customTable_->setSelectionBehavior(QAbstractItemView::SelectRows);
    customTable_->setSelectionMode(QAbstractItemView::SingleSelection);
    customTable_->setEditTriggers(QAbstractItemView::NoEditTriggers);
    customTable_->verticalHeader()->setVisible(false);
    customTable_->horizontalHeader()->setSectionResizeMode(0, QHeaderView::ResizeToContents);
    customTable_->horizontalHeader()->setSectionResizeMode(1, QHeaderView::ResizeToContents);
    customTable_->horizontalHeader()->setSectionResizeMode(2, QHeaderView::Stretch);
    customTable_->horizontalHeader()->setSectionResizeMode(3, QHeaderView::ResizeToContents);
    customTable_->horizontalHeader()->setSectionResizeMode(4, QHeaderView::ResizeToContents);
    customTable_->horizontalHeader()->setSectionResizeMode(5, QHeaderView::Stretch);
    customLayout->addWidget(customTable_, 1);

    auto* editorBox = new QGroupBox(
        L("添加或修改分流规则（跨订阅持久保存，更新订阅不会丢失）",
          "Add or Edit Rule (Persisted across subscription refreshes)"),
        customPage);
    auto* grid = new QGridLayout(editorBox);
    grid->setHorizontalSpacing(10);
    grid->setVerticalSpacing(8);

    typeCombo_ = new QComboBox(editorBox);
    for (const auto& spec : buildRuleTypeSpecs()) {
        typeCombo_->addItem(spec.label, QString::fromLatin1(spec.code));
    }

    targetCombo_ = new QComboBox(editorBox);
    targetCombo_->setMinimumWidth(230);

    positionCombo_ = new QComboBox(editorBox);
    positionCombo_->addItem(L("优先匹配（排在订阅与国内直连前）", "High Priority (Before profile rules)"),
                            QStringLiteral("prepend"));
    positionCombo_->addItem(L("兜底补充（排在最终 MATCH 前）", "Fallback (Before final MATCH)"),
                            QStringLiteral("before_match"));

    payloadEdit_ = new QLineEdit(editorBox);
    noResolveCheck_ = new QCheckBox(L("不解析域名 (no-resolve)", "Skip DNS resolve (no-resolve)"), editorBox);
    noteEdit_ = new QLineEdit(editorBox);
    noteEdit_->setPlaceholderText(L("可选备注（如：学术文献走代理 / 局域网 NAS 直连）", "Optional note"));

    addBtn_ = new QPushButton(L("添加为新规则", "Add as New Rule"), editorBox);
    updateRowBtn_ = new QPushButton(L("更新选中行", "Update Selected"), editorBox);
    updateRowBtn_->setEnabled(false);

    grid->addWidget(new QLabel(L("规则类型:", "Type:"), editorBox), 0, 0);
    grid->addWidget(typeCombo_, 0, 1);
    grid->addWidget(new QLabel(L("出口策略:", "Outbound:"), editorBox), 0, 2);
    grid->addWidget(targetCombo_, 0, 3);
    grid->addWidget(new QLabel(L("位置:", "Position:"), editorBox), 0, 4);
    grid->addWidget(positionCombo_, 0, 5);

    grid->addWidget(new QLabel(L("匹配内容:", "Payload:"), editorBox), 1, 0);
    grid->addWidget(payloadEdit_, 1, 1, 1, 3);
    grid->addWidget(noResolveCheck_, 1, 4, 1, 2);

    auto* noteBtnRow = new QHBoxLayout();
    noteBtnRow->addWidget(noteEdit_, 1);
    noteBtnRow->addWidget(addBtn_);
    noteBtnRow->addWidget(updateRowBtn_);
    grid->addWidget(new QLabel(L("备注说明:", "Note:"), editorBox), 2, 0);
    grid->addLayout(noteBtnRow, 2, 1, 1, 5);
    grid->setColumnStretch(1, 1);
    grid->setColumnStretch(3, 1);

    customLayout->addWidget(editorBox, 0);

    auto* bottomBar = new QHBoxLayout();
    moveUpBtn_ = new QPushButton(L("上移 ↑", "Move Up"), customPage);
    moveDownBtn_ = new QPushButton(L("下移 ↓", "Move Down"), customPage);
    deleteBtn_ = new QPushButton(L("删除选中", "Delete Selected"), customPage);
    unsavedLabel_ = new QLabel(customPage);
    unsavedLabel_->setStyleSheet(QStringLiteral("color: #d97706; font-weight: 600;"));

    saveApplyBtn_ = new QPushButton(L("保存并立即生效", "Save & Apply Now"), customPage);
    saveApplyBtn_->setDefault(true);
    saveApplyBtn_->setStyleSheet(QStringLiteral(
        "QPushButton { background-color: #2563eb; color: #ffffff; font-weight: 600; "
        "border: 1px solid #1d4ed8; border-radius: 5px; padding: 6px 16px; }"
        "QPushButton:hover { background-color: #1d4ed8; }"
        "QPushButton:disabled { background-color: #93c5fd; border-color: #93c5fd; }"));
    auto* closeBtn1 = new QPushButton(L("关闭", "Close"), customPage);

    bottomBar->addWidget(moveUpBtn_);
    bottomBar->addWidget(moveDownBtn_);
    bottomBar->addWidget(deleteBtn_);
    bottomBar->addSpacing(12);
    bottomBar->addWidget(unsavedLabel_, 1);
    bottomBar->addWidget(saveApplyBtn_);
    bottomBar->addWidget(closeBtn1);
    customLayout->addLayout(bottomBar);

    tabs_->addTab(customPage, L("自定义分流规则", "Custom Rules"));

    // ========================================================================
    // Tab 2: Effective Runtime Rules & Rule-Sets Inspector
    // ========================================================================
    auto* effPage = new QWidget(tabs_);
    auto* effLayout = new QVBoxLayout(effPage);
    effLayout->setSpacing(8);

    auto* filterBar = new QHBoxLayout();
    searchEdit_ = new QLineEdit(effPage);
    searchEdit_->setPlaceholderText(L("搜索规则类型 / 域名 / 进程 / 规则集名称 / 出口策略…",
                                      "Filter by type, domain, process, rule-set, or outbound…"));
    effSummaryLabel_ = new QLabel(effPage);
    refreshEffBtn_ = new QPushButton(L("刷新实时命中统计", "Refresh Live Stats"), effPage);
    filterBar->addWidget(searchEdit_, 1);
    filterBar->addWidget(effSummaryLabel_);
    filterBar->addWidget(refreshEffBtn_);
    effLayout->addLayout(filterBar);

    auto* splitter = new QSplitter(Qt::Vertical, effPage);

    effTable_ = new QTableWidget(0, 6, splitter);
    effTable_->setHorizontalHeaderLabels({
        QStringLiteral("#"),
        L("来源", "Source"),
        L("类型", "Type"),
        L("匹配条件 / 规则集", "Payload / Rule-Set"),
        L("出口策略", "Outbound"),
        L("子规则数 / 命中统计", "Size / Hits"),
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
        L("规则详情 / 规则集内部展开预览（点击上方任意行查看）",
          "Rule / Rule-Set Expanded Payload Inspector"),
        splitter);
    auto* detailLayout = new QVBoxLayout(providerDetailBox_);
    providerDetailEdit_ = new QPlainTextEdit(providerDetailBox_);
    providerDetailEdit_->setReadOnly(true);
    providerDetailEdit_->setPlaceholderText(
        L("点击上方表格中的 RuleSet（例如 clashfx-ai-lock 或 clashfx-cn-apps-direct）可查看其包含的全部子规则。",
          "Select a RuleSet row above to inspect its inner rules."));
    detailLayout->addWidget(providerDetailEdit_);

    splitter->addWidget(effTable_);
    splitter->addWidget(providerDetailBox_);
    splitter->setStretchFactor(0, 3);
    splitter->setStretchFactor(1, 2);
    effLayout->addWidget(splitter, 1);

    tabs_->addTab(effPage, L("当前生效规则总览", "Effective Runtime Rules"));

    rootLayout->addWidget(tabs_, 1);

    // Connections
    connect(mixinBtn, &QPushButton::clicked, this, &RuleEditorDialog::onOpenMixinFile);
    connect(typeCombo_, qOverload<int>(&QComboBox::currentIndexChanged), this, &RuleEditorDialog::onRuleTypeChanged);
    connect(customTable_, &QTableWidget::itemSelectionChanged, this, &RuleEditorDialog::onCustomSelectionChanged);
    connect(customTable_, &QTableWidget::cellChanged, this, &RuleEditorDialog::onCustomCellChanged);
    connect(addBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onAddRule);
    connect(updateRowBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onUpdateSelectedRule);
    connect(moveUpBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onMoveUp);
    connect(moveDownBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onMoveDown);
    connect(deleteBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onDeleteSelected);
    connect(saveApplyBtn_, &QPushButton::clicked, this, &RuleEditorDialog::onSaveAndApply);
    connect(closeBtn1, &QPushButton::clicked, this, &QDialog::close);

    connect(refreshEffBtn_, &QPushButton::clicked, this, &RuleEditorDialog::refreshEffectiveRules);
    connect(searchEdit_, &QLineEdit::textChanged, this, &RuleEditorDialog::onFilterEffectiveRules);
    connect(effTable_, &QTableWidget::itemSelectionChanged, this, &RuleEditorDialog::onEffectiveSelectionChanged);
    connect(tabs_, &QTabWidget::currentChanged, this, [this](int idx) {
        if (idx == 1) {
            refreshEffectiveRules();
        }
    });

    onRuleTypeChanged(typeCombo_->currentIndex());
    setDirty(false);

    if (client_) {
        connect(client_, &DaemonClient::stateChanged, this, &RuleEditorDialog::refreshFromSnapshot);
        refreshFromSnapshot(client_->snapshot());
        refreshEffectiveRules();
    }
}

bool RuleEditorDialog::typeSupportsNoResolve(const QString& ruleType) {
    const QString upper = ruleType.trimmed().toUpper();
    return upper == QStringLiteral("IP-CIDR") ||
           upper == QStringLiteral("IP-CIDR6") ||
           upper == QStringLiteral("GEOIP") ||
           upper == QStringLiteral("IP-ASN");
}

void RuleEditorDialog::onRuleTypeChanged(int index) {
    const auto specs = buildRuleTypeSpecs();
    if (index >= 0 && index < specs.size()) {
        payloadEdit_->setPlaceholderText(specs.at(index).placeholder);
    }
    const QString code = typeCombo_->currentData().toString();
    const bool canNoResolve = typeSupportsNoResolve(code);
    noResolveCheck_->setEnabled(canNoResolve);
    if (!canNoResolve) {
        noResolveCheck_->setChecked(false);
    } else if (code == QStringLiteral("IP-CIDR")) {
        noResolveCheck_->setChecked(true);
    }
}

void RuleEditorDialog::rebuildTargetCombo(const QString& preserveTarget) {
    const QString currentVal = preserveTarget.isEmpty() ? targetCombo_->currentData().toString() : preserveTarget;
    targetCombo_->blockSignals(true);
    targetCombo_->clear();

    targetCombo_->addItem(
        L("默认主代理组 (自动适配当前配置: ", "Default Primary Group (Auto: ") + defaultTarget_ + QStringLiteral(")"),
        QStringLiteral("@DEFAULT"));
    targetCombo_->addItem(L("DIRECT (直接连接)", "DIRECT"), QStringLiteral("DIRECT"));
    targetCombo_->addItem(L("REJECT (拦截/阻断)", "REJECT"), QStringLiteral("REJECT"));

    if (client_) {
        const QJsonObject snap = client_->snapshot();
        for (const QJsonValue& gv : snap.value(QStringLiteral("proxy_groups")).toArray()) {
            const QString gName = gv.toObject().value(QStringLiteral("name")).toString().trimmed();
            if (!gName.isEmpty() && gName != QStringLiteral("GLOBAL")) {
                targetCombo_->addItem(L("[代理组] ", "[Group] ") + gName, gName);
            }
        }
        for (const QJsonValue& nv : snap.value(QStringLiteral("ai_lock_candidates")).toArray()) {
            const QString nName = nv.toObject().value(QStringLiteral("name")).toString().trimmed();
            if (!nName.isEmpty()) {
                targetCombo_->addItem(L("[节点] ", "[Node] ") + nName, nName);
            }
        }
    }

    int foundIdx = targetCombo_->findData(currentVal);
    if (foundIdx < 0 && !currentVal.isEmpty()) {
        targetCombo_->addItem(L("[其他配置保留] ", "[Other Profile] ") + currentVal, currentVal);
        foundIdx = targetCombo_->count() - 1;
    }
    targetCombo_->setCurrentIndex(foundIdx >= 0 ? foundIdx : 0);
    targetCombo_->blockSignals(false);
}

QString RuleEditorDialog::formatTargetDisplay(const QString& target) const {
    if (target.compare(QStringLiteral("@DEFAULT"), Qt::CaseInsensitive) == 0) {
        return L("默认主代理组 → ", "Default → ") + defaultTarget_;
    }
    if (target == QStringLiteral("DIRECT")) {
        return L("DIRECT (直连)", "DIRECT");
    }
    if (target == QStringLiteral("REJECT")) {
        return L("REJECT (阻断)", "REJECT");
    }
    if (!validTargetsInProfile_.isEmpty() && !validTargetsInProfile_.contains(target)) {
        return target + L("  [当前配置不存在，已自动跳过]", "  [Skipped in current profile]");
    }
    return target;
}

void RuleEditorDialog::refreshFromSnapshot(const QJsonObject& snapshot) {
    if (snapshot.isEmpty()) {
        return;
    }
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    selectedProfile_ = settings.value(QStringLiteral("selected_profile")).toString(QStringLiteral("config"));
    outboundMode_ = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toUpper();
    defaultTarget_ = snapshot.value(QStringLiteral("default_rule_target")).toString(QStringLiteral("DIRECT"));

    validTargetsInProfile_.clear();
    validTargetsInProfile_.insert(QStringLiteral("DIRECT"));
    validTargetsInProfile_.insert(QStringLiteral("REJECT"));
    validTargetsInProfile_.insert(QStringLiteral("REJECT-DROP"));
    validTargetsInProfile_.insert(QStringLiteral("PASS"));
    validTargetsInProfile_.insert(QStringLiteral("COMPATIBLE"));
    validTargetsInProfile_.insert(QStringLiteral("GLOBAL"));
    for (const QJsonValue& gv : snapshot.value(QStringLiteral("proxy_groups")).toArray()) {
        const QString n = gv.toObject().value(QStringLiteral("name")).toString().trimmed();
        if (!n.isEmpty()) validTargetsInProfile_.insert(n);
    }
    for (const QJsonValue& nv : snapshot.value(QStringLiteral("ai_lock_candidates")).toArray()) {
        const QString n = nv.toObject().value(QStringLiteral("name")).toString().trimmed();
        if (!n.isEmpty()) validTargetsInProfile_.insert(n);
    }

    QString banner = QStringLiteral("%1: %2    |    %3: %4    |    %5: %6")
                         .arg(L("当前生效配置", "Active Profile"),
                              selectedProfile_,
                              L("出站模式", "Mode"),
                              outboundMode_,
                              L("默认主代理组 (@DEFAULT)", "Default Primary Group"),
                              defaultTarget_);
    if (outboundMode_ != QStringLiteral("RULE")) {
        banner += L("    ⚠️ 当前非 RULE 模式，分流规则仅在规则模式下参与匹配",
                    "    (Note: Routing rules only match in RULE mode)");
    }
    infoBanner_->setText(banner);

    rebuildTargetCombo();

    // Do not overwrite user's unsaved table edits during background 3s state pushes!
    if (!dirty_) {
        workingRules_.clear();
        const QJsonArray arr = settings.value(QStringLiteral("custom_rules")).toArray();
        workingRules_.reserve(arr.size());
        for (const QJsonValue& v : arr) {
            const QJsonObject o = v.toObject();
            CustomRuleEntry e;
            e.enabled = o.value(QStringLiteral("enabled")).toBool(true);
            e.type = o.value(QStringLiteral("type")).toString(QStringLiteral("DOMAIN-SUFFIX"));
            e.payload = o.value(QStringLiteral("payload")).toString();
            e.target = o.value(QStringLiteral("target")).toString(QStringLiteral("@DEFAULT"));
            e.position = o.value(QStringLiteral("position")).toString(QStringLiteral("prepend"));
            e.noResolve = o.value(QStringLiteral("no_resolve")).toBool(false);
            e.note = o.value(QStringLiteral("note")).toString();
            workingRules_.push_back(e);
        }
        rebuildCustomTable();
    } else {
        rebuildCustomTable(customTable_->currentRow());
    }
}

void RuleEditorDialog::rebuildCustomTable(int selectRow) {
    updatingTable_ = true;
    customTable_->setRowCount(workingRules_.size());
    for (int i = 0; i < workingRules_.size(); ++i) {
        const CustomRuleEntry& r = workingRules_.at(i);

        auto* chkItem = new QTableWidgetItem();
        chkItem->setFlags(Qt::ItemIsEnabled | Qt::ItemIsSelectable | Qt::ItemIsUserCheckable);
        chkItem->setCheckState(r.enabled ? Qt::Checked : Qt::Unchecked);

        auto* typeItem = new QTableWidgetItem(r.type);
        auto* payloadItem = new QTableWidgetItem(r.payload);
        auto* targetItem = new QTableWidgetItem(formatTargetDisplay(r.target));

        QString posText = (r.position == QStringLiteral("before_match"))
                              ? L("兜底补充 (MATCH 前)", "Before MATCH")
                              : L("优先匹配 (订阅前)", "Prepend");
        if (r.noResolve && typeSupportsNoResolve(r.type)) {
            posText += QStringLiteral(" · no-resolve");
        }
        auto* posItem = new QTableWidgetItem(posText);
        auto* noteItem = new QTableWidgetItem(r.note);

        if (!r.enabled) {
            const QBrush muted(QColor(148, 163, 184));
            typeItem->setForeground(muted);
            payloadItem->setForeground(muted);
            targetItem->setForeground(muted);
            posItem->setForeground(muted);
            noteItem->setForeground(muted);
        }

        customTable_->setItem(i, 0, chkItem);
        customTable_->setItem(i, 1, typeItem);
        customTable_->setItem(i, 2, payloadItem);
        customTable_->setItem(i, 3, targetItem);
        customTable_->setItem(i, 4, posItem);
        customTable_->setItem(i, 5, noteItem);
    }
    updatingTable_ = false;

    if (selectRow >= 0 && selectRow < workingRules_.size()) {
        customTable_->selectRow(selectRow);
    }
    onCustomSelectionChanged();
}

void RuleEditorDialog::setDirty(bool dirty) {
    dirty_ = dirty;
    if (dirty_) {
        unsavedLabel_->setText(L("● 有未保存的规则修改，请点击右侧「保存并立即生效」",
                                 "● Unsaved rule changes — click 'Save & Apply Now'"));
    } else {
        unsavedLabel_->setText(L("所有自定义分流规则已同步生效", "All custom rules saved & active"));
    }
}

void RuleEditorDialog::onCustomSelectionChanged() {
    const int row = customTable_->currentRow();
    const bool valid = (row >= 0 && row < workingRules_.size());
    updateRowBtn_->setEnabled(valid);
    moveUpBtn_->setEnabled(valid && row > 0);
    moveDownBtn_->setEnabled(valid && row + 1 < workingRules_.size());
    deleteBtn_->setEnabled(valid);

    if (!valid) {
        return;
    }
    const CustomRuleEntry& r = workingRules_.at(row);
    int tIdx = typeCombo_->findData(r.type);
    if (tIdx < 0) {
        typeCombo_->addItem(r.type, r.type);
        tIdx = typeCombo_->count() - 1;
    }
    typeCombo_->setCurrentIndex(tIdx);
    payloadEdit_->setText(r.payload);
    rebuildTargetCombo(r.target);
    const int pIdx = positionCombo_->findData(r.position);
    positionCombo_->setCurrentIndex(pIdx >= 0 ? pIdx : 0);
    noResolveCheck_->setChecked(r.noResolve && typeSupportsNoResolve(r.type));
    noteEdit_->setText(r.note);
}

void RuleEditorDialog::onCustomCellChanged(int row, int column) {
    if (updatingTable_ || column != 0 || row < 0 || row >= workingRules_.size()) {
        return;
    }
    QTableWidgetItem* item = customTable_->item(row, 0);
    if (!item) {
        return;
    }
    const bool checked = (item->checkState() == Qt::Checked);
    if (workingRules_[row].enabled != checked) {
        workingRules_[row].enabled = checked;
        setDirty(true);
        rebuildCustomTable(row);
    }
}

bool RuleEditorDialog::readFormIntoEntry(CustomRuleEntry* out, QString* errMsg) const {
    const QString tp = typeCombo_->currentData().toString().trimmed().toUpper();
    const QString payload = payloadEdit_->text().trimmed();
    const QString target = targetCombo_->currentData().toString().trimmed();
    const QString pos = positionCombo_->currentData().toString().trimmed();

    if (payload.isEmpty()) {
        if (errMsg) {
            *errMsg = L("请填写规则的匹配内容（例如域名、IP 段、进程名或端口）。",
                        "Please enter a match payload (domain, CIDR, process name, or port).");
        }
        return false;
    }
    const bool allowComma = (tp == QStringLiteral("DOMAIN-REGEX") ||
                             tp == QStringLiteral("PROCESS-NAME-REGEX") ||
                             tp == QStringLiteral("PROCESS-PATH-REGEX"));
    if (!allowComma && payload.contains(QLatin1Char(','))) {
        if (errMsg) {
            *errMsg = L("匹配内容中不能包含英文逗号（如需多条域名请分别添加）。",
                        "Payload must not contain commas (add separate rules for multiple items).");
        }
        return false;
    }
    if (tp == QStringLiteral("NETWORK") &&
        payload.compare(QStringLiteral("udp"), Qt::CaseInsensitive) != 0 &&
        payload.compare(QStringLiteral("tcp"), Qt::CaseInsensitive) != 0) {
        if (errMsg) {
            *errMsg = L("NETWORK 规则的匹配内容仅支持填写 udp 或 tcp。",
                        "NETWORK rule payload must be 'udp' or 'tcp'.");
        }
        return false;
    }

    out->enabled = true;
    out->type = tp;
    out->payload = (tp == QStringLiteral("NETWORK")) ? payload.toLower() : payload;
    out->target = target.isEmpty() ? QStringLiteral("@DEFAULT") : target;
    out->position = pos.isEmpty() ? QStringLiteral("prepend") : pos;
    out->noResolve = noResolveCheck_->isChecked() && typeSupportsNoResolve(tp);
    out->note = noteEdit_->text().trimmed();
    return true;
}

void RuleEditorDialog::onAddRule() {
    CustomRuleEntry entry;
    QString errMsg;
    if (!readFormIntoEntry(&entry, &errMsg)) {
        QMessageBox::warning(this, windowTitle(), errMsg);
        payloadEdit_->setFocus();
        return;
    }
    workingRules_.push_back(entry);
    setDirty(true);
    rebuildCustomTable(workingRules_.size() - 1);
    payloadEdit_->clear();
    noteEdit_->clear();
    payloadEdit_->setFocus();
}

void RuleEditorDialog::onUpdateSelectedRule() {
    const int row = customTable_->currentRow();
    if (row < 0 || row >= workingRules_.size()) {
        return;
    }
    CustomRuleEntry entry;
    QString errMsg;
    if (!readFormIntoEntry(&entry, &errMsg)) {
        QMessageBox::warning(this, windowTitle(), errMsg);
        return;
    }
    entry.enabled = workingRules_.at(row).enabled;
    workingRules_[row] = entry;
    setDirty(true);
    rebuildCustomTable(row);
}

void RuleEditorDialog::onMoveUp() {
    const int row = customTable_->currentRow();
    if (row <= 0 || row >= workingRules_.size()) {
        return;
    }
    workingRules_.swapItemsAt(row, row - 1);
    setDirty(true);
    rebuildCustomTable(row - 1);
}

void RuleEditorDialog::onMoveDown() {
    const int row = customTable_->currentRow();
    if (row < 0 || row + 1 >= workingRules_.size()) {
        return;
    }
    workingRules_.swapItemsAt(row, row + 1);
    setDirty(true);
    rebuildCustomTable(row + 1);
}

void RuleEditorDialog::onDeleteSelected() {
    const int row = customTable_->currentRow();
    if (row < 0 || row >= workingRules_.size()) {
        return;
    }
    workingRules_.removeAt(row);
    setDirty(true);
    const int nextSel = qMin(row, workingRules_.size() - 1);
    rebuildCustomTable(nextSel);
}

void RuleEditorDialog::onSaveAndApply() {
    if (!client_) {
        return;
    }
    // Convenience: if workingRules_ is empty OR user typed a brand-new payload into payloadEdit_
    // that doesn't match the currently selected row and hasn't clicked "Add" yet, ask/handle cleanly?
    // Keep it predictable: only save workingRules_, UNLESS workingRules_ is empty and payloadEdit_ is non-empty!
    if (workingRules_.isEmpty() && !payloadEdit_->text().trimmed().isEmpty()) {
        CustomRuleEntry autoEntry;
        QString errMsg;
        if (!readFormIntoEntry(&autoEntry, &errMsg)) {
            QMessageBox::warning(this, windowTitle(), errMsg);
            return;
        }
        workingRules_.push_back(autoEntry);
        payloadEdit_->clear();
        noteEdit_->clear();
        rebuildCustomTable(0);
    }

    QJsonArray rulesJson;
    for (const CustomRuleEntry& r : workingRules_) {
        QJsonObject obj;
        obj.insert(QStringLiteral("enabled"), r.enabled);
        obj.insert(QStringLiteral("type"), r.type);
        obj.insert(QStringLiteral("payload"), r.payload);
        obj.insert(QStringLiteral("target"), r.target);
        obj.insert(QStringLiteral("position"), r.position);
        if (r.noResolve && typeSupportsNoResolve(r.type)) {
            obj.insert(QStringLiteral("no_resolve"), true);
        }
        if (!r.note.isEmpty()) {
            obj.insert(QStringLiteral("note"), r.note);
        }
        rulesJson.append(obj);
    }

    QJsonObject params;
    params.insert(QStringLiteral("rules"), rulesJson);
    saveApplyBtn_->setEnabled(false);

    client_->call(QStringLiteral("update_custom_rules"),
                  params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      saveApplyBtn_->setEnabled(true);
                      if (!ok) {
                          QMessageBox::critical(
                              this,
                              L("规则校验未通过（已自动回滚）", "Rule Validation Failed (Rolled Back)"),
                              err);
                          return;
                      }
                      setDirty(false);
                      if (data.isObject()) {
                          refreshFromSnapshot(data.toObject());
                      }
                      refreshEffectiveRules();
                  });
}

void RuleEditorDialog::onOpenMixinFile() {
    if (!client_) {
        return;
    }
    client_->call(QStringLiteral("ensure_profile_mixin"),
                  QJsonObject(),
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      if (!ok) {
                          QMessageBox::warning(this, windowTitle(), err);
                          return;
                      }
                      const QString path = data.toObject().value(QStringLiteral("path")).toString();
                      if (!path.isEmpty()) {
                          QDesktopServices::openUrl(QUrl::fromLocalFile(path));
                      }
                  });
}

void RuleEditorDialog::refreshEffectiveRules() {
    if (!client_) {
        return;
    }
    refreshEffBtn_->setEnabled(false);
    client_->call(QStringLiteral("get_effective_rules"),
                  QJsonObject(),
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      refreshEffBtn_->setEnabled(true);
                      if (!ok || !data.isObject()) {
                          effSummaryLabel_->setText(err);
                          return;
                      }
                      const QJsonObject obj = data.toObject();
                      effectiveRules_ = obj.value(QStringLiteral("rules")).toArray();
                      ruleProviders_ = obj.value(QStringLiteral("rule_providers")).toObject();
                      effSummaryLabel_->setText(
                          L("顶层生效规则 %1 条 · 规则集 %2 个", "%1 top-level rules · %2 rule-sets")
                              .arg(effectiveRules_.size())
                              .arg(ruleProviders_.size()));
                      renderEffectiveTable();
                  });
}

void RuleEditorDialog::onFilterEffectiveRules(const QString&) {
    renderEffectiveTable();
}

void RuleEditorDialog::renderEffectiveTable() {
    const QString kw = searchEdit_->text().trimmed();
    effTable_->setRowCount(0);

    int firstRuleSetVisibleRow = -1;
    for (int i = 0; i < effectiveRules_.size(); ++i) {
        const QJsonObject r = effectiveRules_.at(i).toObject();
        const int idx = r.value(QStringLiteral("index")).toInt(i);
        const QString source = r.value(QStringLiteral("source")).toString(QStringLiteral("profile"));
        const QString srcBadge = formatSourceBadge(source);
        const QString tp = r.value(QStringLiteral("type")).toString();
        const QString payload = r.value(QStringLiteral("payload")).toString();
        const QString proxy = r.value(QStringLiteral("proxy")).toString();
        const int sz = r.value(QStringLiteral("size")).toInt(-1);
        const qint64 hits = static_cast<qint64>(r.value(QStringLiteral("hit_count")).toDouble(0));

        if (!kw.isEmpty()) {
            bool matched = tp.contains(kw, Qt::CaseInsensitive) ||
                           payload.contains(kw, Qt::CaseInsensitive) ||
                           proxy.contains(kw, Qt::CaseInsensitive) ||
                           srcBadge.contains(kw, Qt::CaseInsensitive);
            if (!matched && ruleProviders_.contains(payload)) {
                const QJsonArray inner = ruleProviders_.value(payload)
                                             .toObject()
                                             .value(QStringLiteral("payload"))
                                             .toArray();
                for (const QJsonValue& iv : inner) {
                    if (iv.toString().contains(kw, Qt::CaseInsensitive)) {
                        matched = true;
                        break;
                    }
                }
            }
            if (!matched) {
                continue;
            }
        }

        const int row = effTable_->rowCount();
        effTable_->insertRow(row);

        auto* idxItem = new QTableWidgetItem(QString::number(idx + 1));
        idxItem->setData(Qt::UserRole, i);
        auto* srcItem = new QTableWidgetItem(srcBadge);
        auto* tpItem = new QTableWidgetItem(tp);
        auto* payloadItem = new QTableWidgetItem(payload.isEmpty() ? QStringLiteral("*") : payload);
        auto* proxyItem = new QTableWidgetItem(proxy);

        QString statText;
        if (sz >= 0) {
            statText = L("%1 条子规则 · 命中 %2", "%1 items · %2 hits").arg(sz).arg(hits);
        } else {
            statText = L("命中 %1", "%1 hits").arg(hits);
        }
        auto* statItem = new QTableWidgetItem(statText);

        effTable_->setItem(row, 0, idxItem);
        effTable_->setItem(row, 1, srcItem);
        effTable_->setItem(row, 2, tpItem);
        effTable_->setItem(row, 3, payloadItem);
        effTable_->setItem(row, 4, proxyItem);
        effTable_->setItem(row, 5, statItem);

        if (firstRuleSetVisibleRow < 0 && ruleProviders_.contains(payload)) {
            firstRuleSetVisibleRow = row;
        }
    }

    if (effTable_->rowCount() > 0) {
        effTable_->selectRow(firstRuleSetVisibleRow >= 0 ? firstRuleSetVisibleRow : 0);
    } else {
        providerDetailEdit_->clear();
    }
}

void RuleEditorDialog::onEffectiveSelectionChanged() {
    const int visRow = effTable_->currentRow();
    if (visRow < 0) {
        providerDetailEdit_->clear();
        return;
    }
    QTableWidgetItem* idxItem = effTable_->item(visRow, 0);
    if (!idxItem) {
        return;
    }
    bool convOk = false;
    const int rawIdx = idxItem->data(Qt::UserRole).toInt(&convOk);
    if (!convOk || rawIdx < 0 || rawIdx >= effectiveRules_.size()) {
        return;
    }
    const QJsonObject r = effectiveRules_.at(rawIdx).toObject();
    const QString tp = r.value(QStringLiteral("type")).toString();
    const QString payload = r.value(QStringLiteral("payload")).toString();
    const QString proxy = r.value(QStringLiteral("proxy")).toString();
    const qint64 hits = static_cast<qint64>(r.value(QStringLiteral("hit_count")).toDouble(0));
    const qint64 misses = static_cast<qint64>(r.value(QStringLiteral("miss_count")).toDouble(0));

    QStringList lines;
    lines << QStringLiteral("[%1]  %2  ->  %3    (%4: %5, %6: %7)")
                 .arg(tp,
                      payload.isEmpty() ? QStringLiteral("MATCH") : payload,
                      proxy,
                      L("累计命中", "Hits"),
                      QString::number(hits),
                      L("未命中跳过", "Misses"),
                      QString::number(misses));

    if (ruleProviders_.contains(payload)) {
        const QJsonObject prov = ruleProviders_.value(payload).toObject();
        const QString behavior = prov.value(QStringLiteral("behavior")).toString();
        const QString vehicle = prov.value(QStringLiteral("vehicle_type")).toString();
        const int count = prov.value(QStringLiteral("rule_count")).toInt(0);
        const QJsonArray inner = prov.value(QStringLiteral("payload")).toArray();

        lines << QStringLiteral("------------------------------------------------------------");
        lines << L("规则集名称: %1   |   类型: %2 / %3   |   子规则总数: %4",
                   "Rule-Set: %1   |   Type: %2 / %3   |   Total Rules: %4")
                     .arg(payload, vehicle, behavior)
                     .arg(count);
        lines << QString();
        if (inner.isEmpty()) {
            lines << L("（外部文件/HTTP 规则集，内核已加载 %1 条规则）",
                       "(External File/HTTP rule-set with %1 loaded rules)")
                         .arg(count);
        } else {
            for (int i = 0; i < inner.size(); ++i) {
                lines << QStringLiteral("  %1. %2")
                             .arg(i + 1, 2)
                             .arg(inner.at(i).toString());
            }
        }
    }
    providerDetailEdit_->setPlainText(lines.join(QLatin1Char('\n')));
}

void RuleEditorDialog::closeEvent(QCloseEvent* event) {
    if (!dirty_) {
        event->accept();
        return;
    }
    QMessageBox box(this);
    box.setIcon(QMessageBox::Question);
    box.setWindowTitle(windowTitle());
    box.setText(L("当前有尚未保存生效的分流规则修改，是否立即保存并应用？",
                  "You have unsaved routing rule changes. Save and apply them now?"));
    auto* saveBtn = box.addButton(L("保存并生效", "Save & Apply"), QMessageBox::AcceptRole);
    auto* discardBtn = box.addButton(L("放弃修改", "Discard"), QMessageBox::DestructiveRole);
    box.addButton(L("取消", "Cancel"), QMessageBox::RejectRole);
    box.exec();

    if (box.clickedButton() == saveBtn) {
        onSaveAndApply();
        event->ignore();
        return;
    }
    if (box.clickedButton() == discardBtn) {
        setDirty(false);
        event->accept();
        return;
    }
    event->ignore();
}

} // namespace clashfx
