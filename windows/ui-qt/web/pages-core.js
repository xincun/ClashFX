import { ICONS } from './icons.js';
import {
  esc,
  formatBytes,
  formatExpireDate,
  formatRelativeTime,
  native,
  rpc,
  showConfirm,
  showToast,
  store,
  t,
} from './bridge.js';

const RULE_TYPES = [
  'DOMAIN-SUFFIX',
  'DOMAIN',
  'DOMAIN-KEYWORD',
  'DOMAIN-REGEX',
  'DOMAIN-WILDCARD',
  'GEOSITE',
  'IP-CIDR',
  'IP-CIDR6',
  'SRC-IP-CIDR',
  'GEOIP',
  'IP-ASN',
  'DST-PORT',
  'SRC-PORT',
  'PROCESS-NAME',
  'PROCESS-PATH',
  'PROCESS-NAME-REGEX',
  'PROCESS-PATH-REGEX',
  'RULE-SET',
  'NETWORK',
  'MATCH',
];

const rulesState = {
  subTab: 0,
  loadedProfile: '',
  mixinDirty: false,
  mixinPath: '',
  baseYaml: '',
  yamlText: '',
  bypassCnWeb: false,
  bypassLan: false,
  rules: [],
  defaultTarget: 'Proxy',
  effectiveLoaded: false,
  effective: null,
  effSearch: '',
  effSource: 'all',
  selectedProvider: '',
  providerDraftText: '',
  providerDirty: false,
  editingRuleIdx: -1,
};

const subDraft = {
  url: '',
  name: '',
  ua: 'clash-verge/v2.0.0',
  busy: false,
  refreshingName: '',
};

export function setRulesSubTab(subTab) {
  if (subTab >= 0 && subTab <= 2) {
    rulesState.subTab = subTab;
  }
}

