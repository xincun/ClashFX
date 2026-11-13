#include "TrayController.h"
#include "AiLockAndNodeDialogs.h"
#include "ConsoleWindow.h"
#include "DaemonClient.h"
#include "HotkeyManager.h"
#include "I18n.h"
#include "RuleEditorDialog.h"
#include "SubscriptionsAndSettingsDialogs.h"
#include "TrayIconGenerator.h"

#include <QActionGroup>
#include <QApplication>
#include <QClipboard>
#include <QDateTime>
#include <QDesktopServices>
#include <QDir>
#include <QJsonArray>
#include <QJsonDocument>
#include <QLocalSocket>
#include <QMessageBox>
#include <QTimer>
#include <QUrl>

namespace clashfx {

namespace {

QString formatTrafficBytes(qint64 bytes) {
    if (bytes <= 0) {
        return QStringLiteral("0 B");
    }
    const double gb = static_cast<double>(bytes) / (1024.0 * 1024.0 * 1024.0);
    if (gb >= 1.0) {
        return QString::number(gb, 'f', 2) + QStringLiteral(" GB");
    }
    const double mb = static_cast<double>(bytes) / (1024.0 * 1024.0);
    return QString::number(mb, 'f', 1) + QStringLiteral(" MB");
}

QString buildTopSubscriptionStatusText(const QString& profileName, const QJsonObject& subInfo) {
    if (subInfo.isEmpty()) {
        return QString();
    }
    QString quotaPart;
    const bool hasUp = subInfo.contains(QStringLiteral("upload"));
    const bool hasDown = subInfo.contains(QStringLiteral("download"));
    const bool hasTotal = subInfo.contains(QStringLiteral("total"));
    if (hasUp || hasDown || hasTotal) {
        const qint64 up = static_cast<qint64>(subInfo.value(QStringLiteral("upload")).toDouble(0));
        const qint64 down = static_cast<qint64>(subInfo.value(QStringLiteral("download")).toDouble(0));
        const qint64 total = static_cast<qint64>(subInfo.value(QStringLiteral("total")).toDouble(0));
        const qint64 used = qMax<qint64>(0, up + down);
        if (total > 0) {
            quotaPart = formatTrafficBytes(used) + QStringLiteral(" / ") + formatTrafficBytes(total);
        } else if (used > 0) {
            quotaPart = formatTrafficBytes(used);
        }
    }

    QString expirePart;
    const qint64 expTs = static_cast<qint64>(subInfo.value(QStringLiteral("expire")).toDouble(0));
    const QString expText = subInfo.value(QStringLiteral("expire_text")).toString().trimmed();
    if (expTs > 0) {
        expirePart = L("到期 ", "Exp ") + QDateTime::fromSecsSinceEpoch(expTs).toString(QStringLiteral("yyyy-MM-dd"));
    } else if (!expText.isEmpty()) {
        expirePart = L("到期 ", "Exp ") + expText;
    }

    if (quotaPart.isEmpty() && expirePart.isEmpty()) {
        return QString();
    }
    QStringList parts;
    if (!profileName.isEmpty()) {
        parts << profileName;
    }
    if (!quotaPart.isEmpty()) {
        parts << quotaPart;
    }
    if (!expirePart.isEmpty()) {
        parts << expirePart;
    }
    return parts.join(QStringLiteral(" · "));
}

} // namespace

TrayController::TrayController(DaemonClient* client,
                               const QString& uiServerName,
                               bool showConsoleInitially,
                               QObject* parent)
    : QObject(parent)
    , client_(client)
    , uiServerName_(uiServerName) {
    hotkeys_ = new HotkeyManager(this);
    consoleWindow_ = new ConsoleWindow();
    trayMenu_ = new QMenu();
    trayIcon_ = new QSystemTrayIcon(TrayIconGenerator::generate(QStringLiteral("default"),
                                                                TrayVisualState::Disconnected,
                                                                false),
                                    this);
    trayIcon_->setContextMenu(trayMenu_);
    trayIcon_->setToolTip(TrayIconGenerator::buildTooltip(false, QJsonObject()));
    trayIcon_->show();

    connect(trayIcon_, &QSystemTrayIcon::activated, this, &TrayController::onTrayActivated);
    connect(trayMenu_, &QMenu::aboutToShow, this, [this]() {
        if (menuDirty_) {
            rebuildMenuContents();
        }
    });
    connect(trayMenu_, &QMenu::aboutToHide, this, [this]() {
        if (menuDirty_) {
            QTimer::singleShot(80, this, [this]() {
                if (menuDirty_ && !isMenuTreeVisible()) {
                    rebuildMenuContents();
                }
            });
        }
    });
    connect(hotkeys_, &HotkeyManager::toggleSystemProxyTriggered, this, &TrayController::onToggleSystemProxyHotkey);
    connect(hotkeys_, &HotkeyManager::switchOutboundModeTriggered, this, &TrayController::onSwitchModeHotkey);

    connect(consoleWindow_, &ConsoleWindow::openAiLockRequested, this, &TrayController::openAiLockDialog);
    connect(consoleWindow_, &ConsoleWindow::openMultiNodeRequested, this, &TrayController::openMultiNodeDialog);
    connect(consoleWindow_, &ConsoleWindow::openRulesRequested, this, &TrayController::openRuleEditorDialog);
    connect(consoleWindow_, &ConsoleWindow::openSubscriptionsRequested, this, &TrayController::openSubscriptionsDialog);
    connect(consoleWindow_, &ConsoleWindow::openDiagnosticsRequested, this, [this]() { openDiagnosticsDialog(); });
    connect(consoleWindow_, &ConsoleWindow::openSettingsRequested, this, &TrayController::openSettingsDialog);

    connect(&I18n::instance(), &I18n::languageChanged, this, [this]() { rebuildTrayVisualsAndMenu(true); });

    if (client_) {
        connect(client_, &DaemonClient::connectionChanged, this, &TrayController::onConnectionChanged);
        connect(client_, &DaemonClient::stateChanged, this, &TrayController::onStateChanged);
    }

    rebuildTrayVisualsAndMenu(true);

    if (showConsoleInitially) {
        QTimer::singleShot(150, this, &TrayController::openConsole);
    }
}

TrayController::~TrayController() {
    if (consoleWindow_) {
        consoleWindow_->setAllowClose(true);
        delete consoleWindow_;
        consoleWindow_ = nullptr;
    }
    delete trayMenu_;
    trayMenu_ = nullptr;
}

bool TrayController::startSingleInstanceServer() {
    uiServer_ = new QLocalServer(this);
    QLocalServer::removeServer(uiServerName_);
    if (!uiServer_->listen(uiServerName_)) {
        return false;
    }
    connect(uiServer_, &QLocalServer::newConnection, this, [this]() {
        while (QLocalSocket* sock = uiServer_->nextPendingConnection()) {
            connect(sock, &QLocalSocket::readyRead, this, [this, sock]() {
                const QString msg = QString::fromUtf8(sock->readAll()).trimmed();
                if (!msg.isEmpty()) {
                    handleExternalCommand(msg);
                }
                sock->disconnectFromServer();
            });
            connect(sock, &QLocalSocket::disconnected, sock, &QObject::deleteLater);
        }
    });
    return true;
}

void TrayController::handleExternalCommand(const QString& message) {
    if (message.startsWith(QStringLiteral("URI:"), Qt::CaseInsensitive)) {
        importSchemeUri(message.mid(4).trimmed());
        return;
    }
    if (message == QStringLiteral("ACTIVATE") || message == QStringLiteral("CMD:--open-console")) {
        openConsole();
    } else if (message == QStringLiteral("CMD:--open-ai-lock")) {
        openAiLockDialog();
    } else if (message == QStringLiteral("CMD:--open-multi-node")) {
        openMultiNodeDialog();
    } else if (message == QStringLiteral("CMD:--open-rules")) {
        openRuleEditorDialog();
    } else if (message == QStringLiteral("CMD:--open-subscriptions")) {
        openSubscriptionsDialog();
    } else if (message == QStringLiteral("CMD:--open-diagnostics")) {
        openDiagnosticsDialog();
    } else if (message == QStringLiteral("CMD:--open-settings")) {
        openSettingsDialog();
    } else if (message == QStringLiteral("CMD:--quit")) {
        quitAll();
    } else if (message.startsWith(QStringLiteral("clash://"), Qt::CaseInsensitive) ||
               message.startsWith(QStringLiteral("clashfx://"), Qt::CaseInsensitive)) {
        importSchemeUri(message);
    }
}

void TrayController::onConnectionChanged(bool connected) {
    connected_ = connected;
    consoleWindow_->applySnapshot(connected_, snapshot_);
    rebuildTrayVisualsAndMenu();
}

void TrayController::onStateChanged(const QJsonObject& snapshot) {
    connected_ = true;
    snapshot_ = snapshot;

    const QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    const QString lang = settings.value(QStringLiteral("language")).toString(QStringLiteral("zh_CN"));
    I18n::instance().setLanguage(lang);

    const QJsonObject hk = settings.value(QStringLiteral("hotkeys")).toObject();
    hotkeys_->applyHotkeys(hk.value(QStringLiteral("toggle_system_proxy")).toString(),
                           hk.value(QStringLiteral("switch_outbound_mode")).toString());

    consoleWindow_->applySnapshot(connected_, snapshot_);
    rebuildTrayVisualsAndMenu();
}

void TrayController::onTrayActivated(QSystemTrayIcon::ActivationReason reason) {
    if (reason == QSystemTrayIcon::Trigger || reason == QSystemTrayIcon::DoubleClick) {
        openConsole();
    }
}

void TrayController::onToggleSystemProxyHotkey() {
    if (!client_ || snapshot_.isEmpty()) return;
    const QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    if (settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false)) {
        trayIcon_->showMessage(QStringLiteral("ClashFX"),
                               L("AI 专用锁定开启期间系统代理受保护不可关闭。",
                                 "System Proxy is locked on while AI Proxy Lock is active."));
        return;
    }
    const bool next = !settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    QJsonObject p;
    p.insert(QStringLiteral("enabled"), next);
    client_->call(QStringLiteral("set_system_proxy"), p);
}

