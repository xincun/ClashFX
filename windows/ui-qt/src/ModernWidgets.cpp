#include "ModernWidgets.h"

#include <QAbstractItemView>
#include <QApplication>
#include <QEasingCurve>
#include <QEvent>
#include <QFontMetrics>
#include <QLinearGradient>
#include <QMenu>
#include <QMouseEvent>
#include <QPainter>
#include <QPainterPath>
#include <QSignalBlocker>
#include <QTimer>

#ifdef Q_OS_WIN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <dwmapi.h>
#ifndef DWMWA_USE_IMMERSIVE_DARK_MODE
#define DWMWA_USE_IMMERSIVE_DARK_MODE 20
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

namespace clashfx {

namespace {

struct TonePalette {
    QColor bg;
    QColor fg;
    QColor border;
};

TonePalette paletteForTone(PillTone tone) {
    switch (tone) {
    case PillTone::Green:
        return {QColor(16, 185, 129, 42), QColor(0x34, 0xd3, 0x99), QColor(16, 185, 129, 92)};
    case PillTone::Blue:
        return {QColor(59, 130, 246, 42), QColor(0x60, 0xa5, 0xfa), QColor(59, 130, 246, 92)};
    case PillTone::Violet:
        return {QColor(139, 92, 246, 48), QColor(0xc4, 0xb5, 0xfd), QColor(139, 92, 246, 96)};
    case PillTone::Amber:
        return {QColor(245, 158, 11, 42), QColor(0xfb, 0xbf, 0x24), QColor(245, 158, 11, 92)};
    case PillTone::Red:
        return {QColor(239, 68, 68, 42), QColor(0xf8, 0x71, 0x71), QColor(239, 68, 68, 92)};
    case PillTone::Muted:
    default:
        return {QColor(0x23, 0x25, 0x30), QColor(0x94, 0xa3, 0xb8), QColor(0x30, 0x33, 0x42)};
    }
}

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
                        Win11Dwm::applyDarkTitleBar(w);
                    }
                }
            }
        }
        return QObject::eventFilter(watched, event);
    }
};

} // namespace

QString pillStyleSheet(PillTone tone, bool compact) {
    const TonePalette p = paletteForTone(tone);
    const QString bgRgba = QStringLiteral("rgba(%1,%2,%3,%4)")
                               .arg(p.bg.red())
                               .arg(p.bg.green())
                               .arg(p.bg.blue())
                               .arg(p.bg.alpha());
    const QString borderRgba = QStringLiteral("rgba(%1,%2,%3,%4)")
                                   .arg(p.border.red())
                                   .arg(p.border.green())
                                   .arg(p.border.blue())
                                   .arg(p.border.alpha());
    return QStringLiteral(
               "background-color: %1; color: %2; border: 1px solid %3; "
               "border-radius: %4px; padding: %5; font-size: %6px; font-weight: 600;")
        .arg(bgRgba,
             p.fg.name(),
             borderRgba)
        .arg(compact ? 8 : 10)
        .arg(compact ? QStringLiteral("1px 7px") : QStringLiteral("2px 9px"))
        .arg(compact ? 11 : 11);
}

