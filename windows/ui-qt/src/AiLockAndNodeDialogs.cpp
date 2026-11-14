#include "AiLockAndNodeDialogs.h"

#include "DaemonClient.h"
#include "I18n.h"
#include "ModernWidgets.h"

#include <QAbstractItemView>
#include <QApplication>
#include <QClipboard>
#include <QComboBox>
#include <QDir>
#include <QFrame>
#include <QGridLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QJsonArray>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QMessageBox>
#include <QPlainTextEdit>
#include <QPushButton>
#include <QSet>
#include <QSignalBlocker>
#include <QSpinBox>
#include <QStyle>
#include <QTableWidget>
#include <QTableWidgetItem>
#include <QVBoxLayout>

namespace clashfx {

namespace {

inline QString uiText(const char* zh, const char* en) {
    return I18n::instance().isChinese() ? QString::fromUtf8(zh) : QString::fromUtf8(en);
}

PillTone protocolTone(const QString& typeRaw) {
    const QString t = typeRaw.trimmed().toLower();
    if (t == QLatin1String("trojan")) {
        return PillTone::Violet;
    }
    if (t == QLatin1String("vless") || t == QLatin1String("hysteria") ||
        t == QLatin1String("hysteria2") || t == QLatin1String("tuic") ||
        t == QLatin1String("anytls")) {
        return PillTone::Blue;
    }
    if (t == QLatin1String("ss") || t == QLatin1String("shadowsocks") ||
        t == QLatin1String("ssr") || t == QLatin1String("vmess")) {
        return PillTone::Amber;
    }
    if (t == QLatin1String("direct") || t == QLatin1String("wireguard")) {
        return PillTone::Green;
    }
    return PillTone::Muted;
}

QFrame* createPipelineNodeBox(QWidget* parent, const QString& objName = QStringLiteral("PipelineBox")) {
    auto* box = new QFrame(parent);
    box->setObjectName(objName);
    box->setStyleSheet(QStringLiteral(
        "QFrame#PipelineBox { background: #151721; border: 1px solid #262a36; border-radius: 10px; }"
        "QFrame#PipelineBoxTarget { background: #1e1836; border: 1px solid #593e99; border-radius: 10px; }"
        "QFrame#PipelineBoxReject { background: #221419; border: 1px solid #632833; border-radius: 10px; }"));
    auto* lay = new QVBoxLayout(box);
    lay->setContentsMargins(14, 11, 14, 11);
    lay->setSpacing(4);
    return box;
}

void styleCleanDarkTable(QTableWidget* table) {
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
        "QTableWidget {"
        "  background: transparent;"
        "  border: none;"
        "  color: #e5e7eb;"
        "  selection-background-color: #192238;"
        "  selection-color: #f9fafb;"
        "}"
        "QTableWidget::item {"
        "  border-bottom: 1px solid #1f222d;"
        "  padding: 4px 8px;"
        "}"
        "QTableWidget::item:selected {"
        "  background: #192238;"
        "}"
        "QHeaderView::section {"
        "  background: #14161e;"
        "  color: #8b93a6;"
        "  font-size: 11px;"
        "  font-weight: 600;"
        "  border: none;"
        "  border-bottom: 1px solid #262a36;"
        "  padding: 9px 10px;"
        "}"));
}

}  // namespace

// ===========================================================================
// AiLockPage
// ===========================================================================

AiLockPage::AiLockPage(DaemonClient* client, QWidget* parent)
    : QWidget(parent)
    , client_(client) {
    auto* outerLay = new QVBoxLayout(this);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(this);
    titleLabel_ = hdr.titleLabel;
    statusPill_ = hdr.primaryBadge;
    relayPill_ = hdr.secondaryBadge;
    subtitleLabel_ = hdr.subtitleLabel;

    disableBtn_ = new QPushButton(this);
    disableBtn_->setProperty("ghost", true);
    applyBtn_ = new QPushButton(this);
    applyBtn_->setProperty("primary", true);
    hdr.actionsLayout->addWidget(disableBtn_);
    hdr.actionsLayout->addWidget(applyBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(this);
    auto* root = new QVBoxLayout(body);
    root->setContentsMargins(24, 16, 24, 18);
    root->setSpacing(16);
    outerLay->addWidget(body, 1);

    // Card 1: Tunnel Topology Visualization
    topoCard_ = createCardFrame(body, QStringLiteral("AiLockTopoCard"));
    topoCard_->setStyleSheet(QStringLiteral(
        "QFrame#AiLockTopoCard {"
        "  background: #181a24;"
        "  border: 1px solid #3c2e63;"
        "  border-radius: 12px;"
        "}"));
    auto* topoLay = new QVBoxLayout(topoCard_);
    topoLay->setContentsMargins(18, 16, 18, 16);
    topoLay->setSpacing(14);

    auto* topoTopRow = new QHBoxLayout();
    topoTopRow->setSpacing(12);
    auto* topoTextCol = new QVBoxLayout();
    topoTextCol->setSpacing(3);
    topoTitleLabel_ = new QLabel(topoCard_);
    topoTitleLabel_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 14px; font-weight: 700;"));
    topoDescLabel_ = new QLabel(topoCard_);
    topoDescLabel_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px;"));
    topoDescLabel_->setWordWrap(true);
    topoTextCol->addWidget(topoTitleLabel_);
    topoTextCol->addWidget(topoDescLabel_);
    topoTopRow->addLayout(topoTextCol, 1);

    topoHealthPill_ = new BadgePill(topoCard_, false);
    topoTopRow->addWidget(topoHealthPill_, 0, Qt::AlignTop);
    topoLay->addLayout(topoTopRow);

    auto* pipelineRow = new QHBoxLayout();
    pipelineRow->setSpacing(10);

    auto* box1 = createPipelineNodeBox(topoCard_);
    boxAppsTitle_ = new QLabel(box1);
    boxAppsTitle_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px;"));
    boxAppsVal_ = new QLabel(box1);
    boxAppsVal_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
    static_cast<QVBoxLayout*>(box1->layout())->addWidget(boxAppsTitle_);
    static_cast<QVBoxLayout*>(box1->layout())->addWidget(boxAppsVal_);

    auto* arrow1 = new QLabel(QStringLiteral("→"), topoCard_);
    arrow1->setStyleSheet(QStringLiteral("color: #7c5dfa; font-size: 18px; font-weight: 700; padding: 0 2px;"));
    arrow1->setAlignment(Qt::AlignCenter);

    auto* box2 = createPipelineNodeBox(topoCard_);
    boxRelayTitle_ = new QLabel(box2);
    boxRelayTitle_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px;"));
    boxRelayVal_ = new QLabel(box2);
    boxRelayVal_->setStyleSheet(QStringLiteral("color: #e5e7eb; font-size: 13px; font-weight: 700;"));
    static_cast<QVBoxLayout*>(box2->layout())->addWidget(boxRelayTitle_);
    static_cast<QVBoxLayout*>(box2->layout())->addWidget(boxRelayVal_);

    auto* arrow2 = new QLabel(QStringLiteral("→"), topoCard_);
    arrow2->setStyleSheet(QStringLiteral("color: #7c5dfa; font-size: 18px; font-weight: 700; padding: 0 2px;"));
    arrow2->setAlignment(Qt::AlignCenter);

    boxTargetFrame_ = createPipelineNodeBox(topoCard_, QStringLiteral("PipelineBoxTarget"));
    boxTargetTitle_ = new QLabel(boxTargetFrame_);
    boxTargetTitle_->setStyleSheet(QStringLiteral("color: #c4b5fd; font-size: 11px; font-weight: 600;"));
    boxTargetVal_ = new QLabel(boxTargetFrame_);
    boxTargetVal_->setStyleSheet(QStringLiteral("color: #f5f3ff; font-size: 13px; font-weight: 700;"));
    static_cast<QVBoxLayout*>(boxTargetFrame_->layout())->addWidget(boxTargetTitle_);
    static_cast<QVBoxLayout*>(boxTargetFrame_->layout())->addWidget(boxTargetVal_);

    auto* arrow3 = new QLabel(QStringLiteral("⇄"), topoCard_);
    arrow3->setStyleSheet(QStringLiteral("color: #10b981; font-size: 18px; font-weight: 700; padding: 0 2px;"));
    arrow3->setAlignment(Qt::AlignCenter);

    boxFallbackFrame_ = createPipelineNodeBox(topoCard_, QStringLiteral("PipelineBoxReject"));
    boxFallbackTitle_ = new QLabel(boxFallbackFrame_);
    boxFallbackTitle_->setStyleSheet(QStringLiteral("color: #fca5a5; font-size: 11px;"));
    boxFallbackVal_ = new QLabel(boxFallbackFrame_);
    boxFallbackVal_->setStyleSheet(QStringLiteral("color: #f87171; font-size: 13px; font-weight: 700;"));
    static_cast<QVBoxLayout*>(boxFallbackFrame_->layout())->addWidget(boxFallbackTitle_);
    static_cast<QVBoxLayout*>(boxFallbackFrame_->layout())->addWidget(boxFallbackVal_);

    pipelineRow->addWidget(box1, 1);
    pipelineRow->addWidget(arrow1, 0);
    pipelineRow->addWidget(box2, 1);
    pipelineRow->addWidget(arrow2, 0);
    pipelineRow->addWidget(boxTargetFrame_, 1);
    pipelineRow->addWidget(arrow3, 0);
    pipelineRow->addWidget(boxFallbackFrame_, 1);
    topoLay->addLayout(pipelineRow);
    root->addWidget(topoCard_);

    // Card 2: Searchable Candidate Nodes Table
    auto* tableCard = createCardFrame(this);
    auto* tableLay = new QVBoxLayout(tableCard);
    tableLay->setContentsMargins(16, 14, 16, 14);
    tableLay->setSpacing(12);

    auto* tableTopRow = new QHBoxLayout();
    tableTopRow->setSpacing(12);
    tableTitleLabel_ = new QLabel(tableCard);
    tableTitleLabel_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
    tableTopRow->addWidget(tableTitleLabel_, 1);

    searchEdit_ = new QLineEdit(tableCard);
    searchEdit_->setClearButtonEnabled(true);
    searchEdit_->setMinimumWidth(260);
    searchEdit_->setMaximumWidth(340);
    tableTopRow->addWidget(searchEdit_, 0);
    tableLay->addLayout(tableTopRow);

    table_ = new QTableWidget(0, 5, tableCard);
    styleCleanDarkTable(table_);
    table_->verticalHeader()->setDefaultSectionSize(46);
    table_->setItemDelegateForColumn(0, new PillItemDelegate(10, table_));
    table_->setItemDelegateForColumn(2, new PillItemDelegate(10, table_));
    table_->setItemDelegateForColumn(4, new PillItemDelegate(10, table_));

    QHeaderView* hh = table_->horizontalHeader();
    hh->setSectionResizeMode(0, QHeaderView::Fixed);
    table_->setColumnWidth(0, 128);
    hh->setSectionResizeMode(1, QHeaderView::Stretch);
    hh->setSectionResizeMode(2, QHeaderView::Fixed);
    table_->setColumnWidth(2, 128);
    hh->setSectionResizeMode(3, QHeaderView::Fixed);
    table_->setColumnWidth(3, 196);
    hh->setSectionResizeMode(4, QHeaderView::Fixed);
    table_->setColumnWidth(4, 124);
    tableLay->addWidget(table_, 1);

    root->addWidget(tableCard, 1);

    connect(searchEdit_, &QLineEdit::textChanged, this, [this](const QString&) { rebuildTable(); });
    connect(table_, &QTableWidget::cellClicked, this, [this](int row, int) {
        if (auto* nameItem = table_->item(row, 1)) {
            selectedTarget_ = nameItem->data(Qt::UserRole).toString();
            refreshSelectionColumn();
            updateTopologyCard(lastSnapshot_);
        }
    });
    connect(table_, &QTableWidget::cellDoubleClicked, this, [this](int row, int) {
        if (auto* nameItem = table_->item(row, 1)) {
            selectedTarget_ = nameItem->data(Qt::UserRole).toString();
            refreshSelectionColumn();
            applyLock(true);
        }
    });
    connect(applyBtn_, &QPushButton::clicked, this, [this]() { applyLock(true); });
    connect(disableBtn_, &QPushButton::clicked, this, [this]() { applyLock(false); });
    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });

    retranslateUi();
    if (client_) {
        updateSnapshot(client_->snapshot());
    }
}

