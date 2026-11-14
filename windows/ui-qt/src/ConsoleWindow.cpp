#include "ConsoleWindow.h"

#include "AiLockAndNodeDialogs.h"
#include "DaemonClient.h"
#include "I18n.h"
#include "ModernWidgets.h"
#include "RuleEditorDialog.h"
#include "SubscriptionsAndSettingsDialogs.h"
#include "TrayIconGenerator.h"
#include "WebView2Widget.h"

#include <QApplication>
#include <QClipboard>
#include <QCloseEvent>
#include <QComboBox>
#include <QDateTime>
#include <QDesktopServices>
#include <QFrame>
#include <QHBoxLayout>
#include <QHideEvent>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLabel>
#include <QMessageBox>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QPainter>
#include <QPixmap>
#include <QProgressBar>
#include <QPushButton>
#include <QShowEvent>
#include <QSignalBlocker>
#include <QStackedWidget>
#include <QUrl>
#include <QVBoxLayout>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#endif

namespace clashfx {

namespace {

inline QString uiText(const char* zh, const char* en) {
    return I18n::instance().isChinese() ? QString::fromUtf8(zh) : QString::fromUtf8(en);
}

QString formatQuotaBytes(double bytes) {
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

}  // namespace

ConsoleWindow::ConsoleWindow(DaemonClient* client, QWidget* parent)
    : QMainWindow(parent)
    , client_(client)
    , netMgr_(new QNetworkAccessManager(this)) {
    setObjectName(QStringLiteral("ClashConsoleWindow"));
    setWindowIcon(TrayIconGenerator::appIcon());
    resize(1340, 820);
    setMinimumSize(1120, 680);

    auto* central = new QWidget(this);
    central->setObjectName(QStringLiteral("ClashCentral"));
    central->setStyleSheet(QStringLiteral("QWidget#ClashCentral { background: #14151a; }"));
    auto* rootLay = new QHBoxLayout(central);
    rootLay->setContentsMargins(0, 0, 0, 0);
    rootLay->setSpacing(0);

    rootLay->addWidget(buildSidebar(central));

    rightStack_ = new QStackedWidget(central);
    rightStack_->setStyleSheet(QStringLiteral("QStackedWidget { background: #14151a; }"));

    rightStack_->addWidget(buildDashboardPage(rightStack_));            // 0: Dashboard
    subsPage_ = new SubscriptionsPage(client_, rightStack_);
    rightStack_->addWidget(subsPage_);                                  // 1: Subscriptions
    rulesPage_ = new RuleEditorPage(client_, rightStack_);
    rightStack_->addWidget(rulesPage_);                                 // 2: Rules
    aiLockPage_ = new AiLockPage(client_, rightStack_);
    rightStack_->addWidget(aiLockPage_);                                // 3: AiLock
    multiNodePage_ = new MultiNodePage(client_, rightStack_);
    rightStack_->addWidget(multiNodePage_);                             // 4: MultiNode
    diagPage_ = new NodeDiagnosticPage(client_, rightStack_);
    rightStack_->addWidget(diagPage_);                                  // 5: Diagnostics
    settingsPage_ = new SettingsPage(client_, rightStack_);
    rightStack_->addWidget(settingsPage_);                              // 6: Settings

    rootLay->addWidget(rightStack_, 1);
    setCentralWidget(central);

    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString&) { retranslateUi(); });

    retranslateUi();
    navigateTo(ConsolePage::Dashboard);
    applySnapshot(client_ && client_->isConnected(), client_ ? client_->snapshot() : QJsonObject());
}

