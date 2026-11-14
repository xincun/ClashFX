#include "ConsoleWindow.h"

#include "DaemonClient.h"
#include "I18n.h"
#include "ModernWidgets.h"
#include "WebView2Widget.h"

#include <QApplication>
#include <QClipboard>
#include <QCloseEvent>
#include <QDesktopServices>
#include <QDir>
#include <QEvent>
#include <QFileDialog>
#include <QFileInfo>
#include <QGuiApplication>
#include <QHBoxLayout>
#include <QHideEvent>
#include <QIcon>
#include <QJsonDocument>
#include <QJsonValue>
#include <QLabel>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QProcess>
#include <QPushButton>
#include <QRect>
#include <QScreen>
#include <QSettings>
#include <QShowEvent>
#include <QStackedWidget>
#include <QStandardPaths>
#include <QTimer>
#include <QUrl>
#include <QVBoxLayout>

#ifdef Q_OS_WIN
#include <windows.h>
#endif

namespace clashfx {

namespace {

QSettings uiSettings() {
    return QSettings(QStringLiteral("ClashFX"), QStringLiteral("ClashFXDesktop"));
}

int htCodeForEdge(const QString& edge) {
#ifdef Q_OS_WIN
    if (edge == QLatin1String("top")) return HTTOP;
    if (edge == QLatin1String("bottom")) return HTBOTTOM;
    if (edge == QLatin1String("left")) return HTLEFT;
    if (edge == QLatin1String("right")) return HTRIGHT;
    if (edge == QLatin1String("top-left")) return HTTOPLEFT;
    if (edge == QLatin1String("top-right")) return HTTOPRIGHT;
    if (edge == QLatin1String("bottom-left")) return HTBOTTOMLEFT;
    if (edge == QLatin1String("bottom-right")) return HTBOTTOMRIGHT;
#else
    Q_UNUSED(edge);
#endif
    return 0;
}

} // namespace

ConsoleWindow::ConsoleWindow(DaemonClient* client, QWidget* parent)
    : QMainWindow(parent),
      client_(client),
      netMgr_(new QNetworkAccessManager(this)) {
    setObjectName(QStringLiteral("ClashConsoleWindow"));
    setWindowTitle(QStringLiteral("ClashFX"));
    setWindowIcon(QIcon(QStringLiteral(":/clashfx.ico")));
    setWindowFlags(Qt::Window | Qt::FramelessWindowHint | Qt::WindowMinimizeButtonHint |
                   Qt::WindowMaximizeButtonHint | Qt::WindowSystemMenuHint);
    setMinimumSize(960, 620);

    theme_ = uiSettings().value(QStringLiteral("ui/theme"), QStringLiteral("dark")).toString();
    if (theme_ != QLatin1String("light")) {
        theme_ = QStringLiteral("dark");
    }

    stack_ = new QStackedWidget(this);
    stack_->setContentsMargins(0, 0, 0, 0);
    setCentralWidget(stack_);

    webView_ = new WebView2Widget(stack_);
    webView_->setThemeDark(theme_ == QLatin1String("dark"));
    stack_->addWidget(webView_);

    // Minimal fallback screen shown only if WebView2 initialization fails
    fallbackWidget_ = new QWidget(stack_);
    fallbackWidget_->setStyleSheet(
        QStringLiteral("QWidget { background: #0b0d12; color: #f1f5f9; }"));
    auto* fbLayout = new QVBoxLayout(fallbackWidget_);
    fbLayout->setAlignment(Qt::AlignCenter);
    fbLayout->setSpacing(12);
    fallbackLabel_ = new QLabel(
        L("正在加载 ClashFX 界面...", "Loading ClashFX interface..."), fallbackWidget_);
    fallbackLabel_->setWordWrap(true);
    fallbackLabel_->setAlignment(Qt::AlignCenter);
    fallbackLabel_->setMaximumWidth(540);
    fallbackLabel_->setStyleSheet(QStringLiteral("font-size: 13px; color: #94a3b8;"));
    auto* retryBtn = new QPushButton(L("重新加载界面", "Reload UI"), fallbackWidget_);
    retryBtn->setStyleSheet(QStringLiteral(
        "QPushButton { background: #3b82f6; color: #ffffff; border: none; border-radius: 8px; "
        "padding: 8px 18px; font-weight: 600; }"
        "QPushButton:hover { background: #2563eb; }"));
    fbLayout->addWidget(fallbackLabel_);
    fbLayout->addWidget(retryBtn, 0, Qt::AlignCenter);
    stack_->addWidget(fallbackWidget_);
    stack_->setCurrentWidget(webView_);

    connect(retryBtn, &QPushButton::clicked, this, [this]() {
        stack_->setCurrentWidget(webView_);
        webView_->retryInitialize();
    });
    connect(webView_, &WebView2Widget::webViewReady, this, [this]() {
        stack_->setCurrentWidget(webView_);
    });
    connect(webView_, &WebView2Widget::webViewFailed, this, &ConsoleWindow::onWebViewFailed);
    connect(webView_, &WebView2Widget::webMessageReceived, this, &ConsoleWindow::onWebMessageReceived);

    connect(&I18n::instance(), &I18n::languageChanged, this, [this](const QString& lang) {
        if (uiReady_) {
            webView_->postJsonMessage(QJsonObject{
                {QStringLiteral("type"), QStringLiteral("language")},
                {QStringLiteral("language"), lang},
            });
        }
    });

    telemetryTimer_ = new QTimer(this);
    telemetryTimer_->setInterval(1500);
    connect(telemetryTimer_, &QTimer::timeout, this, &ConsoleWindow::pollTelemetrySnapshot);

    restoreWindowGeometry();
    Win11Dwm::applyFramelessRoundedChrome(this, theme_ == QLatin1String("dark"));
}

ConsoleWindow::~ConsoleWindow() {
    saveWindowGeometry();
    stopTrafficStream();
}

void ConsoleWindow::applySnapshot(bool connected, const QJsonObject& snapshot) {
    connected_ = connected;
    snapshot_ = snapshot;
    if (uiReady_) {
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("state")},
            {QStringLiteral("connected"), connected_},
            {QStringLiteral("snapshot"), snapshot_},
        });
    }
    syncTelemetryState();
}

