import { ICONS } from './icons.js';
import {
  applyTheme,
  esc,
  formatSpeed,
  initBridgeListener,
  native,
  postNative,
  rpc,
  showToast,
  store,
  subscribe,
  t,
  toggleLanguage,
} from './bridge.js';
import { renderRulesPage, renderSubscriptionsPage, setRulesSubTab } from './pages-core.js';
import {
  applyAppUpdateCheck,
  getAvailableAppUpdate,
  isDiagConnectionsActive,
  renderAiLockPage,
  renderDiagnosticsPage,
  renderMultiNodePage,
  renderSettingsPage,
  setDiagSubTab,
  updateConnectionsTelemetry,
} from './pages-ops.js';

const PAGES = [
  { id: 0, icon: 'dashboard', zh: '控制台', en: 'Console', titleZh: '控制台 (MetaCubeXD)', titleEn: 'Console (MetaCubeXD)' },
  { id: 1, icon: 'subscriptions', zh: '订阅与配置', en: 'Subscriptions' },
  { id: 2, icon: 'rules', zh: '分流与规则', en: 'Routing & Rules' },
  { id: 3, icon: 'aiLock', zh: 'AI 锁定', en: 'AI Lock' },
  { id: 4, icon: 'multiNode', zh: '多节点服务', en: 'Multi-Node' },
  { id: 5, icon: 'diagnostics', zh: '节点诊断', en: 'Diagnostics' },
  { id: 6, icon: 'settings', zh: '偏好设置', en: 'Preferences' },
];

const pageRoot = document.getElementById('page-content');
const consoleHost = document.getElementById('console-host');
const consoleBanner = document.getElementById('console-banner');
const consoleStandby = document.getElementById('console-standby');
const consoleFrame = document.getElementById('metacubexd-frame');
const consoleTools = document.getElementById('console-topbar-tools');

let pendingPageRender = false;
let renderSeq = 0;
let loadedConsoleKey = '';
let sidebarNavBuilt = false;
let sidebarDockBuilt = false;
let dockProfileSig = '';
let lastPageSig = '';
let startupUpdateChecked = false;
let coreUpgradeInFlight = false;

function ensureSidebarNavBuilt(navEl) {
  if (!navEl || sidebarNavBuilt) return;
  navEl.innerHTML = PAGES.map(
    (p) => `
      <button type="button" class="nav-item" data-nav-id="${p.id}">
        ${ICONS[p.icon]}
        <span class="nav-label"></span>
        <span class="nav-trailing" hidden></span>
      </button>`
  ).join('');
  navEl.querySelectorAll('[data-nav-id]').forEach((btn) => {
    btn.addEventListener('click', () => go(Number(btn.getAttribute('data-nav-id'))));
  });
  sidebarNavBuilt = true;
}

function ensureSidebarDockBuilt(dockEl) {
  if (!dockEl || sidebarDockBuilt) return;
  dockEl.innerHTML = `
    <div class="dock-row-grid">
      <button type="button" class="dock-toggle-card" id="dock-sys-btn">
        <div class="dock-toggle-top">
          <span class="dock-toggle-label" id="dock-sys-label"></span>
          <span class="mini-pill"></span>
        </div>
        <span class="dock-toggle-sub" id="dock-sys-sub"></span>
      </button>

      <button type="button" class="dock-toggle-card" id="dock-tun-btn">
        <div class="dock-toggle-top">
          <span class="dock-toggle-label" id="dock-tun-label"></span>
          <span class="mini-pill"></span>
        </div>
        <span class="dock-toggle-sub" id="dock-tun-sub"></span>
      </button>
    </div>

    <div class="dock-profile-box">
      <div class="dock-profile-head">
        <span id="dock-profile-label"></span>
        <span class="dock-speed-inline" id="dock-speed">↑ 0 B/s · ↓ 0 B/s</span>
      </div>
      <div class="dock-profile-controls">
        <select class="select select-sm" id="dock-profile-select"></select>
        <button type="button" class="btn btn-sm btn-icon" id="dock-refresh-sub">${ICONS.refresh}</button>
      </div>
    </div>
  `;

  dockEl.querySelector('#dock-sys-btn')?.addEventListener('click', () => {
    const cur = Boolean(store.snapshot?.settings?.system_proxy_enabled);
    rpc('set_system_proxy', { enabled: !cur });
  });
  dockEl.querySelector('#dock-tun-btn')?.addEventListener('click', () => {
    const cur = Boolean(store.snapshot?.settings?.enhanced_mode);
    rpc('set_enhanced_mode', { enabled: !cur });
  });
  dockEl.querySelector('#dock-profile-select')?.addEventListener('change', async (e) => {
    const name = e.target.value;
    const res = await rpc('select_profile', { name });
    if (res.ok) showToast(t(`已切换配置: ${name}`, `Active profile: ${name}`), 'ok');
  });
  dockEl.querySelector('#dock-refresh-sub')?.addEventListener('click', async () => {
    const snapNow = store.snapshot || {};
    const profNow = snapNow.settings?.selected_profile || 'config';
    const hasSub = (snapNow.subscriptions || []).some((s) => s.name === profNow);
    const res = hasSub
      ? await rpc('refresh_subscription', { name: profNow })
      : await rpc('reload_config', {});
    if (res.ok) {
      showToast(hasSub ? t(`订阅「${profNow}」已更新`, `Updated "${profNow}"`) : t('配置已重载', 'Config reloaded'), 'ok');
    }
  });
  sidebarDockBuilt = true;
}

