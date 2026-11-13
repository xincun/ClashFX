#include "WebView2Widget.h"
#include "I18n.h"

#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QShowEvent>
#include <QStandardPaths>
#include <QTimer>

#include <atomic>
#include <functional>
#include <memory>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
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
    explicit ComHandler(Fn&& fn)
        : fn_(std::forward<Fn>(fn)) {}

    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void** ppvObject) override {
        if (!ppvObject) {
            return E_POINTER;
        }
        if (riid == IID_IUnknown || riid == __uuidof(Interface)) {
            *ppvObject = static_cast<Interface*>(this);
            AddRef();
            return S_OK;
        }
        *ppvObject = nullptr;
        return E_NOINTERFACE;
    }

    ULONG STDMETHODCALLTYPE AddRef() override {
        return ++refCount_;
    }

    ULONG STDMETHODCALLTYPE Release() override {
        const ULONG next = --refCount_;
        if (next == 0) {
            delete this;
        }
        return next;
    }

    HRESULT STDMETHODCALLTYPE Invoke(HRESULT errorCode, Arg2 result) override {
        return fn_(errorCode, result);
    }

private:
    std::atomic<ULONG> refCount_{1};
    Fn fn_;
};

template <typename Interface, typename Arg2, typename Fn>
Interface* makeComHandler(Fn&& fn) {
    return new ComHandler<Interface, Arg2, std::decay_t<Fn>>(std::forward<Fn>(fn));
}

QString escapeJsSingleQuoted(QString s) {
    s.replace(QStringLiteral("\\"), QStringLiteral("\\\\"));
    s.replace(QStringLiteral("'"), QStringLiteral("\\'"));
    s.replace(QStringLiteral("\r"), QString());
    s.replace(QStringLiteral("\n"), QStringLiteral("\\n"));
    return s;
}

} // namespace
#endif

WebView2Widget::WebView2Widget(QWidget* parent)
    : QWidget(parent) {
    setAttribute(Qt::WA_NativeWindow, true);
    setAttribute(Qt::WA_DontCreateNativeAncestors, false);
    setMinimumSize(640, 420);
}

WebView2Widget::~WebView2Widget() {
#ifdef Q_OS_WIN
    if (webview_) {
        webview_->Release();
        webview_ = nullptr;
    }
    if (controller_) {
        controller_->Close();
        controller_->Release();
        controller_ = nullptr;
    }
    if (env_) {
        env_->Release();
        env_ = nullptr;
    }
#endif
}

void WebView2Widget::configureDashboard(int apiPort,
                                        const QString& apiSecret,
                                        const QString& dashboardUrl) {
    apiPort_ = apiPort;
    apiSecret_ = apiSecret;
    dashboardUrl_ = dashboardUrl.trimmed();

    if (isVisible()) {
        ensureInitialized();
    }
    if (webview_) {
        applyScriptsAndNavigate(false);
    }
}

void WebView2Widget::reloadDashboard() {
#ifdef Q_OS_WIN
    if (!webview_) {
        ensureInitialized();
        return;
    }
    if (!dashboardUrl_.isEmpty()) {
        applyScriptsAndNavigate(true);
    } else {
        webview_->Reload();
    }
#endif
}

void WebView2Widget::showEvent(QShowEvent* event) {
    QWidget::showEvent(event);
    ensureInitialized();
#ifdef Q_OS_WIN
    if (controller_) {
        controller_->put_IsVisible(TRUE);
        syncBounds();
    }
#endif
}

void WebView2Widget::hideEvent(QHideEvent* event) {
    QWidget::hideEvent(event);
#ifdef Q_OS_WIN
    if (controller_) {
        controller_->put_IsVisible(FALSE);
    }
#endif
}

void WebView2Widget::resizeEvent(QResizeEvent* event) {
    QWidget::resizeEvent(event);
    syncBounds();
}

