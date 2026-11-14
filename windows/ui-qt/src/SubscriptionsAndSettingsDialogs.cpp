#include "SubscriptionsAndSettingsDialogs.h"

#include "DaemonClient.h"
#include "I18n.h"
#include "ModernWidgets.h"

#include <QAbstractItemView>
#include <QComboBox>
#include <QDateTime>
#include <QDesktopServices>
#include <QFileDialog>
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
#include <QScrollArea>
#include <QSignalBlocker>
#include <QSpinBox>
#include <QStackedWidget>
#include <QTableWidget>
#include <QTableWidgetItem>
#include <QUrl>
#include <QVBoxLayout>

namespace clashfx {

namespace {

inline QString uiText(const char* zh, const char* en) {
    return I18n::instance().isChinese() ? QString::fromUtf8(zh) : QString::fromUtf8(en);
}

QString formatBytesReadable(double bytes) {
    if (bytes <= 0.0) {
        return QStringLiteral("0 B");
    }
    static const char* kUnits[] = {"B", "KB", "MB", "GB", "TB"};
    int idx = 0;
    double val = bytes;
    while (val >= 1024.0 && idx < 4) {
        val /= 1024.0;
        ++idx;
    }
    return QString::number(val, 'f', idx <= 1 ? 0 : (val >= 100.0 ? 1 : 2)) + QLatin1Char(' ') +
           QLatin1String(kUnits[idx]);
}

QString maskSubscriptionUrl(const QString& rawUrl) {
    const QString trimmed = rawUrl.trimmed();
    if (trimmed.isEmpty()) {
        return QStringLiteral("—");
    }
    const QUrl url(trimmed);
    if (!url.isValid() || url.host().isEmpty()) {
        return QStringLiteral("https://***/***");
    }
    const QString scheme = url.scheme().isEmpty() ? QStringLiteral("https") : url.scheme();
    return QStringLiteral("%1://%2/***").arg(scheme, url.host());
}

void styleSubTable(QTableWidget* table) {
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

QFrame* addSettingsRow(QVBoxLayout* cardLayout,
                       QWidget* cardParent,
                       QLabel*& titleOut,
                       QLabel*& subOut,
                       QWidget* controlWidget,
                       bool addTopDivider) {
    if (addTopDivider) {
        auto* div = new QFrame(cardParent);
        div->setFixedHeight(1);
        div->setStyleSheet(QStringLiteral("background: #222634; border: none;"));
        cardLayout->addWidget(div);
    }
    auto* rowFrame = new QFrame(cardParent);
    rowFrame->setObjectName(QStringLiteral("SettingRow"));
    rowFrame->setStyleSheet(QStringLiteral("QFrame#SettingRow { background: transparent; border: none; }"));
    auto* h = new QHBoxLayout(rowFrame);
    h->setContentsMargins(0, 12, 0, 12);
    h->setSpacing(18);

    auto* txtCol = new QVBoxLayout();
    txtCol->setSpacing(3);
    titleOut = new QLabel(rowFrame);
    titleOut->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 600;"));
    subOut = new QLabel(rowFrame);
    subOut->setWordWrap(true);
    subOut->setStyleSheet(QStringLiteral("color: #7c8497; font-size: 11px;"));
    txtCol->addWidget(titleOut);
    txtCol->addWidget(subOut);
    h->addLayout(txtCol, 1);

    if (controlWidget) {
        h->addWidget(controlWidget, 0, Qt::AlignVCenter);
    }
    cardLayout->addWidget(rowFrame);
    return rowFrame;
}

QScrollArea* wrapInTransparentScroll(QWidget* parent, QWidget*& contentOut, QVBoxLayout*& layoutOut) {
    auto* scroll = new QScrollArea(parent);
    scroll->setFrameShape(QFrame::NoFrame);
    scroll->setWidgetResizable(true);
    scroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    scroll->setStyleSheet(QStringLiteral(
        "QScrollArea { background: transparent; border: none; }"
        "QWidget#SettingsScrollContent { background: transparent; }"));

    contentOut = new QWidget(scroll);
    contentOut->setObjectName(QStringLiteral("SettingsScrollContent"));
    layoutOut = new QVBoxLayout(contentOut);
    layoutOut->setContentsMargins(0, 0, 6, 8);
    layoutOut->setSpacing(14);
    scroll->setWidget(contentOut);
    return scroll;
}

}  // namespace

// ===========================================================================
// SubscriptionsPage
// ===========================================================================

SubscriptionsPage::SubscriptionsPage(DaemonClient* client, QWidget* parent)
    : QWidget(parent)
    , client_(client) {
    auto* outerLay = new QVBoxLayout(this);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(this);
    titleLabel_ = hdr.titleLabel;
    countPill_ = hdr.primaryBadge;
    formPill_ = hdr.secondaryBadge;
    subtitleLabel_ = hdr.subtitleLabel;

    refreshAllBtn_ = new QPushButton(this);
    saveFormBtn_ = new QPushButton(this);
    saveFormBtn_->setProperty("primary", true);
    hdr.actionsLayout->addWidget(refreshAllBtn_);
    hdr.actionsLayout->addWidget(saveFormBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(this);
    auto* root = new QVBoxLayout(body);
    root->setContentsMargins(24, 16, 24, 18);
    root->setSpacing(14);
    outerLay->addWidget(body, 1);

    // Top Card: Add / Update Subscription + Auto Update Policy
    auto* formCard = createCardFrame(body);
    auto* formLay = new QVBoxLayout(formCard);
    formLay->setContentsMargins(16, 14, 16, 14);
    formLay->setSpacing(12);

    auto* row1 = new QHBoxLayout();
    row1->setSpacing(12);
    nameLbl_ = new QLabel(formCard);
    nameLbl_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px; font-weight: 600;"));
    nameEdit_ = new QLineEdit(formCard);
    nameEdit_->setMaximumWidth(220);

    uaLbl_ = new QLabel(formCard);
    uaLbl_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px; font-weight: 600;"));
    uaEdit_ = new QLineEdit(formCard);
    uaEdit_->setMaximumWidth(220);

    selectAfterLbl_ = new QLabel(formCard);
    selectAfterLbl_->setStyleSheet(QStringLiteral("color: #cbd5e1; font-size: 12px;"));
    selectAfterSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Emerald, formCard);
    selectAfterSwitch_->setCheckedSilent(true);

    row1->addWidget(nameLbl_);
    row1->addWidget(nameEdit_, 1);
    row1->addWidget(uaLbl_);
    row1->addWidget(uaEdit_, 1);
    row1->addSpacing(8);
    row1->addWidget(selectAfterLbl_);
    row1->addWidget(selectAfterSwitch_);
    formLay->addLayout(row1);

    auto* row2 = new QHBoxLayout();
    row2->setSpacing(12);
    urlLbl_ = new QLabel(formCard);
    urlLbl_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px; font-weight: 600;"));
    urlEdit_ = new QLineEdit(formCard);
    urlEdit_->setClearButtonEnabled(true);
    clearFormBtn_ = new QPushButton(formCard);
    clearFormBtn_->setProperty("ghost", true);
    row2->addWidget(urlLbl_);
    row2->addWidget(urlEdit_, 1);
    row2->addWidget(clearFormBtn_);
    formLay->addLayout(row2);

    auto* div = new QFrame(formCard);
    div->setFixedHeight(1);
    div->setStyleSheet(QStringLiteral("background: #222634; border: none;"));
    formLay->addWidget(div);

    auto* row3 = new QHBoxLayout();
    row3->setSpacing(10);
    autoUpdateSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Blue, formCard);
    autoUpdateLbl_ = new QLabel(formCard);
    autoUpdateLbl_->setStyleSheet(QStringLiteral("color: #cbd5e1; font-size: 12px; font-weight: 600;"));
    autoIntervalCombo_ = new QComboBox(formCard);
    autoIntervalCombo_->setMinimumWidth(140);
    row3->addWidget(autoUpdateSwitch_);
    row3->addWidget(autoUpdateLbl_);
    row3->addWidget(autoIntervalCombo_);
    row3->addStretch(1);
    formLay->addLayout(row3);

    root->addWidget(formCard);

    // Subscriptions Table Card
    auto* tableCard = createCardFrame(this);
    auto* tableLay = new QVBoxLayout(tableCard);
    tableLay->setContentsMargins(14, 10, 14, 12);
    table_ = new QTableWidget(0, 6, tableCard);
    styleSubTable(table_);
    table_->verticalHeader()->setDefaultSectionSize(48);
    table_->setItemDelegateForColumn(0, new PillItemDelegate(8, table_));
    table_->setItemDelegateForColumn(3, new QuotaBarDelegate(table_));

