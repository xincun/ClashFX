#include "HotkeyManager.h"

#include <QCoreApplication>
#include <QStringList>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#endif

namespace clashfx {

namespace {
constexpr int kHotkeyIdToggleSystemProxy = 0xCF01;
constexpr int kHotkeyIdSwitchOutboundMode = 0xCF02;
} // namespace

HotkeyManager::HotkeyManager(QObject* parent)
    : QObject(parent) {
    if (qApp) {
        qApp->installNativeEventFilter(this);
    }
}

HotkeyManager::~HotkeyManager() {
    unregisterAll();
    if (qApp) {
        qApp->removeNativeEventFilter(this);
    }
}

void HotkeyManager::unregisterAll() {
#ifdef Q_OS_WIN
    if (sysProxyRegistered_) {
        UnregisterHotKey(nullptr, kHotkeyIdToggleSystemProxy);
        sysProxyRegistered_ = false;
    }
    if (modeRegistered_) {
        UnregisterHotKey(nullptr, kHotkeyIdSwitchOutboundMode);
        modeRegistered_ = false;
    }
#endif
}

bool HotkeyManager::parseChord(const QString& text, unsigned int& outModifiers, unsigned int& outVk) {
    outModifiers = 0;
    outVk = 0;
#ifdef Q_OS_WIN
    QString trimmed = text.trimmed();
    if (trimmed.isEmpty()) {
        return false;
    }
    if (trimmed.endsWith(QStringLiteral("++"))) {
        outVk = VK_OEM_PLUS;
        trimmed.chop(1);
    }
    const QStringList parts = trimmed.split(QLatin1Char('+'), Qt::SkipEmptyParts);
    for (const QString& rawPart : parts) {
        const QString p = rawPart.trimmed().toUpper();
        if (p == QStringLiteral("CTRL") || p == QStringLiteral("CONTROL")) {
            outModifiers |= MOD_CONTROL;
        } else if (p == QStringLiteral("ALT")) {
            outModifiers |= MOD_ALT;
        } else if (p == QStringLiteral("SHIFT")) {
            outModifiers |= MOD_SHIFT;
        } else if (p == QStringLiteral("WIN") || p == QStringLiteral("META")) {
            outModifiers |= MOD_WIN;
        } else if (p.size() == 1 && p[0].unicode() >= 'A' && p[0].unicode() <= 'Z') {
            outVk = static_cast<unsigned int>(p[0].unicode());
        } else if (p.size() == 1 && p[0].unicode() >= '0' && p[0].unicode() <= '9') {
            outVk = static_cast<unsigned int>(p[0].unicode());
        } else if (p.startsWith(QLatin1Char('F')) && p.size() >= 2) {
            bool ok = false;
            const int fn = p.mid(1).toInt(&ok);
            if (ok && fn >= 1 && fn <= 24) {
                outVk = static_cast<unsigned int>(VK_F1 + (fn - 1));
            }
        } else if (p == QStringLiteral("SPACE")) {
            outVk = VK_SPACE;
        } else if (p == QStringLiteral("TAB")) {
            outVk = VK_TAB;
        } else if (p == QStringLiteral("BACKSPACE")) {
            outVk = VK_BACK;
        } else if (p == QStringLiteral("RETURN") || p == QStringLiteral("ENTER")) {
            outVk = VK_RETURN;
        } else if (p == QStringLiteral("ESC") || p == QStringLiteral("ESCAPE")) {
            outVk = VK_ESCAPE;
        } else if (p == QStringLiteral("INS") || p == QStringLiteral("INSERT")) {
            outVk = VK_INSERT;
        } else if (p == QStringLiteral("DEL") || p == QStringLiteral("DELETE")) {
            outVk = VK_DELETE;
        } else if (p == QStringLiteral("HOME")) {
            outVk = VK_HOME;
        } else if (p == QStringLiteral("END")) {
            outVk = VK_END;
        } else if (p == QStringLiteral("PGUP") || p == QStringLiteral("PAGEUP")) {
            outVk = VK_PRIOR;
        } else if (p == QStringLiteral("PGDOWN") || p == QStringLiteral("PAGEDOWN")) {
            outVk = VK_NEXT;
        } else if (p == QStringLiteral("LEFT")) {
            outVk = VK_LEFT;
        } else if (p == QStringLiteral("RIGHT")) {
            outVk = VK_RIGHT;
        } else if (p == QStringLiteral("UP")) {
            outVk = VK_UP;
        } else if (p == QStringLiteral("DOWN")) {
            outVk = VK_DOWN;
        } else if (p == QStringLiteral("-") || p == QStringLiteral("MINUS")) {
            outVk = VK_OEM_MINUS;
        } else if (p == QStringLiteral("=") || p == QStringLiteral("EQUAL")) {
            outVk = VK_OEM_PLUS;
        } else if (p == QStringLiteral("[") || p == QStringLiteral("{")) {
            outVk = VK_OEM_4;
        } else if (p == QStringLiteral("]") || p == QStringLiteral("}")) {
            outVk = VK_OEM_6;
        } else if (p == QStringLiteral("\\") || p == QStringLiteral("|")) {
            outVk = VK_OEM_5;
        } else if (p == QStringLiteral(";") || p == QStringLiteral(":")) {
            outVk = VK_OEM_1;
        } else if (p == QStringLiteral("'") || p == QStringLiteral("\"")) {
            outVk = VK_OEM_7;
        } else if (p == QStringLiteral(",") || p == QStringLiteral("<")) {
            outVk = VK_OEM_COMMA;
        } else if (p == QStringLiteral(".") || p == QStringLiteral(">")) {
            outVk = VK_OEM_PERIOD;
        } else if (p == QStringLiteral("/") || p == QStringLiteral("?")) {
            outVk = VK_OEM_2;
        } else if (p == QStringLiteral("`") || p == QStringLiteral("~")) {
            outVk = VK_OEM_3;
        }
    }
    if (outModifiers == 0 || outVk == 0) {
        return false;
    }
    outModifiers |= MOD_NOREPEAT;
    return true;
#else
    Q_UNUSED(text);
    return false;
#endif
}

QStringList HotkeyManager::applyHotkeys(const QString& toggleSystemProxy,
                                        const QString& switchOutboundMode) {
    QStringList failed;
    if (toggleSystemProxy.trimmed() == currentSysProxyChord_ &&
        switchOutboundMode.trimmed() == currentModeChord_) {
        return failed;
    }
    unregisterAll();
    currentSysProxyChord_ = toggleSystemProxy.trimmed();
    currentModeChord_ = switchOutboundMode.trimmed();

#ifdef Q_OS_WIN
    unsigned int mods = 0;
    unsigned int vk = 0;
    if (!currentSysProxyChord_.isEmpty()) {
        if (parseChord(currentSysProxyChord_, mods, vk)) {
            sysProxyRegistered_ = (RegisterHotKey(nullptr, kHotkeyIdToggleSystemProxy, mods, vk) != FALSE);
        } else {
            sysProxyRegistered_ = false;
        }
        if (!sysProxyRegistered_) {
            failed.append(currentSysProxyChord_);
        }
    }
    if (!currentModeChord_.isEmpty()) {
        if (parseChord(currentModeChord_, mods, vk)) {
            modeRegistered_ = (RegisterHotKey(nullptr, kHotkeyIdSwitchOutboundMode, mods, vk) != FALSE);
        } else {
            modeRegistered_ = false;
        }
        if (!modeRegistered_) {
            failed.append(currentModeChord_);
        }
    }
#endif
    return failed;
}

bool HotkeyManager::nativeEventFilter(const QByteArray& eventType,
                                      void* message,
                                      qintptr* /*result*/) {
#ifdef Q_OS_WIN
    if (eventType == "windows_generic_MSG" || eventType == "windows_dispatcher_MSG") {
        const MSG* msg = static_cast<const MSG*>(message);
        if (msg && msg->message == WM_HOTKEY) {
            if (msg->wParam == kHotkeyIdToggleSystemProxy) {
                emit toggleSystemProxyTriggered();
                return true;
            }
            if (msg->wParam == kHotkeyIdSwitchOutboundMode) {
                emit switchOutboundModeTriggered();
                return true;
            }
        }
    }
#else
    Q_UNUSED(eventType);
    Q_UNUSED(message);
#endif
    return false;
}

} // namespace clashfx