export function renderSubscriptionsPage(root) {
  const snap = store.snapshot || {};
  const settings = snap.settings || {};
  const subs = Array.isArray(snap.subscriptions) ? snap.subscriptions : [];
  const allProfiles = Array.isArray(snap.profiles) ? snap.profiles : [];
  const subNames = new Set(subs.map((s) => s.name));
  const localOnlyProfiles = allProfiles.filter((p) => !subNames.has(p));

  const subCardsHtml = subs
    .map((s) => {
      const isActive = settings.selected_profile === s.name;
      const ui = s.userinfo || {};
      const total = Number(ui.total) || 0;
      const used = (Number(ui.upload) || 0) + (Number(ui.download) || 0);
      const pct = total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0;
      const refreshing = subDraft.refreshingName === s.name;

      return `
        <div class="sub-card ${isActive ? 'is-active' : ''}">
          <div class="row-flex" style="justify-content:space-between;">
            <div class="row-flex" style="min-width:0;">
              <strong style="font-size:14px;">${esc(s.name)}</strong>
              ${isActive ? `<span class="pill pill-green">${t('使用中', 'Active')}</span>` : ''}
            </div>
            <span class="metric-sub">${t('更新:', 'Updated:')} ${esc(formatRelativeTime(s.updated_at))}</span>
          </div>
          <div class="sub-url selectable mono" title="${esc(s.url)}">${esc(s.url)}</div>
          <div>
            <div class="row-flex" style="justify-content:space-between;margin-bottom:5px;">
              <span class="metric-sub">${total > 0 ? `${formatBytes(used)} / ${formatBytes(total)} (${pct}%)` : t('流量信息由机场下发后显示', 'Traffic info pending sync')}</span>
              <span class="metric-sub">${t('到期:', 'Expires:')} ${esc(formatExpireDate(ui.expire, ui.expire_text))}</span>
            </div>
            <div class="progress-track">
              <div class="progress-fill ${pct > 85 ? 'warn' : ''}" style="width:${total > 0 ? pct : 0}%"></div>
            </div>
          </div>
          <div class="row-flex" style="justify-content:space-between;margin-top:2px;">
            <div class="row-flex">
              ${
                isActive
                  ? `<span class="pill pill-blue">${t('当前生效', 'Current')}</span>`
                  : `<button type="button" class="btn btn-sm btn-primary" data-sub-select="${esc(s.name)}">${t('设为当前', 'Use')}</button>`
              }
              <button type="button" class="btn btn-sm ${refreshing ? 'spin' : ''}" data-sub-refresh="${esc(s.name)}" ${refreshing ? 'disabled' : ''}>
                ${ICONS.refresh} ${refreshing ? t('更新中', 'Updating') : t('更新', 'Refresh')}
              </button>
            </div>
            <div class="row-flex">
              <button type="button" class="btn btn-sm btn-ghost" data-sub-copy="${esc(s.url)}" title="${t('复制链接', 'Copy URL')}">${ICONS.copy}</button>
              <button type="button" class="btn btn-sm btn-danger" data-sub-del="${esc(s.name)}" title="${t('移除订阅', 'Remove')}">${ICONS.trash}</button>
            </div>
          </div>
        </div>`;
    })
    .join('');

  const localCardsHtml = localOnlyProfiles
    .map((name) => {
      const isActive = settings.selected_profile === name;
      return `
        <div class="sub-card ${isActive ? 'is-active' : ''}">
          <div class="row-flex" style="justify-content:space-between;">
            <div class="row-flex">
              <strong style="font-size:14px;">${esc(name)}</strong>
              <span class="pill">${t('本地 YAML', 'Local YAML')}</span>
              ${isActive ? `<span class="pill pill-green">${t('使用中', 'Active')}</span>` : ''}
            </div>
            ${
              isActive
                ? `<span class="pill pill-blue">${t('当前生效', 'Current')}</span>`
                : `<button type="button" class="btn btn-sm btn-primary" data-sub-select="${esc(name)}">${t('切换使用', 'Activate')}</button>`
            }
          </div>
          <div class="metric-sub mono">${esc(snap.config_dir || '')}\\${esc(name)}.yaml</div>
        </div>`;
    })
    .join('');

  root.innerHTML = `
    <div class="card">
      <div class="card-head">
        <div class="card-title">${ICONS.plus} ${t('导入或添加订阅 / 配置', 'Import Subscription or Config Link')}</div>
        <div class="row-flex">
          <button type="button" class="btn btn-sm" id="sub-open-dir">${ICONS.folder} ${t('打开配置目录', 'Config Folder')}</button>
          <button type="button" class="btn btn-sm" id="sub-refresh-all">${ICONS.refresh} ${t('全部刷新', 'Refresh All')}</button>
        </div>
      </div>
      <div style="display:grid;grid-template-columns:2.4fr 1.1fr 1.1fr auto;gap:8px;align-items:center;">
        <input type="text" class="input" id="sub-url-input" placeholder="https://...  ${t('或', 'or')}  clash://install-config?url=..." value="${esc(subDraft.url)}" />
        <input type="text" class="input" id="sub-name-input" placeholder="${t('配置名称 (可选)', 'Profile name (optional)')}" value="${esc(subDraft.name)}" />
        <select class="select" id="sub-ua-select">
          <option value="clash-verge/v2.0.0" ${subDraft.ua === 'clash-verge/v2.0.0' ? 'selected' : ''}>UA: clash-verge</option>
          <option value="clash.meta" ${subDraft.ua === 'clash.meta' ? 'selected' : ''}>UA: clash.meta</option>
          <option value="ClashFX/1.0" ${subDraft.ua === 'ClashFX/1.0' ? 'selected' : ''}>UA: ClashFX</option>
        </select>
        <div class="row-flex">
          <button type="button" class="btn" id="sub-paste-btn">${t('剪贴板', 'Paste')}</button>
          <button type="button" class="btn btn-primary" id="sub-import-btn" ${subDraft.busy ? 'disabled' : ''}>
            ${subDraft.busy ? t('导入中…', 'Importing…') : t('导入并启用', 'Import & Use')}
          </button>
        </div>
      </div>
    </div>

    <div class="bento-grid-2">
      ${subCardsHtml}
      ${localCardsHtml}
    </div>
    ${!subs.length && !localOnlyProfiles.length ? `<div class="card empty-state">${t('暂无订阅或本地配置，请粘贴订阅链接导入。', 'No subscriptions or profiles found yet.')}</div>` : ''}
  `;

  root.querySelector('#sub-url-input')?.addEventListener('input', (e) => {
    subDraft.url = e.target.value;
  });
  root.querySelector('#sub-name-input')?.addEventListener('input', (e) => {
    subDraft.name = e.target.value;
  });
  root.querySelector('#sub-ua-select')?.addEventListener('change', (e) => {
    subDraft.ua = e.target.value;
  });

  root.querySelector('#sub-open-dir')?.addEventListener('click', () => {
    if (snap.config_dir) native('open_path', { path: snap.config_dir });
  });

  root.querySelector('#sub-paste-btn')?.addEventListener('click', async () => {
    const res = await native('read_clipboard');
    const txt = res.data?.text?.trim() || '';
    if (!txt) {
      showToast(t('剪贴板为空', 'Clipboard is empty'), 'warn');
      return;
    }
    subDraft.url = txt;
    const inp = root.querySelector('#sub-url-input');
    if (inp) inp.value = txt;
  });

  root.querySelector('#sub-import-btn')?.addEventListener('click', async () => {
    const url = subDraft.url.trim();
    if (!url) {
      showToast(t('请输入订阅链接', 'Please enter a subscription URL'), 'warn');
      return;
    }
    subDraft.busy = true;
    renderSubscriptionsPage(root);
    let res;
    if (/^(clash|clashfx|clash-verge):\/\//i.test(url)) {
      res = await rpc('import_url_scheme', { uri: url });
    } else {
      const name = subDraft.name.trim() || `sub-${Date.now().toString().slice(-4)}`;
      res = await rpc('upsert_subscription', {
        name,
        url,
        user_agent: subDraft.ua,
        select_after: true,
      });
    }
    subDraft.busy = false;
    if (res.ok) {
      subDraft.url = '';
      subDraft.name = '';
      showToast(t('订阅已导入并设为当前配置', 'Subscription imported and activated'), 'ok');
    }
    await rpc('get_state', {}, { silent: true });
    renderSubscriptionsPage(root);
  });

  root.querySelector('#sub-refresh-all')?.addEventListener('click', async () => {
    if (!subs.length) return;
    showToast(t(`正在刷新 ${subs.length} 个订阅…`, `Refreshing ${subs.length} subscriptions…`), 'info');
    for (const s of subs) {
      await rpc('refresh_subscription', { name: s.name });
    }
    await rpc('get_state', {}, { silent: true });
    showToast(t('全部订阅已更新', 'All subscriptions updated'), 'ok');
  });

  root.querySelectorAll('[data-sub-select]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const name = btn.getAttribute('data-sub-select');
      const res = await rpc('select_profile', { name });
      if (res.ok) showToast(t(`已切换到 ${name}`, `Switched to ${name}`), 'ok');
    });
  });

  root.querySelectorAll('[data-sub-refresh]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const name = btn.getAttribute('data-sub-refresh');
      subDraft.refreshingName = name;
      renderSubscriptionsPage(root);
      const res = await rpc('refresh_subscription', { name });
      subDraft.refreshingName = '';
      if (res.ok) showToast(t(`订阅「${name}」已刷新`, `Updated "${name}"`), 'ok');
      await rpc('get_state', {}, { silent: true });
      renderSubscriptionsPage(root);
    });
  });

  root.querySelectorAll('[data-sub-copy]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const url = btn.getAttribute('data-sub-copy');
      await native('copy_text', { text: url });
      showToast(t('已复制订阅链接', 'Subscription URL copied'), 'ok');
    });
  });

  root.querySelectorAll('[data-sub-del]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const name = btn.getAttribute('data-sub-del');
      const delFile = await showConfirm({
        title: t(`移除订阅「${name}」`, `Remove "${name}"`),
        message: t(
          '点击「删除」将移除该订阅记录及本地 YAML 配置文件；如仅想保留本地配置，请点击取消。',
          'Confirm to remove this subscription and delete its local YAML profile file.'
        ),
        confirmText: t('删除订阅及文件', 'Delete Subscription & File'),
        danger: true,
      });
      if (!delFile) return;
      const res = await rpc('remove_subscription', { name, delete_file: true });
      if (res.ok) showToast(t(`已移除 ${name}`, `Removed ${name}`), 'ok');
    });
  });
}