function computePageRenderSignature() {
  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  switch (store.page) {
    case 0:
      return 'console';
    case 1:
      return JSON.stringify([
        store.connected,
        store.language,
        settings.selected_profile || '',
        snap.config_dir || '',
        snap.profiles || [],
        snap.subscriptions || [],
      ]);
    case 2:
      return JSON.stringify([
        store.connected,
        store.language,
        Boolean(snap.core_running),
        settings.selected_profile || '',
        settings.outbound_mode || 'rule',
        Boolean(settings.enhanced_mode),
        Boolean(settings.custom_config_as_is),
        Boolean(settings.claudeProxyLockEnabled),
        settings.claudeProxyLockTarget || '',
        Boolean(settings.bypass_cn_apps),
        snap.config_dir || '',
        snap.profile_mixin_path || '',
        snap.default_rule_target || '',
        (snap.proxy_groups || []).map((g) => g.name),
      ]);
    case 3:
      return JSON.stringify([
        store.connected,
        store.language,
        Boolean(settings.claudeProxyLockEnabled),
        settings.claudeProxyLockTarget || '',
        snap.ai_lock_relay || '',
        snap.ai_lock_candidates || [],
        snap.proxy_groups || [],
      ]);
    case 4:
      return JSON.stringify([store.connected, store.language, snap.node_services || null]);
    case 5:
      return JSON.stringify([
        store.connected,
        store.language,
        snap.logs_dir || '',
        snap.active_network || null,
        (snap.ai_lock_candidates || []).map((c) => c.name),
        (snap.proxy_groups || []).map((g) => [g.name, (g.items || []).map((i) => i.name)]),
      ]);
    case 6:
      return JSON.stringify([
        store.connected,
        store.language,
        store.theme,
        snap.version || '',
        snap.core_version || '',
        snap.config_dir || '',
        snap.active_network || null,
        Boolean(snap.service_installed),
        Boolean(snap.core_via_service),
        settings,
      ]);
    default:
      return '';
  }
}

function isUserTypingInPage() {
  const el = document.activeElement;
  if (!el || !pageRoot || !pageRoot.contains(el)) return false;
  return el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT';
}

function go(pageIdx, subTab = -1, diagNode = '') {
  store.page = Math.max(0, Math.min(6, Number(pageIdx) || 0));
  if (store.page === 2 && subTab >= 0) setRulesSubTab(subTab);
  if (store.page === 5) setDiagSubTab(subTab >= 0 ? subTab : 0, diagNode);
  postNative({ type: 'nav_sync', page: store.page, subTab });
  postNative({
    type: 'telemetry_subscribe',
    enabled: true,
    include_connections: isDiagConnectionsActive(),
  });
  renderAll(true);
}