void TrayController::onSwitchModeHotkey() {
    if (!client_ || snapshot_.isEmpty()) return;
    const QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    if (settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false)) {
        return;
    }
    const QString cur = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toLower();
    QString next = QStringLiteral("rule");
    if (cur == QStringLiteral("rule")) next = QStringLiteral("global");
    else if (cur == QStringLiteral("global")) next = QStringLiteral("direct");

    QJsonObject p;
    p.insert(QStringLiteral("mode"), next);
    client_->call(QStringLiteral("set_outbound_mode"), p);
}

bool TrayController::isMenuTreeVisible() const {
    if (!trayMenu_) {
        return false;
    }
    if (trayMenu_->isVisible()) {
        return true;
    }
    const auto subMenus = trayMenu_->findChildren<QMenu*>();
    for (QMenu* sub : subMenus) {
        if (sub && sub->isVisible()) {
            return true;
        }
    }
    return false;
}

QByteArray TrayController::buildMenuSignature() const {
    const QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    const QJsonObject runtime = snapshot_.value(QStringLiteral("runtime")).toObject();
    QJsonObject sig;
    sig.insert(QStringLiteral("connected"), connected_);
    sig.insert(QStringLiteral("lang"), I18n::instance().language());
    sig.insert(QStringLiteral("preset"), settings.value(QStringLiteral("tray_icon_preset")));
    sig.insert(QStringLiteral("sys"), settings.value(QStringLiteral("system_proxy_enabled")));
    sig.insert(QStringLiteral("tun"), settings.value(QStringLiteral("enhanced_mode")));
    sig.insert(QStringLiteral("ai_en"), settings.value(QStringLiteral("claudeProxyLockEnabled")));
    sig.insert(QStringLiteral("ai_tgt"), settings.value(QStringLiteral("claudeProxyLockTarget")));
    sig.insert(QStringLiteral("mode"), settings.value(QStringLiteral("outbound_mode")));
    sig.insert(QStringLiteral("prof"), settings.value(QStringLiteral("selected_profile")));
    sig.insert(QStringLiteral("allow_lan"), settings.value(QStringLiteral("allow_lan")));
    sig.insert(QStringLiteral("ipv6"), settings.value(QStringLiteral("ipv6")));
    sig.insert(QStringLiteral("bypass_cn"), settings.value(QStringLiteral("bypass_cn_apps")));
    sig.insert(QStringLiteral("as_is"), settings.value(QStringLiteral("custom_config_as_is")));
    sig.insert(QStringLiteral("autostart"), settings.value(QStringLiteral("launch_at_login")));
    sig.insert(QStringLiteral("mixed_port"), runtime.value(QStringLiteral("mixed_port")));
    sig.insert(QStringLiteral("profiles"), snapshot_.value(QStringLiteral("profiles")));
    sig.insert(QStringLiteral("groups"), snapshot_.value(QStringLiteral("proxy_groups")));
    sig.insert(QStringLiteral("sub_info"), snapshot_.value(QStringLiteral("active_subscription_info")));
    sig.insert(QStringLiteral("custom_rules"), settings.value(QStringLiteral("custom_rules")));
    return QJsonDocument(sig).toJson(QJsonDocument::Compact);
}

