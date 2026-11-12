#include "I18n.h"

#include <QCoreApplication>
#include <QHash>

namespace clashfx {

ClashFXTranslator::ClashFXTranslator(QObject* parent)
    : QTranslator(parent) {}

void ClashFXTranslator::setLanguage(const QString& lang) {
    lang_ = (lang.compare(QStringLiteral("en_US"), Qt::CaseInsensitive) == 0 ||
             lang.compare(QStringLiteral("en"), Qt::CaseInsensitive) == 0)
                ? QStringLiteral("en_US")
                : QStringLiteral("zh_CN");
}

QString ClashFXTranslator::translate(const char* /*context*/,
                                     const char* sourceText,
                                     const char* /*disambiguation*/,
                                     int /*n*/) const {
    if (!sourceText || lang_ == QStringLiteral("en_US")) {
        return QString();
    }
    static const QHash<QByteArray, QString> kStdButtons = {
        {"OK", QStringLiteral("确定")},
        {"&OK", QStringLiteral("确定(&O)")},
        {"Cancel", QStringLiteral("取消")},
        {"&Cancel", QStringLiteral("取消(&C)")},
        {"Apply", QStringLiteral("应用")},
        {"Close", QStringLiteral("关闭")},
        {"&Close", QStringLiteral("关闭(&C)")},
        {"Save", QStringLiteral("保存")},
        {"&Save", QStringLiteral("保存(&S)")},
        {"Yes", QStringLiteral("是")},
        {"&Yes", QStringLiteral("是(&Y)")},
        {"No", QStringLiteral("否")},
        {"&No", QStringLiteral("否(&N)")},
    };
    const auto it = kStdButtons.constFind(QByteArray(sourceText));
    if (it != kStdButtons.constEnd()) {
        return it.value();
    }
    return QString();
}

I18n& I18n::instance() {
    static I18n inst;
    return inst;
}

I18n::I18n(QObject* parent)
    : QObject(parent)
    , translator_(this) {
    if (qApp) {
        qApp->installTranslator(&translator_);
    }
}

void I18n::setLanguage(const QString& lang) {
    const QString normalized =
        (lang.compare(QStringLiteral("en_US"), Qt::CaseInsensitive) == 0 ||
         lang.compare(QStringLiteral("en"), Qt::CaseInsensitive) == 0)
            ? QStringLiteral("en_US")
            : QStringLiteral("zh_CN");
    if (translator_.language() == normalized) {
        return;
    }
    if (qApp) {
        qApp->removeTranslator(&translator_);
    }
    translator_.setLanguage(normalized);
    if (qApp) {
        qApp->installTranslator(&translator_);
    }
    emit languageChanged(normalized);
}

QString I18n::pick(const QString& zh, const QString& en) {
    return instance().isChinese() ? zh : en;
}

} // namespace clashfx
