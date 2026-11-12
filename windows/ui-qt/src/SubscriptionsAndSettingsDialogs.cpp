#include "SubscriptionsAndSettingsDialogs.h"
#include "DaemonClient.h"
#include "I18n.h"
#include "TrayIconGenerator.h"

#include <QDateTime>
#include <QFileDialog>
#include <QFormLayout>
#include <QGroupBox>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QJsonArray>
#include <QMessageBox>
#include <QTabWidget>
#include <QVBoxLayout>

namespace clashfx {

// ============================================================================
// SubscriptionsDialog
// ============================================================================

SubscriptionsDialog::SubscriptionsDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setWindowTitle(L("托管配置订阅管理", "Managed Subscriptions"));
    resize(820, 520);

    auto* root = new QVBoxLayout(this);
    root->setSpacing(10);

    auto* autoBar = new QHBoxLayout();
    autoUpdateCheck_ = new QCheckBox(L("自动定时更新托管订阅", "Automatically update subscriptions"), this);
    auto* everyLabel = new QLabel(L("更新间隔 (分钟):", "Interval (minutes):"), this);
    intervalMinutesSpin_ = new QSpinBox(this);
    intervalMinutesSpin_->setRange(5, 10080);
    intervalMinutesSpin_->setValue(120);
    auto* saveIntervalBtn = new QPushButton(L("保存自动更新设置", "Save Interval"), this);

    autoBar->addWidget(autoUpdateCheck_);
    autoBar->addSpacing(12);
    autoBar->addWidget(everyLabel);
    autoBar->addWidget(intervalMinutesSpin_);
    autoBar->addWidget(saveIntervalBtn);
    autoBar->addStretch(1);
    root->addLayout(autoBar);

    table_ = new QTableWidget(0, 5, this);
    table_->setHorizontalHeaderLabels({
        L("配置名", "Profile"),
        L("订阅地址", "Subscription URL"),
        L("已用 / 总流量", "Used / Total"),
        L("到期时间", "Expires"),
        L("最近更新", "Last Updated"),
    });
    table_->setSelectionBehavior(QAbstractItemView::SelectRows);
    table_->setSelectionMode(QAbstractItemView::SingleSelection);
    table_->setEditTriggers(QAbstractItemView::NoEditTriggers);
    table_->verticalHeader()->setVisible(false);
    table_->horizontalHeader()->setSectionResizeMode(0, QHeaderView::ResizeToContents);
    table_->horizontalHeader()->setSectionResizeMode(1, QHeaderView::Stretch);
    table_->horizontalHeader()->setSectionResizeMode(2, QHeaderView::ResizeToContents);
    table_->horizontalHeader()->setSectionResizeMode(3, QHeaderView::ResizeToContents);
    table_->horizontalHeader()->setSectionResizeMode(4, QHeaderView::ResizeToContents);
    root->addWidget(table_, 1);

    auto* editBox = new QGroupBox(L("添加或更新订阅", "Add or Update Subscription"), this);
    auto* editForm = new QFormLayout(editBox);
    nameEdit_ = new QLineEdit(editBox);
    nameEdit_->setPlaceholderText(L("例如 my-airport（留空则自动根据 URL 命名）", "e.g. my-provider (optional)"));
    urlEdit_ = new QLineEdit(editBox);
    urlEdit_->setPlaceholderText(QStringLiteral("https://example.com/api/v1/client/subscribe?token=..."));
    activateCheck_ = new QCheckBox(L("拉取成功后立即切换至该配置", "Switch to this profile immediately after pull"), editBox);
    activateCheck_->setChecked(true);

    editForm->addRow(L("配置名称:", "Profile Name:"), nameEdit_);
    editForm->addRow(L("订阅 URL:", "Subscription URL:"), urlEdit_);
    editForm->addRow(QString(), activateCheck_);
    root->addWidget(editBox);

    auto* btnRow = new QHBoxLayout();
    saveSubBtn_ = new QPushButton(L("拉取并保存订阅", "Save & Pull Subscription"), this);
    saveSubBtn_->setDefault(true);
    auto* refreshSelBtn = new QPushButton(L("立即更新选中", "Refresh Selected"), this);
    auto* refreshAllBtn = new QPushButton(L("更新全部", "Refresh All"), this);
    auto* deleteBtn = new QPushButton(L("删除选中订阅", "Delete Selected"), this);
    auto* closeBtn = new QPushButton(L("关闭", "Close"), this);

