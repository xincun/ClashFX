#pragma once

#include <QJsonArray>
#include <QJsonObject>
#include <QMainWindow>
#include <QString>

class QLabel;
class QNetworkAccessManager;
class QNetworkReply;
class QStackedWidget;
class QTimer;

namespace clashfx {

class DaemonClient;
class WebView2Widget;

enum class ConsolePage {
    Dashboard = 0,
    Subscriptions = 1,
    Rules = 2,
    AiLock = 3,
    MultiNode = 4,
    Diagnostics = 5,
    Settings = 6,
};

class ConsoleWindow final : public QMainWindow {
    Q_OBJECT
public:
    explicit ConsoleWindow(DaemonClient* client, bool smokeMode = false, QWidget* parent = nullptr);
    ~ConsoleWindow() override;

    void setAllowClose(bool allow) { allowClose_ = allow; }
    void configureSmokeTest(int dwellMs, const QString& reportPath);
    void applySnapshot(bool connected, const QJsonObject& snapshot);
    void navigateTo(ConsolePage page, int subTab = -1, const QString& diagNode = QString());
    ConsolePage currentPage() const { return currentPage_; }
    void presentWindow();

signals:
    void hotkeyRecordingChanged(bool active);

protected:
    void showEvent(QShowEvent* event) override;
    void hideEvent(QHideEvent* event) override;
    void closeEvent(QCloseEvent* event) override;
    void changeEvent(QEvent* event) override;
    bool nativeEvent(const QByteArray& eventType, void* message, qintptr* result) override;

private slots:
    void onWebMessageReceived(const QJsonObject& msg);
    void onWebViewFailed(const QString& reason);
    void pollTelemetrySnapshot();

private:
    void pushWindowState();
    void pushInitPayload();
    void handleRpcMessage(const QJsonObject& msg);
    void handleMihomoMessage(const QJsonObject& msg);
    void handleNativeAction(const QJsonObject& msg);
    void applyThemePreference(const QString& theme);
    void startTrafficStream();
    void stopTrafficStream();
    void syncTelemetryState();
    void saveWindowGeometry();
    void restoreWindowGeometry();
    void beginSmokeWalk();
    void finishSmokeTest(const QJsonObject& dom, const QString& timeoutErr);
    static QString findChromiumExecutable();

    DaemonClient* client_ = nullptr;
    QNetworkAccessManager* netMgr_ = nullptr;
    QStackedWidget* stack_ = nullptr;
    WebView2Widget* webView_ = nullptr;
    QWidget* fallbackWidget_ = nullptr;
    QLabel* fallbackLabel_ = nullptr;

    QTimer* telemetryTimer_ = nullptr;
    QTimer* trafficReconnectTimer_ = nullptr;
    QNetworkReply* trafficReply_ = nullptr;
    QByteArray trafficBuffer_;
    qint64 lastUpRate_ = 0;
    qint64 lastDownRate_ = 0;
    int lastTrafficPort_ = 0;
    QString lastTrafficSecret_;

    bool allowClose_ = false;
    bool smokeMode_ = false;
    bool connected_ = false;
    bool uiReady_ = false;
    bool telemetryConnections_ = false;
    int smokeTestMs_ = 0;
    QString smokeReportPath_;
    bool smokeStarted_ = false;
    bool smokeCompleted_ = false;
    qint64 smokeInitEpochMs_ = 0;
    QTimer* smokePollTimer_ = nullptr;
    QTimer* smokeDeadlineTimer_ = nullptr;
    QString theme_ = QStringLiteral("dark");
    ConsolePage currentPage_ = ConsolePage::Dashboard;
    int pendingSubTab_ = -1;
    QString pendingDiagNode_;
    QJsonObject snapshot_;
};

} // namespace clashfx
