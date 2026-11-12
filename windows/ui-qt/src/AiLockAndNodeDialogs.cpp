#include "AiLockAndNodeDialogs.h"
#include "DaemonClient.h"
#include "I18n.h"
#include "TrayIconGenerator.h"

#include <QApplication>
#include <QClipboard>
#include <QFormLayout>
#include <QGroupBox>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QJsonArray>
#include <QMessageBox>
#include <QSet>
#include <QVBoxLayout>

namespace clashfx {

// ============================================================================
// AiLockDialog
// ============================================================================

AiLockDialog::AiLockDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setWindowTitle(L("AI 专用锁定 (AI Proxy Lock)", "AI Proxy Lock"));
    setMinimumWidth(520);

    auto* root = new QVBoxLayout(this);
    root->setSpacing(12);

    auto* descLabel = new QLabel(
        L("将 Claude、Codex、ChatGPT 与 Antigravity 桌面端及 CLI 流量严格锁定到单一具体代理节点：\n"
          "• 自动开启规则模式 + 增强模式 (TUN) + 系统代理；\n"
          "• 目标节点故障时立即 REJECT 阻断，绝不泄漏或回落至 DIRECT / 其他节点。",
          "Strictly locks Claude, Codex, ChatGPT, and Antigravity desktop & CLI traffic to one concrete proxy node:\n"
          "• Enforces Rule Mode + Enhanced TUN + System Proxy;\n"
          "• Fails closed (REJECT) if the locked node goes down — never falls back to DIRECT or other nodes."),
        this);
    descLabel->setWordWrap(true);
    descLabel->setStyleSheet(QStringLiteral("color: #52525b; font-size: 12px;"));
    root->addWidget(descLabel);

    auto* statusBox = new QGroupBox(L("当前保护状态", "Protection Status"), this);
    auto* statusLayout = new QVBoxLayout(statusBox);
    statusLabel_ = new QLabel(statusBox);
    statusLabel_->setStyleSheet(QStringLiteral("font-weight: 600; font-size: 13px;"));
    relayLabel_ = new QLabel(statusBox);
    relayLabel_->setStyleSheet(QStringLiteral("color: #52525b; font-size: 12px;"));
    statusLayout->addWidget(statusLabel_);
    statusLayout->addWidget(relayLabel_);
    root->addWidget(statusBox);

    auto* formLayout = new QFormLayout();
    targetCombo_ = new QComboBox(this);
    targetCombo_->setMinimumWidth(320);
    formLayout->addRow(L("锁定目标节点:", "Target Concrete Node:"), targetCombo_);
    root->addLayout(formLayout);

    auto* btnRow = new QHBoxLayout();
    btnRow->addStretch(1);
    disableBtn_ = new QPushButton(L("停用 AI 专用锁定", "Disable Lock"), this);
    enableBtn_ = new QPushButton(L("启用 / 更新锁定", "Enable / Update Lock"), this);
    enableBtn_->setDefault(true);
    auto* closeBtn = new QPushButton(L("关闭", "Close"), this);
    btnRow->addWidget(disableBtn_);
    btnRow->addWidget(enableBtn_);
    btnRow->addWidget(closeBtn);
    root->addLayout(btnRow);

    connect(enableBtn_, &QPushButton::clicked, this, &AiLockDialog::onEnableClicked);
    connect(disableBtn_, &QPushButton::clicked, this, &AiLockDialog::onDisableClicked);
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::close);

    if (client_) {
        connect(client_, &DaemonClient::stateChanged, this, &AiLockDialog::refreshFromSnapshot);
        refreshFromSnapshot(client_->snapshot());
    }
}

