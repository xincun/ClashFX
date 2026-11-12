#pragma once

#include <QAbstractNativeEventFilter>
#include <QObject>
#include <QString>

namespace clashfx {

class HotkeyManager final : public QObject, public QAbstractNativeEventFilter {
    Q_OBJECT
public:
    explicit HotkeyManager(QObject* parent = nullptr);
    ~HotkeyManager() override;

    void applyHotkeys(const QString& toggleSystemProxy,
                      const QString& switchOutboundMode);

    bool nativeEventFilter(const QByteArray& eventType,
                           void* message,
                           qintptr* result) override;

signals:
    void toggleSystemProxyTriggered();
    void switchOutboundModeTriggered();

private:
    void unregisterAll();
    static bool parseChord(const QString& text, unsigned int& outModifiers, unsigned int& outVk);

    QString currentSysProxyChord_;
    QString currentModeChord_;
    bool sysProxyRegistered_ = false;
    bool modeRegistered_ = false;
};

} // namespace clashfx