    btnRow->addWidget(saveSubBtn_);
    btnRow->addWidget(refreshSelBtn);
    btnRow->addWidget(refreshAllBtn);
    btnRow->addWidget(deleteBtn);
    btnRow->addStretch(1);
    btnRow->addWidget(closeBtn);
    root->addLayout(btnRow);

    connect(saveIntervalBtn, &QPushButton::clicked, this, &SubscriptionsDialog::onSaveAutoUpdate);
    connect(saveSubBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onAddOrUpdate);
    connect(refreshSelBtn, &QPushButton::clicked, this, &SubscriptionsDialog::onRefreshSelected);
    connect(refreshAllBtn, &QPushButton::clicked, this, &SubscriptionsDialog::onRefreshAll);
    connect(deleteBtn, &QPushButton::clicked, this, &SubscriptionsDialog::onDeleteSelected);
    connect(table_, &QTableWidget::itemSelectionChanged, this, &SubscriptionsDialog::onSelectionChanged);
    connect(closeBtn, &QPushButton::clicked, this, &QDialog::close);

    if (client_) {
        connect(client_, &DaemonClient::stateChanged, this, &SubscriptionsDialog::refreshFromSnapshot);
        refreshFromSnapshot(client_->snapshot());
    }
}

QString SubscriptionsDialog::formatBytes(qint64 bytes) {
    if (bytes < 0) return QStringLiteral("0 B");
    const double gb = static_cast<double>(bytes) / (1024.0 * 1024.0 * 1024.0);
    if (gb >= 1.0) return QString::number(gb, 'f', 2) + QStringLiteral(" GB");
    const double mb = static_cast<double>(bytes) / (1024.0 * 1024.0);
    return QString::number(mb, 'f', 1) + QStringLiteral(" MB");
}

void SubscriptionsDialog::refreshFromSnapshot(const QJsonObject& snapshot) {
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    autoUpdateCheck_->setChecked(settings.value(QStringLiteral("auto_update_subs")).toBool(true));
    const int sec = settings.value(QStringLiteral("auto_update_interval_sec")).toInt(7200);
    intervalMinutesSpin_->setValue(qMax(5, sec / 60));

    const QJsonArray subs = snapshot.value(QStringLiteral("subscriptions")).toArray();
    table_->setRowCount(subs.size());
    for (int i = 0; i < subs.size(); ++i) {
        const QJsonObject s = subs.at(i).toObject();
        const QString name = s.value(QStringLiteral("name")).toString();
        const QString url = s.value(QStringLiteral("url")).toString();

        QString trafficStr = QStringLiteral("—");
        QString expireStr = QStringLiteral("—");
        if (s.contains(QStringLiteral("userinfo")) && s.value(QStringLiteral("userinfo")).isObject()) {
            const QJsonObject ui = s.value(QStringLiteral("userinfo")).toObject();
            const qint64 up = static_cast<qint64>(ui.value(QStringLiteral("upload")).toDouble(0));
            const qint64 down = static_cast<qint64>(ui.value(QStringLiteral("download")).toDouble(0));
            const qint64 total = static_cast<qint64>(ui.value(QStringLiteral("total")).toDouble(0));
            if (total > 0 || up > 0 || down > 0) {
                trafficStr = formatBytes(up + down) + QStringLiteral(" / ") +
                             (total > 0 ? formatBytes(total) : QStringLiteral("∞"));
            }
            const qint64 expTs = static_cast<qint64>(ui.value(QStringLiteral("expire")).toDouble(0));
            if (expTs > 0) {
                expireStr = QDateTime::fromSecsSinceEpoch(expTs).toString(QStringLiteral("yyyy-MM-dd"));
            }
        }

        QString updatedStr = QStringLiteral("—");
        const qint64 updTs = static_cast<qint64>(s.value(QStringLiteral("updated_at")).toDouble(0));
        if (updTs > 0) {
            updatedStr = QDateTime::fromSecsSinceEpoch(updTs).toString(QStringLiteral("MM-dd HH:mm"));
        }

        table_->setItem(i, 0, new QTableWidgetItem(name));
        table_->setItem(i, 1, new QTableWidgetItem(url));
        table_->setItem(i, 2, new QTableWidgetItem(trafficStr));
        table_->setItem(i, 3, new QTableWidgetItem(expireStr));
        table_->setItem(i, 4, new QTableWidgetItem(updatedStr));
    }
}