void AiLockDialog::refreshFromSnapshot(const QJsonObject& snapshot) {
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const bool locked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    const QString currentTarget = settings.value(QStringLiteral("claudeProxyLockTarget")).toString();
    const QString relay = snapshot.value(QStringLiteral("ai_lock_relay")).toString();

    if (locked) {
        statusLabel_->setText(L("已启用 — 锁定节点: ", "ACTIVE — Locked to: ") + currentTarget);
        statusLabel_->setStyleSheet(QStringLiteral("color: #059669; font-weight: 700; font-size: 13px;"));
    } else {
        statusLabel_->setText(L("未启用", "Inactive"));
        statusLabel_->setStyleSheet(QStringLiteral("color: #71717a; font-weight: 600; font-size: 13px;"));
    }

    if (!relay.isEmpty()) {
        relayLabel_->setText(L("中继前置 (MATCH dialer-proxy): ", "MATCH Relay Group: ") + relay);
        relayLabel_->setVisible(true);
    } else {
        relayLabel_->setVisible(false);
    }

    const QString prevSelected = targetCombo_->currentData().toString();
    targetCombo_->clear();
    const QJsonArray candidates = snapshot.value(QStringLiteral("ai_lock_candidates")).toArray();
    int matchIdx = -1;
    for (int i = 0; i < candidates.size(); ++i) {
        const QJsonObject item = candidates.at(i).toObject();
        const QString name = item.value(QStringLiteral("name")).toString();
        const QString type = item.value(QStringLiteral("type")).toString();
        const QString server = item.value(QStringLiteral("server")).toString();
        QString label = name;
        if (!type.isEmpty() || !server.isEmpty()) {
            label += QStringLiteral("  [%1%2%3]")
                         .arg(type,
                              (!type.isEmpty() && !server.isEmpty()) ? QStringLiteral(" • ") : QString(),
                              server);
        }
        targetCombo_->addItem(label, name);
        if (!currentTarget.isEmpty() && name == currentTarget) {
            matchIdx = i;
        } else if (matchIdx < 0 && !prevSelected.isEmpty() && name == prevSelected) {
            matchIdx = i;
        }
    }
    if (matchIdx >= 0) {
        targetCombo_->setCurrentIndex(matchIdx);
    }

    disableBtn_->setEnabled(locked);
    enableBtn_->setEnabled(targetCombo_->count() > 0);
}

void AiLockDialog::onEnableClicked() {
    if (!client_) {
        return;
    }
    const QString target = targetCombo_->currentData().toString().trimmed();
    if (target.isEmpty()) {
        QMessageBox::warning(this,
                             windowTitle(),
                             L("当前配置没有可用的具体代理节点，请先导入包含节点的订阅或配置。",
                               "No concrete proxy nodes are available in the active profile."));
        return;
    }
    enableBtn_->setEnabled(false);
    QJsonObject params;
    params.insert(QStringLiteral("enabled"), true);
    params.insert(QStringLiteral("target"), target);
    params.insert(QStringLiteral("allow_uac"), true);
    client_->call(QStringLiteral("set_ai_lock"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        enableBtn_->setEnabled(true);
        if (!ok) {
            QMessageBox::critical(this, windowTitle(), err);
        }
    });
}

void AiLockDialog::onDisableClicked() {
    if (!client_) {
        return;
    }
    disableBtn_->setEnabled(false);
    QJsonObject params;
    params.insert(QStringLiteral("enabled"), false);
    params.insert(QStringLiteral("target"), QString());
    params.insert(QStringLiteral("allow_uac"), false);
    client_->call(QStringLiteral("set_ai_lock"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        disableBtn_->setEnabled(true);
        if (!ok) {
            QMessageBox::critical(this, windowTitle(), err);
        }
    });
}

// ============================================================================
// MultiNodeDialog
// ============================================================================