QPixmap renderVectorIcon(VectorIcon kind, int logicalSize, const QColor& color, qreal dpr) {
    const qreal scale = qMax<qreal>(1.0, dpr);
    const int px = qRound(logicalSize * scale);
    QPixmap pm(px, px);
    pm.setDevicePixelRatio(scale);
    pm.fill(Qt::transparent);

    QPainter p(&pm);
    p.setRenderHint(QPainter::Antialiasing, true);
    p.scale(logicalSize / 24.0, logicalSize / 24.0);

    QPen pen(color, 1.85, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin);
    p.setPen(pen);
    p.setBrush(Qt::NoBrush);

    switch (kind) {
    case VectorIcon::BrandBolt:
    case VectorIcon::Zap: {
        QPainterPath path;
        path.moveTo(13.5, 2.5);
        path.lineTo(4.5, 14.0);
        path.lineTo(11.5, 14.0);
        path.lineTo(10.5, 21.5);
        path.lineTo(19.5, 10.0);
        path.lineTo(12.5, 10.0);
        path.closeSubpath();
        if (kind == VectorIcon::BrandBolt) {
            p.setPen(Qt::NoPen);
            p.setBrush(color);
        }
        p.drawPath(path);
        break;
    }
    case VectorIcon::Globe: {
        p.drawEllipse(QPointF(12, 12), 9, 9);
        p.drawLine(QPointF(3.3, 12), QPointF(20.7, 12));
        p.drawEllipse(QPointF(12, 12), 4.2, 9);
        break;
    }
    case VectorIcon::Refresh: {
        p.drawArc(QRectF(4, 4, 16, 16), 35 * 16, 265 * 16);
        QPainterPath arrow;
        arrow.moveTo(17.5, 4.5);
        arrow.lineTo(20.0, 8.2);
        arrow.lineTo(15.6, 8.8);
        p.drawPath(arrow);
        break;
    }
    case VectorIcon::Dashboard: {
        p.drawRoundedRect(QRectF(3.5, 3.5, 7.2, 7.2), 1.8, 1.8);
        p.drawRoundedRect(QRectF(13.3, 3.5, 7.2, 7.2), 1.8, 1.8);
        p.drawRoundedRect(QRectF(3.5, 13.3, 7.2, 7.2), 1.8, 1.8);
        p.drawRoundedRect(QRectF(13.3, 13.3, 7.2, 7.2), 1.8, 1.8);
        break;
    }
    case VectorIcon::Subscriptions: {
        QPainterPath top;
        top.moveTo(12, 3.5);
        top.lineTo(20.5, 8.0);
        top.lineTo(12, 12.5);
        top.lineTo(3.5, 8.0);
        top.closeSubpath();
        p.drawPath(top);
        p.drawLine(QPointF(3.5, 12.5), QPointF(12, 17.0));
        p.drawLine(QPointF(12, 17.0), QPointF(20.5, 12.5));
        p.drawLine(QPointF(3.5, 16.5), QPointF(12, 21.0));
        p.drawLine(QPointF(12, 21.0), QPointF(20.5, 16.5));
        break;
    }
    case VectorIcon::Rules: {
        p.drawLine(QPointF(4, 7.5), QPointF(19.5, 7.5));
        p.drawLine(QPointF(16.0, 4.3), QPointF(19.8, 7.5));
        p.drawLine(QPointF(16.0, 10.7), QPointF(19.8, 7.5));
        p.drawLine(QPointF(20, 16.5), QPointF(4.5, 16.5));
        p.drawLine(QPointF(8.0, 13.3), QPointF(4.2, 16.5));
        p.drawLine(QPointF(8.0, 19.7), QPointF(4.2, 16.5));
        break;
    }
    case VectorIcon::AiLock: {
        QPainterPath shield;
        shield.moveTo(12, 3.0);
        shield.lineTo(19.5, 6.2);
        shield.lineTo(19.5, 12.2);
        shield.quadTo(19.5, 18.0, 12, 21.2);
        shield.quadTo(4.5, 18.0, 4.5, 12.2);
        shield.lineTo(4.5, 6.2);
        shield.closeSubpath();
        p.drawPath(shield);
        p.drawLine(QPointF(9.2, 12.2), QPointF(11.3, 14.3));
        p.drawLine(QPointF(11.3, 14.3), QPointF(15.2, 10.0));
        break;
    }
    case VectorIcon::MultiNode: {
        p.drawEllipse(QPointF(6.5, 6.5), 2.5, 2.5);
        p.drawEllipse(QPointF(17.5, 6.5), 2.5, 2.5);
        p.drawEllipse(QPointF(12.0, 17.5), 2.5, 2.5);
        p.drawLine(QPointF(8.8, 7.7), QPointF(15.2, 7.7));
        p.drawLine(QPointF(7.7, 8.7), QPointF(10.8, 15.2));
        p.drawLine(QPointF(16.3, 8.7), QPointF(13.2, 15.2));
        break;
    }
    case VectorIcon::Diagnostics: {
        p.drawRoundedRect(QRectF(3.5, 4.5, 17, 15), 3.0, 3.0);
        QPainterPath pulse;
        pulse.moveTo(5.8, 12.5);
        pulse.lineTo(9.0, 12.5);
        pulse.lineTo(10.8, 8.2);
        pulse.lineTo(13.4, 16.0);
        pulse.lineTo(15.2, 12.5);
        pulse.lineTo(18.2, 12.5);
        p.drawPath(pulse);
        break;
    }
    case VectorIcon::Settings: {
        p.drawLine(QPointF(4, 7), QPointF(20, 7));
        p.drawLine(QPointF(4, 12), QPointF(20, 12));
        p.drawLine(QPointF(4, 17), QPointF(20, 17));
        p.setBrush(color);
        p.drawEllipse(QPointF(8.5, 7), 2.1, 2.1);
        p.drawEllipse(QPointF(15.5, 12), 2.1, 2.1);
        p.drawEllipse(QPointF(10.5, 17), 2.1, 2.1);
        break;
    }
    }
    return pm;
}

namespace Win11Dwm {

void applyDarkTitleBar(QWidget* window) {
#ifdef Q_OS_WIN
    if (!window) return;
    HWND hwnd = reinterpret_cast<HWND>(window->winId());
    if (!hwnd) return;
    BOOL useDark = TRUE;
    DwmSetWindowAttribute(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE, &useDark, sizeof(useDark));
    COLORREF captionColor = RGB(0x10, 0x10, 0x14);
    COLORREF textColor = RGB(0xf4, 0xf4, 0xf5);
    COLORREF borderColor = RGB(0x2b, 0x2d, 0x38);
    DwmSetWindowAttribute(hwnd, DWMWA_CAPTION_COLOR, &captionColor, sizeof(captionColor));
    DwmSetWindowAttribute(hwnd, DWMWA_TEXT_COLOR, &textColor, sizeof(textColor));
    DwmSetWindowAttribute(hwnd, DWMWA_BORDER_COLOR, &borderColor, sizeof(borderColor));
#else
    Q_UNUSED(window);
#endif
}

void installGlobalDarkTitleBarFilter(QApplication& app) {
    app.installEventFilter(new DarkTitleBarEventFilter(&app));
}

} // namespace Win11Dwm