void AiLockPage::focusSearch() {
    if (searchEdit_) {
        searchEdit_->setFocus();
    }
}

void AiLockPage::retranslateUi() {
    titleLabel_->setText(uiText("AI 专用节点锁定 (Fail-Closed 防漏保护)",
                                "AI Dedicated Node Lock (Fail-Closed Protection)"));
    subtitleLabel_->setText(uiText(
        "将 Claude、Codex、ChatGPT 与 Antigravity 桌面端及 CLI 流量锁定至单一纯净节点，故障立即 REJECT 绝不回落直连",
        "Pin Claude, Codex, ChatGPT & Antigravity desktop/CLI traffic to one clean exit node with instant Fail-Closed REJECT"));
    disableBtn_->setText(uiText("停用锁定", "Disable Lock"));
    topoTitleLabel_->setText(uiText("当前 AI 流量专属加密链路拓扑", "Active AI Traffic Tunnel Topology"));
    topoDescLabel_->setText(uiText(
        "已自动接管系统代理 + TUN 虚拟网卡 + 规则模式；目标节点不可用时 100% 阻断防 IP 漂移封号",
        "System Proxy + Enhanced TUN + Rule mode are enforced automatically; zero leak to direct IP on node failure"));
    boxAppsTitle_->setText(uiText("受保护应用与域名", "Protected Apps & Domains"));
    boxAppsVal_->setText(QStringLiteral("Claude · OpenAI · Google AI"));
    boxRelayTitle_->setText(uiText("前置链式拨号组", "Upstream Relay Group"));
    boxTargetTitle_->setText(uiText("唯一锁定出口节点", "Pinned Exit Node"));
    boxFallbackTitle_->setText(uiText("断线熔断策略", "Disconnect Fail-Safe"));
    boxFallbackVal_->setText(QStringLiteral("Fail-Closed REJECT"));
    tableTitleLabel_->setText(uiText("选择要锁定的具体代理节点（支持内联节点与外部 proxy-providers）",
                                     "Select Exit Node to Pin (Inline Proxies & Proxy-Providers)"));
    searchEdit_->setPlaceholderText(uiText("搜索节点名称 / 地区 / 协议...", "Search node name / region / protocol..."));

    table_->setHorizontalHeaderLabels({
        uiText("锁定状态", "Lock Status"),
        uiText("节点名称", "Node Name"),
        uiText("协议类型", "Protocol"),
        uiText("归属来源", "Source / Provider"),
        uiText("最新延迟", "Latency"),
    });

    updateSnapshot(lastSnapshot_);
}

void AiLockPage::updateSnapshot(const QJsonObject& snapshot) {
    lastSnapshot_ = snapshot;
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    lockEnabled_ = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    lockedTarget_ = settings.value(QStringLiteral("claudeProxyLockTarget")).toString().trimmed();

    if (selectedTarget_.isEmpty() || !lockedTarget_.isEmpty()) {
        if (!lockedTarget_.isEmpty() && (selectedTarget_.isEmpty() || lockEnabled_)) {
            selectedTarget_ = lockedTarget_;
        }
    }

    // Build latest latency lookup from proxy_groups
    delayCache_.clear();
    const QJsonArray groups = snapshot.value(QStringLiteral("proxy_groups")).toArray();
    for (const QJsonValue& gVal : groups) {
        const QJsonArray items = gVal.toObject().value(QStringLiteral("items")).toArray();
        for (const QJsonValue& itVal : items) {
            const QJsonObject it = itVal.toObject();
            const QString n = it.value(QStringLiteral("name")).toString();
            const int d = it.value(QStringLiteral("delay_ms")).toInt(0);
            const bool tested = it.value(QStringLiteral("tested")).toBool(false);
            if (!n.isEmpty() && (!delayCache_.contains(n) || tested)) {
                delayCache_.insert(n, qMakePair(d, tested));
            }
        }
    }

    if (lockEnabled_) {
        statusPill_->setPill(uiText("严格保护已开启", "Strict Lock Active"), PillTone::Violet);
        applyBtn_->setText(uiText("更新锁定节点", "Update Locked Node"));
        disableBtn_->setVisible(true);
    } else {
        statusPill_->setPill(uiText("未启用锁定", "Lock Inactive"), PillTone::Muted);
        applyBtn_->setText(uiText("启用 AI 专用锁定", "Enable AI Lock"));
        disableBtn_->setVisible(false);
    }

    const QString relay = snapshot.value(QStringLiteral("ai_lock_relay")).toString().trimmed();
    if (!relay.isEmpty()) {
        relayPill_->setPill(uiText("中继: ", "Relay: ") + relay, PillTone::Blue);
    } else {
        relayPill_->setPill(QString(), PillTone::Muted);
    }

    const bool connected = client_ ? client_->isConnected() : false;
    applyBtn_->setEnabled(connected && !busy_);
    disableBtn_->setEnabled(connected && !busy_);

    updateTopologyCard(snapshot);
    rebuildTable();
}

void AiLockPage::updateTopologyCard(const QJsonObject& snapshot) {
    const QString relay = snapshot.value(QStringLiteral("ai_lock_relay")).toString().trimmed();
    boxRelayVal_->setText(relay.isEmpty() ? uiText("MATCH 主策略组中继", "MATCH Primary Relay") : relay);

    const QString displayTarget = !selectedTarget_.isEmpty() ? selectedTarget_ : lockedTarget_;
    QString targetType;
    const QJsonArray cands = snapshot.value(QStringLiteral("ai_lock_candidates")).toArray();
    for (const QJsonValue& v : cands) {
        const QJsonObject obj = v.toObject();
        if (obj.value(QStringLiteral("name")).toString() == displayTarget) {
            targetType = obj.value(QStringLiteral("type")).toString();
            break;
        }
    }
    if (displayTarget.isEmpty()) {
        boxTargetVal_->setText(uiText("尚未选择节点", "No Node Selected"));
    } else if (!targetType.isEmpty()) {
        boxTargetVal_->setText(QStringLiteral("%1 (%2)").arg(displayTarget, targetType));
    } else {
        boxTargetVal_->setText(displayTarget);
    }

    const QString probeName = lockEnabled_ && !lockedTarget_.isEmpty() ? lockedTarget_ : displayTarget;
    if (!lockEnabled_) {
        topoHealthPill_->setPill(uiText("○ 未启用 Fail-Closed", "○ Lock Inactive"), PillTone::Muted);
        topoCard_->setStyleSheet(QStringLiteral(
            "QFrame#AiLockTopoCard { background: #181a24; border: 1px solid #262a36; border-radius: 12px; }"));
    } else {
        topoCard_->setStyleSheet(QStringLiteral(
            "QFrame#AiLockTopoCard { background: #181a24; border: 1px solid #48337d; border-radius: 12px; }"));
        const auto dIt = delayCache_.constFind(probeName);
        if (dIt != delayCache_.constEnd() && dIt->second) {
            if (dIt->first > 0) {
                topoHealthPill_->setPill(
                    uiText("● 专线连通正常 · ", "● Healthy · ") + QString::number(dIt->first) + QStringLiteral(" ms"),
                    PillTone::Green);
            } else {
                topoHealthPill_->setPill(uiText("▲ 目标节点超时 (已熔断)", "▲ Target Timeout (Rejected)"), PillTone::Red);
            }
        } else {
            topoHealthPill_->setPill(uiText("● 严格锁定运行中", "● Strict Lock Active"), PillTone::Blue);
        }
    }
}

