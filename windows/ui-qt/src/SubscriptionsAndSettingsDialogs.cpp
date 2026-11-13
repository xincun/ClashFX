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
#include <QInputDialog>
#include <QJsonArray>
#include <QKeySequence>
#include <QMessageBox>
#include <QPointer>
#include <QTabWidget>
#include <QUrl>
#include <QVBoxLayout>
#include <memory>

namespace clashfx {

// ============================================================================
// SubscriptionsDialog
// ============================================================================

SubscriptionsDialog::SubscriptionsDialog(DaemonClient* client, QWidget* parent)
    : QDialog(parent)
    , client_(client) {
    setWindowIcon(TrayIconGenerator::appIcon());
    resize(840, 540);

    auto* root = new QVBoxLayout(this);
    root->setSpacing(10);

    auto* autoBar = new QHBoxLayout();
    autoUpdateCheck_ = new QCheckBox(this);
    everyLabel_ = new QLabel(this);
    intervalMinutesSpin_ = new QSpinBox(this);
    intervalMinutesSpin_->setRange(5, 10080);
    intervalMinutesSpin_->setValue(120);
    saveIntervalBtn_ = new QPushButton(this);
    statusLabel_ = new QLabel(this);
    statusLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 12px; font-weight: 600;"));

    autoBar->addWidget(autoUpdateCheck_);
    autoBar->addSpacing(12);
    autoBar->addWidget(everyLabel_);
    autoBar->addWidget(intervalMinutesSpin_);
    autoBar->addWidget(saveIntervalBtn_);
    autoBar->addSpacing(12);
    autoBar->addWidget(statusLabel_, 1);
    root->addLayout(autoBar);

    table_ = new QTableWidget(0, 5, this);
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

    editBox_ = new QGroupBox(this);
    auto* editForm = new QFormLayout(editBox_);
    nameEdit_ = new QLineEdit(editBox_);

    auto* urlRow = new QHBoxLayout();
    urlEdit_ = new QLineEdit(editBox_);
    urlEdit_->setEchoMode(QLineEdit::Password);
    urlEdit_->setPlaceholderText(QStringLiteral("https://example.com/api/v1/client/subscribe?token=..."));
    toggleUrlVisBtn_ = new QPushButton(editBox_);
    toggleUrlVisBtn_->setAutoDefault(false);
    urlRow->addWidget(urlEdit_, 1);
    urlRow->addWidget(toggleUrlVisBtn_);

    userAgentEdit_ = new QLineEdit(editBox_);
    activateCheck_ = new QCheckBox(editBox_);
    activateCheck_->setChecked(true);

    nameFormLabel_ = new QLabel(editBox_);
    urlFormLabel_ = new QLabel(editBox_);
    uaFormLabel_ = new QLabel(editBox_);

    editForm->addRow(nameFormLabel_, nameEdit_);
    editForm->addRow(urlFormLabel_, urlRow);
    editForm->addRow(uaFormLabel_, userAgentEdit_);
    editForm->addRow(QString(), activateCheck_);
    root->addWidget(editBox_);

    auto* btnRow = new QHBoxLayout();
    saveSubBtn_ = new QPushButton(this);
    saveSubBtn_->setDefault(true);
    refreshSelBtn_ = new QPushButton(this);
    refreshAllBtn_ = new QPushButton(this);
    deleteBtn_ = new QPushButton(this);
    closeBtn_ = new QPushButton(this);

    btnRow->addWidget(saveSubBtn_);
    btnRow->addWidget(refreshSelBtn_);
    btnRow->addWidget(refreshAllBtn_);
    btnRow->addWidget(deleteBtn_);
    btnRow->addStretch(1);
    btnRow->addWidget(closeBtn_);
    root->addLayout(btnRow);

    connect(saveIntervalBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onSaveAutoUpdate);
    connect(autoUpdateCheck_, &QCheckBox::toggled, this, [this](bool) {
        if (!suppressAutoUpdateSignal_) {
            onSaveAutoUpdate();
        }
    });
    connect(intervalMinutesSpin_, &QSpinBox::editingFinished, this, [this]() {
        if (!suppressAutoUpdateSignal_) {
            onSaveAutoUpdate();
        }
    });
    connect(toggleUrlVisBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onToggleUrlVisibility);
    connect(saveSubBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onAddOrUpdate);
    connect(refreshSelBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onRefreshSelected);
    connect(refreshAllBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onRefreshAll);
    connect(deleteBtn_, &QPushButton::clicked, this, &SubscriptionsDialog::onDeleteSelected);
    connect(table_, &QTableWidget::itemSelectionChanged, this, &SubscriptionsDialog::onSelectionChanged);
    connect(nameEdit_, &QLineEdit::textEdited, this, [this]() { formDirty_ = true; });
    connect(userAgentEdit_, &QLineEdit::textEdited, this, [this]() { formDirty_ = true; });
    connect(urlEdit_, &QLineEdit::textEdited, this, [this]() { formDirty_ = true; });
    connect(urlEdit_, &QLineEdit::textChanged, this, [this](const QString& text) {
        const QString host = QUrl(text.trimmed()).host();
        if (!host.isEmpty()) {
            nameEdit_->setPlaceholderText(host);
        } else {
            nameEdit_->setPlaceholderText(L("例如 my-airport（留空则自动根据 URL 命名）", "e.g. my-provider (optional)"));
        }
    });
    connect(closeBtn_, &QPushButton::clicked, this, &QDialog::close);
    connect(&I18n::instance(), &I18n::languageChanged, this, [this]() { retranslateUi(); });

    retranslateUi();

    if (client_) {
        connect(client_, &DaemonClient::stateChanged, this, &SubscriptionsDialog::refreshFromSnapshot);
        refreshFromSnapshot(client_->snapshot());
    }
}

void SubscriptionsDialog::retranslateUi() {
    setWindowTitle(L("托管配置订阅管理", "Managed Subscriptions"));
    autoUpdateCheck_->setText(L("自动定时更新托管订阅", "Automatically update subscriptions"));
    everyLabel_->setText(L("更新间隔 (分钟):", "Interval (minutes):"));
    saveIntervalBtn_->setText(L("保存自动更新设置", "Save Interval"));
    table_->setHorizontalHeaderLabels({
        L("配置名", "Profile"),
        L("订阅地址 (已脱敏)", "Subscription URL (Masked)"),
        L("已用 / 总流量", "Used / Total"),
        L("到期时间", "Expires"),
        L("最近更新", "Last Updated"),
    });
    editBox_->setTitle(L("添加或更新订阅", "Add or Update Subscription"));
    nameFormLabel_->setText(L("配置名称:", "Profile Name:"));
    urlFormLabel_->setText(L("订阅 URL:", "Subscription URL:"));
    uaFormLabel_->setText(L("自定义 User-Agent:", "Custom User-Agent:"));
    if (urlEdit_->text().trimmed().isEmpty()) {
        nameEdit_->setPlaceholderText(L("例如 my-airport（留空则自动根据 URL 命名）", "e.g. my-provider (optional)"));
    }
    userAgentEdit_->setPlaceholderText(L("可选（留空默认 clash.meta/v1.19.24）", "Optional (default clash.meta/v1.19.24)"));
    activateCheck_->setText(L("拉取成功后立即切换至该配置", "Switch to this profile immediately after pull"));
    toggleUrlVisBtn_->setText(urlVisible_ ? L("隐藏", "Hide") : L("显示", "Show"));
    saveSubBtn_->setText(L("拉取并保存订阅", "Save & Pull Subscription"));
    refreshSelBtn_->setText(L("立即更新选中", "Refresh Selected"));
    refreshAllBtn_->setText(L("更新全部", "Refresh All"));
    deleteBtn_->setText(L("删除选中订阅", "Delete Selected"));
    closeBtn_->setText(L("关闭", "Close"));
}

void SubscriptionsDialog::onToggleUrlVisibility() {
    urlVisible_ = !urlVisible_;
    urlEdit_->setEchoMode(urlVisible_ ? QLineEdit::Normal : QLineEdit::Password);
    toggleUrlVisBtn_->setText(urlVisible_ ? L("隐藏", "Hide") : L("显示", "Show"));
}

QString SubscriptionsDialog::maskSubscriptionUrl(const QString& rawUrl) {
    const QString trimmed = rawUrl.trimmed();
    if (trimmed.isEmpty()) {
        return QStringLiteral("—");
    }
    const QUrl parsed(trimmed);
    if (parsed.isValid() && !parsed.scheme().isEmpty() && !parsed.host().isEmpty()) {
        QString authority = parsed.scheme() + QStringLiteral("://") + parsed.host();
        if (parsed.port() > 0) {
            authority += QLatin1Char(':') + QString::number(parsed.port());
        }
        return authority + QStringLiteral("/***");
    }
    if (trimmed.size() > 14) {
        return trimmed.left(10) + QStringLiteral("***");
    }
    return QStringLiteral("***");
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
    suppressAutoUpdateSignal_ = true;
    autoUpdateCheck_->setChecked(settings.value(QStringLiteral("auto_update_subs")).toBool(true));
    const int sec = settings.value(QStringLiteral("auto_update_interval_sec")).toInt(7200);
    intervalMinutesSpin_->setValue(qMax(5, sec / 60));
    suppressAutoUpdateSignal_ = false;

    if (table_->currentRow() >= 0 && table_->item(table_->currentRow(), 0)) {
        selectedProfileName_ = table_->item(table_->currentRow(), 0)->text();
    }

    const QJsonArray subs = snapshot.value(QStringLiteral("subscriptions")).toArray();
    table_->blockSignals(true);
    table_->setRowCount(subs.size());
    int rowToSelect = -1;

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
            const QString expText = ui.value(QStringLiteral("expire_text")).toString().trimmed();
            if (expTs > 0) {
                expireStr = QDateTime::fromSecsSinceEpoch(expTs).toString(QStringLiteral("yyyy-MM-dd"));
            } else if (!expText.isEmpty()) {
                expireStr = expText;
            }
        }

