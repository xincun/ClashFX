import { ICONS, renderRegionBadge } from './icons.js';
import {
  applyTheme,
  esc,
  formatBytes,
  formatSpeed,
  mihomo,
  native,
  postNative,
  rpc,
  showConfirm,
  showToast,
  store,
  t,
  toggleLanguage,
} from './bridge.js';

const aiState = {
  search: '',
};

const multiNodeState = {
  showSettings: false,
};

const diagState = {
  subTab: 0,
  nodeName: '',
  benchmarkUrl: '',
  probing: false,
  probeReport: null,
  sysInfo: '',
  sysLoading: false,
  connSearch: '',
  connPaused: false,
};

const settingsState = {
  loadedHash: '',
  dirty: false,
  draft: null,
  updateResult: null,
  geoBusy: false,
  updBusy: false,
  recordingKey: '',
};

export function setDiagSubTab(subTab, nodeName = '') {
  if (subTab === 0 || subTab === 1) diagState.subTab = subTab;
  if (nodeName) diagState.nodeName = nodeName;
}

export function isDiagConnectionsActive() {
  return store.page === 5 && diagState.subTab === 1 && !diagState.connPaused;
}

function buildDelayLookup(snap) {
  const map = new Map();
  for (const g of snap.proxy_groups || []) {
    for (const it of g.items || []) {
      if (it.tested && !map.has(it.name)) {
        map.set(it.name, it.delay_ms);
      }
    }
  }
  return map;
}

export function renderAiLockPage(root, handlers) {
  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  const enabled = Boolean(settings.claudeProxyLockEnabled);
  const target = settings.claudeProxyLockTarget || snap.ai_lock_relay || '';
  const candidates = Array.isArray(snap.ai_lock_candidates) ? snap.ai_lock_candidates : [];
  const delayMap = buildDelayLookup(snap);
  const q = aiState.search.trim().toLowerCase();

  const candidateOptions = Array.from(new Set([target, ...candidates.map((c) => c.name)].filter(Boolean)));
  const filtered = candidates.filter(
    (c) => !q || `${c.name} ${c.type} ${c.server} ${c.provider}`.toLowerCase().includes(q)
  );

  const candidateRows = filtered.length
    ? filtered
        .map((c) => {
          const isSelected = c.name === target;
          const ms = delayMap.get(c.name);
          const msPill =
            ms !== undefined
              ? `<span class="delay-pill ${ms > 0 ? (ms < 160 ? 'fast' : 'med') : 'timeout'}">${ms > 0 ? `${ms} ms` : t('超时', 'Timeout')}</span>`
              : `<span class="pill">--</span>`;
          const statusBadge = isSelected
            ? enabled
              ? `<span class="pill pill-green">${t('当前已锁定', 'Locked')}</span>`
              : `<span class="pill pill-blue">${t('已选目标', 'Selected')}</span>`
            : '';
          const pickBtnClass = isSelected && enabled ? 'btn-success' : 'btn-primary';
          const pickLabel = isSelected
            ? enabled
              ? t('已锁定', 'Locked')
              : t('锁定此节点', 'Lock Here')
            : enabled
            ? t('切换至此', 'Use Here')
            : t('设为 AI 节点', 'Lock Node');
          return `
            <tr class="${isSelected ? 'row-selected' : ''}">
              <td>
                <div class="row-flex" style="gap:7px;">
                  ${renderRegionBadge(c.name)}
                  <strong style="font-size:12.5px;">${esc(c.name)}</strong>
                  ${statusBadge}
                </div>
              </td>
              <td><span class="pill">${esc(c.type || 'Proxy')}</span></td>
              <td class="mono" style="color:var(--text-secondary);">${esc(c.server || '—')}</td>
              <td class="metric-sub">${esc(c.provider || t('内置配置', 'Inline'))}</td>
              <td>${msPill}</td>
              <td style="text-align:right;white-space:nowrap;">
                <button type="button" class="btn btn-xs ${pickBtnClass}" data-ai-pick="${esc(c.name)}">
                  ${pickLabel}
                </button>
                <button type="button" class="btn btn-xs" data-ai-diag="${esc(c.name)}">${t('诊断', 'Diagnose')}</button>
              </td>
            </tr>`;
        })
        .join('')
    : `<tr><td colspan="6" class="empty-state">${t('暂无可用 AI 中继候选节点（请确认当前配置包含外部独立节点）', 'No eligible AI relay candidates in current profile')}</td></tr>`;

  root.innerHTML = `
    <div class="card">
      <div class="card-head">
        <div class="row-flex">
          <div class="card-title">${ICONS.aiLock} ${t('AI Coding 专线锁定 (AI Lock)', 'AI Coding Dedicated Lock')}</div>
          <span class="pill ${enabled ? 'pill-green' : ''}">${enabled ? t('已锁定保护', 'LOCKED') : t('未开启', 'UNLOCKED')}</span>
        </div>
        <div class="row-flex">
          <select class="select select-sm" id="ai-target-select" style="width:230px;">
            ${
              candidateOptions.length
                ? candidateOptions.map((n) => `<option value="${esc(n)}" ${n === target ? 'selected' : ''}>${esc(n)}</option>`).join('')
                : `<option value="">${t('无候选节点', 'No candidates')}</option>`
            }
          </select>
          <button type="button" class="btn btn-sm" id="ai-diag-current" ${!target ? 'disabled' : ''}>${t('探测目标链路', 'Probe Relay')}</button>
          <button type="button" class="btn btn-sm ${enabled ? 'btn-danger' : 'btn-primary'}" id="ai-toggle-btn">
            ${enabled ? t('解除 AI 锁定', 'Disable Lock') : t('开启 AI 锁定', 'Enable AI Lock')}
          </button>
        </div>
      </div>

      <div class="pipeline-flow">
        <div class="pipe-node">
          <div class="card-kicker">${t('AI 客户端 / CLI', 'AI Clients')}</div>
          <div style="font-weight:700;margin-top:2px;">Claude · OpenAI · Gemini · Cursor</div>
          <div class="metric-sub" style="margin-top:2px;">${t('命中 clashfx-ai-lock 顶层规则集', 'Matched by clashfx-ai-lock RuleSet')}</div>
        </div>
        <div class="pipe-arrow">→</div>
        <div class="pipe-node">
          <div class="card-kicker">${t('隔离通道', 'Routing Engine')}</div>
          <div style="font-weight:700;margin-top:2px;">${t('固定直连中继策略组', 'Dedicated Relay Policy')}</div>
          <div class="metric-sub" style="margin-top:2px;">${t('不受普通节点切换与自动测速漂移影响', 'Immune to manual group swaps & URLTest drift')}</div>
        </div>
        <div class="pipe-arrow">→</div>
        <div class="pipe-node ${enabled ? 'locked' : ''}">
          <div class="card-kicker">${t('锁定出口节点', 'Pinned Exit Node')}</div>
          <div style="font-weight:700;margin-top:2px;color:${enabled ? 'var(--green)' : 'var(--text-primary)'};">
            ${enabled ? esc(target || 'Active') : t('未开启 (跟随通用代理)', 'Inactive (follows standard routing)')}
          </div>
          <div class="metric-sub" style="margin-top:2px;">${t('可在规则页热更新 .list 域名列表', 'Customize domains live in Rules → .list Studio')}</div>
        </div>
      </div>
    </div>

    <div class="row-flex" style="justify-content:space-between;">
      <div class="search-box">
        ${ICONS.search}
        <input type="text" class="input" id="ai-cand-search" placeholder="${t('筛选候选节点 (名称 / 服务器 / 协议)…', 'Filter candidate nodes…')}" value="${esc(aiState.search)}" />
      </div>
      <div class="row-flex">
        <span class="pill">${t(`共 ${candidates.length} 个独立节点`, `${candidates.length} candidates`)}</span>
        <button type="button" class="btn btn-sm" id="ai-edit-rules">${ICONS.rules} ${t('编辑 clashfx-ai-lock 域名表', 'Edit AI Lock Domains (.list)')}</button>
      </div>
    </div>

    <div class="table-wrap" style="flex:1;min-height:200px;">
      <table class="data-table">
        <thead>
          <tr>
            <th>${t('候选节点', 'Candidate Node')}</th>
            <th>${t('协议', 'Protocol')}</th>
            <th>${t('服务器地址', 'Server')}</th>
            <th>${t('来源', 'Provider')}</th>
            <th>${t('延迟', 'Latency')}</th>
            <th style="text-align:right;">${t('操作', 'Actions')}</th>
          </tr>
        </thead>
        <tbody>${candidateRows}</tbody>
      </table>
    </div>
  `;

  root.querySelector('#ai-cand-search')?.addEventListener('input', (e) => {
    aiState.search = e.target.value;
    const caret = e.target.selectionStart;
    renderAiLockPage(root, handlers);
    const next = root.querySelector('#ai-cand-search');
    if (next) {
      next.focus();
      next.setSelectionRange(caret, caret);
    }
  });

  root.querySelector('#ai-edit-rules')?.addEventListener('click', () => handlers.go(2, 2));
  root.querySelector('#ai-diag-current')?.addEventListener('click', () => {
    const sel = root.querySelector('#ai-target-select')?.value || target;
    if (sel) handlers.go(5, 0, sel);
  });

  root.querySelector('#ai-toggle-btn')?.addEventListener('click', async () => {
    const nextEnabled = !enabled;
    const picked = root.querySelector('#ai-target-select')?.value || target || candidates[0]?.name || '';
    if (nextEnabled && !picked) {
      showToast(t('请先选择一个候选节点', 'Select a candidate node first'), 'warn');
      return;
    }
    const res = await rpc('set_ai_lock', { enabled: nextEnabled, target: picked });
    if (res.ok) {
      showToast(nextEnabled ? t(`AI 已锁定至 ${picked}`, `AI locked to ${picked}`) : t('AI 锁定已关闭', 'AI Lock disabled'), 'ok');
    }
  });

  root.querySelector('#ai-target-select')?.addEventListener('change', async (e) => {
    if (!enabled) return;
    const nextTarget = e.target.value;
    const res = await rpc('set_ai_lock', { enabled: true, target: nextTarget });
    if (res.ok) showToast(t(`AI 锁定目标已切换为 ${nextTarget}`, `AI relay switched to ${nextTarget}`), 'ok');
  });

  root.querySelectorAll('[data-ai-pick]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const node = btn.getAttribute('data-ai-pick');
      const res = await rpc('set_ai_lock', { enabled: true, target: node });
      if (res.ok) showToast(t(`AI 锁定已开启 -> ${node}`, `AI Lock active -> ${node}`), 'ok');
    });
  });

  root.querySelectorAll('[data-ai-diag]').forEach((btn) => {
    btn.addEventListener('click', () => handlers.go(5, 0, btn.getAttribute('data-ai-diag')));
  });
}