// ============================================================================
// BadgePill
// ============================================================================

BadgePill::BadgePill(QWidget* parent, bool compact)
    : QLabel(parent), compact_(compact) {
    setSizePolicy(QSizePolicy::Fixed, QSizePolicy::Fixed);
    setAlignment(Qt::AlignCenter);
    setPill(QString(), PillTone::Muted);
}

void BadgePill::setPill(const QString& text, PillTone tone) {
    tone_ = tone;
    setText(text);
    const bool vis = !text.trimmed().isEmpty();
    setVisible(vis);
    setStyleSheet(pillStyleSheet(tone, compact_));
    if (vis) {
        adjustSize();
        setMinimumWidth(sizeHint().width());
    } else {
        setMinimumWidth(0);
    }
    updateGeometry();
}

// ============================================================================
// ToggleSwitch
// ============================================================================

ToggleSwitch::ToggleSwitch(Accent accent, QWidget* parent)
    : QAbstractButton(parent), accent_(accent) {
    setCheckable(true);
    setCursor(Qt::PointingHandCursor);
    setFixedSize(38, 21);
    setSizePolicy(QSizePolicy::Fixed, QSizePolicy::Fixed);

    anim_ = new QVariantAnimation(this);
    anim_->setDuration(140);
    anim_->setEasingCurve(QEasingCurve::OutCubic);
    connect(anim_, &QVariantAnimation::valueChanged, this, [this](const QVariant& v) {
        progress_ = v.toReal();
        update();
    });
    connect(this, &QAbstractButton::toggled, this, [this](bool checked) {
        if (suppressAnim_) {
            progress_ = checked ? 1.0 : 0.0;
            update();
            return;
        }
        anim_->stop();
        anim_->setStartValue(progress_);
        anim_->setEndValue(checked ? 1.0 : 0.0);
        anim_->start();
    });
}

void ToggleSwitch::setAccent(Accent accent) {
    accent_ = accent;
    update();
}

void ToggleSwitch::setCheckedSilent(bool checked) {
    const QSignalBlocker blocker(this);
    suppressAnim_ = true;
    anim_->stop();
    setChecked(checked);
    progress_ = checked ? 1.0 : 0.0;
    suppressAnim_ = false;
    update();
}

QSize ToggleSwitch::sizeHint() const {
    return QSize(38, 21);
}

void ToggleSwitch::checkStateSet() {
    QAbstractButton::checkStateSet();
}

void ToggleSwitch::nextCheckState() {
    setChecked(!isChecked());
}

void ToggleSwitch::paintEvent(QPaintEvent*) {
    QPainter p(this);
    p.setRenderHint(QPainter::Antialiasing, true);

    QColor onColor(0x10, 0xb9, 0x81);
    if (accent_ == Accent::Blue) onColor = QColor(0x25, 0x63, 0xeb);
    else if (accent_ == Accent::Violet) onColor = QColor(0x7c, 0x3a, 0xed);

    const QColor offColor = isEnabled() ? QColor(0x32, 0x34, 0x42) : QColor(0x22, 0x24, 0x2e);
    if (!isEnabled() && isChecked()) {
        onColor = onColor.darker(150);
    }

    auto lerp = [](int a, int b, qreal t) -> int {
        return qRound(a + (b - a) * t);
    };
    const QColor trackColor(
        lerp(offColor.red(), onColor.red(), progress_),
        lerp(offColor.green(), onColor.green(), progress_),
        lerp(offColor.blue(), onColor.blue(), progress_));

    const QRectF trackRect(0.5, 0.5, width() - 1.0, height() - 1.0);
    p.setPen(Qt::NoPen);
    p.setBrush(trackColor);
    p.drawRoundedRect(trackRect, trackRect.height() / 2.0, trackRect.height() / 2.0);

    const qreal knobSize = 15.0;
    const qreal x = 3.0 + (width() - 6.0 - knobSize) * progress_;
    const qreal y = (height() - knobSize) / 2.0;
    const QColor knobColor = isEnabled()
                                 ? QColor(lerp(0xcb, 0xff, progress_), lerp(0xd5, 0xff, progress_), lerp(0xe1, 0xff, progress_))
                                 : QColor(0x64, 0x74, 0x8b);
    p.setBrush(knobColor);
    p.drawEllipse(QRectF(x, y, knobSize, knobSize));
}

// ============================================================================
// SegmentedControl
// ============================================================================

