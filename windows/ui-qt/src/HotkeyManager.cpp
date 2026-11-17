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
constexpr int kHotkeyIdOpenMainWindow = 0x0F01;
constexpr int kHotkeyIdToggleSystemProxy = 0x0F02;
constexpr int kHotkeyIdSwitchOutboundMode = 0x0F03;
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
    if (openRegistered_) {
        UnregisterHotKey(nullptr, kHotkeyIdOpenMainWindow);
        openRegistered_ = false;
    }
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
        } else if (p == QStringLiteral("WIN") || p == QStringLiteral("META") ||
                   p == QStringLiteral("CMD") || p == QStringLiteral("COMMAND") ||
                   p == QStringLiteral("SUPER")) {
            outModifiers |= MOD_WIN;
        } else if (p == QStringLiteral("FN") || p == QStringLiteral("FUNCTION")) {
            // Hardware Fn key has no Win32 RegisterHotKey modifier bit.
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
        } else if (p == QStringLiteral("LEFT") || p == QStringLiteral("ARROWLEFT")) {
            outVk = VK_LEFT;
        } else if (p == QStringLiteral("RIGHT") || p == QStringLiteral("ARROWRIGHT")) {
            outVk = VK_RIGHT;
        } else if (p == QStringLiteral("UP") || p == QStringLiteral("ARROWUP")) {
            outVk = VK_UP;
        } else if (p == QStringLiteral("DOWN") || p == QStringLiteral("ARROWDOWN")) {
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
    const bool isFnKey = (outVk >= VK_F1 && outVk <= VK_F24);
    if (outVk == 0 || (outModifiers == 0 && !isFnKey)) {
        return false;
    }
    outModifiers |= MOD_NOREPEAT;
    return true;
#else
    Q_UNUSED(text);
    return false;
#endif
}

QStringList HotkeyManager::applyHotkeys(const QString& openMainWindow,
                                        const QString& toggleSystemProxy,
                                        const QString& switchOutboundMode) {
    const QString normOpen = openMainWindow.trimmed();
    const QString normToggle = toggleSystemProxy.trimmed();
    const QString normMode = switchOutboundMode.trimmed();
    if (hasAppliedOnce_ &&
        normOpen == currentOpenChord_ &&
        normToggle == currentSysProxyChord_ &&
        normMode == currentModeChord_) {
        return {};
    }
    hasAppliedOnce_ = true;
    currentOpenChord_ = normOpen;
    currentSysProxyChord_ = normToggle;
    currentModeChord_ = normMode;

    if (suspended_) {
        unregisterAll();
        return {};
    }
    return registerConfiguredHotkeys();
}

void HotkeyManager::setSuspended(bool suspended) {
    if (suspended_ == suspended) {
        return;
    }
    suspended_ = suspended;
    if (suspended_) {
        unregisterAll();
    } else if (hasAppliedOnce_) {
        registerConfiguredHotkeys();
    }
}

QStringList HotkeyManager::registerConfiguredHotkeys() {
    unregisterAll();
    QStringList failed;
#ifdef Q_OS_WIN
    struct HotkeyEntry {
        int id;
        const QString& chord;
        bool& registered;
    };
    const HotkeyEntry entries[] = {
        {kHotkeyIdOpenMainWindow, currentOpenChord_, openRegistered_},
        {kHotkeyIdToggleSystemProxy, currentSysProxyChord_, sysProxyRegistered_},
        {kHotkeyIdSwitchOutboundMode, currentModeChord_, modeRegistered_},
    };
    for (const HotkeyEntry& entry : entries) {
        if (entry.chord.isEmpty()) {
            continue;
        }
        unsigned int mods = 0;
        unsigned int vk = 0;
        if (parseChord(entry.chord, mods, vk)) {
            entry.registered = (RegisterHotKey(nullptr, entry.id, mods, vk) != FALSE);
        } else {
            entry.registered = false;
        }
        if (!entry.registered) {
            failed.append(entry.chord);
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
            if (msg->wParam == kHotkeyIdOpenMainWindow) {
                emit openMainWindowTriggered();
                return true;
            }
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