function renderChrome() {
  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  const runtime = snap.runtime || {};
  const mode = (settings.outbound_mode || 'rule').toLowerCase();
  const sysOn = Boolean(settings.system_proxy_enabled);
  const tunOn = Boolean(settings.enhanced_mode);
  const aiOn = Boolean(settings.claudeProxyLockEnabled);
  const subsCount = Array.isArray(snap.subscriptions) ? snap.subscriptions.length : 0;
  const nsCount = Array.isArray(snap.node_services?.rows) ? snap.node_services.rows.filter((r) => r.enabled).length : 0;
  const profiles = Array.isArray(snap.profiles) ? snap.profiles : [];
  const activeProf = settings.selected_profile || 'config';
  const port = runtime.mixed_port || settings.port_override || 7890;
  const armedStandby = subsCount === 0 && !aiOn;

  // Brand status line
  const verEl = document.getElementById('brand-version');
  const subEl = document.getElementById('brand-status-line');
  const rawCoreVer = snap.core_version || (snap.version ? `v${String(snap.version).replace(/^v/i, '')}` : '');
  if (verEl) verEl.textContent = rawCoreVer || '--';
  if (subEl) {
    if (!store.connected) {
      subEl.className = 'brand-sub err';
      subEl.textContent = t('守护进程未连接', 'Daemon Offline');
    } else if (snap.suspended_by_network) {
      subEl.className = 'brand-sub warn';
      subEl.textContent = t('已因网络规则挂起', 'Suspended');
    } else if (snap.core_running) {
      subEl.className = 'brand-sub ok';
      subEl.textContent = `● ${t('核心运行中', 'Core Running')} :${port}`;
    } else {
      subEl.className = 'brand-sub warn';
      subEl.textContent = t('内核待机', 'Core Standby');
    }
  }

  // Outbound Mode 3-way buttons
  const modeLabels = {
    rule: t('规则', 'Rule'),
    global: t('全局', 'Global'),
    direct: t('直连', 'Direct'),
  };
  document.querySelectorAll('#outbound-mode-bar .mode-btn').forEach((btn) => {
    const m = btn.getAttribute('data-mode');
    btn.textContent = modeLabels[m];
    btn.classList.toggle('active', mode === m);
  });

  // Sidebar Navigation (in-place updates to preserve nodes and avoid DOM rebuild lag)
  const navEl = document.getElementById('sidebar-nav');
  if (navEl) {
    ensureSidebarNavBuilt(navEl);
    for (const p of PAGES) {
      const btn = navEl.querySelector(`[data-nav-id="${p.id}"]`);
      if (!btn) continue;
      btn.classList.toggle('active', store.page === p.id);
      const labelEl = btn.querySelector('.nav-label');
      const nextLabel = t(p.zh, p.en);
      if (labelEl && labelEl.textContent !== nextLabel) labelEl.textContent = nextLabel;

      const trailingEl = btn.querySelector('.nav-trailing');
      if (trailingEl) {
        if (p.id === 0) {
          trailingEl.hidden = false;
          trailingEl.className = 'nav-trailing nav-count';
          trailingEl.textContent = 'XD';
          trailingEl.title = '';
        } else if (p.id === 3 && aiOn) {
          trailingEl.hidden = false;
          trailingEl.className = 'nav-trailing nav-dot';
          trailingEl.textContent = '';
          trailingEl.title = 'AI Lock Active';
        } else if (p.id === 1 && subsCount > 0) {
          trailingEl.hidden = false;
          trailingEl.className = 'nav-trailing nav-count';
          trailingEl.textContent = String(subsCount);
          trailingEl.title = '';
        } else if (p.id === 4 && snap.node_services?.settings?.ports_enabled && nsCount > 0) {
          trailingEl.hidden = false;
          trailingEl.className = 'nav-trailing nav-count';
          trailingEl.textContent = String(nsCount);
          trailingEl.title = '';
        } else if (p.id === 6 && getAvailableAppUpdate()) {
          trailingEl.hidden = false;
          trailingEl.className = 'nav-trailing nav-count';
          trailingEl.textContent = 'NEW';
          trailingEl.title = getAvailableAppUpdate().version;
        } else {
          trailingEl.hidden = true;
          trailingEl.className = 'nav-trailing';
          trailingEl.textContent = '';
          trailingEl.title = '';
        }
      }
    }
  }

  // Sidebar Bottom Quick Dock (in-place updates)
  const dockEl = document.getElementById('sidebar-dock');
  if (dockEl) {
    ensureSidebarDockBuilt(dockEl);
    const sysBtn = dockEl.querySelector('#dock-sys-btn');
    const tunBtn = dockEl.querySelector('#dock-tun-btn');
    const sysLbl = dockEl.querySelector('#dock-sys-label');
    const tunLbl = dockEl.querySelector('#dock-tun-label');
    const sysSub = dockEl.querySelector('#dock-sys-sub');
    const tunSub = dockEl.querySelector('#dock-tun-sub');
    const profLbl = dockEl.querySelector('#dock-profile-label');
    const spdEl = dockEl.querySelector('#dock-speed');
    const refBtn = dockEl.querySelector('#dock-refresh-sub');
    const selectEl = dockEl.querySelector('#dock-profile-select');

    if (sysBtn) sysBtn.classList.toggle('is-on', sysOn);
    if (tunBtn) tunBtn.classList.toggle('is-on', tunOn);
    if (sysLbl) sysLbl.textContent = t('系统代理', 'Sys Proxy');
    if (tunLbl) tunLbl.textContent = t('TUN 增强', 'TUN Mode');
    if (sysSub) sysSub.textContent = sysOn ? (armedStandby ? t('待导入订阅', 'Pending Sub') : `:${port}`) : t('已关闭', 'Off');
    if (tunSub) tunSub.textContent = tunOn ? (armedStandby ? t('待导入订阅', 'Pending Sub') : String(settings.tun_stack || 'mixed')) : t('已关闭', 'Off');
    if (profLbl) profLbl.textContent = t('当前配置', 'Profile');
    if (spdEl) spdEl.textContent = `↑ ${formatSpeed(store.telemetry.up)} · ↓ ${formatSpeed(store.telemetry.down)}`;
    if (refBtn) refBtn.title = t('更新当前订阅 / 配置', 'Refresh active profile');

    if (selectEl && document.activeElement !== selectEl) {
      const emptyLabel = t('未导入订阅', 'No Subscription');
      const nextProfSig = JSON.stringify([activeProf, profiles, emptyLabel]);
      if (nextProfSig !== dockProfileSig) {
        dockProfileSig = nextProfSig;
        selectEl.disabled = profiles.length === 0;
        selectEl.innerHTML = profiles.length
          ? profiles.map((p) => `<option value="${esc(p)}" ${p === activeProf ? 'selected' : ''}>${esc(p)}</option>`).join('')
          : `<option value="">${esc(emptyLabel)}</option>`;
        selectEl.value = profiles.includes(activeProf) ? activeProf : (profiles[0] || '');
      }
    }
  }

  // Topbar metadata & buttons
  const pageDef = PAGES[store.page] || PAGES[0];
  const tbIcon = document.getElementById('topbar-icon');
  const tbTitle = document.getElementById('topbar-title');
  const tbChip = document.getElementById('topbar-chip');
  const winMax = document.getElementById('win-max');
  const themeBtn = document.getElementById('btn-theme-toggle');
  const reloadBtn = document.getElementById('btn-reload-core');
  const langBtn = document.getElementById('btn-lang-toggle');

  if (tbIcon) tbIcon.innerHTML = ICONS[pageDef.icon] || '';
  if (tbTitle) tbTitle.textContent = t(pageDef.titleZh || pageDef.zh, pageDef.titleEn || pageDef.en);
  if (consoleTools) {
    consoleTools.hidden = store.page !== 0;
    const reloadPanelBtn = document.getElementById('btn-reload-panel');
    const openBrowserBtn = document.getElementById('btn-open-browser');
    if (reloadPanelBtn) reloadPanelBtn.innerHTML = `${ICONS.refresh} <span>${t('刷新面板', 'Reload Panel')}</span>`;
    if (openBrowserBtn) {
      openBrowserBtn.innerHTML = ICONS.externalLink;
      openBrowserBtn.title = t('在浏览器中打开控制台', 'Open Console in Browser');
    }
  }
  if (tbChip) {
    const tags = [];
    if (sysOn) tags.push(t('系统代理', 'System'));
    if (tunOn) tags.push('TUN');
    if (aiOn) tags.push('AI Lock');
    if (!store.connected) {
      tbChip.className = 'topbar-chip err';
      tbChip.textContent = t('核心守护未连接', 'Daemon Disconnected');
    } else if (armedStandby && (sysOn || tunOn)) {
      tbChip.className = 'topbar-chip warn';
      tbChip.textContent = t('就绪 · 导入订阅后自动开启代理', 'Ready · Import Sub to Activate');
    } else if (tags.length) {
      tbChip.className = 'topbar-chip ok';
      tbChip.textContent = `${tags.join(' + ')} · :${port}`;
    } else {
      tbChip.className = 'topbar-chip warn';
      tbChip.textContent = t('直连待机 (代理未开启)', 'Standby (Proxies Off)');
    }
  }
  if (winMax) winMax.innerHTML = store.maximized ? ICONS.restore : ICONS.maximize;
  if (themeBtn) themeBtn.innerHTML = store.theme === 'dark' ? ICONS.sun : ICONS.moon;
  if (reloadBtn) reloadBtn.innerHTML = ICONS.refresh;
  if (langBtn) langBtn.textContent = store.language === 'en_US' ? 'EN / 中' : '中 / EN';
}