SegmentedControl::SegmentedControl(bool primaryAccent, QWidget* parent)
    : QWidget(parent), primaryAccent_(primaryAccent) {
    setAttribute(Qt::WA_StyledBackground, true);
    setStyleSheet(QStringLiteral(
        "SegmentedControl { background-color: #191a21; border: 1px solid #262833; border-radius: 8px; }"));
    layout_ = new QHBoxLayout(this);
    layout_->setContentsMargins(3, 3, 3, 3);
    layout_->setSpacing(3);
}

void SegmentedControl::setCompact(bool compact) {
    compact_ = compact;
    updateButtonStyles();
}

void SegmentedControl::setItems(const QStringList& labels) {
    for (QPushButton* b : buttons_) {
        layout_->removeWidget(b);
        b->deleteLater();
    }
    buttons_.clear();

    for (int i = 0; i < labels.size(); ++i) {
        auto* btn = new QPushButton(labels.at(i), this);
        btn->setCursor(Qt::PointingHandCursor);
        btn->setAutoDefault(false);
        btn->setFocusPolicy(Qt::NoFocus);
        buttons_.append(btn);
        layout_->addWidget(btn, primaryAccent_ ? 1 : 0);
        connect(btn, &QPushButton::clicked, this, [this, i]() {
            emit itemClicked(i);
            if (!buttons_.at(i)->isEnabled()) return;
            if (currentIndex_ != i) {
                currentIndex_ = i;
                updateButtonStyles();
                emit currentIndexChanged(i);
            }
        });
    }
    if (currentIndex_ >= buttons_.size()) {
        currentIndex_ = 0;
    }
    updateButtonStyles();
}

void SegmentedControl::setItemText(int index, const QString& text) {
    if (index >= 0 && index < buttons_.size()) {
        buttons_.at(index)->setText(text);
        updateButtonStyles();
        updateGeometry();
    }
}

void SegmentedControl::setItemEnabled(int index, bool enabled) {
    if (index >= 0 && index < buttons_.size()) {
        buttons_.at(index)->setEnabled(enabled);
        updateButtonStyles();
    }
}

void SegmentedControl::setCurrentIndex(int index, bool emitSignal) {
    if (index < 0 || index >= buttons_.size()) return;
    const bool changed = (currentIndex_ != index);
    currentIndex_ = index;
    updateButtonStyles();
    if (emitSignal && changed) {
        emit currentIndexChanged(index);
    }
}

void SegmentedControl::updateButtonStyles() {
    const QString activeBg = primaryAccent_ ? QStringLiteral("#2563eb") : QStringLiteral("#272b38");
    const QString padding = compact_ ? QStringLiteral("5px 10px") : QStringLiteral("6px 14px");
    for (int i = 0; i < buttons_.size(); ++i) {
        QPushButton* btn = buttons_.at(i);
        const bool active = (i == currentIndex_);
        if (active) {
            btn->setStyleSheet(QStringLiteral(
                "QPushButton { background-color: %1; color: #ffffff; border: none; border-radius: 6px; "
                "padding: %2; font-size: 12px; font-weight: 600; }")
                                   .arg(activeBg, padding));
        } else {
            btn->setStyleSheet(QStringLiteral(
                "QPushButton { background-color: transparent; color: #94a3b8; border: none; border-radius: 6px; "
                "padding: %1; font-size: 12px; font-weight: 500; }"
                "QPushButton:hover:enabled { background-color: rgba(255,255,255,0.05); color: #e2e8f0; }"
                "QPushButton:disabled { color: #52525b; }")
                                   .arg(padding));
        }
        const int padPx = compact_ ? 20 : 28;
        btn->setMinimumWidth(btn->fontMetrics().horizontalAdvance(btn->text()) + padPx);
    }
}

// ============================================================================
// SidebarQuickCard
// ============================================================================

SidebarQuickCard::SidebarQuickCard(VectorIcon icon,
                                   ToggleSwitch::Accent accent,
                                   QWidget* parent)
    : QFrame(parent), icon_(icon), accent_(accent) {
    setCursor(Qt::PointingHandCursor);
    setObjectName(QStringLiteral("SidebarQuickCard"));

    auto* v = new QVBoxLayout(this);
    v->setContentsMargins(10, 9, 10, 9);
    v->setSpacing(4);

    auto* top = new QHBoxLayout();
    top->setContentsMargins(0, 0, 0, 0);
    iconLabel_ = new QLabel(this);
    iconLabel_->setFixedSize(20, 20);
    iconLabel_->setStyleSheet(QStringLiteral("background: transparent; border: none;"));
    switch_ = new ToggleSwitch(accent, this);
    top->addWidget(iconLabel_);
    top->addStretch(1);
    top->addWidget(switch_);

    titleLabel_ = new QLabel(this);
    titleLabel_->setStyleSheet(QStringLiteral(
        "color: #f1f5f9; font-size: 12px; font-weight: 600; background: transparent; border: none;"));

    subLabel_ = new QLabel(this);
    subLabel_->setStyleSheet(QStringLiteral(
        "color: #94a3b8; font-size: 11px; background: transparent; border: none;"));

    v->addLayout(top);
    v->addWidget(titleLabel_);
    v->addWidget(subLabel_);

    connect(switch_, &ToggleSwitch::clicked, this, [this](bool checked) {
        emit toggleRequested(checked);
    });
    refreshIconAndStyle();
}

