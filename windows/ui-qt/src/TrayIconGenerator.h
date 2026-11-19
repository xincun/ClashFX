#pragma once

#include <QIcon>
#include <QJsonObject>
#include <QString>

namespace clashfx {

enum class TrayVisualState {
    Disconnected,
    Idle,
    SystemProxy,
    EnhancedTun,
    Alert,
};

class TrayIconGenerator {
public:
    static QIcon appIcon();
    static QIcon generate(const QString& preset,
                          TrayVisualState state,
                          bool aiLocked);

    static QString buildTooltip(bool connected, const QJsonObject& snapshot);
};

} // namespace clashfx