void TrayController::handleConfigMutationResult(const QString& actionTitle,
                                                const std::function<void()>& retryAction,
                                                bool ok,
                                                const QString& err) {
    if (ok) {
        return;
    }
    const bool aiLocked = snapshot_.value(QStringLiteral("settings"))
                              .toObject()
                              .value(QStringLiteral("claudeProxyLockEnabled"))
                              .toBool(false);
    if (aiLocked && retryAction) {
        QMessageBox box(consoleWindow_);
        box.setIcon(QMessageBox::Warning);
        box.setWindowTitle(actionTitle);
        box.setText(actionTitle + L("失败：\n", " failed:\n") + err +
                    L("\n\n当前已启用 AI 专用锁定（要求目标配置包含锁定的具体节点）。是否停用 AI 专用锁定并立即重试？",
                      "\n\nAI Proxy Lock is active (which requires the locked node to exist in the profile). Disable AI Proxy Lock and retry now?"));
        auto* disableRetryBtn = box.addButton(L("停用 AI 锁定并重试", "Disable AI Lock & Retry"), QMessageBox::AcceptRole);
        box.addButton(L("取消", "Cancel"), QMessageBox::RejectRole);
        box.exec();
        if (box.clickedButton() == disableRetryBtn && client_) {
            QJsonObject p;
            p.insert(QStringLiteral("enabled"), false);
            p.insert(QStringLiteral("target"), QString());
            p.insert(QStringLiteral("allow_uac"), false);
            client_->call(QStringLiteral("set_ai_lock"), p, [this, actionTitle, retryAction](bool disOk, const QJsonValue&, const QString& disErr, const QString&) {
                if (!disOk) {
                    QMessageBox::warning(consoleWindow_, actionTitle, disErr);
                    return;
                }
                retryAction();
            });
        }
        return;
    }
    trayIcon_->showMessage(QStringLiteral("ClashFX"), actionTitle + QStringLiteral(": ") + err, QSystemTrayIcon::Warning);
}

void TrayController::rebuildTrayVisualsAndMenu(bool forceMenu) {
    const QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    const QString preset = settings.value(QStringLiteral("tray_icon_preset")).toString(QStringLiteral("default"));
    const bool sysProxy = settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    const bool enhanced = settings.value(QStringLiteral("enhanced_mode")).toBool(false);
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);

    TrayVisualState vis = TrayVisualState::Disconnected;
    if (connected_ && !snapshot_.isEmpty()) {
        if (enhanced) vis = TrayVisualState::EnhancedTun;
        else if (sysProxy) vis = TrayVisualState::SystemProxy;
        else vis = TrayVisualState::Idle;
    }

    const QString iconKey = QStringLiteral("%1|%2|%3")
                                .arg(preset)
                                .arg(static_cast<int>(vis))
                                .arg(aiLocked ? 1 : 0);
    if (iconKey != lastIconKey_) {
        lastIconKey_ = iconKey;
        trayIcon_->setIcon(TrayIconGenerator::generate(preset, vis, aiLocked));
    }

    const QString tooltip = TrayIconGenerator::buildTooltip(connected_, snapshot_);
    if (tooltip != lastTooltip_) {
        lastTooltip_ = tooltip;
        trayIcon_->setToolTip(tooltip);
    }

    const QByteArray nextSig = buildMenuSignature();
    if (!forceMenu && !menuDirty_ && nextSig == lastMenuSig_) {
        return;
    }
    if (isMenuTreeVisible()) {
        menuDirty_ = true;
        return;
    }
    rebuildMenuContents();
}