function postSyncToConsoleFrame({ forceTheme = false, forceLang = false } = {}) {
  if (!consoleFrame || !consoleFrame.contentWindow) return;
  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  const baseUrl = snap.dashboard_url || '';
  if (!baseUrl) return;
  let targetOrigin = '*';
  try {
    targetOrigin = new URL(baseUrl).origin;
  } catch (_) {}
  consoleFrame.contentWindow.postMessage(
    {
      type: 'clashfx_sync',
      aiLocked: Boolean(settings.claudeProxyLockEnabled),
      theme: store.theme,
      language: store.language,
      forceTheme,
      forceLang,
    },
    targetOrigin
  );
}

function syncConsoleHost(forceReload = false) {
  const onConsole = store.page === 0;
  if (consoleHost) {
    consoleHost.hidden = false;
    consoleHost.classList.toggle('is-inactive', !onConsole);
    consoleHost.inert = !onConsole;
  }
  if (pageRoot) pageRoot.hidden = onConsole;
  if (!onConsole) return;

  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  const runtime = snap.runtime || {};
  const aiOn = Boolean(settings.claudeProxyLockEnabled);
  const subsCount = Array.isArray(snap.subscriptions) ? snap.subscriptions.length : 0;

  if (consoleBanner) {
    if (coreUpgradeInFlight) {
      consoleBanner.hidden = false;
      consoleBanner.className = 'console-banner';
      consoleBanner.innerHTML = `<span>${esc(
        t(
          '正在下载并更新 Mihomo 核心，请稍候（约 10–30 秒，完成后将自动重启并刷新面板）…',
          'Downloading and upgrading Mihomo core, please wait (10–30s; console will refresh automatically)…'
        )
      )}</span>`;
    } else if (snap.last_error) {
      consoleBanner.hidden = false;
      consoleBanner.className = 'console-banner err';
      consoleBanner.innerHTML = `
        <span>${esc(snap.last_error)}</span>
        <button type="button" class="btn btn-xs btn-danger" id="console-banner-diag">${t('打开诊断', 'Diagnostics')}</button>`;
      consoleBanner.querySelector('#console-banner-diag')?.addEventListener('click', () => go(5, 0));
    } else if (snap.suspended_by_network) {
      consoleBanner.hidden = false;
      consoleBanner.className = 'console-banner';
      consoleBanner.innerHTML = `
        <span>${esc(t(`检测到受信任网络「${snap.suspended_by_network}」，代理已自动挂起。`, `Trusted network "${snap.suspended_by_network}" detected — proxy suspended.`))}</span>
        <button type="button" class="btn btn-xs" id="console-banner-settings">${t('网络规则', 'Network Rules')}</button>`;
      consoleBanner.querySelector('#console-banner-settings')?.addEventListener('click', () => go(6));
    } else if (store.connected && subsCount === 0 && !aiOn) {
      consoleBanner.hidden = false;
      consoleBanner.className = 'console-banner';
      consoleBanner.innerHTML = `
        <span>${esc(t('权限与默认分流规则已就绪（国内直连 / 局域网直连 / 国内应用直连）。导入一个订阅即可自动开启代理。', 'Permissions & direct bypass rules are ready. Import one subscription to start proxying automatically.'))}</span>
        <button type="button" class="btn btn-xs btn-primary" id="console-banner-import">${t('导入订阅', 'Import Subscription')}</button>`;
      consoleBanner.querySelector('#console-banner-import')?.addEventListener('click', () => go(1));
    } else {
      consoleBanner.hidden = true;
      consoleBanner.innerHTML = '';
    }
  }

  if (!store.connected || !snap.core_running || !snap.dashboard_url) {
    if (!snap.core_running) loadedConsoleKey = '';
    if (consoleFrame) consoleFrame.hidden = true;
    if (consoleStandby) {
      consoleStandby.hidden = false;
      const needPrivService =
        store.connected &&
        !snap.core_running &&
        /ClashFXService is not installed yet|SERVICE_INSTALL_REQUIRED/i.test(snap.last_error || '');
      if (needPrivService) {
        const subPriv =
          store.platform === 'darwin'
            ? t(
                '当前配置开启了 TUN 增强模式或 AI 专用锁定，首次运行需一次性 macOS 管理员授权安装后台服务；也可一键切换为普通系统代理模式直接启动（无需管理员权限）。',
                'Enhanced TUN Mode or AI Proxy Lock is enabled. Authorize once as macOS administrator to install the background service, or switch to standard System Proxy mode without admin privileges.'
              )
            : t(
                '当前配置开启了 TUN 增强模式或 AI 专用锁定，首次运行需一次性管理员授权安装后台服务；也可切换为普通系统代理模式直接启动。',
                'Enhanced TUN Mode or AI Proxy Lock is enabled. Authorize once to install the system service, or start in standard System Proxy mode.'
              );
        consoleStandby.innerHTML = `
          <div style="font-size:15px;font-weight:700;color:var(--text-primary);">${esc(t('需要安装系统特权服务以启用 TUN 增强 / AI 锁定', 'Privileged System Service Required for TUN / AI Lock'))}</div>
          <div style="max-width:480px;font-size:12.5px;line-height:1.55;">${esc(subPriv)}</div>
          <div style="display:flex;gap:10px;flex-wrap:wrap;justify-content:center;margin-top:4px;">
            <button type="button" class="btn btn-primary" id="console-standby-elevate">${ICONS.aiLock} ${t('授权安装服务并启动内核', 'Authorize Service & Start Core')}</button>
            <button type="button" class="btn" id="console-standby-standard">${t('切换为普通模式启动 (免管理员)', 'Start in Standard Mode')}</button>
          </div>`;
        const elevBtn = consoleStandby.querySelector('#console-standby-elevate');
        const stdBtn = consoleStandby.querySelector('#console-standby-standard');
        elevBtn?.addEventListener('click', async () => {
          elevBtn.disabled = true;
          const res = await rpc('reload_config', { allow_uac: true }, { allowUacPrompt: false });
          elevBtn.disabled = false;
          if (res.ok) showToast(t('系统服务已就绪并启动内核', 'Privileged service ready and core started'), 'ok');
        });
        stdBtn?.addEventListener('click', async () => {
          stdBtn.disabled = true;
          try {
            const cur = store.snapshot?.settings || {};
            if (cur.claudeProxyLockEnabled) {
              const r = await rpc('set_ai_lock', { enabled: false, target: '' }, { allowUacPrompt: false });
              if (!r.ok) return;
            }
            if (cur.enhanced_mode) {
              const r = await rpc('set_enhanced_mode', { enabled: false }, { allowUacPrompt: false });
              if (!r.ok) return;
            }
            if (!store.snapshot?.settings?.system_proxy_enabled) {
              await rpc('set_system_proxy', { enabled: true });
            }
            if (!store.snapshot?.core_running) {
              await rpc('reload_config', {}, { allowUacPrompt: false });
            }
            if (store.snapshot?.core_running) {
              showToast(t('已切换为普通系统代理模式并启动内核', 'Started core in standard System Proxy mode'), 'ok');
            }
          } finally {
            if (stdBtn) stdBtn.disabled = false;
          }
        });
        return;
      }
      const title = !store.connected
        ? t('核心守护进程未连接', 'Daemon Disconnected')
        : t('Mihomo 内核正在待机或启动中', 'Mihomo Core Standing By');
      const sub = !store.connected
        ? t('正在尝试重新建立核心守护进程连接…', 'Waiting for ClashFX daemon IPC connection…')
        : t('内核启动就绪后将自动加载完整 MetaCubeXD 控制台。', 'MetaCubeXD console will load automatically once the core API is ready.');
      consoleStandby.innerHTML = `
        <div style="font-size:15px;font-weight:700;color:var(--text-primary);">${esc(title)}</div>
        <div style="max-width:440px;font-size:12.5px;">${esc(sub)}</div>
        <button type="button" class="btn btn-primary" id="console-standby-reload">${ICONS.refresh} ${t('重载并启动内核', 'Reload Core')}</button>`;
      consoleStandby.querySelector('#console-standby-reload')?.addEventListener('click', async () => {
        const res = await rpc('reload_config', {});
        if (res.ok) showToast(t('已请求重载核心配置', 'Requested core reload'), 'ok');
      });
    }
    return;
  }

  if (consoleStandby) consoleStandby.hidden = true;
  if (consoleFrame) consoleFrame.hidden = false;

  const nextKey = `${runtime.api_port || 0}|${runtime.api_secret || ''}|${snap.dashboard_url}|${snap.core_version || ''}`;
  if (forceReload || loadedConsoleKey !== nextKey) {
    const langParam = store.language === 'en_US' ? 'en' : 'zh';
    const sep = snap.dashboard_url.includes('?') ? '&' : '?';
    const urlWithMeta = `${snap.dashboard_url}${sep}cfx_theme=${store.theme}&cfx_lang=${langParam}&cfx_ai=${aiOn ? '1' : '0'}${forceReload ? `&_t=${Date.now()}` : ''}`;
    loadedConsoleKey = nextKey;
    if (consoleFrame) consoleFrame.src = urlWithMeta;
  } else {
    postSyncToConsoleFrame();
  }
}