MultiNodeDialog::MultiNodeDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setWindowTitle(L("多节点固定端口代理与巡检测速", "Multiple Node Fixed-Port Proxies"));
    resize(880, 560);

    auto* root = new QVBoxLayout(this);
    root->setSpacing(10);

    auto* topBar = new QHBoxLayout();
    portsEnabledCheck_ = new QCheckBox(L("启用多节点固定本地端口", "Enable Fixed Local Ports"), this);
    recoveryEnabledCheck_ = new QCheckBox(L("启用 60s 204 巡检与失败直连自愈", "Enable 60s 204 Health Watchdog & Direct Refresh"), this);
    auto* firstPortLabel = new QLabel(L("起始端口:", "Start Port:"), this);
    firstPortSpin_ = new QSpinBox(this);
    firstPortSpin_->setRange(1024, 65000);
    firstPortSpin_->setValue(21000);

    topBar->addWidget(portsEnabledCheck_);
    topBar->addSpacing(12);
    topBar->addWidget(recoveryEnabledCheck_);
    topBar->addSpacing(12);
    topBar->addWidget(firstPortLabel);
    topBar->addWidget(firstPortSpin_);
    topBar->addStretch(1);
    root->addLayout(topBar);

    statusBanner_ = new QLabel(this);
    statusBanner_->setStyleSheet(QStringLiteral(
        "background-color: #f4f4f5; color: #27272a; border: 1px solid #e4e4e7; border-radius: 6px; padding: 6px 10px;"));
    root->addWidget(statusBanner_);

    model_ = new QStandardItemModel(0, 5, this);
    model_->setHorizontalHeaderLabels({
        L("启用", "Enabled"),
        L("节点名称", "Node Name"),
        L("本地端口 (HTTP+SOCKS5)", "Local Port (HTTP+SOCKS5)"),
        L("204 连通状态", "204 Connectivity"),
        L("下载速度 (20MB)", "Download Speed"),
    });

    tableView_ = new QTableView(this);
    tableView_->setModel(model_);
    tableView_->setSelectionBehavior(QAbstractItemView::SelectRows);
    tableView_->setSelectionMode(QAbstractItemView::SingleSelection);
    tableView_->setAlternatingRowColors(true);
    tableView_->verticalHeader()->setVisible(false);
    tableView_->horizontalHeader()->setSectionResizeMode(0, QHeaderView::ResizeToContents);
    tableView_->horizontalHeader()->setSectionResizeMode(1, QHeaderView::Stretch);
    tableView_->horizontalHeader()->setSectionResizeMode(2, QHeaderView::ResizeToContents);
    tableView_->horizontalHeader()->setSectionResizeMode(3, QHeaderView::ResizeToContents);
    tableView_->horizontalHeader()->setSectionResizeMode(4, QHeaderView::ResizeToContents);
    root->addWidget(tableView_, 1);

    auto* row1 = new QHBoxLayout();
    auto* selectAllBtn = new QPushButton(L("全部勾选", "Enable All"), this);
    auto* disableAllBtn = new QPushButton(L("全部停用", "Disable All"), this);
    saveBtn_ = new QPushButton(L("保存端口设置并重载", "Save & Apply Ports"), this);
    saveBtn_->setDefault(true);
    checkBtn_ = new QPushButton(L("立即 204 巡检", "Check 204 Now"), this);
    directRefreshBtn_ = new QPushButton(L("直连刷新订阅", "Direct Refresh Sub"), this);
    speedTestBtn_ = new QPushButton(L("开始 20MB 测速", "Start 20MB Speed Test"), this);

    row1->addWidget(selectAllBtn);
    row1->addWidget(disableAllBtn);
    row1->addWidget(saveBtn_);
    row1->addStretch(1);
    row1->addWidget(checkBtn_);
    row1->addWidget(directRefreshBtn_);
    row1->addWidget(speedTestBtn_);
    root->addLayout(row1);

    auto* row2 = new QHBoxLayout();
    auto* copyHttpBtn = new QPushButton(L("复制选中 HTTP 代理", "Copy HTTP Address"), this);
    auto* copySocksBtn = new QPushButton(L("复制选中 SOCKS5 代理", "Copy SOCKS5 Address"), this);
    auto* chromeGuideBtn = new QPushButton(L("Chrome 多开分流说明", "Chrome Setup Guide"), this);
    auto* closeBtn = new QPushButton(L("关闭", "Close"), this);

    row2->addWidget(copyHttpBtn);
    row2->addWidget(copySocksBtn);
    row2->addWidget(chromeGuideBtn);
    row2->addStretch(1);
    row2->addWidget(closeBtn);
    root->addLayout(row2);

    connect(model_, &QStandardItemModel::itemChanged, this, [this](QStandardItem* item) {
        if (!updatingModel_ && item && (item->column() == 0 || item->column() == 2)) {
            userEditedPending_ = true;
        }
    });
    connect(portsEnabledCheck_, &QCheckBox::toggled, this, [this]() {
        if (!updatingModel_) userEditedPending_ = true;
    });
    connect(recoveryEnabledCheck_, &QCheckBox::toggled, this, [this]() {
        if (!updatingModel_) userEditedPending_ = true;
    });
    connect(firstPortSpin_, &QSpinBox::valueChanged, this, [this]() {
        if (!updatingModel_) userEditedPending_ = true;
    });

    connect(selectAllBtn, &QPushButton::clicked, this, &MultiNodeDialog::onSelectAll);
    connect(disableAllBtn, &QPushButton::clicked, this, &MultiNodeDialog::onDisableAll);
    connect(saveBtn_, &QPushButton::clicked, this, &MultiNodeDialog::onSaveAndApply);
    connect(checkBtn_, &QPushButton::clicked, this, &MultiNodeDialog::onCheckConnectivity);
    connect(directRefreshBtn_, &QPushButton::clicked, this, &MultiNodeDialog::onDirectRefresh);
    connect(speedTestBtn_, &QPushButton::clicked, this, &MultiNodeDialog::onToggleSpeedTest);
    connect(copyHttpBtn, &QPushButton::clicked, this, &MultiNodeDialog::onCopyHttp);
    connect(copySocksBtn, &QPushButton::clicked, this, &MultiNodeDialog::onCopySocks);
    connect(chromeGuideBtn, &QPushButton::clicked, this, &MultiNodeDialog::onShowChromeGuide);
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::close);

    if (client_) {
        connect(client_, &DaemonClient::stateChanged, this, &MultiNodeDialog::refreshFromSnapshot);
        refreshFromSnapshot(client_->snapshot());
    }
}