void SubscriptionsDialog::onSelectionChanged() {
    const int row = table_->currentRow();
    if (row < 0 || row >= table_->rowCount()) return;
    nameEdit_->setText(table_->item(row, 0)->text());
    urlEdit_->setText(table_->item(row, 1)->text());
}

void SubscriptionsDialog::onAddOrUpdate() {
    if (!client_) return;
    const QString url = urlEdit_->text().trimmed();
    if (url.isEmpty()) {
        QMessageBox::warning(this, windowTitle(), L("请输入订阅 URL。", "Please enter a subscription URL."));
        return;
    }
    saveSubBtn_->setEnabled(false);
    QJsonObject params;
    params.insert(QStringLiteral("name"), nameEdit_->text().trimmed());
    params.insert(QStringLiteral("url"), url);
    params.insert(QStringLiteral("select_after"), activateCheck_->isChecked());
    client_->call(QStringLiteral("upsert_subscription"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        saveSubBtn_->setEnabled(true);
        if (!ok) {
            QMessageBox::critical(this, windowTitle(), err);
            return;
        }
        client_->call(QStringLiteral("get_state"));
    });
}

void SubscriptionsDialog::onRefreshSelected() {
    if (!client_) return;
    const int row = table_->currentRow();
    if (row < 0 || row >= table_->rowCount()) return;
    QJsonObject params;
    params.insert(QStringLiteral("name"), table_->item(row, 0)->text());
    client_->call(QStringLiteral("refresh_subscription"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        if (!ok) QMessageBox::warning(this, windowTitle(), err);
        client_->call(QStringLiteral("get_state"));
    });
}

void SubscriptionsDialog::onRefreshAll() {
    if (!client_) return;
    for (int i = 0; i < table_->rowCount(); ++i) {
        QJsonObject params;
        params.insert(QStringLiteral("name"), table_->item(i, 0)->text());
        client_->call(QStringLiteral("refresh_subscription"), params, [this](bool, const QJsonValue&, const QString&, const QString&) {
            client_->call(QStringLiteral("get_state"));
        });
    }
}

void SubscriptionsDialog::onDeleteSelected() {
    if (!client_) return;
    const int row = table_->currentRow();
    if (row < 0 || row >= table_->rowCount()) return;
    const QString name = table_->item(row, 0)->text();
    const auto ans = QMessageBox::question(
        this,
        windowTitle(),
        L("确定移除托管订阅「", "Remove managed subscription \"") + name +
            L("」吗？\n点击「Yes」将同时删除本地对应的 .yaml 配置文件；点击「No」仅取消订阅关联但保留配置文件。",
              "\"?\nClick 'Yes' to also delete its local .yaml file, or 'No' to keep the file."),
        QMessageBox::Yes | QMessageBox::No | QMessageBox::Cancel);
    if (ans == QMessageBox::Cancel) return;

    QJsonObject params;
    params.insert(QStringLiteral("name"), name);
    params.insert(QStringLiteral("delete_file"), ans == QMessageBox::Yes);
    client_->call(QStringLiteral("remove_subscription"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        if (!ok) QMessageBox::warning(this, windowTitle(), err);
    });
}

void SubscriptionsDialog::onSaveAutoUpdate() {
    if (!client_) return;
    QJsonObject settings = client_->snapshot().value(QStringLiteral("settings")).toObject();
    settings.insert(QStringLiteral("auto_update_subs"), autoUpdateCheck_->isChecked());
    settings.insert(QStringLiteral("auto_update_interval_sec"), intervalMinutesSpin_->value() * 60);
    QJsonObject params;
    params.insert(QStringLiteral("settings"), settings);
    client_->call(QStringLiteral("update_settings"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        if (!ok) QMessageBox::warning(this, windowTitle(), err);
    });
}

// ============================================================================
// SettingsDialog
// ============================================================================