QWidget* ConsoleWindow::buildSidebar(QWidget* parent) {
    sidebar_ = new QFrame(parent);
    sidebar_->setFixedWidth(256);
    sidebar_->setObjectName(QStringLiteral("ClashSidebar"));
    sidebar_->setStyleSheet(QStringLiteral(
        "QFrame#ClashSidebar {"
        "  background: #101014;"
        "  border-right: 1px solid #222530;"
        "}"));

    auto* lay = new QVBoxLayout(sidebar_);
    lay->setContentsMargins(14, 16, 14, 14);
    lay->setSpacing(12);

    // 1. Brand Header Row
    auto* brandRow = new QHBoxLayout();
    brandRow->setSpacing(10);

    brandIconLabel_ = new QLabel(sidebar_);
    brandIconLabel_->setFixedSize(36, 36);
    brandIconLabel_->setAlignment(Qt::AlignCenter);
    brandIconLabel_->setStyleSheet(QStringLiteral(
        "background: qlineargradient(x1:0, y1:0, x2:1, y2:1, stop:0 #2563eb, stop:1 #0ea5e9);"
        "border-radius: 10px;"));
    brandIconLabel_->setPixmap(renderVectorIcon(VectorIcon::BrandBolt, 20, Qt::white, devicePixelRatioF()));

    auto* brandTextCol = new QVBoxLayout();
    brandTextCol->setSpacing(1);
    auto* brandNameLbl = new QLabel(QStringLiteral("ClashFX"), sidebar_);
    brandNameLbl->setStyleSheet(QStringLiteral("color: #f9fafb; font-size: 16px; font-weight: 700;"));
    brandSubLabel_ = new QLabel(QStringLiteral("● Mihomo"), sidebar_);
    brandSubLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 11px; font-weight: 600;"));
    brandTextCol->addWidget(brandNameLbl);
    brandTextCol->addWidget(brandSubLabel_);

    sidebarAiPill_ = new BadgePill(sidebar_, true);
    brandRow->addWidget(brandIconLabel_);
    brandRow->addLayout(brandTextCol, 1);
    brandRow->addWidget(sidebarAiPill_, 0, Qt::AlignTop);
    lay->addLayout(brandRow);

    // 2. Outbound Mode 3-Way Segmented Switcher
    modeSwitch_ = new SegmentedControl(true, sidebar_);
    modeSwitch_->setCompact(true);
    modeSwitch_->setItems({QStringLiteral("Rule"), QStringLiteral("Global"), QStringLiteral("Direct")});
    connect(modeSwitch_, &SegmentedControl::itemClicked, this, &ConsoleWindow::switchOutboundMode);
    lay->addWidget(modeSwitch_);

    // 3. System Proxy & Enhanced TUN Quick Cards
    auto* quickRow = new QHBoxLayout();
    quickRow->setSpacing(8);
    sysProxyCard_ = new SidebarQuickCard(VectorIcon::Globe, ToggleSwitch::Accent::Blue, sidebar_);
    tunCard_ = new SidebarQuickCard(VectorIcon::Zap, ToggleSwitch::Accent::Emerald, sidebar_);
    quickRow->addWidget(sysProxyCard_, 1);
    quickRow->addWidget(tunCard_, 1);
    lay->addLayout(quickRow);

    connect(sysProxyCard_, &SidebarQuickCard::toggleRequested, this, &ConsoleWindow::toggleSystemProxy);
    connect(tunCard_, &SidebarQuickCard::toggleRequested, this, [this](bool next) { toggleEnhancedMode(next, false); });

    // 4. Active Profile & Subscription Quota Card
    auto* profCard = new QFrame(sidebar_);
    profCard->setObjectName(QStringLiteral("SidebarProfileCard"));
    profCard->setStyleSheet(QStringLiteral(
        "QFrame#SidebarProfileCard {"
        "  background: #151721;"
        "  border: 1px solid #242836;"
        "  border-radius: 10px;"
        "}"));
    auto* profLay = new QVBoxLayout(profCard);
    profLay->setContentsMargins(10, 9, 10, 9);
    profLay->setSpacing(7);

    auto* profTop = new QHBoxLayout();
    profTop->setSpacing(6);
    profileCombo_ = new QComboBox(profCard);
    profileCombo_->setStyleSheet(QStringLiteral(
        "QComboBox { background: transparent; border: none; color: #f3f4f6; font-size: 12px; font-weight: 700;"
        "  padding: 2px 4px; }"
        "QComboBox::drop-down { border: none; width: 16px; }"));
    profileActivePill_ = new BadgePill(profCard, true);
    profileRefreshBtn_ = new QPushButton(profCard);
    profileRefreshBtn_->setFixedSize(24, 24);
    profileRefreshBtn_->setCursor(Qt::PointingHandCursor);
    profileRefreshBtn_->setIcon(QIcon(renderVectorIcon(VectorIcon::Refresh, 14, QColor(QStringLiteral("#9ca3af")),
                                                       devicePixelRatioF())));
    profileRefreshBtn_->setStyleSheet(QStringLiteral(
        "QPushButton { background: transparent; border: 1px solid transparent; border-radius: 6px; padding: 0; }"
        "QPushButton:hover { background: #212534; border-color: #313649; }"));

    profTop->addWidget(profileCombo_, 1);
    profTop->addWidget(profileActivePill_);
    profTop->addWidget(profileRefreshBtn_);
    profLay->addLayout(profTop);

    quotaBar_ = new QProgressBar(profCard);
    quotaBar_->setRange(0, 100);
    quotaBar_->setValue(0);
    quotaBar_->setTextVisible(false);
    quotaBar_->setFixedHeight(4);
    quotaBar_->setStyleSheet(QStringLiteral(
        "QProgressBar { background: #232635; border: none; border-radius: 2px; }"
        "QProgressBar::chunk { background: qlineargradient(x1:0, y1:0, x2:1, y2:0, stop:0 #3b82f6, stop:1 #10b981);"
        "  border-radius: 2px; }"));
    profLay->addWidget(quotaBar_);

    quotaTextLabel_ = new QLabel(profCard);
    quotaTextLabel_->setStyleSheet(QStringLiteral("color: #8b93a6; font-size: 10px;"));
    profLay->addWidget(quotaTextLabel_);
    lay->addWidget(profCard);

    connect(profileCombo_, &QComboBox::activated, this, [this](int idx) {
        if (!updatingUi_ && idx >= 0) {
            selectActiveProfile(profileCombo_->itemText(idx));
        }
    });
    connect(profileRefreshBtn_, &QPushButton::clicked, this, &ConsoleWindow::refreshActiveProfile);

    // 5. Navigation Items
    navSectionLabel_ = new QLabel(sidebar_);
    navSectionLabel_->setStyleSheet(
        QStringLiteral("color: #6b7280; font-size: 11px; font-weight: 600; padding: 4px 4px 0 4px;"));
    lay->addWidget(navSectionLabel_);

    auto* navCol = new QVBoxLayout();
    navCol->setSpacing(4);
    navDashboard_ = new SidebarNavButton(VectorIcon::Dashboard, sidebar_);
    navSubscriptions_ = new SidebarNavButton(VectorIcon::Subscriptions, sidebar_);
    navRules_ = new SidebarNavButton(VectorIcon::Rules, sidebar_);
    navAiLock_ = new SidebarNavButton(VectorIcon::AiLock, sidebar_);
    navMultiNode_ = new SidebarNavButton(VectorIcon::MultiNode, sidebar_);
    navDiagnostics_ = new SidebarNavButton(VectorIcon::Diagnostics, sidebar_);
    navSettings_ = new SidebarNavButton(VectorIcon::Settings, sidebar_);

    for (SidebarNavButton* btn : {navDashboard_, navSubscriptions_, navRules_, navAiLock_, navMultiNode_, navDiagnostics_,
                                  navSettings_}) {
        navCol->addWidget(btn);
    }
    lay->addLayout(navCol);

    connect(navDashboard_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::Dashboard); });
    connect(navSubscriptions_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::Subscriptions); });
    connect(navRules_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::Rules); });
    connect(navAiLock_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::AiLock); });
    connect(navMultiNode_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::MultiNode); });
    connect(navDiagnostics_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::Diagnostics); });
    connect(navSettings_, &QPushButton::clicked, this, [this]() { navigateTo(ConsolePage::Settings); });

    lay->addStretch(1);

    // 6. Live Traffic & Port Telemetry Box
    auto* trafficCard = new QFrame(sidebar_);
    trafficCard->setObjectName(QStringLiteral("SidebarTrafficCard"));
    trafficCard->setStyleSheet(QStringLiteral(
        "QFrame#SidebarTrafficCard {"
        "  background: #151721;"
        "  border: 1px solid #242836;"
        "  border-radius: 10px;"
        "}"));
    auto* tLay = new QVBoxLayout(trafficCard);
    tLay->setContentsMargins(12, 10, 12, 10);
    tLay->setSpacing(5);

    auto* upRow = new QHBoxLayout();
    upTitleLabel_ = new QLabel(trafficCard);
    upTitleLabel_->setStyleSheet(QStringLiteral("color: #8b93a6; font-size: 11px;"));
    upSpeedLabel_ = new QLabel(QStringLiteral("0 B/s"), trafficCard);
    upSpeedLabel_->setStyleSheet(
        QStringLiteral("color: #60a5fa; font-size: 12px; font-weight: 700; font-family: Consolas, monospace;"));
    upRow->addWidget(upTitleLabel_);
    upRow->addStretch(1);
    upRow->addWidget(upSpeedLabel_);

    auto* dnRow = new QHBoxLayout();
    downTitleLabel_ = new QLabel(trafficCard);
    downTitleLabel_->setStyleSheet(QStringLiteral("color: #8b93a6; font-size: 11px;"));
    downSpeedLabel_ = new QLabel(QStringLiteral("0 B/s"), trafficCard);
    downSpeedLabel_->setStyleSheet(
        QStringLiteral("color: #34d399; font-size: 12px; font-weight: 700; font-family: Consolas, monospace;"));
    dnRow->addWidget(downTitleLabel_);
    dnRow->addStretch(1);
    dnRow->addWidget(downSpeedLabel_);

    auto* tDiv = new QFrame(trafficCard);
    tDiv->setFixedHeight(1);
    tDiv->setStyleSheet(QStringLiteral("background: #222634; border: none;"));

    auto* portRow = new QHBoxLayout();
    mixedPortLabel_ = new QLabel(QStringLiteral("Mixed :7890"), trafficCard);
    mixedPortLabel_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 10px; font-family: Consolas, monospace;"));
    apiPortLabel_ = new QLabel(QStringLiteral("API :19090"), trafficCard);
    apiPortLabel_->setStyleSheet(QStringLiteral("color: #6b7280; font-size: 10px; font-family: Consolas, monospace;"));
    portRow->addWidget(mixedPortLabel_);
    portRow->addStretch(1);
    portRow->addWidget(apiPortLabel_);

    tLay->addLayout(upRow);
    tLay->addLayout(dnRow);
    tLay->addWidget(tDiv);
    tLay->addLayout(portRow);
    lay->addWidget(trafficCard);

    return sidebar_;
}