QString MultiNodeDialog::formatHealth(const QJsonObject& healthObj, bool portEnabled, bool portsGlobalEnabled) {
    if (!portsGlobalEnabled || !portEnabled) {
        return L("已停用", "Disabled");
    }
    const QString kind = healthObj.value(QStringLiteral("kind")).toString();
    if (kind == QStringLiteral("healthy")) {
        const int ms = healthObj.value(QStringLiteral("delay_ms")).toInt();
        return QStringLiteral("204 OK (%1 ms)").arg(ms);
    }
    if (kind == QStringLiteral("failed")) {
        const int fails = healthObj.value(QStringLiteral("consecutive_failures")).toInt(1);
        return L("失败 (连续 ", "Failed (x") + QString::number(fails) + (I18n::instance().isChinese() ? QStringLiteral(" 次)") : QStringLiteral(")"));
    }
    if (kind == QStringLiteral("unavailable")) {
        return L("不可用", "Unavailable");
    }
    return L("未检测", "Unchecked");
}

QString MultiNodeDialog::formatSpeed(const QJsonObject& speedObj) {
    const QString kind = speedObj.value(QStringLiteral("kind")).toString();
    const double mbps = speedObj.value(QStringLiteral("mbps")).toDouble(0.0);
    if (kind == QStringLiteral("measuring")) {
        return L("测速中 ", "Testing ") + QString::number(mbps, 'f', 2) + QStringLiteral(" MB/s");
    }
    if (kind == QStringLiteral("measured")) {
        return QString::number(mbps, 'f', 2) + QStringLiteral(" MB/s");
    }
    if (kind == QStringLiteral("waiting")) {
        return L("排队中...", "Queued...");
    }
    if (kind == QStringLiteral("cancelled")) {
        return L("已取消", "Cancelled");
    }
    if (kind == QStringLiteral("failed")) {
        return L("测速失败", "Failed");
    }
    return QStringLiteral("—");
}