function renderPageContent() {
  if (!pageRoot || store.page === 0) return;
  const seq = ++renderSeq;
  const scrollBefore = pageRoot.scrollTop;
  pageRoot.classList.toggle('no-pad-scroll', store.page === 2);

  const handlers = { go };
  if (store.page === 1) {
    renderSubscriptionsPage(pageRoot, handlers);
  } else if (store.page === 2) {
    renderRulesPage(pageRoot, -1);
  } else if (store.page === 3) {
    renderAiLockPage(pageRoot, handlers);
  } else if (store.page === 4) {
    renderMultiNodePage(pageRoot, handlers);
  } else if (store.page === 5) {
    renderDiagnosticsPage(pageRoot, handlers);
  } else if (store.page === 6) {
    renderSettingsPage(pageRoot, handlers);
  }
  if (seq === renderSeq && store.page !== 2) {
    pageRoot.scrollTop = scrollBefore;
  }
}

function renderAll(forcePage = false) {
  renderChrome();
  syncConsoleHost(false);
  if (store.page === 0) {
    lastPageSig = '0:console';
    pendingPageRender = false;
    return;
  }
  const nextSig = `${store.page}:${computePageRenderSignature()}`;
  if (!forcePage && nextSig === lastPageSig) {
    return;
  }
  if (!forcePage && isUserTypingInPage()) {
    pendingPageRender = true;
    return;
  }
  pendingPageRender = false;
  lastPageSig = nextSig;
  renderPageContent();
}

