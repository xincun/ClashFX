#include "ConsoleWindow.h"
#include "I18n.h"
#include "TrayIconGenerator.h"
#include "WebView2Widget.h"

#include <QCloseEvent>
#include <QFrame>
#include <QHBoxLayout>
#include <QVBoxLayout>
#include <QWidget>

namespace clashfx {

ConsoleWindow::ConsoleWindow(QWidget* parent)
    : QMainWindow(parent) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setMinimumSize(960, 620);
    resize(1160, 740);

    auto* central = new QWidget(this);
    auto* rootLayout = new QVBoxLayout(central);
    rootLayout->setContentsMargins(0, 0, 0, 0);
    rootLayout->setSpacing(0);

    auto* headerBar = new QFrame(central);
    headerBar->setObjectName(QStringLiteral("ConsoleHeaderBar"));
    headerBar->setStyleSheet(QStringLiteral(
        "#ConsoleHeaderBar { background-color: #18181b; border-bottom: 1px solid #27272a; }"
        "QLabel { color: #e4e4e7; font-size: 12px; }"
        "QPushButton { background-color: #27272a; color: #f4f4f5; border: 1px solid #3f3f46; "
        "border-radius: 6px; padding: 4px 10px; font-size: 12px; }"
        "QPushButton:hover { background-color: #3f3f46; }"));

    auto* barLayout = new QHBoxLayout(headerBar);
    barLayout->setContentsMargins(12, 7, 12, 7);
    barLayout->setSpacing(8);

