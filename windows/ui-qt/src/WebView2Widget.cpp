#include "WebView2Widget.h"

#include <QCoreApplication>
#include <QDesktopServices>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QHideEvent>
#include <QJsonArray>
#include <QJsonDocument>
#include <QMetaObject>
#include <QResizeEvent>
#include <QShowEvent>
#include <QStandardPaths>
#include <QTimer>
#include <QUrl>
#include <QWindow>

#include <atomic>

#ifdef Q_OS_WIN
#include <windows.h>
#include <objbase.h>
#include <WebView2.h>
#endif

namespace clashfx {

#ifdef Q_OS_WIN
namespace {

template <typename Interface, typename Arg2, typename Fn>
class ComHandler final : public Interface {
public:
    explicit ComHandler(Fn fn) : fn_(std::move(fn)) {}

    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void** ppv) override {
        if (!ppv) return E_POINTER;
        if (riid == __uuidof(IUnknown) || riid == __uuidof(Interface)) {
            *ppv = static_cast<Interface*>(this);
            AddRef();
            return S_OK;
        }
        *ppv = nullptr;
        return E_NOINTERFACE;
    }

    ULONG STDMETHODCALLTYPE AddRef() override {
        return ++refCount_;
    }

    ULONG STDMETHODCALLTYPE Release() override {
        const ULONG n = --refCount_;
        if (n == 0) {
            delete this;
        }
        return n;
    }

    HRESULT STDMETHODCALLTYPE Invoke(HRESULT result, Arg2 arg) override {
        return fn_(result, arg);
    }

private:
    ~ComHandler() = default;
    std::atomic<ULONG> refCount_{1};
    Fn fn_;
};

template <typename Interface, typename SenderT, typename ArgsT, typename Fn>
class ComEventHandler final : public Interface {
public:
    explicit ComEventHandler(Fn fn) : fn_(std::move(fn)) {}

    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void** ppv) override {
        if (!ppv) return E_POINTER;
        if (riid == __uuidof(IUnknown) || riid == __uuidof(Interface)) {
            *ppv = static_cast<Interface*>(this);
            AddRef();
            return S_OK;
        }
        *ppv = nullptr;
        return E_NOINTERFACE;
    }

    ULONG STDMETHODCALLTYPE AddRef() override {
        return ++refCount_;
    }

    ULONG STDMETHODCALLTYPE Release() override {
        const ULONG n = --refCount_;
        if (n == 0) {
            delete this;
        }
        return n;
    }

    HRESULT STDMETHODCALLTYPE Invoke(SenderT sender, ArgsT args) override {
        return fn_(sender, args);
    }

private:
    ~ComEventHandler() = default;
    std::atomic<ULONG> refCount_{1};
    Fn fn_;
};

template <typename Interface, typename Arg2, typename Fn>
Interface* makeComHandler(Fn fn) {
    return new ComHandler<Interface, Arg2, Fn>(std::move(fn));
}

template <typename Interface, typename SenderT, typename ArgsT, typename Fn>
Interface* makeComEventHandler(Fn fn) {
    return new ComEventHandler<Interface, SenderT, ArgsT, Fn>(std::move(fn));
}

} // namespace
#endif

WebView2Widget::WebView2Widget(QWidget* parent)
    : QWidget(parent) {
    setAttribute(Qt::WA_NativeWindow, true);
    setFocusPolicy(Qt::StrongFocus);
}

WebView2Widget::~WebView2Widget() {
#ifdef Q_OS_WIN
    if (webview_ && webMessageTokenValue_ != 0) {
        EventRegistrationToken token{};
        token.value = static_cast<__int64>(webMessageTokenValue_);
        webview_->remove_WebMessageReceived(token);
        webMessageTokenValue_ = 0;
    }
    if (webview_ && newWindowTokenValue_ != 0) {
        EventRegistrationToken token{};
        token.value = static_cast<__int64>(newWindowTokenValue_);
        webview_->remove_NewWindowRequested(token);
        newWindowTokenValue_ = 0;
    }
    if (webview_ && navStartingTokenValue_ != 0) {
        EventRegistrationToken token{};
        token.value = static_cast<__int64>(navStartingTokenValue_);
        webview_->remove_NavigationStarting(token);
        navStartingTokenValue_ = 0;
    }
    if (controller_) {
        controller_->Close();
        controller_->Release();
        controller_ = nullptr;
    }
    if (webview_) {
        webview_->Release();
        webview_ = nullptr;
    }
    if (env_) {
        env_->Release();
        env_ = nullptr;
    }
#endif
}

