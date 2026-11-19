#include "DaemonClient.h"

#include <QCoreApplication>
#include <QDateTime>
#include <QDir>
#include <QFileInfo>
#include <QJsonDocument>
#include <QProcess>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#endif

namespace clashfx {

QString DaemonClient::sanitizedUsername() {
    QString raw;
#ifdef Q_OS_WIN
    wchar_t buf[256] = {};
    DWORD len = 256;
    if (GetUserNameW(buf, &len) && len > 1) {
        raw = QString::fromWCharArray(buf, static_cast<int>(len - 1));
    }
#endif
    if (raw.isEmpty()) {
        raw = qEnvironmentVariable("USERNAME");
        if (raw.isEmpty()) {
            raw = qEnvironmentVariable("USER");
        }
    }
    const int slashIdx = raw.lastIndexOf(QLatin1Char('\\'));
    if (slashIdx >= 0) {
        raw = raw.mid(slashIdx + 1);
    }
    QString out;
    out.reserve(raw.size());
    for (const QChar ch : raw) {
        const ushort u = ch.unicode();
        if ((u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z') ||
            (u >= '0' && u <= '9') || u == '-' || u == '_') {
            out.append(ch);
        }
    }
    return out.isEmpty() ? QStringLiteral("default") : out;
}

QString DaemonClient::defaultServerName() {
    return QStringLiteral("ClashFXDaemon-") + sanitizedUsername();
}

DaemonClient::DaemonClient(const QString& customPipe,
                           const QString& customDataDir,
                           QObject* parent)
    : QObject(parent)
    , customPipeArg_(customPipe.trimmed())
    , customDataDir_(customDataDir.trimmed()) {
    if (customPipeArg_.isEmpty()) {
        serverName_ = defaultServerName();
    } else {
        QString cleaned = customPipeArg_;
        const QString prefix = QStringLiteral("\\\\.\\pipe\\");
        if (cleaned.startsWith(prefix, Qt::CaseInsensitive)) {
            cleaned = cleaned.mid(prefix.size());
        }
        serverName_ = cleaned;
    }

    socket_ = new QLocalSocket(this);
    reconnectTimer_ = new QTimer(this);
    reconnectTimer_->setSingleShot(true);

    connect(socket_, &QLocalSocket::connected, this, &DaemonClient::onConnected);
    connect(socket_, &QLocalSocket::disconnected, this, &DaemonClient::onDisconnected);
    connect(socket_, &QLocalSocket::readyRead, this, &DaemonClient::onReadyRead);
    connect(socket_, &QLocalSocket::errorOccurred, this, &DaemonClient::onSocketError);
    connect(reconnectTimer_, &QTimer::timeout, this, &DaemonClient::attemptConnect);
}

DaemonClient::~DaemonClient() {
    stopping_ = true;
    if (reconnectTimer_) {
        reconnectTimer_->stop();
    }
    queuedCalls_.clear();
    pending_.clear();
    if (socket_) {
        socket_->disconnect(this);
        socket_->abort();
    }
}

void DaemonClient::invokeIfAlive(bool hasContext,
                                 const QPointer<QObject>& context,
                                 const RpcCallback& callback,
                                 bool ok,
                                 const QJsonValue& data,
                                 const QString& error,
                                 const QString& errorCode) {
    if (!callback) {
        return;
    }
    if (hasContext && context.isNull()) {
        return;
    }
    callback(ok, data, error, errorCode);
}

void DaemonClient::start() {
    stopping_ = false;
    attemptConnect();
}

void DaemonClient::stopReconnect(bool notifyQueued) {
    stopping_ = true;
    if (reconnectTimer_) {
        reconnectTimer_->stop();
    }
    const auto stale = queuedCalls_;
    queuedCalls_.clear();
    if (!notifyQueued) {
        return;
    }
    for (const auto& q : stale) {
        invokeIfAlive(q.hasContext, q.context, q.callback, false, QJsonValue(),
                      QStringLiteral("clashfx-daemon client stopped"), QStringLiteral("NOT_CONNECTED"));
    }
}

bool DaemonClient::sendQuitDaemonSync(bool forceRestoreProxy, int timeoutMs, bool killAiClients) {
    stopReconnect();
    QLocalSocket sock;
    sock.connectToServer(serverName_);
    if (!sock.waitForConnected(250)) {
        return false;
    }
    QJsonObject params;
    if (forceRestoreProxy) {
        params.insert(QStringLiteral("force_restore_proxy"), true);
    }
    if (killAiClients) {
        params.insert(QStringLiteral("kill_ai_clients"), true);
    }
    QJsonObject frame;
    frame.insert(QStringLiteral("id"), 999999);
    frame.insert(QStringLiteral("method"), QStringLiteral("quit_daemon"));
    if (!params.isEmpty()) {
        frame.insert(QStringLiteral("params"), params);
    }
    QByteArray raw = QJsonDocument(frame).toJson(QJsonDocument::Compact);
    raw.append('\n');
    sock.write(raw);
    sock.flush();
    sock.waitForBytesWritten(250);
    sock.waitForReadyRead(qMax(100, timeoutMs));
    sock.disconnectFromServer();
    return true;
}

bool DaemonClient::isConnected() const {
    return socket_ && socket_->state() == QLocalSocket::ConnectedState;
}

void DaemonClient::attemptConnect() {
    if (stopping_) {
        return;
    }
    if (socket_->state() == QLocalSocket::ConnectedState ||
        socket_->state() == QLocalSocket::ConnectingState) {
        return;
    }
    socket_->abort();
    socket_->connectToServer(serverName_);
}

void DaemonClient::maybeAutoSpawnDaemon() {
    if (stopping_) {
        return;
    }
    const qint64 nowMs = QDateTime::currentMSecsSinceEpoch();
    if (nowMs - lastSpawnEpochMs_ < 6000) {
        return;
    }
    const QString appDir = QCoreApplication::applicationDirPath();
    const QString daemonExe = QDir(appDir).filePath(QStringLiteral("clashfx-daemon.exe"));
    if (!QFileInfo::exists(daemonExe)) {
        return;
    }
    lastSpawnEpochMs_ = nowMs;

    QStringList args;
    if (!customPipeArg_.isEmpty()) {
        args << QStringLiteral("-pipe") << customPipeArg_;
    }
    if (!customDataDir_.isEmpty()) {
        args << QStringLiteral("-data-dir") << customDataDir_;
    }

    QProcess proc;
    proc.setProgram(daemonExe);
    proc.setArguments(args);
    proc.setWorkingDirectory(appDir);
#ifdef Q_OS_WIN
    proc.setCreateProcessArgumentsModifier([](QProcess::CreateProcessArguments* cpArgs) {
        cpArgs->flags &= ~DWORD(DETACHED_PROCESS);
        cpArgs->flags |= CREATE_NO_WINDOW;
        cpArgs->startupInfo->dwFlags |= STARTF_USESHOWWINDOW;
        cpArgs->startupInfo->wShowWindow = SW_HIDE;
    });
#endif
    proc.startDetached();
}

void DaemonClient::onConnected() {
    readBuffer_.clear();
    emit connectionChanged(true);
    call(this, QStringLiteral("subscribe"), QJsonObject(), [this](bool ok, const QJsonValue& data, const QString&, const QString&) {
        if (ok && data.isObject()) {
            snapshot_ = data.toObject();
            emit stateChanged(snapshot_);
        }
    });
    const auto queued = queuedCalls_;
    queuedCalls_.clear();
    for (const auto& q : queued) {
        if (q.hasContext && q.context.isNull()) {
            continue;
        }
        call(q.hasContext ? q.context.data() : nullptr, q.method, q.params, q.callback);
    }
}

void DaemonClient::expireStaleQueuedCalls() {
    if (queuedCalls_.isEmpty()) {
        return;
    }
    const qint64 nowMs = QDateTime::currentMSecsSinceEpoch();
    QList<QueuedCall> keep;
    keep.reserve(queuedCalls_.size());
    for (const auto& q : queuedCalls_) {
        if (q.hasContext && q.context.isNull()) {
            continue;
        }
        if (nowMs - q.queuedEpochMs >= 4500) {
            invokeIfAlive(q.hasContext, q.context, q.callback, false, QJsonValue(),
                          QStringLiteral("clashfx-daemon is starting up, please retry in a moment."),
                          QStringLiteral("NOT_CONNECTED"));
        } else {
            keep.append(q);
        }
    }
    queuedCalls_ = std::move(keep);
}

void DaemonClient::onDisconnected() {
    const auto callbacks = pending_;
    pending_.clear();
    if (!stopping_) {
        for (auto it = callbacks.cbegin(); it != callbacks.cend(); ++it) {
            invokeIfAlive(it.value().hasContext, it.value().context, it.value().callback,
                          false, QJsonValue(), QStringLiteral("Disconnected from clashfx-daemon"), QString());
        }
    }
    emit connectionChanged(false);
    if (!stopping_) {
        reconnectTimer_->start(700);
    }
}

void DaemonClient::onSocketError(QLocalSocket::LocalSocketError /*socketError*/) {
    if (stopping_) {
        return;
    }
    maybeAutoSpawnDaemon();
    expireStaleQueuedCalls();
    if (!reconnectTimer_->isActive()) {
        reconnectTimer_->start(650);
    }
}

void DaemonClient::call(const QString& method,
                        const QJsonObject& params,
                        RpcCallback callback) {
    call(nullptr, method, params, std::move(callback));
}

void DaemonClient::call(QObject* context,
                        const QString& method,
                        const QJsonObject& params,
                        RpcCallback callback) {
    const bool hasContext = (context != nullptr);
    QPointer<QObject> ctxPtr(context);

    if (!isConnected()) {
        if (!stopping_ && method != QStringLiteral("quit_daemon")) {
            maybeAutoSpawnDaemon();
            if (queuedCalls_.size() < 32) {
                queuedCalls_.append(QueuedCall{
                    method, params, std::move(callback), hasContext, ctxPtr, QDateTime::currentMSecsSinceEpoch()
                });
                if (!reconnectTimer_->isActive()) {
                    reconnectTimer_->start(250);
                }
                if (socket_->state() == QLocalSocket::UnconnectedState) {
                    attemptConnect();
                }
                return;
            }
        }
        invokeIfAlive(hasContext, ctxPtr, callback, false, QJsonValue(),
                      QStringLiteral("clashfx-daemon is starting up, please retry in a moment."),
                      QStringLiteral("NOT_CONNECTED"));
        return;
    }

    const qint64 id = nextId_++;
    pending_.insert(id, PendingCall{method, std::move(callback), hasContext, ctxPtr});

    QJsonObject frame;
    frame.insert(QStringLiteral("id"), id);
    frame.insert(QStringLiteral("method"), method);
    if (!params.isEmpty()) {
        frame.insert(QStringLiteral("params"), params);
    }

    QByteArray raw = QJsonDocument(frame).toJson(QJsonDocument::Compact);
    raw.append('\n');
    socket_->write(raw);
    socket_->flush();
}

void DaemonClient::onReadyRead() {
    readBuffer_.append(socket_->readAll());
    QList<QByteArray> completeLines;
    int offset = 0;
    while (true) {
        const int nl = readBuffer_.indexOf('\n', offset);
        if (nl < 0) {
            break;
        }
        const QByteArray line = readBuffer_.mid(offset, nl - offset).trimmed();
        offset = nl + 1;
        if (!line.isEmpty()) {
            completeLines.append(line);
        }
    }
    if (offset > 0) {
        readBuffer_.remove(0, offset);
    }
    for (const QByteArray& line : completeLines) {
        processLine(line);
    }
}

void DaemonClient::processLine(const QByteArray& line) {
    QJsonParseError parseErr;
    const QJsonDocument doc = QJsonDocument::fromJson(line, &parseErr);
    if (parseErr.error != QJsonParseError::NoError || !doc.isObject()) {
        return;
    }
    const QJsonObject obj = doc.object();

    if (obj.value(QStringLiteral("event")).toString() == QStringLiteral("state_changed")) {
        const QJsonValue st = obj.value(QStringLiteral("state"));
        if (st.isObject()) {
            snapshot_ = st.toObject();
            emit stateChanged(snapshot_);
        }
        return;
    }

    const qint64 id = static_cast<qint64>(obj.value(QStringLiteral("id")).toDouble(0));
    const bool ok = obj.value(QStringLiteral("ok")).toBool(false);
    const QJsonValue data = obj.value(QStringLiteral("data"));
    const QString errStr = obj.value(QStringLiteral("error")).toString();
    const QString errCode = obj.value(QStringLiteral("error_code")).toString();

    if (ok && data.isObject()) {
        const QJsonObject dObj = data.toObject();
        if (dObj.contains(QStringLiteral("settings")) && dObj.contains(QStringLiteral("runtime"))) {
            snapshot_ = dObj;
            emit stateChanged(snapshot_);
        }
    }

    const auto it = pending_.find(id);
    if (it != pending_.end()) {
        const PendingCall callInfo = it.value();
        pending_.erase(it);
        if (!ok && !errStr.isEmpty()) {
            emit rpcErrorOccurred(callInfo.method, errStr, errCode);
        }
        invokeIfAlive(callInfo.hasContext, callInfo.context, callInfo.callback, ok, data, errStr, errCode);
    }
}

} // namespace clashfx