void MultiNodeDialog::refreshFromSnapshot(const QJsonObject& snapshot) {
    const QJsonObject ns = snapshot.value(QStringLiteral("node_services")).toObject();
    const QJsonObject nsSettings = ns.value(QStringLiteral("settings")).toObject();
    const QJsonArray rows = ns.value(QStringLiteral("rows")).toArray();

    const bool portsGlobalEnabled = nsSettings.value(QStringLiteral("ports_enabled")).toBool(true);
    const bool recoveryEnabled = nsSettings.value(QStringLiteral("recovery_enabled")).toBool(true);
    const int firstPort = nsSettings.value(QStringLiteral("first_port")).toInt(21000);
    isSpeedTesting_ = ns.value(QStringLiteral("is_speed_testing")).toBool(false);

    const QString statusText = ns.value(QStringLiteral("status_text")).toString();
    statusBanner_->setText(statusText.isEmpty()
                               ? L("每个启用节点在 127.0.0.1:<端口> 同时监听 HTTP 与 SOCKS5 入站。",
                                   "Each enabled node listens for both HTTP and SOCKS5 on 127.0.0.1:<port>.")
                               : statusText);

    speedTestBtn_->setText(isSpeedTesting_
                               ? L("停止下载测速", "Stop Speed Test")
                               : L("开始 20MB 测速", "Start 20MB Speed Test"));
    checkBtn_->setEnabled(!ns.value(QStringLiteral("is_checking")).toBool(false));
    directRefreshBtn_->setEnabled(!ns.value(QStringLiteral("is_refreshing")).toBool(false));

    updatingModel_ = true;
    if (!userEditedPending_) {
        portsEnabledCheck_->setChecked(portsGlobalEnabled);
        recoveryEnabledCheck_->setChecked(recoveryEnabled);
        firstPortSpin_->setValue(firstPort);
    }

    const int prevSelectedRow = tableView_->currentIndex().isValid() ? tableView_->currentIndex().row() : -1;

    if (!userEditedPending_ || model_->rowCount() != rows.size()) {
        model_->removeRows(0, model_->rowCount());
        for (int i = 0; i < rows.size(); ++i) {
            const QJsonObject r = rows.at(i).toObject();
            const QString name = r.value(QStringLiteral("name")).toString();
            const int port = r.value(QStringLiteral("port")).toInt();
            const bool en = r.value(QStringLiteral("enabled")).toBool(true);

            auto* checkItem = new QStandardItem();
            checkItem->setCheckable(true);
            checkItem->setCheckState(en ? Qt::Checked : Qt::Unchecked);
            checkItem->setEditable(false);

            auto* nameItem = new QStandardItem(name);
            nameItem->setEditable(false);

            auto* portItem = new QStandardItem(QString::number(port));
            portItem->setEditable(true);

            auto* healthItem = new QStandardItem(formatHealth(r.value(QStringLiteral("health")).toObject(), en, portsGlobalEnabled));
            healthItem->setEditable(false);

            auto* speedItem = new QStandardItem(formatSpeed(r.value(QStringLiteral("speed")).toObject()));
            speedItem->setEditable(false);

            model_->appendRow({checkItem, nameItem, portItem, healthItem, speedItem});
        }
        if (prevSelectedRow >= 0 && prevSelectedRow < model_->rowCount()) {
            tableView_->selectRow(prevSelectedRow);
        }
    } else {
        // Update live health & speed columns without clobbering unsaved port/checkbox edits!
        for (int i = 0; i < rows.size() && i < model_->rowCount(); ++i) {
            const QJsonObject r = rows.at(i).toObject();
            const bool en = r.value(QStringLiteral("enabled")).toBool(true);
            if (auto* hItem = model_->item(i, 3)) {
                hItem->setText(formatHealth(r.value(QStringLiteral("health")).toObject(), en, portsGlobalEnabled));
            }
            if (auto* sItem = model_->item(i, 4)) {
                sItem->setText(formatSpeed(r.value(QStringLiteral("speed")).toObject()));
            }
        }
    }
    updatingModel_ = false;
}

