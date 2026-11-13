#pragma once

#include <QObject>
#include <QString>
#include <QTranslator>

namespace clashfx {

class ClashFXTranslator final : public QTranslator {
    Q_OBJECT
public:
    explicit ClashFXTranslator(QObject* parent = nullptr);
    void setLanguage(const QString& lang);
    QString language() const { return lang_; }

    QString translate(const char* context,
                      const char* sourceText,
                      const char* disambiguation = nullptr,
                      int n = -1) const override;

private:
    QString lang_ = QStringLiteral("zh_CN");
};

class I18n final : public QObject {
    Q_OBJECT
public:
    static I18n& instance();

    QString language() const { return translator_.language(); }
    bool isChinese() const { return translator_.language() != QStringLiteral("en_US"); }

    void setLanguage(const QString& lang);

    static QString pick(const QString& zh, const QString& en);
    static QString translateBackendText(const QString& text);

signals:
    void languageChanged(const QString& lang);

private:
    explicit I18n(QObject* parent = nullptr);
    ClashFXTranslator translator_;
};

inline QString L(const char* zhUtf8, const char* enUtf8) {
    return I18n::pick(QString::fromUtf8(zhUtf8), QString::fromUtf8(enUtf8));
}

} // namespace clashfx