SettingsDialog::SettingsDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setWindowTitle(L("ClashFX 偏好设置", "ClashFX Settings"));
    resize(660, 540);

    auto* root = new QVBoxLayout(this);
    auto* tabs = new QTabWidget(this);

    // ---- Tab 1: General ----
    auto* genPage = new QWidget(tabs);
    auto* genForm = new QFormLayout(genPage);

    portOverrideSpin_ = new QSpinBox(genPage);
    portOverrideSpin_->setRange(0, 65535);
    portOverrideSpin_->setSpecialValueText(L("0 (使用配置文件端口)", "0 (Use config port)"));

    apiPortSpin_ = new QSpinBox(genPage);
    apiPortSpin_->setRange(1024, 65535);

    apiSecretEdit_ = new QLineEdit(genPage);
    apiSecretEdit_->setPlaceholderText(L("可选外部控制 API 密钥", "Optional REST API secret"));

    logLevelCombo_ = new QComboBox(genPage);
    for (const QString& lvl : {QStringLiteral("silent"), QStringLiteral("error"), QStringLiteral("warning"), QStringLiteral("info"), QStringLiteral("debug")}) {
        logLevelCombo_->addItem(lvl, lvl);
    }

    auto* dirRow = new QHBoxLayout();
    customDirEdit_ = new QLineEdit(genPage);
    customDirEdit_->setPlaceholderText(L("默认 %APPDATA%\\ClashFX", "Default %APPDATA%\\ClashFX"));
    auto* browseBtn = new QPushButton(L("浏览...", "Browse..."), genPage);
    auto* resetDirBtn = new QPushButton(L("默认", "Default"), genPage);
    dirRow->addWidget(customDirEdit_, 1);
    dirRow->addWidget(browseBtn);
    dirRow->addWidget(resetDirBtn);

    bypassEdit_ = new QPlainTextEdit(genPage);
    bypassEdit_->setMaximumHeight(78);
    auto* resetBypassBtn = new QPushButton(L("恢复 Windows 默认绕过列表", "Reset Default Windows Bypass List"), genPage);

    benchmarkUrlEdit_ = new QLineEdit(genPage);
    updateChannelCombo_ = new QComboBox(genPage);
    updateChannelCombo_->addItem(L("稳定版 (Stable)", "Stable"), QStringLiteral("stable"));
    updateChannelCombo_->addItem(L("实验版 (Lab / Prerelease)", "Lab (Prerelease)"), QStringLiteral("lab"));

    genForm->addRow(L("混合端口覆写:", "Mixed Port Override:"), portOverrideSpin_);
    genForm->addRow(L("控制面板 API 端口:", "Controller API Port:"), apiPortSpin_);
    genForm->addRow(L("API 访问密钥:", "Controller Secret:"), apiSecretEdit_);
    genForm->addRow(L("内核日志等级:", "Core Log Level:"), logLevelCombo_);
    genForm->addRow(L("自定义配置文件夹:", "Custom Config Folder:"), dirRow);
    genForm->addRow(L("系统代理绕过列表:", "System Proxy Bypass:"), bypassEdit_);
    genForm->addRow(QString(), resetBypassBtn);
    genForm->addRow(L("延迟测速 URL:", "Benchmark URL:"), benchmarkUrlEdit_);
    genForm->addRow(L("更新检查通道:", "Update Channel:"), updateChannelCombo_);
    tabs->addTab(genPage, L("通用设置", "General"));

    // ---- Tab 2: Enhanced TUN ----
    auto* tunPage = new QWidget(tabs);
    auto* tunForm = new QFormLayout(tunPage);

    serviceStatusLabel_ = new QLabel(tunPage);
    tunStackCombo_ = new QComboBox(tunPage);
    for (const QString& st : {QStringLiteral("mixed"), QStringLiteral("gvisor"), QStringLiteral("system")}) {
        tunStackCombo_->addItem(st, st);
    }

    tunMtuSpin_ = new QSpinBox(tunPage);
    tunMtuSpin_->setRange(1280, 9000);
    tunMtuSpin_->setValue(1500);

    tunInterfaceEdit_ = new QLineEdit(tunPage);
    tunInterfaceEdit_->setPlaceholderText(L("可选物理出口网卡名（留空自动检测）", "Optional outbound interface name (auto-detect if empty)"));

    tunExcludesEdit_ = new QPlainTextEdit(tunPage);
    tunExcludesEdit_->setMaximumHeight(82);
    tunExcludesEdit_->setPlaceholderText(QStringLiteral("192.168.50.0/24\n10.8.0.0/16"));

    bypassCnAppsCheck_ = new QCheckBox(
        L("绕过常用国内客户端进程 (微信 / QQ / 钉钉 / 飞书 / WPS / 百度网盘 / 迅雷等)",
          "Bypass common Chinese desktop apps (WeChat / QQ / DingTalk / Feishu / WPS / BaiduNetdisk)"),
        tunPage);
    customConfigAsIsCheck_ = new QCheckBox(
        L("增强模式按原样使用自定义配置 (不注入托管 TUN 覆写)",
          "Use custom config as-is in Enhanced Mode (skip managed TUN injection)"),
        tunPage);

    tunForm->addRow(L("提权服务状态:", "Privileged Service:"), serviceStatusLabel_);
    tunForm->addRow(L("TUN 协议栈:", "TUN Stack:"), tunStackCombo_);
    tunForm->addRow(L("TUN MTU:", "TUN MTU:"), tunMtuSpin_);
    tunForm->addRow(L("绑定出口网卡:", "Outbound Interface:"), tunInterfaceEdit_);
    tunForm->addRow(L("路由排除网段 (CIDR):", "Route Exclude CIDRs:"), tunExcludesEdit_);
    tunForm->addRow(QString(), bypassCnAppsCheck_);
    tunForm->addRow(QString(), customConfigAsIsCheck_);
    tabs->addTab(tunPage, L("增强模式与分流", "Enhanced TUN"));

    // ---- Tab 3: Hotkeys & Network Auto-Suspend ----
    auto* autoPage = new QWidget(tabs);
    auto* autoForm = new QFormLayout(autoPage);

    hotkeySysProxyEdit_ = new QLineEdit(autoPage);
    hotkeySysProxyEdit_->setPlaceholderText(QStringLiteral("Ctrl+Alt+P"));
    hotkeyModeEdit_ = new QLineEdit(autoPage);
    hotkeyModeEdit_->setPlaceholderText(QStringLiteral("Ctrl+Alt+M"));

    currentNetLabel_ = new QLabel(autoPage);
    currentNetLabel_->setWordWrap(true);
    currentNetLabel_->setStyleSheet(QStringLiteral("color: #52525b; font-size: 12px;"));
    auto* addCurNetBtn = new QPushButton(L("将当前活动网络加入自动暂停列表", "Add Current Network to Auto-Suspend"), autoPage);

    suspendNetsEdit_ = new QPlainTextEdit(autoPage);
    suspendNetsEdit_->setPlaceholderText(
        L("每行填写一个网络名称 (Network Profile)、网关 IP 或 DNS 后缀；匹配时自动暂停系统代理。",
          "One Windows Network Profile name, Gateway IP, or DNS suffix per line."));

    autoForm->addRow(L("开关系统代理快捷键:", "Toggle System Proxy Hotkey:"), hotkeySysProxyEdit_);
    autoForm->addRow(L("切换出站模式快捷键:", "Switch Outbound Mode Hotkey:"), hotkeyModeEdit_);
    autoForm->addRow(L("当前检测到的网络特征:", "Detected Active Network:"), currentNetLabel_);
    autoForm->addRow(QString(), addCurNetBtn);
    autoForm->addRow(L("自动暂停系统代理列表:", "Auto-Suspend Networks:"), suspendNetsEdit_);
    tabs->addTab(autoPage, L("快捷键与网络自暂停", "Hotkeys & Network"));

    root->addWidget(tabs, 1);

    auto* btnRow = new QHBoxLayout();
    btnRow->addStretch(1);
    saveBtn_ = new QPushButton(L("保存并应用", "Save & Apply"), this);
    saveBtn_->setDefault(true);
    auto* cancelBtn = new QPushButton(L("取消", "Cancel"), this);
    btnRow->addWidget(saveBtn_);
    btnRow->addWidget(cancelBtn);
    root->addLayout(btnRow);

    connect(browseBtn, &QPushButton::clicked, this, &SettingsDialog::onBrowseCustomDir);
    connect(resetDirBtn, &QPushButton::clicked, this, &SettingsDialog::onResetCustomDir);
    connect(resetBypassBtn, &QPushButton::clicked, this, &SettingsDialog::onResetBypass);
    connect(addCurNetBtn, &QPushButton::clicked, this, &SettingsDialog::onAddCurrentNetwork);
    connect(saveBtn_, &QPushButton::clicked, this, &SettingsDialog::onSaveClicked);
    connect(cancelBtn, &QPushButton::clicked, this, &QDialog::reject);

    if (client_) {
        loadFromSnapshot(client_->snapshot());
    }
}