void MultiNodeDialog::onSelectAll() {
    updatingModel_ = true;
    for (int i = 0; i < model_->rowCount(); ++i) {
        if (auto* it = model_->item(i, 0)) {
            it->setCheckState(Qt::Checked);
        }
    }
    updatingModel_ = false;
    userEditedPending_ = true;
}

void MultiNodeDialog::onDisableAll() {
    updatingModel_ = true;
    for (int i = 0; i < model_->rowCount(); ++i) {
        if (auto* it = model_->item(i, 0)) {
            it->setCheckState(Qt::Unchecked);
        }
    }
    updatingModel_ = false;
    userEditedPending_ = true;
}

void MultiNodeDialog::onSaveAndApply() {
    if (!client_) {
        return;
    }

    QJsonObject portsMap;
    QJsonObject enabledMap;
    for (int i = 0; i < model_->rowCount(); ++i) {
        const QString name = model_->item(i, 1)->text();
        const bool enabled = (model_->item(i, 0)->checkState() == Qt::Checked);
        bool okPort = false;
        const int port = model_->item(i, 2)->text().trimmed().toInt(&okPort);
        if (!okPort || port < 1024 || port > 65000) {
            QMessageBox::warning(this,
                                 windowTitle(),
                                 L("节点端口必须在 1024–65000 之间: ", "Port must be between 1024 and 65000 for node: ") + name);
            return;
        }
        portsMap.insert(name, port);
        enabledMap.insert(name, enabled);
    }

    const QJsonObject prevNs = client_->snapshot()
                                   .value(QStringLiteral("node_services"))
                                   .toObject()
                                   .value(QStringLiteral("settings"))
                                   .toObject();
    QJsonObject settingsObj;
    settingsObj.insert(QStringLiteral("ports_enabled"), portsEnabledCheck_->isChecked());
    settingsObj.insert(QStringLiteral("recovery_enabled"), recoveryEnabledCheck_->isChecked());
    settingsObj.insert(QStringLiteral("first_port"), firstPortSpin_->value());
    settingsObj.insert(QStringLiteral("check_interval_sec"), prevNs.value(QStringLiteral("check_interval_sec")).toInt(60));
    settingsObj.insert(QStringLiteral("failure_threshold"), prevNs.value(QStringLiteral("failure_threshold")).toInt(3));
    settingsObj.insert(QStringLiteral("refresh_cooldown_sec"), prevNs.value(QStringLiteral("refresh_cooldown_sec")).toInt(300));

    QJsonObject params;
    params.insert(QStringLiteral("settings"), settingsObj);
    params.insert(QStringLiteral("ports"), portsMap);
    params.insert(QStringLiteral("enabled"), enabledMap);

    saveBtn_->setEnabled(false);
    userEditedPending_ = false;
    client_->call(QStringLiteral("update_node_services"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        saveBtn_->setEnabled(true);
        if (!ok) {
            userEditedPending_ = true;
            QMessageBox::critical(this, windowTitle(), err);
        }
    });
}

void MultiNodeDialog::onCheckConnectivity() {
    if (!client_) return;
    checkBtn_->setEnabled(false);
    client_->call(QStringLiteral("node_check_connectivity"), QJsonObject(), [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        checkBtn_->setEnabled(true);
        if (!ok) QMessageBox::warning(this, windowTitle(), err);
    });
}

void MultiNodeDialog::onDirectRefresh() {
    if (!client_) return;
    directRefreshBtn_->setEnabled(false);
    client_->call(QStringLiteral("node_refresh_direct"), QJsonObject(), [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        directRefreshBtn_->setEnabled(true);
        if (!ok) QMessageBox::warning(this, windowTitle(), err);
    });
}

void MultiNodeDialog::onToggleSpeedTest() {
    if (!client_) return;
    const QString method = isSpeedTesting_ ? QStringLiteral("node_speed_test_cancel")
                                           : QStringLiteral("node_speed_test_start");
    client_->call(method, QJsonObject(), [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        if (!ok) QMessageBox::warning(this, windowTitle(), err);
    });
}