void SidebarQuickCard::setState(bool active, const QString& title, const QString& subtitle) {
    active_ = active;
    switch_->setCheckedSilent(active);
    titleLabel_->setText(title);
    subLabel_->setText(subtitle);
    refreshIconAndStyle();
}

void SidebarQuickCard::setCardEnabled(bool enabled) {
    cardEnabled_ = enabled;
    switch_->setEnabled(enabled);
    setCursor(enabled ? Qt::PointingHandCursor : Qt::ArrowCursor);
    refreshIconAndStyle();
}

void SidebarQuickCard::refreshIconAndStyle() {
    const QColor iconColor = active_
                                 ? (accent_ == ToggleSwitch::Accent::Blue ? QColor(0x60, 0xa5, 0xfa)
                                                                          : QColor(0x34, 0xd3, 0x99))
                                 : QColor(0x94, 0xa3, 0xb8);
    iconLabel_->setPixmap(renderVectorIcon(icon_, 17, iconColor, devicePixelRatioF()));
    update();
}

void SidebarQuickCard::paintEvent(QPaintEvent*) {
    QPainter p(this);
    p.setRenderHint(QPainter::Antialiasing, true);

    QColor bgTop(0x18, 0x19, 0x20);
    QColor bgBot(0x18, 0x19, 0x20);
    QColor border(0x26, 0x28, 0x34);

    if (active_) {
        if (accent_ == ToggleSwitch::Accent::Blue) {
            bgTop = QColor(0x18, 0x23, 0x3b);
            bgBot = QColor(0x16, 0x18, 0x22);
            border = QColor(59, 130, 246, 115);
        } else {
            bgTop = QColor(0x13, 0x29, 0x24);
            bgBot = QColor(0x16, 0x18, 0x22);
            border = QColor(16, 185, 129, 115);
        }
    }

    QLinearGradient grad(0, 0, 0, height());
    grad.setColorAt(0.0, bgTop);
    grad.setColorAt(1.0, bgBot);
    p.setPen(QPen(border, 1.0));
    p.setBrush(grad);
    p.drawRoundedRect(QRectF(0.5, 0.5, width() - 1.0, height() - 1.0), 9.0, 9.0);
}

void SidebarQuickCard::mouseReleaseEvent(QMouseEvent* event) {
    QFrame::mouseReleaseEvent(event);
    if (!cardEnabled_ || event->button() != Qt::LeftButton) return;
    if (rect().contains(event->pos())) {
        emit toggleRequested(!active_);
    }
}

// ============================================================================
// SidebarNavButton
// ============================================================================

SidebarNavButton::SidebarNavButton(VectorIcon icon, QWidget* parent)
    : QPushButton(parent), icon_(icon) {
    setFixedHeight(36);
    setCursor(Qt::PointingHandCursor);
    setFlat(true);
    setFocusPolicy(Qt::NoFocus);
}

void SidebarNavButton::setNavTitle(const QString& title) {
    title_ = title;
    update();
}

void SidebarNavButton::setBadge(const QString& text, PillTone tone) {
    badgeText_ = text;
    badgeTone_ = tone;
    update();
}

void SidebarNavButton::setActive(bool active) {
    if (active_ == active) return;
    active_ = active;
    update();
}

#if QT_VERSION >= QT_VERSION_CHECK(6, 0, 0)
void SidebarNavButton::enterEvent(QEnterEvent*) {
    hovered_ = true;
    update();
}
#else
void SidebarNavButton::enterEvent(QEvent*) {
    hovered_ = true;
    update();
}
#endif

void SidebarNavButton::leaveEvent(QEvent*) {
    hovered_ = false;
    update();
}

