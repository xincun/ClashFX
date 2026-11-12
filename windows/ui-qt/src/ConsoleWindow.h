#pragma once

#include <QJsonObject>
#include <QLabel>
#include <QMainWindow>
#include <QPushButton>

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
    void openSubscriptionsRequested();
    void openDiagnosticsRequested();
    void openSettingsRequested();

protected:
    void closeEvent(QCloseEvent* event) override;

private:
    void retranslateUi();

    bool allowClose_ = false;
    bool lastConnected_ = false;
    QJsonObject lastSnapshot_;

    QLabel* statusBadge_ = nullptr;
    QLabel* detailLabel_ = nullptr;
    QPushButton* aiLockBtn_ = nullptr;
    QPushButton* multiNodeBtn_ = nullptr;
    QPushButton* subsBtn_ = nullptr;
    QPushButton* diagBtn_ = nullptr;
    QPushButton* settingsBtn_ = nullptr;
    QPushButton* reloadBtn_ = nullptr;
    WebView2Widget* webView_ = nullptr;
};

} // namespace clashfx