        QString updatedStr = QStringLiteral("—");
        const qint64 updTs = static_cast<qint64>(s.value(QStringLiteral("updated_at")).toDouble(0));
        if (updTs > 0) {
            updatedStr = QDateTime::fromSecsSinceEpoch(updTs).toString(QStringLiteral("MM-dd HH:mm"));
        }

        auto* nameItem = new QTableWidgetItem(name);
        nameItem->setData(Qt::UserRole, s.value(QStringLiteral("user_agent")).toString());

        auto* urlItem = new QTableWidgetItem(maskSubscriptionUrl(url));
        urlItem->setData(Qt::UserRole, url);

        table_->setItem(i, 0, nameItem);
        table_->setItem(i, 1, urlItem);
        table_->setItem(i, 2, new QTableWidgetItem(trafficStr));
        table_->setItem(i, 3, new QTableWidgetItem(expireStr));
        table_->setItem(i, 4, new QTableWidgetItem(updatedStr));

        if (!selectedProfileName_.isEmpty() && name == selectedProfileName_) {
            rowToSelect = i;
        }
    }
    if (rowToSelect >= 0) {
        table_->selectRow(rowToSelect);
    }
    table_->blockSignals(false);

    if (rowToSelect >= 0 && !formDirty_) {
        suppressSelectionFill_ = true;
        onSelectionChanged();
        suppressSelectionFill_ = false;
    }
}

