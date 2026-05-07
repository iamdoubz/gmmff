'use strict';

// ── Config ──────────────────────────────────────────────────────────────────
const THEME_URL = 'themes/default.json';
const I18N_URL  = 'i18n/en.json';
const WASM_URL  = 'gmmff.wasm';

// ── State ────────────────────────────────────────────────────────────────────

// normaliseServerURL rewrites 'localhost' → '127.0.0.1' so the browser's
// native WebSocket API can connect without a DNS lookup, which fails in Wasm.
function normaliseServerURL(url) {
  return url.replace(/\/\/localhost([:/#?]|$)/, '//127.0.0.1$1');
}
let i18n   = {};
let cancel = null; // function set by Wasm to cancel active transfer

// ── Boot sequence ─────────────────────────────────────────────────────────────
async function boot() {
  try {
    const [theme, strings] = await Promise.all([
      fetch(THEME_URL).then(r => r.json()),
      fetch(I18N_URL).then(r => r.json()),
    ]);
    applyTheme(theme);
    applyI18n(strings);
    await loadWasm();
    hideLoading();
  } catch (err) {
    showFatalError(err);
  }
}

// ── Theme ──────────────────────────────────────────────────────────────────
function applyTheme(theme) {
  const map = {
    font_family:        '--font-family',
    font_size_base:     '--font-size-base',
    font_size_sm:       '--font-size-sm',
    font_size_lg:       '--font-size-lg',
    font_size_xl:       '--font-size-xl',
    font_weight_normal: '--font-weight-normal',
    font_weight_medium: '--font-weight-medium',
    font_weight_bold:   '--font-weight-bold',
    line_height:        '--line-height',
    color_bg:             '--color-bg',
    color_surface:        '--color-surface',
    color_surface_raised: '--color-surface-raised',
    color_border:         '--color-border',
    color_border_focus:   '--color-border-focus',
    color_text:           '--color-text',
    color_text_muted:     '--color-text-muted',
    color_text_inverse:   '--color-text-inverse',
    color_accent:         '--color-accent',
    color_accent_hover:   '--color-accent-hover',
    color_accent_active:  '--color-accent-active',
    color_success:        '--color-success',
    color_warning:        '--color-warning',
    color_error:          '--color-error',
    color_progress_track: '--color-progress-track',
    color_progress_fill:  '--color-progress-fill',
    radius_sm:   '--radius-sm',
    radius_md:   '--radius-md',
    radius_lg:   '--radius-lg',
    radius_pill: '--radius-pill',
    spacing_xs: '--spacing-xs',
    spacing_sm: '--spacing-sm',
    spacing_md: '--spacing-md',
    spacing_lg: '--spacing-lg',
    spacing_xl: '--spacing-xl',
    shadow_card:  '--shadow-card',
    shadow_focus: '--shadow-focus',
    transition: '--transition',
    max_width:  '--max-width',
  };
  const root = document.documentElement;
  for (const [key, prop] of Object.entries(map)) {
    if (theme[key] !== undefined) root.style.setProperty(prop, theme[key]);
  }
  // Update theme-color meta to match bg
  const tm = document.querySelector('meta[name="theme-color"]');
  if (tm && theme.color_bg) tm.content = theme.color_bg;
}

// ── i18n ───────────────────────────────────────────────────────────────────
function applyI18n(strings) {
  i18n = strings;
  // Text content
  document.querySelectorAll('[data-i18n]').forEach(el => {
    const key = el.dataset.i18n;
    if (strings[key] !== undefined) el.textContent = strings[key];
  });
  // Placeholders
  document.querySelectorAll('[data-i18n-placeholder]').forEach(el => {
    const key = el.dataset.i18nPlaceholder;
    if (strings[key] !== undefined) el.placeholder = strings[key];
  });
  // Page title
  if (strings.app_name) document.title = strings.app_name;
}

function t(key, vars = {}) {
  let s = i18n[key] || key;
  for (const [k, v] of Object.entries(vars)) s = s.replaceAll(`{${k}}`, v);
  return s;
}

// ── Wasm ────────────────────────────────────────────────────────────────────
async function loadWasm() {
  const go = new Go();
  const result = await WebAssembly.instantiateStreaming(fetch(WASM_URL), go.importObject);
  go.run(result.instance); // runs the Go main() — registers JS callbacks
}

// ── Loading overlay ──────────────────────────────────────────────────────────
function hideLoading() {
  const overlay = document.getElementById('loading-overlay');
  overlay.classList.add('hidden');
  setTimeout(() => overlay.remove(), 450);
}

function showFatalError(err) {
  const overlay = document.getElementById('loading-overlay');
  const text    = overlay.querySelector('.loading-overlay__text');
  overlay.querySelector('.loading-overlay__spinner').style.display = 'none';
  text.textContent = t('error_wasm_load');
  text.style.color = 'var(--color-error)';
  console.error('gmmff boot error:', err);
}

// ── Tabs ──────────────────────────────────────────────────────────────────────
document.querySelectorAll('.tab').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach(t => t.setAttribute('aria-selected', 'false'));
    document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
    btn.setAttribute('aria-selected', 'true');
    document.getElementById(btn.getAttribute('aria-controls')).classList.add('active');
  });
});

