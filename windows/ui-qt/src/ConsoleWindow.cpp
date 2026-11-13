#include "ConsoleWindow.h"
#include "I18n.h"
#include "TrayIconGenerator.h"
#include "WebView2Widget.h"

#include <QCloseEvent>
#include <QDesktopServices>
#include <QFrame>
#include <QHBoxLayout>
#include <QHideEvent>
#include <QJsonDocument>
#include <QNetworkRequest>
#include <QShowEvent>
#include <QUrl>
#include <QVBoxLayout>
#include <QWidget>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#endif

namespace clashfx {

ConsoleWindow::ConsoleWindow(QWidget* parent)
    : QMainWindow(parent) {
    setWindowIcon(TrayIconGenerator::appIcon());
    setMinimumSize(980, 620);
    resize(1180, 740);

    netMgr_ = new QNetworkAccessManager(this);

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

    trafficLabel_ = new QLabel(QStringLiteral("↑ 0 B/s  ↓ 0 B/s"), headerBar);
    trafficLabel_->setStyleSheet(QStringLiteral(
        "background-color: #222226; color: #93c5fd; border: 1px solid #2e2e33; "
        "border-radius: 4px; padding: 2px 8px; font-family: Consolas, monospace; font-size: 11px;"));

    aiLockBtn_ = new QPushButton(headerBar);
    multiNodeBtn_ = new QPushButton(headerBar);
    rulesBtn_ = new QPushButton(headerBar);
    subsBtn_ = new QPushButton(headerBar);
    diagBtn_ = new QPushButton(headerBar);
    settingsBtn_ = new QPushButton(headerBar);
    reloadBtn_ = new QPushButton(headerBar);

    barLayout->addWidget(statusBadge_);
    barLayout->addWidget(detailLabel_, 1);
    barLayout->addWidget(trafficLabel_);
    barLayout->addWidget(aiLockBtn_);
    barLayout->addWidget(multiNodeBtn_);
    barLayout->addWidget(rulesBtn_);
    barLayout->addWidget(subsBtn_);
    barLayout->addWidget(diagBtn_);
    barLayout->addWidget(settingsBtn_);
    barLayout->addWidget(reloadBtn_);

    contentStack_ = new QStackedWidget(central);
    webView_ = new WebView2Widget(contentStack_);

    fallbackPage_ = new QWidget(contentStack_);
    fallbackPage_->setStyleSheet(QStringLiteral("background-color: #18181b; color: #f4f4f5;"));
    auto* fbLayout = new QVBoxLayout(fallbackPage_);
    fbLayout->setContentsMargins(48, 48, 48, 48);
    fbLayout->setSpacing(14);
    fbLayout->addStretch(1);

    fallbackTitleLabel_ = new QLabel(fallbackPage_);
    fallbackTitleLabel_->setAlignment(Qt::AlignCenter);
    fallbackTitleLabel_->setStyleSheet(QStringLiteral("font-size: 18px; font-weight: 700; color: #f4f4f5;"));

    fallbackReasonLabel_ = new QLabel(fallbackPage_);
    fallbackReasonLabel_->setAlignment(Qt::AlignCenter);
    fallbackReasonLabel_->setWordWrap(true);
    fallbackReasonLabel_->setStyleSheet(QStringLiteral("font-size: 13px; color: #a1a1aa;"));

    auto* fbBtnRow = new QHBoxLayout();
    fbBtnRow->addStretch(1);
    downloadWebViewBtn_ = new QPushButton(fallbackPage_);
    downloadWebViewBtn_->setStyleSheet(QStringLiteral(
        "QPushButton { background-color: #2563eb; color: #ffffff; border: 1px solid #1d4ed8; "
        "border-radius: 6px; padding: 8px 18px; font-weight: 600; }"
        "QPushButton:hover { background-color: #1d4ed8; }"));
    retryWebViewBtn_ = new QPushButton(fallbackPage_);
    retryWebViewBtn_->setStyleSheet(QStringLiteral("padding: 8px 18px;"));
    fbBtnRow->addWidget(downloadWebViewBtn_);
    fbBtnRow->addSpacing(10);
    fbBtnRow->addWidget(retryWebViewBtn_);
    fbBtnRow->addStretch(1);

    fbLayout->addWidget(fallbackTitleLabel_);
    fbLayout->addWidget(fallbackReasonLabel_);
    fbLayout->addSpacing(8);
    fbLayout->addLayout(fbBtnRow);
    fbLayout->addStretch(1);

    contentStack_->addWidget(webView_);
    contentStack_->addWidget(fallbackPage_);
    contentStack_->setCurrentWidget(webView_);

    rootLayout->addWidget(headerBar, 0);
    rootLayout->addWidget(contentStack_, 1);
    setCentralWidget(central);

    connect(aiLockBtn_, &QPushButton::clicked, this, &ConsoleWindow::openAiLockRequested);
    connect(multiNodeBtn_, &QPushButton::clicked, this, &ConsoleWindow::openMultiNodeRequested);
    connect(rulesBtn_, &QPushButton::clicked, this, &ConsoleWindow::openRulesRequested);
    connect(subsBtn_, &QPushButton::clicked, this, &ConsoleWindow::openSubscriptionsRequested);
    connect(diagBtn_, &QPushButton::clicked, this, &ConsoleWindow::openDiagnosticsRequested);
    connect(settingsBtn_, &QPushButton::clicked, this, &ConsoleWindow::openSettingsRequested);
    connect(reloadBtn_, &QPushButton::clicked, this, [this]() {
        if (contentStack_->currentWidget() == fallbackPage_) {
            webView_->retryInitialize();
        } else {
            webView_->reloadDashboard();
        }
    });
    connect(webView_, &WebView2Widget::webViewReady, this, [this]() {
        contentStack_->setCurrentWidget(webView_);
    });
    connect(webView_, &WebView2Widget::webViewFailed, this, [this](const QString& reason) {
        fallbackReasonLabel_->setText(
            L("控制台嵌入式面板依赖 Microsoft Edge WebView2 运行时（托盘菜单与上方所有原生设置窗口仍可正常使用）。\n详细信息：",
              "The embedded MetaCubeXD dashboard requires Microsoft Edge WebView2 Runtime (all tray & native dialogs remain functional).\nDetails: ") +
            reason);
        contentStack_->setCurrentWidget(fallbackPage_);
    });
    connect(downloadWebViewBtn_, &QPushButton::clicked, this, []() {
        QDesktopServices::openUrl(QUrl(QStringLiteral("https://go.microsoft.com/fwlink/p/?LinkId=2124703")));
    });
    connect(retryWebViewBtn_, &QPushButton::clicked, this, [this]() {
        contentStack_->setCurrentWidget(webView_);
        webView_->retryInitialize();
    });
    connect(&I18n::instance(), &I18n::languageChanged, this, [this]() { retranslateUi(); });

    retranslateUi();
}

void ConsoleWindow::retranslateUi() {
    setWindowTitle(L("ClashFX 控制台", "ClashFX Console"));
    aiLockBtn_->setText(L("AI 专用锁定", "AI Proxy Lock"));
    multiNodeBtn_->setText(L("多节点固定端口", "Multi-Node Ports"));
    rulesBtn_->setText(L("配置补丁 / 分流", "Config Patch / Rules"));
    subsBtn_->setText(L("托管订阅", "Subscriptions"));
    diagBtn_->setText(L("单节点诊断", "Node Diagnostic"));
    settingsBtn_->setText(L("设置", "Settings"));
    reloadBtn_->setText(L("刷新面板", "Reload"));
    fallbackTitleLabel_->setText(L("无法加载嵌入式 WebView2 控制面板", "Embedded WebView2 Dashboard Unavailable"));
    if (fallbackReasonLabel_->text().isEmpty()) {
        fallbackReasonLabel_->setText(
            L("未检测到 Microsoft Edge WebView2 运行时，请下载安装后点击「重试加载」。",
              "Microsoft Edge WebView2 Runtime was not found. Install it and click 'Retry'."));
    }
    downloadWebViewBtn_->setText(L("下载 Microsoft Edge WebView2 运行时", "Download Edge WebView2 Runtime"));
    retryWebViewBtn_->setText(L("重试加载", "Retry"));
    applySnapshot(lastConnected_, lastSnapshot_);
}

QString ConsoleWindow::formatSpeedRate(qint64 bytesPerSec) {
    if (bytesPerSec <= 0) {
        return QStringLiteral("0 B/s");
    }
    if (bytesPerSec < 1024) {
        return QString::number(bytesPerSec) + QStringLiteral(" B/s");
    }
    const double kb = static_cast<double>(bytesPerSec) / 1024.0;
    if (kb < 1024.0) {
        return QString::number(kb, 'f', kb >= 100.0 ? 0 : 1) + QStringLiteral(" KB/s");
    }
    const double mb = kb / 1024.0;
    return QString::number(mb, 'f', 2) + QStringLiteral(" MB/s");
}

void ConsoleWindow::stopTrafficStream() {
    if (trafficReply_) {
        trafficReply_->disconnect(this);
        trafficReply_->abort();
        trafficReply_->deleteLater();
        trafficReply_ = nullptr;
    }
    trafficBuf_.clear();
    trafficPort_ = 0;
    trafficSecret_.clear();
}

void ConsoleWindow::syncTrafficStream() {
    const QJsonObject runtime = lastSnapshot_.value(QStringLiteral("runtime")).toObject();
    const bool coreRunning = lastConnected_ && lastSnapshot_.value(QStringLiteral("core_running")).toBool(false);
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt(0);
    const QString apiSecret = runtime.value(QStringLiteral("api_secret")).toString();

    if (!isVisible() || !coreRunning || apiPort <= 0) {
        stopTrafficStream();
        trafficLabel_->setText(QStringLiteral("↑ 0 B/s  ↓ 0 B/s"));
        return;
    }

    if (trafficReply_ && trafficPort_ == apiPort && trafficSecret_ == apiSecret) {
        return;
    }

    stopTrafficStream();
    trafficPort_ = apiPort;
    trafficSecret_ = apiSecret;

    QNetworkRequest req(QUrl(QStringLiteral("http://127.0.0.1:%1/traffic").arg(apiPort)));
    if (!apiSecret.isEmpty()) {
        req.setRawHeader("Authorization", QByteArray("Bearer ") + apiSecret.toUtf8());
    }
    trafficReply_ = netMgr_->get(req);
    connect(trafficReply_, &QIODevice::readyRead, this, [this]() {
        if (!trafficReply_) return;
        trafficBuf_.append(trafficReply_->readAll());
        int nl = -1;
        QByteArray lastLine;
        while ((nl = trafficBuf_.indexOf('\n')) >= 0) {
            const QByteArray line = trafficBuf_.left(nl).trimmed();
            trafficBuf_.remove(0, nl + 1);
            if (!line.isEmpty()) {
                lastLine = line;
            }
        }
        if (trafficBuf_.size() > 16384) {
            trafficBuf_.clear();
        }
        if (!lastLine.isEmpty()) {
            const QJsonDocument doc = QJsonDocument::fromJson(lastLine);
            if (doc.isObject()) {
                const QJsonObject o = doc.object();
                const qint64 up = static_cast<qint64>(o.value(QStringLiteral("up")).toDouble(0));
                const qint64 down = static_cast<qint64>(o.value(QStringLiteral("down")).toDouble(0));
                trafficLabel_->setText(QStringLiteral("↑ %1  ↓ %2")
                                           .arg(formatSpeedRate(up), formatSpeedRate(down)));
            }
        }
    });
    connect(trafficReply_, &QNetworkReply::finished, this, [this]() {
        if (trafficReply_) {
            trafficReply_->deleteLater();
            trafficReply_ = nullptr;
        }
        trafficPort_ = 0;
    });
}

void ConsoleWindow::applySnapshot(bool connected, const QJsonObject& snapshot) {
    lastConnected_ = connected;
    lastSnapshot_ = snapshot;

    if (!connected || snapshot.isEmpty()) {
        statusBadge_->setText(L("连接中", "Connecting"));
        statusBadge_->setStyleSheet(QStringLiteral(
            "background-color: #52525b; color: #ffffff; border-radius: 4px; padding: 2px 8px; font-weight: 600;"));
        detailLabel_->setText(L("正在等待 clashfx-daemon 就绪...", "Waiting for clashfx-daemon..."));
        syncTrafficStream();
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
    const QString lastErr = I18n::translateBackendText(snapshot.value(QStringLiteral("last_error")).toString());

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
        webView_->configureDashboard(apiPort, apiSecret, dashUrl, aiLocked);
    }
    syncTrafficStream();
}

void ConsoleWindow::showEvent(QShowEvent* event) {
    QMainWindow::showEvent(event);
    syncTrafficStream();
}

void ConsoleWindow::hideEvent(QHideEvent* event) {
    stopTrafficStream();
    QMainWindow::hideEvent(event);
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
#ifdef Q_OS_WIN
    if (HWND hwnd = reinterpret_cast<HWND>(winId())) {
        SetForegroundWindow(hwnd);
    }
#endif
}

void ConsoleWindow::closeEvent(QCloseEvent* event) {
    stopTrafficStream();
    if (allowClose_) {
        event->accept();
        return;
    }
    event->ignore();
    hide();
}

} // namespace clashfx