QWidget* ConsoleWindow::buildDashboardPage(QWidget* parent) {
    auto* page = new QWidget(parent);
    auto* outerLay = new QVBoxLayout(page);
    outerLay->setContentsMargins(0, 0, 0, 0);
    outerLay->setSpacing(0);

    const WorkspaceHeaderRefs hdr = buildWorkspaceHeader(page);
    dashTitleLabel_ = hdr.titleLabel;
    dashStatusPill_ = hdr.primaryBadge;
    dashAiPill_ = hdr.secondaryBadge;
    dashSubLabel_ = hdr.subtitleLabel;

    benchAllBtn_ = new QPushButton(page);
    copyTermBtn_ = new QPushButton(page);
    reloadDashBtn_ = new QPushButton(page);
    reloadDashBtn_->setProperty("primary", true);

    hdr.actionsLayout->addWidget(benchAllBtn_);
    hdr.actionsLayout->addWidget(copyTermBtn_);
    hdr.actionsLayout->addWidget(reloadDashBtn_);
    outerLay->addWidget(hdr.container);

    auto* body = new QWidget(page);
    auto* lay = new QVBoxLayout(body);
    lay->setContentsMargins(24, 16, 24, 18);
    lay->setSpacing(14);
    outerLay->addWidget(body, 1);

    auto* dashCard = createCardFrame(body);
    auto* cardLay = new QVBoxLayout(dashCard);
    cardLay->setContentsMargins(1, 1, 1, 1);
    cardLay->setSpacing(0);

    dashboardStack_ = new QStackedWidget(dashCard);
    webView_ = new WebView2Widget(dashboardStack_);
    dashboardStack_->addWidget(webView_);

    fallbackPage_ = new QWidget(dashboardStack_);
    auto* fbLay = new QVBoxLayout(fallbackPage_);
    fbLay->setAlignment(Qt::AlignCenter);
    fbLay->setSpacing(12);
    fallbackTitleLabel_ = new QLabel(fallbackPage_);
    fallbackTitleLabel_->setStyleSheet(QStringLiteral("color: #f3f4f6; font-size: 16px; font-weight: 700;"));
    fallbackTitleLabel_->setAlignment(Qt::AlignCenter);
    fallbackReasonLabel_ = new QLabel(fallbackPage_);
    fallbackReasonLabel_->setStyleSheet(QStringLiteral("color: #9ca3af; font-size: 12px;"));
    fallbackReasonLabel_->setAlignment(Qt::AlignCenter);
    fallbackReasonLabel_->setWordWrap(true);
    fallbackReasonLabel_->setMaximumWidth(540);
    auto* fbBtns = new QHBoxLayout();
    fbBtns->setSpacing(10);
    fbBtns->setAlignment(Qt::AlignCenter);
    downloadWebViewBtn_ = new QPushButton(fallbackPage_);
    retryWebViewBtn_ = new QPushButton(fallbackPage_);
    retryWebViewBtn_->setProperty("primary", true);
    fbBtns->addWidget(downloadWebViewBtn_);
    fbBtns->addWidget(retryWebViewBtn_);
    fbLay->addWidget(fallbackTitleLabel_);
    fbLay->addWidget(fallbackReasonLabel_);
    fbLay->addLayout(fbBtns);
    dashboardStack_->addWidget(fallbackPage_);

    cardLay->addWidget(dashboardStack_, 1);
    lay->addWidget(dashCard, 1);

    connect(webView_, &WebView2Widget::webViewReady, this, [this]() {
        dashboardStack_->setCurrentWidget(webView_);
        webView_->setPageActive(currentPage_ == ConsolePage::Dashboard && isVisible());
    });
    connect(webView_, &WebView2Widget::webViewFailed, this, [this](const QString& reason) {
        fallbackReasonLabel_->setText(reason);
        dashboardStack_->setCurrentWidget(fallbackPage_);
    });
    connect(downloadWebViewBtn_, &QPushButton::clicked, this, []() {
        QDesktopServices::openUrl(
            QUrl(QStringLiteral("https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download-section")));
    });
    connect(retryWebViewBtn_, &QPushButton::clicked, this, [this]() {
        dashboardStack_->setCurrentWidget(webView_);
        webView_->retryInitialize();
    });

    connect(benchAllBtn_, &QPushButton::clicked, this, &ConsoleWindow::runBenchmarkAll);
    connect(copyTermBtn_, &QPushButton::clicked, this, &ConsoleWindow::copyTerminalProxyCommand);
    connect(reloadDashBtn_, &QPushButton::clicked, this, [this]() {
        if (webView_) {
            webView_->reloadDashboard();
            ToastOverlay::show(this, uiText("正在重新加载 MetaCubeXD 面板...", "Reloading MetaCubeXD dashboard..."),
                               PillTone::Blue);
        }
    });

    return page;
}