function renderHealthPill(health = {}) {
  const kind = health.kind || 'unchecked';
  if (kind === 'healthy') {
    const ms = health.delay_ms || 0;
    return `<span class="pill pill-green">${t('连通', 'Healthy')}${ms > 0 ? ` · ${ms} ms` : ''}</span>`;
  }
  if (kind === 'failed' || kind === 'unavailable') {
    const fails = health.consecutive_failures || 0;
    return `<span class="pill pill-red">${t('异常', 'Failed')}${fails > 0 ? ` (${fails})` : ''}</span>`;
  }
  return `<span class="pill">${t('未检测', 'Unchecked')}</span>`;
}

function renderSpeedPill(speed = {}) {
  const kind = speed.kind || 'idle';
  if (kind === 'measured') {
    return `<span class="pill pill-blue">${Number(speed.mbps || 0).toFixed(2)} MB/s</span>`;
  }
  if (kind === 'measuring') {
    return `<span class="pill pill-amber">${t('测速中…', 'Measuring…')}</span>`;
  }
  if (kind === 'waiting') {
    return `<span class="pill">${t('排队中', 'Queued')}</span>`;
  }
  if (kind === 'failed') {
    return `<span class="pill pill-red">${t('测速失败', 'Failed')}</span>`;
  }
  if (kind === 'cancelled') {
    return `<span class="pill">${t('已取消', 'Cancelled')}</span>`;
  }
  return `<span class="metric-sub">—</span>`;
}