void AiLockPage::rebuildTable() {
    const QJsonArray candidates = lastSnapshot_.value(QStringLiteral("ai_lock_candidates")).toArray();
    const QString profileName =
        lastSnapshot_.value(QStringLiteral("settings")).toObject().value(QStringLiteral("selected_profile")).toString();
    const QString filter = searchEdit_->text().trimmed().toLower();

    struct CandRow {
        QString name;
        QString type;
        QString provider;
    };
    QList<CandRow> rows;
    rows.reserve(candidates.size());

    bool selectedExists = false;
    for (const QJsonValue& val : candidates) {
        const QJsonObject obj = val.toObject();
        const QString name = obj.value(QStringLiteral("name")).toString();
        const QString type = obj.value(QStringLiteral("type")).toString();
        const QString provider = obj.value(QStringLiteral("provider")).toString();
        if (name == selectedTarget_) {
            selectedExists = true;
        }
        if (!filter.isEmpty()) {
            const QString hay = (name + QLatin1Char(' ') + type + QLatin1Char(' ') + provider).toLower();
            if (!hay.contains(filter)) {
                continue;
            }
        }
        rows.push_back({name, type, provider});
    }
    if (!selectedExists && !candidates.isEmpty() && selectedTarget_.isEmpty()) {
        selectedTarget_ = candidates.first().toObject().value(QStringLiteral("name")).toString();
    }

    QSignalBlocker blocker(table_);
    table_->setRowCount(static_cast<int>(rows.size()));
    int selectedRowIdx = -1;

    for (int i = 0; i < rows.size(); ++i) {
        const CandRow& r = rows.at(i);

        auto* lockCell = new QTableWidgetItem();
        lockCell->setTextAlignment(Qt::AlignCenter);
        table_->setItem(i, 0, lockCell);

        auto* nameItem = new QTableWidgetItem(r.name);
        nameItem->setData(Qt::UserRole, r.name);
        QFont nf = nameItem->font();
        nf.setBold(r.name == selectedTarget_ || (lockEnabled_ && r.name == lockedTarget_));
        nameItem->setFont(nf);
        table_->setItem(i, 1, nameItem);

        const QString protoText = r.type.isEmpty() ? QStringLiteral("Proxy") : r.type;
        auto* protoItem = new QTableWidgetItem(protoText);
        protoItem->setData(kPillToneRole, static_cast<int>(protocolTone(protoText)));
        protoItem->setTextAlignment(Qt::AlignCenter);
        table_->setItem(i, 2, protoItem);

        const QString srcText =
            r.provider.isEmpty() ? (profileName.isEmpty() ? uiText("当前配置", "Active Profile") : profileName)
                                 : (QStringLiteral("provider:") + r.provider);
        auto* srcItem = new QTableWidgetItem(srcText);
        srcItem->setForeground(QColor(QStringLiteral("#9ca3af")));
        table_->setItem(i, 3, srcItem);

        auto* delayItem = new QTableWidgetItem();
        delayItem->setTextAlignment(Qt::AlignCenter);
        const auto dIt = delayCache_.constFind(r.name);
        if (dIt != delayCache_.constEnd() && dIt->second) {
            if (dIt->first > 0) {
                delayItem->setText(QString::number(dIt->first) + QStringLiteral(" ms"));
                delayItem->setData(kPillToneRole,
                                   static_cast<int>(dIt->first <= 250 ? PillTone::Green : PillTone::Amber));
            } else {
                delayItem->setText(uiText("超时", "Timeout"));
                delayItem->setData(kPillToneRole, static_cast<int>(PillTone::Red));
            }
        } else {
            delayItem->setText(uiText("未测速", "Untested"));
            delayItem->setData(kPillToneRole, static_cast<int>(PillTone::Muted));
        }
        table_->setItem(i, 4, delayItem);

        if (r.name == selectedTarget_) {
            selectedRowIdx = i;
        }
    }

    refreshSelectionColumn();
    if (selectedRowIdx >= 0) {
        table_->selectRow(selectedRowIdx);
    }
}

void AiLockPage::refreshSelectionColumn() {
    for (int i = 0; i < table_->rowCount(); ++i) {
        QTableWidgetItem* nameItem = table_->item(i, 1);
        QTableWidgetItem* lockCell = table_->item(i, 0);
        if (!nameItem || !lockCell) {
            continue;
        }
        const QString nodeName = nameItem->data(Qt::UserRole).toString();
        const bool isLocked = lockEnabled_ && (nodeName == lockedTarget_);
        const bool isPicked = (nodeName == selectedTarget_);

        if (isLocked && isPicked) {
            lockCell->setText(uiText("✓ 当前锁定", "✓ Locked"));
            lockCell->setData(kPillToneRole, static_cast<int>(PillTone::Violet));
        } else if (isLocked) {
            lockCell->setText(uiText("● 锁定中", "● Active"));
            lockCell->setData(kPillToneRole, static_cast<int>(PillTone::Violet));
        } else if (isPicked) {
            lockCell->setText(uiText("● 已选择", "● Selected"));
            lockCell->setData(kPillToneRole, static_cast<int>(PillTone::Blue));
        } else {
            lockCell->setText(uiText("○ 点击选择", "○ Select"));
            lockCell->setData(kPillToneRole, static_cast<int>(PillTone::Muted));
        }
    }
}

void AiLockPage::applyLock(bool enable, bool allowUac) {
    if (!client_ || busy_) {
        return;
    }
    const QString target = selectedTarget_.trimmed().isEmpty() ? lockedTarget_ : selectedTarget_.trimmed();
    if (enable && target.isEmpty()) {
        ToastOverlay::show(window(), uiText("请先在下方列表选择一个代理节点", "Please select a candidate node first"),
                           PillTone::Amber);
        return;
    }

    busy_ = true;
    applyBtn_->setEnabled(false);
    disableBtn_->setEnabled(false);

    QJsonObject params;
    params.insert(QStringLiteral("enabled"), enable);
    params.insert(QStringLiteral("target"), target);
    params.insert(QStringLiteral("allow_uac"), allowUac);

    client_->call(QStringLiteral("set_ai_lock"), params,
                  [this, enable, allowUac, target](bool ok, const QJsonValue& data, const QString& err,
                                                   const QString& errCode) {
                      busy_ = false;
                      applyBtn_->setEnabled(client_ && client_->isConnected());
                      disableBtn_->setEnabled(client_ && client_->isConnected());

                      if (!ok) {
                          if (errCode == QLatin1String("SERVICE_INSTALL_REQUIRED") && !allowUac) {
                              const auto choice = QMessageBox::question(
                                  window(),
                                  uiText("需要安装 ClashFX TUN 服务", "Install ClashFX TUN Service"),
                                  uiText("开启 AI 专用锁定需要自动启用 TUN 虚拟网卡，将通过 Windows UAC 安装后台服务。是否继续？",
                                         "AI Dedicated Node Lock requires Enhanced TUN mode, which needs ClashFXService "
                                         "installed via Windows UAC. Continue?"));
                              if (choice == QMessageBox::Yes) {
                                  applyLock(enable, true);
                              }
                              return;
                          }
                          QMessageBox::warning(
                              window(), uiText("AI 锁定操作失败", "AI Lock Operation Failed"),
                              I18n::instance().translateBackendText(err));
                          return;
                      }

                      if (data.isObject()) {
                          updateSnapshot(data.toObject());
                      }
                      ToastOverlay::show(
                          window(),
                          enable ? (uiText("已将 AI 流量严格锁定至节点 ", "AI traffic strictly locked to ") + target)
                                 : uiText("已停用 AI 专用节点锁定", "AI dedicated node lock disabled"),
                          enable ? PillTone::Violet : PillTone::Muted);
                  });
}

// ===========================================================================
// MultiNodePage
// ===========================================================================