void WebView2Widget::setUiRootDir(const QString& dirPath) {
    uiRootDir_ = dirPath.trimmed();
    if (webview_ && !navigated_) {
        configureMappingAndNavigate();
    }
}

void WebView2Widget::setThemeDark(bool dark) {
    if (themeDark_ == dark) return;
    themeDark_ = dark;
    applyBackgroundColor();
}

void WebView2Widget::setPageActive(bool active) {
    pageActive_ = active;
#ifdef Q_OS_WIN
    if (controller_) {
        controller_->put_IsVisible(active ? TRUE : FALSE);
        if (active) {
            syncBounds();
        }
    }
#endif
}

void WebView2Widget::postJsonMessage(const QJsonObject& message) {
#ifdef Q_OS_WIN
    if (!webview_) return;
    const QString json = QString::fromUtf8(QJsonDocument(message).toJson(QJsonDocument::Compact));
    const std::wstring wJson = json.toStdWString();
    webview_->PostWebMessageAsJson(wJson.c_str());
#else
    Q_UNUSED(message);
#endif
}

void WebView2Widget::reloadApp() {
#ifdef Q_OS_WIN
    if (webview_) {
        webview_->Navigate(L"http://app.clashfx.local/index.html");
    } else {
        retryInitialize();
    }
#endif
}

void WebView2Widget::retryInitialize() {
    initStarted_ = false;
    navigated_ = false;
    ensureInitialized();
}

void WebView2Widget::showEvent(QShowEvent* event) {
    QWidget::showEvent(event);
    ensureInitialized();
    setPageActive(true);
    QTimer::singleShot(0, this, [this]() { syncBounds(); });
}

void WebView2Widget::hideEvent(QHideEvent* event) {
    QWidget::hideEvent(event);
    setPageActive(false);
}

void WebView2Widget::resizeEvent(QResizeEvent* event) {
    QWidget::resizeEvent(event);
    syncBounds();
}

QString WebView2Widget::resolvedUiRootDir() const {
    if (!uiRootDir_.isEmpty()) {
        return QDir::cleanPath(uiRootDir_);
    }
    const QString appDir = QCoreApplication::applicationDirPath();
    const QString packagedDir = QDir(appDir).filePath(QStringLiteral("resources/ui"));
    if (QFileInfo::exists(QDir(packagedDir).filePath(QStringLiteral("index.html")))) {
        return packagedDir;
    }
    const QString devFallback = QDir(appDir).filePath(QStringLiteral("../../windows/ui-qt/web"));
    if (QFileInfo::exists(QDir(devFallback).filePath(QStringLiteral("index.html")))) {
        return QDir::cleanPath(devFallback);
    }
    return packagedDir;
}

void WebView2Widget::applyBackgroundColor() {
#ifdef Q_OS_WIN
    if (!controller_) return;
    ICoreWebView2Controller2* controller2 = nullptr;
    if (SUCCEEDED(controller_->QueryInterface(IID_PPV_ARGS(&controller2))) && controller2) {
        const COREWEBVIEW2_COLOR color = themeDark_
            ? COREWEBVIEW2_COLOR{255, 11, 13, 18}
            : COREWEBVIEW2_COLOR{255, 244, 246, 250};
        controller2->put_DefaultBackgroundColor(color);
        controller2->Release();
    }
#endif
}

