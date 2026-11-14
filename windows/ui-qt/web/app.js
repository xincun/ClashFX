import { ICONS } from './icons.js';
import {
  applyTheme,
  esc,
  formatSpeed,
  initBridgeListener,
  postNative,
  rpc,
  showToast,
  store,
  subscribe,
  t,
  toggleLanguage,
} from './bridge.js';
import { renderOverviewPage, renderRulesPage, renderSubscriptionsPage, setRulesSubTab, updateOverviewTelemetry } from './pages-core.js';
import {
  isDiagConnectionsActive,
  renderAiLockPage,
  renderDiagnosticsPage,
  renderMultiNodePage,
  renderSettingsPage,
  setDiagSubTab,
  updateConnectionsTelemetry,
} from './pages-ops.js';

const PAGES = [
  { id: 0, icon: 'dashboard', zh: '概览与节点', en: 'Overview & Nodes' },
  { id: 1, icon: 'subscriptions', zh: '订阅与配置', en: 'Subscriptions' },
  { id: 2, icon: 'rules', zh: '分流与规则', en: 'Routing & Rules' },
  { id: 3, icon: 'aiLock', zh: 'AI 锁定', en: 'AI Lock' },
  { id: 4, icon: 'multiNode', zh: '多节点服务', en: 'Multi-Node' },
  { id: 5, icon: 'diagnostics', zh: '节点诊断', en: 'Diagnostics' },
  { id: 6, icon: 'settings', zh: '偏好设置', en: 'Preferences' },
];