void ConsoleWindow::navigateTo(ConsolePage page, int subTab, const QString& diagNode) {
    currentPage_ = page;
    pendingSubTab_ = subTab;
    pendingDiagNode_ = diagNode;
    if (uiReady_) {
        QJsonObject msg{
            {QStringLiteral("type"), QStringLiteral("navigate")},
            {QStringLiteral("page"), static_cast<int>(page)},
            {QStringLiteral("subTab"), subTab},
        };
        if (!diagNode.isEmpty()) {
            msg.insert(QStringLiteral("diagNode"), diagNode);
        }
        webView_->postJsonMessage(msg);
    }
}

void ConsoleWindow::presentWindow() {
    if (isMinimized()) {
        showNormal();
    }
    show();
    raise();
    activateWindow();
    Win11Dwm::applyFramelessRoundedChrome(this, theme_ == QLatin1String("dark"));
    webView_->setPageActive(true);
    pushWindowState();
    syncTelemetryState();
}

void ConsoleWindow::showEvent(QShowEvent* event) {
    QMainWindow::showEvent(event);
    Win11Dwm::applyFramelessRoundedChrome(this, theme_ == QLatin1String("dark"));
    webView_->setPageActive(true);
    pushWindowState();
    syncTelemetryState();
}

void ConsoleWindow::hideEvent(QHideEvent* event) {
    QMainWindow::hideEvent(event);
    webView_->setPageActive(false);
    stopTrafficStream();
    if (telemetryTimer_) {
        telemetryTimer_->stop();
    }
}