async function ensureRulesDataLoaded(force = false) {
  if (!store.connected) {
    rulesState.effectiveLoaded = false;
    return;
  }
  const activeProfile = store.snapshot?.settings?.selected_profile || '';
  if (force || (rulesState.loadedProfile !== activeProfile && !rulesState.mixinDirty)) {
    const res = await rpc('get_profile_mixin', {}, { silent: true });
    if (res.ok && res.data) {
      const d = res.data;
      rulesState.loadedProfile = activeProfile;
      rulesState.mixinPath = d.path || store.snapshot?.profile_mixin_path || '';
      rulesState.yamlText = d.yaml || '';
      rulesState.baseYaml = d.yaml || '';
      rulesState.bypassCnWeb = Boolean(d.bypass_cn_web);
      rulesState.bypassLan = Boolean(d.bypass_lan);
      rulesState.rules = Array.isArray(d.rules) ? d.rules.map((r) => ({ ...r })) : [];
      rulesState.defaultTarget = d.default_target || store.snapshot?.default_rule_target || 'Proxy';
      rulesState.mixinDirty = false;
    }
  }
  if (force || !rulesState.effectiveLoaded) {
    const eff = await rpc('get_effective_rules', {}, { silent: true });
    if (eff.ok && eff.data) {
      rulesState.effectiveLoaded = true;
      rulesState.effective = eff.data;
      const providers = Object.values(eff.data.rule_providers || {}).filter((p) => p && (p.editable || p.raw_text));
      if (providers.length && (!rulesState.selectedProvider || !eff.data.rule_providers?.[rulesState.selectedProvider])) {
        rulesState.selectedProvider = providers[0].name;
        rulesState.providerDraftText = providers[0].raw_text || '';
        rulesState.providerDirty = false;
      } else if (rulesState.selectedProvider && !rulesState.providerDirty) {
        const cur = eff.data.rule_providers?.[rulesState.selectedProvider];
        if (cur) rulesState.providerDraftText = cur.raw_text || '';
      }
    }
  }
}

function countListLines(rawText = '') {
  return String(rawText)
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith('#')).length;
}