void WebView2Widget::syncBounds() {
#ifdef Q_OS_WIN
    if (!controller_) {
        return;
    }
    HWND hwnd = reinterpret_cast<HWND>(winId());
    if (!hwnd) {
        return;
    }
    RECT rc{};
    if (GetClientRect(hwnd, &rc)) {
        controller_->put_Bounds(rc);
    }
#endif
}

void WebView2Widget::ensureInitialized() {
#ifdef Q_OS_WIN
    if (initStarted_) {
        return;
    }
    initStarted_ = true;

    CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

    QString baseDir = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    if (baseDir.isEmpty()) {
        baseDir = QDir::tempPath() + QStringLiteral("/ClashFX");
    }
    const QString userDataDir = QDir::toNativeSeparators(QDir(baseDir).filePath(QStringLiteral("WebView2Data")));
    QDir().mkpath(userDataDir);
    const std::wstring userDataW = userDataDir.toStdWString();

    QPointer<WebView2Widget> self(this);
    auto* envHandler = makeComHandler<ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler,
                                      ICoreWebView2Environment*>(
        [self](HRESULT hr, ICoreWebView2Environment* createdEnv) -> HRESULT {
            if (!self) {
                return S_OK;
            }
            if (FAILED(hr) || !createdEnv) {
                emit self->webViewFailed(QStringLiteral("CreateCoreWebView2Environment failed: 0x%1")
                                             .arg(static_cast<quint32>(hr), 8, 16, QLatin1Char('0')));
                return S_OK;
            }
            self->env_ = createdEnv;
            self->env_->AddRef();

            HWND hwnd = reinterpret_cast<HWND>(self->winId());
            auto* ctrlHandler = makeComHandler<ICoreWebView2CreateCoreWebView2ControllerCompletedHandler,
                                               ICoreWebView2Controller*>(
                [self](HRESULT ctrlHr, ICoreWebView2Controller* ctrl) -> HRESULT {
                    if (!self) {
                        return S_OK;
                    }
                    if (FAILED(ctrlHr) || !ctrl) {
                        emit self->webViewFailed(QStringLiteral("CreateCoreWebView2Controller failed: 0x%1")
                                                     .arg(static_cast<quint32>(ctrlHr), 8, 16, QLatin1Char('0')));
                        return S_OK;
                    }
                    self->controller_ = ctrl;
                    self->controller_->AddRef();

                    ICoreWebView2* wv = nullptr;
                    if (SUCCEEDED(self->controller_->get_CoreWebView2(&wv)) && wv) {
                        self->webview_ = wv; // already AddRef'd by get_CoreWebView2

                        ICoreWebView2Settings* settings = nullptr;
                        if (SUCCEEDED(self->webview_->get_Settings(&settings)) && settings) {
                            settings->put_IsStatusBarEnabled(FALSE);
                            settings->put_AreDefaultContextMenusEnabled(TRUE);
                            settings->put_AreDevToolsEnabled(TRUE);

                            ICoreWebView2Settings2* settings2 = nullptr;
                            if (SUCCEEDED(settings->QueryInterface(__uuidof(ICoreWebView2Settings2),
                                                                   reinterpret_cast<void**>(&settings2))) &&
                                settings2) {
                                LPWSTR rawUa = nullptr;
                                if (SUCCEEDED(settings2->get_UserAgent(&rawUa)) && rawUa) {
                                    QString ua = QString::fromWCharArray(rawUa);
                                    CoTaskMemFree(rawUa);
                                    if (!ua.contains(QStringLiteral("ClashFX Runtime"))) {
                                        ua += QStringLiteral(" ClashFX Runtime");
                                        const std::wstring newUa = ua.toStdWString();
                                        settings2->put_UserAgent(newUa.c_str());
                                    }
                                }
                                settings2->Release();
                            }
                            settings->Release();
                        }
                    }

                    self->controller_->put_IsVisible(self->isVisible() ? TRUE : FALSE);
                    self->syncBounds();
                    self->applyScriptsAndNavigate(true);
                    emit self->webViewReady();
                    return S_OK;
                });

            const HRESULT createHr = self->env_->CreateCoreWebView2Controller(hwnd, ctrlHandler);
            ctrlHandler->Release();
            if (FAILED(createHr)) {
                emit self->webViewFailed(QStringLiteral("Invoke CreateCoreWebView2Controller failed"));
            }
            return S_OK;
        });

    const HRESULT startHr = CreateCoreWebView2EnvironmentWithOptions(
        nullptr, userDataW.c_str(), nullptr, envHandler);
    envHandler->Release();
    if (FAILED(startHr)) {
        initStarted_ = false;
        emit webViewFailed(L("未检测到 Microsoft Edge WebView2 运行时。",
                             "Microsoft Edge WebView2 Runtime is not available."));
    }
#endif
}