void ConsoleWindow::navigateTo(ConsolePage page, int subTab, const QString& diagNode) {
    currentPage_ = page;

    // CRITICAL: Hide native WebView2 child HWND BEFORE showing non-Dashboard pages!
    if (webView_) {
        webView_->setPageActive(page == ConsolePage::Dashboard && isVisible());
    }

    rightStack_->setCurrentIndex(static_cast<int>(page));

    navDashboard_->setActive(page == ConsolePage::Dashboard);
    navSubscriptions_->setActive(page == ConsolePage::Subscriptions);
    navRules_->setActive(page == ConsolePage::Rules);
    navAiLock_->setActive(page == ConsolePage::AiLock);
    navMultiNode_->setActive(page == ConsolePage::MultiNode);
    navDiagnostics_->setActive(page == ConsolePage::Diagnostics);
    navSettings_->setActive(page == ConsolePage::Settings);

    updateWindowTitle();

    if (page == ConsolePage::Rules && rulesPage_) {
        rulesPage_->onPageActivated(subTab);
    } else if (page == ConsolePage::AiLock && aiLockPage_) {
        aiLockPage_->focusSearch();
    } else if (page == ConsolePage::Diagnostics && diagPage_ && !diagNode.isEmpty()) {
        diagPage_->startDiagnosisForNode(diagNode);
    }
}

void ConsoleWindow::updateWindowTitle() {
    QString suffix;
    switch (currentPage_) {
    case ConsolePage::Dashboard:
        suffix = uiText("控制台与节点拓扑", "Dashboard & Topology");
        break;
    case ConsolePage::Subscriptions:
        suffix = uiText("托管订阅管理", "Subscriptions");
        break;
    case ConsolePage::Rules:
        suffix = uiText("分流与配置补丁", "Rules & Mixin");
        break;
    case ConsolePage::AiLock:
        suffix = uiText("AI 专用锁定", "AI Proxy Lock");
        break;
    case ConsolePage::MultiNode:
        suffix = uiText("多节点固定端口", "Multi-Node Ports");
        break;
    case ConsolePage::Diagnostics:
        suffix = uiText("测速与故障诊断", "Node Diagnostics");
        break;
    case ConsolePage::Settings:
        suffix = uiText("偏好设置", "Preferences");
        break;
    }
    setWindowTitle(uiText("ClashFX 控制台 — ", "ClashFX Console — ") + suffix);
}

void ConsoleWindow::retranslateUi() {
    modeSwitch_->setItemText(0, uiText("规则 Rule", "Rule"));
    modeSwitch_->setItemText(1, uiText("全局 Global", "Global"));
    modeSwitch_->setItemText(2, uiText("直连 Direct", "Direct"));

    navSectionLabel_->setText(uiText("工作台导航", "WORKSPACE"));
    navDashboard_->setNavTitle(uiText("控制台与节点", "Dashboard"));
    navDashboard_->setBadge(QStringLiteral("MetaCubeXD"), PillTone::Muted);
    navSubscriptions_->setNavTitle(uiText("托管订阅管理", "Subscriptions"));
    navRules_->setNavTitle(uiText("分流与配置补丁", "Rules & Mixin"));
    navRules_->setBadge(QStringLiteral("Mixin"), PillTone::Muted);
    navAiLock_->setNavTitle(uiText("AI 专用锁定", "AI Proxy Lock"));
    navMultiNode_->setNavTitle(uiText("多节点固定端口", "Multi-Node Ports"));
    navDiagnostics_->setNavTitle(uiText("测速与故障诊断", "Diagnostics"));
    navSettings_->setNavTitle(uiText("偏好设置", "Preferences"));

    upTitleLabel_->setText(uiText("↑ 实时上传", "↑ Upload"));
    downTitleLabel_->setText(uiText("↓ 实时下载", "↓ Download"));

    dashTitleLabel_->setText(uiText("控制台与节点拓扑", "Dashboard & Node Topology"));
    dashSubLabel_->setText(uiText("内嵌 MetaCubeXD 实时面板 · 左侧可直接切换系统代理、TUN 网卡与出站模式",
                                  "Embedded MetaCubeXD console · Toggle System Proxy, TUN & Outbound mode on the left"));
    benchAllBtn_->setText(uiText("⚡ 全节点测速", "⚡ Benchmark"));
    copyTermBtn_->setText(uiText("📋 终端代理", "📋 Proxy Env"));
    reloadDashBtn_->setText(uiText("↻ 刷新面板", "↻ Reload"));

    fallbackTitleLabel_->setText(uiText("未检测到 Microsoft Edge WebView2 运行时",
                                        "Microsoft Edge WebView2 Runtime Not Found"));
    downloadWebViewBtn_->setText(uiText("下载 WebView2 运行时", "Download WebView2"));
    retryWebViewBtn_->setText(uiText("重试加载面板", "Retry"));

    updateWindowTitle();
    updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
    updateDashboardHeader(lastConnected_, lastSnapshot_);
}