// ── File picker ───────────────────────────────────────────────────────────────
const fileInput   = document.getElementById('send-file-input');
const fileName    = document.getElementById('send-file-name');
const pickBtn     = document.getElementById('send-pick-btn');

pickBtn.addEventListener('click', () => fileInput.click());
fileInput.addEventListener('change', () => {
  if (fileInput.files[0]) {
    fileName.textContent = fileInput.files[0].name;
    fileName.classList.add('has-file');
  } else {
    fileName.textContent = t('send_no_file');
    fileName.classList.remove('has-file');
  }
});

// ── Copy code button ───────────────────────────────────────────────────────────
document.getElementById('send-copy-btn').addEventListener('click', async () => {
  const code = document.getElementById('send-code-value').textContent;
  const btn  = document.getElementById('send-copy-btn');
  try {
    await navigator.clipboard.writeText(code);
    btn.textContent = t('code_copied');
    setTimeout(() => { btn.textContent = t('code_copy'); }, 2000);
  } catch (_) {}
});

// ── Cancel buttons ─────────────────────────────────────────────────────────────
['send-cancel-btn', 'send-cancel-progress-btn', 'receive-cancel-btn'].forEach(id => {
  document.getElementById(id)?.addEventListener('click', () => {
    if (cancel) { cancel(); cancel = null; }
    resetUI();
  });
});

// ── Send button ───────────────────────────────────────────────────────────────
document.getElementById('send-btn').addEventListener('click', () => {
  const file   = fileInput.files[0];
  const server = normaliseServerURL(document.getElementById('send-server').value.trim());
  const errEl  = document.getElementById('send-error');

  errEl.textContent = '';
  if (!file)   { errEl.textContent = t('error_no_file');   return; }
  if (!server) { errEl.textContent = t('error_no_server'); return; }

  // Hand off to Go/Wasm
  if (typeof window.gmmffSend === 'function') {
    window.gmmffSend(file, server);
  }
});

// ── Receive button ─────────────────────────────────────────────────────────────
document.getElementById('receive-btn').addEventListener('click', () => {
  const code   = document.getElementById('receive-code').value.trim();
  const server = normaliseServerURL(document.getElementById('receive-server').value.trim());
  const errEl  = document.getElementById('receive-error');

  errEl.textContent = '';
  if (!code)   { errEl.textContent = t('error_no_code');   return; }
  if (!server) { errEl.textContent = t('error_no_server'); return; }

  if (typeof window.gmmffReceive === 'function') {
    window.gmmffReceive(code, server);
  }
});

// ── UI state helpers (called by Wasm) ─────────────────────────────────────────

// Show the code box after slot is created
window.uiShowCode = function(code) {
  document.getElementById('send-form').classList.add('hidden');
  document.getElementById('send-code').classList.remove('hidden');
  document.getElementById('send-code-value').textContent = code;
};

