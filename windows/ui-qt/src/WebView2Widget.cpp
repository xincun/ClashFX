#include "WebView2Widget.h"

#include <QCoreApplication>
#include <QDir>
#include <QFileInfo>
#include <QHideEvent>
#include <QJsonArray>
#include <QJsonDocument>
#include <QMetaObject>
#include <QResizeEvent>
#include <QShowEvent>
#include <QStandardPaths>
#include <QTimer>
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
        webview_->Navigate(L"https://app.clashfx.local/index.html");
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

    navigated_ = true;
    webview_->Navigate(L"https://app.clashfx.local/index.html");
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