void TrayController::rebuildMenuContents() {
    const QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    const QJsonObject runtime = snapshot_.value(QStringLiteral("runtime")).toObject();

    const QString preset = settings.value(QStringLiteral("tray_icon_preset")).toString(QStringLiteral("default"));
    const bool sysProxy = settings.value(QStringLiteral("system_proxy_enabled")).toBool(false);
    const bool enhanced = settings.value(QStringLiteral("enhanced_mode")).toBool(false);
    const bool aiLocked = settings.value(QStringLiteral("claudeProxyLockEnabled")).toBool(false);
    const QString aiTarget = settings.value(QStringLiteral("claudeProxyLockTarget")).toString();
    const QString mode = settings.value(QStringLiteral("outbound_mode")).toString(QStringLiteral("rule")).toLower();
    const QString selProfile = settings.value(QStringLiteral("selected_profile")).toString(QStringLiteral("config"));
    const int mixedPort = runtime.value(QStringLiteral("mixed_port")).toInt(7890);

    lastMenuSig_ = buildMenuSignature();
    menuDirty_ = false;

    const auto oldSubMenus = trayMenu_->findChildren<QMenu*>(QString(), Qt::FindDirectChildrenOnly);
    trayMenu_->clear();
    for (QMenu* sub : oldSubMenus) {
        sub->deleteLater();
    }

    // 0. Top subscription quota & expiry status row (aligned with macOS ClashFX)
    const QJsonObject activeSubInfo = snapshot_.value(QStringLiteral("active_subscription_info")).toObject();
    const QString subStatusLine = buildTopSubscriptionStatusText(selProfile, activeSubInfo);
    if (!subStatusLine.isEmpty()) {
        QAction* subStatusAct = trayMenu_->addAction(subStatusLine);
        subStatusAct->setEnabled(false);
        trayMenu_->addSeparator();
    }

    // 1. Open Console
    QAction* openAct = trayMenu_->addAction(L("打开控制台 (MetaCubeXD)", "Open Console (MetaCubeXD)"),
                                            this,
                                            &TrayController::openConsole);
    QFont boldFont = openAct->font();
    boldFont.setBold(true);
    openAct->setFont(boldFont);

    trayMenu_->addSeparator();

    // 2. Outbound Mode submenu
    QString modeTitle = L("出站模式", "Outbound Mode") + QStringLiteral(" (%1)").arg(mode.toUpper());
    if (aiLocked) {
        modeTitle += L(" [AI 锁定中]", " [AI Locked]");
    }
    QMenu* modeMenu = trayMenu_->addMenu(modeTitle);
    auto* modeGroup = new QActionGroup(modeMenu);
    const QList<QPair<QString, QString>> modes = {
        {QStringLiteral("rule"), L("规则判断 (Rule)", "Rule")},
        {QStringLiteral("global"), L("全局连接 (Global)", "Global")},
        {QStringLiteral("direct"), L("直接连接 (Direct)", "Direct")},
    };
    for (const auto& pair : modes) {
        QAction* act = modeMenu->addAction(pair.second);
        act->setCheckable(true);
        act->setChecked(mode == pair.first);
        act->setEnabled(connected_ && !aiLocked);
        modeGroup->addAction(act);
        const QString targetMode = pair.first;
        connect(act, &QAction::triggered, this, [this, targetMode]() {
            QJsonObject p;
            p.insert(QStringLiteral("mode"), targetMode);
            client_->call(QStringLiteral("set_outbound_mode"), p);
        });
    }

    // 3. Dynamic Proxy Groups (GLOBAL only shown in Global mode; hidden groups already filtered)
    const QJsonArray groups = snapshot_.value(QStringLiteral("proxy_groups")).toArray();
    bool addedGroupsSep = false;
    for (const QJsonValue& gv : groups) {
        const QJsonObject g = gv.toObject();
        const QString gName = g.value(QStringLiteral("name")).toString();
        const QString gType = g.value(QStringLiteral("type")).toString();
        const QString now = g.value(QStringLiteral("now")).toString();
        const bool hasManual = g.value(QStringLiteral("has_manual_override")).toBool(false);

        if (gName == QStringLiteral("GLOBAL") && mode != QStringLiteral("global")) {
            continue;
        }
        if (!addedGroupsSep) {
            trayMenu_->addSeparator();
            addedGroupsSep = true;
        }

        const bool isLoadBalance = (gType.compare(QStringLiteral("LoadBalance"), Qt::CaseInsensitive) == 0 ||
                                    gType.compare(QStringLiteral("load-balance"), Qt::CaseInsensitive) == 0);
        const bool isRelay = (gType.compare(QStringLiteral("Relay"), Qt::CaseInsensitive) == 0);
        const bool isAutoGroup = (gType.compare(QStringLiteral("URLTest"), Qt::CaseInsensitive) == 0 ||
                                  gType.compare(QStringLiteral("Fallback"), Qt::CaseInsensitive) == 0);

        QString groupMenuTitle = gName;
        if (isLoadBalance) {
            groupMenuTitle = QStringLiteral("%1  (%2)").arg(gName, L("负载均衡", "Load Balance"));
        } else if (!now.isEmpty()) {
            groupMenuTitle = QStringLiteral("%1  (%2)").arg(gName, now);
        }
        QMenu* gMenu = trayMenu_->addMenu(groupMenuTitle);

        QAction* benchGrpAct = gMenu->addAction(L("测试本组延迟", "Test Group Latency"));
        benchGrpAct->setEnabled(connected_);
        connect(benchGrpAct, &QAction::triggered, this, [this, gName]() {
            QJsonObject p;
            p.insert(QStringLiteral("group"), gName);
            client_->call(QStringLiteral("benchmark_group"), p);
        });
        gMenu->addSeparator();

        const QJsonArray items = g.value(QStringLiteral("items")).toArray();
        for (const QJsonValue& iv : items) {
            const QJsonObject it = iv.toObject();
            const QString itemName = it.value(QStringLiteral("name")).toString();
            const int delay = it.value(QStringLiteral("delay_ms")).toInt(0);
            const bool tested = it.value(QStringLiteral("tested")).toBool(false);
            QString title = itemName;
            if (delay > 0) {
                title += QStringLiteral("   (%1 ms)").arg(delay);
            } else if (tested && itemName != QStringLiteral("DIRECT") && itemName != QStringLiteral("REJECT")) {
                title += QStringLiteral("   (%1)").arg(L("超时", "Timeout"));
            }
            QAction* itemAct = gMenu->addAction(title);
            if (isLoadBalance || isRelay) {
                itemAct->setEnabled(false);
            } else {
                itemAct->setCheckable(true);
                itemAct->setChecked(itemName == now);
                connect(itemAct, &QAction::triggered, this, [this, gName, itemName]() {
                    QJsonObject p;
                    p.insert(QStringLiteral("group"), gName);
                    p.insert(QStringLiteral("target"), itemName);
                    client_->call(QStringLiteral("select_proxy"), p);
                });
            }
        }

        const bool showDiag = (!isLoadBalance && !now.isEmpty() &&
                               now != QStringLiteral("DIRECT") && now != QStringLiteral("REJECT"));
        if (isAutoGroup || showDiag) {
            gMenu->addSeparator();
            if (isAutoGroup) {
                if (hasManual) {
                    QAction* autoAct = gMenu->addAction(L("恢复自动选择", "Restore Automatic Selection"));
                    connect(autoAct, &QAction::triggered, this, [this, gName]() {
                        QJsonObject p;
                        p.insert(QStringLiteral("group"), gName);
                        client_->call(QStringLiteral("restore_auto_selection"), p);
                    });
                } else {
                    QAction* autoHintAct = gMenu->addAction(L("自动测速择优中", "Automatic Selection Active"));
                    autoHintAct->setEnabled(false);
                }
            }
            if (showDiag) {
                QAction* diagAct = gMenu->addAction(L("诊断当前节点…", "Diagnose Current Node…"));
                connect(diagAct, &QAction::triggered, this, [this, now]() {
                    openDiagnosticsDialog(now);
                });
            }
        }
    }

    trayMenu_->addSeparator();

    // 4. System Proxy / Enhanced TUN / Turn Off All
    if (aiLocked) {
        QAction* lockHint = trayMenu_->addAction(
            L("已由 AI 专用锁定接管代理开关与规则模式", "Proxy & Rule mode managed by AI Proxy Lock"));
        lockHint->setEnabled(false);
    }
    QAction* sysAct = trayMenu_->addAction(L("设置为系统代理", "Set as System Proxy"));
    sysAct->setCheckable(true);
    sysAct->setChecked(sysProxy);
    sysAct->setEnabled(connected_ && !aiLocked);
    connect(sysAct, &QAction::triggered, this, [this](bool checked) {
        QJsonObject p;
        p.insert(QStringLiteral("enabled"), checked);
        client_->call(QStringLiteral("set_system_proxy"), p);
    });

    QAction* tunAct = trayMenu_->addAction(L("增强模式 (TUN)", "Enhanced Mode (TUN)"));
    tunAct->setCheckable(true);
    tunAct->setChecked(enhanced);
    tunAct->setEnabled(connected_ && !aiLocked);
    connect(tunAct, &QAction::triggered, this, [this](bool checked) {
        QJsonObject p;
        p.insert(QStringLiteral("enabled"), checked);
        p.insert(QStringLiteral("allow_uac"), true);
        client_->call(QStringLiteral("set_enhanced_mode"), p, [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
            if (!ok) {
                trayIcon_->showMessage(QStringLiteral("ClashFX"), err, QSystemTrayIcon::Warning);
            }
        });
    });

    QAction* offAllAct = trayMenu_->addAction(L("关闭所有代理模式", "Turn Off All Proxy Modes"));
    offAllAct->setEnabled(connected_ && !aiLocked);
    connect(offAllAct, &QAction::triggered, this, [this]() {
        client_->call(QStringLiteral("turn_off_all_proxies"));
    });

    trayMenu_->addSeparator();

    // 5. Exclusive features: AI Proxy Lock & Multi-Node Proxies
    QString aiMenuTitle = L("AI 专用锁定…", "AI Proxy Lock…");
    if (aiLocked && !aiTarget.isEmpty()) {
        aiMenuTitle += QStringLiteral("  [%1]").arg(aiTarget);
    }
    trayMenu_->addAction(aiMenuTitle, this, &TrayController::openAiLockDialog);
    trayMenu_->addAction(L("多节点固定端口代理…", "Multiple Node Proxies…"), this, &TrayController::openMultiNodeDialog);
    int enabledCustomCount = 0;
    for (const QJsonValue& rv : settings.value(QStringLiteral("custom_rules")).toArray()) {
        if (rv.toObject().value(QStringLiteral("enabled")).toBool(false)) {
            ++enabledCustomCount;
        }
    }
    QString rulesMenuTitle = L("分流规则编辑器…", "Routing Rules Editor…");
    if (enabledCustomCount > 0) {
        rulesMenuTitle += L("  [%1 条自定义]", "  [%1 Custom]").arg(enabledCustomCount);
    }
    trayMenu_->addAction(rulesMenuTitle, this, &TrayController::openRuleEditorDialog);

    trayMenu_->addSeparator();

    // 6. Config submenu
    QMenu* cfgMenu = trayMenu_->addMenu(L("配置管理", "Config") + QStringLiteral(" (%1)").arg(selProfile));
    for (const QJsonValue& pv : snapshot_.value(QStringLiteral("profiles")).toArray()) {
        const QString pName = pv.toString();
        QAction* pAct = cfgMenu->addAction(pName);
        pAct->setCheckable(true);
        pAct->setChecked(pName == selProfile);
        connect(pAct, &QAction::triggered, this, [this, pName]() {
            auto doSelect = [this, pName]() {
                QJsonObject p;
                p.insert(QStringLiteral("name"), pName);
                client_->call(QStringLiteral("select_profile"), p, [this, pName](bool ok, const QJsonValue&, const QString& err, const QString&) {
                    handleConfigMutationResult(
                        L("切换配置", "Switch Profile"),
                        [this, pName]() {
                            QJsonObject retryP;
                            retryP.insert(QStringLiteral("name"), pName);
                            client_->call(QStringLiteral("select_profile"), retryP);
                        },
                        ok,
                        err);
                });
            };
            doSelect();
        });
    }
    cfgMenu->addSeparator();
    cfgMenu->addAction(L("重载当前配置", "Reload Config"), this, [this]() {
        client_->call(QStringLiteral("reload_config"), QJsonObject(), [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
            handleConfigMutationResult(
                L("重载当前配置", "Reload Config"),
                [this]() { client_->call(QStringLiteral("reload_config")); },
                ok,
                err);
        });
    });
    cfgMenu->addAction(L("分流规则编辑器…", "Routing Rules Editor…"), this, &TrayController::openRuleEditorDialog);
    cfgMenu->addAction(L("编辑 Profile Mixin…", "Edit Profile Mixin…"), this, [this]() {
        client_->call(QStringLiteral("ensure_profile_mixin"), QJsonObject(), [](bool ok, const QJsonValue& data, const QString&, const QString&) {
            if (ok && data.isObject()) {
                const QString path = data.toObject().value(QStringLiteral("path")).toString();
                if (!path.isEmpty()) {
                    QDesktopServices::openUrl(QUrl::fromLocalFile(path));
                }
            }
        });
    });
    cfgMenu->addAction(L("打开配置文件夹", "Open Config Folder"), this, [this]() {
        const QString dir = snapshot_.value(QStringLiteral("config_dir")).toString();
        if (!dir.isEmpty()) {
            QDesktopServices::openUrl(QUrl::fromLocalFile(dir));
        }
    });
    cfgMenu->addAction(L("托管订阅管理…", "Managed Subscriptions…"), this, &TrayController::openSubscriptionsDialog);
    cfgMenu->addAction(L("更新 GEO 数据库 (MMDB / GeoSite)", "Update GEO Databases"), this, [this]() {
        client_->call(QStringLiteral("update_geo_databases"), QJsonObject(), [this](bool ok, const QJsonValue&, const QString& err, const QString&) {
            trayIcon_->showMessage(
                QStringLiteral("ClashFX"),
                ok ? L("GEO 数据库更新完成并已热重载。", "GEO databases updated successfully.") : err);
        });
    });

    // 7. Speed test & Copy terminal command
    trayMenu_->addAction(L("一键延迟测速", "Latency Speed Test"), this, [this]() {
        client_->call(QStringLiteral("benchmark_all"));
    });

    QMenu* copyCmdMenu = trayMenu_->addMenu(L("复制终端代理命令", "Copy Terminal Proxy Command"));
    copyCmdMenu->addAction(QStringLiteral("PowerShell"), this, [mixedPort]() {
        const QString cmd = QStringLiteral(
            "$env:HTTP_PROXY=\"http://127.0.0.1:%1\"; $env:HTTPS_PROXY=\"http://127.0.0.1:%1\"; $env:ALL_PROXY=\"socks5://127.0.0.1:%1\"")
                                .arg(mixedPort);
        QApplication::clipboard()->setText(cmd);
    });
    copyCmdMenu->addAction(QStringLiteral("CMD"), this, [mixedPort]() {
        const QString cmd = QStringLiteral(
            "set HTTP_PROXY=http://127.0.0.1:%1 & set HTTPS_PROXY=http://127.0.0.1:%1")
                                .arg(mixedPort);
        QApplication::clipboard()->setText(cmd);
    });
    copyCmdMenu->addAction(QStringLiteral("Bash / WSL"), this, [mixedPort]() {
        const QString cmd = QStringLiteral(
            "export https_proxy=http://127.0.0.1:%1 http_proxy=http://127.0.0.1:%1 all_proxy=socks5://127.0.0.1:%1")
                                .arg(mixedPort);
        QApplication::clipboard()->setText(cmd);
    });

    // 8. Settings submenu
    QMenu* settingsMenu = trayMenu_->addMenu(L("设置", "Settings"));
    settingsMenu->addAction(L("偏好设置…", "Preferences…"), this, &TrayController::openSettingsDialog);
    settingsMenu->addSeparator();

    auto addToggleSetting = [&](const QString& title, const QString& jsonKey) {
        QAction* act = settingsMenu->addAction(title);
        act->setCheckable(true);
        act->setChecked(settings.value(jsonKey).toBool(false));
        act->setEnabled(connected_);
        connect(act, &QAction::triggered, this, [this, jsonKey](bool checked) {
            mutateSettingBool(jsonKey, checked);
        });
    };

    addToggleSetting(L("允许局域网连接 (Allow LAN)", "Allow LAN"), QStringLiteral("allow_lan"));
    addToggleSetting(L("启用 IPv6", "Enable IPv6"), QStringLiteral("ipv6"));
    addToggleSetting(L("绕过常用国内应用", "Bypass Common Chinese Apps"), QStringLiteral("bypass_cn_apps"));
    addToggleSetting(L("增强模式按原样使用配置", "Use Custom Config as-is"), QStringLiteral("custom_config_as_is"));
    addToggleSetting(L("开机自动启动", "Launch at Login"), QStringLiteral("launch_at_login"));

    settingsMenu->addSeparator();

    QMenu* iconStyleMenu = settingsMenu->addMenu(L("托盘图标风格", "Tray Icon Style"));
    const QList<QPair<QString, QString>> presets = {
        {QStringLiteral("default"), L("经典猫咪 (Default)", "Default Cat")},
        {QStringLiteral("monochrome"), L("极简闪电 (Monochrome)", "Monochrome Lightning")},
        {QStringLiteral("ocean"), L("海洋护盾 (Ocean)", "Ocean Shield")},
    };
    for (const auto& pr : presets) {
        QAction* act = iconStyleMenu->addAction(pr.second);
        act->setCheckable(true);
        act->setChecked(preset.compare(pr.first, Qt::CaseInsensitive) == 0);
        const QString val = pr.first;
        connect(act, &QAction::triggered, this, [this, val]() {
            mutateSettingString(QStringLiteral("tray_icon_preset"), val);
        });
    }

    QMenu* langMenu = settingsMenu->addMenu(L("语言 / Language", "Language / 语言"));
    for (const auto& lp : QList<QPair<QString, QString>>{
             {QStringLiteral("zh_CN"), QStringLiteral("简体中文")},
             {QStringLiteral("en_US"), QStringLiteral("English")},
         }) {
        QAction* act = langMenu->addAction(lp.second);
        act->setCheckable(true);
        act->setChecked(I18n::instance().language() == lp.first);
        const QString langCode = lp.first;
        connect(act, &QAction::triggered, this, [this, langCode]() {
            I18n::instance().setLanguage(langCode);
            mutateSettingString(QStringLiteral("language"), langCode);
        });
    }

    // 9. Help submenu
    QMenu* helpMenu = trayMenu_->addMenu(L("帮助与诊断", "Help & Diagnostics"));
    helpMenu->addAction(L("单节点测速诊断…", "Single-Node Diagnostic…"), this, [this]() { openDiagnosticsDialog(); });
    helpMenu->addAction(L("复制脱敏诊断报告", "Copy Redacted Diagnostic Info"), this, &TrayController::copyDiagnosticInfo);
    helpMenu->addAction(L("打开日志文件夹", "Open Logs Folder"), this, [this]() {
        const QString dir = snapshot_.value(QStringLiteral("logs_dir")).toString();
        if (!dir.isEmpty()) {
            QDir().mkpath(dir);
            QDesktopServices::openUrl(QUrl::fromLocalFile(dir));
        }
    });
    helpMenu->addAction(L("检查版本更新…", "Check for Updates…"), this, &TrayController::checkForUpdates);
    helpMenu->addAction(L("关于 ClashFX", "About ClashFX"), this, &TrayController::showAboutBox);

    trayMenu_->addSeparator();
    trayMenu_->addAction(L("退出 ClashFX", "Quit ClashFX"), this, &TrayController::quitAll);
}

