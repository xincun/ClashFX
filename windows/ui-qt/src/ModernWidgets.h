#pragma once

#include <QAbstractButton>
#include <QColor>
#include <QFrame>
#include <QHBoxLayout>
#include <QLabel>
#include <QPixmap>
#include <QPointer>
#include <QPushButton>
#include <QString>
#include <QStringList>
#include <QStyledItemDelegate>
#include <QVBoxLayout>
#include <QVariantAnimation>
#include <QWidget>

class QApplication;

namespace clashfx {

enum class PillTone {
    Green = 0,
    Blue = 1,
    Violet = 2,
    Amber = 3,
    Red = 4,
    Muted = 5,
};

enum class VectorIcon {
    BrandBolt,
    Globe,
    Zap,
    Refresh,
    Dashboard,
    Subscriptions,
    Rules,
    AiLock,
    MultiNode,
    Diagnostics,
    Settings,
};

constexpr int kPillToneRole = Qt::UserRole + 10;
constexpr int kQuotaRatioRole = Qt::UserRole + 11;

QString pillStyleSheet(PillTone tone, bool compact = false);
QPixmap renderVectorIcon(VectorIcon kind, int logicalSize, const QColor& color, qreal dpr);

namespace Win11Dwm {
void applyDarkTitleBar(QWidget* window);
void installGlobalDarkTitleBarFilter(QApplication& app);
} // namespace Win11Dwm

class BadgePill final : public QLabel {
    Q_OBJECT
public:
    explicit BadgePill(QWidget* parent = nullptr, bool compact = false);
    void setPill(const QString& text, PillTone tone);

private:
    bool compact_ = false;
    PillTone tone_ = PillTone::Muted;
};

class ToggleSwitch final : public QAbstractButton {
    Q_OBJECT
public:
    enum class Accent {
        Blue,
        Emerald,
        Violet,
    };

    explicit ToggleSwitch(Accent accent = Accent::Emerald, QWidget* parent = nullptr);

    void setAccent(Accent accent);
    void setCheckedSilent(bool checked);
    QSize sizeHint() const override;

protected:
    void paintEvent(QPaintEvent* event) override;
    void checkStateSet() override;
    void nextCheckState() override;

private:
    Accent accent_ = Accent::Emerald;
    qreal progress_ = 0.0;
    bool suppressAnim_ = false;
    QVariantAnimation* anim_ = nullptr;
};

class SegmentedControl final : public QWidget {
    Q_OBJECT
public:
    explicit SegmentedControl(bool primaryAccent = true, QWidget* parent = nullptr);

    void setItems(const QStringList& labels);
    void setItemText(int index, const QString& text);
    void setItemEnabled(int index, bool enabled);
    void setCurrentIndex(int index, bool emitSignal = false);
    int currentIndex() const { return currentIndex_; }
    void setCompact(bool compact);

signals:
    void currentIndexChanged(int index);
    void itemClicked(int index);

private:
    void updateButtonStyles();

    bool primaryAccent_ = true;
    bool compact_ = false;
    int currentIndex_ = 0;
    QHBoxLayout* layout_ = nullptr;
    QList<QPushButton*> buttons_;
};

class SidebarQuickCard final : public QFrame {
    Q_OBJECT
public:
    explicit SidebarQuickCard(VectorIcon icon,
                              ToggleSwitch::Accent accent,
                              QWidget* parent = nullptr);

    void setState(bool active, const QString& title, const QString& subtitle);
    void setCardEnabled(bool enabled);

signals:
    void toggleRequested(bool nextChecked);

protected:
    void mouseReleaseEvent(QMouseEvent* event) override;
    void paintEvent(QPaintEvent* event) override;

private:
    void refreshIconAndStyle();

    VectorIcon icon_;
    ToggleSwitch::Accent accent_;
    bool active_ = false;
    bool cardEnabled_ = true;
    QLabel* iconLabel_ = nullptr;
    ToggleSwitch* switch_ = nullptr;
    QLabel* titleLabel_ = nullptr;
    QLabel* subLabel_ = nullptr;
};

class SidebarNavButton final : public QPushButton {
    Q_OBJECT
public:
    explicit SidebarNavButton(VectorIcon icon, QWidget* parent = nullptr);

    void setNavTitle(const QString& title);
    void setBadge(const QString& text, PillTone tone = PillTone::Muted);
    void setActive(bool active);
    bool isActive() const { return active_; }

protected:
    void paintEvent(QPaintEvent* event) override;
#if QT_VERSION >= QT_VERSION_CHECK(6, 0, 0)
    void enterEvent(QEnterEvent* event) override;
#else
    void enterEvent(QEvent* event) override;
#endif
    void leaveEvent(QEvent* event) override;

private:
    VectorIcon icon_;
    QString title_;
    QString badgeText_;
    PillTone badgeTone_ = PillTone::Muted;
    bool active_ = false;
    bool hovered_ = false;
};

class PillItemDelegate final : public QStyledItemDelegate {
    Q_OBJECT
public:
    explicit PillItemDelegate(int horizontalMargin = 10, QObject* parent = nullptr);

    void paint(QPainter* painter,
               const QStyleOptionViewItem& option,
               const QModelIndex& index) const override;
    QSize sizeHint(const QStyleOptionViewItem& option, const QModelIndex& index) const override;

private:
    int hMargin_ = 10;
};

class QuotaBarDelegate final : public QStyledItemDelegate {
    Q_OBJECT
public:
    explicit QuotaBarDelegate(QObject* parent = nullptr);

    void paint(QPainter* painter,
               const QStyleOptionViewItem& option,
               const QModelIndex& index) const override;
    QSize sizeHint(const QStyleOptionViewItem& option, const QModelIndex& index) const override;
};

class ToastOverlay final : public QWidget {
    Q_OBJECT
public:
    static void show(QWidget* anchorWidget,
                     const QString& text,
                     PillTone tone = PillTone::Green,
                     int durationMs = 2600);

protected:
    void paintEvent(QPaintEvent* event) override;
    bool eventFilter(QObject* watched, QEvent* event) override;

private:
    explicit ToastOverlay(QWidget* anchorWindow,
                          const QString& text,
                          PillTone tone,
                          int durationMs);
    void reposition();
    void dismiss();

    QPointer<QWidget> anchorWindow_;
    QString text_;
    PillTone tone_ = PillTone::Green;
    QVariantAnimation* fadeAnim_ = nullptr;
};

struct WorkspaceHeaderRefs {
    QFrame* container = nullptr;
    QLabel* titleLabel = nullptr;
    BadgePill* primaryBadge = nullptr;
    BadgePill* secondaryBadge = nullptr;
    QLabel* subtitleLabel = nullptr;
    QHBoxLayout* actionsLayout = nullptr;
};

WorkspaceHeaderRefs buildWorkspaceHeader(QWidget* parent);
QFrame* createCardFrame(QWidget* parent, const QString& objectName = QStringLiteral("ClashCard"));

} // namespace clashfx