export function renderMultiNodePage(root, handlers) {
  const ns = store.snapshot?.node_services || {};
  const nsSet = ns.settings || {};
  const rows = Array.isArray(ns.rows) ? ns.rows : [];
  const portsOn = Boolean(nsSet.ports_enabled);

  const tableRows = rows.length
    ? rows
        .map(
          (r) => `
        <tr class="${!r.enabled ? 'row-disabled' : ''}">
          <td style="width:52px;text-align:center;">
            <label class="switch" style="transform:scale(0.84);">
              <input type="checkbox" data-ns-enable="${esc(r.name)}" ${r.enabled ? 'checked' : ''} />
              <span class="switch-track"></span>
            </label>
          </td>
          <td>
            <div class="row-flex" style="gap:7px;">
              ${renderRegionBadge(r.name)}
              <strong>${esc(r.name)}</strong>
            </div>
          </td>
          <td class="mono" style="width:120px;">
            <button type="button" class="btn btn-xs mono" data-ns-copy-port="${r.port}" title="${t('复制本地代理地址', 'Copy local proxy URL')}">:${r.port}</button>
          </td>
          <td>${renderHealthPill(r.health)}</td>
          <td>${renderSpeedPill(r.speed)}</td>
          <td style="text-align:right;white-space:nowrap;">
            <button type="button" class="btn btn-xs" data-ns-chrome="${r.port}" ${!portsOn || !r.enabled ? 'disabled' : ''}>${ICONS.globe} Chrome</button>
            <button type="button" class="btn btn-xs" data-ns-diag="${esc(r.name)}">${t('诊断', 'Diagnose')}</button>
          </td>
        </tr>`
        )
        .join('')
    : `<tr><td colspan="6" class="empty-state">${t('暂无多节点端口映射行（请启用多节点服务或导入包含多节点的订阅）', 'No multi-node entries yet. Enable Multi-Node Ports or import a subscription.')}</td></tr>`;

  root.innerHTML = `
    <div class="bento-grid-3">
      <div class="card card-tight row-flex" style="justify-content:space-between;">
        <div>
          <div class="card-kicker">${t('多节点独立端口', 'Multi-Node Fixed Ports')}</div>
          <div style="font-weight:700;font-size:15px;margin-top:4px;">${portsOn ? t('服务运行中', 'Active') : t('已暂停分配', 'Paused')}</div>
          <div class="metric-sub">${t('起始端口', 'First Port')}: <span class="mono">:${nsSet.first_port || 10100}</span> · ${rows.length} ${t('节点', 'nodes')}</div>
        </div>
        <label class="switch">
          <input type="checkbox" id="ns-ports-enabled" ${portsOn ? 'checked' : ''} />
          <span class="switch-track"></span>
        </label>
      </div>

      <div class="card card-tight row-flex" style="justify-content:space-between;">
        <div>
          <div class="card-kicker">${t('本地直连端口 (DIRECT)', 'Direct Passthrough Port')}</div>
          <div style="font-weight:700;font-size:15px;margin-top:4px;" class="mono">${ns.direct_port > 0 ? `127.0.0.1:${ns.direct_port}` : t('未分配', 'Disabled')}</div>
          <div class="metric-sub">${esc(ns.status_text || t('就绪', 'Ready'))}</div>
        </div>
        <div class="row-flex">
          ${
            ns.direct_port > 0
              ? `<button type="button" class="btn btn-xs" data-ns-chrome="${ns.direct_port}" title="Launch Direct Chrome">${ICONS.globe}</button>`
              : ''
          }
          <button type="button" class="btn btn-xs ${ns.is_refreshing ? 'spin' : ''}" id="ns-refresh-direct" ${ns.is_refreshing ? 'disabled' : ''}>${ICONS.refresh}</button>
        </div>
      </div>

      <div class="card card-tight" style="display:flex;flex-direction:column;justify-content:space-between;gap:8px;">
        <div class="card-kicker">${t('批量检测与测速', 'Bulk Health & 20MB Speed')}</div>
        <div class="row-flex">
          <button type="button" class="btn btn-sm ${ns.is_checking ? 'spin' : ''}" id="ns-check-health" style="flex:1;" ${ns.is_checking ? 'disabled' : ''}>
            ${ICONS.bolt} ${ns.is_checking ? t('检测中…', 'Checking…') : t('204 连通探测', 'Check Health')}
          </button>
          ${
            ns.is_speed_testing
              ? `<button type="button" class="btn btn-sm btn-danger" id="ns-stop-speed" style="flex:1;">${ICONS.stop} ${t('停止测速', 'Stop Speed')}</button>`
              : `<button type="button" class="btn btn-sm btn-primary" id="ns-start-speed" style="flex:1;">${ICONS.play} ${t('20MB 吞吐测速', '20MB Speed Test')}</button>`
          }
        </div>
      </div>
    </div>

    <div class="row-flex" style="justify-content:space-between;">
      <span class="metric-sub">${t('为爬虫、指纹浏览器或并发工具固定绑定每个节点独立的 HTTP/SOCKS 本地端口。', 'Pin deterministic local HTTP/SOCKS ports per proxy node for isolated Chrome profiles or CLI tools.')}</span>
      <button type="button" class="btn btn-sm" id="ns-toggle-settings">${ICONS.settings} ${multiNodeState.showSettings ? t('收起端口参数', 'Hide Parameters') : t('端口与巡检参数', 'Port & Probe Settings')}</button>
    </div>

    ${
      multiNodeState.showSettings
        ? `<div class="card card-tight">
             <div style="display:grid;grid-template-columns:repeat(5, minmax(0, 1fr)) auto;gap:8px;align-items:end;">
               <div><div class="metric-sub">${t('起始端口', 'First Port')}</div><input type="number" class="input input-sm mono" id="ns-set-port" value="${nsSet.first_port || 10100}" /></div>
               <div><div class="metric-sub">${t('巡检间隔 (秒)', 'Interval (s)')}</div><input type="number" class="input input-sm mono" id="ns-set-interval" value="${nsSet.check_interval_sec || 60}" /></div>
               <div><div class="metric-sub">${t('连续失败阈值', 'Failure Threshold')}</div><input type="number" class="input input-sm mono" id="ns-set-fail" value="${nsSet.failure_threshold || 3}" /></div>
               <div><div class="metric-sub">${t('刷新冷却 (秒)', 'Cooldown (s)')}</div><input type="number" class="input input-sm mono" id="ns-set-cool" value="${nsSet.refresh_cooldown_sec || 60}" /></div>
               <label class="row-flex" style="cursor:pointer;height:27px;"><input type="checkbox" id="ns-set-rec" ${nsSet.recovery_enabled ? 'checked' : ''} /> <span style="font-size:12px;">${t('自动恢复', 'Auto Recovery')}</span></label>
               <button type="button" class="btn btn-sm btn-primary" id="ns-save-settings">${t('应用参数', 'Apply')}</button>
             </div>
           </div>`
        : ''
    }

    <div class="table-wrap" style="flex:1;min-height:200px;">
      <table class="data-table">
        <thead>
          <tr>
            <th style="text-align:center;">${t('启用', 'On')}</th>
            <th>${t('节点名称', 'Node Name')}</th>
            <th>${t('本地端口', 'Local Port')}</th>
            <th>${t('204 连通性', '204 Health')}</th>
            <th>${t('20MB 下载速率', 'Speed (20MB)')}</th>
            <th style="text-align:right;">${t('浏览器 / 诊断', 'Actions')}</th>
          </tr>
        </thead>
        <tbody>${tableRows}</tbody>
      </table>
    </div>
  `;

  root.querySelector('#ns-ports-enabled')?.addEventListener('change', async (e) => {
    const res = await rpc('update_node_services', { settings: { ...nsSet, ports_enabled: e.target.checked } });
    if (res.ok) showToast(e.target.checked ? t('多节点端口已启用', 'Multi-node ports enabled') : t('多节点端口已关闭', 'Multi-node ports paused'), 'ok');
  });

  root.querySelector('#ns-refresh-direct')?.addEventListener('click', () => rpc('node_refresh_direct', {}));
  root.querySelector('#ns-check-health')?.addEventListener('click', () => rpc('node_check_connectivity', {}));
  root.querySelector('#ns-start-speed')?.addEventListener('click', () => rpc('node_speed_test_start', {}));
  root.querySelector('#ns-stop-speed')?.addEventListener('click', () => rpc('node_speed_test_cancel', {}));
  root.querySelector('#ns-toggle-settings')?.addEventListener('click', () => {
    multiNodeState.showSettings = !multiNodeState.showSettings;
    renderMultiNodePage(root, handlers);
  });

  root.querySelector('#ns-save-settings')?.addEventListener('click', async () => {
    const nextSettings = {
      ...nsSet,
      first_port: Number(root.querySelector('#ns-set-port')?.value) || 10100,
      check_interval_sec: Number(root.querySelector('#ns-set-interval')?.value) || 60,
      failure_threshold: Number(root.querySelector('#ns-set-fail')?.value) || 3,
      refresh_cooldown_sec: Number(root.querySelector('#ns-set-cool')?.value) || 60,
      recovery_enabled: Boolean(root.querySelector('#ns-set-rec')?.checked),
    };
    const res = await rpc('update_node_services', { settings: nextSettings });
    if (res.ok) showToast(t('多节点服务参数已保存', 'Multi-node settings saved'), 'ok');
  });

  root.querySelectorAll('[data-ns-enable]').forEach((cb) => {
    cb.addEventListener('change', async (e) => {
      const name = cb.getAttribute('data-ns-enable');
      await rpc('update_node_services', { enabled: { [name]: e.target.checked } });
    });
  });

  root.querySelectorAll('[data-ns-copy-port]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const port = btn.getAttribute('data-ns-copy-port');
      await native('copy_text', { text: `http://127.0.0.1:${port}` });
      showToast(t(`已复制代理地址 http://127.0.0.1:${port}`, `Copied http://127.0.0.1:${port}`), 'ok');
    });
  });

  root.querySelectorAll('[data-ns-chrome]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const port = Number(btn.getAttribute('data-ns-chrome'));
      const res = await native('launch_chrome_proxy', { port });
      if (res.ok) showToast(t(`已启动独立代理浏览器 (:${port})`, `Launched isolated browser on :${port}`), 'ok');
      else showToast(res.error || t('启动浏览器失败', 'Browser launch failed'), 'err');
    });
  });

  root.querySelectorAll('[data-ns-diag]').forEach((btn) => {
    btn.addEventListener('click', () => handlers.go(5, 0, btn.getAttribute('data-ns-diag')));
  });
}

