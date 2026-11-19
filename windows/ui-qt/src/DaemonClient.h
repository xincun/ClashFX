#pragma once

#include <QByteArray>
#include <QHash>
#include <QJsonObject>
#include <QJsonValue>
#include <QList>
#include <QLocalSocket>
#include <QObject>
#include <QPointer>
#include <QString>
#include <QTimer>

#include <functional>

namespace clashfx {

class DaemonClient final : public QObject {
    Q_OBJECT
public:
    using RpcCallback = std::function<void(bool ok,
                                           const QJsonValue& data,
                                           const QString& error,
                                           const QString& errorCode)>;

    explicit DaemonClient(const QString& customPipe = QString(),
                          const QString& customDataDir = QString(),
                          QObject* parent = nullptr);
    ~DaemonClient() override;

    static QString sanitizedUsername();
    static QString defaultServerName();

    void start();
    void stopReconnect(bool notifyQueued = false);
    bool sendQuitDaemonSync(bool forceRestoreProxy = false, int timeoutMs = 600, bool killAiClients = false);

    bool isConnected() const;
    QJsonObject snapshot() const { return snapshot_; }

    void call(const QString& method,
              const QJsonObject& params = QJsonObject(),
              RpcCallback callback = {});
    void call(QObject* context,
              const QString& method,
              const QJsonObject& params = QJsonObject(),
              RpcCallback callback = {});

signals:
    void connectionChanged(bool connected);
    void stateChanged(const QJsonObject& snapshot);
    void rpcErrorOccurred(const QString& method, const QString& error, const QString& errorCode);

private slots:
    void onConnected();
    void onDisconnected();
    void onReadyRead();
    void onSocketError(QLocalSocket::LocalSocketError socketError);
    void attemptConnect();

private:
    void maybeAutoSpawnDaemon();
    void processLine(const QByteArray& line);

    struct PendingCall {
        QString method;
        RpcCallback callback;
        bool hasContext = false;
        QPointer<QObject> context;
    };

    struct QueuedCall {
        QString method;
        QJsonObject params;
        RpcCallback callback;
        bool hasContext = false;
        QPointer<QObject> context;
        qint64 queuedEpochMs = 0;
    };

    static void invokeIfAlive(bool hasContext,
                              const QPointer<QObject>& context,
                              const RpcCallback& callback,
                              bool ok,
                              const QJsonValue& data,
                              const QString& error,
                              const QString& errorCode);

    void expireStaleQueuedCalls();

    QString serverName_;
    QString customPipeArg_;
    QString customDataDir_;
    QLocalSocket* socket_ = nullptr;
    QTimer* reconnectTimer_ = nullptr;
    QByteArray readBuffer_;
    qint64 nextId_ = 1;
    qint64 lastSpawnEpochMs_ = 0;
    bool stopping_ = false;
    QHash<qint64, PendingCall> pending_;
    QList<QueuedCall> queuedCalls_;
    QJsonObject snapshot_;
};

} // namespace clashfx
