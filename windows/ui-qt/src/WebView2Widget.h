#pragma once

#include <QJsonObject>
#include <QList>
#include <QPointer>
#include <QString>
#include <QWidget>

struct ICoreWebView2Environment;
struct ICoreWebView2Controller;
struct ICoreWebView2;

namespace clashfx {

class WebView2Widget final : public QWidget {
    Q_OBJECT
public:
    explicit WebView2Widget(QWidget* parent = nullptr);
    ~WebView2Widget() override;

    void setUiRootDir(const QString& dirPath);
    void setThemeDark(bool dark);
    void setPageActive(bool active);
    void postJsonMessage(const QJsonObject& message);
    void reloadApp();
    void retryInitialize();

signals:
    void webViewReady();
    void webViewFailed(const QString& reason);
    void webMessageReceived(const QJsonObject& message);

protected:
    void showEvent(QShowEvent* event) override;
    void hideEvent(QHideEvent* event) override;
    void resizeEvent(QResizeEvent* event) override;

private:
    void cleanupWebView();
    void ensureInitialized();
    void syncBounds();
    void applyBackgroundColor();
    void configureMappingAndNavigate();
    void installMetaCubeXDSubframeScripts();
    static QString readBundledCompatibilityJS();
    QString resolvedUiRootDir() const;

    bool initStarted_ = false;
    bool pageActive_ = true;
    bool themeDark_ = true;
    bool navigated_ = false;
    bool subframeScriptsInstalled_ = false;
    unsigned long long initGeneration_ = 0;
    QString uiRootDir_;

#ifdef Q_OS_WIN
    unsigned long long webMessageTokenValue_ = 0;
    unsigned long long newWindowTokenValue_ = 0;
    unsigned long long navStartingTokenValue_ = 0;
#endif
    ICoreWebView2Environment* env_ = nullptr;
    ICoreWebView2Controller* controller_ = nullptr;
    ICoreWebView2* webview_ = nullptr;
};

} // namespace clashfx