function collectAllNodeNames(snap) {
  const set = new Set();
  for (const c of snap.ai_lock_candidates || []) {
    if (c.name) set.add(c.name);
  }
  for (const g of snap.proxy_groups || []) {
    for (const it of g.items || []) {
      if (it.name && it.name !== 'DIRECT' && it.name !== 'REJECT') set.add(it.name);
    }
  }
  return Array.from(set);
}

export function updateConnectionsTelemetry() {
  if (store.page !== 5 || diagState.subTab !== 1 || diagState.connPaused) return;
  const tbody = document.getElementById('diag-conn-tbody');
  const sumEl = document.getElementById('diag-conn-summary');
  if (!tbody) return;

  const conns = Array.isArray(store.telemetry.connections) ? store.telemetry.connections : [];
  const q = diagState.connSearch.trim().toLowerCase();
  const filtered = conns.filter((c) => {
    if (!q) return true;
    const host = c.metadata?.host || c.metadata?.destinationIP || '';
    const proc = c.metadata?.process || '';
    const chains = (c.chains || []).join(' ');
    return `${host} ${proc} ${c.rule || ''} ${chains}`.toLowerCase().includes(q);
  });

  if (sumEl) {
    sumEl.textContent = `${filtered.length} / ${conns.length} ${t('条活跃连接', 'active')} · ↑ ${formatBytes(store.telemetry.upTotal)} · ↓ ${formatBytes(store.telemetry.downTotal)}`;
  }

  tbody.innerHTML = filtered.length
    ? filtered
        .slice(0, 150)
        .map((c) => {
          const md = c.metadata || {};
          const host = md.host || `${md.destinationIP || ''}:${md.destinationPort || ''}`;
          const proc = md.process || '—';
          const chain = Array.isArray(c.chains) ? [...c.chains].reverse().join(' → ') : '—';
          return `
            <tr>
              <td class="mono" style="max-width:250px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${esc(host)}">${esc(host)}</td>
              <td class="metric-sub">${esc(proc)} · ${esc(md.network || 'tcp')}</td>
              <td class="mono metric-sub" style="max-width:170px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${esc(c.rule || 'Match')}</td>
              <td class="mono" style="max-width:190px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--accent);">${esc(chain)}</td>
              <td class="mono metric-sub" style="text-align:right;white-space:nowrap;">↑${formatBytes(c.upload)} / ↓${formatBytes(c.download)}</td>
              <td style="text-align:right;width:44px;">
                <button type="button" class="btn btn-xs btn-danger" data-close-conn="${esc(c.id)}" title="${t('断开此连接', 'Close connection')}">×</button>
              </td>
            </tr>`;
        })
        .join('')
    : `<tr><td colspan="6" class="empty-state">${t('当前无匹配活跃连接', 'No active connections')}</td></tr>`;

  tbody.querySelectorAll('[data-close-conn]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const id = btn.getAttribute('data-close-conn');
      await mihomo('DELETE', `/connections/${encodeURIComponent(id)}`);
    });
  });
}

async function fetchDiagnosticBundle(root, { silent = false } = {}) {
  if (diagState.sysLoading) return;
  diagState.sysLoading = true;
  if (!silent) renderDiagnosticsPage(root);
  const res = await rpc('get_diagnostic_info', {}, { silent });
  diagState.sysLoading = false;
  if (res.ok && res.data !== undefined) {
    diagState.sysInfo =
      typeof res.data === 'string'
        ? res.data
        : typeof res.data?.report === 'string'
        ? res.data.report
        : JSON.stringify(res.data, null, 2);
  }
  if (store.page === 5 && diagState.subTab === 0) {
    renderDiagnosticsPage(root);
  }
}

