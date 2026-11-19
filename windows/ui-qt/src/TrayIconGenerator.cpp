#include "TrayIconGenerator.h"
#include "I18n.h"

#include <QColor>
#include <QImage>
#include <QJsonArray>
#include <QPainter>
#include <QPainterPath>
#include <QPixmap>

namespace clashfx {

namespace {

QImage tintTemplateImage(const QString& resPath, int size, const QColor& color) {
    QImage src(resPath);
    if (src.isNull()) {
        QImage fallback(size, size, QImage::Format_ARGB32_Premultiplied);
        fallback.fill(Qt::transparent);
        return fallback;
    }
    QImage scaled = src.scaled(size, size, Qt::KeepAspectRatio, Qt::SmoothTransformation)
                        .convertToFormat(QImage::Format_ARGB32_Premultiplied);
    QPainter p(&scaled);
    p.setCompositionMode(QPainter::CompositionMode_SourceIn);
    p.fillRect(scaled.rect(), color);
    p.end();
    return scaled;
}

QPixmap renderTrayFrame(int size,
                        const QString& preset,
                        TrayVisualState state,
                        bool aiLocked) {
    QImage canvas(size, size, QImage::Format_ARGB32_Premultiplied);
    canvas.fill(Qt::transparent);
    QPainter p(&canvas);
    p.setRenderHint(QPainter::Antialiasing, true);
    p.setRenderHint(QPainter::SmoothPixmapTransform, true);

    const QString normPreset = preset.trimmed().toLower();
    const bool isMono = (normPreset == QStringLiteral("monochrome") || normPreset == QStringLiteral("lightning"));
    const bool isOcean = (normPreset == QStringLiteral("ocean") || normPreset == QStringLiteral("shield"));

    QColor bgColor;
    QColor fgColor(Qt::white);
    QString iconRes = QStringLiteral(":/clashfx/icons/menu-cat-filled-face.png");

    if (isMono) {
        iconRes = (state == TrayVisualState::EnhancedTun || state == TrayVisualState::SystemProxy)
                      ? QStringLiteral(":/clashfx/icons/menu-cat-lightning-solid.png")
                      : QStringLiteral(":/clashfx/icons/menu-cat-lightning-outline.png");
        switch (state) {
        case TrayVisualState::Disconnected:
            bgColor = QColor(90, 95, 105, 220);
            fgColor = QColor(210, 214, 220);
            break;
        case TrayVisualState::Idle:
            bgColor = QColor(52, 58, 64, 235);
            fgColor = QColor(240, 244, 248);
            break;
        case TrayVisualState::SystemProxy:
            bgColor = QColor(28, 32, 38, 250);
            fgColor = QColor(116, 192, 252);
            break;
        case TrayVisualState::EnhancedTun:
            bgColor = QColor(20, 24, 28, 255);
            fgColor = QColor(99, 230, 190);
            break;
        case TrayVisualState::Alert:
            bgColor = QColor(220, 38, 38, 255);
            fgColor = QColor(255, 255, 255);
            break;
        }
    } else if (isOcean) {
        iconRes = (state == TrayVisualState::EnhancedTun)
                      ? QStringLiteral(":/clashfx/icons/menu-cat-shield.png")
                      : QStringLiteral(":/clashfx/icons/menu-cat-network.png");
        switch (state) {
        case TrayVisualState::Disconnected:
            bgColor = QColor(108, 117, 125, 225);
            break;
        case TrayVisualState::Idle:
            bgColor = QColor(73, 80, 87, 235);
            break;
        case TrayVisualState::SystemProxy:
            bgColor = QColor(15, 118, 178, 250);
            break;
        case TrayVisualState::EnhancedTun:
            bgColor = QColor(12, 133, 153, 255);
            break;
        case TrayVisualState::Alert:
            bgColor = QColor(220, 38, 38, 255);
            break;
        }
    } else {
        iconRes = (state == TrayVisualState::Idle || state == TrayVisualState::Disconnected)
                      ? QStringLiteral(":/clashfx/icons/menu-cat-outline-face.png")
                      : QStringLiteral(":/clashfx/icons/menu-cat-filled-face.png");
        switch (state) {
        case TrayVisualState::Disconnected:
            bgColor = QColor(120, 124, 130, 220);
            break;
        case TrayVisualState::Idle:
            bgColor = QColor(78, 86, 98, 235);
            break;
        case TrayVisualState::SystemProxy:
            bgColor = QColor(37, 99, 235, 250); // Royal blue
            break;
        case TrayVisualState::EnhancedTun:
            bgColor = QColor(16, 185, 129, 255); // Emerald TUN active
            break;
        case TrayVisualState::Alert:
            bgColor = QColor(220, 38, 38, 255); // Crimson alert
            break;
        }
    }

    const qreal pad = size * 0.04;
    const qreal radius = size * 0.24;
    p.setPen(Qt::NoPen);
    p.setBrush(bgColor);
    p.drawRoundedRect(QRectF(pad, pad, size - pad * 2.0, size - pad * 2.0), radius, radius);

    const int catSize = qMax(10, static_cast<int>(size * 0.72));
    const QImage catImg = tintTemplateImage(iconRes, catSize, fgColor);
    const int offsetX = (size - catImg.width()) / 2;
    const int offsetY = (size - catImg.height()) / 2;
    p.drawImage(offsetX, offsetY, catImg);

    if (aiLocked) {
        const qreal badgeR = qMax(3.0, size * 0.20);
        const QPointF center(size - badgeR - pad * 0.5, size - badgeR - pad * 0.5);
        p.setPen(QPen(QColor(255, 255, 255), qMax(1.0, size * 0.05)));
        p.setBrush(QColor(245, 158, 11)); // Amber lock indicator
        p.drawEllipse(center, badgeR, badgeR);
    }

    p.end();
    return QPixmap::fromImage(canvas);
}

} // namespace

QIcon TrayIconGenerator::appIcon() {
    QIcon icon(QStringLiteral(":/clashfx/icons/app-icon-256.png"));
    return icon;
}

QIcon TrayIconGenerator::generate(const QString& preset,
                                  TrayVisualState state,
                                  bool aiLocked) {
    QIcon icon;
    for (int sz : {16, 20, 24, 32, 48, 64}) {
        icon.addPixmap(renderTrayFrame(sz, preset, state, aiLocked));
    }
    return icon;
}

QString TrayIconGenerator::buildTooltip(bool connected, const QJsonObject& snapshot) {
    if (!connected || snapshot.isEmpty()) {
        return L("ClashFX — 正在连接后台守护进程...", "ClashFX — Connecting to background daemon...");
    }

    const QJsonObject settings = snapshot.value(QStringLiteral("settings")).toObject();
    const QJsonObject runtime = snapshot.value(QStringLiteral("runtime")).toObject();
    const bool coreRunning = snapshot.value(QStringLiteral("core_running")).toBool(false);
    const QJsonArray profiles = snapshot.value(QStringLiteral("profiles")).toArray();
    const QString profile = profiles.isEmpty()
        ? L("未导入订阅", "No Subscription")
        : settings.value(QStringLiteral("selected_profile")).toString(profiles.first().toString());
    const QString mode = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toUpper();
    const int mixedPort = runtime.value(QStringLiteral("mixed_port")).toInt(7890);
    const bool sysProxy = settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    const bool enhanced = settings.value(QStringLiteral("enhanced_mode")).toBool(false);
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    const QString aiTarget = settings.value(QStringLiteral("claudeProxyLockTarget")).toString();
    const QString suspendedNet = snapshot.value(QStringLiteral("suspended_by_network")).toString();
    const QString coreHealth = snapshot.value(QStringLiteral("core_health")).toString();
    const QJsonObject aiGuard = snapshot.value(QStringLiteral("ai_guard")).toObject();
    const QString guardMode = aiGuard.value(QStringLiteral("mode")).toString();

    QStringList lines;
    lines << QStringLiteral("ClashFX (%1)").arg(snapshot.value(QStringLiteral("arch")).toString());
    if (!coreRunning) {
        if (coreHealth == QStringLiteral("restarting")) {
            lines << L("状态: 内核自动恢复中…", "Status: Core Recovering…");
        } else if (coreHealth == QStringLiteral("crash_loop")) {
            lines << L("⚠ 内核异常停止 (请点击修复)", "⚠ Core Crash Loop (Click to Fix)");
        } else {
            lines << L("状态: 内核未运行 (请检查配置)", "Status: Core Stopped (Check Config)");
        }
    }
    if (aiLocked && guardMode == QStringLiteral("blocking_leaks")) {
        lines << L("🛡 AI 防漏网护栏: 已阻断直连", "🛡 AI Leak Guard: Blocking Direct");
    }
    lines << L("配置: ", "Profile: ") + profile + QStringLiteral("  |  ") +
                 L("模式: ", "Mode: ") + mode;
    lines << L("混合端口: ", "Mixed Port: ") + QString::number(mixedPort);

    if (!suspendedNet.isEmpty()) {
        lines << L("系统代理: 已自动暂停 (", "System Proxy: Paused (") + suspendedNet + QStringLiteral(")");
    } else {
        const bool armedStandby = profiles.isEmpty() && !aiLocked;
        const QString onLabel = armedStandby ? L("待导入订阅", "Pending Sub") : L("已开启", "On");
        lines << L("系统代理: ", "System Proxy: ") + (sysProxy ? onLabel : L("已关闭", "Off")) +
                     QStringLiteral("  |  TUN: ") + (enhanced ? onLabel : L("已关闭", "Off"));
    }

    if (aiLocked) {
        lines << L("AI 专用锁定: ", "AI Proxy Lock: ") + aiTarget;
    }

    // Windows NotifyIcon szTip max is 127 WCHARs; join and safely clamp to 124 chars
    QString full = lines.join(QLatin1Char('\n'));
    if (full.size() > 124) {
        full = full.left(121) + QStringLiteral("...");
    }
    return full;
}

} // namespace clashfx
