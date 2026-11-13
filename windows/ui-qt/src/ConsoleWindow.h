#pragma once

#include <QByteArray>
#include <QJsonObject>
#include <QLabel>
#include <QMainWindow>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QPointer>
#include <QPushButton>
#include <QStackedWidget>

namespace clashfx {

class WebView2Widget;

class ConsoleWindow final : public QMainWindow {
    Q_OBJECT
public:
    explicit ConsoleWindow(QWidget* parent = nullptr);

    void setAllowClose(bool allow) { allowClose_ = allow; }
    void applySnapshot(bool connected, const QJsonObject& snapshot);
    void presentWindow();

signals:
    void openAiLockRequested();
    void openMultiNodeRequested();
    void openRulesRequested();
    void openSubscriptionsRequested();
    void openDiagnosticsRequested();
    void openSettingsRequested();

protected:
    void showEvent(QShowEvent* event) override;
    void hideEvent(QHideEvent* event) override;
    void closeEvent(QCloseEvent* event) override;

private:
    void retranslateUi();
    void syncTrafficStream();
    void stopTrafficStream();
    static QString formatSpeedRate(qint64 bytesPerSec);

    bool allowClose_ = false;
    bool lastConnected_ = false;
    QJsonObject lastSnapshot_;

    QNetworkAccessManager* netMgr_ = nullptr;
    QPointer<QNetworkReply> trafficReply_;
    QByteArray trafficBuf_;
    int trafficPort_ = 0;
    QString trafficSecret_;

    QLabel* statusBadge_ = nullptr;
    QLabel* detailLabel_ = nullptr;
    QLabel* trafficLabel_ = nullptr;
    QPushButton* aiLockBtn_ = nullptr;
    QPushButton* multiNodeBtn_ = nullptr;
    QPushButton* rulesBtn_ = nullptr;
    QPushButton* subsBtn_ = nullptr;
    QPushButton* diagBtn_ = nullptr;
    QPushButton* settingsBtn_ = nullptr;
    QPushButton* reloadBtn_ = nullptr;

    QStackedWidget* contentStack_ = nullptr;
    WebView2Widget* webView_ = nullptr;
    QWidget* fallbackPage_ = nullptr;
    QLabel* fallbackTitleLabel_ = nullptr;
    QLabel* fallbackReasonLabel_ = nullptr;
    QPushButton* downloadWebViewBtn_ = nullptr;
    QPushButton* retryWebViewBtn_ = nullptr;
};

} // namespace clashfx