void ConsoleWindow::applySnapshot(bool connected, const QJsonObject& snapshot) {
    lastConnected_ = connected;
    lastSnapshot_ = snapshot;

    updateSidebarFromSnapshot(connected, snapshot);
    updateDashboardHeader(connected, snapshot);

    const QJsonObject runtime = snapshot.value(QStringLiteral("runtime")).toObject();
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt(settings.value(QStringLiteral("api_port")).toInt(19090));
    const QString apiSecret = runtime.contains(QStringLiteral("api_secret"))
                                  ? runtime.value(QStringLiteral("api_secret")).toString()
                                  : settings.value(QStringLiteral("api_secret")).toString();
    const QString dashUrl = snapshot.value(QStringLiteral("dashboard_url")).toString();
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);

    if (webView_ && apiPort > 0 && !dashUrl.isEmpty()) {
        webView_->configureDashboard(apiPort, apiSecret, dashUrl, aiLocked);
        webView_->setPageActive(currentPage_ == ConsolePage::Dashboard && isVisible());
    }

    if (subsPage_) subsPage_->updateSnapshot(snapshot);
    if (rulesPage_) rulesPage_->updateSnapshot(snapshot);
    if (aiLockPage_) aiLockPage_->updateSnapshot(snapshot);
    if (multiNodePage_) multiNodePage_->updateSnapshot(snapshot);
    if (diagPage_) diagPage_->updateSnapshot(snapshot);
    if (settingsPage_) settingsPage_->updateSnapshot(snapshot);

    syncTrafficStream();
}

void ConsoleWindow::updateSidebarFromSnapshot(bool connected, const QJsonObject& snapshot) {
    updatingUi_ = true;

    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const QJsonObject runtime = snapshot.value(QStringLiteral("runtime")).toObject();
    const bool coreRunning = snapshot.value(QStringLiteral("core_running")).toBool(false);
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    const QString aiTarget = settings.value(QStringLiteral("claudeProxyLockTarget")).toString();
    const bool sysProxyOn = settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    const bool tunOn = settings.value(QStringLiteral("enhanced_mode")).toBool(false);
    const QString mode = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toLower();

    const int mixedPort = runtime.value(QStringLiteral("mixed_port")).toInt(7890);
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt(settings.value(QStringLiteral("api_port")).toInt(19090));

    // Brand status line
    if (!connected) {
        brandSubLabel_->setText(uiText("● 守护进程断开", "● Daemon Offline"));
        brandSubLabel_->setStyleSheet(QStringLiteral("color: #f87171; font-size: 11px; font-weight: 600;"));
    } else if (coreRunning) {
        brandSubLabel_->setText(QStringLiteral("● Mihomo v1.19.24"));
        brandSubLabel_->setStyleSheet(QStringLiteral("color: #34d399; font-size: 11px; font-weight: 600;"));
    } else {
        brandSubLabel_->setText(uiText("● 内核已停止", "● Core Stopped"));
        brandSubLabel_->setStyleSheet(QStringLiteral("color: #fbbf24; font-size: 11px; font-weight: 600;"));
    }

    sidebarAiPill_->setPill(aiLocked ? uiText("AI 锁定", "AI Lock") : QString(), PillTone::Violet);

    int modeIdx = 0;
    if (mode == QLatin1String("global")) {
        modeIdx = 1;
    } else if (mode == QLatin1String("direct")) {
        modeIdx = 2;
    }
    modeSwitch_->setCurrentIndex(modeIdx, false);
    modeSwitch_->setItemEnabled(1, connected && !aiLocked);
    modeSwitch_->setItemEnabled(2, connected && !aiLocked);
    modeSwitch_->setItemEnabled(0, connected);

    sysProxyCard_->setCardEnabled(connected);
    sysProxyCard_->setState(
        sysProxyOn, uiText("系统代理", "System Proxy"),
        sysProxyOn ? QStringLiteral("127.0.0.1:%1").arg(mixedPort > 0 ? mixedPort : 7890)
                   : uiText("未开启", "Disabled"));

    tunCard_->setCardEnabled(connected);
    tunCard_->setState(
        tunOn, uiText("增强模式 TUN", "Enhanced TUN"),
        tunOn ? uiText("Wintun · 零弹窗", "Wintun · Active") : uiText("已关闭", "Disabled"));

    // Active profile combo + quota
    const QString selectedProf = settings.value(QStringLiteral("selected_profile")).toString();
    const QJsonArray profiles = snapshot.value(QStringLiteral("profiles")).toArray();
    {
        QSignalBlocker sb(profileCombo_);
        profileCombo_->clear();
        for (const QJsonValue& p : profiles) {
            profileCombo_->addItem(p.toString());
        }
        if (!selectedProf.isEmpty()) {
            const int idx = profileCombo_->findText(selectedProf);
            if (idx >= 0) {
                profileCombo_->setCurrentIndex(idx);
            } else {
                profileCombo_->addItem(selectedProf);
                profileCombo_->setCurrentIndex(profileCombo_->count() - 1);
            }
        }
    }
    profileCombo_->setEnabled(connected && profileCombo_->count() > 0);
    profileRefreshBtn_->setEnabled(connected);
    profileActivePill_->setPill(selectedProf.isEmpty() ? QString() : uiText("当前激活", "Active"), PillTone::Green);

    const QJsonObject subInfo = snapshot.value(QStringLiteral("active_subscription_info")).toObject();
    if (!subInfo.isEmpty() && (subInfo.value(QStringLiteral("total")).toDouble(0) > 0 ||
                               subInfo.contains(QStringLiteral("upload")) ||
                               subInfo.contains(QStringLiteral("download")))) {
        const double up = subInfo.value(QStringLiteral("upload")).toDouble(0.0);
        const double down = subInfo.value(QStringLiteral("download")).toDouble(0.0);
        const double total = subInfo.value(QStringLiteral("total")).toDouble(0.0);
        const double used = qMax(0.0, up + down);
        if (total > 0.0) {
            quotaBar_->setValue(qBound(0, static_cast<int>((used / total) * 100.0 + 0.5), 100));
            QString txt = uiText("已用 ", "Used ") + formatQuotaBytes(used) + QStringLiteral(" / ") +
                          formatQuotaBytes(total);
            const qint64 expTs = static_cast<qint64>(subInfo.value(QStringLiteral("expire")).toDouble(0));
            if (expTs > 0) {
                txt += QStringLiteral(" · ") +
                       QDateTime::fromSecsSinceEpoch(expTs).toString(QStringLiteral("yyyy-MM-dd")) +
                       uiText(" 到期", " exp");
            }
            quotaTextLabel_->setText(txt);
        } else {
            quotaBar_->setValue(0);
            quotaTextLabel_->setText(uiText("已用 ", "Used ") + formatQuotaBytes(used));
        }
    } else {
        quotaBar_->setValue(0);
        quotaTextLabel_->setText(uiText("本地配置 / 无流量配额头", "Local Profile / No Quota Info"));
    }

    // Nav dynamic badges
    const QJsonArray subs = snapshot.value(QStringLiteral("subscriptions")).toArray();
    navSubscriptions_->setBadge(subs.isEmpty() ? QString()
                                               : (QString::number(subs.size()) + uiText(" 个订阅", " subs")),
                                PillTone::Muted);

    navAiLock_->setBadge(aiLocked ? uiText("已保护", "Locked") : QString(), PillTone::Violet);
    Q_UNUSED(aiTarget);

    const QJsonObject ns = snapshot.value(QStringLiteral("node_services")).toObject();
    const bool portsEnabled = ns.value(QStringLiteral("settings")).toObject().value(QStringLiteral("ports_enabled")).toBool(true);
    const QJsonArray nsRows = ns.value(QStringLiteral("rows")).toArray();
    int activePorts = 0;
    for (const QJsonValue& r : nsRows) {
        if (r.toObject().value(QStringLiteral("enabled")).toBool(false)) {
            ++activePorts;
        }
    }
    if (!nsRows.isEmpty()) {
        navMultiNode_->setBadge(
            QStringLiteral("%1 / %2").arg(activePorts).arg(nsRows.size()),
            (portsEnabled && activePorts > 0) ? PillTone::Green : PillTone::Muted);
    } else {
        navMultiNode_->setBadge(QString(), PillTone::Muted);
    }

    mixedPortLabel_->setText(QStringLiteral("Mixed :%1").arg(mixedPort > 0 ? mixedPort : 7890));
    apiPortLabel_->setText(QStringLiteral("API :%1").arg(apiPort > 0 ? apiPort : 19090));

    updatingUi_ = false;
}