    QHeaderView* hh = table_->horizontalHeader();
    hh->setSectionResizeMode(0, QHeaderView::Fixed);
    table_->setColumnWidth(0, 96);
    hh->setSectionResizeMode(1, QHeaderView::Fixed);
    table_->setColumnWidth(1, 134);
    hh->setSectionResizeMode(2, QHeaderView::Fixed);
    table_->setColumnWidth(2, 172);
    hh->setSectionResizeMode(3, QHeaderView::Stretch);
    hh->setSectionResizeMode(4, QHeaderView::Fixed);
    table_->setColumnWidth(4, 166);
    hh->setSectionResizeMode(5, QHeaderView::Fixed);
    table_->setColumnWidth(5, 220);
    tableLay->addWidget(table_, 1);
    root->addWidget(tableCard, 1);

    auto onFormEdit = [this](const QString&) {
        if (!refreshing_) {
            setFormDirty(!nameEdit_->text().trimmed().isEmpty() || !urlEdit_->text().trimmed().isEmpty() ||
                         !uaEdit_->text().trimmed().isEmpty());
        }
    };
    connect(nameEdit_, &QLineEdit::textEdited, this, onFormEdit);
    connect(urlEdit_, &QLineEdit::textEdited, this, onFormEdit);
    connect(uaEdit_, &QLineEdit::textEdited, this, onFormEdit);
    connect(clearFormBtn_, &QPushButton::clicked, this, [this]() { clearForm(); });
    connect(saveFormBtn_, &QPushButton::clicked, this, [this]() { upsertSubscription(); });
    connect(refreshAllBtn_, &QPushButton::clicked, this, [this]() { refreshAllSubscriptions(); });
    connect(autoUpdateSwitch_, &ToggleSwitch::toggled, this, [this](bool) {
        if (!refreshing_) {
            saveAutoUpdatePolicy();
        }
    });
    connect(autoIntervalCombo_, &QComboBox::currentIndexChanged, this, [this](int) {
        if (!refreshing_) {
            saveAutoUpdatePolicy();
        }
    });
    connect(table_, &QTableWidget::cellDoubleClicked, this, [this](int row, int) {
        if (auto* item = table_->item(row, 1)) {
            const QString profileName = item->data(Qt::UserRole).toString();
            if (!profileName.isEmpty()) {
                activateProfile(profileName);
            }
        }
    });
    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });

    retranslateUi();
    if (client_) {
        updateSnapshot(client_->snapshot());
    }
}

void SubscriptionsPage::setFormDirty(bool dirty) {
    formDirty_ = dirty;
    if (formDirty_) {
        formPill_->setPill(uiText("正在编辑表单", "Editing Form"), PillTone::Amber);
    } else {
        formPill_->setPill(QString(), PillTone::Muted);
    }
}

void SubscriptionsPage::clearForm() {
    refreshing_ = true;
    nameEdit_->clear();
    urlEdit_->clear();
    uaEdit_->clear();
    refreshing_ = false;
    setFormDirty(false);
}

void SubscriptionsPage::retranslateUi() {
    titleLabel_->setText(uiText("托管订阅与配置文件管理", "Managed Subscriptions & Profiles"));
    subtitleLabel_->setText(uiText(
        "安全托管机场订阅链接与流量配额；表内地址已自动脱敏，切换订阅时自动合并保留你的 Mixin 规则与 AI 锁定策略",
        "Manage remote subscriptions and local profiles with masked URLs; Mixin rules and AI Lock persist across switches"));
    refreshAllBtn_->setText(uiText("刷新全部订阅", "Refresh All"));
    saveFormBtn_->setText(uiText("拉取并保存订阅", "Fetch & Save"));
    nameLbl_->setText(uiText("配置名称", "Name"));
    nameEdit_->setPlaceholderText(uiText("例如 demo-airport", "e.g. demo-airport"));
    uaLbl_->setText(QStringLiteral("User-Agent"));
    uaEdit_->setPlaceholderText(QStringLiteral("clash.meta/1.19.24"));
    selectAfterLbl_->setText(uiText("保存后立即激活", "Activate After Fetch"));
    urlLbl_->setText(uiText("订阅链接", "Sub URL"));
    urlEdit_->setPlaceholderText(QStringLiteral("https://example.com/api/v1/client/subscribe?token=..."));
    clearFormBtn_->setText(uiText("清空表单", "Clear"));
    autoUpdateLbl_->setText(uiText("后台定时自动更新订阅间隔:", "Auto-Update Interval:"));

    {
        QSignalBlocker sb(autoIntervalCombo_);
        const int curSec = autoIntervalCombo_->currentData().toInt();
        autoIntervalCombo_->clear();
        autoIntervalCombo_->addItem(uiText("每 30 分钟", "Every 30m"), 1800);
        autoIntervalCombo_->addItem(uiText("每 1 小时", "Every 1h"), 3600);
        autoIntervalCombo_->addItem(uiText("每 2 小时", "Every 2h"), 7200);
        autoIntervalCombo_->addItem(uiText("每 6 小时", "Every 6h"), 21600);
        autoIntervalCombo_->addItem(uiText("每 12 小时", "Every 12h"), 43200);
        autoIntervalCombo_->addItem(uiText("每 24 小时", "Every 24h"), 86400);
        const int idx = autoIntervalCombo_->findData(curSec > 0 ? curSec : 7200);
        autoIntervalCombo_->setCurrentIndex(idx >= 0 ? idx : 2);
    }

    table_->setHorizontalHeaderLabels({
        uiText("状态", "Status"),
        uiText("订阅 / 配置名称", "Profile Name"),
        uiText("脱敏来源", "Masked Source"),
        uiText("流量配额进度", "Quota Usage"),
        uiText("到期与更新", "Expiry & Updated"),
        uiText("操作", "Actions"),
    });

    setFormDirty(formDirty_);
    updateSnapshot(lastSnapshot_);
}