void SubscriptionsDialog::onSelectionChanged() {
    const int row = table_->currentRow();
    if (row < 0 || row >= table_->rowCount()) return;
    selectedProfileName_ = table_->item(row, 0)->text();
    nameEdit_->setText(selectedProfileName_);
    urlEdit_->setText(table_->item(row, 1)->data(Qt::UserRole).toString());
    userAgentEdit_->setText(table_->item(row, 0)->data(Qt::UserRole).toString());
    if (!suppressSelectionFill_) {
        formDirty_ = false;
    }
}

void SubscriptionsDialog::onAddOrUpdate() {
    if (!client_) return;
    const QString url = urlEdit_->text().trimmed();
    if (url.isEmpty()) {
        QMessageBox::warning(this, windowTitle(), L("请输入订阅 URL。", "Please enter a subscription URL."));
        return;
    }
    saveSubBtn_->setEnabled(false);
    statusLabel_->setStyleSheet(QStringLiteral("color: #60a5fa; font-size: 12px; font-weight: 600;"));
    statusLabel_->setText(L("正在拉取并校验订阅配置…", "Pulling & validating subscription…"));

    QJsonObject params;
    params.insert(QStringLiteral("name"), nameEdit_->text().trimmed());
    params.insert(QStringLiteral("url"), url);
    params.insert(QStringLiteral("user_agent"), userAgentEdit_->text().trimmed());
    params.insert(QStringLiteral("select_after"), activateCheck_->isChecked());
    client_->call(QStringLiteral("upsert_subscription"), params, [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
        saveSubBtn_->setEnabled(true);
        if (!ok) {
            statusLabel_->setStyleSheet(QStringLiteral("color: #f87171; font-size: 12px; font-weight: 600;"));
            statusLabel_->setText(L("拉取失败", "Failed"));
            QMessageBox::critical(this, windowTitle(), I18n::translateBackendText(err));
            return;
        }
        formDirty_ = false;
        const QString savedName = data.toObject().value(QStringLiteral("name")).toString();
        if (!savedName.isEmpty()) {
            selectedProfileName_ = savedName;
        }
        statusLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 12px; font-weight: 600;"));
        statusLabel_->setText(L("✓ 订阅已保存并完成拉取", "✓ Subscription saved & updated"));
        client_->call(QStringLiteral("get_state"));
    });
}