void ConsoleWindow::updateDashboardHeader(bool connected, const QJsonObject& snapshot) {
    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const bool coreRunning = snapshot.value(QStringLiteral("core_running")).toBool(false);
    const bool tunOn = settings.value(QStringLiteral("enhanced_mode")).toBool(false);
    const bool sysProxyOn = settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    const QString aiTarget = settings.value(QStringLiteral("claudeProxyLockTarget")).toString();
    const QString mode = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toUpper();

    if (!connected) {
        dashStatusPill_->setPill(uiText("● 守护进程未连接", "● Daemon Disconnected"), PillTone::Red);
    } else if (!coreRunning) {
        const QString err = snapshot.value(QStringLiteral("last_error")).toString().trimmed();
        dashStatusPill_->setPill(
            err.isEmpty() ? uiText("● 内核已停止", "● Core Stopped") : uiText("● 启动异常", "● Core Error"),
            PillTone::Red);
    } else {
        QStringList tags;
        if (tunOn) tags << QStringLiteral("TUN");
        if (sysProxyOn) tags << QStringLiteral("SYSPROXY");
        tags << mode;
        dashStatusPill_->setPill(QStringLiteral("● ") + tags.join(QStringLiteral(" + ")) +
                                     uiText(" 运行中", " Running"),
                                 PillTone::Green);
    }

    if (aiLocked && !aiTarget.isEmpty()) {
        dashAiPill_->setPill(QStringLiteral("AI → ") + aiTarget, PillTone::Violet);
    } else {
        dashAiPill_->setPill(QString(), PillTone::Muted);
    }

    benchAllBtn_->setEnabled(connected && coreRunning);
}

void ConsoleWindow::switchOutboundMode(int index) {
    if (updatingUi_ || !client_) {
        return;
    }
    const QJsonObject settings = lastSnapshot_.value(QStringLiteral("settings")).toObject();
    if (settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false) && index != 0) {
        updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
        ToastOverlay::show(
            this,
            uiText("AI 专用锁定开启期间强制使用规则模式，如需切换请先在 AI 锁定页停用锁定",
                   "Outbound mode stays locked to Rule while AI Lock is active"),
            PillTone::Violet);
        return;
    }

    QString nextMode = QStringLiteral("rule");
    if (index == 1) nextMode = QStringLiteral("global");
    else if (index == 2) nextMode = QStringLiteral("direct");

    QJsonObject params;
    params.insert(QStringLiteral("mode"), nextMode);
    client_->call(QStringLiteral("set_outbound_mode"), params,
                  [this, nextMode](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
                          ToastOverlay::show(this, I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      ToastOverlay::show(
                          this, uiText("已切换出站模式为 ", "Switched outbound mode to ") + nextMode.toUpper(),
                          PillTone::Blue);
                  });
}

void ConsoleWindow::toggleSystemProxy(bool nextChecked) {
    if (updatingUi_ || !client_) {
        return;
    }
    const QJsonObject settings = lastSnapshot_.value(QStringLiteral("settings")).toObject();
    if (settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false) && !nextChecked) {
        updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
        ToastOverlay::show(
            this, uiText("AI 专用锁定开启期间系统代理受保护不可关闭", "System Proxy is protected while AI Lock is active"),
            PillTone::Violet);
        return;
    }

    QJsonObject params;
    params.insert(QStringLiteral("enabled"), nextChecked);
    client_->call(QStringLiteral("set_system_proxy"), params,
                  [this, nextChecked](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
                          ToastOverlay::show(this, I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      ToastOverlay::show(
                          this,
                          nextChecked ? uiText("已开启 Windows 系统代理", "System Proxy enabled")
                                      : uiText("已关闭 Windows 系统代理", "System Proxy disabled"),
                          nextChecked ? PillTone::Blue : PillTone::Muted);
                  });
}