MultiNodePage::MultiNodePage(DaemonClient* client, QWidget* parent)
    : QWidget(parent)
    , client_(client) {
    auto* outerLay = new QVBoxLayout(this);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(this);
    titleLabel_ = hdr.titleLabel;
    statusPill_ = hdr.primaryBadge;
    dirtyPill_ = hdr.secondaryBadge;
    subtitleLabel_ = hdr.subtitleLabel;

    checkBtn_ = new QPushButton(this);
    speedBtn_ = new QPushButton(this);
    saveBtn_ = new QPushButton(this);
    saveBtn_->setProperty("primary", true);
    hdr.actionsLayout->addWidget(checkBtn_);
    hdr.actionsLayout->addWidget(speedBtn_);
    hdr.actionsLayout->addWidget(saveBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(this);
    auto* root = new QVBoxLayout(body);
    root->setContentsMargins(24, 16, 24, 18);
    root->setSpacing(14);
    outerLay->addWidget(body, 1);

    // 4-column KPI summary cards row
    auto* kpiGrid = new QGridLayout();
    kpiGrid->setHorizontalSpacing(12);
    kpiGrid->setVerticalSpacing(0);
    for (int col = 0; col < 4; ++col) {
        kpiGrid->setColumnStretch(col, 1);
    }

    // KPI Card 1: Fixed Local Port Listener
    auto* card1 = createCardFrame(this);
    auto* c1Lay = new QVBoxLayout(card1);
    c1Lay->setContentsMargins(16, 13, 16, 13);
    c1Lay->setSpacing(6);
    auto* c1Top = new QHBoxLayout();
    kpi1Title_ = new QLabel(card1);
    kpi1Title_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    portsSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Emerald, card1);
    c1Top->addWidget(kpi1Title_, 1);
    c1Top->addWidget(portsSwitch_, 0);
    kpi1Value_ = new QLabel(QStringLiteral("—"), card1);
    kpi1Value_->setStyleSheet(QStringLiteral(
        "color: #f9fafb; font-size: 20px; font-weight: 700; font-family: Consolas, monospace;"));
    kpi1Sub_ = new QLabel(card1);
    kpi1Sub_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px;"));
    c1Lay->addLayout(c1Top);
    c1Lay->addWidget(kpi1Value_);
    c1Lay->addWidget(kpi1Sub_);
    kpiGrid->addWidget(card1, 0, 0);

    // KPI Card 2: 204 Health Recovery
    auto* card2 = createCardFrame(this);
    auto* c2Lay = new QVBoxLayout(card2);
    c2Lay->setContentsMargins(16, 13, 16, 13);
    c2Lay->setSpacing(6);
    auto* c2Top = new QHBoxLayout();
    kpi2Title_ = new QLabel(card2);
    kpi2Title_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    recoverySwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Blue, card2);
    c2Top->addWidget(kpi2Title_, 1);
    c2Top->addWidget(recoverySwitch_, 0);
    kpi2Value_ = new QLabel(QStringLiteral("—"), card2);
    kpi2Value_->setStyleSheet(QStringLiteral("color: #f9fafb; font-size: 20px; font-weight: 700;"));
    kpi2Sub_ = new QLabel(card2);
    kpi2Sub_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px;"));
    c2Lay->addLayout(c2Top);
    c2Lay->addWidget(kpi2Value_);
    c2Lay->addWidget(kpi2Sub_);
    kpiGrid->addWidget(card2, 0, 1);

    // KPI Card 3: Connectivity Ratio
    auto* card3 = createCardFrame(this);
    auto* c3Lay = new QVBoxLayout(card3);
    c3Lay->setContentsMargins(16, 13, 16, 13);
    c3Lay->setSpacing(6);
    kpi3Title_ = new QLabel(card3);
    kpi3Title_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    kpi3Value_ = new QLabel(QStringLiteral("—"), card3);
    kpi3Value_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 18px; font-weight: 700;"));
    kpi3Sub_ = new QLabel(card3);
    kpi3Sub_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px;"));
    c3Lay->addWidget(kpi3Title_);
    c3Lay->addWidget(kpi3Value_);
    c3Lay->addWidget(kpi3Sub_);
    kpiGrid->addWidget(card3, 0, 2);

    // KPI Card 4: Multi-Instance Access Wizard
    auto* card4 = createCardFrame(this);
    auto* c4Lay = new QVBoxLayout(card4);
    c4Lay->setContentsMargins(16, 13, 16, 13);
    c4Lay->setSpacing(6);
    kpi4Title_ = new QLabel(card4);
    kpi4Title_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    auto* c4Btns = new QHBoxLayout();
    c4Btns->setSpacing(6);
    chromeGuideBtn_ = new QPushButton(card4);
    enableAllBtn_ = new QPushButton(card4);
    advToggleBtn_ = new QPushButton(card4);
    for (QPushButton* b : {chromeGuideBtn_, enableAllBtn_, advToggleBtn_}) {
        b->setStyleSheet(QStringLiteral(
            "QPushButton { padding: 4px 8px; font-size: 11px; min-height: 24px; border-radius: 6px; }"));
    }
    advToggleBtn_->setCheckable(true);
    c4Btns->addWidget(chromeGuideBtn_);
    c4Btns->addWidget(enableAllBtn_);
    c4Btns->addWidget(advToggleBtn_);
    kpi4Sub_ = new QLabel(card4);
    kpi4Sub_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px;"));
    c4Lay->addWidget(kpi4Title_);
    c4Lay->addLayout(c4Btns);
    c4Lay->addWidget(kpi4Sub_);
    kpiGrid->addWidget(card4, 0, 3);

    root->addLayout(kpiGrid);

    // Collapsible Advanced Parameters Strip
    advDrawer_ = createCardFrame(this, QStringLiteral("MultiNodeAdvCard"));
    advDrawer_->setStyleSheet(QStringLiteral(
        "QFrame#MultiNodeAdvCard { background: #151720; border: 1px solid #262a36; border-radius: 10px; }"));
    auto* advLay = new QHBoxLayout(advDrawer_);
    advLay->setContentsMargins(14, 10, 14, 10);
    advLay->setSpacing(12);

    auto makeLabeledSpin = [&](QLabel*& lbl, QSpinBox*& spin, int minV, int maxV) {
        auto* box = new QHBoxLayout();
        box->setSpacing(6);
        lbl = new QLabel(advDrawer_);
        lbl->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px;"));
        spin = new QSpinBox(advDrawer_);
        spin->setRange(minV, maxV);
        spin->setMinimumWidth(78);
        box->addWidget(lbl);
        box->addWidget(spin);
        advLay->addLayout(box);
    };
    makeLabeledSpin(firstPortLbl_, firstPortSpin_, 1024, 65000);
    makeLabeledSpin(intervalLbl_, intervalSpin_, 15, 3600);
    makeLabeledSpin(thresholdLbl_, thresholdSpin_, 2, 10);
    makeLabeledSpin(cooldownLbl_, cooldownSpin_, 60, 86400);

    advLay->addStretch(1);
    disableAllBtn_ = new QPushButton(advDrawer_);
    refreshDirectBtn_ = new QPushButton(advDrawer_);
    for (QPushButton* b : {disableAllBtn_, refreshDirectBtn_}) {
        b->setStyleSheet(QStringLiteral("padding: 4px 10px; font-size: 11px; border-radius: 6px;"));
    }
    advLay->addWidget(disableAllBtn_);
    advLay->addWidget(refreshDirectBtn_);
    advDrawer_->setVisible(false);
    root->addWidget(advDrawer_);

    // Nodes Table Card
    auto* tableCard = createCardFrame(this);
    auto* tableLay = new QVBoxLayout(tableCard);
    tableLay->setContentsMargins(14, 10, 14, 12);
    tableLay->setSpacing(0);

    table_ = new QTableWidget(0, 6, tableCard);
    styleCleanDarkTable(table_);
    table_->verticalHeader()->setDefaultSectionSize(48);
    table_->setItemDelegateForColumn(3, new PillItemDelegate(8, table_));

    QHeaderView* hh = table_->horizontalHeader();
    hh->setSectionResizeMode(0, QHeaderView::Fixed);
    table_->setColumnWidth(0, 66);
    hh->setSectionResizeMode(1, QHeaderView::Stretch);
    hh->setSectionResizeMode(2, QHeaderView::Fixed);
    table_->setColumnWidth(2, 158);
    hh->setSectionResizeMode(3, QHeaderView::Fixed);
    table_->setColumnWidth(3, 158);
    hh->setSectionResizeMode(4, QHeaderView::Fixed);
    table_->setColumnWidth(4, 100);
    hh->setSectionResizeMode(5, QHeaderView::Fixed);
    table_->setColumnWidth(5, 168);
    tableLay->addWidget(table_, 1);
    root->addWidget(tableCard, 1);

    auto markDirty = [this]() {
        if (!refreshingFromSnapshot_) {
            userEditedPending_ = true;
            dirtyPill_->setPill(uiText("未保存修改", "Unsaved Edits"), PillTone::Amber);
        }
    };
    connect(portsSwitch_, &ToggleSwitch::toggled, this, [markDirty](bool) { markDirty(); });
    connect(recoverySwitch_, &ToggleSwitch::toggled, this, [markDirty](bool) { markDirty(); });
    connect(firstPortSpin_, &QSpinBox::valueChanged, this, [markDirty](int) { markDirty(); });
    connect(intervalSpin_, &QSpinBox::valueChanged, this, [markDirty](int) { markDirty(); });
    connect(thresholdSpin_, &QSpinBox::valueChanged, this, [markDirty](int) { markDirty(); });
    connect(cooldownSpin_, &QSpinBox::valueChanged, this, [markDirty](int) { markDirty(); });

    connect(advToggleBtn_, &QPushButton::toggled, this, [this](bool on) {
        advancedExpanded_ = on;
        advDrawer_->setVisible(on);
    });
    connect(enableAllBtn_, &QPushButton::clicked, this, [this]() { toggleAllNodes(true); });
    connect(disableAllBtn_, &QPushButton::clicked, this, [this]() { toggleAllNodes(false); });
    connect(chromeGuideBtn_, &QPushButton::clicked, this, [this]() { showChromeLaunchHelper(); });
    connect(saveBtn_, &QPushButton::clicked, this, [this]() { saveNodeServices(); });

    connect(checkBtn_, &QPushButton::clicked, this, [this]() {
        if (!client_) {
            return;
        }
        checkBtn_->setEnabled(false);
        client_->call(QStringLiteral("node_check_connectivity"), QJsonObject(),
                      [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                          checkBtn_->setEnabled(client_ && client_->isConnected());
                          if (!ok) {
                              ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          if (data.isObject()) {
                              updateSnapshot(data.toObject());
                          }
                          ToastOverlay::show(window(), uiText("已完成全节点 204 连通巡检", "Completed 204 health check"),
                                             PillTone::Green);
                      });
    });

    connect(refreshDirectBtn_, &QPushButton::clicked, this, [this]() {
        if (!client_) {
            return;
        }
        refreshDirectBtn_->setEnabled(false);
        client_->call(QStringLiteral("node_refresh_direct"), QJsonObject(),
                      [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                          refreshDirectBtn_->setEnabled(client_ && client_->isConnected());
                          if (!ok) {
                              ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          if (data.isObject()) {
                              updateSnapshot(data.toObject());
                          }
                          ToastOverlay::show(window(),
                                             uiText("已通过 DIRECT 直连强制刷新订阅", "Refreshed subscription via DIRECT"),
                                             PillTone::Green);
                      });
    });

    connect(speedBtn_, &QPushButton::clicked, this, [this]() {
        if (!client_) {
            return;
        }
        const bool isTesting =
            lastSnapshot_.value(QStringLiteral("node_services")).toObject().value(QStringLiteral("is_speed_testing")).toBool(false);
        if (isTesting) {
            client_->call(QStringLiteral("node_speed_test_cancel"), QJsonObject(),
                          [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                              if (!ok) {
                                  ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                                  return;
                              }
                              if (data.isObject()) {
                                  updateSnapshot(data.toObject());
                              }
                              ToastOverlay::show(window(), uiText("已取消 20MB 下载测速", "Cancelled speed test"),
                                                 PillTone::Amber);
                          });
            return;
        }

        if (userEditedPending_) {
            const auto ans = QMessageBox::question(
                window(), uiText("存在未保存的端口改动", "Unsaved Port Changes"),
                uiText("多节点端口或开关有尚未保存的修改。是否先保存并生效再开始测速？",
                       "There are unsaved port/switch changes. Save and apply them before starting the speed test?"));
            if (ans != QMessageBox::Yes) {
                return;
            }
            saveNodeServices();
            return;
        }

        client_->call(QStringLiteral("node_speed_test_start"), QJsonObject(),
                      [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                          if (!ok) {
                              ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          if (data.isObject()) {
                              updateSnapshot(data.toObject());
                          }
                          ToastOverlay::show(window(),
                                             uiText("已启动顺序 20MB 下载测速", "Started sequential 20MB speed test"),
                                             PillTone::Blue);
                      });
    });

    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });
    retranslateUi();
    if (client_) {
        updateSnapshot(client_->snapshot());
    }
}