void SubscriptionsPage::updateSnapshot(const QJsonObject& snapshot) {
    lastSnapshot_ = snapshot;
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const QString selectedProfile = settings.value(QStringLiteral("selected_profile")).toString();
    const QJsonArray subs = snapshot.value(QStringLiteral("subscriptions")).toArray();
    const QJsonArray profiles = snapshot.value(QStringLiteral("profiles")).toArray();
    const bool connected = client_ && client_->isConnected();

    refreshAllBtn_->setEnabled(connected && !busy_ && !subs.isEmpty());
    saveFormBtn_->setEnabled(connected && !busy_);

    refreshing_ = true;
    autoUpdateSwitch_->setCheckedSilent(settings.value(QStringLiteral("auto_update_subs")).toBool(true));
    const int sec = settings.value(QStringLiteral("auto_update_interval_sec")).toInt(7200);
    const int intervalIdx = autoIntervalCombo_->findData(sec);
    if (intervalIdx >= 0) {
        autoIntervalCombo_->setCurrentIndex(intervalIdx);
    }
    refreshing_ = false;

    struct RowEntry {
        QString name;
        QString url;
        QString userAgent;
        qint64 updatedAt = 0;
        QJsonObject userInfo;
        bool isManaged = false;
    };
    QList<RowEntry> entries;
    QSet<QString> managedNames;
    subNamesOrder_.clear();

    for (const QJsonValue& v : subs) {
        const QJsonObject o = v.toObject();
        const QString n = o.value(QStringLiteral("name")).toString();
        managedNames.insert(n);
        subNamesOrder_.push_back(n);
        entries.push_back({
            n,
            o.value(QStringLiteral("url")).toString(),
            o.value(QStringLiteral("user_agent")).toString(),
            static_cast<qint64>(o.value(QStringLiteral("updated_at")).toDouble(0)),
            o.value(QStringLiteral("userinfo")).toObject(),
            true,
        });
    }
    for (const QJsonValue& pVal : profiles) {
        const QString p = pVal.toString();
        if (!p.isEmpty() && !managedNames.contains(p)) {
            entries.push_back({p, QString(), QString(), 0, QJsonObject(), false});
        }
    }

    if (selectedProfile.isEmpty()) {
        countPill_->setPill(QString::number(subs.size()) + uiText(" 个托管订阅", " subscriptions"), PillTone::Blue);
    } else {
        countPill_->setPill(uiText("当前: ", "Active: ") + selectedProfile + QStringLiteral(" · ") +
                                QString::number(entries.size()),
                            PillTone::Green);
    }

    QSignalBlocker sb(table_);
    table_->setRowCount(static_cast<int>(entries.size()));

    for (int i = 0; i < entries.size(); ++i) {
        const RowEntry& e = entries.at(i);
        const bool isActive = (e.name == selectedProfile);

        // Col 0: Status Pill
        auto* statusItem = new QTableWidgetItem();
        statusItem->setTextAlignment(Qt::AlignCenter);
        if (isActive) {
            statusItem->setText(uiText("● 当前激活", "● Active"));
            statusItem->setData(kPillToneRole, static_cast<int>(PillTone::Green));
        } else if (e.isManaged) {
            statusItem->setText(uiText("○ 托管订阅", "○ Standby"));
            statusItem->setData(kPillToneRole, static_cast<int>(PillTone::Muted));
        } else {
            statusItem->setText(uiText("○ 本地文件", "○ Local"));
            statusItem->setData(kPillToneRole, static_cast<int>(PillTone::Blue));
        }
        table_->setItem(i, 0, statusItem);

        // Col 1: Name
        auto* nameItem = new QTableWidgetItem(e.name);
        nameItem->setData(Qt::UserRole, e.name);
        QFont nf = nameItem->font();
        nf.setBold(true);
        nameItem->setFont(nf);
        table_->setItem(i, 1, nameItem);

        // Col 2: Masked URL
        const QString masked = e.isManaged ? maskSubscriptionUrl(e.url) : uiText("本地 YAML 配置", "Local YAML File");
        auto* srcItem = new QTableWidgetItem(masked);
        srcItem->setForeground(QColor(QStringLiteral("#9ca3af")));
        table_->setItem(i, 2, srcItem);

        // Col 3: Quota Bar (QuotaBarDelegate)
        double ratio = -1.0;
        QString quotaLabel = uiText("未返回流量头", "No Quota Header");
        if (e.isManaged && !e.userInfo.isEmpty()) {
            const double up = e.userInfo.value(QStringLiteral("upload")).toDouble(0.0);
            const double down = e.userInfo.value(QStringLiteral("download")).toDouble(0.0);
            const double total = e.userInfo.value(QStringLiteral("total")).toDouble(0.0);
            const double used = qMax(0.0, up + down);
            if (total > 0.0) {
                ratio = qBound(0.0, used / total, 1.0);
                quotaLabel = formatBytesReadable(used) + QStringLiteral(" / ") + formatBytesReadable(total);
            } else if (e.userInfo.contains(QStringLiteral("upload")) ||
                       e.userInfo.contains(QStringLiteral("download"))) {
                quotaLabel = uiText("已用 ", "Used ") + formatBytesReadable(used);
            }
        }
        auto* quotaItem = new QTableWidgetItem(quotaLabel);
        quotaItem->setData(kQuotaRatioRole, ratio);
        table_->setItem(i, 3, quotaItem);

        // Col 4: Expiry & Updated Time
        QStringList metaLines;
        if (!e.userInfo.isEmpty()) {
            const qint64 expTs = static_cast<qint64>(e.userInfo.value(QStringLiteral("expire")).toDouble(0));
            const QString expTxt = e.userInfo.value(QStringLiteral("expire_text")).toString().trimmed();
            if (expTs > 0) {
                metaLines << QDateTime::fromSecsSinceEpoch(expTs).toString(QStringLiteral("yyyy-MM-dd")) +
                                 uiText(" 到期", " exp");
            } else if (!expTxt.isEmpty()) {
                metaLines << expTxt;
            }
        }
        if (e.updatedAt > 0) {
            metaLines << uiText("更新 ", "Upd ") +
                             QDateTime::fromSecsSinceEpoch(e.updatedAt).toString(QStringLiteral("MM-dd HH:mm"));
        }
        auto* expItem = new QTableWidgetItem(metaLines.isEmpty() ? QStringLiteral("—") : metaLines.join(QStringLiteral(" · ")));
        expItem->setForeground(QColor(QStringLiteral("#9ca3af")));
        table_->setItem(i, 4, expItem);

        // Col 5: Action Buttons
        auto* actWrap = new QWidget(table_);
        actWrap->setObjectName(QStringLiteral("SubActWrap"));
        actWrap->setStyleSheet(QStringLiteral("QWidget#SubActWrap { background: transparent; }"));
        auto* actLay = new QHBoxLayout(actWrap);
        actLay->setContentsMargins(6, 0, 6, 0);
        actLay->setSpacing(6);

        auto mkRowBtn = [&](const QString& text) {
            auto* b = new QPushButton(text, actWrap);
            b->setStyleSheet(QStringLiteral(
                "QPushButton { padding: 4px 8px; font-size: 11px; min-height: 24px; border-radius: 6px; }"));
            return b;
        };

        auto* actBtn = mkRowBtn(isActive ? uiText("已激活", "Active") : uiText("激活", "Use"));
        actBtn->setEnabled(!isActive);
        if (!isActive) {
            actBtn->setProperty("primary", true);
        }
        actLay->addWidget(actBtn);
        connect(actBtn, &QPushButton::clicked, this, [this, n = e.name]() { activateProfile(n); });

        if (e.isManaged) {
            auto* updBtn = mkRowBtn(uiText("更新", "Pull"));
            auto* editBtn = mkRowBtn(uiText("编辑", "Edit"));
            auto* delBtn = mkRowBtn(uiText("删除", "Del"));
            delBtn->setStyleSheet(QStringLiteral(
                "QPushButton { background: #26161b; border: 1px solid #592632; color: #f87171;"
                "  padding: 4px 8px; font-size: 11px; min-height: 24px; border-radius: 6px; font-weight: 600; }"
                "QPushButton:hover { background: #361d25; }"));
            actLay->addWidget(updBtn);
            actLay->addWidget(editBtn);
            actLay->addWidget(delBtn);

            connect(updBtn, &QPushButton::clicked, this, [this, n = e.name]() { refreshSubscription(n); });
            connect(editBtn, &QPushButton::clicked, this, [this, n = e.name, u = e.url, ua = e.userAgent]() {
                refreshing_ = true;
                nameEdit_->setText(n);
                urlEdit_->setText(u);
                uaEdit_->setText(ua);
                refreshing_ = false;
                setFormDirty(true);
                nameEdit_->setFocus();
            });
            connect(delBtn, &QPushButton::clicked, this, [this, n = e.name]() { removeSubscription(n); });
        }
        actLay->addStretch(1);
        table_->setCellWidget(i, 5, actWrap);
    }
}

