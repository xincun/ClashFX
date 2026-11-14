#pragma once

class QApplication;
class QWidget;

namespace clashfx::Win11Dwm {

void applyDarkTitleBar(QWidget* window, bool dark = true);
void applyFramelessRoundedChrome(QWidget* window, bool dark = true);
void installGlobalDarkTitleBarFilter(QApplication& app);

} // namespace clashfx::Win11Dwm
