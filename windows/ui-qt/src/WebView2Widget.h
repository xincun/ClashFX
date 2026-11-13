#pragma once

#include <QPointer>
#include <QString>
#include <QStringList>
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

    void configureDashboard(int apiPort,
                            const QString& apiSecret,
                            const QString& dashboardUrl);
    void reloadDashboard();

signals:
    void webViewReady();
    void webViewFailed(const QString& reason);

protected:
    void showEvent(QShowEvent* event) override;
    void hideEvent(QHideEvent* event) override;
    void resizeEvent(QResizeEvent* event) override;

private:
    void ensureInitialized();
    void syncBounds();
    void applyScriptsAndNavigate(bool forceNavigate);
    void removeDynamicScripts();

    static QString readBundledCompatibilityJS();
    static QString buildMetaCubeXDConfigJS(int port, const QString& secret);
    static QString buildRulesSnapshotJS(int port, const QString& staleMessage);

    bool initStarted_ = false;
    bool staticScriptsInstalled_ = false;
    int apiPort_ = 0;
    QString apiSecret_;
    QString dashboardUrl_;
    int appliedPort_ = 0;
    QString appliedSecret_;
    QString appliedUrl_;
    QStringList dynamicScriptIds_;

    ICoreWebView2Environment* env_ = nullptr;
    ICoreWebView2Controller* controller_ = nullptr;
    ICoreWebView2* webview_ = nullptr;
};

} // namespace clashfx