function wireWindowControls() {
  document.getElementById('win-min')?.addEventListener('click', () => postNative({ type: 'window_control', action: 'minimize' }));
  document.getElementById('win-max')?.addEventListener('click', () => postNative({ type: 'window_control', action: 'toggle_maximize' }));
  document.getElementById('win-close')?.addEventListener('click', () => postNative({ type: 'window_control', action: 'close' }));

  document.getElementById('btn-theme-toggle')?.addEventListener('click', () => {
    applyTheme(store.theme === 'dark' ? 'light' : 'dark');
    renderAll(false);
  });

  document.getElementById('btn-lang-toggle')?.addEventListener('click', () => toggleLanguage());

  document.getElementById('btn-reload-panel')?.addEventListener('click', () => {
    syncConsoleHost(true);
    showToast(t('已刷新控制台面板', 'Reloaded MetaCubeXD console'), 'ok');
  });

  document.getElementById('btn-open-browser')?.addEventListener('click', () => {
    const url = store.snapshot?.dashboard_url || '';
    if (url) native('open_url', { url });
  });

  consoleFrame?.addEventListener('load', () => {
    postSyncToConsoleFrame();
  });

  const inFlightIframeRpc = {};
  window.addEventListener('message', (ev) => {
    const d = ev.data;
    if (!d || d.type !== 'clashfx_iframe_rpc') return;
    if (!consoleFrame || ev.source !== consoleFrame.contentWindow) return;
    if (!/^https?:\/\/(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$/i.test(String(ev.origin || ''))) return;

    const method = String(d.method || '');
    const replyOrigin = ev.origin;
    const replyToFrame = (res) => {
      try {
        consoleFrame?.contentWindow?.postMessage(
          {
            type: 'clashfx_iframe_rpc_result',
            id: d.id,
            ok: Boolean(res?.ok),
            data: res?.data ?? null,
            error: res?.error || '',
          },
          replyOrigin
        );
      } catch (_) {}
    };

    if (method !== 'upgrade_core' && method !== 'restart_core') {
      replyToFrame({ ok: false, error: `Unsupported iframe RPC: ${method}` });
      return;
    }

    if (inFlightIframeRpc[method]) {
      inFlightIframeRpc[method].then(replyToFrame);
      return;
    }

    if (method === 'upgrade_core') {
      coreUpgradeInFlight = true;
      syncConsoleHost(false);
    }

    const dismissBusy = showToast(
      method === 'upgrade_core'
        ? t('正在下载并更新 Mihomo 核心，请稍候…', 'Downloading and upgrading Mihomo core, please wait…')
        : t('正在重启 Mihomo 核心…', 'Restarting Mihomo core…'),
      'info',
      0
    );

    const reqPromise = rpc(method, d.params && typeof d.params === 'object' ? d.params : {}, { silent: true })
      .then((res) => {
        dismissBusy();
        if (method === 'upgrade_core') {
          if (!res.ok) {
            showToast(`${t('更新核心失败', 'Core upgrade failed')}: ${res.error || ''}`, 'err');
          } else if (res.data?.already_latest) {
            const ver = res.data.current_version || store.snapshot?.core_version || '';
            showToast(t(`当前已是最新核心 (${ver})`, `Core is already up to date (${ver})`), 'ok');
          } else {
            const ver = res.data?.current_version || '';
            showToast(t(`核心已更新至 ${ver} 并自动重启`, `Core upgraded to ${ver} and restarted`), 'ok');
            syncConsoleHost(true);
          }
        } else {
          if (!res.ok) {
            showToast(`${t('重启核心失败', 'Core restart failed')}: ${res.error || ''}`, 'err');
          } else {
            showToast(t('Mihomo 核心已重启', 'Mihomo core restarted'), 'ok');
            syncConsoleHost(true);
          }
        }
        return res;
      })
      .finally(() => {
        dismissBusy();
        if (method === 'upgrade_core') {
          coreUpgradeInFlight = false;
          syncConsoleHost(false);
        }
        delete inFlightIframeRpc[method];
      });

    inFlightIframeRpc[method] = reqPromise;
    reqPromise.then(replyToFrame);
  });

  document.getElementById('btn-reload-core')?.addEventListener('click', async () => {
    const res = await rpc('reload_config', {});
    if (res.ok) showToast(t('配置已热重载', 'Core configuration reloaded'), 'ok');
  });

  document.querySelectorAll('#outbound-mode-bar .mode-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      const mode = btn.getAttribute('data-mode');
      rpc('set_outbound_mode', { mode });
    });
  });

  document.querySelectorAll('[data-drag-region]').forEach((region) => {
    region.addEventListener('mousedown', (e) => {
      if (e.button !== 0 || e.detail > 1) return;
      if (e.target.closest('button, a, input, select, textarea, [data-no-drag]')) return;
      postNative({ type: 'window_drag' });
    });
    region.addEventListener('dblclick', (e) => {
      if (e.target.closest('button, a, input, select, textarea, [data-no-drag]')) return;
      if (store.platform === 'darwin' && region.classList.contains('brand-header')) return;
      postNative({ type: 'window_control', action: 'toggle_maximize' });
    });
  });

  document.querySelectorAll('[data-resize]').forEach((edgeEl) => {
    edgeEl.addEventListener('mousedown', (e) => {
      if (e.button !== 0) return;
      postNative({ type: 'window_resize', edge: edgeEl.getAttribute('data-resize') });
    });
  });

  let focusOutTimer = null;
  pageRoot?.addEventListener(
    'focusout',
    () => {
      if (focusOutTimer) clearTimeout(focusOutTimer);
      focusOutTimer = setTimeout(() => {
        focusOutTimer = null;
        if (pendingPageRender && !isUserTypingInPage()) {
          pendingPageRender = false;
          lastPageSig = `${store.page}:${computePageRenderSignature()}`;
          renderPageContent();
        }
      }, 180);
    },
    true
  );
}