void ConsoleWindow::closeEvent(QCloseEvent* event) {
    saveWindowGeometry();
    if (allowClose_) {
        stopTrafficStream();
        event->accept();
        return;
    }
    hide();
    event->ignore();
}

void ConsoleWindow::changeEvent(QEvent* event) {
    QMainWindow::changeEvent(event);
    if (event->type() == QEvent::WindowStateChange) {
        pushWindowState();
    }
}

bool ConsoleWindow::nativeEvent(const QByteArray& eventType, void* message, qintptr* result) {
#ifdef Q_OS_WIN
    if (eventType == "windows_generic_MSG" || eventType == "windows_dispatcher_MSG") {
        MSG* msg = static_cast<MSG*>(message);
        if (msg->message == WM_NCCALCSIZE && msg->wParam == TRUE) {
            auto* params = reinterpret_cast<NCCALCSIZE_PARAMS*>(msg->lParam);
            if (IsZoomed(msg->hwnd)) {
                HMONITOR mon = MonitorFromWindow(msg->hwnd, MONITOR_DEFAULTTONEAREST);
                MONITORINFO mi{};
                mi.cbSize = sizeof(mi);
                if (GetMonitorInfoW(mon, &mi)) {
                    params->rgrc[0] = mi.rcWork;
                }
            }
            *result = 0;
            return true;
        }
        if (msg->message == WM_GETMINMAXINFO) {
            HMONITOR mon = MonitorFromWindow(msg->hwnd, MONITOR_DEFAULTTONEAREST);
            MONITORINFO mi{};
            mi.cbSize = sizeof(mi);
            if (GetMonitorInfoW(mon, &mi)) {
                auto* mmi = reinterpret_cast<MINMAXINFO*>(msg->lParam);
                mmi->ptMaxPosition.x = mi.rcWork.left - mi.rcMonitor.left;
                mmi->ptMaxPosition.y = mi.rcWork.top - mi.rcMonitor.top;
                mmi->ptMaxSize.x = mi.rcWork.right - mi.rcWork.left;
                mmi->ptMaxSize.y = mi.rcWork.bottom - mi.rcWork.top;
            }
            *result = 0;
            return false;
        }
    }
#else
    Q_UNUSED(eventType);
    Q_UNUSED(message);
    Q_UNUSED(result);
#endif
    return QMainWindow::nativeEvent(eventType, message, result);
}

void ConsoleWindow::onWebViewFailed(const QString& reason) {
    if (fallbackLabel_) {
        fallbackLabel_->setText(reason);
    }
    stack_->setCurrentWidget(fallbackWidget_);
}

void ConsoleWindow::pushWindowState() {
    if (!uiReady_) return;
    webView_->postJsonMessage(QJsonObject{
        {QStringLiteral("type"), QStringLiteral("window_state")},
        {QStringLiteral("maximized"), isMaximized()},
    });
}

void ConsoleWindow::pushInitPayload() {
    QJsonObject initMsg{
        {QStringLiteral("type"), QStringLiteral("init")},
        {QStringLiteral("connected"), connected_},
        {QStringLiteral("snapshot"), snapshot_},
        {QStringLiteral("language"), I18n::instance().language()},
        {QStringLiteral("theme"), theme_},
        {QStringLiteral("page"), static_cast<int>(currentPage_)},
        {QStringLiteral("subTab"), pendingSubTab_},
        {QStringLiteral("maximized"), isMaximized()},
    };
    if (!pendingDiagNode_.isEmpty()) {
        initMsg.insert(QStringLiteral("diagNode"), pendingDiagNode_);
    }
    webView_->postJsonMessage(initMsg);
}