void TrayController::mutateSettingBool(const QString& key, bool value) {
    if (!client_ || snapshot_.isEmpty()) return;
    QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    settings.insert(key, value);
    QJsonObject params;
    params.insert(QStringLiteral("settings"), settings);
    client_->call(QStringLiteral("update_settings"), params);
}

void TrayController::mutateSettingString(const QString& key, const QString& value) {
    if (!client_ || snapshot_.isEmpty()) return;
    QJsonObject settings = snapshot_.value(QStringLiteral("settings")).toObject();
    settings.insert(key, value);
    QJsonObject params;
    params.insert(QStringLiteral("settings"), settings);
    client_->call(QStringLiteral("update_settings"), params);
}

void TrayController::importSchemeUri(const QString& uri) {
    if (!client_ || uri.isEmpty()) return;
    QJsonObject params;
    params.insert(QStringLiteral("uri"), uri);
    client_->call(QStringLiteral("import_url_scheme"), params, [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
        if (!ok) {
            trayIcon_->showMessage(QStringLiteral("ClashFX"),
                                   L("导入订阅链接失败: ", "Failed to import URI: ") + err,
                                   QSystemTrayIcon::Warning);
            return;
        }
        const QString name = data.toObject().value(QStringLiteral("name")).toString();
        trayIcon_->showMessage(QStringLiteral("ClashFX"),
                               L("已成功导入并激活配置: ", "Imported and activated profile: ") + name);
        client_->call(QStringLiteral("get_state"));
    });
}