function scheduleStartupUpdateCheck() {
  if (startupUpdateChecked || window.__CLASHFX_SMOKE__) return;
  startupUpdateChecked = true;
  setTimeout(async () => {
    try {
      const res = await rpc('check_updates', {}, { silent: true });
      if (res.ok && res.data) {
        const avail = applyAppUpdateCheck(res.data, store.snapshot?.version || '');
        if (avail) {
          renderAll(false);
          showToast(
            t(
              `发现新版本 ${res.data.latest_version}，可在「偏好设置」下载升级`,
              `New ClashFX ${res.data.latest_version} available in Preferences`
            ),
            'info'
          );
        }
      }
    } catch {
      // ignore offline startup update check failure
    }
  }, 4000);
}

function wireIconsAndBootstrap() {
  document.getElementById('win-min').innerHTML = ICONS.minimize;
  document.getElementById('win-max').innerHTML = ICONS.maximize;
  document.getElementById('win-close').innerHTML = ICONS.close;
  wireWindowControls();
  initBridgeListener();

  subscribe((eventType) => {
    if (eventType === 'init' || eventType === 'navigate') {
      if (eventType === 'init') scheduleStartupUpdateCheck();
      if (store.page === 2 && store.subTab >= 0) setRulesSubTab(store.subTab);
      if (store.page === 5) setDiagSubTab(store.subTab >= 0 ? store.subTab : 0, store.diagNode);
      postNative({
        type: 'telemetry_subscribe',
        enabled: true,
        include_connections: isDiagConnectionsActive(),
      });
      renderAll(true);
      return;
    }
    if (eventType === 'state') {
      renderAll(false);
      return;
    }
    if (eventType === 'theme') {
      postSyncToConsoleFrame({ forceTheme: true });
      return;
    }
    if (eventType === 'language') {
      renderAll(false);
      postSyncToConsoleFrame({ forceLang: true });
      return;
    }
    if (eventType === 'window_state') {
      const winMax = document.getElementById('win-max');
      if (winMax) winMax.innerHTML = store.maximized ? ICONS.restore : ICONS.maximize;
      return;
    }
    if (eventType === 'telemetry') {
      const dockSpd = document.getElementById('dock-speed');
      if (dockSpd) dockSpd.textContent = `↑ ${formatSpeed(store.telemetry.up)} · ↓ ${formatSpeed(store.telemetry.down)}`;
      if (store.page === 5) updateConnectionsTelemetry();
    }
  });

  window.addEventListener('error', (e) => {
    console.error('ClashFX UI Error:', e.message);
  });

  postNative({ type: 'ui_ready' });
}

wireIconsAndBootstrap();