void SubscriptionsDialog::onRefreshSelected() {
    if (!client_) return;
    const int row = table_->currentRow();
    if (row < 0 || row >= table_->rowCount()) {
        QMessageBox::information(this, windowTitle(), L("请先在列表中选中一个托管订阅。", "Please select a subscription row first."));
        return;
    }
    const QString subName = table_->item(row, 0)->text();
    refreshSelBtn_->setEnabled(false);
    statusLabel_->setStyleSheet(QStringLiteral("color: #60a5fa; font-size: 12px; font-weight: 600;"));
    statusLabel_->setText(L("正在更新订阅「%1」…", "Refreshing \"%1\"…").arg(subName));

    QJsonObject params;
    params.insert(QStringLiteral("name"), subName);
    client_->call(QStringLiteral("refresh_subscription"), params, [this, subName](bool ok, const QJsonValue&, const QString& err, const QString&) {
        refreshSelBtn_->setEnabled(true);
        if (!ok) {
            statusLabel_->setStyleSheet(QStringLiteral("color: #f87171; font-size: 12px; font-weight: 600;"));
            statusLabel_->setText(L("更新失败", "Refresh failed"));
            QMessageBox::warning(this, windowTitle(), I18n::translateBackendText(err));
        } else {
            statusLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 12px; font-weight: 600;"));
            statusLabel_->setText(L("✓ 已更新订阅「%1」", "✓ Refreshed \"%1\"").arg(subName));
        }
        client_->call(QStringLiteral("get_state"));
    });
}

void SubscriptionsDialog::onRefreshAll() {
    if (!client_ || table_->rowCount() <= 0) return;
    const int total = table_->rowCount();
    refreshAllBtn_->setEnabled(false);
    statusLabel_->setStyleSheet(QStringLiteral("color: #60a5fa; font-size: 12px; font-weight: 600;"));
    statusLabel_->setText(L("正在并发更新全部 %1 个订阅…", "Refreshing all %1 subscriptions…").arg(total));

    auto remaining = std::make_shared<int>(total);
    auto failures = std::make_shared<QStringList>();
    QPointer<SubscriptionsDialog> self(this);

    for (int i = 0; i < total; ++i) {
        const QString subName = table_->item(i, 0)->text();
        QJsonObject params;
        params.insert(QStringLiteral("name"), subName);
        client_->call(QStringLiteral("refresh_subscription"), params, [self, subName, remaining, failures](bool ok, const QJsonValue&, const QString& err, const QString&) {
            if (!ok) {
                failures->append(subName + QStringLiteral(": ") + I18n::translateBackendText(err));
            }
            if (--(*remaining) == 0 && self) {
                self->refreshAllBtn_->setEnabled(true);
                if (failures->isEmpty()) {
                    self->statusLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 12px; font-weight: 600;"));
                    self->statusLabel_->setText(L("✓ 全部订阅已更新完成", "✓ All subscriptions refreshed"));
                } else {
                    self->statusLabel_->setStyleSheet(QStringLiteral("color: #fbbf24; font-size: 12px; font-weight: 600;"));
                    self->statusLabel_->setText(L("部分订阅更新失败", "Some subscriptions failed"));
                    QMessageBox::warning(self, self->windowTitle(), failures->join(QLatin1Char('\n')));
                }
                self->client_->call(QStringLiteral("get_state"));
            }
        });
    }
}