const pageRoot = document.getElementById('page-content');
let pendingPageRender = false;
let renderSeq = 0;

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

  // Brand status line
  const verEl = document.getElementById('brand-version');
  const subEl = document.getElementById('brand-status-line');
  if (verEl) verEl.textContent = snap.version ? `v${snap.version}` : '--';
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

  // Sidebar Navigation
  const navEl = document.getElementById('sidebar-nav');
  if (navEl) {
    navEl.innerHTML = PAGES.map((p) => {
      const active = store.page === p.id;
      let trailing = '';
      if (p.id === 3 && aiOn) trailing = `<span class="nav-dot" title="AI Lock Active"></span>`;
      else if (p.id === 1 && subsCount > 0) trailing = `<span class="nav-count">${subsCount}</span>`;
      else if (p.id === 4 && snap.node_services?.settings?.ports_enabled && nsCount > 0) trailing = `<span class="nav-count">${nsCount}</span>`;
      return `
        <button type="button" class="nav-item ${active ? 'active' : ''}" data-nav-id="${p.id}">
          ${ICONS[p.icon]}
          <span class="nav-label">${esc(t(p.zh, p.en))}</span>
          ${trailing}
        </button>`;
    }).join('');
    navEl.querySelectorAll('[data-nav-id]').forEach((btn) => {
      btn.addEventListener('click', () => go(Number(btn.getAttribute('data-nav-id'))));
    });
  }

  // Sidebar Bottom Quick Dock
  const dockEl = document.getElementById('sidebar-dock');
  if (dockEl) {
    dockEl.innerHTML = `
      <div class="dock-row-grid">
        <button type="button" class="dock-toggle-card ${sysOn ? 'is-on' : ''}" id="dock-sys-btn">
          <div class="dock-toggle-top">
            <span class="dock-toggle-label">${t('系统代理', 'Sys Proxy')}</span>
            <span class="mini-pill"></span>
          </div>
          <span class="dock-toggle-sub">${sysOn ? `:${port}` : t('已关闭', 'Off')}</span>
        </button>

        <button type="button" class="dock-toggle-card ${tunOn ? 'is-on' : ''}" id="dock-tun-btn">
          <div class="dock-toggle-top">
            <span class="dock-toggle-label">${t('TUN 增强', 'TUN Mode')}</span>
            <span class="mini-pill"></span>
          </div>
          <span class="dock-toggle-sub">${tunOn ? esc(settings.tun_stack || 'mixed') : t('已关闭', 'Off')}</span>
        </button>
      </div>

      <div class="dock-profile-box">
        <div class="dock-profile-head">
          <span>${t('当前配置', 'Profile')}</span>
          <span class="dock-speed-inline" id="dock-speed">↑ ${formatSpeed(store.telemetry.up)} · ↓ ${formatSpeed(store.telemetry.down)}</span>
        </div>
        <div class="dock-profile-controls">
          <select class="select select-sm" id="dock-profile-select">
            ${
              profiles.length
                ? profiles.map((p) => `<option value="${esc(p)}" ${p === activeProf ? 'selected' : ''}>${esc(p)}</option>`).join('')
                : `<option value="${esc(activeProf)}">${esc(activeProf)}</option>`
            }
          </select>
          <button type="button" class="btn btn-sm btn-icon" id="dock-refresh-sub" title="${t('更新当前订阅 / 配置', 'Refresh active profile')}">${ICONS.refresh}</button>
        </div>
      </div>
    `;

    dockEl.querySelector('#dock-sys-btn')?.addEventListener('click', () => {
      rpc('set_system_proxy', { enabled: !sysOn });
    });
    dockEl.querySelector('#dock-tun-btn')?.addEventListener('click', () => {
      rpc('set_enhanced_mode', { enabled: !tunOn });
    });
    dockEl.querySelector('#dock-profile-select')?.addEventListener('change', async (e) => {
      const name = e.target.value;
      const res = await rpc('select_profile', { name });
      if (res.ok) showToast(t(`已切换配置: ${name}`, `Active profile: ${name}`), 'ok');
    });
    dockEl.querySelector('#dock-refresh-sub')?.addEventListener('click', async () => {
      const hasSub = (snap.subscriptions || []).some((s) => s.name === activeProf);
      const res = hasSub
        ? await rpc('refresh_subscription', { name: activeProf })
        : await rpc('reload_config', {});
      if (res.ok) showToast(hasSub ? t(`订阅「${activeProf}」已更新`, `Updated "${activeProf}"`) : t('配置已重载', 'Config reloaded'), 'ok');
    });
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
  if (tbTitle) tbTitle.textContent = t(pageDef.zh, pageDef.en);
  if (tbChip) {
    const tags = [];
    if (sysOn) tags.push(t('系统代理', 'System'));
    if (tunOn) tags.push('TUN');
    if (aiOn) tags.push('AI Lock');
    if (!store.connected) {
      tbChip.className = 'topbar-chip err';
      tbChip.textContent = t('核心守护未连接', 'Daemon Disconnected');
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

async function renderPageContent() {
  if (!pageRoot) return;
  const seq = ++renderSeq;
  const scrollBefore = pageRoot.scrollTop;
  pageRoot.classList.toggle('no-pad-scroll', store.page === 2);

  const handlers = { go };
  if (store.page === 0) {
    renderOverviewPage(pageRoot, handlers);
  } else if (store.page === 1) {
    renderSubscriptionsPage(pageRoot, handlers);
  } else if (store.page === 2) {
    await renderRulesPage(pageRoot, -1);
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
  if (!forcePage && isUserTypingInPage()) {
    pendingPageRender = true;
    return;
  }
  pendingPageRender = false;
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
      postNative({ type: 'window_control', action: 'toggle_maximize' });
    });
  });

  document.querySelectorAll('[data-resize]').forEach((edgeEl) => {
    edgeEl.addEventListener('mousedown', (e) => {
      if (e.button !== 0) return;
      postNative({ type: 'window_resize', edge: edgeEl.getAttribute('data-resize') });
    });
  });

  pageRoot?.addEventListener(
    'focusout',
    () => {
      if (pendingPageRender && !isUserTypingInPage()) {
        pendingPageRender = false;
        renderPageContent();
      }
    },
    true
  );
}

function wireIconsAndBootstrap() {
  document.getElementById('win-min').innerHTML = ICONS.minimize;
  document.getElementById('win-max').innerHTML = ICONS.maximize;
  document.getElementById('win-close').innerHTML = ICONS.close;
  wireWindowControls();
  initBridgeListener();

  subscribe((eventType) => {
    if (eventType === 'init' || eventType === 'navigate') {
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
    if (eventType === 'state' || eventType === 'language') {
      renderAll(false);
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
      if (store.page === 0) updateOverviewTelemetry();
      if (store.page === 5 && !isUserTypingInPage()) updateConnectionsTelemetry();
    }
  });

  window.addEventListener('error', (e) => {
    console.error('ClashFX UI Error:', e.message);
  });

  postNative({ type: 'ui_ready' });
}

wireIconsAndBootstrap();