export function renderDiagnosticsPage(root) {
  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  const nodes = collectAllNodeNames(snap);
  if (!diagState.nodeName && nodes.length) diagState.nodeName = nodes[0];
  if (!diagState.benchmarkUrl) diagState.benchmarkUrl = settings.benchmark_url || 'https://cp.cloudflare.com/generate_204';

  postNative({
    type: 'telemetry_subscribe',
    enabled: true,
    include_connections: diagState.subTab === 1 && !diagState.connPaused,
  });

  const rep = diagState.probeReport;
  const verdictHtml = rep
    ? `<div class="card">
         <div class="row-flex" style="justify-content:space-between;margin-bottom:8px;">
           <div class="row-flex">
             <span class="pill ${rep.healthy ? 'pill-green' : 'pill-red'}">${rep.healthy ? t('节点健康', 'Healthy') : t('探测异常', 'Probe Failed')}</span>
             <strong>${esc(rep.node_name)}</strong>
             ${rep.delay_ms > 0 ? `<span class="pill pill-blue">${rep.delay_ms} ms</span>` : ''}
             ${rep.code ? `<span class="pill">${esc(rep.code)}</span>` : ''}
           </div>
           <span class="metric-sub mono">${esc(rep.benchmark_url || '')}</span>
         </div>
         <div style="font-weight:600;margin-bottom:4px;">${esc(t(rep.summary_zh, rep.summary_en) || rep.raw_message || '')}</div>
         ${rep.advice_zh || rep.advice_en ? `<div class="metric-sub" style="margin-bottom:8px;">${esc(t(rep.advice_zh, rep.advice_en))}</div>` : ''}
         ${rep.formatted ? `<pre class="code-editor selectable" style="min-height:95px;max-height:170px;overflow:auto;">${esc(rep.formatted)}</pre>` : ''}
       </div>`
    : '';

  const bodyHtml =
    diagState.subTab === 0
      ? `<div class="card">
           <div class="card-head">
             <div class="card-title">${ICONS.diagnostics} ${t('单节点全链路深度探测', 'Deep Single-Node Probe')}</div>
             <div class="row-flex">
               <button type="button" class="btn btn-sm" id="diag-open-logs">${ICONS.folder} ${t('日志目录', 'Logs Folder')}</button>
               <button type="button" class="btn btn-sm" id="diag-fetch-sys">${diagState.sysLoading ? t('收集信息中…', 'Collecting…') : t('获取系统诊断报告', 'Daemon Report')}</button>
             </div>
           </div>
           <div style="display:grid;grid-template-columns:1.2fr 1.6fr auto;gap:8px;align-items:center;">
             <select class="select" id="diag-node-select">
               ${
                 nodes.length
                   ? nodes.map((n) => `<option value="${esc(n)}" ${n === diagState.nodeName ? 'selected' : ''}>${esc(n)}</option>`).join('')
                   : `<option value="">${t('暂无可诊断节点', 'No nodes available')}</option>`
               }
             </select>
             <input type="text" class="input mono" id="diag-url-input" value="${esc(diagState.benchmarkUrl)}" placeholder="https://cp.cloudflare.com/generate_204" />
             <button type="button" class="btn btn-primary" id="diag-run-btn" ${diagState.probing || !diagState.nodeName ? 'disabled' : ''}>
               ${ICONS.bolt} ${diagState.probing ? t('探测中…', 'Probing…') : t('开始探测', 'Run Probe')}
             </button>
           </div>
         </div>
         ${verdictHtml}
         ${
           diagState.sysInfo
             ? `<div class="card" style="display:flex;flex-direction:column;gap:8px;flex:1;">
                  <div class="row-flex" style="justify-content:space-between;">
                    <strong>${t('守护进程与网络环境报告', 'Daemon & Network Bundle')}</strong>
                    <button type="button" class="btn btn-xs" id="diag-copy-sys">${ICONS.copy} ${t('复制报告', 'Copy Report')}</button>
                  </div>
                  <pre class="code-editor selectable" style="flex:1;min-height:170px;overflow:auto;white-space:pre-wrap;word-break:break-word;">${esc(diagState.sysInfo)}</pre>
                </div>`
             : `<div class="card empty-state">${diagState.sysLoading ? t('正在自动收集守护进程与网络环境报告…', 'Loading daemon & network diagnostic bundle…') : t('选择任意节点点击「开始探测」可分析 DNS / TLS / 节点认证错误；点击右上角可获取完整守护进程环境报告。', 'Run a single-node probe above or fetch the full daemon diagnostic bundle.')}</div>`
         }`
      : `<div class="row-flex" style="justify-content:space-between;">
           <div class="search-box">
             ${ICONS.search}
             <input type="text" class="input" id="diag-conn-search" placeholder="${t('筛选连接 (域名 / 进程 / 策略)…', 'Filter connections (host / process / rule)…')}" value="${esc(diagState.connSearch)}" />
           </div>
           <div class="row-flex">
             <span class="pill" id="diag-conn-summary">--</span>
             <button type="button" class="btn btn-sm" id="diag-conn-pause">${diagState.connPaused ? t('恢复刷新', 'Resume') : t('暂停刷新', 'Pause')}</button>
             <button type="button" class="btn btn-sm btn-danger" id="diag-conn-clear">${t('断开全部连接', 'Close All')}</button>
           </div>
         </div>
         <div class="table-wrap" style="flex:1;min-height:240px;">
           <table class="data-table">
             <thead>
               <tr>
                 <th>${t('目标地址', 'Host')}</th>
                 <th>${t('进程 / 协议', 'Process / Net')}</th>
                 <th>${t('命中规则', 'Rule')}</th>
                 <th>${t('代理链路', 'Chain')}</th>
                 <th style="text-align:right;">${t('累计流量', 'Transfer')}</th>
                 <th></th>
               </tr>
             </thead>
             <tbody id="diag-conn-tbody"></tbody>
           </table>
         </div>`;

  root.innerHTML = `
    <div class="row-flex" style="justify-content:space-between;">
      <div class="subtabs">
        <button type="button" class="subtab-btn ${diagState.subTab === 0 ? 'active' : ''}" data-diag-tab="0">${t('节点排障与系统报告', 'Node Probe & Bundle')}</button>
        <button type="button" class="subtab-btn ${diagState.subTab === 1 ? 'active' : ''}" data-diag-tab="1">${t('实时活跃连接 (Connections)', 'Live Connections')}</button>
      </div>
      <span class="metric-sub mono">${t('网络环境:', 'Network:')} ${esc((snap.active_network?.profile_names || []).join(', ') || 'Default')}</span>
    </div>
    ${bodyHtml}
  `;

  root.querySelectorAll('[data-diag-tab]').forEach((btn) => {
    btn.addEventListener('click', () => {
      diagState.subTab = Number(btn.getAttribute('data-diag-tab'));
      renderDiagnosticsPage(root);
    });
  });

  if (diagState.subTab === 0) {
    root.querySelector('#diag-node-select')?.addEventListener('change', (e) => {
      diagState.nodeName = e.target.value;
    });
    root.querySelector('#diag-url-input')?.addEventListener('input', (e) => {
      diagState.benchmarkUrl = e.target.value;
    });
    root.querySelector('#diag-open-logs')?.addEventListener('click', () => {
      if (snap.logs_dir) native('open_path', { path: snap.logs_dir });
    });
    root.querySelector('#diag-run-btn')?.addEventListener('click', async () => {
      diagState.probing = true;
      renderDiagnosticsPage(root);
      const res = await rpc('diagnose_node', {
        node_name: diagState.nodeName,
        benchmark_url: diagState.benchmarkUrl.trim(),
      });
      diagState.probing = false;
      if (res.ok && res.data) {
        diagState.probeReport = res.data;
      }
      renderDiagnosticsPage(root);
    });
    root.querySelector('#diag-fetch-sys')?.addEventListener('click', () => {
      fetchDiagnosticBundle(root, { silent: false });
    });
    if (!diagState.sysInfo && !diagState.sysLoading && store.connected) {
      fetchDiagnosticBundle(root, { silent: true });
    }
    root.querySelector('#diag-copy-sys')?.addEventListener('click', async () => {
      await native('copy_text', { text: diagState.sysInfo });
      showToast(t('已复制诊断报告', 'Diagnostic bundle copied'), 'ok');
    });
  } else {
    updateConnectionsTelemetry();
    root.querySelector('#diag-conn-search')?.addEventListener('input', (e) => {
      diagState.connSearch = e.target.value;
      updateConnectionsTelemetry();
    });
    root.querySelector('#diag-conn-pause')?.addEventListener('click', () => {
      diagState.connPaused = !diagState.connPaused;
      renderDiagnosticsPage(root);
    });
    root.querySelector('#diag-conn-clear')?.addEventListener('click', async () => {
      await mihomo('DELETE', '/connections');
      showToast(t('已关闭全部活跃连接', 'Closed all active connections'), 'ok');
    });
  }
}