void ConsoleWindow::onWebMessageReceived(const QJsonObject& msg) {
    const QString type = msg.value(QStringLiteral("type")).toString();

    if (type == QLatin1String("ui_ready")) {
        uiReady_ = true;
        pushInitPayload();
        syncTelemetryState();
        return;
    }

    if (type == QLatin1String("nav_sync")) {
        const int pageIdx = msg.value(QStringLiteral("page")).toInt(0);
        if (pageIdx >= 0 && pageIdx <= 6) {
            currentPage_ = static_cast<ConsolePage>(pageIdx);
        }
        pendingSubTab_ = msg.value(QStringLiteral("subTab")).toInt(-1);
        pendingDiagNode_.clear();
        return;
    }

    if (type == QLatin1String("rpc")) {
        handleRpcMessage(msg);
        return;
    }

    if (type == QLatin1String("mihomo_api")) {
        handleMihomoMessage(msg);
        return;
    }

    if (type == QLatin1String("native_action")) {
        handleNativeAction(msg);
        return;
    }

    if (type == QLatin1String("set_theme")) {
        applyThemePreference(msg.value(QStringLiteral("theme")).toString());
        return;
    }

    if (type == QLatin1String("set_language")) {
        const QString lang = msg.value(QStringLiteral("language")).toString();
        if (lang == QLatin1String("zh_CN") || lang == QLatin1String("en_US")) {
            I18n::instance().setLanguage(lang);
        }
        return;
    }

    if (type == QLatin1String("telemetry_subscribe")) {
        telemetryConnections_ = msg.value(QStringLiteral("include_connections")).toBool(false);
        syncTelemetryState();
        if (msg.value(QStringLiteral("enabled")).toBool(true)) {
            pollTelemetrySnapshot();
        }
        return;
    }

    if (type == QLatin1String("window_drag")) {
#ifdef Q_OS_WIN
        ReleaseCapture();
        POINT pt{};
        GetCursorPos(&pt);
        SendMessageW(reinterpret_cast<HWND>(winId()), WM_NCLBUTTONDOWN, HTCAPTION, MAKELPARAM(pt.x, pt.y));
#endif
        return;
    }

    if (type == QLatin1String("window_resize")) {
#ifdef Q_OS_WIN
        if (!isMaximized()) {
            const int ht = htCodeForEdge(msg.value(QStringLiteral("edge")).toString());
            if (ht != 0) {
                ReleaseCapture();
                POINT pt{};
                GetCursorPos(&pt);
                SendMessageW(reinterpret_cast<HWND>(winId()), WM_NCLBUTTONDOWN, ht, MAKELPARAM(pt.x, pt.y));
            }
        }
#endif
        return;
    }

    if (type == QLatin1String("window_control")) {
        const QString action = msg.value(QStringLiteral("action")).toString();
        if (action == QLatin1String("minimize")) {
            showMinimized();
        } else if (action == QLatin1String("maximize") || action == QLatin1String("toggle_maximize")) {
            if (isMaximized()) {
                showNormal();
            } else {
                showMaximized();
            }
        } else if (action == QLatin1String("close")) {
            close();
        }
        return;
    }
}

void ConsoleWindow::handleRpcMessage(const QJsonObject& msg) {
    const QJsonValue id = msg.value(QStringLiteral("id"));
    const QString method = msg.value(QStringLiteral("method")).toString();
    const QJsonObject params = msg.value(QStringLiteral("params")).toObject();

    if (!client_) {
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("rpc_result")},
            {QStringLiteral("id"), id},
            {QStringLiteral("ok"), false},
            {QStringLiteral("error"), QStringLiteral("DaemonClient unavailable")},
        });
        return;
    }

    client_->call(method, params, [this, id](bool ok, const QJsonValue& data, const QString& err, const QString& code) {
        if (!uiReady_) return;
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("rpc_result")},
            {QStringLiteral("id"), id},
            {QStringLiteral("ok"), ok},
            {QStringLiteral("data"), data},
            {QStringLiteral("error"), err},
            {QStringLiteral("error_code"), code},
        });
    });
}