int MultiNodeDialog::selectedRowPort(QString* outNodeName) const {
    const QModelIndex idx = tableView_->currentIndex();
    const int row = idx.isValid() ? idx.row() : (model_->rowCount() > 0 ? 0 : -1);
    if (row < 0 || row >= model_->rowCount()) {
        return 0;
    }
    if (outNodeName) {
        *outNodeName = model_->item(row, 1)->text();
    }
    return model_->item(row, 2)->text().toInt();
}

void MultiNodeDialog::onCopyHttp() {
    const int port = selectedRowPort();
    if (port <= 0) return;
    QApplication::clipboard()->setText(QStringLiteral("http://127.0.0.1:%1").arg(port));
}

void MultiNodeDialog::onCopySocks() {
    const int port = selectedRowPort();
    if (port <= 0) return;
    QApplication::clipboard()->setText(QStringLiteral("socks5://127.0.0.1:%1").arg(port));
}

void MultiNodeDialog::onShowChromeGuide() {
    QString nodeName;
    int port = selectedRowPort(&nodeName);
    if (port <= 0) {
        port = firstPortSpin_->value();
        nodeName = QStringLiteral("ExampleNode");
    }
    const QString cmd = QStringLiteral(
        "chrome.exe --proxy-server=\"http=127.0.0.1:%1;https=127.0.0.1:%1\" "
        "--user-data-dir=\"%LOCALAPPDATA%\\Google\\Chrome\\ClashFX-%1\"")
                            .arg(port);
    QMessageBox box(this);
    box.setWindowTitle(L("Chrome 独立端口分流配置指南", "Chrome Fixed-Port Proxy Setup"));
    box.setText(L("方式 1（推荐）：使用 SwitchyOmega / ZeroOmega 插件，新建情景模式指向 127.0.0.1:",
                  "Option 1 (Recommended): Point SwitchyOmega / ZeroOmega profile to 127.0.0.1:") +
                QString::number(port) +
                L("（协议可选 HTTP 或 SOCKS5）。\n\n方式 2：使用独立 Chrome Profile 启动参数绑定节点「",
                  " (HTTP or SOCKS5).\n\nOption 2: Launch an isolated Chrome profile pinned to node \"") +
                nodeName + L("」：\n", "\":\n") + cmd);
    auto* copyCmdBtn = box.addButton(L("复制启动命令", "Copy Launch Command"), QMessageBox::ActionRole);
    box.addButton(QMessageBox::Ok);
    box.exec();
    if (box.clickedButton() == copyCmdBtn) {
        QApplication::clipboard()->setText(cmd);
    }
}

// ============================================================================
// NodeDiagnosticDialog
// ============================================================================

NodeDiagnosticDialog::NodeDiagnosticDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setWindowTitle(L("单节点测速与故障诊断", "Single-Node Probe & Diagnostics"));
    resize(620, 440);

    auto* root = new QVBoxLayout(this);
    auto* form = new QFormLayout();

    nodeCombo_ = new QComboBox(this);
    nodeCombo_->setEditable(true);
    providerEdit_ = new QLineEdit(this);
    providerEdit_->setPlaceholderText(L("可选（仅外部 proxy-provider 节点需填写）", "Optional (proxy-provider name if applicable)"));
    urlEdit_ = new QLineEdit(QStringLiteral("https://cp.cloudflare.com/generate_204"), this);

    form->addRow(L("目标节点:", "Target Node:"), nodeCombo_);
    form->addRow(L("Provider 名称:", "Provider Name:"), providerEdit_);
    form->addRow(L("探测 URL:", "Benchmark URL:"), urlEdit_);
    root->addLayout(form);

    outputEdit_ = new QPlainTextEdit(this);
    outputEdit_->setReadOnly(true);
    outputEdit_->setPlaceholderText(
        L("点击「开始单节点探测」将直接调用内核测试该节点（不改动当前选组状态），并输出 DNS / TLS / SNI / QUIC / 认证 / 超时 分类诊断建议。",
          "Click 'Run Probe' to test the selected node non-intrusively and view structured DNS / TLS / SNI / QUIC / Auth / Timeout diagnostics."));
    root->addWidget(outputEdit_, 1);

    auto* btnRow = new QHBoxLayout();
    runBtn_ = new QPushButton(L("开始单节点探测", "Run Probe"), this);
    runBtn_->setDefault(true);
    auto* copyBtn = new QPushButton(L("复制诊断结果", "Copy Report"), this);
    auto* closeBtn = new QPushButton(L("关闭", "Close"), this);

    btnRow->addWidget(runBtn_);
    btnRow->addWidget(copyBtn);
    btnRow->addStretch(1);
    btnRow->addWidget(closeBtn);
    root->addLayout(btnRow);

    connect(runBtn_, &QPushButton::clicked, this, &NodeDiagnosticDialog::onRunProbe);
    connect(copyBtn, &QPushButton::clicked, this, &NodeDiagnosticDialog::onCopyReport);
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::close);

    if (client_) {
        refreshFromSnapshot(client_->snapshot());
    }
}