void SettingsDialog::loadFromSnapshot(const QJsonObject& snapshot) {
    baseSettings_ = snapshot.value(QStringLiteral("settings")).toObject();

    portOverrideSpin_->setValue(baseSettings_.value(QStringLiteral("port_override")).toInt(0));
    apiPortSpin_->setValue(baseSettings_.value(QStringLiteral("api_port")).toInt(19090));
    apiSecretEdit_->setText(baseSettings_.value(QStringLiteral("api_secret")).toString());

    const QString lvl = baseSettings_.value(QStringLiteral("log_level")).toString(QStringLiteral("info"));
    const int lvlIdx = logLevelCombo_->findData(lvl);
    if (lvlIdx >= 0) logLevelCombo_->setCurrentIndex(lvlIdx);

    customDirEdit_->setText(baseSettings_.value(QStringLiteral("custom_config_dir")).toString());
    bypassEdit_->setPlainText(baseSettings_.value(QStringLiteral("proxy_bypass_list")).toString());
    benchmarkUrlEdit_->setText(baseSettings_.value(QStringLiteral("benchmark_url")).toString());

    const QString ch = baseSettings_.value(QStringLiteral("update_channel")).toString(QStringLiteral("stable"));
    const int chIdx = updateChannelCombo_->findData(ch);
    if (chIdx >= 0) updateChannelCombo_->setCurrentIndex(chIdx);

    const bool svcInstalled = snapshot.value(QStringLiteral("service_installed")).toBool(false);
    serviceStatusLabel_->setText(svcInstalled
                                     ? L("ClashFXService 已注册（日常切换 TUN 零 UAC 弹窗）",
                                         "ClashFXService installed (zero UAC prompts)")
                                     : L("尚未安装（首次开启增强模式时单次授权安装）",
                                         "Not installed yet (one-time UAC setup on first TUN enable)"));

    const QString stack = baseSettings_.value(QStringLiteral("tun_stack")).toString(QStringLiteral("mixed"));
    const int stIdx = tunStackCombo_->findData(stack);
    if (stIdx >= 0) tunStackCombo_->setCurrentIndex(stIdx);

    tunMtuSpin_->setValue(baseSettings_.value(QStringLiteral("tun_mtu")).toInt(1500));
    tunInterfaceEdit_->setText(baseSettings_.value(QStringLiteral("tun_interface_name")).toString());
    tunExcludesEdit_->setPlainText(baseSettings_.value(QStringLiteral("tun_route_excludes")).toString());
    bypassCnAppsCheck_->setChecked(baseSettings_.value(QStringLiteral("bypass_cn_apps")).toBool(false));
    customConfigAsIsCheck_->setChecked(baseSettings_.value(QStringLiteral("custom_config_as_is")).toBool(false));

    const QJsonObject hk = baseSettings_.value(QStringLiteral("hotkeys")).toObject();
    hotkeySysProxyEdit_->setText(hk.value(QStringLiteral("toggle_system_proxy")).toString());
    hotkeyModeEdit_->setText(hk.value(QStringLiteral("switch_outbound_mode")).toString());

    QStringList suspLines;
    for (const QJsonValue& v : baseSettings_.value(QStringLiteral("suspend_networks")).toArray()) {
        if (!v.toString().trimmed().isEmpty()) {
            suspLines << v.toString().trimmed();
        }
    }
    suspendNetsEdit_->setPlainText(suspLines.join(QLatin1Char('\n')));

    detectedNetTokens_.clear();
    const QJsonObject netObj = snapshot.value(QStringLiteral("active_network")).toObject();
    auto collectTokens = [&](const QString& key) {
        for (const QJsonValue& v : netObj.value(key).toArray()) {
            const QString s = v.toString().trimmed();
            if (!s.isEmpty() && !detectedNetTokens_.contains(s)) {
                detectedNetTokens_ << s;
            }
        }
    };
    collectTokens(QStringLiteral("profile_names"));
    collectTokens(QStringLiteral("gateways"));
    collectTokens(QStringLiteral("dns_suffixes"));
    collectTokens(QStringLiteral("interfaces"));

    currentNetLabel_->setText(detectedNetTokens_.isEmpty()
                                  ? L("未检测到活动外网特征", "No active network tokens detected")
                                  : detectedNetTokens_.join(QStringLiteral("  |  ")));
}