void SidebarNavButton::paintEvent(QPaintEvent*) {
    QPainter p(this);
    p.setRenderHint(QPainter::Antialiasing, true);

    const QRectF r(0.5, 0.5, width() - 1.0, height() - 1.0);
    if (active_) {
        p.setPen(QPen(QColor(59, 130, 246, 92), 1.0));
        p.setBrush(QColor(0x1e, 0x24, 0x36));
        p.drawRoundedRect(r, 8.0, 8.0);

        p.setPen(Qt::NoPen);
        p.setBrush(QColor(0x3b, 0x82, 0xf6));
        p.drawRoundedRect(QRectF(1.0, 7.0, 3.0, height() - 14.0), 1.5, 1.5);
    } else if (hovered_) {
        p.setPen(Qt::NoPen);
        p.setBrush(QColor(0x1a, 0x1c, 0x25));
        p.drawRoundedRect(r, 8.0, 8.0);
    }

    const QColor iconColor = active_ ? QColor(0x60, 0xa5, 0xfa)
                                     : (hovered_ ? QColor(0xe4, 0xe4, 0xe7) : QColor(0x94, 0xa3, 0xb8));
    const QPixmap pm = renderVectorIcon(icon_, 16, iconColor, devicePixelRatioF());
    p.drawPixmap(QPointF(12.0, (height() - 16.0) / 2.0), pm);

    int rightLimit = width() - 10;
    if (!badgeText_.trimmed().isEmpty()) {
        QFont bf = font();
        bf.setPixelSize(11);
        bf.setWeight(QFont::DemiBold);
        QFontMetrics bfm(bf);
        const int bw = bfm.horizontalAdvance(badgeText_) + 14;
        const int bh = 18;
        const QRectF bRect(width() - bw - 8.0, (height() - bh) / 2.0, bw, bh);
        const TonePalette tp = paletteForTone(badgeTone_);
        p.setPen(QPen(tp.border, 1.0));
        p.setBrush(tp.bg);
        p.drawRoundedRect(bRect, 9.0, 9.0);
        p.setFont(bf);
        p.setPen(tp.fg);
        p.drawText(bRect, Qt::AlignCenter, badgeText_);
        rightLimit = static_cast<int>(bRect.left()) - 6;
    }

    QFont tf = font();
    tf.setPixelSize(13);
    tf.setWeight(active_ ? QFont::DemiBold : QFont::Medium);
    p.setFont(tf);
    p.setPen(active_ ? QColor(0xff, 0xff, 0xff)
                     : (hovered_ ? QColor(0xf1, 0xf5, 0xf9) : QColor(0xa1, 0xa1, 0xaa)));
    const QRect textRect(36, 0, qMax(20, rightLimit - 36), height());
    p.drawText(textRect, Qt::AlignVCenter | Qt::AlignLeft,
               QFontMetrics(tf).elidedText(title_, Qt::ElideRight, textRect.width()));
}

// ============================================================================
// PillItemDelegate & QuotaBarDelegate
// ============================================================================

PillItemDelegate::PillItemDelegate(int horizontalMargin, QObject* parent)
    : QStyledItemDelegate(parent), hMargin_(horizontalMargin) {}

void PillItemDelegate::paint(QPainter* painter,
                             const QStyleOptionViewItem& option,
                             const QModelIndex& index) const {
    const QVariant toneVar = index.data(kPillToneRole);
    if (!toneVar.isValid()) {
        QStyledItemDelegate::paint(painter, option, index);
        return;
    }

    QStyleOptionViewItem opt(option);
    initStyleOption(&opt, index);

    painter->save();
    painter->setRenderHint(QPainter::Antialiasing, true);

    if (opt.state & QStyle::State_Selected) {
        painter->fillRect(opt.rect, QColor(37, 99, 235, 36));
    } else if (opt.backgroundBrush.style() != Qt::NoBrush) {
        painter->fillRect(opt.rect, opt.backgroundBrush);
    }

    const QString text = opt.text.trimmed();
    if (!text.isEmpty()) {
        const PillTone tone = static_cast<PillTone>(toneVar.toInt());
        const TonePalette tp = paletteForTone(tone);
        QFont f = opt.font;
        f.setPixelSize(11);
        f.setWeight(QFont::DemiBold);
        QFontMetrics fm(f);
        const int pillW = qMin(opt.rect.width() - hMargin_ * 2, fm.horizontalAdvance(text) + 18);
        const int pillH = 22;
        const int y = opt.rect.top() + (opt.rect.height() - pillH) / 2;
        const QRectF pillRect(opt.rect.left() + hMargin_, y, pillW, pillH);

        painter->setPen(QPen(tp.border, 1.0));
        painter->setBrush(tp.bg);
        painter->drawRoundedRect(pillRect, 11.0, 11.0);

        painter->setFont(f);
        painter->setPen(tp.fg);
        painter->drawText(pillRect.adjusted(6, 0, -6, 0), Qt::AlignCenter,
                          fm.elidedText(text, Qt::ElideRight, pillW - 12));
    }
    painter->restore();
}

QSize PillItemDelegate::sizeHint(const QStyleOptionViewItem& option, const QModelIndex& index) const {
    QSize base = QStyledItemDelegate::sizeHint(option, index);
    if (index.data(kPillToneRole).isValid()) {
        QFont f = option.font;
        f.setPixelSize(11);
        f.setWeight(QFont::DemiBold);
        const int w = QFontMetrics(f).horizontalAdvance(index.data().toString()) + 18 + hMargin_ * 2;
        return QSize(qMax(base.width(), w), qMax(base.height(), 36));
    }
    return base;
}

QuotaBarDelegate::QuotaBarDelegate(QObject* parent)
    : QStyledItemDelegate(parent) {}