void TrayController::copyDiagnosticInfo() {
    if (!client_) return;
    client_->call(QStringLiteral("get_diagnostic_info"), QJsonObject(), [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
        if (!ok) {
            trayIcon_->showMessage(QStringLiteral("ClashFX"), err, QSystemTrayIcon::Warning);
            return;
        }
        const QString rep = data.toObject().value(QStringLiteral("report")).toString();
        QApplication::clipboard()->setText(rep);
        trayIcon_->showMessage(QStringLiteral("ClashFX"),
                               L("脱敏诊断信息已复制到剪贴板。", "Redacted diagnostic info copied to clipboard."));
    });
}

void TrayController::checkForUpdates() {
    if (!client_) return;
    client_->call(QStringLiteral("check_updates"), QJsonObject(), [this](bool ok, const QJsonValue& data, const QString& err, const QString&) {
        if (!ok) {
            QMessageBox::warning(consoleWindow_, L("检查更新", "Check for Updates"), err);
            return;
        }
        const QJsonObject res = data.toObject();
        const QString curVer = res.value(QStringLiteral("current_version")).toString();
        const bool avail = res.value(QStringLiteral("update_available")).toBool(false);
        const QString latest = res.value(QStringLiteral("latest_version")).toString();
        const QString url = res.value(QStringLiteral("download_url")).toString().isEmpty()
                                ? res.value(QStringLiteral("release_url")).toString()
                                : res.value(QStringLiteral("download_url")).toString();
        if (!avail) {
            QMessageBox::information(consoleWindow_,
                                     L("检查更新", "Check for Updates"),
                                     L("当前已是最新版本 (", "You are running the latest version (") + curVer + QStringLiteral(")."));
            return;
        }
        const auto btn = QMessageBox::question(
            consoleWindow_,
            L("发现新版本", "Update Available"),
            L("发现新版本 ", "New version available: ") + latest +
                L("\n是否立即在浏览器打开下载页面？", "\nOpen download page in browser now?"));
        if (btn == QMessageBox::Yes && !url.isEmpty()) {
            QDesktopServices::openUrl(QUrl(url));
        }
    });
}

