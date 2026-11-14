#include "ModernWidgets.h"

#include <QApplication>
#include <QEvent>
#include <QMenu>
#include <QObject>
#include <QWidget>

#ifdef Q_OS_WIN
#include <windows.h>
#include <dwmapi.h>

#ifndef DWMWA_USE_IMMERSIVE_DARK_MODE
#define DWMWA_USE_IMMERSIVE_DARK_MODE 20
#endif
#ifndef DWMWA_WINDOW_CORNER_PREFERENCE
#define DWMWA_WINDOW_CORNER_PREFERENCE 33
#endif
#ifndef DWMWA_BORDER_COLOR
#define DWMWA_BORDER_COLOR 34
#endif
#ifndef DWMWA_CAPTION_COLOR
#define DWMWA_CAPTION_COLOR 35
#endif
#ifndef DWMWA_TEXT_COLOR
#define DWMWA_TEXT_COLOR 36
#endif
#endif

namespace clashfx::Win11Dwm {
namespace {

class DarkTitleBarEventFilter final : public QObject {
public:
    explicit DarkTitleBarEventFilter(QObject* parent = nullptr)
        : QObject(parent) {}

protected:
    bool eventFilter(QObject* watched, QEvent* event) override {
        const QEvent::Type t = event->type();
        if (t == QEvent::Show || t == QEvent::WinIdChange) {
            if (auto* w = qobject_cast<QWidget*>(watched)) {
                if (w->isWindow() && !qobject_cast<QMenu*>(w)) {
                    const Qt::WindowFlags flags = w->windowFlags();
                    const Qt::WindowType wt = w->windowType();
                    if ((wt == Qt::Window || wt == Qt::Dialog || wt == Qt::Sheet) &&
                        !(flags & Qt::FramelessWindowHint) &&
                        wt != Qt::Popup &&
                        wt != Qt::ToolTip) {
                        applyDarkTitleBar(w, true);
                    }
                }
            }
        }
        return QObject::eventFilter(watched, event);
    }
};

} // namespace

void applyDarkTitleBar(QWidget* window, bool dark) {
#ifdef Q_OS_WIN
    if (!window) return;
    HWND hwnd = reinterpret_cast<HWND>(window->winId());
    if (!hwnd) return;
    BOOL useDark = dark ? TRUE : FALSE;
    DwmSetWindowAttribute(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, &useDark, sizeof(useDark));
    COLORREF captionColor = dark ? RGB(0x0b, 0x0d, 0x12) : RGB(0xf5, 0xf7, 0xfb);
    COLORREF textColor = dark ? RGB(0xf1, 0xf5, 0xf9) : RGB(0x0f, 0x17, 0x2a);
    COLORREF borderColor = dark ? RGB(0x26, 0x2a, 0x36) : RGB(0xd6, 0xdc, 0xe8);
    DwmSetWindowAttribute(hwnd, DWMWA_CAPTION_COLOR, &captionColor, sizeof(captionColor));
    DwmSetWindowAttribute(hwnd, DWMWA_TEXT_COLOR, &textColor, sizeof(textColor));
    DwmSetWindowAttribute(hwnd, DWMWA_BORDER_COLOR, &borderColor, sizeof(borderColor));
#else
    Q_UNUSED(window);
    Q_UNUSED(dark);
#endif
}

void applyFramelessRoundedChrome(QWidget* window, bool dark) {
#ifdef Q_OS_WIN
    if (!window) return;
    HWND hwnd = reinterpret_cast<HWND>(window->winId());
    if (!hwnd) return;

    // Enable native Aero Snap, minimize/maximize animations, and system menu on frameless window
    LONG_PTR style = GetWindowLongPtrW(hwnd, GWL_STYLE);
    const LONG_PTR desired = (style & ~static_cast<LONG_PTR>(WS_POPUP)) |
                             WS_OVERLAPPED | WS_THICKFRAME | WS_CAPTION |
                             WS_MAXIMIZEBOX | WS_MINIMIZEBOX | WS_SYSMENU;
    if (style != desired) {
        SetWindowLongPtrW(hwnd, GWL_STYLE, desired);
        SetWindowPos(hwnd, nullptr, 0, 0, 0, 0,
                     SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE | SWP_FRAMECHANGED);
    }

    BOOL useDark = dark ? TRUE : FALSE;
    DwmSetWindowAttribute(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, &useDark, sizeof(useDark));

    const DWORD roundPref = 2; // DWMWCP_ROUND
    DwmSetWindowAttribute(hwnd, DWMWA_WINDOW_CORNER_PREFERENCE, &roundPref, sizeof(roundPref));

    COLORREF borderColor = dark ? RGB(0x28, 0x2d, 0x3b) : RGB(0xd4, 0xda, 0xe6);
    DwmSetWindowAttribute(hwnd, DWMWA_BORDER_COLOR, &borderColor, sizeof(borderColor));

    // Subtle 1px DWM frame extension gives Windows 11 native window shadow
    const MARGINS shadow = {0, 0, 0, 1};
    DwmExtendFrameIntoClientArea(hwnd, &shadow);
#else
    Q_UNUSED(window);
    Q_UNUSED(dark);
#endif
}

void installGlobalDarkTitleBarFilter(QApplication& app) {
    app.installEventFilter(new DarkTitleBarEventFilter(&app));
}

} // namespace clashfx::Win11Dwm