void SettingsDialog::onBrowseCustomDir() {
    const QString dir = QFileDialog::getExistingDirectory(
        this, L("选择自定义配置目录", "Select Custom Config Directory"), customDirEdit_->text());
    if (!dir.isEmpty()) {
        customDirEdit_->setText(dir);
    }
}

void SettingsDialog::onResetCustomDir() {
    customDirEdit_->clear();
}

void SettingsDialog::onResetBypass() {
    bypassEdit_->setPlainText(QStringLiteral(
        "<local>;localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*"));
}

void SettingsDialog::onAddCurrentNetwork() {
    if (detectedNetTokens_.isEmpty()) return;
    const QString candidate = detectedNetTokens_.first();
    QStringList existing = suspendNetsEdit_->toPlainText().split(QLatin1Char('\n'), Qt::SkipEmptyParts);
    if (!existing.contains(candidate, Qt::CaseInsensitive)) {
        existing << candidate;
        suspendNetsEdit_->setPlainText(existing.join(QLatin1Char('\n')));
    }
}

void SettingsDialog::onSaveClicked() {
    if (!client_) return;

    QJsonObject updated = baseSettings_;
    updated.insert(QStringLiteral("port_override"), portOverrideSpin_->value());
    updated.insert(QStringLiteral("api_port"), apiPortSpin_->value());
    updated.insert(QStringLiteral("api_secret"), apiSecretEdit_->text().trimmed());
    updated.insert(QStringLiteral("log_level"), logLevelCombo_->currentData().toString());
    updated.insert(QStringLiteral("custom_config_dir"), customDirEdit_->text().trimmed());
    updated.insert(QStringLiteral("proxy_bypass_list"), bypassEdit_->toPlainText().trimmed());
    updated.insert(QStringLiteral("benchmark_url"), benchmarkUrlEdit_->text().trimmed());
    updated.insert(QStringLiteral("update_channel"), updateChannelCombo_->currentData().toString());

    updated.insert(QStringLiteral("tun_stack"), tunStackCombo_->currentData().toString());
    updated.insert(QStringLiteral("tun_mtu"), tunMtuSpin_->value());
    updated.insert(QStringLiteral("tun_interface_name"), tunInterfaceEdit_->text().trimmed());
    updated.insert(QStringLiteral("tun_route_excludes"), tunExcludesEdit_->toPlainText().trimmed());
    updated.insert(QStringLiteral("bypass_cn_apps"), bypassCnAppsCheck_->isChecked());
    updated.insert(QStringLiteral("custom_config_as_is"), customConfigAsIsCheck_->isChecked());

    QJsonObject hk;
    hk.insert(QStringLiteral("toggle_system_proxy"), hotkeySysProxyEdit_->text().trimmed());
    hk.insert(QStringLiteral("switch_outbound_mode"), hotkeyModeEdit_->text().trimmed());
    updated.insert(QStringLiteral("hotkeys"), hk);

    QJsonArray suspArr;
    for (const QString& line : suspendNetsEdit_->toPlainText().split(QLatin1Char('\n'), Qt::SkipEmptyParts)) {
        const QString t = line.trimmed();
        if (!t.isEmpty()) suspArr.append(t);
    }
    updated.insert(QStringLiteral("suspend_networks"), suspArr);

    saveBtn_->setEnabled(false);
    QJsonObject params;
    params.insert(QStringLiteral("settings"), updated);
    client_->call(QStringLiteral("update_settings"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        saveBtn_->setEnabled(true);
        if (!ok) {
            QMessageBox::critical(this, windowTitle(), err);
            return;
        }
        accept();
    });
}

} // namespace clashfx