void NodeDiagnosticDialog::refreshFromSnapshot(const QJsonObject& snapshot) {
    const QString prevText = nodeCombo_->currentText();
    QSet<QString> seen;
    nodeCombo_->clear();

    const QJsonArray candidates = snapshot.value(QStringLiteral("ai_lock_candidates")).toArray();
    for (const QJsonValue& v : candidates) {
        const QJsonObject o = v.toObject();
        const QString name = o.value(QStringLiteral("name")).toString();
        if (!name.isEmpty() && !seen.contains(name)) {
            seen.insert(name);
            nodeCombo_->addItem(name);
        }
    }
    const QJsonArray groups = snapshot.value(QStringLiteral("proxy_groups")).toArray();
    for (const QJsonValue& gv : groups) {
        const QJsonArray items = gv.toObject().value(QStringLiteral("items")).toArray();
        for (const QJsonValue& iv : items) {
            const QString name = iv.toObject().value(QStringLiteral("name")).toString();
            if (!name.isEmpty() && !seen.contains(name) &&
                name != QStringLiteral("DIRECT") && name != QStringLiteral("REJECT")) {
                seen.insert(name);
                nodeCombo_->addItem(name);
            }
        }
    }
    if (!prevText.isEmpty()) {
        nodeCombo_->setCurrentText(prevText);
    }

    const QString benchUrl = snapshot.value(QStringLiteral("settings"))
                                 .toObject()
                                 .value(QStringLiteral("benchmark_url"))
                                 .toString();
    if (!benchUrl.isEmpty() && urlEdit_->text().trimmed().isEmpty()) {
        urlEdit_->setText(benchUrl);
    }
}

void NodeDiagnosticDialog::preselectNode(const QString& nodeName) {
    if (!nodeName.trimmed().isEmpty()) {
        nodeCombo_->setCurrentText(nodeName.trimmed());
    }
}

void NodeDiagnosticDialog::onRunProbe() {
    if (!client_) return;
    const QString nodeName = nodeCombo_->currentText().trimmed();
    if (nodeName.isEmpty()) {
        return;
    }
    runBtn_->setEnabled(false);
    outputEdit_->setPlainText(L("正在探测节点，请稍候...", "Probing node, please wait..."));

    QJsonObject params;
    params.insert(QStringLiteral("node_name"), nodeName);
    params.insert(QStringLiteral("provider_name"), providerEdit_->text().trimmed());
    params.insert(QStringLiteral("benchmark_url"), urlEdit_->text().trimmed());

    client_->call(QStringLiteral("diagnose_node"), params, [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
        runBtn_->setEnabled(true);
        if (!ok) {
            outputEdit_->setPlainText(L("探测请求失败：\n", "Probe request failed:\n") + err);
            return;
        }
        const QJsonObject rep = data.toObject();
        outputEdit_->setPlainText(rep.value(QStringLiteral("formatted")).toString());
    });
}

void NodeDiagnosticDialog::onCopyReport() {
    QApplication::clipboard()->setText(outputEdit_->toPlainText());
}

} // namespace clashfx