void ConsoleWindow::handleMihomoMessage(const QJsonObject& msg) {
    const QJsonValue id = msg.value(QStringLiteral("id"));
    const QString verb = msg.value(QStringLiteral("method")).toString(QStringLiteral("GET")).toUpper();
    QString path = msg.value(QStringLiteral("path")).toString();
    if (!path.startsWith(QLatin1Char('/'))) {
        path.prepend(QLatin1Char('/'));
    }

    const QJsonObject runtime = snapshot_.value(QStringLiteral("runtime")).toObject();
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt();
    const QString secret = runtime.value(QStringLiteral("api_secret")).toString();
    if (apiPort <= 0) {
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("mihomo_result")},
            {QStringLiteral("id"), id},
            {QStringLiteral("ok"), false},
            {QStringLiteral("status"), 0},
            {QStringLiteral("error"), QStringLiteral("Mihomo API port unavailable")},
        });
        return;
    }

    const QUrl url(QStringLiteral("http://127.0.0.1:%1%2").arg(apiPort).arg(path));
    QNetworkRequest req(url);
    req.setTransferTimeout(6000);
    if (!secret.isEmpty()) {
        req.setRawHeader("Authorization", QByteArray("Bearer ") + secret.toUtf8());
    }

    QByteArray bodyBytes;
    const QJsonValue bodyVal = msg.value(QStringLiteral("body"));
    if (!bodyVal.isUndefined() && !bodyVal.isNull()) {
        req.setHeader(QNetworkRequest::ContentTypeHeader, QStringLiteral("application/json"));
        if (bodyVal.isObject()) {
            bodyBytes = QJsonDocument(bodyVal.toObject()).toJson(QJsonDocument::Compact);
        } else if (bodyVal.isArray()) {
            bodyBytes = QJsonDocument(bodyVal.toArray()).toJson(QJsonDocument::Compact);
        } else {
            bodyBytes = bodyVal.toString().toUtf8();
        }
    }

    QNetworkReply* reply = netMgr_->sendCustomRequest(req, verb.toUtf8(), bodyBytes);
    connect(reply, &QNetworkReply::finished, this, [this, reply, id]() {
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const QByteArray payload = reply->readAll();
        const bool netOk = (reply->error() == QNetworkReply::NoError);
        const QString errStr = netOk ? QString() : reply->errorString();
        reply->deleteLater();

        QJsonValue parsed = QString::fromUtf8(payload);
        if (!payload.trimmed().isEmpty()) {
            const QJsonDocument doc = QJsonDocument::fromJson(payload);
            if (doc.isObject()) parsed = doc.object();
            else if (doc.isArray()) parsed = doc.array();
        }

        if (!uiReady_) return;
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("mihomo_result")},
            {QStringLiteral("id"), id},
            {QStringLiteral("ok"), netOk && status >= 200 && status < 400},
            {QStringLiteral("status"), status},
            {QStringLiteral("data"), parsed},
            {QStringLiteral("error"), errStr},
        });
    });
}