void TrayController::showAboutBox() {
    const QString ver = snapshot_.value(QStringLiteral("version")).toString(QStringLiteral("1.19.24-win.1"));
    const QString arch = snapshot_.value(QStringLiteral("arch")).toString();
    QMessageBox::about(
        consoleWindow_,
        L("关于 ClashFX", "About ClashFX"),
        QStringLiteral("<h3>ClashFX for Windows</h3>"
                       "<p>Version: <b>%1</b> (%2)</p>"
                       "<p>Qt %3 + Microsoft Edge WebView2 + Mihomo Core v1.19.24</p>"
                       "<p>Features: AI Proxy Lock, Multi-Node Fixed Ports &amp; 204 Recovery, Wintun Service.</p>")
            .arg(ver, arch, QString::fromLatin1(qVersion())));
}

void TrayController::openConsole() {
    consoleWindow_->presentWindow();
}

void TrayController::openAiLockDialog() {
    if (!aiLockDlg_) {
        aiLockDlg_ = new AiLockDialog(client_, consoleWindow_);
        aiLockDlg_->setAttribute(Qt::WA_DeleteOnClose, true);
    }
    aiLockDlg_->show();
    aiLockDlg_->raise();
    aiLockDlg_->activateWindow();
}

void TrayController::openMultiNodeDialog() {
    if (!multiNodeDlg_) {
        multiNodeDlg_ = new MultiNodeDialog(client_, consoleWindow_);
        multiNodeDlg_->setAttribute(Qt::WA_DeleteOnClose, true);
    }
    multiNodeDlg_->show();
    multiNodeDlg_->raise();
    multiNodeDlg_->activateWindow();
}