void WebView2Widget::ensureInitialized() {
#ifdef Q_OS_WIN
    if (initStarted_) return;
    const HWND hwnd = reinterpret_cast<HWND>(winId());
    if (!hwnd) return;
    initStarted_ = true;

    QString userDataDir = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    if (userDataDir.isEmpty()) {
        userDataDir = QDir::tempPath() + QStringLiteral("/ClashFX");
    }
    userDataDir = QDir::toNativeSeparators(userDataDir + QStringLiteral("/WebView2Data"));
    QDir().mkpath(userDataDir);

    QPointer<WebView2Widget> self(this);
    const std::wstring wDataDir = userDataDir.toStdWString();

    auto* envHandler = makeComHandler<ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler,
                                      ICoreWebView2Environment*>(
        [self, hwnd](HRESULT hr, ICoreWebView2Environment* env) -> HRESULT {
            if (!self) return S_OK;
            if (FAILED(hr) || !env) {
                const QString msg = QStringLiteral("WebView2 environment creation failed (0x%1)")
                                        .arg(static_cast<quint32>(hr), 8, 16, QLatin1Char('0'));
                QMetaObject::invokeMethod(self, [self, msg]() {
                    if (self) emit self->webViewFailed(msg);
                }, Qt::QueuedConnection);
                return S_OK;
            }

            env->AddRef();
            self->env_ = env;

            auto* ctrlHandler =
                makeComHandler<ICoreWebView2CreateCoreWebView2ControllerCompletedHandler,
                               ICoreWebView2Controller*>(
                    [self](HRESULT ctrlHr, ICoreWebView2Controller* controller) -> HRESULT {
                        if (!self) return S_OK;
                        if (FAILED(ctrlHr) || !controller) {
                            const QString msg = QStringLiteral("WebView2 controller creation failed (0x%1)")
                                                    .arg(static_cast<quint32>(ctrlHr), 8, 16, QLatin1Char('0'));
                            QMetaObject::invokeMethod(self, [self, msg]() {
                                if (self) emit self->webViewFailed(msg);
                            }, Qt::QueuedConnection);
                            return S_OK;
                        }

                        controller->AddRef();
                        self->controller_ = controller;
                        controller->get_CoreWebView2(&self->webview_);

                        self->applyBackgroundColor();

                        if (self->webview_) {
                            ICoreWebView2Settings* settings = nullptr;
                            if (SUCCEEDED(self->webview_->get_Settings(&settings)) && settings) {
                                settings->put_AreDefaultContextMenusEnabled(FALSE);
                                settings->put_IsStatusBarEnabled(FALSE);
                                settings->put_IsZoomControlEnabled(FALSE);
                                settings->put_AreDevToolsEnabled(FALSE);
                                ICoreWebView2Settings2* settings2 = nullptr;
                                if (SUCCEEDED(settings->QueryInterface(IID_PPV_ARGS(&settings2))) && settings2) {
                                    LPWSTR rawUa = nullptr;
                                    if (SUCCEEDED(settings2->get_UserAgent(&rawUa)) && rawUa) {
                                        QString ua = QString::fromWCharArray(rawUa);
                                        CoTaskMemFree(rawUa);
                                        if (!ua.contains(QStringLiteral("ClashFX Runtime"))) {
                                            ua += QStringLiteral(" ClashFX Runtime");
                                            const std::wstring wUa = ua.toStdWString();
                                            settings2->put_UserAgent(wUa.c_str());
                                        }
                                    }
                                    settings2->Release();
                                }
                                ICoreWebView2Settings3* settings3 = nullptr;
                                if (SUCCEEDED(settings->QueryInterface(IID_PPV_ARGS(&settings3))) && settings3) {
                                    settings3->put_AreBrowserAcceleratorKeysEnabled(FALSE);
                                    settings3->Release();
                                }
                                settings->Release();
                            }

                            EventRegistrationToken token{};
                            auto* msgHandler = makeComEventHandler<
                                ICoreWebView2WebMessageReceivedEventHandler,
                                ICoreWebView2*,
                                ICoreWebView2WebMessageReceivedEventArgs*>(
                                [self](ICoreWebView2*, ICoreWebView2WebMessageReceivedEventArgs* args) -> HRESULT {
                                    if (!self || !args) return S_OK;
                                    LPWSTR rawSource = nullptr;
                                    QString srcUrl;
                                    if (SUCCEEDED(args->get_Source(&rawSource)) && rawSource) {
                                        srcUrl = QString::fromWCharArray(rawSource);
                                        CoTaskMemFree(rawSource);
                                    }
                                    if (!srcUrl.startsWith(QLatin1String("http://app.clashfx.local/")) &&
                                        !srcUrl.startsWith(QLatin1String("https://app.clashfx.local/"))) {
                                        return S_OK;
                                    }
                                    LPWSTR rawJson = nullptr;
                                    if (SUCCEEDED(args->get_WebMessageAsJson(&rawJson)) && rawJson) {
                                        const QString jsonStr = QString::fromWCharArray(rawJson);
                                        CoTaskMemFree(rawJson);
                                        const QByteArray utf8 = jsonStr.toUtf8();
                                        const QJsonDocument doc = QJsonDocument::fromJson(utf8);
                                        QJsonObject obj;
                                        if (doc.isObject()) {
                                            obj = doc.object();
                                        } else {
                                            const QJsonDocument wrapped = QJsonDocument::fromJson(
                                                QByteArrayLiteral("[") + utf8 + QByteArrayLiteral("]"));
                                            if (wrapped.isArray() && !wrapped.array().isEmpty() &&
                                                wrapped.array().at(0).isString()) {
                                                const QJsonDocument inner = QJsonDocument::fromJson(
                                                    wrapped.array().at(0).toString().toUtf8());
                                                if (inner.isObject()) {
                                                    obj = inner.object();
                                                }
                                            }
                                        }
                                        if (!obj.isEmpty()) {
                                            QMetaObject::invokeMethod(self, [self, obj]() {
                                                if (self) emit self->webMessageReceived(obj);
                                            }, Qt::QueuedConnection);
                                        }
                                    }
                                    return S_OK;
                                });
                            if (SUCCEEDED(self->webview_->add_WebMessageReceived(msgHandler, &token))) {
                                self->webMessageTokenValue_ = static_cast<unsigned long long>(token.value);
                            }
                            msgHandler->Release();

                            auto isExternalBrowserUrl = [](const QUrl& url) {
                                if (!url.isValid()) return false;
                                const QString scheme = url.scheme().toLower();
                                if (scheme != QLatin1String("http") && scheme != QLatin1String("https")) return false;
                                const QString host = url.host().toLower();
                                return host != QLatin1String("app.clashfx.local") &&
                                       host != QLatin1String("127.0.0.1") &&
                                       host != QLatin1String("localhost");
                            };

                            EventRegistrationToken newWinToken{};
                            auto* newWinHandler = makeComEventHandler<
                                ICoreWebView2NewWindowRequestedEventHandler,
                                ICoreWebView2*,
                                ICoreWebView2NewWindowRequestedEventArgs*>(
                                [self, isExternalBrowserUrl](ICoreWebView2*, ICoreWebView2NewWindowRequestedEventArgs* args) -> HRESULT {
                                    if (!self || !args) return S_OK;
                                    LPWSTR rawUri = nullptr;
                                    if (SUCCEEDED(args->get_Uri(&rawUri)) && rawUri) {
                                        const QUrl url(QString::fromWCharArray(rawUri));
                                        CoTaskMemFree(rawUri);
                                        if (isExternalBrowserUrl(url)) {
                                            args->put_Handled(TRUE);
                                            QMetaObject::invokeMethod(self, [url]() {
                                                QDesktopServices::openUrl(url);
                                            }, Qt::QueuedConnection);
                                        }
                                    }
                                    return S_OK;
                                });
                            if (SUCCEEDED(self->webview_->add_NewWindowRequested(newWinHandler, &newWinToken))) {
                                self->newWindowTokenValue_ = static_cast<unsigned long long>(newWinToken.value);
                            }
                            newWinHandler->Release();

                            EventRegistrationToken navToken{};
                            auto* navHandler = makeComEventHandler<
                                ICoreWebView2NavigationStartingEventHandler,
                                ICoreWebView2*,
                                ICoreWebView2NavigationStartingEventArgs*>(
                                [self, isExternalBrowserUrl](ICoreWebView2*, ICoreWebView2NavigationStartingEventArgs* args) -> HRESULT {
                                    if (!self || !args) return S_OK;
                                    LPWSTR rawUri = nullptr;
                                    if (SUCCEEDED(args->get_Uri(&rawUri)) && rawUri) {
                                        const QUrl url(QString::fromWCharArray(rawUri));
                                        CoTaskMemFree(rawUri);
                                        if (isExternalBrowserUrl(url)) {
                                            args->put_Cancel(TRUE);
                                            QMetaObject::invokeMethod(self, [url]() {
                                                QDesktopServices::openUrl(url);
                                            }, Qt::QueuedConnection);
                                        }
                                    }
                                    return S_OK;
                                });
                            if (SUCCEEDED(self->webview_->add_NavigationStarting(navHandler, &navToken))) {
                                self->navStartingTokenValue_ = static_cast<unsigned long long>(navToken.value);
                            }
                            navHandler->Release();
                        }

                        self->syncBounds();
                        controller->put_IsVisible(self->pageActive_ ? TRUE : FALSE);
                        self->configureMappingAndNavigate();
                        QMetaObject::invokeMethod(self, [self]() {
                            if (self) emit self->webViewReady();
                        }, Qt::QueuedConnection);
                        return S_OK;
                    });

            const HRESULT ctrlCall = env->CreateCoreWebView2Controller(hwnd, ctrlHandler);
            ctrlHandler->Release();
            if (FAILED(ctrlCall)) {
                const QString msg = QStringLiteral("CreateCoreWebView2Controller failed (0x%1)")
                                        .arg(static_cast<quint32>(ctrlCall), 8, 16, QLatin1Char('0'));
                QMetaObject::invokeMethod(self, [self, msg]() {
                    if (self) emit self->webViewFailed(msg);
                }, Qt::QueuedConnection);
            }
            return S_OK;
        });

    const HRESULT hr = CreateCoreWebView2EnvironmentWithOptions(
        nullptr, wDataDir.c_str(), nullptr, envHandler);
    envHandler->Release();
    if (FAILED(hr)) {
        const QString msg = QStringLiteral("WebView2 runtime unavailable (0x%1). Please install Microsoft Edge WebView2 Runtime.")
                                .arg(static_cast<quint32>(hr), 8, 16, QLatin1Char('0'));
        QMetaObject::invokeMethod(this, [this, msg]() {
            emit webViewFailed(msg);
        }, Qt::QueuedConnection);
    }
#endif
}