void SubscriptionsDialog::onDeleteSelected() {
    if (!client_) return;
    const int row = table_->currentRow();
    if (row < 0 || row >= table_->rowCount()) {
        QMessageBox::information(this, windowTitle(), L("请先在列表中选中要移除的订阅。", "Please select a subscription row first."));
        return;
    }
    const QString name = table_->item(row, 0)->text();

    QMessageBox box(this);
    box.setIcon(QMessageBox::Question);
    box.setWindowTitle(windowTitle());
    box.setText(L("如何处理托管订阅「%1」？", "How would you like to remove \"%1\"?").arg(name));
    box.setInformativeText(
        L("你可以同时删除对应的本地 .yaml 配置文件，或者仅移除定时更新记录并保留本地配置文件。",
          "You can either delete both the subscription and its local .yaml profile, or unlink auto-updates while keeping the local .yaml file."));
    auto* delBothBtn = box.addButton(L("删除订阅及本地文件", "Delete Subscription & Local File"), QMessageBox::DestructiveRole);
    auto* unlinkOnlyBtn = box.addButton(L("仅移除订阅记录 (保留本地文件)", "Unlink Only (Keep Local File)"), QMessageBox::ActionRole);
    auto* cancelBtn = box.addButton(L("取消", "Cancel"), QMessageBox::RejectRole);
    box.setDefaultButton(cancelBtn);
    box.exec();

    if (box.clickedButton() != delBothBtn && box.clickedButton() != unlinkOnlyBtn) {
        return;
    }

    const bool deleteFile = (box.clickedButton() == delBothBtn);
    QJsonObject params;
    params.insert(QStringLiteral("name"), name);
    params.insert(QStringLiteral("delete_file"), deleteFile);
    client_->call(QStringLiteral("remove_subscription"), params, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
        if (!ok) {
            QMessageBox::warning(this, windowTitle(), I18n::translateBackendText(err));
            return;
        }
        selectedProfileName_.clear();
        formDirty_ = false;
        nameEdit_->clear();
        urlEdit_->clear();
        userAgentEdit_->clear();
        statusLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 12px; font-weight: 600;"));
        statusLabel_->setText(L("✓ 已移除订阅", "✓ Subscription removed"));
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
        if (!ok) {
            QMessageBox::warning(this, windowTitle(), I18n::translateBackendText(err));
            return;
        }
        statusLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 12px; font-weight: 600;"));
        statusLabel_->setText(L("✓ 自动更新设置已保存", "✓ Auto-update interval saved"));
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
    resize(680, 560);

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

    auto* hkSysRow = new QHBoxLayout();
    hotkeySysProxyEdit_ = new QKeySequenceEdit(autoPage);
    auto* clearHkSysBtn = new QPushButton(L("清除", "Clear"), autoPage);
    clearHkSysBtn->setAutoDefault(false);
    hkSysRow->addWidget(hotkeySysProxyEdit_, 1);
    hkSysRow->addWidget(clearHkSysBtn);

    auto* hkModeRow = new QHBoxLayout();
    hotkeyModeEdit_ = new QKeySequenceEdit(autoPage);
    auto* clearHkModeBtn = new QPushButton(L("清除", "Clear"), autoPage);
    clearHkModeBtn->setAutoDefault(false);
    hkModeRow->addWidget(hotkeyModeEdit_, 1);
    hkModeRow->addWidget(clearHkModeBtn);

    auto keepSingleChord = [](QKeySequenceEdit* ed) {
        connect(ed, &QKeySequenceEdit::keySequenceChanged, ed, [ed](const QKeySequence& seq) {
            if (seq.count() > 1) {
                ed->setKeySequence(QKeySequence(seq[0]));
            }
        });
    };
    keepSingleChord(hotkeySysProxyEdit_);
    keepSingleChord(hotkeyModeEdit_);
    connect(clearHkSysBtn, &QPushButton::clicked, hotkeySysProxyEdit_, &QKeySequenceEdit::clear);
    connect(clearHkModeBtn, &QPushButton::clicked, hotkeyModeEdit_, &QKeySequenceEdit::clear);

    currentNetLabel_ = new QLabel(autoPage);
    currentNetLabel_->setWordWrap(true);
    currentNetLabel_->setStyleSheet(QStringLiteral("color: #a1a1aa; font-size: 12px;"));
    auto* addCurNetBtn = new QPushButton(L("选择当前网络特征加入自动暂停列表…", "Add Active Network Feature to Auto-Suspend…"), autoPage);

    suspendNetsEdit_ = new QPlainTextEdit(autoPage);
    suspendNetsEdit_->setPlaceholderText(
        L("每行填写一个当前连接的 Wi-Fi / 网络配置名、网关 IP 或 DNS 后缀；匹配时自动暂停系统代理。",
          "One active Wi-Fi / Network Profile name, Gateway IP, or DNS suffix per line."));

    autoForm->addRow(L("开关系统代理快捷键:", "Toggle System Proxy Hotkey:"), hkSysRow);
    autoForm->addRow(L("切换出站模式快捷键:", "Switch Outbound Mode Hotkey:"), hkModeRow);
    autoForm->addRow(L("当前活动网络特征:", "Detected Active Network:"), currentNetLabel_);
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
    hotkeySysProxyEdit_->setKeySequence(
        QKeySequence::fromString(hk.value(QStringLiteral("toggle_system_proxy")).toString(), QKeySequence::PortableText));
    hotkeyModeEdit_->setKeySequence(
        QKeySequence::fromString(hk.value(QStringLiteral("switch_outbound_mode")).toString(), QKeySequence::PortableText));

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
    if (detectedNetTokens_.isEmpty()) {
        QMessageBox::information(this, windowTitle(), L("当前未检测到可匹配的活动网络特征。", "No active network features detected."));
        return;
    }
    QString candidate = detectedNetTokens_.first();
    if (detectedNetTokens_.size() > 1) {
        bool okChoice = false;
        const QString picked = QInputDialog::getItem(
            this,
            windowTitle(),
            L("请选择要用于识别当前网络的特征（当连接到该网络时自动暂停系统代理）：",
              "Select the active network identifier to trigger auto-suspend:"),
            detectedNetTokens_,
            0,
            false,
            &okChoice);
        if (!okChoice || picked.trimmed().isEmpty()) {
            return;
        }
        candidate = picked.trimmed();
    }
    QStringList existing = suspendNetsEdit_->toPlainText().split(QLatin1Char('\n'), Qt::SkipEmptyParts);
    if (!existing.contains(candidate, Qt::CaseInsensitive)) {
        existing << candidate;
        suspendNetsEdit_->setPlainText(existing.join(QLatin1Char('\n')));
    }
}

void SettingsDialog::onSaveClicked() {
    if (!client_) return;

    // Merge onto live daemon settings snapshot so tray changes made while this dialog was open are preserved.
    const QJsonObject liveSettings = client_->snapshot().value(QStringLiteral("settings")).toObject();
    QJsonObject updated = liveSettings.isEmpty() ? baseSettings_ : liveSettings;

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
    hk.insert(QStringLiteral("toggle_system_proxy"),
              hotkeySysProxyEdit_->keySequence().toString(QKeySequence::PortableText).trimmed());
    hk.insert(QStringLiteral("switch_outbound_mode"),
              hotkeyModeEdit_->keySequence().toString(QKeySequence::PortableText).trimmed());
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
            QMessageBox::critical(this, windowTitle(), I18n::translateBackendText(err));
            return;
        }
        accept();
    });
}

} // namespace clashfx