void TrayController::openRuleEditorDialog() {
    if (!rulesDlg_) {
        rulesDlg_ = new RuleEditorDialog(client_, consoleWindow_);
        rulesDlg_->setAttribute(Qt::WA_DeleteOnClose, true);
    }
    rulesDlg_->show();
    rulesDlg_->raise();
    rulesDlg_->activateWindow();
}

void TrayController::openSubscriptionsDialog() {
    if (!subsDlg_) {
        subsDlg_ = new SubscriptionsDialog(client_, consoleWindow_);
        subsDlg_->setAttribute(Qt::WA_DeleteOnClose, true);
    }
    subsDlg_->show();
    subsDlg_->raise();
    subsDlg_->activateWindow();
}

void TrayController::openDiagnosticsDialog(const QString& preselectNode) {
    if (!diagDlg_) {
        diagDlg_ = new NodeDiagnosticDialog(client_, consoleWindow_);
        diagDlg_->setAttribute(Qt::WA_DeleteOnClose, true);
    }
    if (!preselectNode.isEmpty()) {
        diagDlg_->preselectNode(preselectNode);
    }
    diagDlg_->show();
    diagDlg_->raise();
    diagDlg_->activateWindow();
}

void TrayController::openSettingsDialog() {
    if (!settingsDlg_) {
        settingsDlg_ = new SettingsDialog(client_, consoleWindow_);
        settingsDlg_->setAttribute(Qt::WA_DeleteOnClose, true);
    }
    settingsDlg_->show();
    settingsDlg_->raise();
    settingsDlg_->activateWindow();
}

void TrayController::quitAll() {
    if (consoleWindow_) {
        consoleWindow_->setAllowClose(true);
    }
    if (client_ && client_->isConnected()) {
        client_->stopReconnect();
        client_->call(QStringLiteral("quit_daemon"), QJsonObject(), [](bool, const QJsonValue&, const QString&, const QString&) {
            QTimer::singleShot(120, qApp, &QCoreApplication::quit);
        });
        QTimer::singleShot(800, qApp, &QCoreApplication::quit);
    } else {
        if (client_) client_->stopReconnect();
        QCoreApplication::quit();
    }
}

} // namespace clashfx