void SubscriptionsPage::upsertSubscription() {
    if (!client_ || busy_) {
        return;
    }
    const QString name = nameEdit_->text().trimmed();
    const QString url = urlEdit_->text().trimmed();
    if (name.isEmpty() || url.isEmpty()) {
        ToastOverlay::show(window(), uiText("请填写配置名称与订阅链接 URL", "Profile name and URL are required"),
                           PillTone::Amber);
        return;
    }

    busy_ = true;
    saveFormBtn_->setEnabled(false);
    QJsonObject params;
    params.insert(QStringLiteral("name"), name);
    params.insert(QStringLiteral("url"), url);
    params.insert(QStringLiteral("user_agent"), uaEdit_->text().trimmed());
    params.insert(QStringLiteral("select_after"), selectAfterSwitch_->isChecked());

    client_->call(QStringLiteral("upsert_subscription"), params,
                  [this, name](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      busy_ = false;
                      saveFormBtn_->setEnabled(client_ && client_->isConnected());
                      if (!ok) {
                          QMessageBox::warning(window(), uiText("拉取或保存订阅失败", "Subscription Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      clearForm();
                      client_->call(QStringLiteral("get_state"));
                      ToastOverlay::show(
                          window(),
                          uiText("已保存并拉取订阅 ", "Fetched and saved subscription ") + name,
                          PillTone::Green);
                  });
}

void SubscriptionsPage::refreshSubscription(const QString& name) {
    if (!client_ || name.isEmpty()) {
        return;
    }
    QJsonObject params;
    params.insert(QStringLiteral("name"), name);
    client_->call(QStringLiteral("refresh_subscription"), params,
                  [this, name](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      client_->call(QStringLiteral("get_state"));
                      ToastOverlay::show(window(), uiText("已更新订阅 ", "Refreshed subscription ") + name,
                                         PillTone::Green);
                  });
}

void SubscriptionsPage::refreshAllSubscriptions() {
    if (!client_ || subNamesOrder_.isEmpty()) {
        return;
    }
    refreshAllBtn_->setEnabled(false);
    const QStringList targets = subNamesOrder_;
    auto remaining = std::make_shared<int>(targets.size());
    auto failures = std::make_shared<int>(0);

    for (const QString& n : targets) {
        QJsonObject params;
        params.insert(QStringLiteral("name"), n);
        client_->call(QStringLiteral("refresh_subscription"), params,
                      [this, remaining, failures](bool ok, const QJsonValue&, const QString&, const QString&) {
                          if (!ok) {
                              ++(*failures);
                          }
                          --(*remaining);
                          if (*remaining <= 0) {
                              refreshAllBtn_->setEnabled(client_ && client_->isConnected());
                              client_->call(QStringLiteral("get_state"));
                              if (*failures == 0) {
                                  ToastOverlay::show(window(), uiText("已刷新全部托管订阅", "Refreshed all subscriptions"),
                                                     PillTone::Green);
                              } else {
                                  ToastOverlay::show(
                                      window(),
                                      uiText("部分订阅更新失败 (", "Some subscriptions failed (") +
                                          QString::number(*failures) + QStringLiteral(")"),
                                      PillTone::Amber);
                              }
                          }
                      });
    }
}

void SubscriptionsPage::activateProfile(const QString& name) {
    if (!client_ || name.isEmpty()) {
        return;
    }
    QJsonObject params;
    params.insert(QStringLiteral("name"), name);
    client_->call(QStringLiteral("select_profile"), params,
                  [this, name](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          const bool aiLocked = lastSnapshot_.value(QStringLiteral("settings"))
                                                    .toObject()
                                                    .value(QStringLiteral("claudeProxyLockEnabled"))
                                                    .toBool(false);
                          if (aiLocked) {
                              const auto ans = QMessageBox::question(
                                  window(), uiText("AI 专用锁定冲突", "AI Proxy Lock Active"),
                                  I18n::instance().translateBackendText(err) +
                                      uiText("\n\n是否先关闭 AI 专用节点锁定并切换至 ",
                                             "\n\nDisable AI Proxy Lock and switch to ") +
                                      name + QStringLiteral("?"));
                              if (ans == QMessageBox::Yes) {
                                  QJsonObject dis;
                                  dis.insert(QStringLiteral("enabled"), false);
                                  client_->call(QStringLiteral("set_ai_lock"), dis,
                                                [this, name](bool dOk, const QJsonValue&, const QString& dErr,
                                                             const QString&) {
                                                    if (!dOk) {
                                                        ToastOverlay::show(window(),
                                                                           I18n::instance().translateBackendText(dErr),
                                                                           PillTone::Red);
                                                        return;
                                                    }
                                                    activateProfile(name);
                                                });
                              }
                              return;
                          }
                          QMessageBox::warning(window(), uiText("切换配置失败", "Switch Profile Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      ToastOverlay::show(window(), uiText("已激活配置文件 ", "Activated profile ") + name,
                                         PillTone::Green);
                  });
}

void SubscriptionsPage::removeSubscription(const QString& name) {
    if (!client_ || name.isEmpty()) {
        return;
    }
    QMessageBox box(window());
    box.setWindowTitle(uiText("移除订阅", "Remove Subscription"));
    box.setText(uiText("确认移除托管订阅 \"", "Remove managed subscription \"") + name + QStringLiteral("\" ?"));
    box.setInformativeText(uiText("你可以选择同时清理磁盘上的 YAML 配置文件，或仅移出订阅列表。",
                                  "Choose whether to also delete the cached YAML file on disk."));
    QPushButton* delBothBtn =
        box.addButton(uiText("移除并删除本地文件", "Remove & Delete File"), QMessageBox::DestructiveRole);
    QPushButton* keepFileBtn =
        box.addButton(uiText("仅移除订阅记录", "Keep Local File"), QMessageBox::ActionRole);
    QPushButton* cancelBtn = box.addButton(uiText("取消", "Cancel"), QMessageBox::RejectRole);
    box.setDefaultButton(cancelBtn);
    box.exec();

    if (box.clickedButton() == cancelBtn || !box.clickedButton()) {
        return;
    }
    const bool deleteFile = (box.clickedButton() == delBothBtn);
    Q_UNUSED(keepFileBtn);

    QJsonObject params;
    params.insert(QStringLiteral("name"), name);
    params.insert(QStringLiteral("delete_file"), deleteFile);
    client_->call(QStringLiteral("remove_subscription"), params,
                  [this, name](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          QMessageBox::warning(window(), uiText("移除订阅失败", "Remove Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      ToastOverlay::show(window(), uiText("已移除订阅 ", "Removed subscription ") + name,
                                         PillTone::Muted);
                  });
}

void SubscriptionsPage::saveAutoUpdatePolicy() {
    if (!client_ || !client_->isConnected()) {
        return;
    }
    QJsonObject settings = lastSnapshot_.value(QStringLiteral("settings")).toObject();
    settings.insert(QStringLiteral("auto_update_subs"), autoUpdateSwitch_->isChecked());
    bool okInterval = false;
    const int intervalSec = autoIntervalCombo_->currentData().toInt(&okInterval);
    settings.insert(QStringLiteral("auto_update_interval_sec"), (okInterval && intervalSec > 0) ? intervalSec : 7200);

    QJsonObject params;
    params.insert(QStringLiteral("settings"), settings);
    client_->call(QStringLiteral("update_settings"), params,
                  [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          ToastOverlay::show(window(), I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      ToastOverlay::show(window(), uiText("订阅定时自动更新策略已保存", "Auto-update policy saved"),
                                         PillTone::Green);
                  });
}

// ===========================================================================
// SettingsPage
// ===========================================================================

SettingsPage::SettingsPage(DaemonClient* client, QWidget* parent)
    : QWidget(parent)
    , client_(client) {
    auto* outerLay = new QVBoxLayout(this);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(this);
    titleLabel_ = hdr.titleLabel;
    servicePill_ = hdr.primaryBadge;
    dirtyPill_ = hdr.secondaryBadge;
    subtitleLabel_ = hdr.subtitleLabel;

    tabSwitch_ = new SegmentedControl(true, this);
    tabSwitch_->setCompact(true);
    tabSwitch_->setItems({QStringLiteral("1"), QStringLiteral("2"), QStringLiteral("3")});

    saveBtn_ = new QPushButton(this);
    saveBtn_->setProperty("primary", true);

    hdr.actionsLayout->addWidget(tabSwitch_);
    hdr.actionsLayout->addWidget(saveBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(this);
    auto* root = new QVBoxLayout(body);
    root->setContentsMargins(24, 16, 24, 14);
    root->setSpacing(14);
    outerLay->addWidget(body, 1);

    subStack_ = new QStackedWidget(body);
    root->addWidget(subStack_, 1);

    // -----------------------------------------------------------------------
    // Sub-tab 0: General & Ports
    // -----------------------------------------------------------------------
    QWidget* gContent = nullptr;
    QVBoxLayout* gLay = nullptr;
    QScrollArea* gScroll = wrapInTransparentScroll(subStack_, gContent, gLay);

    // Card 1: Ports & Core Listeners
    auto* portsCard = createCardFrame(gContent);
    auto* pLay = new QVBoxLayout(portsCard);
    pLay->setContentsMargins(18, 14, 18, 10);
    pLay->setSpacing(0);
    cardPortsTitle_ = new QLabel(portsCard);
    cardPortsTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700; margin-bottom: 6px;"));
    pLay->addWidget(cardPortsTitle_);

    portOverrideSpin_ = new QSpinBox(portsCard);
    portOverrideSpin_->setRange(0, 65535);
    portOverrideSpin_->setMinimumWidth(210);
    addSettingsRow(pLay, portsCard, portOverrideTitle_, portOverrideSub_, portOverrideSpin_, false);

    apiPortSpin_ = new QSpinBox(portsCard);
    apiPortSpin_->setRange(1024, 65535);
    apiPortSpin_->setMinimumWidth(210);
    addSettingsRow(pLay, portsCard, apiPortTitle_, apiPortSub_, apiPortSpin_, true);

    apiSecretEdit_ = new QLineEdit(portsCard);
    apiSecretEdit_->setEchoMode(QLineEdit::PasswordEchoOnEdit);
    apiSecretEdit_->setMinimumWidth(210);
    addSettingsRow(pLay, portsCard, apiSecretTitle_, apiSecretSub_, apiSecretEdit_, true);

    allowLanSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Emerald, portsCard);
    addSettingsRow(pLay, portsCard, allowLanTitle_, allowLanSub_, allowLanSwitch_, true);

    ipv6Switch_ = new ToggleSwitch(ToggleSwitch::Accent::Emerald, portsCard);
    addSettingsRow(pLay, portsCard, ipv6Title_, ipv6Sub_, ipv6Switch_, true);
    gLay->addWidget(portsCard);

    // Card 2: Startup, Appearance & Routing Experience
    auto* sysCard = createCardFrame(gContent);
    auto* sLay = new QVBoxLayout(sysCard);
    sLay->setContentsMargins(18, 14, 18, 10);
    sLay->setSpacing(0);
    cardSystemTitle_ = new QLabel(sysCard);
    cardSystemTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700; margin-bottom: 6px;"));
    sLay->addWidget(cardSystemTitle_);

    launchSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Emerald, sysCard);
    addSettingsRow(sLay, sysCard, launchTitle_, launchSub_, launchSwitch_, false);

    bypassCnAppsSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Emerald, sysCard);
    addSettingsRow(sLay, sysCard, bypassCnAppsTitle_, bypassCnAppsSub_, bypassCnAppsSwitch_, true);

    customAsIsSwitch_ = new ToggleSwitch(ToggleSwitch::Accent::Blue, sysCard);
    addSettingsRow(sLay, sysCard, customAsIsTitle_, customAsIsSub_, customAsIsSwitch_, true);

    auto* uiCtrlWrap = new QWidget(sysCard);
    auto* uiCtrlLay = new QHBoxLayout(uiCtrlWrap);
    uiCtrlLay->setContentsMargins(0, 0, 0, 0);
    uiCtrlLay->setSpacing(8);
    langCombo_ = new QComboBox(uiCtrlWrap);
    langCombo_->addItem(QStringLiteral("简体中文"), QStringLiteral("zh_CN"));
    langCombo_->addItem(QStringLiteral("English"), QStringLiteral("en_US"));
    trayCombo_ = new QComboBox(uiCtrlWrap);
    logLevelCombo_ = new QComboBox(uiCtrlWrap);
    logLevelCombo_->addItems({QStringLiteral("silent"), QStringLiteral("error"), QStringLiteral("warning"),
                              QStringLiteral("info"), QStringLiteral("debug")});
    uiCtrlLay->addWidget(langCombo_);
    uiCtrlLay->addWidget(trayCombo_);
    uiCtrlLay->addWidget(logLevelCombo_);
    addSettingsRow(sLay, sysCard, uiLangTitle_, uiLangSub_, uiCtrlWrap, true);

    benchUrlEdit_ = new QLineEdit(sysCard);
    benchUrlEdit_->setMinimumWidth(300);
    addSettingsRow(sLay, sysCard, benchUrlTitle_, benchUrlSub_, benchUrlEdit_, true);

    bypassListEdit_ = new QLineEdit(sysCard);
    bypassListEdit_->setMinimumWidth(300);
    addSettingsRow(sLay, sysCard, bypassListTitle_, bypassListSub_, bypassListEdit_, true);

    auto* dirWrap = new QWidget(sysCard);
    auto* dirLay = new QHBoxLayout(dirWrap);
    dirLay->setContentsMargins(0, 0, 0, 0);
    dirLay->setSpacing(8);
    customDirEdit_ = new QLineEdit(dirWrap);
    customDirEdit_->setMinimumWidth(240);
    browseDirBtn_ = new QPushButton(dirWrap);
    dirLay->addWidget(customDirEdit_);
    dirLay->addWidget(browseDirBtn_);
    addSettingsRow(sLay, sysCard, customDirTitle_, customDirSub_, dirWrap, true);

    gLay->addWidget(sysCard);
    gLay->addStretch(1);
    subStack_->addWidget(gScroll);

    // -----------------------------------------------------------------------
    // Sub-tab 1: Enhanced TUN & Directories
    // -----------------------------------------------------------------------
    QWidget* tContent = nullptr;
    QVBoxLayout* tLay = nullptr;
    QScrollArea* tScroll = wrapInTransparentScroll(subStack_, tContent, tLay);

    auto* svcCard = createCardFrame(tContent);
    auto* svcLay = new QVBoxLayout(svcCard);
    svcLay->setContentsMargins(18, 14, 18, 14);
    auto* dirBtns = new QWidget(svcCard);
    auto* dirBtnLay = new QHBoxLayout(dirBtns);
    dirBtnLay->setContentsMargins(0, 0, 0, 0);
    dirBtnLay->setSpacing(8);
    openCfgDirBtn_ = new QPushButton(dirBtns);
    openLogDirBtn_ = new QPushButton(dirBtns);
    dirBtnLay->addWidget(openCfgDirBtn_);
    dirBtnLay->addWidget(openLogDirBtn_);
    addSettingsRow(svcLay, svcCard, tunServiceTitle_, tunServiceSub_, dirBtns, false);
    tLay->addWidget(svcCard);

    auto* tunCard = createCardFrame(tContent);
    auto* tuLay = new QVBoxLayout(tunCard);
    tuLay->setContentsMargins(18, 14, 18, 14);
    tuLay->setSpacing(0);
    tunCardTitle_ = new QLabel(tunCard);
    tunCardTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700; margin-bottom: 6px;"));
    tuLay->addWidget(tunCardTitle_);

    tunStackCombo_ = new QComboBox(tunCard);
    tunStackCombo_->addItems({QStringLiteral("mixed"), QStringLiteral("system"), QStringLiteral("gvisor")});
    tunStackCombo_->setMinimumWidth(180);
    addSettingsRow(tuLay, tunCard, tunStackTitle_, tunStackSub_, tunStackCombo_, false);

    tunMtuSpin_ = new QSpinBox(tunCard);
    tunMtuSpin_->setRange(1280, 9000);
    tunMtuSpin_->setMinimumWidth(180);
    addSettingsRow(tuLay, tunCard, tunMtuTitle_, tunMtuSub_, tunMtuSpin_, true);

    tunIfaceEdit_ = new QLineEdit(tunCard);
    tunIfaceEdit_->setMinimumWidth(180);
    addSettingsRow(tuLay, tunCard, tunIfaceTitle_, tunIfaceSub_, tunIfaceEdit_, true);

    auto* exDiv = new QFrame(tunCard);
    exDiv->setFixedHeight(1);
    exDiv->setStyleSheet(QStringLiteral("background: #222634; border: none;"));
    tuLay->addWidget(exDiv);
    auto* exCol = new QVBoxLayout();
    exCol->setContentsMargins(0, 12, 0, 0);
    exCol->setSpacing(6);
    tunExcludesTitle_ = new QLabel(tunCard);
    tunExcludesTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 600;"));
    tunExcludesSub_ = new QLabel(tunCard);
    tunExcludesSub_->setStyleSheet(QStringLiteral("color: #7c8497; font-size: 11px;"));
    tunExcludesEdit_ = new QPlainTextEdit(tunCard);
    tunExcludesEdit_->setFixedHeight(92);
    tunExcludesEdit_->setStyleSheet(QStringLiteral(
        "QPlainTextEdit { background: #101218; border: 1px solid #262a36; border-radius: 8px;"
        "  padding: 8px; color: #e5e7eb; font-family: Consolas, monospace; font-size: 12px; }"));
    exCol->addWidget(tunExcludesTitle_);
    exCol->addWidget(tunExcludesSub_);
    exCol->addWidget(tunExcludesEdit_);
    tuLay->addLayout(exCol);
    tLay->addWidget(tunCard);
    tLay->addStretch(1);
    subStack_->addWidget(tScroll);

    // -----------------------------------------------------------------------
    // Sub-tab 2: Global Hotkeys & Auto-Suspend Networks
    // -----------------------------------------------------------------------
    QWidget* hContent = nullptr;
    QVBoxLayout* hLay = nullptr;
    QScrollArea* hScroll = wrapInTransparentScroll(subStack_, hContent, hLay);

    auto* hkCard = createCardFrame(hContent);
    auto* hkLay = new QVBoxLayout(hkCard);
    hkLay->setContentsMargins(18, 14, 18, 10);
    hkLay->setSpacing(0);
    hkCardTitle_ = new QLabel(hkCard);
    hkCardTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700; margin-bottom: 6px;"));
    hkLay->addWidget(hkCardTitle_);

    hkProxyEdit_ = new QLineEdit(hkCard);
    hkProxyEdit_->setClearButtonEnabled(true);
    hkProxyEdit_->setMinimumWidth(220);
    hkProxyEdit_->setPlaceholderText(QStringLiteral("Ctrl+Alt+P"));
    addSettingsRow(hkLay, hkCard, hkProxyTitle_, hkProxySub_, hkProxyEdit_, false);

    hkModeEdit_ = new QLineEdit(hkCard);
    hkModeEdit_->setClearButtonEnabled(true);
    hkModeEdit_->setMinimumWidth(220);
    hkModeEdit_->setPlaceholderText(QStringLiteral("Ctrl+Alt+M"));
    addSettingsRow(hkLay, hkCard, hkModeTitle_, hkModeSub_, hkModeEdit_, true);
    hLay->addWidget(hkCard);

    auto* susCard = createCardFrame(hContent);
    auto* suLay = new QVBoxLayout(susCard);
    suLay->setContentsMargins(18, 14, 18, 14);
    suLay->setSpacing(10);
    suspendCardTitle_ = new QLabel(susCard);
    suspendCardTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 700;"));
    suLay->addWidget(suspendCardTitle_);

    auto* netBar = new QFrame(susCard);
    netBar->setObjectName(QStringLiteral("SettingsNetBar"));
    netBar->setStyleSheet(QStringLiteral(
        "QFrame#SettingsNetBar { background: #13151e; border: 1px solid #262a36; border-radius: 8px; }"));
    auto* nbLay = new QHBoxLayout(netBar);
    nbLay->setContentsMargins(12, 10, 12, 10);
    nbLay->setSpacing(8);
    auto* nbTxt = new QVBoxLayout();
    nbTxt->setSpacing(2);
    activeNetTitle_ = new QLabel(netBar);
    activeNetTitle_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 11px; font-weight: 600;"));
    activeNetInfo_ = new QLabel(QStringLiteral("—"), netBar);
    activeNetInfo_->setWordWrap(true);
    activeNetInfo_->setStyleSheet(QStringLiteral("color: #e5e7eb; font-size: 12px; font-family: Consolas, monospace;"));
    nbTxt->addWidget(activeNetTitle_);
    nbTxt->addWidget(activeNetInfo_);
    nbLay->addLayout(nbTxt, 1);

    addNetProfileBtn_ = new QPushButton(netBar);
    addNetGwBtn_ = new QPushButton(netBar);
    nbLay->addWidget(addNetProfileBtn_);
    nbLay->addWidget(addNetGwBtn_);
    suLay->addWidget(netBar);

    suspendRulesTitle_ = new QLabel(susCard);
    suspendRulesTitle_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 13px; font-weight: 600;"));
    suspendRulesSub_ = new QLabel(susCard);
    suspendRulesSub_->setStyleSheet(QStringLiteral("color: #7c8497; font-size: 11px;"));
    suspendRulesEdit_ = new QPlainTextEdit(susCard);
    suspendRulesEdit_->setFixedHeight(120);
    suspendRulesEdit_->setStyleSheet(QStringLiteral(
        "QPlainTextEdit { background: #101218; border: 1px solid #262a36; border-radius: 8px;"
        "  padding: 8px; color: #e5e7eb; font-family: Consolas, monospace; font-size: 12px; }"));
    suLay->addWidget(suspendRulesTitle_);
    suLay->addWidget(suspendRulesSub_);
    suLay->addWidget(suspendRulesEdit_);
    hLay->addWidget(susCard);
    hLay->addStretch(1);
    subStack_->addWidget(hScroll);

    // Dirty tracking across controls
    auto markDirty = [this]() {
        if (!refreshing_) {
            setDirty(true);
        }
    };
    for (QSpinBox* sp : {portOverrideSpin_, apiPortSpin_, tunMtuSpin_}) {
        connect(sp, &QSpinBox::valueChanged, this, [markDirty](int) { markDirty(); });
    }
    for (QLineEdit* le : {apiSecretEdit_, benchUrlEdit_, bypassListEdit_, customDirEdit_, tunIfaceEdit_, hkProxyEdit_,
                          hkModeEdit_}) {
        connect(le, &QLineEdit::textEdited, this, [markDirty](const QString&) { markDirty(); });
    }
    for (ToggleSwitch* sw : {allowLanSwitch_, ipv6Switch_, launchSwitch_, bypassCnAppsSwitch_, customAsIsSwitch_}) {
        connect(sw, &ToggleSwitch::toggled, this, [markDirty](bool) { markDirty(); });
    }
    for (QComboBox* cb : {langCombo_, trayCombo_, logLevelCombo_, tunStackCombo_}) {
        connect(cb, &QComboBox::currentIndexChanged, this, [markDirty](int) { markDirty(); });
    }
    connect(tunExcludesEdit_, &QPlainTextEdit::textChanged, this, [markDirty]() { markDirty(); });
    connect(suspendRulesEdit_, &QPlainTextEdit::textChanged, this, [markDirty]() { markDirty(); });

    connect(tabSwitch_, &SegmentedControl::currentIndexChanged, this, [this](int idx) {
        subStack_->setCurrentIndex(idx);
    });
    connect(saveBtn_, &QPushButton::clicked, this, [this]() { saveSettings(false); });
    connect(browseDirBtn_, &QPushButton::clicked, this, [this]() {
        const QString dir = QFileDialog::getExistingDirectory(window(), uiText("选择自定义配置目录", "Choose Config Dir"));
        if (!dir.isEmpty()) {
            customDirEdit_->setText(dir);
            setDirty(true);
        }
    });
    connect(openCfgDirBtn_, &QPushButton::clicked, this, [this]() {
        if (!configDir_.isEmpty()) {
            QDesktopServices::openUrl(QUrl::fromLocalFile(configDir_));
        }
    });
    connect(openLogDirBtn_, &QPushButton::clicked, this, [this]() {
        if (!logsDir_.isEmpty()) {
            QDesktopServices::openUrl(QUrl::fromLocalFile(logsDir_));
        }
    });

    auto appendSuspendTokens = [this](const QStringList& tokens) {
        QStringList existing =
            suspendRulesEdit_->toPlainText().split(QLatin1Char('\n'), Qt::SkipEmptyParts);
        for (QString& s : existing) {
            s = s.trimmed();
        }
        bool added = false;
        for (const QString& tok : tokens) {
            const QString t = tok.trimmed();
            if (!t.isEmpty() && !existing.contains(t, Qt::CaseInsensitive)) {
                existing.push_back(t);
                added = true;
            }
        }
        if (added) {
            suspendRulesEdit_->setPlainText(existing.join(QLatin1Char('\n')));
            setDirty(true);
            ToastOverlay::show(window(), uiText("已添加到自动暂停规则列表", "Added to auto-suspend list"),
                               PillTone::Blue);
        }
    };
    connect(addNetProfileBtn_, &QPushButton::clicked, this, [this, appendSuspendTokens]() {
        appendSuspendTokens(curNetProfiles_);
    });
    connect(addNetGwBtn_, &QPushButton::clicked, this, [this, appendSuspendTokens]() {
        appendSuspendTokens(curNetGateways_);
    });
    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });

    retranslateUi();
    if (client_) {
        updateSnapshot(client_->snapshot());
    }
}

void SettingsPage::setDirty(bool dirty) {
    dirty_ = dirty;
    if (dirty_) {
        dirtyPill_->setPill(uiText("有未保存修改", "Unsaved Changes"), PillTone::Amber);
    } else {
        dirtyPill_->setPill(QString(), PillTone::Muted);
    }
}

void SettingsPage::retranslateUi() {
    titleLabel_->setText(uiText("偏好设置", "Preferences"));
    subtitleLabel_->setText(uiText(
        "清晰的分组卡片与现代滑动开关，替代原先拥挤的表单弹窗",
        "Grouped preference cards with modern switches cleanly replace fragmented dialogs"));
    tabSwitch_->setItemText(0, uiText("通用与端口", "General & Ports"));
    tabSwitch_->setItemText(1, uiText("增强模式 (TUN)", "Enhanced TUN"));
    tabSwitch_->setItemText(2, uiText("快捷键与网络自暂停", "Hotkeys & Auto-Suspend"));
    saveBtn_->setText(uiText("保存并应用", "Save & Apply"));

    cardPortsTitle_->setText(uiText("端口与内核监听", "Ports & Core Listeners"));
    portOverrideTitle_->setText(uiText("混合代理端口覆写 (HTTP + SOCKS5)", "Mixed Proxy Port Override"));
    portOverrideSub_->setText(uiText("设为 0 时自动跟随当前订阅配置文件中的 mixed-port / port",
                                     "Set 0 to follow the active profile mixed-port / port automatically"));
    apiPortTitle_->setText(uiText("外部控制 API 端口 (External Controller)", "External Controller API Port"));
    apiPortSub_->setText(uiText("供嵌入式 MetaCubeXD 面板与本地规则热更新调用（仅监听 127.0.0.1）",
                                "Used by embedded MetaCubeXD and live rule hot-reload (bound to 127.0.0.1)"));
    apiSecretTitle_->setText(uiText("外部控制 API 密钥 (Secret)", "External Controller Secret"));
    apiSecretSub_->setText(uiText("留空则仅允许本地无鉴权访问，修改后需重新载入面板",
                                  "Optional bearer token for localhost REST API calls"));
    allowLanTitle_->setText(uiText("允许局域网设备连接 (Allow LAN)", "Allow LAN Connections"));
    allowLanSub_->setText(uiText("开启后同一局域网手机 / 其他电脑可通过本机 IP 使用代理端口",
                                 "Expose mixed HTTP/SOCKS5 port to devices on your local network"));
    ipv6Title_->setText(uiText("启用 IPv6 流量支持", "Enable IPv6"));
    ipv6Sub_->setText(uiText("允许内核解析和转发 IPv6 AAAA 域名与地址",
                             "Allow Mihomo core to handle IPv6 routes and DNS responses"));

    cardSystemTitle_->setText(uiText("系统启动与分流体验", "System Startup & Routing UX"));
    launchTitle_->setText(uiText("开机自动后台静默启动", "Launch Silently at Windows Login"));
    launchSub_->setText(uiText("登录 Windows 时自动驻留系统托盘并恢复上次代理状态",
                               "Start daemon and tray icon cleanly on login and restore last state"));
    bypassCnAppsTitle_->setText(uiText("绕过常用国内客户端进程 (微信 / QQ / 飞书 / 钉钉 / WPS / 百度网盘)",
                                       "Bypass Common Chinese Desktop Apps (WeChat / QQ / Feishu / WPS)"));
    bypassCnAppsSub_->setText(uiText("在增强模式 (TUN) 下让国内桌面软件走本地直连，避免异地登录风控与语音卡顿",
                                     "Keep Chinese desktop clients direct under TUN mode to prevent false security alerts"));
    customAsIsTitle_->setText(uiText("自定义配置原样运行 (Custom Config As-Is)", "Run Custom Config As-Is"));
    customAsIsSub_->setText(uiText("保持手工编写配置的原始路由与 DNS（AI 专锁启用时仍受防漏规则保护）",
                                   "Keep custom YAML routing/DNS untouched except active AI Lock guards"));
    uiLangTitle_->setText(uiText("界面语言 / 托盘样式 / 日志级别", "Language / Tray Style / Log Level"));
    uiLangSub_->setText(uiText("即时切换双语界面、系统托盘风格与内核日志详细度",
                               "Switch bilingual UI, tray icon preset and core log verbosity"));

    {
        QSignalBlocker sb(trayCombo_);
        const QString curTray = trayCombo_->currentData().toString();
        trayCombo_->clear();
        trayCombo_->addItem(uiText("默认彩标", "Default Accent"), QStringLiteral("default"));
        trayCombo_->addItem(uiText("单色极简", "Monochrome"), QStringLiteral("monochrome"));
        trayCombo_->addItem(uiText("闪电徽章", "Bolt Badge"), QStringLiteral("bolt"));
        const int idx = trayCombo_->findData(curTray.isEmpty() ? QStringLiteral("default") : curTray);
        trayCombo_->setCurrentIndex(idx >= 0 ? idx : 0);
    }

    benchUrlTitle_->setText(uiText("节点延迟测速基准 URL", "Latency Benchmark URL"));
    benchUrlSub_->setText(uiText("用于一键延迟测速与 204 连通性守护探测",
                                 "Used for latency probes and 204 connectivity checks"));
    bypassListTitle_->setText(uiText("Windows 系统代理绕过列表 (Proxy Bypass)", "Windows System Proxy Bypass List"));
    bypassListSub_->setText(uiText("分号分隔的内网域名与网段，不经系统 HTTP 代理",
                                   "Semicolon-separated hosts/CIDRs bypassing Windows system proxy"));
    customDirTitle_->setText(uiText("自定义配置目录", "Custom Config Directory"));
    customDirSub_->setText(uiText("留空使用默认 %APPDATA%\\ClashFX\\configs",
                                  "Leave blank to use default %APPDATA%\\ClashFX\\configs"));
    browseDirBtn_->setText(uiText("浏览...", "Browse..."));

    tunServiceTitle_->setText(uiText("ClashFXService 系统级虚拟网卡服务", "ClashFXService System Service"));
    tunServiceSub_->setText(uiText("一次安装即可零 UAC 弹窗静默开关增强模式 (TUN) 与 AI 专用锁定",
                                   "Enables zero-prompt TUN toggling and Fail-Closed AI Proxy Lock"));
    openCfgDirBtn_->setText(uiText("打开配置目录", "Open Config Dir"));
    openLogDirBtn_->setText(uiText("打开日志目录", "OpenLogs Dir"));
    tunCardTitle_->setText(uiText("Wintun 虚拟网卡与路由参数", "Wintun Adapter & Route Exclusions"));
    tunStackTitle_->setText(uiText("TUN 协议栈 (Stack)", "TUN Network Stack"));
    tunStackSub_->setText(uiText("推荐 mixed 兼顾兼容与性能，遇到驱动异常可切至 system 或 gvisor",
                                 "Recommended: mixed for balanced TCP/UDP forwarding"));
    tunMtuTitle_->setText(uiText("TUN 虚拟网卡 MTU", "TUN Adapter MTU"));
    tunMtuSub_->setText(uiText("标准以太网环境下推荐 1500（范围 1280 – 9000）",
                               "Standard MTU is 1500 (valid range 1280 – 9000)"));
    tunIfaceTitle_->setText(uiText("虚拟网卡接口名", "Interface Name"));
    tunIfaceSub_->setText(uiText("留空默认为 Mihomo，创建后显示于网络连接中",
                                 "Defaults to Mihomo when left blank"));
    tunExcludesTitle_->setText(uiText("TUN 排除直连网段 (Route Excludes)", "TUN Route Exclude CIDRs"));
    tunExcludesSub_->setText(uiText("以下 CIDR 网段流量不进入 TUN 虚拟网卡（逗号或换行分隔）",
                                    "CIDRs excluded from TUN routing table (comma or newline separated)"));

    hkCardTitle_->setText(uiText("Windows 全局快捷键", "Windows Global Hotkeys"));
    hkProxyTitle_->setText(uiText("一键开关系统代理", "Toggle System Proxy Hotkey"));
    hkProxySub_->setText(uiText("例如 Ctrl+Alt+P（留空禁用该快捷键）", "Example: Ctrl+Alt+P (clear to disable)"));
    hkModeTitle_->setText(uiText("循环切换出站模式 (Rule / Global / Direct)", "Cycle Outbound Mode Hotkey"));
    hkModeSub_->setText(uiText("例如 Ctrl+Alt+M（留空禁用该快捷键）", "Example: Ctrl+Alt+M (clear to disable)"));

    suspendCardTitle_->setText(uiText("可信网络自动暂停代理 (Auto-Suspend)", "Trusted Network Auto-Suspend"));
    activeNetTitle_->setText(uiText("当前检测到的活跃网络签名", "Currently Detected Network Signature"));
    addNetProfileBtn_->setText(uiText("+ 加入网络名称", "+ Add Network"));
    addNetGwBtn_->setText(uiText("+ 加入当前网关", "+ Add Gateway"));
    suspendRulesTitle_->setText(uiText("命中即自动暂停代理的规则列表（每行一条）", "Auto-Suspend Match Rules (One Per Line)"));
    suspendRulesSub_->setText(uiText("支持填写公司内网 Wi-Fi 名称、Windows 网络配置文件名、网关 IP 或 DNS 后缀",
                                     "Matches Windows network profile name, DNS suffix, gateway IP, or interface"));

    setDirty(dirty_);
    updateSnapshot(lastSnapshot_);
}

void SettingsPage::updateSnapshot(const QJsonObject& snapshot) {
    lastSnapshot_ = snapshot;
    configDir_ = snapshot.value(QStringLiteral("config_dir")).toString();
    logsDir_ = snapshot.value(QStringLiteral("logs_dir")).toString();

    const bool svcInstalled = snapshot.value(QStringLiteral("service_installed")).toBool(false);
    if (svcInstalled) {
        servicePill_->setPill(uiText("ClashFXService 系统服务已就绪", "ClashFXService Ready"), PillTone::Green);
    } else {
        servicePill_->setPill(uiText("ClashFXService 按需安装", "Service On-Demand"), PillTone::Muted);
    }

    const int mixedPort = snapshot.value(QStringLiteral("runtime")).toObject().value(QStringLiteral("mixed_port")).toInt(7890);
    portOverrideSpin_->setSpecialValueText(
        uiText("0 (跟随配置文件 :", "0 (Follow Profile :") + QString::number(mixedPort > 0 ? mixedPort : 7890) + QStringLiteral(")"));

    updateActiveNetworkBanner(snapshot);
    saveBtn_->setEnabled(client_ && client_->isConnected());

    if (dirty_) {
        return;
    }

    const QJsonObject s = snapshot.value(QStringLiteral("settings")).toObject();
    refreshing_ = true;

    portOverrideSpin_->setValue(s.value(QStringLiteral("port_override")).toInt(0));
    apiPortSpin_->setValue(s.value(QStringLiteral("api_port")).toInt(19090));
    apiSecretEdit_->setText(s.value(QStringLiteral("api_secret")).toString());
    allowLanSwitch_->setCheckedSilent(s.value(QStringLiteral("allow_lan")).toBool(false));
    ipv6Switch_->setCheckedSilent(s.value(QStringLiteral("ipv6")).toBool(false));

    launchSwitch_->setCheckedSilent(s.value(QStringLiteral("launch_at_login")).toBool(false));
    bypassCnAppsSwitch_->setCheckedSilent(s.value(QStringLiteral("bypass_cn_apps")).toBool(true));
    customAsIsSwitch_->setCheckedSilent(s.value(QStringLiteral("custom_config_as_is")).toBool(false));

    const int lIdx = langCombo_->findData(s.value(QStringLiteral("language")).toString(QStringLiteral("zh_CN")));
    if (lIdx >= 0) {
        langCombo_->setCurrentIndex(lIdx);
    }
    const int tIdx = trayCombo_->findData(s.value(QStringLiteral("tray_icon_preset")).toString(QStringLiteral("default")));
    if (tIdx >= 0) {
        trayCombo_->setCurrentIndex(tIdx);
    }
    logLevelCombo_->setCurrentText(s.value(QStringLiteral("log_level")).toString(QStringLiteral("info")));

    benchUrlEdit_->setText(s.value(QStringLiteral("benchmark_url")).toString());
    bypassListEdit_->setText(s.value(QStringLiteral("proxy_bypass_list")).toString());
    customDirEdit_->setText(s.value(QStringLiteral("custom_config_dir")).toString());

    tunStackCombo_->setCurrentText(s.value(QStringLiteral("tun_stack")).toString(QStringLiteral("mixed")));
    tunMtuSpin_->setValue(s.value(QStringLiteral("tun_mtu")).toInt(1500));
    tunIfaceEdit_->setText(s.value(QStringLiteral("tun_interface_name")).toString());
    tunExcludesEdit_->setPlainText(s.value(QStringLiteral("tun_route_excludes")).toString());

    const QJsonObject hk = s.value(QStringLiteral("hotkeys")).toObject();
    hkProxyEdit_->setText(hk.value(QStringLiteral("toggle_system_proxy")).toString());
    hkModeEdit_->setText(hk.value(QStringLiteral("switch_outbound_mode")).toString());

    QStringList susList;
    for (const QJsonValue& v : s.value(QStringLiteral("suspend_networks")).toArray()) {
        const QString str = v.toString().trimmed();
        if (!str.isEmpty()) {
            susList.push_back(str);
        }
    }
    suspendRulesEdit_->setPlainText(susList.join(QLatin1Char('\n')));

    refreshing_ = false;
    setDirty(false);
}

void SettingsPage::updateActiveNetworkBanner(const QJsonObject& snapshot) {
    const QJsonObject net = snapshot.value(QStringLiteral("active_network")).toObject();
    curNetProfiles_.clear();
    curNetGateways_.clear();
    QStringList dnsList;

    for (const QJsonValue& v : net.value(QStringLiteral("profile_names")).toArray()) {
        curNetProfiles_.push_back(v.toString());
    }
    for (const QJsonValue& v : net.value(QStringLiteral("gateways")).toArray()) {
        curNetGateways_.push_back(v.toString());
    }
    for (const QJsonValue& v : net.value(QStringLiteral("dns_suffixes")).toArray()) {
        dnsList.push_back(v.toString());
    }

    QStringList summaryParts;
    if (!curNetProfiles_.isEmpty()) {
        summaryParts << (uiText("网络: ", "Profile: ") + curNetProfiles_.join(QStringLiteral(", ")));
    }
    if (!curNetGateways_.isEmpty()) {
        summaryParts << (uiText("网关: ", "GW: ") + curNetGateways_.join(QStringLiteral(", ")));
    }
    if (!dnsList.isEmpty()) {
        summaryParts << (QStringLiteral("DNS: ") + dnsList.join(QStringLiteral(", ")));
    }

    activeNetInfo_->setText(summaryParts.isEmpty() ? uiText("未识别活跃网络接口", "No active network detected")
                                                   : summaryParts.join(QStringLiteral("  |  ")));
    addNetProfileBtn_->setEnabled(!curNetProfiles_.isEmpty());
    addNetGwBtn_->setEnabled(!curNetGateways_.isEmpty());
}

void SettingsPage::saveSettings(bool allowUac) {
    if (!client_) {
        return;
    }

    // Merge edited fields onto current snapshot settings so mode/profile/AI Lock keys stay intact
    QJsonObject merged = lastSnapshot_.value(QStringLiteral("settings")).toObject();
    merged.insert(QStringLiteral("port_override"), portOverrideSpin_->value());
    merged.insert(QStringLiteral("api_port"), apiPortSpin_->value());
    merged.insert(QStringLiteral("api_secret"), apiSecretEdit_->text());
    merged.insert(QStringLiteral("allow_lan"), allowLanSwitch_->isChecked());
    merged.insert(QStringLiteral("ipv6"), ipv6Switch_->isChecked());

    merged.insert(QStringLiteral("launch_at_login"), launchSwitch_->isChecked());
    merged.insert(QStringLiteral("bypass_cn_apps"), bypassCnAppsSwitch_->isChecked());
    merged.insert(QStringLiteral("custom_config_as_is"), customAsIsSwitch_->isChecked());
    merged.insert(QStringLiteral("language"), langCombo_->currentData().toString());
    merged.insert(QStringLiteral("tray_icon_preset"), trayCombo_->currentData().toString());
    merged.insert(QStringLiteral("log_level"), logLevelCombo_->currentText());
    merged.insert(QStringLiteral("benchmark_url"), benchUrlEdit_->text().trimmed());
    merged.insert(QStringLiteral("proxy_bypass_list"), bypassListEdit_->text().trimmed());
    merged.insert(QStringLiteral("custom_config_dir"), customDirEdit_->text().trimmed());

    merged.insert(QStringLiteral("tun_stack"), tunStackCombo_->currentText());
    merged.insert(QStringLiteral("tun_mtu"), tunMtuSpin_->value());
    merged.insert(QStringLiteral("tun_interface_name"), tunIfaceEdit_->text().trimmed());
    merged.insert(QStringLiteral("tun_route_excludes"), tunExcludesEdit_->toPlainText().trimmed());

    QJsonObject hk;
    hk.insert(QStringLiteral("toggle_system_proxy"), hkProxyEdit_->text().trimmed());
    hk.insert(QStringLiteral("switch_outbound_mode"), hkModeEdit_->text().trimmed());
    merged.insert(QStringLiteral("hotkeys"), hk);

    QJsonArray susArr;
    for (const QString& line : suspendRulesEdit_->toPlainText().split(QLatin1Char('\n'), Qt::SkipEmptyParts)) {
        const QString t = line.trimmed();
        if (!t.isEmpty()) {
            susArr.append(t);
        }
    }
    merged.insert(QStringLiteral("suspend_networks"), susArr);

    QJsonObject params;
    params.insert(QStringLiteral("settings"), merged);
    if (allowUac) {
        params.insert(QStringLiteral("allow_uac"), true);
    }

    saveBtn_->setEnabled(false);
    client_->call(QStringLiteral("update_settings"), params,
                  [this, allowUac, lang = merged.value(QStringLiteral("language")).toString()](
                      bool ok, const QJsonValue& data, const QString& err, const QString& errCode) {
                      saveBtn_->setEnabled(client_ && client_->isConnected());
                      if (!ok) {
                          if (errCode == QLatin1String("SERVICE_INSTALL_REQUIRED") && !allowUac) {
                              const auto ans = QMessageBox::question(
                                  window(), uiText("需要安装系统服务", "Service Required"),
                                  uiText("应用当前增强模式配置需要安装 ClashFXService。是否通过 UAC 安装后重试？",
                                         "Applying TUN settings requires ClashFXService. Install via UAC and retry?"));
                              if (ans == QMessageBox::Yes) {
                                  saveSettings(true);
                              }
                              return;
                          }
                          QMessageBox::warning(window(), uiText("保存设置失败", "Save Settings Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      setDirty(false);
                      I18n::instance().setLanguage(lang);
                      if (data.isObject()) {
                          updateSnapshot(data.toObject());
                      }
                      ToastOverlay::show(window(), uiText("偏好设置已保存并立即应用", "Preferences saved & applied"),
                                         PillTone::Green);
                  });
}

}  // namespace clashfx