void MultiNodePage::retranslateUi() {
    titleLabel_->setText(uiText("多节点固定端口代理与巡检测速", "Multi-Node Fixed Ports & Health Watchdog"));
    subtitleLabel_->setText(uiText(
        "每个节点绑定独立本地 HTTP + SOCKS5 混合端口，供指纹浏览器 / Chrome 多开隔离固定 IP",
        "Bind each node to a dedicated local HTTP + SOCKS5 port for multi-profile browsers & isolated fixed IPs"));
    checkBtn_->setText(uiText("立即 204 巡检", "Check 204 Now"));
    saveBtn_->setText(uiText("保存端口设置并生效", "Save & Apply Ports"));

    kpi1Title_->setText(uiText("固定本地端口监听", "Fixed Local Port Listeners"));
    kpi2Title_->setText(uiText("204 故障直连自愈守护", "204 Direct Auto-Recovery"));
    kpi3Title_->setText(uiText("204 连通健康率", "204 Health Ratio"));
    kpi3Sub_->setText(uiText("独立端口流量不受 AI 锁定影响", "Dedicated port traffic bypasses AI Lock"));
    kpi4Title_->setText(uiText("多开接入向导", "Multi-Profile Quick Tools"));
    chromeGuideBtn_->setText(uiText("Chrome 隔离参数", "Chrome Flags"));
    enableAllBtn_->setText(uiText("全部启用", "Enable All"));
    advToggleBtn_->setText(uiText("参数", "Config"));
    kpi4Sub_->setText(uiText("支持 SwitchyOmega / AdsPower / Chrome", "Works with SwitchyOmega / AdsPower / Chrome"));

    firstPortLbl_->setText(uiText("起始端口", "Start Port"));
    intervalLbl_->setText(uiText("巡检间隔(s)", "Check(s)"));
    thresholdLbl_->setText(uiText("失败阈值", "Threshold"));
    cooldownLbl_->setText(uiText("冷却(s)", "Cooldown(s)"));
    disableAllBtn_->setText(uiText("全部停用", "Disable All"));
    refreshDirectBtn_->setText(uiText("DIRECT 直连刷新订阅", "Refresh Sub via DIRECT"));

    table_->setHorizontalHeaderLabels({
        uiText("启用", "Enable"),
        uiText("节点名称", "Node Name"),
        uiText("本地监听地址 (HTTP / SOCKS5)", "Local Endpoint (HTTP / SOCKS5)"),
        uiText("204 连通状态", "204 Health"),
        uiText("20MB 下载测速", "20MB Speed"),
        uiText("快捷复制", "Quick Copy"),
    });

    if (userEditedPending_) {
        dirtyPill_->setPill(uiText("未保存修改", "Unsaved Edits"), PillTone::Amber);
    } else {
        dirtyPill_->setPill(QString(), PillTone::Muted);
    }
    updateSnapshot(lastSnapshot_);
}

void MultiNodePage::updateSnapshot(const QJsonObject& snapshot) {
    lastSnapshot_ = snapshot;
    const QJsonObject ns = snapshot.value(QStringLiteral("node_services")).toObject();
    const QJsonObject nsSettings = ns.value(QStringLiteral("settings")).toObject();
    const bool connected = client_ ? client_->isConnected() : false;

    const bool isTesting = ns.value(QStringLiteral("is_speed_testing")).toBool(false);
    speedBtn_->setProperty("danger", isTesting);
    speedBtn_->setText(isTesting ? uiText("取消测速", "Cancel Speed Test")
                                 : uiText("开始 20MB 测速", "20MB Speed Test"));
    speedBtn_->style()->unpolish(speedBtn_);
    speedBtn_->style()->polish(speedBtn_);

    checkBtn_->setEnabled(connected && !ns.value(QStringLiteral("is_checking")).toBool(false));
    speedBtn_->setEnabled(connected);
    saveBtn_->setEnabled(connected);
    refreshDirectBtn_->setEnabled(connected && !ns.value(QStringLiteral("is_refreshing")).toBool(false));

    if (!userEditedPending_) {
        refreshingFromSnapshot_ = true;
        portsSwitch_->setCheckedSilent(nsSettings.value(QStringLiteral("ports_enabled")).toBool(true));
        recoverySwitch_->setCheckedSilent(nsSettings.value(QStringLiteral("recovery_enabled")).toBool(true));
        firstPortSpin_->setValue(nsSettings.value(QStringLiteral("first_port")).toInt(21000));
        intervalSpin_->setValue(nsSettings.value(QStringLiteral("check_interval_sec")).toInt(60));
        thresholdSpin_->setValue(nsSettings.value(QStringLiteral("failure_threshold")).toInt(3));
        cooldownSpin_->setValue(nsSettings.value(QStringLiteral("refresh_cooldown_sec")).toInt(300));
        dirtyPill_->setPill(QString(), PillTone::Muted);
        refreshingFromSnapshot_ = false;
    }

    // Extract AI locked node & node type lookup
    const QJsonObject appSettings = snapshot.value(QStringLiteral("settings")).toObject();
    const QString aiLockedNode = appSettings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false)
                                     ? appSettings.value(QStringLiteral("claudeProxyLockTarget")).toString()
                                     : QString();

    QHash<QString, QString> nodeTypeMap;
    const QJsonArray cands = snapshot.value(QStringLiteral("ai_lock_candidates")).toArray();
    for (const QJsonValue& v : cands) {
        const QJsonObject obj = v.toObject();
        nodeTypeMap.insert(obj.value(QStringLiteral("name")).toString(), obj.value(QStringLiteral("type")).toString());
    }
    const QJsonArray groups = snapshot.value(QStringLiteral("proxy_groups")).toArray();
    for (const QJsonValue& g : groups) {
        for (const QJsonValue& itVal : g.toObject().value(QStringLiteral("items")).toArray()) {
            const QJsonObject it = itVal.toObject();
            const QString n = it.value(QStringLiteral("name")).toString();
            const QString t = it.value(QStringLiteral("type")).toString();
            if (!n.isEmpty() && !t.isEmpty() && !nodeTypeMap.contains(n)) {
                nodeTypeMap.insert(n, t);
            }
        }
    }

    updateKpiCards(ns);
    rebuildOrRefreshTable(ns, aiLockedNode, nodeTypeMap);
}

void MultiNodePage::updateKpiCards(const QJsonObject& ns) {
    const QJsonObject nsSettings = ns.value(QStringLiteral("settings")).toObject();
    const QJsonArray rows = ns.value(QStringLiteral("rows")).toArray();

    int enabledCount = 0;
    int healthyCount = 0;
    int failedCount = 0;
    int minPort = 0;
    int maxPort = 0;

    for (const QJsonValue& v : rows) {
        const QJsonObject r = v.toObject();
        const QString name = r.value(QStringLiteral("name")).toString();
        const bool en = userEditedPending_ ? pendingEnabled_.value(name, r.value(QStringLiteral("enabled")).toBool())
                                           : r.value(QStringLiteral("enabled")).toBool();
        const int port = userEditedPending_ ? pendingPorts_.value(name, r.value(QStringLiteral("port")).toInt())
                                            : r.value(QStringLiteral("port")).toInt();
        if (en) {
            ++enabledCount;
            if (port > 0) {
                if (minPort == 0 || port < minPort) {
                    minPort = port;
                }
                if (port > maxPort) {
                    maxPort = port;
                }
            }
        }
        const QString healthKind = r.value(QStringLiteral("health")).toObject().value(QStringLiteral("kind")).toString();
        if (healthKind == QLatin1String("healthy")) {
            ++healthyCount;
        } else if (healthKind == QLatin1String("failed") || healthKind == QLatin1String("unavailable")) {
            ++failedCount;
        }
    }

    const bool portsEnabled = portsSwitch_->isChecked();
    const int firstPort = firstPortSpin_->value();
    const int intervalSec = intervalSpin_->value();
    const int threshold = thresholdSpin_->value();

    if (portsEnabled && enabledCount > 0 && minPort > 0) {
        statusPill_->setPill(
            uiText("已监听 ", "Listening ") + QString::number(enabledCount) + QStringLiteral(" / ") +
                QString::number(rows.size()) + uiText(" 个独立端口", " ports"),
            PillTone::Green);
        kpi1Value_->setText(minPort == maxPort
                                ? QString::number(minPort)
                                : QStringLiteral("%1 – %2").arg(minPort).arg(maxPort));
    } else if (!portsEnabled) {
        statusPill_->setPill(uiText("多节点监听已关闭", "Port Listeners Off"), PillTone::Muted);
        kpi1Value_->setText(uiText("未启用", "Disabled"));
    } else {
        statusPill_->setPill(uiText("已就绪 0 / ", "Ready 0 / ") + QString::number(rows.size()), PillTone::Amber);
        kpi1Value_->setText(QStringLiteral("0 端口"));
    }
    kpi1Sub_->setText(uiText("起始端口 ", "Start port ") + QString::number(firstPort) + QStringLiteral(" · HTTP & SOCKS5"));

    if (recoverySwitch_->isChecked()) {
        kpi2Value_->setText(uiText("每 ", "Every ") + QString::number(intervalSec) + uiText("s 巡检", "s Check"));
    } else {
        kpi2Value_->setText(uiText("已暂停守护", "Watchdog Paused"));
    }
    kpi2Sub_->setText(uiText("连续失败 ", "Auto-refresh after ") + QString::number(threshold) +
                      uiText(" 次自动直连刷新订阅", " failures via DIRECT"));

    if (rows.isEmpty()) {
        kpi3Value_->setText(uiText("暂无节点", "No Nodes"));
    } else {
        kpi3Value_->setText(QStringLiteral("%1 %2 · %3 %4")
                                .arg(healthyCount)
                                .arg(uiText("正常", "OK"))
                                .arg(failedCount)
                                .arg(uiText("告警", "Warn")));
        kpi3Value_->setStyleSheet(QStringLiteral("color: %1; font-size: 18px; font-weight: 700;")
                                      .arg(failedCount > 0 ? QStringLiteral("#fbbf24") : QStringLiteral("#34d399")));
    }
    Q_UNUSED(nsSettings);
}

