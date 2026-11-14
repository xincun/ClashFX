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
    stopReconnect();
}

void DaemonClient::start() {
    stopping_ = false;
    attemptConnect();
}

void DaemonClient::stopReconnect() {
    stopping_ = true;
    reconnectTimer_->stop();
    const auto stale = queuedCalls_;
    queuedCalls_.clear();
    for (const auto& q : stale) {
        if (q.callback) {
            q.callback(false, QJsonValue(), QStringLiteral("clashfx-daemon client stopped"), QStringLiteral("NOT_CONNECTED"));
        }
    }
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
    call(QStringLiteral("subscribe"), QJsonObject(), [this](bool ok, const QJsonValue& data, const QString&, const QString&) {
        if (ok && data.isObject()) {
            snapshot_ = data.toObject();
            emit stateChanged(snapshot_);
        }
    });
    const auto queued = queuedCalls_;
    queuedCalls_.clear();
    for (const auto& q : queued) {
        call(q.method, q.params, q.callback);
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
        if (nowMs - q.queuedEpochMs >= 4500) {
            if (q.callback) {
                q.callback(false, QJsonValue(), QStringLiteral("clashfx-daemon is starting up, please retry in a moment."), QStringLiteral("NOT_CONNECTED"));
            }
        } else {
            keep.append(q);
        }
    }
    queuedCalls_ = std::move(keep);
}

void DaemonClient::onDisconnected() {
    const auto callbacks = pending_;
    pending_.clear();
    for (auto it = callbacks.cbegin(); it != callbacks.cend(); ++it) {
        if (it.value().callback) {
            it.value().callback(false, QJsonValue(), QStringLiteral("Disconnected from clashfx-daemon"), QString());
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
    if (!isConnected()) {
        maybeAutoSpawnDaemon();
        if (!stopping_ && queuedCalls_.size() < 32) {
            queuedCalls_.append(QueuedCall{method, params, std::move(callback), QDateTime::currentMSecsSinceEpoch()});
            if (!reconnectTimer_->isActive()) {
                reconnectTimer_->start(250);
            }
            if (socket_->state() == QLocalSocket::UnconnectedState) {
                attemptConnect();
            }
            return;
        }
        if (callback) {
            callback(false, QJsonValue(), QStringLiteral("clashfx-daemon is starting up, please retry in a moment."), QStringLiteral("NOT_CONNECTED"));
        }
        return;
    }

    const qint64 id = nextId_++;
    pending_.insert(id, PendingCall{method, std::move(callback)});

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
    int offset = 0;
    while (true) {
        const int nl = readBuffer_.indexOf('\n', offset);
        if (nl < 0) {
            break;
        }
        const QByteArray line = readBuffer_.mid(offset, nl - offset).trimmed();
        offset = nl + 1;
        if (!line.isEmpty()) {
            processLine(line);
        }
    }
    if (offset > 0) {
        readBuffer_.remove(0, offset);
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
        if (callInfo.callback) {
            callInfo.callback(ok, data, errStr, errCode);
        }
    }
}

} // namespace clashfx