void ConsoleWindow::handleNativeAction(const QJsonObject& msg) {
    const QJsonValue id = msg.value(QStringLiteral("id"));
    const QString action = msg.value(QStringLiteral("action")).toString();

    auto reply = [this, id](bool ok, const QJsonValue& data = QJsonValue(), const QString& err = QString()) {
        if (!uiReady_ || id.isUndefined()) return;
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("native_result")},
            {QStringLiteral("id"), id},
            {QStringLiteral("ok"), ok},
            {QStringLiteral("data"), data},
            {QStringLiteral("error"), err},
        });
    };

    if (action == QLatin1String("copy_text")) {
        if (auto* cb = QApplication::clipboard()) {
            cb->setText(msg.value(QStringLiteral("text")).toString());
        }
        reply(true, QJsonObject{{QStringLiteral("copied"), true}});
        return;
    }

    if (action == QLatin1String("read_clipboard")) {
        const QString text = QApplication::clipboard() ? QApplication::clipboard()->text() : QString();
        reply(true, QJsonObject{{QStringLiteral("text"), text}});
        return;
    }

    if (action == QLatin1String("open_path")) {
        const QString path = msg.value(QStringLiteral("path")).toString();
        const bool ok = !path.isEmpty() && QDesktopServices::openUrl(QUrl::fromLocalFile(path));
        reply(ok, QJsonValue(), ok ? QString() : QStringLiteral("Failed to open path"));
        return;
    }

    if (action == QLatin1String("open_url")) {
        const QUrl url(msg.value(QStringLiteral("url")).toString());
        const bool allowed = url.isValid() &&
                             (url.scheme() == QLatin1String("http") || url.scheme() == QLatin1String("https"));
        const bool ok = allowed && QDesktopServices::openUrl(url);
        reply(ok, QJsonValue(), ok ? QString() : QStringLiteral("Invalid or blocked URL"));
        return;
    }

    if (action == QLatin1String("pick_directory")) {
        const QString title = msg.value(QStringLiteral("title")).toString(
            L("选择目录", "Select Directory"));
        const QString initial = msg.value(QStringLiteral("initial")).toString();
        const QString picked = QFileDialog::getExistingDirectory(this, title, initial);
        reply(true, QJsonObject{{QStringLiteral("path"), picked}});
        return;
    }

    if (action == QLatin1String("launch_chrome_proxy")) {
        const int port = msg.value(QStringLiteral("port")).toInt();
        const QString exe = findChromiumExecutable();
        if (exe.isEmpty() || port <= 0) {
            reply(false, QJsonValue(), L("未找到 Chrome 或 Edge 浏览器", "Chrome or Edge browser not found"));
            return;
        }
        const QString dataBase = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
        const QString profileDir = QDir::toNativeSeparators(
            QStringLiteral("%1/ChromeProfiles/port_%2").arg(dataBase).arg(port));
        QDir().mkpath(profileDir);
        const QStringList args{
            QStringLiteral("--proxy-server=http://127.0.0.1:%1").arg(port),
            QStringLiteral("--user-data-dir=%1").arg(profileDir),
            QStringLiteral("--no-first-run"),
            QStringLiteral("--no-default-browser-check"),
        };
        const bool launched = QProcess::startDetached(exe, args);
        reply(launched, QJsonObject{{QStringLiteral("exe"), exe}},
              launched ? QString() : L("启动浏览器失败", "Failed to launch browser"));
        return;
    }

    if (action == QLatin1String("reload_ui")) {
        webView_->reloadApp();
        reply(true);
        return;
    }

    reply(false, QJsonValue(), QStringLiteral("Unknown native action"));
}

void ConsoleWindow::applyThemePreference(const QString& theme) {
    theme_ = (theme == QLatin1String("light")) ? QStringLiteral("light") : QStringLiteral("dark");
    uiSettings().setValue(QStringLiteral("ui/theme"), theme_);
    const bool dark = (theme_ == QLatin1String("dark"));
    Win11Dwm::applyFramelessRoundedChrome(this, dark);
    webView_->setThemeDark(dark);
}

namespace {

template <typename OnObj>
void bindNdjsonStream(QObject* owner, QNetworkReply*& replySlot, QByteArray& buffer,
                      QNetworkReply* reply, OnObj&& onObj) {
    replySlot = reply;
    QObject::connect(reply, &QNetworkReply::readyRead, owner, [reply, &replySlot, &buffer, onObj]() {
        if (!reply || reply != replySlot) return;
        buffer.append(reply->readAll());
        while (true) {
            const int nl = buffer.indexOf('\n');
            if (nl < 0) break;
            const QByteArray line = buffer.left(nl).trimmed();
            buffer.remove(0, nl + 1);
            if (line.isEmpty()) continue;
            const QJsonDocument doc = QJsonDocument::fromJson(line);
            if (doc.isObject()) {
                onObj(doc.object());
            }
        }
    });
    QObject::connect(reply, &QNetworkReply::finished, owner, [&replySlot, reply]() {
        if (replySlot == reply) {
            replySlot = nullptr;
        }
        reply->deleteLater();
    });
}

void abortNdjsonStream(QObject* owner, QNetworkReply*& replySlot, QByteArray& buffer) {
    if (replySlot) {
        replySlot->disconnect(owner);
        replySlot->abort();
        replySlot->deleteLater();
        replySlot = nullptr;
    }
    buffer.clear();
}

} // namespace