function sourceBadge(source = '') {
  const s = String(source).toLowerCase();
  if (s === 'system') return `<span class="pill pill-green">${t('系统守护', 'System')}</span>`;
  if (s === 'ai_lock') return `<span class="pill pill-purple">${t('AI 锁定', 'AI Lock')}</span>`;
  if (s === 'cn_apps') return `<span class="pill pill-blue">${t('CN 直连', 'CN Direct')}</span>`;
  if (s === 'mixin') return `<span class="pill pill-amber">${t('Mixin 补丁', 'Mixin')}</span>`;
  return `<span class="pill">${t('订阅规则', 'Profile')}</span>`;
}

export async function renderRulesPage(root, initialSubTab = -1) {
  if (initialSubTab >= 0 && initialSubTab <= 2) {
    rulesState.subTab = initialSubTab;
  }
  await ensureRulesDataLoaded(false);

  const snap = store.snapshot || {};
  const groups = Array.isArray(snap.proxy_groups) ? snap.proxy_groups.map((g) => g.name) : [];
  const targets = Array.from(new Set(['DIRECT', 'REJECT', rulesState.defaultTarget, ...groups].filter(Boolean)));
  const eff = rulesState.effective || { total_rules: 0, rules: [], rule_providers: {} };
  const providers = Object.values(eff.rule_providers || {}).filter((p) => p && (p.editable || p.raw_text));
  const activeProv = providers.find((p) => p.name === rulesState.selectedProvider) || providers[0] || null;
  if (activeProv && !rulesState.selectedProvider) {
    rulesState.selectedProvider = activeProv.name;
    rulesState.providerDraftText = activeProv.raw_text || '';
  }

  const headerHtml = `
    <div class="row-flex" style="justify-content:space-between;">
      <div class="row-flex">
        <span class="pill pill-green">${t('切换订阅自动保留', 'Preserved Across Profiles')}</span>
        ${rulesState.mixinDirty || rulesState.providerDirty ? `<span class="pill pill-amber">${t('有未保存改动', 'Unsaved Changes')}</span>` : ''}
      </div>
      <div class="row-flex">
        <div class="subtabs">
          <button type="button" class="subtab-btn ${rulesState.subTab === 0 ? 'active' : ''}" data-rule-tab="0">${t('可视化分流', 'Visual Mixin')}</button>
          <button type="button" class="subtab-btn ${rulesState.subTab === 1 ? 'active' : ''}" data-rule-tab="1">${t('YAML 补丁源码', 'YAML Source')}</button>
          <button type="button" class="subtab-btn ${rulesState.subTab === 2 ? 'active' : ''}" data-rule-tab="2">${t('当前生效与热更新', 'Live & .list Studio')} (${eff.total_rules || 0})</button>
        </div>
        <button type="button" class="btn btn-sm btn-icon" id="rules-reload-btn" title="${t('重新加载', 'Refresh')}">${ICONS.refresh}</button>
      </div>
    </div>`;

  let bodyHtml = '';
  if (rulesState.subTab === 0) {
    const rowsHtml = rulesState.rules.length
      ? rulesState.rules
          .map(
            (r, idx) => `
          <tr>
            <td class="mono" style="width:42px;color:var(--text-muted);">${idx + 1}</td>
            <td style="width:160px;">
              <select class="select select-sm mono" data-rule-field="type" data-idx="${idx}">
                ${RULE_TYPES.map((rt) => `<option value="${rt}" ${r.type === rt ? 'selected' : ''}>${rt}</option>`).join('')}
              </select>
            </td>
            <td>
              <input type="text" class="input input-sm mono" data-rule-field="payload" data-idx="${idx}" value="${esc(r.payload || '')}" ${r.type === 'MATCH' ? 'disabled placeholder="(MATCH)"' : ''} />
            </td>
            <td style="width:170px;">
              <select class="select select-sm" data-rule-field="target" data-idx="${idx}">
                ${Array.from(new Set([...targets, r.target].filter(Boolean)))
                  .map((tg) => `<option value="${esc(tg)}" ${r.target === tg ? 'selected' : ''}>${esc(tg)}</option>`)
                  .join('')}
              </select>
            </td>
            <td style="width:128px;text-align:right;white-space:nowrap;">
              <button type="button" class="btn btn-xs" data-rule-move="-1" data-idx="${idx}" ${idx === 0 ? 'disabled' : ''}>↑</button>
              <button type="button" class="btn btn-xs" data-rule-move="1" data-idx="${idx}" ${idx === rulesState.rules.length - 1 ? 'disabled' : ''}>↓</button>
              <button type="button" class="btn btn-xs btn-danger" data-rule-del="${idx}">${ICONS.trash}</button>
            </td>
          </tr>`
          )
          .join('')
      : `<tr><td colspan="5" class="empty-state">${t('暂无顶层自定义规则，可使用上方添加栏或快捷直连开关。', 'No custom rules yet. Use the quick switches or add bar above.')}</td></tr>`;

    bodyHtml = `
      <div class="bento-grid-2">
        <div class="card card-tight row-flex" style="justify-content:space-between;">
          <div>
            <div class="form-label">${t('国内网站直连 (CN Web Direct)', 'Bypass China Web Traffic')}</div>
            <div class="form-desc">${t('在 Mixin 末尾注入 GEOSITE,cn 与 GEOIP,cn 直连兜底', 'Append GEOSITE,cn and GEOIP,cn -> DIRECT')}</div>
          </div>
          <label class="switch">
            <input type="checkbox" id="mixin-cn-web" ${rulesState.bypassCnWeb ? 'checked' : ''} />
            <span class="switch-track"></span>
          </label>
        </div>
        <div class="card card-tight row-flex" style="justify-content:space-between;">
          <div>
            <div class="form-label">${t('局域网及私有地址直连 (LAN Direct)', 'Bypass LAN & Private IP')}</div>
            <div class="form-desc">${t('优先直连 GEOSITE,private 与 GEOIP,lan', 'Route GEOSITE,private & GEOIP,lan -> DIRECT')}</div>
          </div>
          <label class="switch">
            <input type="checkbox" id="mixin-lan" ${rulesState.bypassLan ? 'checked' : ''} />
            <span class="switch-track"></span>
          </label>
        </div>
      </div>

      <div class="card card-tight">
        <div style="display:grid;grid-template-columns:180px 1fr 180px auto;gap:8px;align-items:center;">
          <select class="select mono" id="new-rule-type">
            ${RULE_TYPES.map((rt) => `<option value="${rt}">${rt}</option>`).join('')}
          </select>
          <input type="text" class="input mono" id="new-rule-payload" placeholder="openai.com / 192.168.0.0/16 / cursor.exe" />
          <select class="select" id="new-rule-target">
            ${targets.map((tg) => `<option value="${esc(tg)}">${esc(tg)}</option>`).join('')}
          </select>
          <button type="button" class="btn btn-primary" id="btn-add-rule">${ICONS.plus} ${t('添加顶层规则', 'Add Rule')}</button>
        </div>
      </div>

      <div class="table-wrap" style="flex:1;min-height:180px;">
        <table class="data-table">
          <thead>
            <tr>
              <th>#</th>
              <th>${t('规则类型', 'Type')}</th>
              <th>${t('匹配内容 (Payload)', 'Payload')}</th>
              <th>${t('目标策略', 'Target')}</th>
              <th style="text-align:right;">${t('顺序 / 操作', 'Actions')}</th>
            </tr>
          </thead>
          <tbody>${rowsHtml}</tbody>
        </table>
      </div>

      <div class="row-flex" style="justify-content:space-between;">
        <span class="metric-sub mono selectable">${esc(rulesState.mixinPath || snap.profile_mixin_path || '')}</span>
        <button type="button" class="btn btn-primary" id="btn-save-visual-mixin">${ICONS.check} ${t('保存并应用 Mixin 补丁', 'Save & Apply Mixin')}</button>
      </div>
    `;
  } else if (rulesState.subTab === 1) {
    bodyHtml = `
      <div class="row-flex" style="justify-content:space-between;">
        <span class="metric-sub mono selectable">${esc(rulesState.mixinPath || snap.profile_mixin_path || '')}</span>
        <div class="row-flex">
          <button type="button" class="btn btn-sm" id="btn-open-mixin-dir">${ICONS.folder} ${t('打开所在目录', 'Open Folder')}</button>
          <button type="button" class="btn btn-sm btn-primary" id="btn-save-yaml-mixin">${ICONS.check} ${t('保存并应用 YAML', 'Save & Apply YAML')}</button>
        </div>
      </div>
      <textarea class="code-editor" id="mixin-yaml-editor" spellcheck="false">${esc(rulesState.yamlText)}</textarea>
    `;
  } else {
    const allEffRules = Array.isArray(eff.rules) ? eff.rules : [];
    const q = rulesState.effSearch.trim().toLowerCase();
    const filtered = allEffRules.filter((r) => {
      if (rulesState.effSource !== 'all' && String(r.source).toLowerCase() !== rulesState.effSource) return false;
      if (!q) return true;
      return `${r.type} ${r.payload} ${r.proxy} ${r.source}`.toLowerCase().includes(q);
    });

    const effRowsHtml = filtered.length
      ? filtered
          .map((r) => {
            const isSetMatch =
              r.type === 'RuleSet' &&
              activeProv &&
              String(r.payload || '').toLowerCase() === activeProv.name.toLowerCase();
            return `
            <tr class="${r.disabled ? 'row-disabled' : ''} ${isSetMatch ? 'row-selected' : ''}" data-eff-row-payload="${esc(r.payload || '')}" data-eff-row-type="${esc(r.type || '')}">
              <td style="width:48px;text-align:center;">
                <input type="checkbox" data-rule-enable="${r.index}" ${r.disabled ? '' : 'checked'} />
              </td>
              <td style="width:104px;">${sourceBadge(r.source)}</td>
              <td class="mono" style="width:118px;font-weight:600;">${esc(r.type)}</td>
              <td class="mono selectable">${esc(r.payload || '—')}</td>
              <td class="mono" style="max-width:190px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--accent);">${esc(r.proxy)}</td>
              <td class="mono" style="width:90px;text-align:right;color:var(--text-secondary);">${r.hit_count ?? 0} / ${r.miss_count ?? 0}</td>
            </tr>`;
          })
          .join('')
      : `<tr><td colspan="6" class="empty-state">${t('无匹配的生效规则', 'No matching runtime rules')}</td></tr>`;

    const provRuleCount = activeProv ? activeProv.rule_count || countListLines(rulesState.providerDraftText) : 0;

    bodyHtml = `
      <div class="rules-split">
        <div class="rules-top-pane">
          <div class="row-flex">
            <div class="search-box" style="max-width:none;">
              ${ICONS.search}
              <input type="text" class="input input-sm" id="eff-search" placeholder="${t('搜索生效规则 (类型 / 域名 / 策略 / 来源)…', 'Search effective rules…')}" value="${esc(rulesState.effSearch)}" />
            </div>
            <select class="select select-sm" id="eff-source" style="width:160px;">
              <option value="all" ${rulesState.effSource === 'all' ? 'selected' : ''}>${t('全部规则来源', 'All Sources')}</option>
              <option value="system" ${rulesState.effSource === 'system' ? 'selected' : ''}>${t('系统守护', 'System')}</option>
              <option value="ai_lock" ${rulesState.effSource === 'ai_lock' ? 'selected' : ''}>${t('AI 锁定', 'AI Lock')}</option>
              <option value="cn_apps" ${rulesState.effSource === 'cn_apps' ? 'selected' : ''}>${t('CN 直连', 'CN Direct')}</option>
              <option value="mixin" ${rulesState.effSource === 'mixin' ? 'selected' : ''}>${t('Mixin 补丁', 'Mixin')}</option>
              <option value="subscription" ${rulesState.effSource === 'subscription' ? 'selected' : ''}>${t('订阅规则', 'Profile')}</option>
            </select>
            <span class="pill">${filtered.length} / ${allEffRules.length}</span>
          </div>
          <div class="table-wrap" style="flex:1;min-height:110px;">
            <table class="data-table">
              <thead>
                <tr>
                  <th style="text-align:center;">${t('启用', 'On')}</th>
                  <th>${t('来源', 'Source')}</th>
                  <th>${t('规则类型', 'Type')}</th>
                  <th>${t('匹配内容 (Payload)', 'Payload')}</th>
                  <th>${t('命中策略组', 'Proxy Target')}</th>
                  <th style="text-align:right;">${t('命中/未中', 'Hit/Miss')}</th>
                </tr>
              </thead>
              <tbody>${effRowsHtml}</tbody>
            </table>
          </div>
        </div>

        <div class="rules-bottom-pane">
          <div class="row-flex" style="justify-content:space-between;">
            <div class="row-flex">
              <strong style="font-size:12.5px;">${t('本地规则集热更新 (.list)', 'Local RuleSet Hot-Reload (.list)')}</strong>
              ${
                providers.length
                  ? `<select class="select select-sm mono" id="prov-select" style="width:210px;">
                      ${providers.map((p) => `<option value="${esc(p.name)}" ${activeProv?.name === p.name ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}
                     </select>`
                  : `<span class="pill">${t('暂无可编辑规则集', 'No editable .list providers')}</span>`
              }
              ${activeProv ? `<span class="pill pill-blue">${esc(activeProv.behavior || 'Classical')}</span>` : ''}
              ${activeProv ? `<span class="pill pill-green" id="prov-count-pill">${provRuleCount} ${t('条规则', 'rules')}</span>` : ''}
              ${activeProv?.file_path ? `<span class="metric-sub mono selectable" style="max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${esc(activeProv.file_path)}">${esc(activeProv.file_path)}</span>` : ''}
            </div>
            ${
              activeProv
                ? `<div class="row-flex">
                    <button type="button" class="btn btn-sm" id="prov-reset-btn" ${!activeProv.can_reset ? 'disabled' : ''}>${t('恢复内置默认', 'Reset Default')}</button>
                    <button type="button" class="btn btn-sm btn-primary" id="prov-save-btn">${ICONS.check} ${t('保存并零断流热更新', 'Save & Hot-Reload')}</button>
                   </div>`
                : ''
            }
          </div>
          <textarea class="code-editor" id="prov-editor" spellcheck="false" ${!activeProv ? 'disabled' : ''} placeholder="${t('选择上方 RuleSet 规则集或从下拉框切换即可直接编辑，保存后零断流热更新。', 'Select a RuleSet row above or choose a .list provider to edit and hot-reload.')}">${esc(rulesState.providerDraftText)}</textarea>
        </div>
      </div>
    `;
  }

  root.innerHTML = `<div class="rules-shell">${headerHtml}${bodyHtml}</div>`;

  // Sub-tab switch with bidirectional Visual <-> YAML conversion
  root.querySelectorAll('[data-rule-tab]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const next = Number(btn.getAttribute('data-rule-tab'));
      if (next === rulesState.subTab) return;
      if (rulesState.subTab === 0 && next === 1) {
        const res = await rpc('convert_profile_mixin', {
          direction: 'to_yaml',
          yaml: rulesState.baseYaml,
          bypass_cn_web: rulesState.bypassCnWeb,
          bypass_lan: rulesState.bypassLan,
          rules: rulesState.rules,
        });
        if (res.ok && res.data) rulesState.yamlText = res.data.yaml || rulesState.yamlText;
      } else if (rulesState.subTab === 1 && next === 0) {
        const res = await rpc('convert_profile_mixin', {
          direction: 'to_structured',
          yaml: rulesState.yamlText,
        });
        if (res.ok && res.data) {
          rulesState.bypassCnWeb = Boolean(res.data.bypass_cn_web);
          rulesState.bypassLan = Boolean(res.data.bypass_lan);
          rulesState.rules = Array.isArray(res.data.rules) ? res.data.rules : [];
          rulesState.baseYaml = rulesState.yamlText;
        }
      }
      rulesState.subTab = next;
      if (next === 2) {
        await ensureRulesDataLoaded(true);
      }
      renderRulesPage(root);
    });
  });

  root.querySelector('#rules-reload-btn')?.addEventListener('click', async () => {
    rulesState.mixinDirty = false;
    rulesState.providerDirty = false;
    await ensureRulesDataLoaded(true);
    renderRulesPage(root);
    showToast(t('规则状态已刷新', 'Rules reloaded'), 'ok');
  });

  if (rulesState.subTab === 0) {
    root.querySelector('#mixin-cn-web')?.addEventListener('change', (e) => {
      rulesState.bypassCnWeb = e.target.checked;
      rulesState.mixinDirty = true;
    });
    root.querySelector('#mixin-lan')?.addEventListener('change', (e) => {
      rulesState.bypassLan = e.target.checked;
      rulesState.mixinDirty = true;
    });
    const newTypeEl = root.querySelector('#new-rule-type');
    const newPayloadEl = root.querySelector('#new-rule-payload');
    newTypeEl?.addEventListener('change', () => {
      if (!newPayloadEl) return;
      const isMatch = newTypeEl.value === 'MATCH';
      newPayloadEl.disabled = isMatch;
      newPayloadEl.placeholder = isMatch ? '(MATCH)' : 'openai.com / 192.168.0.0/16 / cursor.exe';
    });
    root.querySelector('#btn-add-rule')?.addEventListener('click', () => {
      const type = root.querySelector('#new-rule-type')?.value || 'DOMAIN-SUFFIX';
      const payload = (root.querySelector('#new-rule-payload')?.value || '').trim();
      const target = root.querySelector('#new-rule-target')?.value || rulesState.defaultTarget;
      if (type !== 'MATCH' && !payload) {
        showToast(t('请输入匹配内容 Payload', 'Please enter a payload'), 'warn');
        return;
      }
      rulesState.rules.unshift({ type, payload: type === 'MATCH' ? '' : payload, target });
      rulesState.mixinDirty = true;
      renderRulesPage(root);
    });
    root.querySelectorAll('[data-rule-field]').forEach((el) => {
      const ev = el.tagName === 'INPUT' ? 'input' : 'change';
      el.addEventListener(ev, () => {
        const idx = Number(el.getAttribute('data-idx'));
        const field = el.getAttribute('data-rule-field');
        if (!rulesState.rules[idx]) return;
        rulesState.rules[idx][field] = el.value;
        rulesState.mixinDirty = true;
      });
    });
    root.querySelectorAll('[data-rule-move]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const idx = Number(btn.getAttribute('data-idx'));
        const delta = Number(btn.getAttribute('data-rule-move'));
        const targetIdx = idx + delta;
        if (targetIdx < 0 || targetIdx >= rulesState.rules.length) return;
        const tmp = rulesState.rules[idx];
        rulesState.rules[idx] = rulesState.rules[targetIdx];
        rulesState.rules[targetIdx] = tmp;
        rulesState.mixinDirty = true;
        renderRulesPage(root);
      });
    });
    root.querySelectorAll('[data-rule-del]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const idx = Number(btn.getAttribute('data-rule-del'));
        rulesState.rules.splice(idx, 1);
        rulesState.mixinDirty = true;
        renderRulesPage(root);
      });
    });
    root.querySelector('#btn-save-visual-mixin')?.addEventListener('click', async () => {
      const res = await rpc('save_profile_mixin', {
        mode: 'structured',
        base_yaml: rulesState.baseYaml,
        bypass_cn_web: rulesState.bypassCnWeb,
        bypass_lan: rulesState.bypassLan,
        rules: rulesState.rules,
      });
      if (res.ok) {
        rulesState.mixinDirty = false;
        rulesState.effectiveLoaded = false;
        await ensureRulesDataLoaded(true);
        renderRulesPage(root);
        showToast(t('Mixin 分流补丁已保存并应用', 'Mixin patch saved and applied'), 'ok');
      }
    });
  } else if (rulesState.subTab === 1) {
    root.querySelector('#mixin-yaml-editor')?.addEventListener('input', (e) => {
      rulesState.yamlText = e.target.value;
      rulesState.mixinDirty = true;
    });
    root.querySelector('#btn-open-mixin-dir')?.addEventListener('click', () => {
      if (snap.config_dir) native('open_path', { path: snap.config_dir });
    });
    root.querySelector('#btn-save-yaml-mixin')?.addEventListener('click', async () => {
      const res = await rpc('save_profile_mixin', {
        mode: 'raw',
        yaml: rulesState.yamlText,
      });
      if (res.ok) {
        rulesState.mixinDirty = false;
        rulesState.effectiveLoaded = false;
        await ensureRulesDataLoaded(true);
        renderRulesPage(root);
        showToast(t('YAML 补丁源码已保存并应用', 'YAML Mixin saved and applied'), 'ok');
      }
    });
  } else {
    const effSearchEl = root.querySelector('#eff-search');
    effSearchEl?.addEventListener('input', (e) => {
      rulesState.effSearch = e.target.value;
      const caret = e.target.selectionStart;
      renderRulesPage(root);
      const next = root.querySelector('#eff-search');
      if (next) {
        next.focus();
        next.setSelectionRange(caret, caret);
      }
    });
    root.querySelector('#eff-source')?.addEventListener('change', (e) => {
      rulesState.effSource = e.target.value;
      renderRulesPage(root);
    });
    root.querySelectorAll('[data-rule-enable]').forEach((cb) => {
      cb.addEventListener('click', (e) => e.stopPropagation());
      cb.addEventListener('change', async (e) => {
        const index = Number(cb.getAttribute('data-rule-enable'));
        const res = await rpc('set_rule_disabled', { indices: [index], disabled: !e.target.checked });
        if (res.ok) {
          await ensureRulesDataLoaded(true);
          renderRulesPage(root);
          showToast(e.target.checked ? t('规则已启用', 'Rule enabled') : t('规则已禁用', 'Rule disabled'), 'ok');
        }
      });
    });
    root.querySelectorAll('[data-eff-row-type="RuleSet"]').forEach((tr) => {
      tr.addEventListener('click', () => {
        const payload = tr.getAttribute('data-eff-row-payload') || '';
        const matched = providers.find((p) => p.name.toLowerCase() === payload.toLowerCase());
        if (matched && matched.name !== rulesState.selectedProvider) {
          rulesState.selectedProvider = matched.name;
          rulesState.providerDraftText = matched.raw_text || '';
          rulesState.providerDirty = false;
          renderRulesPage(root);
        }
      });
    });
    root.querySelector('#prov-select')?.addEventListener('change', (e) => {
      const nextName = e.target.value;
      const matched = providers.find((p) => p.name === nextName);
      rulesState.selectedProvider = nextName;
      rulesState.providerDraftText = matched?.raw_text || '';
      rulesState.providerDirty = false;
      renderRulesPage(root);
    });
    root.querySelector('#prov-editor')?.addEventListener('input', (e) => {
      rulesState.providerDraftText = e.target.value;
      rulesState.providerDirty = true;
      const pill = root.querySelector('#prov-count-pill');
      if (pill) {
        pill.textContent = `${countListLines(e.target.value)} ${t('条规则', 'rules')}`;
      }
    });
    root.querySelector('#prov-reset-btn')?.addEventListener('click', async () => {
      if (!activeProv) return;
      const yes = await showConfirm({
        title: t(`恢复默认规则集「${activeProv.name}」`, `Reset "${activeProv.name}"`),
        message: t('将覆盖当前编辑中的内容并立即热更新生效。确认恢复？', 'Reset to built-in rules and hot-reload immediately?'),
        confirmText: t('恢复默认', 'Reset'),
      });
      if (!yes) return;
      const res = await rpc('reset_rule_provider', { provider_name: activeProv.name });
      if (res.ok) {
        rulesState.providerDirty = false;
        await ensureRulesDataLoaded(true);
        renderRulesPage(root);
        showToast(t('已恢复内置默认并热更新', 'Restored default and reloaded'), 'ok');
      }
    });
    root.querySelector('#prov-save-btn')?.addEventListener('click', async () => {
      if (!activeProv) return;
      const res = await rpc('save_rule_provider', {
        provider_name: activeProv.name,
        content: rulesState.providerDraftText,
      });
      if (res.ok) {
        rulesState.providerDirty = false;
        await ensureRulesDataLoaded(true);
        renderRulesPage(root);
        showToast(t(`规则集「${activeProv.name}」已保存并零断流热更新`, `Hot-reloaded "${activeProv.name}"`), 'ok');
      }
    });
  }
}