void MultiNodePage::rebuildOrRefreshTable(const QJsonObject& ns,
                                          const QString& aiLockedNode,
                                          const QHash<QString, QString>& nodeTypeMap) {
    const QJsonArray rows = ns.value(QStringLiteral("rows")).toArray();

    QStringList incomingNames;
    incomingNames.reserve(rows.size());
    for (const QJsonValue& v : rows) {
        incomingNames.push_back(v.toObject().value(QStringLiteral("name")).toString());
    }

    const bool structureChanged = (incomingNames != orderedNodeNames_) || (table_->rowCount() != rows.size());

    if (structureChanged) {
        orderedNodeNames_ = incomingNames;
        QSignalBlocker tableBlocker(table_);
        table_->setRowCount(static_cast<int>(rows.size()));

        for (int i = 0; i < rows.size(); ++i) {
            const QJsonObject r = rows.at(i).toObject();
            const QString name = r.value(QStringLiteral("name")).toString();
            const bool en = userEditedPending_ ? pendingEnabled_.value(name, r.value(QStringLiteral("enabled")).toBool())
                                               : r.value(QStringLiteral("enabled")).toBool();
            const int port = userEditedPending_ ? pendingPorts_.value(name, r.value(QStringLiteral("port")).toInt())
                                                : r.value(QStringLiteral("port")).toInt();
            pendingEnabled_[name] = en;
            pendingPorts_[name] = port;

            // Col 0: Centered Emerald ToggleSwitch
            auto* swWrap = new QWidget(table_);
            swWrap->setObjectName(QStringLiteral("MnRowWrap"));
            swWrap->setStyleSheet(QStringLiteral("QWidget#MnRowWrap { background: transparent; }"));
            auto* swLay = new QHBoxLayout(swWrap);
            swLay->setContentsMargins(0, 0, 0, 0);
            swLay->setAlignment(Qt::AlignCenter);
            auto* sw = new ToggleSwitch(ToggleSwitch::Accent::Emerald, swWrap);
            sw->setProperty("node_switch", true);
            sw->setCheckedSilent(en);
            swLay->addWidget(sw);
            table_->setCellWidget(i, 0, swWrap);

            connect(sw, &ToggleSwitch::toggled, this, [this, name](bool checked) {
                if (refreshingFromSnapshot_) {
                    return;
                }
                pendingEnabled_[name] = checked;
                userEditedPending_ = true;
                dirtyPill_->setPill(uiText("未保存修改", "Unsaved Edits"), PillTone::Amber);
                updateKpiCards(lastSnapshot_.value(QStringLiteral("node_services")).toObject());
            });

            // Col 1: Node Name + Protocol / AI Lock Pill
            auto* nameWrap = new QWidget(table_);
            nameWrap->setObjectName(QStringLiteral("MnRowWrap"));
            nameWrap->setStyleSheet(QStringLiteral("QWidget#MnRowWrap { background: transparent; }"));
            auto* nameLay = new QHBoxLayout(nameWrap);
            nameLay->setContentsMargins(8, 0, 8, 0);
            nameLay->setSpacing(8);
            auto* nameLbl = new QLabel(name, nameWrap);
            nameLbl->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 12px; font-weight: 600;"));
            nameLbl->setAttribute(Qt::WA_TransparentForMouseEvents);
            nameLay->addWidget(nameLbl);

            const QString proto = nodeTypeMap.value(name);
            const bool isAiLocked = (!aiLockedNode.isEmpty() && name == aiLockedNode);
            if (!proto.isEmpty() || isAiLocked) {
                auto* pill = new BadgePill(nameWrap, true);
                pill->setObjectName(QStringLiteral("RowProtoPill"));
                if (isAiLocked) {
                    pill->setPill(proto.isEmpty() ? uiText("AI 锁定", "AI Lock")
                                                  : (proto + uiText(" · AI 锁定", " · AI Lock")),
                                  PillTone::Violet);
                } else {
                    pill->setPill(proto, protocolTone(proto));
                }
                pill->setAttribute(Qt::WA_TransparentForMouseEvents);
                nameLay->addWidget(pill);
            }
            nameLay->addStretch(1);
            table_->setCellWidget(i, 1, nameWrap);

            // Col 2: Inline Clean Monospace Port Editor (127.0.0.1:<port>)
            auto* portSpin = new QSpinBox(table_);
            portSpin->setRange(1024, 65000);
            portSpin->setPrefix(QStringLiteral("127.0.0.1:"));
            portSpin->setButtonSymbols(QAbstractSpinBox::NoButtons);
            portSpin->setAlignment(Qt::AlignLeft | Qt::AlignVCenter);
            portSpin->setValue(qBound(1024, port, 65000));
            portSpin->setStyleSheet(QStringLiteral(
                "QSpinBox { background: transparent; border: 1px solid transparent; border-radius: 6px;"
                "  padding: 4px 8px; color: #e5e7eb; font-family: Consolas, 'Cascadia Mono', monospace;"
                "  font-size: 12px; font-weight: 600; }"
                "QSpinBox:hover { border-color: #353b4a; background: #13151e; }"
                "QSpinBox:focus { border-color: #3b82f6; background: #0f1116; }"));
            table_->setCellWidget(i, 2, portSpin);

            connect(portSpin, &QSpinBox::valueChanged, this, [this, name](int newPort) {
                if (refreshingFromSnapshot_) {
                    return;
                }
                pendingPorts_[name] = newPort;
                userEditedPending_ = true;
                dirtyPill_->setPill(uiText("未保存修改", "Unsaved Edits"), PillTone::Amber);
                updateKpiCards(lastSnapshot_.value(QStringLiteral("node_services")).toObject());
            });

            // Col 3: 204 Health Status Pill
            auto* healthItem = new QTableWidgetItem();
            healthItem->setTextAlignment(Qt::AlignCenter);
            table_->setItem(i, 3, healthItem);

            // Col 4: 20MB Speed Result
            auto* speedItem = new QTableWidgetItem();
            speedItem->setTextAlignment(Qt::AlignLeft | Qt::AlignVCenter);
            table_->setItem(i, 4, speedItem);

            // Col 5: Quick Copy HTTP / SOCKS5 Buttons
            auto* copyWrap = new QWidget(table_);
            copyWrap->setObjectName(QStringLiteral("MnRowWrap"));
            copyWrap->setStyleSheet(QStringLiteral("QWidget#MnRowWrap { background: transparent; }"));
            auto* copyLay = new QHBoxLayout(copyWrap);
            copyLay->setContentsMargins(6, 0, 6, 0);
            copyLay->setSpacing(6);
            auto* httpBtn = new QPushButton(uiText("复制 HTTP", "Copy HTTP"), copyWrap);
            auto* socksBtn = new QPushButton(QStringLiteral("SOCKS5"), copyWrap);
            for (QPushButton* b : {httpBtn, socksBtn}) {
                b->setStyleSheet(QStringLiteral(
                    "QPushButton { padding: 4px 8px; font-size: 11px; min-height: 24px; border-radius: 6px; }"));
                copyLay->addWidget(b);
            }
            copyLay->addStretch(1);
            table_->setCellWidget(i, 5, copyWrap);

            connect(httpBtn, &QPushButton::clicked, this, [this, name, portSpin]() {
                const QString addr = QStringLiteral("http://127.0.0.1:%1").arg(portSpin->value());
                if (QClipboard* cb = QApplication::clipboard()) {
                    cb->setText(addr);
                }
                ToastOverlay::show(
                    window(),
                    uiText("已复制 ", "Copied ") + name + uiText(" 的 HTTP 代理地址 ", " HTTP endpoint ") + addr,
                    PillTone::Green);
            });
            connect(socksBtn, &QPushButton::clicked, this, [this, name, portSpin]() {
                const QString addr = QStringLiteral("socks5://127.0.0.1:%1").arg(portSpin->value());
                if (QClipboard* cb = QApplication::clipboard()) {
                    cb->setText(addr);
                }
                ToastOverlay::show(
                    window(),
                    uiText("已复制 ", "Copied ") + name + uiText(" 的 SOCKS5 代理地址 ", " SOCKS5 endpoint ") + addr,
                    PillTone::Green);
            });
        }
    } else if (!userEditedPending_) {
        refreshingFromSnapshot_ = true;
        for (int i = 0; i < rows.size(); ++i) {
            const QJsonObject r = rows.at(i).toObject();
            const QString name = r.value(QStringLiteral("name")).toString();
            const bool en = r.value(QStringLiteral("enabled")).toBool();
            const int port = r.value(QStringLiteral("port")).toInt();
            pendingEnabled_[name] = en;
            pendingPorts_[name] = port;

            if (QWidget* swWrap = table_->cellWidget(i, 0)) {
                if (auto* sw = swWrap->findChild<ToggleSwitch*>()) {
                    sw->setCheckedSilent(en);
                }
            }
            if (auto* portSpin = qobject_cast<QSpinBox*>(table_->cellWidget(i, 2))) {
                QSignalBlocker sb(portSpin);
                portSpin->setValue(qBound(1024, port, 65000));
            }
        }
        refreshingFromSnapshot_ = false;
    }

    // ALWAYS update live health (Col 3) & speed (Col 4) even when edits are pending
    for (int i = 0; i < rows.size(); ++i) {
        const QJsonObject r = rows.at(i).toObject();
        const QJsonObject h = r.value(QStringLiteral("health")).toObject();
        const QString hKind = h.value(QStringLiteral("kind")).toString();
        const int delayMs = h.value(QStringLiteral("delay_ms")).toInt(0);
        const int fails = h.value(QStringLiteral("consecutive_failures")).toInt(0);

        if (QTableWidgetItem* hItem = table_->item(i, 3)) {
            if (hKind == QLatin1String("healthy")) {
                hItem->setText(uiText("● 204 正常 · ", "● 204 OK · ") + QString::number(delayMs) + QStringLiteral(" ms"));
                hItem->setData(kPillToneRole, static_cast<int>(PillTone::Green));
            } else if (hKind == QLatin1String("failed")) {
                hItem->setText(fails > 0 ? (uiText("▲ 超时 (连续 ", "▲ Fail (x") + QString::number(fails) +
                                            uiText(" 次)", ")"))
                                         : uiText("▲ 204 失败", "▲ 204 Fail"));
                hItem->setData(kPillToneRole, static_cast<int>(PillTone::Red));
            } else if (hKind == QLatin1String("unavailable")) {
                hItem->setText(uiText("● 内核未监听", "● Unavailable"));
                hItem->setData(kPillToneRole, static_cast<int>(PillTone::Amber));
            } else {
                hItem->setText(uiText("○ 未检测", "○ Unchecked"));
                hItem->setData(kPillToneRole, static_cast<int>(PillTone::Muted));
            }
        }

        const QJsonObject sp = r.value(QStringLiteral("speed")).toObject();
        const QString spKind = sp.value(QStringLiteral("kind")).toString();
        const double mbps = sp.value(QStringLiteral("mbps")).toDouble(0.0);

        if (QTableWidgetItem* sItem = table_->item(i, 4)) {
            QFont f = sItem->font();
            f.setBold(spKind == QLatin1String("measured") || spKind == QLatin1String("measuring"));
            sItem->setFont(f);
            if (spKind == QLatin1String("measured")) {
                sItem->setText(QString::number(mbps, 'f', 2) + QStringLiteral(" MB/s"));
                sItem->setForeground(QColor(QStringLiteral("#38bdf8")));
            } else if (spKind == QLatin1String("measuring")) {
                sItem->setText(uiText("测速中...", "Testing..."));
                sItem->setForeground(QColor(QStringLiteral("#60a5fa")));
            } else if (spKind == QLatin1String("waiting")) {
                sItem->setText(uiText("排队中", "Waiting"));
                sItem->setForeground(QColor(QStringLiteral("#9ca3af")));
            } else if (spKind == QLatin1String("failed")) {
                sItem->setText(uiText("测速失败", "Failed"));
                sItem->setForeground(QColor(QStringLiteral("#f87171")));
            } else if (spKind == QLatin1String("cancelled")) {
                sItem->setText(uiText("已取消", "Cancelled"));
                sItem->setForeground(QColor(QStringLiteral("#fbbf24")));
            } else {
                sItem->setText(QStringLiteral("—"));
                sItem->setForeground(QColor(QStringLiteral("#6b7280")));
            }
        }
    }
}