void ConsoleWindow::syncTelemetryState() {
    const bool coreRunning = snapshot_.value(QStringLiteral("core_running")).toBool(false);
    const int apiPort = snapshot_.value(QStringLiteral("runtime")).toObject().value(QStringLiteral("api_port")).toInt();
    const bool active = isVisible() && uiReady_ && connected_ && coreRunning && apiPort > 0;

    if (!active) {
        stopTrafficStream();
        if (telemetryTimer_->isActive()) telemetryTimer_->stop();
        lastUpRate_ = 0;
        lastDownRate_ = 0;
        lastMemInuse_ = 0;
        return;
    }

    startTrafficStream();
    if (!telemetryTimer_->isActive()) {
        telemetryTimer_->start();
        pollTelemetrySnapshot();
    }
}

void ConsoleWindow::startTrafficStream() {
    const QJsonObject runtime = snapshot_.value(QStringLiteral("runtime")).toObject();
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt();
    const QString secret = runtime.value(QStringLiteral("api_secret")).toString();
    if (apiPort <= 0) return;

    if (trafficReply_ && lastTrafficPort_ == apiPort && lastTrafficSecret_ == secret) {
        return;
    }
    stopTrafficStream();

    lastTrafficPort_ = apiPort;
    lastTrafficSecret_ = secret;

    auto makeReq = [&](const QString& path) {
        QNetworkRequest req(QUrl(QStringLiteral("http://127.0.0.1:%1%2").arg(apiPort).arg(path)));
        if (!secret.isEmpty()) {
            req.setRawHeader("Authorization", QByteArray("Bearer ") + secret.toUtf8());
        }
        return req;
    };

    auto pushTelemetry = [this]() {
        if (!uiReady_) return;
        webView_->postJsonMessage(QJsonObject{
            {QStringLiteral("type"), QStringLiteral("telemetry")},
            {QStringLiteral("up"), lastUpRate_},
            {QStringLiteral("down"), lastDownRate_},
            {QStringLiteral("memInuse"), lastMemInuse_},
        });
    };

    bindNdjsonStream(this, trafficReply_, trafficBuffer_, netMgr_->get(makeReq(QStringLiteral("/traffic"))),
                     [this, pushTelemetry](const QJsonObject& obj) {
                         lastUpRate_ = static_cast<qint64>(obj.value(QStringLiteral("up")).toDouble(0));
                         lastDownRate_ = static_cast<qint64>(obj.value(QStringLiteral("down")).toDouble(0));
                         pushTelemetry();
                     });

    // Mihomo reports runtime memory usage via a dedicated /memory NDJSON stream rather than /connections.
    bindNdjsonStream(this, memoryReply_, memoryBuffer_, netMgr_->get(makeReq(QStringLiteral("/memory"))),
                     [this, pushTelemetry](const QJsonObject& obj) {
                         const qint64 inuse = static_cast<qint64>(obj.value(QStringLiteral("inuse")).toDouble(0));
                         if (inuse > 0) {
                             lastMemInuse_ = inuse;
                             pushTelemetry();
                         }
                     });
}

void ConsoleWindow::stopTrafficStream() {
    abortNdjsonStream(this, trafficReply_, trafficBuffer_);
    abortNdjsonStream(this, memoryReply_, memoryBuffer_);
    lastTrafficPort_ = 0;
    lastTrafficSecret_.clear();
}