void ConsoleWindow::toggleEnhancedMode(bool nextChecked, bool allowUac) {
    if (updatingUi_ || !client_) {
        return;
    }
    const QJsonObject settings = lastSnapshot_.value(QStringLiteral("settings")).toObject();
    if (settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false) && !nextChecked) {
        updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
        ToastOverlay::show(
            this, uiText("AI 专用锁定开启期间增强模式 (TUN) 受保护不可关闭", "Enhanced TUN is protected while AI Lock is active"),
            PillTone::Violet);
        return;
    }

    QJsonObject params;
    params.insert(QStringLiteral("enabled"), nextChecked);
    params.insert(QStringLiteral("allow_uac"), allowUac);

    client_->call(QStringLiteral("set_enhanced_mode"), params,
                  [this, nextChecked, allowUac](bool ok, const QJsonValue&, const QString& err,
                                                const QString& errCode) {
                      if (!ok) {
                          updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
                          if (errCode == QLatin1String("SERVICE_INSTALL_REQUIRED") && !allowUac) {
                              const auto ans = QMessageBox::question(
                                  this, uiText("需要安装 ClashFX TUN 服务", "Install ClashFX TUN Service"),
                                  uiText("首次启用增强模式 (TUN) 需要通过 Windows UAC 安装系统服务。是否继续？",
                                         "Enabling Enhanced TUN requires installing ClashFXService via Windows UAC. Continue?"));
                              if (ans == QMessageBox::Yes) {
                                  toggleEnhancedMode(nextChecked, true);
                              }
                              return;
                          }
                          QMessageBox::warning(this, uiText("增强模式切换失败", "Enhanced Mode Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      ToastOverlay::show(
                          this,
                          nextChecked ? uiText("增强模式 (TUN) 已启用", "Enhanced TUN mode enabled")
                                      : uiText("增强模式 (TUN) 已关闭", "Enhanced TUN mode disabled"),
                          nextChecked ? PillTone::Green : PillTone::Muted);
                  });
}

void ConsoleWindow::selectActiveProfile(const QString& profileName) {
    if (!client_ || profileName.isEmpty()) {
        return;
    }
    QJsonObject params;
    params.insert(QStringLiteral("name"), profileName);
    client_->call(QStringLiteral("select_profile"), params,
                  [this, profileName](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      if (!ok) {
                          updateSidebarFromSnapshot(lastConnected_, lastSnapshot_);
                          const bool aiLocked = lastSnapshot_.value(QStringLiteral("settings"))
                                                    .toObject()
                                                    .value(QStringLiteral("claudeProxyLockEnabled"))
                                                    .toBool(false);
                          if (aiLocked) {
                              const auto ans = QMessageBox::question(
                                  this, uiText("AI 专用锁定冲突", "AI Lock Conflict"),
                                  I18n::instance().translateBackendText(err) +
                                      uiText("\n\n是否先关闭 AI 专用节点锁定并切换到 ",
                                             "\n\nDisable AI Lock and switch to ") +
                                      profileName + QStringLiteral("?"));
                              if (ans == QMessageBox::Yes) {
                                  QJsonObject dis;
                                  dis.insert(QStringLiteral("enabled"), false);
                                  client_->call(QStringLiteral("set_ai_lock"), dis,
                                                [this, profileName](bool dOk, const QJsonValue&, const QString& dErr,
                                                                    const QString&) {
                                                    if (!dOk) {
                                                        ToastOverlay::show(this,
                                                                           I18n::instance().translateBackendText(dErr),
                                                                           PillTone::Red);
                                                        return;
                                                    }
                                                    selectActiveProfile(profileName);
                                                });
                              }
                              return;
                          }
                          QMessageBox::warning(this, uiText("切换配置失败", "Switch Profile Failed"),
                                               I18n::instance().translateBackendText(err));
                          return;
                      }
                      ToastOverlay::show(
                          this, uiText("已切换并激活配置 ", "Switched active profile to ") + profileName,
                          PillTone::Green);
                  });
}

void ConsoleWindow::refreshActiveProfile() {
    if (!client_) {
        return;
    }
    const QString active = lastSnapshot_.value(QStringLiteral("settings")).toObject().value(QStringLiteral("selected_profile")).toString();
    bool isManaged = false;
    const QJsonArray subs = lastSnapshot_.value(QStringLiteral("subscriptions")).toArray();
    for (const QJsonValue& s : subs) {
        if (s.toObject().value(QStringLiteral("name")).toString() == active) {
            isManaged = true;
            break;
        }
    }

    profileRefreshBtn_->setEnabled(false);
    if (isManaged && !active.isEmpty()) {
        QJsonObject params;
        params.insert(QStringLiteral("name"), active);
        client_->call(QStringLiteral("refresh_subscription"), params,
                      [this, active](bool ok, const QJsonValue&, const QString& err, const QString&) {
                          profileRefreshBtn_->setEnabled(lastConnected_);
                          if (!ok) {
                              ToastOverlay::show(this, I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          client_->call(QStringLiteral("get_state"));
                          ToastOverlay::show(this, uiText("已刷新当前订阅 ", "Refreshed subscription ") + active,
                                             PillTone::Green);
                      });
    } else {
        client_->call(QStringLiteral("reload_config"), QJsonObject(),
                      [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
                          profileRefreshBtn_->setEnabled(lastConnected_);
                          if (!ok) {
                              ToastOverlay::show(this, I18n::instance().translateBackendText(err), PillTone::Red);
                              return;
                          }
                          ToastOverlay::show(this, uiText("已重新加载当前配置文件", "Reloaded active configuration"),
                                             PillTone::Green);
                      });
    }
}

void ConsoleWindow::copyTerminalProxyCommand() {
    const int port = lastSnapshot_.value(QStringLiteral("runtime")).toObject().value(QStringLiteral("mixed_port")).toInt(7890);
    const int p = port > 0 ? port : 7890;
    const QString cmd = QStringLiteral("$env:HTTP_PROXY=\"http://127.0.0.1:%1\"; $env:HTTPS_PROXY=\"http://127.0.0.1:%1\"; $env:ALL_PROXY=\"socks5://127.0.0.1:%1\"").arg(p);
    if (QClipboard* cb = QApplication::clipboard()) {
        cb->setText(cmd);
    }
    ToastOverlay::show(
        this, uiText("已复制 PowerShell 终端代理环境变量 (:", "Copied PowerShell proxy command (: ") + QString::number(p) +
                  QStringLiteral(")"),
        PillTone::Blue);
}

void ConsoleWindow::runBenchmarkAll() {
    if (!client_) {
        return;
    }
    benchAllBtn_->setEnabled(false);
    client_->call(QStringLiteral("benchmark_all"), QJsonObject(),
                  [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
                      benchAllBtn_->setEnabled(lastConnected_);
                      if (!ok) {
                          ToastOverlay::show(this, I18n::instance().translateBackendText(err), PillTone::Red);
                          return;
                      }
                      ToastOverlay::show(this, uiText("已完成全策略组节点测速", "All proxy groups benchmarked"),
                                         PillTone::Green);
                  });
}

void ConsoleWindow::presentWindow() {
    if (isMinimized()) {
        showNormal();
    } else {
        show();
    }
    setWindowState((windowState() & ~Qt::WindowMinimized) | Qt::WindowActive);
    raise();
    activateWindow();
    Win11Dwm::applyDarkTitleBar(this);
#ifdef Q_OS_WIN
    if (HWND hwnd = reinterpret_cast<HWND>(winId())) {
        SetForegroundWindow(hwnd);
    }
#endif
}

void ConsoleWindow::showEvent(QShowEvent* event) {
    QMainWindow::showEvent(event);
    Win11Dwm::applyDarkTitleBar(this);
    if (webView_) {
        webView_->setPageActive(currentPage_ == ConsolePage::Dashboard);
    }
    syncTrafficStream();
}

void ConsoleWindow::hideEvent(QHideEvent* event) {
    QMainWindow::hideEvent(event);
    if (webView_) {
        webView_->setPageActive(false);
    }
    stopTrafficStream();
}

void ConsoleWindow::closeEvent(QCloseEvent* event) {
    if (!allowClose_) {
        event->ignore();
        hide();
        return;
    }
    stopTrafficStream();
    QMainWindow::closeEvent(event);
}

QString ConsoleWindow::formatSpeedRate(qint64 bytesPerSec) {
    if (bytesPerSec < 0) bytesPerSec = 0;
    if (bytesPerSec < 1024) {
        return QString::number(bytesPerSec) + QStringLiteral(" B/s");
    }
    if (bytesPerSec < 1024 * 1024) {
        return QString::number(bytesPerSec / 1024.0, 'f', 1) + QStringLiteral(" KB/s");
    }
    if (bytesPerSec < 1024LL * 1024 * 1024) {
        return QString::number(bytesPerSec / (1024.0 * 1024.0), 'f', 2) + QStringLiteral(" MB/s");
    }
    return QString::number(bytesPerSec / (1024.0 * 1024.0 * 1024.0), 'f', 2) + QStringLiteral(" GB/s");
}

void ConsoleWindow::stopTrafficStream() {
    if (trafficReply_) {
        QNetworkReply* rep = trafficReply_;
        trafficReply_.clear();
        rep->disconnect(this);
        rep->abort();
        rep->deleteLater();
    }
    trafficBuf_.clear();
    trafficPort_ = 0;
    trafficSecret_.clear();
    if (upSpeedLabel_) upSpeedLabel_->setText(QStringLiteral("0 B/s"));
    if (downSpeedLabel_) downSpeedLabel_->setText(QStringLiteral("0 B/s"));
}

void ConsoleWindow::syncTrafficStream() {
    const bool coreRunning = lastSnapshot_.value(QStringLiteral("core_running")).toBool(false);
    if (!isVisible() || !lastConnected_ || !coreRunning) {
        stopTrafficStream();
        return;
    }

    const QJsonObject rt = lastSnapshot_.value(QStringLiteral("runtime")).toObject();
    const QJsonObject settings = lastSnapshot_.value(QStringLiteral("settings")).toObject();
    const int port = rt.value(QStringLiteral("api_port")).toInt(settings.value(QStringLiteral("api_port")).toInt(19090));
    const QString secret = rt.contains(QStringLiteral("api_secret"))
                               ? rt.value(QStringLiteral("api_secret")).toString()
                               : settings.value(QStringLiteral("api_secret")).toString();

    if (port <= 0) {
        stopTrafficStream();
        return;
    }
    if (trafficReply_ && trafficPort_ == port && trafficSecret_ == secret) {
        return;
    }

    stopTrafficStream();
    trafficPort_ = port;
    trafficSecret_ = secret;

    QNetworkRequest req(QUrl(QStringLiteral("http://127.0.0.1:%1/traffic").arg(port)));
    if (!secret.isEmpty()) {
        req.setRawHeader("Authorization", "Bearer " + secret.toUtf8());
    }
    req.setRawHeader("Accept", "application/json");

    QNetworkReply* reply = netMgr_->get(req);
    trafficReply_ = reply;

    connect(reply, &QNetworkReply::readyRead, this, [this, reply]() {
        if (trafficReply_ != reply) {
            return;
        }
        trafficBuf_.append(reply->readAll());
        while (true) {
            const int nl = trafficBuf_.indexOf('\n');
            if (nl < 0) {
                break;
            }
            const QByteArray line = trafficBuf_.left(nl).trimmed();
            trafficBuf_.remove(0, nl + 1);
            if (line.isEmpty()) {
                continue;
            }
            const QJsonDocument doc = QJsonDocument::fromJson(line);
            if (!doc.isObject()) {
                continue;
            }
            const QJsonObject obj = doc.object();
            const qint64 up = static_cast<qint64>(obj.value(QStringLiteral("up")).toDouble(0));
            const qint64 down = static_cast<qint64>(obj.value(QStringLiteral("down")).toDouble(0));
            upSpeedLabel_->setText(formatSpeedRate(up));
            downSpeedLabel_->setText(formatSpeedRate(down));
        }
    });

    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        if (trafficReply_ == reply) {
            trafficReply_.clear();
            trafficPort_ = 0;
        }
        reply->deleteLater();
    });
}

}  // namespace clashfx