void WebView2Widget::removeDynamicScripts() {
#ifdef Q_OS_WIN
    if (!webview_) {
        dynamicScriptIds_.clear();
        return;
    }
    for (const QString& id : dynamicScriptIds_) {
        const std::wstring idW = id.toStdWString();
        webview_->RemoveScriptToExecuteOnDocumentCreated(idW.c_str());
    }
    dynamicScriptIds_.clear();
#endif
}

void WebView2Widget::applyScriptsAndNavigate(bool forceNavigate) {
#ifdef Q_OS_WIN
    if (!webview_ || apiPort_ <= 0 || dashboardUrl_.isEmpty()) {
        return;
    }

    if (!staticScriptsInstalled_) {
        staticScriptsInstalled_ = true;
        const QString compatJs = readBundledCompatibilityJS();
        if (!compatJs.isEmpty()) {
            const std::wstring w = compatJs.toStdWString();
            webview_->AddScriptToExecuteOnDocumentCreated(w.c_str(), nullptr);
        }
    }

    const bool configChanged = (appliedPort_ != apiPort_) ||
                               (appliedSecret_ != apiSecret_) ||
                               (appliedUrl_ != dashboardUrl_);
    if (!configChanged && !forceNavigate) {
        return;
    }

    appliedPort_ = apiPort_;
    appliedSecret_ = apiSecret_;
    appliedUrl_ = dashboardUrl_;

    removeDynamicScripts();

    const QString staleMsg = L("内核重载中，当前显示最近一次规则快照",
                               "Core unavailable; showing the last valid rules snapshot");

    const QStringList dynamicScripts = {
        buildMetaCubeXDConfigJS(apiPort_, apiSecret_),
        buildRulesSnapshotJS(apiPort_, staleMsg),
    };

    auto remaining = std::make_shared<int>(dynamicScripts.size());
    QPointer<WebView2Widget> self(this);
    const QString targetUrl = dashboardUrl_;

    for (const QString& script : dynamicScripts) {
        const std::wstring scriptW = script.toStdWString();
        auto* handler = makeComHandler<ICoreWebView2AddScriptToExecuteOnDocumentCreatedCompletedHandler,
                                       LPCWSTR>(
            [self, remaining, targetUrl](HRESULT hr, LPCWSTR id) -> HRESULT {
                if (!self) {
                    return S_OK;
                }
                if (SUCCEEDED(hr) && id) {
                    self->dynamicScriptIds_.append(QString::fromWCharArray(id));
                }
                if (--(*remaining) == 0 && self->webview_) {
                    const std::wstring navW = targetUrl.toStdWString();
                    self->webview_->Navigate(navW.c_str());
                }
                return S_OK;
            });
        webview_->AddScriptToExecuteOnDocumentCreated(scriptW.c_str(), handler);
        handler->Release();
    }
#else
    Q_UNUSED(forceNavigate);
#endif
}