void ConsoleWindow::pollTelemetrySnapshot() {
    if (!isVisible() || !uiReady_) return;
    const QJsonObject runtime = snapshot_.value(QStringLiteral("runtime")).toObject();
    const int apiPort = runtime.value(QStringLiteral("api_port")).toInt();
    const QString secret = runtime.value(QStringLiteral("api_secret")).toString();
    if (apiPort <= 0) return;

    // Fetch /connections (gives uploadTotal, downloadTotal, active count + optional list)
    QNetworkRequest connReq(QUrl(QStringLiteral("http://127.0.0.1:%1/connections").arg(apiPort)));
    connReq.setTransferTimeout(2000);
    if (!secret.isEmpty()) {
        connReq.setRawHeader("Authorization", QByteArray("Bearer ") + secret.toUtf8());
    }
    QNetworkReply* connReply = netMgr_->get(connReq);
    connect(connReply, &QNetworkReply::finished, this, [this, connReply]() {
        const bool ok = (connReply->error() == QNetworkReply::NoError);
        const QByteArray data = connReply->readAll();
        connReply->deleteLater();
        if (!ok || !uiReady_) return;
        const QJsonDocument doc = QJsonDocument::fromJson(data);
        if (!doc.isObject()) return;
        const QJsonObject obj = doc.object();
        const QJsonArray conns = obj.value(QStringLiteral("connections")).toArray();
        const qint64 connMem = static_cast<qint64>(obj.value(QStringLiteral("memory")).toDouble(0));
        if (connMem > 0) {
            lastMemInuse_ = connMem;
        }
        QJsonObject payload{
            {QStringLiteral("type"), QStringLiteral("telemetry")},
            {QStringLiteral("up"), lastUpRate_},
            {QStringLiteral("down"), lastDownRate_},
            {QStringLiteral("upTotal"), obj.value(QStringLiteral("uploadTotal"))},
            {QStringLiteral("downTotal"), obj.value(QStringLiteral("downloadTotal"))},
            {QStringLiteral("memory"), connMem > 0 ? connMem : lastMemInuse_},
            {QStringLiteral("memInuse"), lastMemInuse_},
            {QStringLiteral("connectionCount"), conns.size()},
        };
        if (telemetryConnections_) {
            payload.insert(QStringLiteral("connections"), conns);
        }
        webView_->postJsonMessage(payload);
    });
}

void ConsoleWindow::saveWindowGeometry() {
    auto s = uiSettings();
    s.setValue(QStringLiteral("window/maximized"), isMaximized());
    if (!isMaximized()) {
        s.setValue(QStringLiteral("window/geometry"), saveGeometry());
    }
}

void ConsoleWindow::restoreWindowGeometry() {
    const auto s = uiSettings();
    const QByteArray geom = s.value(QStringLiteral("window/geometry")).toByteArray();
    bool restored = false;
    if (!geom.isEmpty()) {
        restored = restoreGeometry(geom);
    }
    if (!restored) {
        QRect avail(0, 0, 1440, 900);
        if (auto* screen = QGuiApplication::primaryScreen()) {
            avail = screen->availableGeometry();
        }
        const int w = qBound(980, qRound(avail.width() * 0.78), 1320);
        const int h = qBound(640, qRound(avail.height() * 0.82), 860);
        resize(w, h);
        move(avail.center() - rect().center());
    }
}

QString ConsoleWindow::findChromiumExecutable() {
#ifdef Q_OS_WIN
    const QString localApp = qEnvironmentVariable("LOCALAPPDATA");
    const QString pf = qEnvironmentVariable("ProgramFiles");
    const QString pf86 = qEnvironmentVariable("ProgramFiles(x86)");
    const QStringList candidates{
        pf + QStringLiteral("/Google/Chrome/Application/chrome.exe"),
        pf86 + QStringLiteral("/Google/Chrome/Application/chrome.exe"),
        localApp + QStringLiteral("/Google/Chrome/Application/chrome.exe"),
        pf + QStringLiteral("/Microsoft/Edge/Application/msedge.exe"),
        pf86 + QStringLiteral("/Microsoft/Edge/Application/msedge.exe"),
    };
    for (const QString& c : candidates) {
        if (QFileInfo::exists(c)) return QDir::toNativeSeparators(c);
    }
#endif
    return QString();
}

} // namespace clashfx