void QuotaBarDelegate::paint(QPainter* painter,
                             const QStyleOptionViewItem& option,
                             const QModelIndex& index) const {
    const QVariant ratioVar = index.data(kQuotaRatioRole);
    if (!ratioVar.isValid() || ratioVar.toDouble() < 0.0) {
        QStyledItemDelegate::paint(painter, option, index);
        return;
    }

    QStyleOptionViewItem opt(option);
    initStyleOption(&opt, index);

    painter->save();
    painter->setRenderHint(QPainter::Antialiasing, true);

    if (opt.state & QStyle::State_Selected) {
        painter->fillRect(opt.rect, QColor(37, 99, 235, 36));
    }

    const qreal ratio = qBound<qreal>(0.0, ratioVar.toDouble(), 1.0);
    const QRect r = opt.rect.adjusted(10, 5, -10, -5);

    QFont f = opt.font;
    f.setPixelSize(12);
    painter->setFont(f);
    painter->setPen(QColor(0xe2, 0xe8, 0xf0));
    const QRect textRect(r.left(), r.top(), r.width(), r.height() - 8);
    painter->drawText(textRect, Qt::AlignLeft | Qt::AlignVCenter, opt.text);

    const QRectF track(r.left(), r.bottom() - 4.0, r.width(), 4.5);
    painter->setPen(Qt::NoPen);
    painter->setBrush(QColor(0x27, 0x29, 0x35));
    painter->drawRoundedRect(track, 2.2, 2.2);

    if (ratio > 0.0) {
        const qreal fillW = qMax<qreal>(4.5, track.width() * ratio);
        const QRectF fillRect(track.left(), track.top(), fillW, track.height());
        if (ratio >= 0.85) {
            painter->setBrush(QColor(0xf5, 0x9e, 0x0b));
        } else {
            QLinearGradient grad(fillRect.left(), 0, fillRect.right(), 0);
            grad.setColorAt(0.0, QColor(0x3b, 0x82, 0xf6));
            grad.setColorAt(1.0, QColor(0x10, 0xb9, 0x81));
            painter->setBrush(grad);
        }
        painter->drawRoundedRect(fillRect, 2.2, 2.2);
    }

    painter->restore();
}

QSize QuotaBarDelegate::sizeHint(const QStyleOptionViewItem& option, const QModelIndex& index) const {
    QSize s = QStyledItemDelegate::sizeHint(option, index);
    return QSize(qMax(s.width(), 155), qMax(s.height(), 42));
}

// ============================================================================
// ToastOverlay
// ============================================================================

ToastOverlay::ToastOverlay(QWidget* anchorWindow,
                           const QString& text,
                           PillTone tone,
                           int durationMs)
    : QWidget(nullptr,
              Qt::ToolTip | Qt::FramelessWindowHint | Qt::NoDropShadowWindowHint |
                  Qt::WindowDoesNotAcceptFocus | Qt::WindowTransparentForInput)
    , anchorWindow_(anchorWindow)
    , text_(text)
    , tone_(tone) {
    setAttribute(Qt::WA_TranslucentBackground, true);
    setAttribute(Qt::WA_DeleteOnClose, true);
    setAttribute(Qt::WA_ShowWithoutActivating, true);

    QFont f = font();
    f.setPixelSize(12);
    f.setWeight(QFont::DemiBold);
    setFont(f);
    const QFontMetrics fm(f);
    const int w = qBound(200, fm.horizontalAdvance(text_) + 70, 560);
    const int h = 40;
    resize(w, h);

    if (anchorWindow_) {
        anchorWindow_->installEventFilter(this);
    }
    reposition();

    fadeAnim_ = new QVariantAnimation(this);
    fadeAnim_->setDuration(180);
    fadeAnim_->setStartValue(0.0);
    fadeAnim_->setEndValue(1.0);
    fadeAnim_->setEasingCurve(QEasingCurve::OutCubic);
    connect(fadeAnim_, &QVariantAnimation::valueChanged, this, [this](const QVariant& v) {
        setWindowOpacity(v.toReal());
    });
    setWindowOpacity(0.0);
    QWidget::show();
    fadeAnim_->start();

    QTimer::singleShot(qMax(800, durationMs), this, &ToastOverlay::dismiss);
}

void ToastOverlay::show(QWidget* anchorWidget, const QString& text, PillTone tone, int durationMs) {
    if (!anchorWidget || text.trimmed().isEmpty()) return;
    QWidget* topWin = anchorWidget->window();
    if (!topWin || !topWin->isVisible()) return;

    // Close existing toasts on the same window so they don't stack awkwardly
    for (QWidget* w : QApplication::topLevelWidgets()) {
        if (auto* existing = qobject_cast<ToastOverlay*>(w)) {
            if (existing->anchorWindow_ == topWin) {
                existing->close();
            }
        }
    }
    new ToastOverlay(topWin, text, tone, durationMs);
}

void ToastOverlay::reposition() {
    if (!anchorWindow_ || !anchorWindow_->isVisible()) return;
    const QRect g = anchorWindow_->geometry();
    move(g.right() - width() - 22, g.bottom() - height() - 18);
    raise();
}