void WebView2Widget::configureMappingAndNavigate() {
#ifdef Q_OS_WIN
    if (!webview_) return;
    const QString uiDir = resolvedUiRootDir();
    const QString indexPath = QDir(uiDir).filePath(QStringLiteral("index.html"));
    if (!QFileInfo::exists(indexPath)) {
        emit webViewFailed(QStringLiteral("UI assets missing: %1").arg(indexPath));
        return;
    }

    ICoreWebView2_3* webview3 = nullptr;
    if (SUCCEEDED(webview_->QueryInterface(IID_PPV_ARGS(&webview3))) && webview3) {
        const std::wstring wUiDir = QDir::toNativeSeparators(uiDir).toStdWString();
        const HRESULT mapHr = webview3->SetVirtualHostNameToFolderMapping(
            L"app.clashfx.local",
            wUiDir.c_str(),
            COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_ALLOW);
        webview3->Release();
        if (FAILED(mapHr)) {
            emit webViewFailed(QStringLiteral("SetVirtualHostNameToFolderMapping failed (0x%1)")
                                   .arg(static_cast<quint32>(mapHr), 8, 16, QLatin1Char('0')));
            return;
        }
    } else {
        emit webViewFailed(QStringLiteral("ICoreWebView2_3 interface unsupported on current WebView2 Runtime"));
        return;
    }

    installMetaCubeXDSubframeScripts();
    navigated_ = true;
    webview_->Navigate(L"http://app.clashfx.local/index.html");
#endif
}