QString WebView2Widget::readBundledCompatibilityJS() {
    const QString appDir = QCoreApplication::applicationDirPath();
    const QStringList candidates = {
        QDir(appDir).filePath(QStringLiteral("resources/clashfx-compat.js")),
        QDir(appDir).filePath(QStringLiteral("resources/DashboardCompatibility/clashfx-compat.js")),
    };
    for (const QString& path : candidates) {
        QFile f(path);
        if (f.exists() && f.open(QIODevice::ReadOnly)) {
            return QString::fromUtf8(f.readAll());
        }
    }
    return QString();
}

QString WebView2Widget::buildMetaCubeXDConfigJS(int port, const QString& secret) {
#ifdef Q_OS_WIN
    const QString backend = escapeJsSingleQuoted(QStringLiteral("http://127.0.0.1:%1").arg(port));
    const QString sec = escapeJsSingleQuoted(secret);
    return QStringLiteral("window.__METACUBEXD_CONFIG__ = { defaultBackendURL: '%1', defaultBackendSecret: '%2' };")
        .arg(backend, sec);
#else
    Q_UNUSED(port);
    Q_UNUSED(secret);
    return QString();
#endif
}

QString WebView2Widget::buildRulesSnapshotJS(int port, const QString& staleMessage) {
#ifdef Q_OS_WIN
    const QString backend = escapeJsSingleQuoted(QStringLiteral("http://127.0.0.1:%1").arg(port));
    const QString msg = escapeJsSingleQuoted(staleMessage);
    QString tpl = QStringLiteral(R"JS(
(function() {
  var BACKEND_ORIGIN = '__BACKEND__';
  var STALE_MESSAGE = '__STALE__';
  var BANNER_ID = 'clashfx-stale-rules-banner';
  var rulesSnapshot = null;
  var originalFetch = window.fetch;

  function isRulesRequest(input, init) {
    var method = ((init && init.method) || (typeof input !== 'string' && input.method) || 'GET').toUpperCase();
    if (method !== 'GET') return false;
    var rawURL = (typeof input === 'string') ? input : (input.url || '');
    try {
      var url = new URL(rawURL, window.location.href);
      return url.origin === BACKEND_ORIGIN && url.pathname === '/rules';
    } catch (_) {
      return false;
    }
  }
  function showStaleBanner() {
    if (document.getElementById(BANNER_ID)) return;
    var banner = document.createElement('div');
    banner.id = BANNER_ID;
    banner.textContent = STALE_MESSAGE;
    banner.style.cssText = 'position:fixed;top:10px;left:50%;transform:translateX(-50%);z-index:2147483647;padding:7px 12px;border-radius:8px;background:rgba(154,87,0,0.94);color:#fff;font:12px Segoe UI,sans-serif;box-shadow:0 2px 8px rgba(0,0,0,0.22);pointer-events:none;';
    (document.body || document.documentElement).appendChild(banner);
  }
  function hideStaleBanner() {
    var banner = document.getElementById(BANNER_ID);
    if (banner) banner.remove();
  }
  function isMeaningfulRuleTime(count, value) {
    if (Number(count) <= 0 || typeof value !== 'string') return false;
    var ms = Date.parse(value);
    return isFinite(ms) && ms > 0;
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
    if (!isRulesRequest(input, init)) return originalFetch.apply(this, arguments);
    var receiver = this, args = arguments;
    return originalFetch.apply(receiver, args).then(function(response) {
      if (response && response.ok) {
        return response.clone().text().then(function(text) {
          try {
            var payload = JSON.parse(text);
            if (!Array.isArray(payload.rules)) return response;
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
})();
)JS");
    tpl.replace(QStringLiteral("__BACKEND__"), backend);
    tpl.replace(QStringLiteral("__STALE__"), msg);
    return tpl;
#else
    Q_UNUSED(port);
    Q_UNUSED(staleMessage);
    return QString();
#endif
}

} // namespace clashfx