function formatKeyEventToChord(e) {
  const mods = [];
  if (e.ctrlKey) mods.push('Ctrl');
  if (e.altKey) mods.push('Alt');
  if (e.shiftKey) mods.push('Shift');
  if (e.metaKey) mods.push(store.platform === 'darwin' ? 'Cmd' : 'Win');
  const k = e.key;
  if (['Control', 'Alt', 'Shift', 'Meta'].includes(k)) return null;
  if (!mods.length) return null;
  let mainKey = k.toUpperCase();
  if (mainKey === ' ') mainKey = 'Space';
  return `${mods.join('+')}+${mainKey}`;
}

export function renderSettingsPage(root) {
  const snap = store.snapshot || {};
  const srcSettings = snap.settings || {};
  const hash = JSON.stringify(srcSettings);
  if (!settingsState.dirty && settingsState.loadedHash !== hash) {
    settingsState.loadedHash = hash;
    settingsState.draft = JSON.parse(hash || '{}');
    if (!settingsState.draft.hotkeys) settingsState.draft.hotkeys = {};
    if (!Array.isArray(settingsState.draft.suspend_networks)) settingsState.draft.suspend_networks = [];
  }
  const d = settingsState.draft || {};
  const hk = d.hotkeys || {};
  const activeNetNames = snap.active_network?.profile_names || [];

  root.innerHTML = `
    <div class="settings-grid">
      <div class="card">
        <div class="card-head"><div class="card-title">${ICONS.settings} ${t('外观与桌面集成', 'Appearance & Integration')}</div></div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('界面主题', 'Interface Theme')}</div><div class="form-desc">${t('Obsidian 深邃暗色 / Alabaster 清爽浅色', 'Switch between Obsidian Dark and Alabaster Light')}</div></div>
          <div class="form-control">
            <select class="select" id="set-theme">
              <option value="dark" ${store.theme === 'dark' ? 'selected' : ''}>Obsidian Dark</option>
              <option value="light" ${store.theme === 'light' ? 'selected' : ''}>Alabaster Light</option>
            </select>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('显示语言', 'Language')}</div></div>
          <div class="form-control">
            <select class="select" id="set-lang">
              <option value="zh_CN" ${d.language !== 'en_US' ? 'selected' : ''}>简体中文</option>
              <option value="en_US" ${d.language === 'en_US' ? 'selected' : ''}>English</option>
            </select>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('托盘图标风格', 'Tray Icon Style')}</div></div>
          <div class="form-control">
            <select class="select" id="set-tray">
              <option value="default" ${d.tray_icon_preset === 'default' ? 'selected' : ''}>${t('经典猫咪 (Default)', 'Default Cat')}</option>
              <option value="monochrome" ${d.tray_icon_preset === 'monochrome' ? 'selected' : ''}>${t('极简闪电 (Monochrome)', 'Monochrome Lightning')}</option>
              <option value="ocean" ${d.tray_icon_preset === 'ocean' ? 'selected' : ''}>${t('海洋护盾 (Ocean)', 'Ocean Shield')}</option>
            </select>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('开机自动启动', store.platform === 'darwin' ? 'Launch at Login' : 'Launch at Windows Login')}</div></div>
          <label class="switch"><input type="checkbox" data-set-bool="launch_at_login" ${d.launch_at_login ? 'checked' : ''} /><span class="switch-track"></span></label>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">${ICONS.bolt} ${t('内核与代理端口', 'Kernel & Proxy Ports')}</div></div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('混合代理端口 (Mixed Port)', 'Mixed Proxy Port')}</div><div class="form-desc">${t('HTTP + SOCKS5 共用监听端口', 'Shared HTTP & SOCKS5 listener')}</div></div>
          <div class="form-control"><input type="number" class="input mono" data-set-num="port_override" value="${d.port_override || 7890}" /></div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('控制面板 API 端口', 'Controller API Port')}</div></div>
          <div class="form-control"><input type="number" class="input mono" data-set-num="api_port" value="${d.api_port || 19090}" /></div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('允许局域网共享 (Allow LAN)', 'Allow LAN Connections')}</div></div>
          <label class="switch"><input type="checkbox" data-set-bool="allow_lan" ${d.allow_lan ? 'checked' : ''} /><span class="switch-track"></span></label>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('开启 IPv6 流量支持', 'Enable IPv6')}</div></div>
          <label class="switch"><input type="checkbox" data-set-bool="ipv6" ${d.ipv6 ? 'checked' : ''} /><span class="switch-track"></span></label>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('中国大陆桌面应用自动直连', 'Bypass China Desktop Apps')}</div></div>
          <label class="switch"><input type="checkbox" data-set-bool="bypass_cn_apps" ${d.bypass_cn_apps ? 'checked' : ''} /><span class="switch-track"></span></label>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('原样加载本地配置 (As-Is)', 'Load Custom Config As-Is')}</div></div>
          <label class="switch"><input type="checkbox" data-set-bool="custom_config_as_is" ${d.custom_config_as_is ? 'checked' : ''} /><span class="switch-track"></span></label>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">${ICONS.globe} ${t('TUN 虚拟网卡与系统旁路', 'TUN & System Bypass')}</div></div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('TUN 网络栈 (Stack)', 'TUN Stack')}</div></div>
          <div class="form-control">
            <select class="select" data-set-str="tun_stack">
              <option value="mixed" ${d.tun_stack === 'mixed' ? 'selected' : ''}>mixed</option>
              <option value="system" ${d.tun_stack === 'system' ? 'selected' : ''}>system</option>
              <option value="gvisor" ${d.tun_stack === 'gvisor' ? 'selected' : ''}>gvisor</option>
            </select>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('TUN 网卡名称 / MTU', 'Interface / MTU')}</div></div>
          <div class="form-control">
            <input type="text" class="input mono" placeholder="${store.platform === 'darwin' ? 'utun0 (auto)' : 'ClashFX-TUN'}" data-set-str="tun_interface_name" value="${esc(d.tun_interface_name || '')}" style="width:125px;" />
            <input type="number" class="input mono" data-set-num="tun_mtu" value="${d.tun_mtu || 1500}" style="width:78px;" />
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('内核日志级别', 'Core Log Level')}</div></div>
          <div class="form-control">
            <select class="select" data-set-str="log_level">
              ${['info', 'warning', 'error', 'debug', 'silent'].map((lvl) => `<option value="${lvl}" ${d.log_level === lvl ? 'selected' : ''}>${lvl}</option>`).join('')}
            </select>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('测速 Benchmark URL', 'Benchmark URL')}</div></div>
          <div class="form-control" style="width:240px;"><input type="text" class="input mono" data-set-str="benchmark_url" value="${esc(d.benchmark_url || '')}" /></div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('系统代理忽略清单 (Bypass List)', 'System Proxy Bypass')}</div></div>
          <div class="form-control" style="width:240px;"><input type="text" class="input mono" data-set-str="proxy_bypass_list" value="${esc(d.proxy_bypass_list || '')}" /></div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">${ICONS.aiLock} ${t('快捷键与环境挂起', 'Hotkeys & Auto-Suspend')}</div></div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('切换系统代理快捷键', 'Toggle System Proxy Hotkey')}</div><div class="form-desc">${t(`点击输入框后按下组合键 (如 ${store.platform === 'darwin' ? 'Cmd+Shift+P' : 'Ctrl+Alt+P'})`, `Click box and press key chord (e.g. ${store.platform === 'darwin' ? 'Cmd+Shift+P' : 'Ctrl+Alt+P'})`)}</div></div>
          <div class="form-control">
            <input type="text" readonly class="input mono hotkey-input ${settingsState.recordingKey === 'toggle_system_proxy' ? 'recording' : ''}" data-hk-rec="toggle_system_proxy" value="${esc(settingsState.recordingKey === 'toggle_system_proxy' ? t('请按键…', 'Press keys…') : hk.toggle_system_proxy || '')}" placeholder="${t('未设置', 'None')}" />
            <button type="button" class="btn btn-sm" data-hk-clear="toggle_system_proxy">×</button>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('切换出站模式快捷键', 'Switch Outbound Mode Hotkey')}</div></div>
          <div class="form-control">
            <input type="text" readonly class="input mono hotkey-input ${settingsState.recordingKey === 'switch_outbound_mode' ? 'recording' : ''}" data-hk-rec="switch_outbound_mode" value="${esc(settingsState.recordingKey === 'switch_outbound_mode' ? t('请按键…', 'Press keys…') : hk.switch_outbound_mode || '')}" placeholder="${t('未设置', 'None')}" />
            <button type="button" class="btn btn-sm" data-hk-clear="switch_outbound_mode">×</button>
          </div>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('自动更新订阅', 'Auto Update Subscriptions')}</div></div>
          <label class="switch"><input type="checkbox" data-set-bool="auto_update_subs" ${d.auto_update_subs ? 'checked' : ''} /><span class="switch-track"></span></label>
        </div>
        <div class="form-row">
          <div class="form-label-col"><div class="form-label">${t('特定 Wi-Fi / 网络下自动挂起', 'Suspend on Trusted Networks')}</div><div class="form-desc">${t('逗号分隔网络名', 'Comma-separated network names')}</div></div>
          <div class="form-control" style="width:240px;">
            <input type="text" class="input" id="set-suspend-nets" value="${esc((d.suspend_networks || []).join(', '))}" />
            ${activeNetNames.length ? `<button type="button" class="btn btn-sm" id="set-add-cur-net" title="${esc(activeNetNames[0])}">+当前</button>` : ''}
          </div>
        </div>
      </div>

      <div class="card" style="grid-column:1 / -1;">
        <div class="card-head">
          <div class="card-title">${ICONS.folder} ${t('目录路径、GEO 规则库与维护', 'Directories, GEO Databases & Maintenance')}</div>
          <div class="row-flex">
            <button type="button" class="btn btn-sm" id="set-flush-fakeip">${t('清空 Fake-IP 缓存', 'Flush Fake-IP')}</button>
            <button type="button" class="btn btn-sm" id="set-reload-cfg">${ICONS.refresh} ${t('重载核心配置', 'Reload Config')}</button>
            <button type="button" class="btn btn-sm" id="set-update-geo" ${settingsState.geoBusy ? 'disabled' : ''}>${settingsState.geoBusy ? t('下载中…', 'Updating…') : t('更新 GEO 数据库', 'Update GEO DB')}</button>
            <button type="button" class="btn btn-sm btn-primary" id="set-check-upd" ${settingsState.updBusy ? 'disabled' : ''}>${settingsState.updBusy ? t('检查中…', 'Checking…') : t('检查版本更新', 'Check Updates')}</button>
          </div>
        </div>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
          <div class="row-flex">
            <span class="metric-sub" style="width:92px;">${t('配置目录:', 'Config Dir:')}</span>
            <input type="text" class="input mono" id="set-cfg-dir" value="${esc(d.custom_config_dir || snap.config_dir || '')}" style="flex:1;" />
            <button type="button" class="btn btn-sm" id="set-pick-cfg">${t('选择', 'Browse')}</button>
            <button type="button" class="btn btn-sm" id="set-open-cfg">${ICONS.folder}</button>
          </div>
          <div class="row-flex">
            <span class="metric-sub" style="width:92px;">${t('更新通道:', 'Channel:')}</span>
            <select class="select" data-set-str="update_channel" style="width:130px;">
              <option value="stable" ${d.update_channel !== 'lab' ? 'selected' : ''}>Stable</option>
              <option value="lab" ${d.update_channel === 'lab' ? 'selected' : ''}>Lab Preview</option>
            </select>
            <span class="metric-sub" style="margin-left:auto;">${settingsState.updateResult ? esc(settingsState.updateResult) : `v${esc(snap.version || '--')}`}</span>
          </div>
        </div>
      </div>
    </div>

    ${
      settingsState.dirty
        ? `<div class="sticky-save-bar">
             <span>${t('您有尚未保存的偏好设置修改', 'You have unsaved preference changes')}</span>
             <div class="row-flex">
               <button type="button" class="btn btn-sm" id="set-discard">${t('放弃修改', 'Discard')}</button>
               <button type="button" class="btn btn-sm btn-primary" id="set-save">${ICONS.check} ${t('保存并应用设置', 'Save & Apply')}</button>
             </div>
           </div>`
        : ''
    }
  `;

  root.querySelector('#set-theme')?.addEventListener('change', (e) => {
    applyTheme(e.target.value);
    renderSettingsPage(root);
  });

  root.querySelector('#set-lang')?.addEventListener('change', async (e) => {
    d.language = e.target.value;
    if (d.language !== store.language) {
      await toggleLanguage();
    }
  });

  root.querySelector('#set-tray')?.addEventListener('change', (e) => {
    d.tray_icon_preset = e.target.value;
    settingsState.dirty = true;
    renderSettingsPage(root);
  });

  root.querySelectorAll('[data-set-bool]').forEach((cb) => {
    cb.addEventListener('change', (e) => {
      d[cb.getAttribute('data-set-bool')] = e.target.checked;
      settingsState.dirty = true;
      renderSettingsPage(root);
    });
  });

  root.querySelectorAll('[data-set-num]').forEach((inp) => {
    inp.addEventListener('input', (e) => {
      d[inp.getAttribute('data-set-num')] = Number(e.target.value) || 0;
      if (!settingsState.dirty) {
        settingsState.dirty = true;
        const bar = root.querySelector('.sticky-save-bar');
        if (!bar) renderSettingsPage(root);
      }
    });
  });

  root.querySelectorAll('[data-set-str]').forEach((el) => {
    const ev = el.tagName === 'INPUT' ? 'input' : 'change';
    el.addEventListener(ev, (e) => {
      d[el.getAttribute('data-set-str')] = e.target.value;
      if (!settingsState.dirty) {
        settingsState.dirty = true;
        if (el.tagName !== 'INPUT') renderSettingsPage(root);
      }
    });
  });

  root.querySelector('#set-suspend-nets')?.addEventListener('input', (e) => {
    d.suspend_networks = e.target.value
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    settingsState.dirty = true;
  });

  root.querySelector('#set-add-cur-net')?.addEventListener('click', () => {
    if (!activeNetNames.length) return;
    d.suspend_networks = Array.from(new Set([...(d.suspend_networks || []), activeNetNames[0]]));
    settingsState.dirty = true;
    renderSettingsPage(root);
  });

  root.querySelectorAll('[data-hk-rec]').forEach((inp) => {
    const field = inp.getAttribute('data-hk-rec');
    inp.addEventListener('click', () => {
      settingsState.recordingKey = field;
      renderSettingsPage(root);
      const ref = root.querySelector(`[data-hk-rec="${field}"]`);
      ref?.focus();
    });
    inp.addEventListener('keydown', (e) => {
      if (settingsState.recordingKey !== field) return;
      e.preventDefault();
      if (e.key === 'Escape') {
        settingsState.recordingKey = '';
        renderSettingsPage(root);
        return;
      }
      const chord = formatKeyEventToChord(e);
      if (chord) {
        if (!d.hotkeys) d.hotkeys = {};
        d.hotkeys[field] = chord;
        settingsState.recordingKey = '';
        settingsState.dirty = true;
        renderSettingsPage(root);
      }
    });
    inp.addEventListener('blur', () => {
      if (settingsState.recordingKey === field) {
        settingsState.recordingKey = '';
      }
    });
  });

  root.querySelectorAll('[data-hk-clear]').forEach((btn) => {
    btn.addEventListener('click', () => {
      if (!d.hotkeys) d.hotkeys = {};
      d.hotkeys[btn.getAttribute('data-hk-clear')] = '';
      settingsState.dirty = true;
      renderSettingsPage(root);
    });
  });

  root.querySelector('#set-cfg-dir')?.addEventListener('input', (e) => {
    d.custom_config_dir = e.target.value.trim();
    settingsState.dirty = true;
  });
  root.querySelector('#set-pick-cfg')?.addEventListener('click', async () => {
    const res = await native('pick_directory', {
      title: t('选择配置目录', 'Select Config Directory'),
      initial: d.custom_config_dir || snap.config_dir || '',
    });
    if (res.ok && res.data?.path) {
      d.custom_config_dir = res.data.path;
      settingsState.dirty = true;
      renderSettingsPage(root);
    }
  });
  root.querySelector('#set-open-cfg')?.addEventListener('click', () => {
    if (snap.config_dir) native('open_path', { path: snap.config_dir });
  });

  root.querySelector('#set-flush-fakeip')?.addEventListener('click', async () => {
    const res = await mihomo('POST', '/cache/fakeip/flush');
    showToast(res.ok ? t('已清理 Fake-IP 缓存', 'Fake-IP cache flushed') : t('清理失败', 'Flush failed'), res.ok ? 'ok' : 'err');
  });

  root.querySelector('#set-reload-cfg')?.addEventListener('click', async () => {
    const res = await rpc('reload_config', {});
    if (res.ok) showToast(t('核心配置已重载', 'Core configuration reloaded'), 'ok');
  });

  root.querySelector('#set-update-geo')?.addEventListener('click', async () => {
    settingsState.geoBusy = true;
    renderSettingsPage(root);
    const res = await rpc('update_geo_databases', {});
    settingsState.geoBusy = false;
    if (res.ok) showToast(t('GEO 数据库已更新', 'GEO databases updated'), 'ok');
    renderSettingsPage(root);
  });

  root.querySelector('#set-check-upd')?.addEventListener('click', async () => {
    settingsState.updBusy = true;
    renderSettingsPage(root);
    const res = await rpc('check_updates', {});
    settingsState.updBusy = false;
    if (res.ok && res.data) {
      settingsState.updateResult = res.data.update_available
        ? `${t('发现新版本', 'Update')}: ${res.data.latest_version}`
        : `${t('已是最新', 'Up to date')} (${res.data.current_version || snap.version})`;
    }
    renderSettingsPage(root);
  });

  root.querySelector('#set-discard')?.addEventListener('click', () => {
    settingsState.dirty = false;
    settingsState.loadedHash = '';
    renderSettingsPage(root);
  });

  root.querySelector('#set-save')?.addEventListener('click', async () => {
    const res = await rpc('update_settings', { settings: d });
    if (res.ok) {
      settingsState.dirty = false;
      settingsState.loadedHash = '';
      showToast(t('偏好设置已保存并生效', 'Preferences saved & applied'), 'ok');
      await rpc('get_state', {}, { silent: true });
      renderSettingsPage(root);
    }
  });
}