QString WebView2Widget::readBundledCompatibilityJS() {
    const QString appDir = QCoreApplication::applicationDirPath();
    const QStringList candidates{
        QDir(appDir).filePath(QStringLiteral("resources/clashfx-compat.js")),
        QDir(appDir).filePath(QStringLiteral("resources/DashboardCompatibility/clashfx-compat.js")),
        QDir(appDir).filePath(QStringLiteral("../../macos/resources/DashboardCompatibility/clashfx-compat.js")),
    };
    for (const QString& path : candidates) {
        QFile f(path);
        if (f.exists() && f.open(QIODevice::ReadOnly)) {
            return QString::fromUtf8(f.readAll());
        }
    }
    return QString();
}

void WebView2Widget::installMetaCubeXDSubframeScripts() {
#ifdef Q_OS_WIN
    if (!webview_ || subframeScriptsInstalled_) return;
    subframeScriptsInstalled_ = true;

    const QString compatJs = readBundledCompatibilityJS();
    QString script = QStringLiteral(R"JS(
(function() {
  if (window === window.top) return;
  if (location.hostname !== '127.0.0.1' && location.hostname !== 'localhost') return;

  var cfxSecret = '';
  var cfxTheme = '';
  var cfxLang = '';
  var cfxAi = false;
  try {
    var h = String(location.hash || '');
    var qPos = h.indexOf('?');
    if (qPos >= 0) {
      var sp = new URLSearchParams(h.slice(qPos + 1));
      cfxSecret = sp.get('secret') || '';
      cfxTheme = sp.get('cfx_theme') || '';
      cfxLang = sp.get('cfx_lang') || '';
      cfxAi = sp.get('cfx_ai') === '1';
    }
    window.__CLASHFX_AI_LOCKED__ = cfxAi;
    window.__METACUBEXD_CONFIG__ = Object.assign({}, window.__METACUBEXD_CONFIG__ || {}, {
      defaultBackendURL: location.origin,
      defaultBackendSecret: cfxSecret
    });
    if (localStorage.getItem('onboardingDismissed') === null) {
      localStorage.setItem('onboardingDismissed', 'true');
    }
    if ((cfxTheme === 'dark' || cfxTheme === 'light') &&
        (localStorage.getItem('theme') === null || localStorage.getItem('__cfx_theme_synced') !== cfxTheme)) {
      var mapped = cfxTheme === 'light' ? 'winter' : 'night';
      localStorage.setItem('theme', mapped);
      localStorage.setItem('__cfx_theme_synced', cfxTheme);
    }
    if ((cfxLang === 'zh' || cfxLang === 'en') && localStorage.getItem('__cfx_lang_synced') !== cfxLang) {
      document.cookie = 'metacubexd_lang=' + cfxLang + '; path=/; max-age=31536000';
      localStorage.setItem('__cfx_lang_synced', cfxLang);
    }
  } catch (_) {}
)JS");

    if (!compatJs.isEmpty()) {
        script += QLatin1Char('\n');
        script += compatJs;
        script += QLatin1Char('\n');
    }

    script += QStringLiteral(R"JS(
  var BANNER_ID = 'clashfx-stale-rules-banner';
  var NOTICE_ID = 'clashfx-guard-notice-banner';
  var rulesSnapshot = null;
  var rulePayloadByIndex = {};
  var aiLockIndices = [];
  var originalFetch = window.fetch;

  function isEnLang() {
    try {
      return document.cookie.indexOf('metacubexd_lang=en') !== -1 ||
             localStorage.getItem('__cfx_lang_synced') === 'en';
    } catch (_) {
      return false;
    }
  }
  function showGuardNotice(text) {
    var existing = document.getElementById(NOTICE_ID);
    if (existing) existing.remove();
    var toast = document.createElement('div');
    toast.id = NOTICE_ID;
    toast.textContent = text;
    toast.style.cssText = 'position:fixed;top:12px;left:50%;transform:translateX(-50%);z-index:2147483647;padding:8px 14px;border-radius:8px;background:rgba(185,28,28,0.95);color:#fff;font:600 12px Segoe UI,sans-serif;box-shadow:0 4px 12px rgba(0,0,0,0.35);pointer-events:none;';
    (document.body || document.documentElement).appendChild(toast);
    setTimeout(function() { if (toast.parentNode) toast.remove(); }, 2800);
  }
  function showStaleBanner() {
    if (document.getElementById(BANNER_ID)) return;
    var banner = document.createElement('div');
    banner.id = BANNER_ID;
    banner.textContent = isEnLang()
      ? 'Core reloading — showing last valid rules snapshot'
      : '内核重载中，当前显示最近一次规则快照';
    banner.style.cssText = 'position:fixed;top:10px;left:50%;transform:translateX(-50%);z-index:2147483647;padding:7px 12px;border-radius:8px;background:rgba(154,87,0,0.94);color:#fff;font:12px Segoe UI,sans-serif;box-shadow:0 2px 8px rgba(0,0,0,0.22);pointer-events:none;';
    (document.body || document.documentElement).appendChild(banner);
  }
  function hideStaleBanner() {
    var banner = document.getElementById(BANNER_ID);
    if (banner) banner.remove();
  }
  function parseBackendRequest(input, init) {
    var method = ((init && init.method) || (typeof input !== 'string' && input.method) || 'GET').toUpperCase();
    var rawURL = (typeof input === 'string') ? input : (input && input.url ? input.url : '');
    try {
      var url = new URL(rawURL, window.location.href);
      if (url.origin !== location.origin) return null;
      return { method: method, pathname: url.pathname.replace(/\/+$/, '') || '/' };
    } catch (_) {
      return null;
    }
  }
  function isMeaningfulRuleTime(count, value) {
    if (Number(count) <= 0 || typeof value !== 'string') return false;
    var ms = Date.parse(value);
    return isFinite(ms) && ms > 0;
  }
  function indexRuleMetadata(payload) {
    if (!payload || !Array.isArray(payload.rules)) return;
    var nextMap = {};
    var nextAi = [];
    for (var i = 0; i < payload.rules.length; i++) {
      var r = payload.rules[i];
      if (!r || typeof r !== 'object') continue;
      var idx = (typeof r.index === 'number') ? r.index : i;
      var pl = String(r.payload || '');
      nextMap[String(idx)] = pl;
      if (pl === 'clashfx-ai-lock') nextAi.push(String(idx));
    }
    rulePayloadByIndex = nextMap;
    aiLockIndices = nextAi;
  }
  function sanitizeRuleTimes(payload) {
    if (!payload || !Array.isArray(payload.rules)) return false;
    var changed = false;
    for (var i = 0; i < payload.rules.length; i++) {
      var extra = payload.rules[i] && payload.rules[i].extra;
      if (!extra || typeof extra !== 'object') continue;
      if (!isMeaningfulRuleTime(extra.hitCount, extra.hitAt) && Object.prototype.hasOwnProperty.call(extra, 'hitAt')) {
        delete extra.hitAt;
        changed = true;
      }
      if (!isMeaningfulRuleTime(extra.missCount, extra.missAt) && Object.prototype.hasOwnProperty.call(extra, 'missAt')) {
        delete extra.missAt;
        changed = true;
      }
    }
    return changed;
  }

  window.fetch = function(input, init) {
    var reqInfo = parseBackendRequest(input, init);
    if (!reqInfo) return originalFetch.apply(this, arguments);

    if ((reqInfo.pathname === '/upgrade' || reqInfo.pathname === '/restart') &&
        (reqInfo.method === 'POST' || reqInfo.method === 'PUT' || reqInfo.method === 'PATCH')) {
      return Promise.resolve(new Response(JSON.stringify({message: 'Managed by ClashFX'}), {
        status: 403, headers: {'Content-Type': 'application/json'}
      }));
    }

    if (reqInfo.method === 'PATCH' && reqInfo.pathname === '/configs' && window.__CLASHFX_AI_LOCKED__) {
      if (init && typeof init.body === 'string') {
        try {
          var cfgPatch = JSON.parse(init.body);
          if (cfgPatch && typeof cfgPatch.mode === 'string' && cfgPatch.mode.toLowerCase() !== 'rule') {
            var aiMsg = isEnLang()
              ? 'Outbound mode is locked to Rule while AI Proxy Lock is active'
              : 'AI 专用锁定开启期间出站模式固定为规则模式';
            showGuardNotice(aiMsg);
            return Promise.resolve(new Response(JSON.stringify({message: aiMsg}), {
              status: 403, headers: {'Content-Type': 'application/json'}
            }));
          }
        } catch (_) {}
      }
    }

    if (reqInfo.method === 'PATCH' && reqInfo.pathname === '/rules/disable' && init && typeof init.body === 'string') {
      try {
        var patchMap = JSON.parse(init.body);
        if (patchMap && typeof patchMap === 'object') {
          var keys = Object.keys(patchMap);
          var touchedAiVal = null;
          for (var k = 0; k < keys.length; k++) {
            var plName = rulePayloadByIndex[keys[k]] || '';
            if (plName === 'clashfx-internal-direct') {
              var guardMsg = isEnLang()
                ? 'ClashFX internal loop-prevention guard cannot be disabled'
                : '系统防环回守护规则固定开启，不可停用';
              showGuardNotice(guardMsg);
              return Promise.resolve(new Response(JSON.stringify({message: guardMsg}), {
                status: 403, headers: {'Content-Type': 'application/json'}
              }));
            }
            if (plName === 'clashfx-ai-lock') {
              touchedAiVal = Boolean(patchMap[keys[k]]);
            }
          }
          if (touchedAiVal !== null && aiLockIndices.length > 1) {
            for (var a = 0; a < aiLockIndices.length; a++) {
              patchMap[aiLockIndices[a]] = touchedAiVal;
            }
            var clonedInit = Object.assign({}, init, { body: JSON.stringify(patchMap) });
            return originalFetch.call(this, input, clonedInit);
          }
        }
      } catch (_) {}
    }

    if (reqInfo.method !== 'GET' || reqInfo.pathname !== '/rules') {
      return originalFetch.apply(this, arguments);
    }

    var receiver = this, args = arguments;
    return originalFetch.apply(receiver, args).then(function(response) {
      if (response && response.ok) {
        return response.clone().text().then(function(text) {
          try {
            var payload = JSON.parse(text);
            if (!Array.isArray(payload.rules)) return response;
            indexRuleMetadata(payload);
            var changed = sanitizeRuleTimes(payload);
            var normalized = changed ? JSON.stringify(payload) : text;
            rulesSnapshot = normalized;
            hideStaleBanner();
            if (!changed) return response;
            var headers = new Headers(response.headers);
            headers.delete('content-length');
            return new Response(normalized, {status: response.status, statusText: response.statusText, headers: headers});
          } catch (_) {
            return response;
          }
        });
      }
      if (rulesSnapshot && response && response.status >= 500) {
        showStaleBanner();
        return new Response(rulesSnapshot, {status: 200, headers: {'Content-Type': 'application/json', 'X-ClashFX-Stale-Snapshot': 'rules'}});
      }
      return response;
    }).catch(function(err) {
      if (rulesSnapshot) {
        showStaleBanner();
        return new Response(rulesSnapshot, {status: 200, headers: {'Content-Type': 'application/json', 'X-ClashFX-Stale-Snapshot': 'rules'}});
      }
      throw err;
    });
  };

  var origXhrOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(method, url) {
    this._cfxBlocked = false;
    try {
      var u = new URL(String(url || ''), window.location.href);
      var pn = u.pathname.replace(/\/+$/, '') || '/';
      if (u.origin === location.origin &&
          (pn === '/upgrade' || pn === '/restart') &&
          /^(POST|PUT|PATCH)$/i.test(String(method || ''))) {
        this._cfxBlocked = true;
      }
    } catch (_) {}
    return origXhrOpen.apply(this, arguments);
  };
  var origXhrSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.send = function() {
    if (this._cfxBlocked) {
      Object.defineProperty(this, 'readyState', {get: function() { return 4; }});
      Object.defineProperty(this, 'status', {get: function() { return 403; }});
      Object.defineProperty(this, 'responseText', {get: function() { return '{"message":"Managed by ClashFX"}'; }});
      if (typeof this.onreadystatechange === 'function') this.onreadystatechange();
      if (typeof this.onload === 'function') this.onload();
      return;
    }
    return origXhrSend.apply(this, arguments);
  };

  window.addEventListener('message', function(ev) {
    if (ev.origin !== 'http://app.clashfx.local' && ev.origin !== 'https://app.clashfx.local') return;
    var d = ev.data;
    if (!d || d.type !== 'clashfx_sync') return;
    window.__CLASHFX_AI_LOCKED__ = Boolean(d.aiLocked);
    try {
      if (d.forceTheme && (d.theme === 'dark' || d.theme === 'light')) {
        var nextT = d.theme === 'light' ? 'winter' : 'night';
        var prevT = localStorage.getItem('theme');
        localStorage.setItem('theme', nextT);
        localStorage.setItem('__cfx_theme_synced', d.theme);
        if (prevT !== nextT) {
          window.dispatchEvent(new StorageEvent('storage', {
            key: 'theme', oldValue: prevT, newValue: nextT, storageArea: localStorage
          }));
        }
        if (document.documentElement) {
          document.documentElement.setAttribute('data-theme', nextT);
        }
      }
      if (d.forceLang) {
        var lc = d.language === 'en_US' ? 'en' : 'zh';
        if (localStorage.getItem('__cfx_lang_synced') !== lc) {
          document.cookie = 'metacubexd_lang=' + lc + '; path=/; max-age=31536000';
          localStorage.setItem('__cfx_lang_synced', lc);
          location.reload();
        }
      }
    } catch (_) {}
  });
})();
)JS");

    const std::wstring wScript = script.toStdWString();
    webview_->AddScriptToExecuteOnDocumentCreated(wScript.c_str(), nullptr);
#endif
}

void WebView2Widget::syncBounds() {
#ifdef Q_OS_WIN
    if (!controller_) return;
    RECT rc{};
    const HWND hwnd = reinterpret_cast<HWND>(winId());
    if (!hwnd || !GetClientRect(hwnd, &rc) || rc.right <= 0 || rc.bottom <= 0) {
        const qreal dpr = (window() && window()->windowHandle())
            ? window()->windowHandle()->devicePixelRatio()
            : devicePixelRatioF();
        rc.left = 0;
        rc.top = 0;
        rc.right = qMax(1, qRound(width() * dpr));
        rc.bottom = qMax(1, qRound(height() * dpr));
    }
    controller_->put_Bounds(rc);
#endif
}

} // namespace clashfx