void MultiNodePage::toggleAllNodes(bool enableAll) {
    refreshingFromSnapshot_ = true;
    for (int i = 0; i < table_->rowCount(); ++i) {
        if (i < orderedNodeNames_.size()) {
            pendingEnabled_[orderedNodeNames_.at(i)] = enableAll;
        }
        if (QWidget* swWrap = table_->cellWidget(i, 0)) {
            if (auto* sw = swWrap->findChild<ToggleSwitch*>()) {
                sw->setCheckedSilent(enableAll);
            }
        }
    }
    refreshingFromSnapshot_ = false;
    userEditedPending_ = true;
    dirtyPill_->setPill(uiText("未保存修改", "Unsaved Edits"), PillTone::Amber);
    updateKpiCards(lastSnapshot_.value(QStringLiteral("node_services")).toObject());
}

void MultiNodePage::saveNodeServices() {
    if (!client_) {
        return;
    }

    // Validate duplicate ports client-side for instant feedback
    QSet<int> seenPorts;
    QJsonObject portsMap;
    QJsonObject enabledMap;
    for (const QString& name : orderedNodeNames_) {
        const int port = pendingPorts_.value(name, 21000);
        const bool en = pendingEnabled_.value(name, false);
        if (port < 1024 || port > 65000) {
            ToastOverlay::show(window(),
                               uiText("端口号必须在 1024 到 65000 之间", "Ports must be between 1024 and 65000"),
                               PillTone::Red);
            return;
        }
        if (seenPorts.contains(port)) {
            ToastOverlay::show(window(),
                               uiText("检测到冲突端口号 ", "Duplicate port detected: ") + QString::number(port),
                               PillTone::Red);
            return;
        }
        seenPorts.insert(port);
        portsMap.insert(name, port);
        enabledMap.insert(name, en);
    }

    QJsonObject settingsObj;
    settingsObj.insert(QStringLiteral("ports_enabled"), portsSwitch_->isChecked());
    settingsObj.insert(QStringLiteral("recovery_enabled"), recoverySwitch_->isChecked());
    settingsObj.insert(QStringLiteral("first_port"), firstPortSpin_->value());
    settingsObj.insert(QStringLiteral("check_interval_sec"), intervalSpin_->value());
    settingsObj.insert(QStringLiteral("failure_threshold"), thresholdSpin_->value());
    settingsObj.insert(QStringLiteral("refresh_cooldown_sec"), cooldownSpin_->value());

    QJsonObject params;
    params.insert(QStringLiteral("settings"), settingsObj);
    params.insert(QStringLiteral("ports"), portsMap);
    params.insert(QStringLiteral("enabled"), enabledMap);

    saveBtn_->setEnabled(false);
    client_->call(QStringLiteral("update_node_services"), params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      saveBtn_->setEnabled(client_ && client_->isConnected());
                      if (!ok) {
                          QMessageBox::warning(window(), uiText("保存失败", "Save Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      userEditedPending_ = false;
                      dirtyPill_->setPill(QString(), PillTone::Muted);
                      if (data.isObject()) {
                          updateSnapshot(data.toObject());
                      }
                      ToastOverlay::show(window(),
                                         uiText("多节点固定端口配置已保存并应用", "Multi-node port listeners saved & applied"),
                                         PillTone::Green);
                  });
}

void MultiNodePage::showChromeLaunchHelper() {
    QString sampleNode;
    int samplePort = firstPortSpin_->value();
    const int curRow = table_->currentRow();
    if (curRow >= 0 && curRow < orderedNodeNames_.size()) {
        sampleNode = orderedNodeNames_.at(curRow);
        samplePort = pendingPorts_.value(sampleNode, samplePort);
    } else if (!orderedNodeNames_.isEmpty()) {
        for (const QString& n : orderedNodeNames_) {
            if (pendingEnabled_.value(n, false)) {
                sampleNode = n;
                samplePort = pendingPorts_.value(n, samplePort);
                break;
            }
        }
        if (sampleNode.isEmpty()) {
            sampleNode = orderedNodeNames_.first();
            samplePort = pendingPorts_.value(sampleNode, samplePort);
        }
    }

    const QString cmd = QStringLiteral(
                            "chrome.exe --proxy-server=\"http://127.0.0.1:%1\" "
                            "--user-data-dir=\"C:\\ChromeProfiles\\port-%1\"")
                            .arg(samplePort);
    if (QClipboard* cb = QApplication::clipboard()) {
        cb->setText(cmd);
    }
    ToastOverlay::show(
        window(),
        uiText("已复制 Chrome 隔离启动命令 (端口 ", "Copied Chrome isolated launch command (port ") +
            QString::number(samplePort) + QStringLiteral(")"),
        PillTone::Blue, 3400);
}

// ===========================================================================
// NodeDiagnosticPage
// ===========================================================================

NodeDiagnosticPage::NodeDiagnosticPage(DaemonClient* client, QWidget* parent)
    : QWidget(parent)
    , client_(client) {
    auto* outerLay = new QVBoxLayout(this);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(this);
    titleLabel_ = hdr.titleLabel;
    statusPill_ = hdr.primaryBadge;
    subtitleLabel_ = hdr.subtitleLabel;

    copyBtn_ = new QPushButton(this);
    runBtn_ = new QPushButton(this);
    runBtn_->setProperty("primary", true);
    hdr.actionsLayout->addWidget(copyBtn_);
    hdr.actionsLayout->addWidget(runBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(this);
    auto* root = new QVBoxLayout(body);
    root->setContentsMargins(24, 16, 24, 18);
    root->setSpacing(14);
    outerLay->addWidget(body, 1);

    // Controls Card
    auto* ctrlCard = createCardFrame(body);
    auto* ctrlLay = new QHBoxLayout(ctrlCard);
    ctrlLay->setContentsMargins(16, 12, 16, 12);
    ctrlLay->setSpacing(12);

    targetLbl_ = new QLabel(ctrlCard);
    targetLbl_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px; font-weight: 600;"));
    nodeCombo_ = new QComboBox(ctrlCard);
    nodeCombo_->setMinimumWidth(240);
    providerTag_ = new QLabel(ctrlCard);
    providerTag_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px;"));

    urlLbl_ = new QLabel(ctrlCard);
    urlLbl_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px; font-weight: 600;"));
    urlCombo_ = new QComboBox(ctrlCard);
    urlCombo_->setEditable(true);
    urlCombo_->setMinimumWidth(280);
    urlCombo_->addItems({
        QStringLiteral("https://www.gstatic.com/generate_204"),
        QStringLiteral("https://cp.cloudflare.com/generate_204"),
        QStringLiteral("https://api.anthropic.com"),
        QStringLiteral("https://api.openai.com"),
    });

    ctrlLay->addWidget(targetLbl_);
    ctrlLay->addWidget(nodeCombo_, 1);
    ctrlLay->addWidget(providerTag_);
    ctrlLay->addSpacing(8);
    ctrlLay->addWidget(urlLbl_);
    ctrlLay->addWidget(urlCombo_, 1);
    root->addWidget(ctrlCard);

    // 3 KPI Result Cards
    auto* kpiRow = new QHBoxLayout();
    kpiRow->setSpacing(12);

    auto* k1 = createCardFrame(this);
    auto* k1Lay = new QVBoxLayout(k1);
    k1Lay->setContentsMargins(16, 13, 16, 13);
    k1Lay->setSpacing(6);
    kpiVerdictTitle_ = new QLabel(k1);
    kpiVerdictTitle_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    auto* k1Top = new QHBoxLayout();
    kpiVerdictVal_ = new QLabel(QStringLiteral("—"), k1);
    kpiVerdictVal_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 18px; font-weight: 700;"));
    kpiVerdictCode_ = new BadgePill(k1, true);
    k1Top->addWidget(kpiVerdictVal_, 1);
    k1Top->addWidget(kpiVerdictCode_, 0);
    k1Lay->addWidget(kpiVerdictTitle_);
    k1Lay->addLayout(k1Top);
    kpiRow->addWidget(k1, 1);

    auto* k2 = createCardFrame(this);
    auto* k2Lay = new QVBoxLayout(k2);
    k2Lay->setContentsMargins(16, 13, 16, 13);
    k2Lay->setSpacing(4);
    kpiSummaryTitle_ = new QLabel(k2);
    kpiSummaryTitle_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    kpiSummaryVal_ = new QLabel(QStringLiteral("—"), k2);
    kpiSummaryVal_->setWordWrap(true);
    kpiSummaryVal_->setStyleSheet(QStringLiteral("color: #e5e7eb; font-size: 13px; font-weight: 600;"));
    kpiRawMsg_ = new QLabel(k2);
    kpiRawMsg_->setWordWrap(true);
    kpiRawMsg_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 11px; font-family: Consolas, monospace;"));
    k2Lay->addWidget(kpiSummaryTitle_);
    k2Lay->addWidget(kpiSummaryVal_);
    k2Lay->addWidget(kpiRawMsg_);
    kpiRow->addWidget(k2, 2);

    auto* k3 = createCardFrame(this);
    auto* k3Lay = new QVBoxLayout(k3);
    k3Lay->setContentsMargins(16, 13, 16, 13);
    k3Lay->setSpacing(4);
    kpiAdviceTitle_ = new QLabel(k3);
    kpiAdviceTitle_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    kpiAdviceVal_ = new QLabel(QStringLiteral("—"), k3);
    kpiAdviceVal_->setWordWrap(true);
    kpiAdviceVal_->setStyleSheet(QStringLiteral("color: #cbd5e1; font-size: 12px;"));
    k3Lay->addWidget(kpiAdviceTitle_);
    k3Lay->addWidget(kpiAdviceVal_);
    kpiRow->addWidget(k3, 2);

    root->addLayout(kpiRow);

    // Console Log Card
    auto* logCard = createCardFrame(this);
    auto* logLay = new QVBoxLayout(logCard);
    logLay->setContentsMargins(16, 13, 16, 14);
    logLay->setSpacing(8);
    consoleTitle_ = new QLabel(logCard);
    consoleTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
    outputEdit_ = new QPlainTextEdit(logCard);
    outputEdit_->setReadOnly(true);
    outputEdit_->setStyleSheet(QStringLiteral(
        "QPlainTextEdit {"
        "  background: #101218;"
        "  border: 1px solid #262a36;"
        "  border-radius: 8px;"
        "  padding: 10px;"
        "  color: #cbd5e1;"
        "  font-family: Consolas, 'Cascadia Mono', monospace;"
        "  font-size: 12px;"
        "}"));
    logLay->addWidget(consoleTitle_);
    logLay->addWidget(outputEdit_, 1);
    root->addWidget(logCard, 1);

    connect(nodeCombo_, &QComboBox::currentTextChanged, this, [this](const QString& nodeName) {
        const QString prov = nodeProviderMap_.value(nodeName);
        providerTag_->setText(prov.isEmpty() ? QString() : (QStringLiteral("provider:") + prov));
    });
    connect(runBtn_, &QPushButton::clicked, this, [this]() { runProbe(); });
    connect(copyBtn_, &QPushButton::clicked, this, [this]() { copyReport(); });
    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });

    retranslateUi();
    if (client_) {
        updateSnapshot(client_->snapshot());
    }
}

void NodeDiagnosticPage::retranslateUi() {
    titleLabel_->setText(uiText("单节点链路诊断与故障排查", "Single-Node Probe & Fault Diagnostics"));
    subtitleLabel_->setText(uiText(
        "针对指定代理节点执行真实 HTTPS TLS 握手与目标地址连通性测试，精准定位订阅、鉴权或节点故障",
        "Execute real TLS handshake and endpoint reachability probes against any proxy node to diagnose failures"));
    copyBtn_->setText(uiText("复制诊断报告", "Copy Report"));
    runBtn_->setText(probing_ ? uiText("诊断中...", "Probing...") : uiText("开始深度诊断", "Run Deep Probe"));
    targetLbl_->setText(uiText("目标节点", "Target Node"));
    urlLbl_->setText(uiText("探测地址", "Probe URL"));
    kpiVerdictTitle_->setText(uiText("连通判定", "Probe Verdict"));
    kpiSummaryTitle_->setText(uiText("故障诊断结论", "Diagnostic Summary"));
    kpiAdviceTitle_->setText(uiText("排查与修复建议", "Recommended Action"));
    consoleTitle_->setText(uiText("完整探测日志与响应报文", "Detailed Probe Output"));

    if (lastReport_.isEmpty()) {
        statusPill_->setPill(probing_ ? uiText("探测中...", "Probing...") : uiText("就绪", "Ready"),
                             probing_ ? PillTone::Blue : PillTone::Muted);
    } else {
        const bool ok = lastReport_.value(QStringLiteral("healthy")).toBool(false);
        const int ms = lastReport_.value(QStringLiteral("delay_ms")).toInt(0);
        const QString code = lastReport_.value(QStringLiteral("code")).toString();
        const QString sumZh = lastReport_.value(QStringLiteral("summary_zh")).toString();
        const QString sumEn = lastReport_.value(QStringLiteral("summary_en")).toString();
        const QString advZh = lastReport_.value(QStringLiteral("advice_zh")).toString();
        const QString advEn = lastReport_.value(QStringLiteral("advice_en")).toString();

        statusPill_->setPill(
            ok ? (uiText("节点正常 · ", "Healthy · ") + QString::number(ms) + QStringLiteral(" ms"))
               : uiText("诊断发现异常", "Fault Detected"),
            ok ? PillTone::Green : PillTone::Red);
        kpiVerdictVal_->setText(ok ? (uiText("✓ 正常 · ", "✓ OK · ") + QString::number(ms) + QStringLiteral(" ms"))
                                   : uiText("✕ 连接异常", "✕ Failed"));
        kpiVerdictVal_->setStyleSheet(QStringLiteral("color: %1; font-size: 18px; font-weight: 700;")
                                          .arg(ok ? QStringLiteral("#34d399") : QStringLiteral("#f87171")));
        kpiVerdictCode_->setPill(code.isEmpty() ? lastReport_.value(QStringLiteral("kind")).toString() : code,
                                 ok ? PillTone::Green : PillTone::Red);
        kpiSummaryVal_->setText(I18n::instance().isChinese() ? (sumZh.isEmpty() ? sumEn : sumZh)
                                                             : (sumEn.isEmpty() ? sumZh : sumEn));
        kpiAdviceVal_->setText(I18n::instance().isChinese() ? (advZh.isEmpty() ? advEn : advZh)
                                                            : (advEn.isEmpty() ? advZh : advEn));
    }
}

void NodeDiagnosticPage::updateSnapshot(const QJsonObject& snapshot) {
    lastSnapshot_ = snapshot;
    populateNodeChoices(snapshot);
    runBtn_->setEnabled(client_ && client_->isConnected() && !probing_ && nodeCombo_->count() > 0);
}

void NodeDiagnosticPage::populateNodeChoices(const QJsonObject& snapshot) {
    const QString prev = nodeCombo_->currentText();
    QStringList names;
    QSet<QString> seen;
    nodeProviderMap_.clear();

    const QJsonArray cands = snapshot.value(QStringLiteral("ai_lock_candidates")).toArray();
    for (const QJsonValue& v : cands) {
        const QJsonObject obj = v.toObject();
        const QString n = obj.value(QStringLiteral("name")).toString();
        if (!n.isEmpty() && !seen.contains(n)) {
            seen.insert(n);
            names.push_back(n);
            nodeProviderMap_.insert(n, obj.value(QStringLiteral("provider")).toString());
        }
    }

    const QJsonArray groups = snapshot.value(QStringLiteral("proxy_groups")).toArray();
    for (const QJsonValue& g : groups) {
        for (const QJsonValue& itVal : g.toObject().value(QStringLiteral("items")).toArray()) {
            const QString n = itVal.toObject().value(QStringLiteral("name")).toString();
            if (!n.isEmpty() && !seen.contains(n)) {
                seen.insert(n);
                names.push_back(n);
            }
        }
    }

    QSignalBlocker sb(nodeCombo_);
    nodeCombo_->clear();
    nodeCombo_->addItems(names);
    if (!prev.isEmpty() && names.contains(prev)) {
        nodeCombo_->setCurrentText(prev);
    } else if (!names.isEmpty()) {
        nodeCombo_->setCurrentIndex(0);
    }
    const QString cur = nodeCombo_->currentText();
    const QString prov = nodeProviderMap_.value(cur);
    providerTag_->setText(prov.isEmpty() ? QString() : (QStringLiteral("provider:") + prov));
}

void NodeDiagnosticPage::startDiagnosisForNode(const QString& nodeName) {
    if (!nodeName.isEmpty() && nodeCombo_->findText(nodeName) >= 0) {
        nodeCombo_->setCurrentText(nodeName);
    }
    runProbe();
}

void NodeDiagnosticPage::runProbe() {
    if (!client_ || probing_) {
        return;
    }
    const QString nodeName = nodeCombo_->currentText().trimmed();
    if (nodeName.isEmpty()) {
        ToastOverlay::show(window(), uiText("没有可供诊断的代理节点", "No proxy node available to probe"),
                           PillTone::Amber);
        return;
    }

    probing_ = true;
    runBtn_->setEnabled(false);
    runBtn_->setText(uiText("诊断中...", "Probing..."));
    statusPill_->setPill(uiText("探测中...", "Probing..."), PillTone::Blue);

    QJsonObject params;
    params.insert(QStringLiteral("node_name"), nodeName);
    params.insert(QStringLiteral("provider_name"), nodeProviderMap_.value(nodeName));
    params.insert(QStringLiteral("benchmark_url"), urlCombo_->currentText().trimmed());

    client_->call(QStringLiteral("diagnose_node"), params,
                  [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
                      probing_ = false;
                      runBtn_->setEnabled(client_ && client_->isConnected() && nodeCombo_->count() > 0);
                      runBtn_->setText(uiText("开始深度诊断", "Run Deep Probe"));

                      if (!ok) {
                          statusPill_->setPill(uiText("调用失败", "RPC Error"), PillTone::Red);
                          outputEdit_->setPlainText(I18n::instance().translateBackendText(err));
                          ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }

                      lastReport_ = data.toObject();
                      kpiRawMsg_->setText(lastReport_.value(QStringLiteral("raw_message")).toString());
                      outputEdit_->setPlainText(lastReport_.value(QStringLiteral("formatted")).toString());
                      retranslateUi();
                  });
}

void NodeDiagnosticPage::copyReport() {
    const QString text = outputEdit_->toPlainText().trimmed();
    if (text.isEmpty()) {
        ToastOverlay::show(window(), uiText("请先执行一次节点诊断", "Run a diagnostic probe first"), PillTone::Amber);
        return;
    }
    if (QClipboard* cb = QApplication::clipboard()) {
        cb->setText(text);
    }
    ToastOverlay::show(window(), uiText("已复制完整诊断报告", "Copied diagnostic report"), PillTone::Green);
}

}  // namespace clashfx