void ToastOverlay::dismiss() {
    if (!fadeAnim_) {
        close();
        return;
    }
    fadeAnim_->disconnect(this);
    fadeAnim_->setStartValue(windowOpacity());
    fadeAnim_->setEndValue(0.0);
    fadeAnim_->setDuration(220);
    connect(fadeAnim_, &QVariantAnimation::valueChanged, this, [this](const QVariant& v) {
        setWindowOpacity(v.toReal());
    });
    connect(fadeAnim_, &QVariantAnimation::finished, this, &QWidget::close);
    fadeAnim_->start();
}

bool ToastOverlay::eventFilter(QObject* watched, QEvent* event) {
    if (watched == anchorWindow_) {
        const QEvent::Type t = event->type();
        if (t == QEvent::Move || t == QEvent::Resize) {
            reposition();
        } else if (t == QEvent::Hide || t == QEvent::Close || t == QEvent::WindowStateChange) {
            if (!anchorWindow_->isVisible() || anchorWindow_->isMinimized()) {
                close();
            }
        }
    }
    return QWidget::eventFilter(watched, event);
}

void ToastOverlay::paintEvent(QPaintEvent*) {
    QPainter p(this);
    p.setRenderHint(QPainter::Antialiasing, true);

    const TonePalette tp = paletteForTone(tone_);
    p.setPen(QPen(tp.border, 1.2));
    p.setBrush(QColor(22, 27, 38, 245));
    p.drawRoundedRect(QRectF(0.6, 0.6, width() - 1.2, height() - 1.2), 9.0, 9.0);

    const QRectF circle(12.0, (height() - 20.0) / 2.0, 20.0, 20.0);
    p.setPen(Qt::NoPen);
    p.setBrush(tp.bg);
    p.drawEllipse(circle);

    QFont iconFont = font();
    iconFont.setPixelSize(11);
    iconFont.setWeight(QFont::Bold);
    p.setFont(iconFont);
    p.setPen(tp.fg);
    const QString glyph = (tone_ == PillTone::Red || tone_ == PillTone::Amber)
                              ? QStringLiteral("!")
                              : QStringLiteral("✓");
    p.drawText(circle, Qt::AlignCenter, glyph);

    p.setFont(font());
    p.setPen(QColor(0xf1, 0xf5, 0xf9));
    const QRect textRect(40, 0, width() - 52, height());
    p.drawText(textRect, Qt::AlignVCenter | Qt::AlignLeft,
               QFontMetrics(font()).elidedText(text_, Qt::ElideRight, textRect.width()));
}

// ============================================================================
// Helpers
// ============================================================================

WorkspaceHeaderRefs buildWorkspaceHeader(QWidget* parent) {
    WorkspaceHeaderRefs refs;
    refs.container = new QFrame(parent);
    refs.container->setFixedHeight(60);
    refs.container->setObjectName(QStringLiteral("WorkspaceHeader"));
    refs.container->setStyleSheet(QStringLiteral(
        "#WorkspaceHeader { background-color: #16171d; border-bottom: 1px solid #22242e; }"));

    auto* h = new QHBoxLayout(refs.container);
    h->setContentsMargins(24, 8, 24, 8);
    h->setSpacing(12);

    auto* leftCol = new QVBoxLayout();
    leftCol->setContentsMargins(0, 0, 0, 0);
    leftCol->setSpacing(2);

    auto* titleRow = new QHBoxLayout();
    titleRow->setContentsMargins(0, 0, 0, 0);
    titleRow->setSpacing(8);

    refs.titleLabel = new QLabel(refs.container);
    refs.titleLabel->setStyleSheet(QStringLiteral(
        "color: #f8fafc; font-size: 16px; font-weight: 700; background: transparent;"));
    refs.primaryBadge = new BadgePill(refs.container, true);
    refs.secondaryBadge = new BadgePill(refs.container, true);

    titleRow->addWidget(refs.titleLabel);
    titleRow->addWidget(refs.primaryBadge);
    titleRow->addWidget(refs.secondaryBadge);
    titleRow->addStretch(1);

    refs.subtitleLabel = new QLabel(refs.container);
    refs.subtitleLabel->setStyleSheet(QStringLiteral(
        "color: #94a3b8; font-size: 12px; background: transparent;"));

    leftCol->addLayout(titleRow);
    leftCol->addWidget(refs.subtitleLabel);

    refs.actionsLayout = new QHBoxLayout();
    refs.actionsLayout->setContentsMargins(0, 0, 0, 0);
    refs.actionsLayout->setSpacing(8);

    h->addLayout(leftCol, 1);
    h->addLayout(refs.actionsLayout, 0);
    return refs;
}

QFrame* createCardFrame(QWidget* parent, const QString& objectName) {
    auto* card = new QFrame(parent);
    card->setObjectName(objectName);
    card->setStyleSheet(QStringLiteral(
        "#%1 { background-color: #1a1c24; border: 1px solid #272a36; border-radius: 10px; }")
                            .arg(objectName));
    return card;
}

} // namespace clashfx
