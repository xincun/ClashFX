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
        {"Open", QStringLiteral("打开")},
        {"Discard", QStringLiteral("放弃")},
        {"Reset", QStringLiteral("重置")},
        {"Retry", QStringLiteral("重试")},
        {"Abort", QStringLiteral("中止")},
        {"Ignore", QStringLiteral("忽略")},
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

QString I18n::translateBackendText(const QString& text) {
    const QString trimmed = text.trimmed();
    if (trimmed.isEmpty() || !instance().isChinese()) {
        return trimmed;
    }

    static const QHash<QString, QString> kExact = {
        {QStringLiteral("Waiting for the next node check"), QStringLiteral("等待下一轮节点 204 连通性巡检")},
        {QStringLiteral("The active configuration has no inline proxy nodes"), QStringLiteral("当前配置不包含可分配独立端口的内联代理节点")},
        {QStringLiteral("Checking node connectivity..."), QStringLiteral("正在巡检节点 204 连通性...")},
        {QStringLiteral("Node connectivity check finished"), QStringLiteral("节点 204 连通性巡检已完成")},
        {QStringLiteral("Node failure detected; subscription refresh is cooling down"), QStringLiteral("检测到节点故障；直连刷新订阅正处于冷却时间内")},
        {QStringLiteral("Refreshing subscription through DIRECT..."), QStringLiteral("正在通过直连通道刷新订阅...")},
        {QStringLiteral("Subscription refreshed directly and reloaded"), QStringLiteral("已通过直连通道刷新订阅并完成重载")},
        {QStringLiteral("Enable and save at least one node before testing download speed"), QStringLiteral("请先至少勾选并保存一个启用端口的节点后再测速")},
        {QStringLiteral("Testing enabled nodes' download speeds..."), QStringLiteral("正在测试已启用节点的 20MB 下载速度...")},
        {QStringLiteral("Download speed test stopped"), QStringLiteral("下载测速已停止")},
        {QStringLiteral("Download speed test finished"), QStringLiteral("下载测速已完成")},
        {QStringLiteral("proxy core is not running"), QStringLiteral("代理内核当前未运行")},
        {QStringLiteral("multi-node ports are disabled"), QStringLiteral("多节点固定端口总开关当前已关闭")},
        {QStringLiteral("no enabled node ports"), QStringLiteral("没有已启用的节点固定端口")},
        {QStringLiteral("direct-refresh listener port is not active"), QStringLiteral("直连刷新专用监听端口未就绪（请先保存启用多节点端口）")},
        {QStringLiteral("system proxy cannot be disabled while AI Proxy Lock is active"), QStringLiteral("AI 专用锁定开启期间系统代理受保护不可关闭")},
        {QStringLiteral("enhanced TUN mode cannot be disabled while AI Proxy Lock is active"), QStringLiteral("AI 专用锁定开启期间增强模式 (TUN) 受保护不可关闭")},
        {QStringLiteral("outbound mode is locked to Rule while AI Proxy Lock is active"), QStringLiteral("AI 专用锁定开启期间出站模式固定为规则模式")},
        {QStringLiteral("Not connected to clashfx-daemon"), QStringLiteral("尚未连接到 clashfx-daemon 后台服务")},
        {QStringLiteral("Disconnected from clashfx-daemon"), QStringLiteral("与 clashfx-daemon 后台服务的连接已断开")},
    };

    const auto it = kExact.constFind(trimmed);
    if (it != kExact.constEnd()) {
        return it.value();
    }

    const QString directFailPrefix = QStringLiteral("Direct refresh failed; previous configuration retained (");
    if (trimmed.startsWith(directFailPrefix)) {
        return QStringLiteral("直连刷新订阅失败，已保留原配置 (") + trimmed.mid(directFailPrefix.size());
    }
    if (trimmed.contains(QStringLiteral("privileged TUN service is not installed or running"))) {
        return QStringLiteral("提权 TUN 服务尚未安装或未运行，请允许管理员权限完成初始化。");
    }
    if (trimmed.contains(QStringLiteral("duplicate node listener port"), Qt::CaseInsensitive)) {
        return QStringLiteral("节点监听端口存在重复冲突（") + trimmed + QStringLiteral("）");
    }
    if (trimmed.contains(QStringLiteral("collides with reserved port"), Qt::CaseInsensitive)) {
        return QStringLiteral("节点端口与混合端口或 API 端口冲突（") + trimmed + QStringLiteral("）");
    }
    return trimmed;
}

} // namespace clashfx