    statusBadge_ = new QLabel(headerBar);
    statusBadge_->setStyleSheet(QStringLiteral(
        "background-color: #2563eb; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
    detailLabel_ = new QLabel(headerBar);

    aiLockBtn_ = new QPushButton(headerBar);
    multiNodeBtn_ = new QPushButton(headerBar);
    subsBtn_ = new QPushButton(headerBar);
    diagBtn_ = new QPushButton(headerBar);
    settingsBtn_ = new QPushButton(headerBar);
    reloadBtn_ = new QPushButton(headerBar);

    barLayout->addWidget(statusBadge_);
    barLayout->addWidget(detailLabel_, 1);
    barLayout->addWidget(aiLockBtn_);
    barLayout->addWidget(multiNodeBtn_);
    barLayout->addWidget(subsBtn_);
    barLayout->addWidget(diagBtn_);
    barLayout->addWidget(settingsBtn_);
    barLayout->addWidget(reloadBtn_);

    webView_ = new WebView2Widget(central);

    rootLayout->addWidget(headerBar, 0);
    rootLayout->addWidget(webView_, 1);
    setCentralWidget(central);

    connect(aiLockBtn_, &QPushButton::clicked, this, &ConsoleWindow::openAiLockRequested);
    connect(multiNodeBtn_, &QPushButton::clicked, this, &ConsoleWindow::openMultiNodeRequested);
    connect(subsBtn_, &QPushButton::clicked, this, &ConsoleWindow::openSubscriptionsRequested);
    connect(diagBtn_, &QPushButton::clicked, this, &ConsoleWindow::openDiagnosticsRequested);
    connect(settingsBtn_, &QPushButton::clicked, this, &ConsoleWindow::openSettingsRequested);
    connect(reloadBtn_, &QPushButton::clicked, webView_, &WebView2Widget::reloadDashboard);
    connect(&I18n::instance(), &I18n::languageChanged, this, [this]() { retranslateUi(); });

    retranslateUi();
}

void ConsoleWindow::retranslateUi() {
    setWindowTitle(L("ClashFX 控制台", "ClashFX Console"));
    aiLockBtn_->setText(L("AI 专用锁定", "AI Proxy Lock"));
    multiNodeBtn_->setText(L("多节点固定端口", "Multi-Node Ports"));
    subsBtn_->setText(L("托管订阅", "Subscriptions"));
    diagBtn_->setText(L("单节点诊断", "Node Diagnostic"));
    settingsBtn_->setText(L("设置", "Settings"));
    reloadBtn_->setText(L("刷新面板", "Reload"));
    applySnapshot(lastConnected_, lastSnapshot_);
}

void ConsoleWindow::applySnapshot(bool connected, const QJsonObject& snapshot) {
    lastConnected_ = connected;
    lastSnapshot_ = snapshot;

    if (!connected || snapshot.isEmpty()) {
        statusBadge_->setText(L("连接中", "Connecting"));
        statusBadge_->setStyleSheet(QStringLiteral(
            "background-color: #52525b; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
        detailLabel_->setText(L("正在等待 clashfx-daemon 就绪...", "Waiting for clashfx-daemon..."));
        return;
    }

    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const QJsonObject runtime = snapshot.value(QStringLiteral("runtime")).toObject();
    const bool coreRunning = snapshot.value(QStringLiteral("core_running")).toBool(false);
    const bool sysProxy = settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    const bool enhanced = settings.value(QStringLiteral("enhanced_mode")).toBool(false);
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    const QString aiTarget = settings.value(QStringLiteral("claudeProxyLockTarget")).toString();
    const QString profile = settings.value(QStringLiteral("selected_profile")).toString(QStringLiteral("config"));
    const QString mode = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toUpper();
    const int mixedPort = runtime.value(QStringLiteral("mixed_port")).toInt(7890);
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt(19090);
    const QString apiSecret = runtime.value(QStringLiteral("api_secret")).toString();
    const QString dashUrl = snapshot.value(QStringLiteral("dashboard_url")).toString();
    const QString lastErr = snapshot.value(QStringLiteral("last_error")).toString().trimmed();

    if (!coreRunning) {
        statusBadge_->setText(L("内核未就绪", "Core Stopped"));
        statusBadge_->setStyleSheet(QStringLiteral(
            "background-color: #dc2626; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
    } else if (enhanced) {
        statusBadge_->setText(QStringLiteral("TUN + %1").arg(mode));
        statusBadge_->setStyleSheet(QStringLiteral(
            "background-color: #059669; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
    } else if (sysProxy) {
        statusBadge_->setText(L("系统代理 + ", "SysProxy + ") + mode);
        statusBadge_->setStyleSheet(QStringLiteral(
            "background-color: #2563eb; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
    } else {
        statusBadge_->setText(mode);
        statusBadge_->setStyleSheet(QStringLiteral(
            "background-color: #3f3f46; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
    }

    QString summary = QStringLiteral("%1: %2   |   Mixed: 127.0.0.1:%3   |   API: %4")
                          .arg(L("当前配置", "Profile"), profile)
                          .arg(mixedPort)
                          .arg(apiPort);
    if (aiLocked && !aiTarget.isEmpty()) {
        summary += QStringLiteral("   |   ") + L("AI 锁定: ", "AI Lock: ") + aiTarget;
    }
    if (!coreRunning && !lastErr.isEmpty()) {
        const QString shortErr = lastErr.size() > 96 ? (lastErr.left(96) + QStringLiteral("...")) : lastErr;
        summary = L("启动异常: ", "Error: ") + shortErr;
        detailLabel_->setToolTip(lastErr);
    } else {
        detailLabel_->setToolTip(QString());
    }
    detailLabel_->setText(summary);

    if (coreRunning && apiPort > 0 && !dashUrl.isEmpty()) {
        webView_->configureDashboard(apiPort, apiSecret, dashUrl);
    }
}

void ConsoleWindow::presentWindow() {
    show();
    if (isMinimized()) {
        setWindowState((windowState() & ~Qt::WindowMinimized) | Qt::WindowActive);
    }
    raise();
    activateWindow();
}

void ConsoleWindow::closeEvent(QCloseEvent* event) {
    if (allowClose_) {
        event->accept();
        return;
    }
    event->ignore();
    hide();
}

} // namespace clashfx
