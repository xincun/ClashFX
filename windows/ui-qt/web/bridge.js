const pendingRpc = new Map();
const pendingMihomo = new Map();
const pendingNative = new Map();
const listeners = new Set();

let nextId = 1;
const collapsedGroups = new Set();

export const store = {
  connected: false,
  snapshot: {},
  language: 'zh_CN',
  theme: 'dark',
  maximized: false,
  page: 0,
  subTab: -1,
  diagNode: '',
  telemetry: {
    up: 0,
    down: 0,
    upTotal: 0,
    downTotal: 0,
    memInuse: 0,
    connectionCount: 0,
    connections: [],
  },
  telemetryHistory: Array.from({ length: 36 }, () => ({ up: 0, down: 0 })),
  collapsedGroups,
  proxySearch: '',
  pendingNav: null,
};

export function t(zh, en) {
  return store.language === 'en_US' ? en : zh;
}

export function esc(val) {
  return String(val ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

export function postNative(msg) {
  if (window.chrome && window.chrome.webview) {
    window.chrome.webview.postMessage(msg);
  }
}

export function subscribe(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function notify(eventType, payload) {
  for (const fn of listeners) {
    try {
      fn(eventType, payload);
    } catch (err) {
      console.error('Store listener error:', err);
    }
  }
}

export function rpc(method, params = {}, options = {}) {
  const { allowUacPrompt = true, silent = false } = options;
  const sendOnce = (p) =>
    new Promise((resolve) => {
      const id = nextId++;
      pendingRpc.set(id, resolve);
      postNative({ type: 'rpc', id, method, params: p || {} });
      setTimeout(() => {
        if (pendingRpc.has(id)) {
          pendingRpc.delete(id);
          resolve({ ok: false, error: t('请求超时', 'Request timed out'), error_code: 'TIMEOUT' });
        }
      }, 30000);
    });

  return sendOnce(params).then(async (res) => {
    if (!res.ok && res.error_code === 'SERVICE_INSTALL_REQUIRED' && allowUacPrompt) {
      const yes = await showConfirm({
        title: t('需要管理员权限安装核心服务', 'Administrator Elevation Required'),
        message: t(
          '启用 TUN 增强模式或 AI 锁定需要以系统服务权限运行。是否立即请求 Windows UAC 授权安装并启用 ClashFX 服务？',
          'Enabling Enhanced TUN Mode or AI Lock requires the ClashFX system service. Grant Windows UAC elevation to install and start the service now?'
        ),
        confirmText: t('授权并启用', 'Elevate & Enable'),
      });
      if (!yes) {
        return res;
      }
      const retryRes = await sendOnce({ ...params, allow_uac: true });
      if (!retryRes.ok && !silent) {
        showToast(retryRes.error || t('操作失败', 'Action failed'), 'err');
      }
      return retryRes;
    }
    if (!res.ok && !silent) {
      showToast(res.error || t('操作失败', 'Action failed'), 'err');
    }
    return res;
  });
}

export function mihomo(method, path, body = undefined) {
  return new Promise((resolve) => {
    const id = nextId++;
    pendingMihomo.set(id, resolve);
    const msg = { type: 'mihomo_api', id, method, path };
    if (body !== undefined) msg.body = body;
    postNative(msg);
    setTimeout(() => {
      if (pendingMihomo.has(id)) {
        pendingMihomo.delete(id);
        resolve({ ok: false, status: 0, error: 'Timeout' });
      }
    }, 10000);
  });
}

export function native(action, payload = {}) {
  return new Promise((resolve) => {
    const id = nextId++;
    pendingNative.set(id, resolve);
    postNative({ type: 'native_action', id, action, ...payload });
    setTimeout(() => {
      if (pendingNative.has(id)) {
        pendingNative.delete(id);
        resolve({ ok: false, error: 'Timeout' });
      }
    }, 15000);
  });
}

export function applyTheme(theme) {
  const next = theme === 'light' ? 'light' : 'dark';
  store.theme = next;
  document.documentElement.setAttribute('data-theme', next);
  postNative({ type: 'set_theme', theme: next });
}

export async function toggleLanguage() {
  const next = store.language === 'en_US' ? 'zh_CN' : 'en_US';
  store.language = next;
  postNative({ type: 'set_language', language: next });
  const settings = { ...(store.snapshot?.settings || {}), language: next };
  rpc('update_settings', { settings }, { silent: true });
  notify('language', next);
}

export function formatSpeed(bytesPerSec = 0) {
  const n = Number(bytesPerSec) || 0;
  if (n < 1024) return `${n.toFixed(0)} B/s`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB/s`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(2)} MB/s`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB/s`;
}

export function formatBytes(bytes = 0) {
  const n = Number(bytes) || 0;
  if (n <= 0) return '0 B';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (n < 1024 * 1024 * 1024 * 1024) return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  return `${(n / (1024 * 1024 * 1024 * 1024)).toFixed(2)} TB`;
}

export function formatRelativeTime(epochSec) {
  if (!epochSec) return t('未同步', 'Never');
  const diff = Math.max(0, Math.floor(Date.now() / 1000) - Number(epochSec));
  if (diff < 60) return t('刚刚', 'Just now');
  if (diff < 3600) return t(`${Math.floor(diff / 60)} 分钟前`, `${Math.floor(diff / 60)}m ago`);
  if (diff < 86400) return t(`${Math.floor(diff / 3600)} 小时前`, `${Math.floor(diff / 3600)}h ago`);
  return t(`${Math.floor(diff / 86400)} 天前`, `${Math.floor(diff / 86400)}d ago`);
}

export function formatExpireDate(expireEpoch, expireText = '') {
  if (expireText) return expireText;
  if (!expireEpoch || Number(expireEpoch) <= 0) return t('长期有效', 'Unlimited');
  const d = new Date(Number(expireEpoch) * 1000);
  if (Number.isNaN(d.getTime())) return '--';
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  const daysLeft = Math.ceil((d.getTime() - Date.now()) / 86400000);
  const dateStr = `${y}-${m}-${day}`;
  if (daysLeft < 0) return `${dateStr} (${t('已过期', 'Expired')})`;
  return `${dateStr} (${daysLeft}d)`;
}

export function buildSparklineSvg(history) {
  const pts = history && history.length ? history : [{ up: 0, down: 0 }];
  const maxVal = Math.max(10240, ...pts.map((p) => Math.max(p.up || 0, p.down || 0)));
  const W = 280;
  const H = 44;
  const toPath = (key, fill) => {
    const coords = pts.map((p, idx) => {
      const x = pts.length === 1 ? W : (idx / (pts.length - 1)) * W;
      const y = H - 3 - Math.min(1, (p[key] || 0) / maxVal) * (H - 8);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    });
    if (!fill) {
      return `M ${coords.join(' L ')}`;
    }
    return `M 0,${H} L ${coords.join(' L ')} L ${W},${H} Z`;
  };
  return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
    <defs>
      <linearGradient id="dlGrad" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="#14b8a6" stop-opacity="0.34"/>
        <stop offset="100%" stop-color="#14b8a6" stop-opacity="0.02"/>
      </linearGradient>
    </defs>
    <path d="${toPath('down', true)}" fill="url(#dlGrad)"/>
    <path d="${toPath('down', false)}" fill="none" stroke="#14b8a6" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
    <path d="${toPath('up', false)}" fill="none" stroke="#3b82f6" stroke-width="1.5" stroke-dasharray="3 2" stroke-linecap="round"/>
  </svg>`;
}

export function showToast(message, tone = 'info', durationMs = 3400) {
  const container = document.getElementById('toast-container');
  if (!container) return;
  const el = document.createElement('div');
  el.className = `toast ${tone}`;
  el.textContent = String(message || '');
  container.appendChild(el);
  setTimeout(() => {
    el.style.opacity = '0';
    el.style.transform = 'translateY(6px)';
    el.style.transition = 'all 0.16s ease';
    setTimeout(() => el.remove(), 180);
  }, durationMs);
}

export function showConfirm({ title, message, confirmText, cancelText, danger = false }) {
  return new Promise((resolve) => {
    const root = document.getElementById('modal-root');
    if (!root) {
      resolve(false);
      return;
    }
    root.innerHTML = `
      <div class="modal-card" role="dialog" aria-modal="true">
        <div class="modal-title">${esc(title)}</div>
        <div class="modal-body">${esc(message)}</div>
        <div class="modal-actions">
          <button type="button" class="btn" id="modal-cancel">${esc(cancelText || t('取消', 'Cancel'))}</button>
          <button type="button" class="btn ${danger ? 'btn-danger' : 'btn-primary'}" id="modal-ok">${esc(confirmText || t('确认', 'Confirm'))}</button>
        </div>
      </div>`;
    root.classList.add('open');
    root.setAttribute('aria-hidden', 'false');
    const close = (val) => {
      root.classList.remove('open');
      root.setAttribute('aria-hidden', 'true');
      root.innerHTML = '';
      resolve(val);
    };
    root.querySelector('#modal-cancel')?.addEventListener('click', () => close(false));
    root.querySelector('#modal-ok')?.addEventListener('click', () => close(true));
    root.onclick = (e) => {
      if (e.target === root) close(false);
    };
  });
}

export function initBridgeListener() {
  if (!window.chrome || !window.chrome.webview) return;
  window.chrome.webview.addEventListener('message', (event) => {
    const msg = typeof event.data === 'string' ? JSON.parse(event.data) : event.data;
    if (!msg || typeof msg !== 'object') return;

    switch (msg.type) {
      case 'init':
        store.connected = Boolean(msg.connected);
        store.snapshot = msg.snapshot || {};
        store.language = msg.language || 'zh_CN';
        store.theme = msg.theme === 'light' ? 'light' : 'dark';
        store.maximized = Boolean(msg.maximized);
        store.page = Number.isInteger(msg.page) ? msg.page : 0;
        store.subTab = Number.isInteger(msg.subTab) ? msg.subTab : -1;
        store.diagNode = msg.diagNode || '';
        document.documentElement.setAttribute('data-theme', store.theme);
        document.documentElement.classList.toggle('is-maximized', store.maximized);
        notify('init', msg);
        break;
      case 'state':
        store.connected = Boolean(msg.connected);
        store.snapshot = msg.snapshot || {};
        notify('state', msg);
        break;
      case 'navigate':
        store.page = Number.isInteger(msg.page) ? msg.page : 0;
        store.subTab = Number.isInteger(msg.subTab) ? msg.subTab : -1;
        if (msg.diagNode) store.diagNode = msg.diagNode;
        notify('navigate', msg);
        break;
      case 'language':
        store.language = msg.language || 'zh_CN';
        notify('language', msg.language);
        break;
      case 'window_state':
        store.maximized = Boolean(msg.maximized);
        document.documentElement.classList.toggle('is-maximized', store.maximized);
        notify('window_state', msg);
        break;
      case 'telemetry':
        store.telemetry = {
          ...store.telemetry,
          up: Number(msg.up) || 0,
          down: Number(msg.down) || 0,
          upTotal: msg.upTotal ?? store.telemetry.upTotal,
          downTotal: msg.downTotal ?? store.telemetry.downTotal,
          memInuse: Number(msg.memory || msg.memInuse || store.telemetry.memInuse || 0),
          connectionCount: msg.connectionCount ?? store.telemetry.connectionCount,
          connections: Array.isArray(msg.connections) ? msg.connections : store.telemetry.connections,
        };
        if ('up' in msg || 'down' in msg) {
          store.telemetryHistory = [
            ...store.telemetryHistory.slice(1),
            { up: store.telemetry.up, down: store.telemetry.down },
          ];
        }
        notify('telemetry', store.telemetry);
        break;
      case 'rpc_result': {
        const cb = pendingRpc.get(msg.id);
        if (cb) {
          pendingRpc.delete(msg.id);
          cb({ ok: Boolean(msg.ok), data: msg.data, error: msg.error || '', error_code: msg.error_code || '' });
        }
        break;
      }
      case 'mihomo_result': {
        const cb = pendingMihomo.get(msg.id);
        if (cb) {
          pendingMihomo.delete(msg.id);
          cb({ ok: Boolean(msg.ok), status: msg.status || 0, data: msg.data, error: msg.error || '' });
        }
        break;
      }
      case 'native_result': {
        const cb = pendingNative.get(msg.id);
        if (cb) {
          pendingNative.delete(msg.id);
          cb({ ok: Boolean(msg.ok), data: msg.data, error: msg.error || '' });
        }
        break;
      }
      default:
        break;
    }
  });
}