// Show sender progress bar
window.uiSendProgress = function(pct, bytesSent, totalBytes, speed, eta) {
  document.getElementById('send-code').classList.add('hidden');
  const prog = document.getElementById('send-progress');
  prog.classList.remove('hidden');
  const bar = document.getElementById('send-bar');
  bar.style.width = pct + '%';
  bar.closest('[role="progressbar"]').setAttribute('aria-valuenow', pct);
  document.getElementById('send-progress-bytes').textContent =
    t('progress_of', { sent: fmtBytes(bytesSent), total: fmtBytes(totalBytes) });
  document.getElementById('send-progress-speed').textContent =
    fmtBytes(speed) + '/s' + (eta > 0 ? '  ' + fmtEta(eta) : '');
};

// Show receiver progress bar
window.uiReceiveProgress = function(pct, bytesRecv, totalBytes, speed, eta) {
  document.getElementById('receive-form').classList.add('hidden');
  const prog = document.getElementById('receive-progress');
  prog.classList.remove('hidden');
  const bar = document.getElementById('receive-bar');
  bar.style.width = pct + '%';
  bar.closest('[role="progressbar"]').setAttribute('aria-valuenow', pct);
  document.getElementById('receive-progress-bytes').textContent =
    t('progress_of', { sent: fmtBytes(bytesRecv), total: fmtBytes(totalBytes) });
  document.getElementById('receive-progress-speed').textContent =
    fmtBytes(speed) + '/s' + (eta > 0 ? '  ' + fmtEta(eta) : '');
};

// Set status text on the active panel
window.uiStatus = function(key, vars, panel) {
  const el = document.getElementById(panel === 'send' ? 'send-status' : 'receive-status');
  if (!el) return;
  el.className = 'status';
  el.textContent = t(key, vars || {});
};

window.uiStatusRaw = function(text, variant, panel) {
  const el = document.getElementById(panel === 'send' ? 'send-status' : 'receive-status');
  if (!el) return;
  el.className = 'status' + (variant ? ` status--${variant}` : '');
  el.textContent = text;
};

// Called when Wasm registers the cancel callback
window.uiRegisterCancel = function(fn) { cancel = fn; };

// Called on clean completion
window.uiDone = function(panel, message) {
  window.uiStatusRaw(message || t(panel === 'send' ? 'status_done_send' : 'status_done_receive'),
                     'success', panel);
  // Hide cancel button
  const cancelBtn = panel === 'send'
    ? document.getElementById('send-cancel-progress-btn')
    : document.getElementById('receive-cancel-btn');
  if (cancelBtn) cancelBtn.classList.add('hidden');
  cancel = null;
};

// Called on error
window.uiError = function(message, panel) {
  if (panel === 'send') {
    resetSendUI();
    document.getElementById('send-error').textContent = t('status_error', { message });
  } else {
    resetReceiveUI();
    document.getElementById('receive-error').textContent = t('status_error', { message });
  }
};

// ── Reset helpers ────────────────────────────────────────────────────────────
function resetSendUI() {
  document.getElementById('send-form').classList.remove('hidden');
  document.getElementById('send-code').classList.add('hidden');
  document.getElementById('send-progress').classList.add('hidden');
  document.getElementById('send-cancel-progress-btn').classList.remove('hidden');
  document.getElementById('send-status').textContent = '';
  document.getElementById('send-bar').style.width = '0%';
}
function resetReceiveUI() {
  document.getElementById('receive-form').classList.remove('hidden');
  document.getElementById('receive-progress').classList.add('hidden');
  document.getElementById('receive-cancel-btn').classList.remove('hidden');
  document.getElementById('receive-status').textContent = '';
  document.getElementById('receive-bar').style.width = '0%';
}
function resetUI() { resetSendUI(); resetReceiveUI(); }

// ── Formatting helpers ────────────────────────────────────────────────────────
function fmtEta(seconds) {
  if (!seconds || seconds <= 0 || !isFinite(seconds)) return '';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return h + 'h ' + m + 'm';
  if (m > 0) return m + 'm ' + s + 's';
  return s + 's';
}

function fmtBytes(n) {
  if (n === undefined || n === null) return '';
  if (n < 1024)        return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 ** 3)   return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}

// ── Go ────────────────────────────────────────────────────────────────────────
boot();
