'use strict';

// ── Config ──────────────────────────────────────────────────────────────────
const THEME_URL      = 'themes/default.json';
const LANGUAGES_URL  = 'i18n/languages.json';
const WASM_URL       = 'gmmff.wasm';

// ── State ────────────────────────────────────────────────────────────────────
let currentLang    = 'en';
let availableLangs = [];
let filteredLangs  = []; // subset of availableLangs after server config applied
let uiConfig       = {}; // feature flags from /config.json

// Track the last known progress values for each panel so uiDone can
// snapshot them for the completion summary line.
const lastProgress = {
  send:    { total: 0, speed: 0, startTime: null },
  receive: { total: 0, speed: 0, startTime: null },
};

// Load Inter font asynchronously — avoids CSP inline-event-handler violations.
// Falls back to system-ui immediately; Inter swaps in once downloaded.
// Skipped when offline (request simply times out, no error shown to user).
(function loadInterFont() {
  const link  = document.createElement('link');
  link.rel    = 'stylesheet';
  link.media  = 'print';
  link.href   = 'https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&display=swap';
  link.onload = function() { this.media = 'all'; };
  document.head.appendChild(link);
}());


// native WebSocket API can connect without a DNS lookup, which fails in Wasm.
function normaliseServerURL(url) {
  return url.replace(/\/\/localhost([:/#?]|$)/, '//127.0.0.1$1');
}
let i18n   = {};
let cancel        = null; // function set by Wasm to cancel active transfer
let myName    = '';          // own display name — empty means use default 'Me' locally
let peerNames = new Map();   // peerIndex (1-based) → display name
let peerCount = 0;           // number of peers joined so far (for auto-naming)

// NAME_PREFIX is a sentinel prepended to name-announcement messages.
// It lets the receiver distinguish a name announcement from a chat message.
const NAME_PREFIX   = '\x01name:';
const ROSTER_PREFIX = '\x01roster:';

// ── Boot sequence ─────────────────────────────────────────────────────────────
async function boot() {
  try {
    const [theme, langs, cfg] = await Promise.all([
      fetch(THEME_URL).then(r => r.json()),
      fetch(LANGUAGES_URL).then(r => r.json()),
      fetch('/config.json').then(r => r.json()).catch(() => ({})),
    ]);
    applyTheme(theme);
    applyUIConfig(cfg, langs);
    availableLangs = filteredLangs;
    currentLang = detectLanguage(filteredLangs);
    await switchLanguage(currentLang);
    await loadWasm();
    hideLoading();
    // Init schedule feature after Wasm is ready.
    if (typeof window.schedInit === 'function') window.schedInit(cfg);
  } catch (err) {
    showFatalError(err);
  }
}

// ── UI Config (feature flags from /config.json) ───────────────────────────────

// applyUIConfig reads the server-provided feature flags and adjusts the DOM.
// Called once during boot before the loading overlay is removed.
function applyUIConfig(cfg, allLangs) {
  uiConfig = cfg;

  // ── Tab visibility ────────────────────────────────────────────────────────
  const showFiles = cfg.show_files !== false;
  const showChat  = cfg.show_chat  !== false;

  if (!showFiles) {
    document.getElementById('tab-files')?.classList.add('hidden');
    document.getElementById('panel-files')?.classList.add('hidden');
  }
  if (!showChat) {
    document.getElementById('tab-chat')?.classList.add('hidden');
    document.getElementById('panel-chat')?.classList.add('hidden');
  }

  // Both tabs hidden — show the "weird" message.
  if (!showFiles && !showChat) {
    const body = document.getElementById('main-content') || document.body;
    const msg  = document.createElement('div');
    msg.id        = 'weird-message';
    msg.className = 'weird-message';
    msg.innerHTML = `
      <p class="weird-emoji">😶</p>
      <h2 data-i18n="weird_heading">Your environment looks… weird.</h2>
      <p data-i18n="weird_body">Both the Files and Chat tabs have been disabled by your server
      administrator. There's nothing to do here, but at least the connection is encrypted.</p>`;
    body.prepend(msg);
  }

  // ── Tab grid width ────────────────────────────────────────────────────────
  const visibleTabs = [showFiles, showChat, cfg.show_schedule === true].filter(Boolean).length;
  const cols = Array(visibleTabs).fill('1fr').join(' ');
  // Set a CSS custom property on :root — avoids style-src-attr CSP restriction.
  document.documentElement.style.setProperty('--tabs-columns', cols);

  // ── ICE settings panel ────────────────────────────────────────────────────
  const showICE = cfg.show_ice_settings !== false;
  if (!showICE) {
    document.getElementById('ice-settings')?.classList.add('ice-hidden');
  } else {
    if (cfg.allow_stun === false) {
      document.getElementById('ice-stun-add-btn')?.closest('.ice-section')?.classList.add('hidden');
    }
    if (cfg.allow_turn === false) {
      document.getElementById('ice-turn-add-btn')?.closest('.ice-section')?.classList.add('hidden');
    }
  }

  // ── Share link + QR code ──────────────────────────────────────────────────
  if (cfg.show_share_link === false) {
    ['files-share-link', 'chat-share-link'].forEach(id =>
      document.getElementById(id)?.classList.add('hidden'));
  }
  if (cfg.show_qr_code === false) {
    ['files-qr-toggle', 'files-qr-container', 'chat-qr-toggle', 'chat-qr-container'].forEach(id =>
      document.getElementById(id)?.classList.add('hidden'));
  }

  // ── Custom server field ───────────────────────────────────────────────────
  if (cfg.allow_custom_server === false) {
    ['files-server', 'chat-server'].forEach(id =>
      document.getElementById(id)?.closest('.field')?.classList.add('hidden'));
  }

  // ── Max peers slider ──────────────────────────────────────────────────────
  const showPeers  = cfg.show_peers_limit !== false;
  const maxPeers   = typeof cfg.max_peers_limit === 'number' ? cfg.max_peers_limit : 10;
  const peerSlider = document.getElementById('files-max-peers');
  if (!showPeers) peerSlider?.closest('.field')?.classList.add('hidden');
  if (peerSlider) {
    peerSlider.max = String(maxPeers);
    if (parseInt(peerSlider.value) > maxPeers) {
      peerSlider.value = String(maxPeers);
      const label = document.getElementById('files-max-peers-value');
      if (label) label.textContent = String(maxPeers);
    }
  }

  // ── MOTD ──────────────────────────────────────────────────────────────────
  if (cfg.motd && cfg.motd.trim() !== '') {
    const banner = document.createElement('div');
    banner.id        = 'motd-banner';
    banner.className = 'motd-banner';
    banner.textContent = cfg.motd;
    document.body.prepend(banner);
  }

  // ── Language filtering ────────────────────────────────────────────────────
  if (Array.isArray(cfg.allowed_langs) && cfg.allowed_langs.length > 0) {
    const allowed = new Set(cfg.allowed_langs);
    filteredLangs = allLangs.filter(l => allowed.has(l.code));
    if (filteredLangs.length === 0) filteredLangs = allLangs.filter(l => l.code === 'en');
  } else {
    filteredLangs = allLangs;
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

// ── Language detection & switching ─────────────────────────────────────────

// detectLanguage picks the best match from availableLangs using the browser's
// navigator.languages preference list, falling back to 'en'.
function detectLanguage(langs) {
  const codes = langs.map(l => l.code);
  // Honour a previously saved preference first.
  try {
    const saved = localStorage.getItem('gmmff_lang');
    if (saved && codes.includes(saved)) return saved;
  } catch(_) {}
  // Match browser preferences against available codes.
  // Strategy: exact match first (e.g. 'pt-BR'), then base-language
  // match (e.g. 'pt' → first 'pt-*' entry), then fallback to 'en'.
  for (const pref of (navigator.languages || [navigator.language || 'en'])) {
    const lower = pref.toLowerCase();
    // 1. Exact match
    if (codes.includes(lower)) return lower;
    // 2. Exact match case-insensitive (handles 'pt-BR' vs 'pt-br')
    const exact = codes.find(c => c.toLowerCase() === lower);
    if (exact) return exact;
    // 3. Base-language prefix match (e.g. 'pt' matches 'pt-BR')
    const base = lower.split('-')[0];
    const prefix = codes.find(c => c.toLowerCase().startsWith(base + '-') || c.toLowerCase() === base);
    if (prefix) return prefix;
  }
  // Default to English, or first available language.
  return codes.includes('en') ? 'en' : (codes[0] || 'en');
}

// switchLanguage loads a language file, applies it, and re-renders the picker.
async function switchLanguage(code) {
  const strings = await fetch('i18n/' + code + '.json').then(r => r.json());
  currentLang = code;
  // Persist choice across page reloads.
  try { localStorage.setItem('gmmff_lang', code); } catch(_) {}
  applyI18n(strings);
  renderLangPicker();
}

// renderLangPicker builds a <select> dropdown in the footer.
// Hidden when only one language is available.
function renderLangPicker() {
  const el = document.getElementById('lang-picker');
  if (!el) return;
  if (availableLangs.length <= 1) {
    el.style.display = 'none';
    return;
  }
  // Re-use existing <select> if already rendered, just update value.
  let sel = el.querySelector('select');
  if (!sel) {
    sel = document.createElement('select');
    sel.className = 'lang-picker__select';
    sel.setAttribute('aria-label', 'Language');
    availableLangs.forEach(lang => {
      const opt = document.createElement('option');
      opt.value = lang.code;
      opt.textContent = lang.name;
      sel.appendChild(opt);
    });
    sel.addEventListener('change', () => switchLanguage(sel.value));
    el.appendChild(sel);
  }
  sel.value = currentLang;
}

// ── i18n ───────────────────────────────────────────────────────────────────
function applyI18n(strings) {
  i18n = strings;
  ensureChatJoinLink();
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

  // Load and render ICE settings from localStorage.
  loadIceState();
  renderIceLists();

  // Pre-fill the signaling server fields using the current page URL.
  // Converts http(s):// → ws(s):// and appends /ws.
  const serverURL = location.origin.replace(/^http/, 'ws') + '/ws';
  const filesField = document.getElementById('files-server');
  if (filesField && !filesField.value) filesField.value = serverURL;

  // Check for ?code= in the URL — pre-fill the join form.
  checkURLParams();
  // Check for ?type=schedule in the URL.
  if (typeof window.schedHandleURLParams === 'function') window.schedHandleURLParams();
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
    const targetPanel = document.getElementById(btn.getAttribute('aria-controls'));
    targetPanel?.classList.remove('hidden');
    targetPanel?.classList.add('active');
    // Show ICE settings only on Files and Chat tabs.
    const ctrl    = btn.getAttribute('aria-controls');
    const iceEl   = document.getElementById('ice-settings');
    const showICE = uiConfig.show_ice_settings !== false;
    if (iceEl) {
      if (showICE && ctrl !== 'panel-schedule') {
        iceEl.classList.remove('ice-hidden');
      } else {
        iceEl.classList.add('ice-hidden');
      }
    }
    // Pre-fill server fields when switching tabs.
    if (ctrl === 'panel-chat') {
      const sf = document.getElementById('chat-server');
      if (sf && !sf.value) sf.value = normaliseServerURL(location.origin.replace(/^http/, 'ws') + '/ws');
    }
    if (ctrl === 'panel-files') {
      const sf = document.getElementById('files-server');
      if (sf && !sf.value) sf.value = normaliseServerURL(location.origin.replace(/^http/, 'ws') + '/ws');
    }
  });
});

// ── Files session ─────────────────────────────────────────────────────────────

// ── Files state machine ───────────────────────────────────────────────────────
// States: form | code | join | active
function showFilesState(state) {
  ['files-form','files-code','files-join','files-active'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.classList.toggle('hidden', id !== 'files-' + state);
  });
}

let filesIsInitiator = false;
let filesSelectedFiles = [];

function setFilesSelectedFiles(files) {
  filesSelectedFiles = Array.from(files);
  const nameEl = document.getElementById('files-file-name');
  if (!nameEl) return;
  if (filesSelectedFiles.length === 0) {
    nameEl.textContent = t('send_no_file');
    nameEl.classList.remove('has-file');
  } else if (filesSelectedFiles.length === 1) {
    nameEl.textContent = filesSelectedFiles[0].name;
    nameEl.classList.add('has-file');
  } else {
    const first = filesSelectedFiles[0].webkitRelativePath;
    const folder = first ? first.split('/')[0] : null;
    const allSame = folder && filesSelectedFiles.every(f => f.webkitRelativePath?.startsWith(folder + '/'));
    nameEl.textContent = allSame
      ? folder + '/ (' + filesSelectedFiles.length + ' ' + t('files_count') + ')'
      : filesSelectedFiles.length + ' ' + t('files_count');
    nameEl.classList.add('has-file');
  }
  document.getElementById('files-error')?.textContent && (document.getElementById('files-error').textContent = '');
}

// File pickers
const filesFileInput   = document.getElementById('files-file-input');
const filesFolderInput = document.getElementById('files-folder-input');
document.getElementById('files-pick-btn')?.addEventListener('click', () => filesFileInput?.click());
document.getElementById('files-pick-folder-btn')?.addEventListener('click', () => filesFolderInput?.click());
filesFileInput?.addEventListener('change', () => { if (filesFileInput.files.length) setFilesSelectedFiles(filesFileInput.files); });
filesFolderInput?.addEventListener('change', () => { if (filesFolderInput.files.length) setFilesSelectedFiles(filesFolderInput.files); });

// Max peers slider
const maxPeersSlider = document.getElementById('files-max-peers');
const maxPeersValue  = document.getElementById('files-max-peers-value');
maxPeersSlider?.addEventListener('input', () => {
  if (maxPeersValue) maxPeersValue.textContent = maxPeersSlider.value;
});

// Create button
document.getElementById('files-create-btn')?.addEventListener('click', () => {
  const server = normaliseServerURL(document.getElementById('files-server').value.trim());
  const errEl  = document.getElementById('files-error');
  errEl.textContent = '';
  if (!server) { errEl.textContent = t('error_no_server'); return; }
  const maxPeers = parseInt(document.getElementById('files-max-peers')?.value || '2', 10);
  const myNameVal = document.getElementById('files-my-name')?.value.trim();
  if (myNameVal) myName = myNameVal;
  // Disable button immediately to prevent double-click.
  const createBtn = document.getElementById('files-create-btn');
  if (createBtn) { createBtn.disabled = true; createBtn.textContent = t('create_creating') || 'Creating…'; }
  if (typeof window.gmmffCreateSession === 'function') {
    window.gmmffCreateSession(server, maxPeers, buildIceConfig());
  }
});

// Join link (shown below create button)
(function ensureFilesJoinLink() {
  const form = document.getElementById('files-form');
  if (!form || form.querySelector('#files-show-join-btn')) return;
  const btn = document.createElement('button');
  btn.id = 'files-show-join-btn';
  btn.className = 'btn-ghost';
  btn.setAttribute('data-i18n', 'chat_join_link');
  btn.textContent = t('chat_join_link') || 'Join with a code';
  btn.addEventListener('click', () => {
    showFilesState('join');
    document.getElementById('files-join-code')?.focus();
  });
  form.appendChild(btn);
}());

// Join button
document.getElementById('files-join-btn')?.addEventListener('click', () => {
  const code   = document.getElementById('files-join-code')?.value.trim();
  const server = normaliseServerURL(document.getElementById('files-server').value.trim()
    || location.origin.replace(/^http/, 'ws') + '/ws');
  const errEl  = document.getElementById('files-join-error');
  if (errEl) errEl.textContent = '';
  if (!code)   { if (errEl) errEl.textContent = t('error_no_code');   return; }
  if (!server) { if (errEl) errEl.textContent = t('error_no_server'); return; }
  // Capture display name; disable button immediately to prevent double-join.
  const nameVal = document.getElementById('files-join-name')?.value.trim();
  if (nameVal) myName = nameVal;
  const joinBtn = document.getElementById('files-join-btn');
  if (joinBtn) { joinBtn.disabled = true; joinBtn.textContent = t('join_connecting') || 'Connecting…'; }
  if (typeof window.gmmffJoinSession === 'function') {
    window.gmmffJoinSession(code, server, buildIceConfig());
  }
});

// Back button
document.getElementById('files-back-btn')?.addEventListener('click', () => showFilesState('form'));
document.getElementById('files-cancel-code-btn')?.addEventListener('click', () => {
  if (cancel) { cancel(); cancel = null; }
  showFilesState('form');
});

// Copy code
document.getElementById('files-copy-btn')?.addEventListener('click', () => {
  const code = document.getElementById('files-code-value')?.textContent;
  copyToClipboard(code, document.getElementById('files-copy-btn'), t('code_copy'));
});

// Send files button
document.getElementById('files-send-btn')?.addEventListener('click', () => {
  if (filesSelectedFiles.length === 0) return;
  if (typeof window.gmmffSessionSendFiles === 'function') {
    window.gmmffSessionSendFiles(filesSelectedFiles);
    setFilesSelectedFiles([]);
  }
});

// Send message
document.getElementById('files-msg-btn')?.addEventListener('click', sendFilesMessage);
document.getElementById('files-msg-input')?.addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendFilesMessage(); }
});
function sendFilesMessage() {
  const input = document.getElementById('files-msg-input');
  const text  = input?.value.trim();
  if (!text) return;
  if (typeof window.gmmffSessionSendMessage === 'function') window.gmmffSessionSendMessage(text);
  appendFilesMessage('me', myName || 'Me', text);
  input.value = '';
  input.focus();
}

// Close button
document.getElementById('files-close-btn')?.addEventListener('click', () => {
  if (filesIsInitiator && typeof window.gmmffSessionClose === 'function') {
    window.gmmffSessionClose();
  } else if (typeof window.gmmffSessionLeave === 'function') {
    window.gmmffSessionLeave();
  }
  filesDisableInput();
  appendFilesSystem(t('chat_you_left') || 'You left the session.');
});

// ── Files transfer progress bars ──────────────────────────────────────────────
const filesTransferBars = {}; // label → { el, lastProgress }

function getOrCreateTransferBar(label) {
  if (filesTransferBars[label]) return filesTransferBars[label];
  const container = document.getElementById('files-transfers');
  if (!container) return null;
  const wrap = document.createElement('div');
  wrap.className = 'files-transfer';
  wrap.setAttribute('data-label', label);
  wrap.innerHTML = `
    <div class="progress">
      <div class="progress__bar-track" role="progressbar" aria-valuenow="0" aria-valuemin="0" aria-valuemax="100">
        <div class="progress__bar-fill files-bar" id="files-bar-${label.replace(/[^a-z0-9]/gi,'_')}"></div>
      </div>
      <div class="progress__meta">
        <span class="files-bytes" id="files-bytes-${label.replace(/[^a-z0-9]/gi,'_')}"></span>
        <span class="files-speed" id="files-speed-${label.replace(/[^a-z0-9]/gi,'_')}"></span>
      </div>
    </div>`;
  container.appendChild(wrap);
  const entry = { el: wrap, startTime: Date.now(), lastSpeed: 0 };
  filesTransferBars[label] = entry;
  return entry;
}

function removeTransferBar(label) {
  const entry = filesTransferBars[label];
  if (!entry) return;
  entry.el.remove();
  delete filesTransferBars[label];
}

// ── Wasm → JS callbacks ───────────────────────────────────────────────────────

window.uiFilesShowCode = function(code) {
  document.getElementById('files-code-value').textContent = code;
  populateShareLink('files', code);
  showFilesState('code');
};

window.uiFilesSessionReady = function(isInitiator, peerCount, maxPeers) {
  filesIsInitiator = isInitiator;
  peerNames = new Map();
  peerCount = 0;
  if (peerCount && maxPeers) {
    window.uiFilesPeerCount(peerCount, maxPeers);
  }
  // Announce our name to the other side.
  // Send empty string if no name set — receiver will use 'Participant N'.
  setTimeout(() => {
    if (typeof window.gmmffSessionSendMessage === 'function') {
      window.gmmffSessionSendMessage(NAME_PREFIX + (myName || ''));
    }
  }, 300);
  document.getElementById('files-messages').innerHTML = '';
  document.getElementById('files-transfers').innerHTML = '';
  const statusEl = document.getElementById('files-active-status');
  if (statusEl) { statusEl.textContent = t('chat_connected') || 'Connected'; statusEl.style.color = 'var(--color-success)'; }
  document.getElementById('files-send-btn').disabled = false;
  document.getElementById('files-close-btn').classList.remove('hidden');
  showFilesState('active');
  appendFilesSystem(t('files_session_open') || 'Session open. End-to-end encrypted.');
};

window.uiFilesProgress = function(label, pct, done, total) {
  const entry = getOrCreateTransferBar(label);
  if (!entry) return;
  const safeLabel = label.replace(/[^a-z0-9]/gi,'_');
  const bar   = document.getElementById('files-bar-' + safeLabel);
  const bytes = document.getElementById('files-bytes-' + safeLabel);
  const speed = document.getElementById('files-speed-' + safeLabel);
  if (bar)   { bar.style.width = pct + '%'; bar.closest('[role="progressbar"]')?.setAttribute('aria-valuenow', pct); }
  if (bytes) bytes.textContent = t('progress_of', { sent: fmtBytes(done), total: fmtBytes(total) });
  const elapsed = (Date.now() - entry.startTime) / 1000;
  if (speed && elapsed > 0) {
    const bps = done / elapsed;
    entry.lastSpeed = bps;
    const eta = bps > 0 ? (total - done) / bps : 0;
    speed.textContent = fmtBytes(bps) + '/s' + (eta > 0 ? '  ' + fmtEta(eta) : '');
  }
};

window.uiFilesTransferDone = function(label, filename) {
  const entry = filesTransferBars[label];
  if (entry) {
    const safeLabel = label.replace(/[^a-z0-9]/gi,'_');
    const bar = document.getElementById('files-bar-' + safeLabel);
    if (bar) bar.style.width = '100%';
    setTimeout(() => removeTransferBar(label), 2000);
  }
  appendFilesSystem((t('files_transfer_done') || 'Transfer complete') + (filename ? ': ' + filename : ''));
};

window.uiFilesTransferError = function(label, msg) {
  removeTransferBar(label);
  appendFilesSystem('Transfer error: ' + msg);
};

window.uiFilesInboundStarted = function(label, total) {
  getOrCreateTransferBar(label);
  appendFilesSystem(t('files_receiving') || 'Receiving file…');
};

window.uiFilesMessage = function(from, text) {
  if (text.startsWith(ROSTER_PREFIX)) {
    // Roster broadcast from initiator — populate all peer names at once.
    // Format: \x01roster:initiator=FFName,peerID=MobName,...
    const entries = text.slice(ROSTER_PREFIX.length).split(',');
    entries.forEach(entry => {
      const eq = entry.indexOf('=');
      if (eq === -1) return;
      const pid  = entry.slice(0, eq); // 'initiator' or a UUID
      const name = entry.slice(eq + 1).trim() || null;
      if (!peerNames.has(pid)) {
        peerNames.set(pid, name || 'Participant');
      }
    });
    return;
  }
  if (text.startsWith(NAME_PREFIX)) {
    // Name announcement — record name keyed by peer ID (from).
    const announcedName = text.slice(NAME_PREFIX.length).trim();
    if (from && announcedName) {
      peerNames.set(from, announcedName);
      appendFilesSystem(announcedName + ' joined.');
    } else if (from) {
      // No name set — assign a numbered fallback.
      const n = peerNames.size + 1;
      peerNames.set(from, 'Participant ' + n);
      appendFilesSystem('Participant ' + n + ' joined.');
    }
    return;
  }
  // Look up the sender by peer ID; fall back to Participant if unknown.
  const label = (from && peerNames.has(from))
    ? peerNames.get(from)
    : 'Participant';
  appendFilesMessage('them', label, text);
};

window.uiFilesPeerCount = function(peerCount, maxPeers) {
  const el = document.getElementById('files-peer-count');
  if (!el) return;
  el.textContent = peerCount + '/' + maxPeers;
  el.classList.toggle('hidden', peerCount <= 0 || maxPeers <= 0);
};

window.uiFilesParticipantLeft = function(msg, from) {
  const label = (from && peerNames.has(from))
    ? peerNames.get(from)
    : (peerNames.size > 0 ? [...peerNames.values()].at(-1) : 'Participant');
  if (from) peerNames.delete(from);
  appendFilesSystem(msg || (label + ' left.'));
};

window.uiFilesSessionClosed = function(msg) {
  appendFilesSystem(msg || 'Session ended.');
  filesDisableInput();
  // Scroll the message into view so the user sees it.
  const list = document.getElementById('files-messages');
  if (list) list.scrollTop = list.scrollHeight;
  // Update status prominently.
  const statusEl = document.getElementById('files-active-status');
  if (statusEl) {
    statusEl.textContent = t('session_ended') || 'Session ended';
    statusEl.style.color = 'var(--color-error, #e53e3e)';
  }
};

window.uiFilesError = function(msg) {
  // Re-enable create and join buttons so the user can retry.
  const createBtn = document.getElementById('files-create-btn');
  if (createBtn) { createBtn.disabled = false; createBtn.textContent = t('files_create_btn') || 'Start session'; }
  const joinBtn = document.getElementById('files-join-btn');
  if (joinBtn) { joinBtn.disabled = false; joinBtn.textContent = t('chat_join_btn') || 'Join'; }
  const errEl = document.getElementById('files-error');
  if (errEl) errEl.textContent = t('status_error', { message: msg });
  showFilesState('form');
};

function filesDisableInput() {
  const statusEl = document.getElementById('files-active-status');
  if (statusEl) { statusEl.textContent = t('chat_disconnected') || 'Disconnected'; statusEl.style.color = 'var(--color-text-muted)'; }
  const sendBtn  = document.getElementById('files-send-btn');
  const msgBtn   = document.getElementById('files-msg-btn');
  const msgInput = document.getElementById('files-msg-input');
  const closeBtn = document.getElementById('files-close-btn');
  if (sendBtn)  sendBtn.disabled  = true;
  if (msgBtn)   msgBtn.disabled   = true;
  if (msgInput) msgInput.disabled = true;
  if (closeBtn) closeBtn.classList.add('hidden');
}

function appendFilesMessage(side, from, text) {
  const list = document.getElementById('files-messages');
  if (!list) return;
  const wrap = document.createElement('div');
  wrap.className = 'chat-bubble chat-bubble--' + side;
  if (side === 'them') {
    const meta = document.createElement('div');
    meta.className = 'chat-bubble__meta';
    meta.textContent = from;
    wrap.appendChild(meta);
  }
  const body = document.createElement('div');
  body.textContent = text;
  wrap.appendChild(body);
  list.appendChild(wrap);
  list.scrollTop = list.scrollHeight;
}

function appendFilesSystem(text) {
  const list = document.getElementById('files-messages');
  if (!list) return;
  const el = document.createElement('div');
  el.className = 'chat-system';
  el.textContent = text;
  list.appendChild(el);
  list.scrollTop = list.scrollHeight;
}

// Drag and drop for files panel — set filesSelectedFiles

// ── Drag and drop ────────────────────────────────────────────────────────────
(function initDragAndDrop() {
  const overlay = document.getElementById('drop-overlay');
  let dragDepth = 0;

  window.addEventListener('dragenter', e => {
    if (!e.dataTransfer?.types?.includes('Files')) return;
    e.preventDefault();
    dragDepth++;
    overlay.classList.remove('hidden');
  });

  window.addEventListener('dragleave', () => {
    dragDepth--;
    if (dragDepth <= 0) {
      dragDepth = 0;
      overlay.classList.add('hidden');
    }
  });

  window.addEventListener('dragover', e => {
    if (!e.dataTransfer?.types?.includes('Files')) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });

  window.addEventListener('drop', e => {
    e.preventDefault();
    dragDepth = 0;
    overlay.classList.add('hidden');

    const droppedFiles = e.dataTransfer?.files;
    if (!droppedFiles?.length) return;

    // Switch to Files tab if not already active.
    const filesTab = document.getElementById('tab-files');
    if (filesTab?.getAttribute('aria-selected') !== 'true') filesTab?.click();

    // Populate the file picker.
    setFilesSelectedFiles(droppedFiles);
  });
}());

// ── Progress / formatting helpers ────────────────────────────────────────────
function fmtElapsed(seconds) {
  if (!seconds || seconds < 0) return '';
  const m = Math.floor(seconds / 60);
  const s = Math.round(seconds % 60);
  if (m > 0) return m + 'm ' + s + 's';
  return s + 's';
}

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

// ── Wasm callbacks shared by all panels ──────────────────────────────────────
window.uiRegisterCancel = function(fn) { cancel = fn; };

// ── Chat UI ──────────────────────────────────────────────────────────────────

function showChatState(state) {
  ['chat-form','chat-code','chat-join','chat-active'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.classList.toggle('hidden', id !== 'chat-' + state);
  });
}

// Start session button
document.getElementById('chat-start-btn')?.addEventListener('click', () => {
  const server    = normaliseServerURL(document.getElementById('chat-server').value.trim());
  const nameVal   = document.getElementById('chat-my-name')?.value.trim();
  const errEl     = document.getElementById('chat-error');
  errEl.textContent = '';
  if (!server) { errEl.textContent = t('error_no_server'); return; }
  if (nameVal) myName = nameVal;
  if (typeof window.gmmffChat === 'function') window.gmmffChat(server, buildIceConfig());
});

// Dynamically add "Join with a code" link below Start button
function ensureChatJoinLink() {
  const form = document.getElementById('chat-form');
  if (!form || form.querySelector('#chat-show-join-btn')) return;
  const btn = document.createElement('button');
  btn.id = 'chat-show-join-btn';
  btn.className = 'btn-ghost';
  btn.setAttribute('data-i18n', 'chat_join_link');
  btn.textContent = t('chat_join_link') || 'Join with a code';
  btn.addEventListener('click', () => {
    showChatState('join');
    document.getElementById('chat-join-code')?.focus();
  });
  form.appendChild(btn);
}

document.getElementById('chat-back-btn')?.addEventListener('click', () => showChatState('form'));

document.getElementById('chat-cancel-code-btn')?.addEventListener('click', () => {
  if (cancel) { cancel(); cancel = null; }
  showChatState('form');
});

document.getElementById('chat-copy-btn')?.addEventListener('click', () => {
  const code = document.getElementById('chat-code-value')?.textContent;
  copyToClipboard(code, document.getElementById('chat-copy-btn'), t('code_copy'));
});

document.getElementById('chat-join-btn')?.addEventListener('click', () => {
  const code   = document.getElementById('chat-join-code').value.trim();
  const server = normaliseServerURL(document.getElementById('chat-server').value.trim()
    || location.origin.replace(/^http/, 'ws') + '/ws');
  const errEl  = document.getElementById('chat-join-error');
  errEl.textContent = '';
  if (!code)   { errEl.textContent = t('error_no_code');   return; }
  if (!server) { errEl.textContent = t('error_no_server'); return; }
  const chatNameVal = document.getElementById('chat-join-name')?.value.trim();
  if (chatNameVal) myName = chatNameVal;
  const chatJoinBtn = document.getElementById('chat-join-btn');
  if (chatJoinBtn) { chatJoinBtn.disabled = true; chatJoinBtn.textContent = t('join_connecting') || 'Connecting…'; }
  if (typeof window.gmmffChatJoin === 'function') window.gmmffChatJoin(code, server, buildIceConfig());
});

document.getElementById('chat-send-btn')?.addEventListener('click', sendChatMessage);
document.getElementById('chat-input')?.addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendChatMessage(); }
});

function sendChatMessage() {
  const input = document.getElementById('chat-input');
  const text  = input?.value.trim();
  if (!text) return;
  if (text === '\\q') {
    input.value = '';
    if (typeof window.gmmffChatQuit === 'function') window.gmmffChatQuit();
    return;
  }
  if (typeof window.gmmffChatSend === 'function') window.gmmffChatSend(text);
  appendChatBubble('me', myName || 'Me', text);
  input.value = '';
  input.focus();
}

document.getElementById('chat-close-btn')?.addEventListener('click', () => {
  if (typeof window.gmmffChatLeave === 'function') window.gmmffChatLeave();
  appendChatSystem(t('chat_you_left') || 'You left the session.');
  chatDisableInput();
});

function chatDisableInput() {
  const statusEl = document.getElementById('chat-active-status');
  if (statusEl) {
    statusEl.textContent = t('chat_disconnected') || 'Disconnected';
    statusEl.style.color = 'var(--color-text-muted)';
  }
  const sendBtn  = document.getElementById('chat-send-btn');
  const input    = document.getElementById('chat-input');
  const closeBtn = document.getElementById('chat-close-btn');
  if (sendBtn)  sendBtn.disabled = true;
  if (input)    input.disabled = true;
  if (closeBtn) closeBtn.classList.add('hidden');
}

function appendChatBubble(side, from, text) {
  const list = document.getElementById('chat-messages');
  if (!list) return;
  const wrap = document.createElement('div');
  wrap.className = 'chat-bubble chat-bubble--' + side;
  if (side === 'them') {
    const meta = document.createElement('div');
    meta.className = 'chat-bubble__meta';
    meta.textContent = from;
    wrap.appendChild(meta);
  }
  const body = document.createElement('div');
  body.textContent = text;
  wrap.appendChild(body);
  list.appendChild(wrap);
  list.scrollTop = list.scrollHeight;
}

function appendChatSystem(text) {
  const list = document.getElementById('chat-messages');
  if (!list) return;
  const el = document.createElement('div');
  el.className = 'chat-system';
  el.textContent = text;
  list.appendChild(el);
  list.scrollTop = list.scrollHeight;
}

// ── Chat Wasm → JS callbacks ──────────────────────────────────────────────────

window.uiChatShowCode = function(code) {
  document.getElementById('chat-code-value').textContent = code;
  populateShareLink('chat', code);
  showChatState('code');
};

window.uiChatOpen = function(remoteRole) {
  document.getElementById('chat-messages').innerHTML = '';
  // Announce our name.
  setTimeout(() => {
    if (typeof window.gmmffChatSend === 'function') {
      window.gmmffChatSend(NAME_PREFIX + myName);
    }
  }, 300);
  const statusEl = document.getElementById('chat-active-status');
  statusEl.textContent = t('chat_connected') || 'Connected';
  statusEl.style.color = 'var(--color-success)';
  document.getElementById('chat-send-btn').disabled = false;
  document.getElementById('chat-input').disabled = false;
  document.getElementById('chat-close-btn').classList.remove('hidden');
  showChatState('active');
  document.getElementById('chat-input').focus();
  appendChatSystem(t('chat_session_open') || 'Session open. Messages are end-to-end encrypted.');
};

window.uiChatMessage = function(from, text) {
  if (text.startsWith(NAME_PREFIX)) {
    const announcedName = text.slice(NAME_PREFIX.length).trim() || 'Participant';
    peerNames.set(1, announcedName);
    appendChatSystem(announcedName + ' joined.');
    return;
  }
  const label = peerNames.get(1) || 'Participant';
  appendChatBubble('them', label, text);
};

window.uiChatClosed = function(reason) {
  appendChatSystem(reason);
  chatDisableInput();
};

window.uiChatParticipantLeft = function(who) {
  const label = peerNames.get(1) || 'Participant';
  appendChatSystem((who || label) + ' left the session.');
};

window.uiChatError = function(message) {
  document.getElementById('chat-error').textContent = t('status_error', { message });
  showChatState('form');
};

// ── ICE settings ─────────────────────────────────────────────────────────────

const ICE_STORAGE_KEY = 'gmmff_ice_config';
const ICE_TTL_MS      = 7 * 24 * 60 * 60 * 1000; // 7 days

let iceState = { stun: [], turn: [] };

function loadIceState() {
  try {
    const raw = localStorage.getItem(ICE_STORAGE_KEY);
    if (!raw) return;
    const saved = JSON.parse(raw);
    if (!saved.expiry || Date.now() > saved.expiry) {
      localStorage.removeItem(ICE_STORAGE_KEY);
      return;
    }
    iceState = { stun: saved.stun || [], turn: saved.turn || [] };
  } catch(_) {}
}

function saveIceState() {
  try {
    localStorage.setItem(ICE_STORAGE_KEY, JSON.stringify({
      stun:   iceState.stun,
      turn:   iceState.turn,
      expiry: Date.now() + ICE_TTL_MS,
    }));
  } catch(_) {}
}

function buildIceConfig() {
  return { stun: [...iceState.stun], turn: [...iceState.turn] };
}

document.getElementById('ice-toggle')?.addEventListener('click', () => {
  const btn  = document.getElementById('ice-toggle');
  const body = document.getElementById('ice-settings-body');
  const open = btn.getAttribute('aria-expanded') === 'true';
  btn.setAttribute('aria-expanded', open ? 'false' : 'true');
  body.classList.toggle('hidden', open);
});

function renderIceLists() {
  renderIceList('ice-stun-list', iceState.stun, 'stun');
  renderIceList('ice-turn-list', iceState.turn, 'turn');
}

function renderIceList(listId, items, type) {
  const ul = document.getElementById(listId);
  if (!ul) return;
  ul.innerHTML = '';
  if (type === 'stun') {
    ul.appendChild(makeIceItem('stun:stun.l.google.com:19302', null, true));
  }
  items.forEach((url, idx) => {
    ul.appendChild(makeIceItem(url, () => removeIceEntry(type, idx)));
  });
}

function makeIceItem(url, onRemove, isDefault = false) {
  const li  = document.createElement('li');
  li.className = 'ice-list__item' + (isDefault ? ' ice-list__item--default' : '');
  const span = document.createElement('span');
  span.className = 'ice-list__url';
  span.textContent = url;
  span.title = url;
  li.appendChild(span);
  const btn = document.createElement('button');
  btn.className = 'ice-list__remove';
  btn.textContent = '×';
  btn.setAttribute('aria-label', t('ice_remove_btn') || 'Remove');
  if (isDefault) {
    btn.disabled = true;
  } else {
    btn.addEventListener('click', onRemove);
  }
  li.appendChild(btn);
  return li;
}

function removeIceEntry(type, idx) {
  iceState[type].splice(idx, 1);
  saveIceState();
  renderIceLists();
}

function promptIceAdd(type) {
  const label = type === 'stun'
    ? (t('ice_stun_prompt') || 'Enter STUN URL (e.g. stun:host:3478)')
    : (t('ice_turn_prompt') || 'Enter TURN URL (e.g. turn:host:3478?user=u&pass=p)');
  const val = window.prompt(label);
  if (!val || !val.trim()) return;
  const url = val.trim();
  if (type === 'stun' && !url.startsWith('stun:') && !url.startsWith('stuns:')) {
    alert(t('ice_stun_invalid') || 'URL must start with stun: or stuns:');
    return;
  }
  if (type === 'turn' && !url.startsWith('turn:') && !url.startsWith('turns:')) {
    alert(t('ice_turn_invalid') || 'URL must start with turn: or turns:');
    return;
  }
  if (iceState[type].length >= 3) {
    alert(t('ice_limit') || 'Maximum 3 servers per type.');
    return;
  }
  iceState[type].push(url);
  saveIceState();
  renderIceLists();
}

document.getElementById('ice-stun-add-btn')?.addEventListener('click', () => promptIceAdd('stun'));
document.getElementById('ice-turn-add-btn')?.addEventListener('click', () => promptIceAdd('turn'));

document.getElementById('ice-reset-btn')?.addEventListener('click', () => {
  iceState = { stun: [], turn: [] };
  try { localStorage.removeItem(ICE_STORAGE_KEY); } catch(_) {}
  renderIceLists();
});

// ── Share link & QR code ─────────────────────────────────────────────────────

// populateShareLink builds the share URL, fills the URL display, and generates QR.
function populateShareLink(panel, code) {
  const type  = panel === 'chat' ? 'chat' : 'files';
  const url   = location.origin + location.pathname
    + '?code=' + encodeURIComponent(code) + '&type=' + type;
  const urlEl = document.getElementById(panel + '-share-url');
  const qrEl  = document.getElementById(panel + '-qr-code');
  if (urlEl) {
    urlEl.textContent = url;
    urlEl.title       = url;
    urlEl.onclick     = () => copyToClipboard(url, urlEl, t('share_copy_url'));
  }
  if (qrEl && typeof QRCode !== 'undefined') {
    qrEl.innerHTML = '';
    new QRCode(qrEl, {
      text:         url,
      width:        180,
      height:       180,
      colorDark:    '#000000',
      colorLight:   '#ffffff',
      correctLevel: QRCode.CorrectLevel.M,
    });
  }
}

// Copy URL buttons
document.getElementById('files-copy-url-btn')?.addEventListener('click', () => {
  const url = document.getElementById('files-share-url')?.textContent;
  if (url) copyToClipboard(url, document.getElementById('files-copy-url-btn'), t('share_copy_url'));
});
document.getElementById('chat-copy-url-btn')?.addEventListener('click', () => {
  const url = document.getElementById('chat-share-url')?.textContent;
  if (url) copyToClipboard(url, document.getElementById('chat-copy-url-btn'), t('share_copy_url'));
});

// QR toggle buttons
function wireQRToggle(panel) {
  const btn       = document.getElementById(panel + '-qr-toggle');
  const container = document.getElementById(panel + '-qr-container');
  if (!btn || !container) return;
  btn.addEventListener('click', () => {
    const nowHidden = container.classList.toggle('hidden');
    btn.textContent = nowHidden ? t('share_show_qr') : t('share_hide_qr');
  });
}
wireQRToggle('files');
wireQRToggle('chat');

// Generic clipboard helper with temporary button label feedback
async function copyToClipboard(text, btn, originalLabel) {
  try {
    await navigator.clipboard.writeText(text);
    if (btn) {
      const orig = btn.textContent;
      btn.textContent = t('code_copied') || 'Copied!';
      setTimeout(() => { btn.textContent = orig; }, 2000);
    }
  } catch(_) {}
}

// ── URL parameter detection ───────────────────────────────────────────────────

// checkURLParams reads ?code=, ?type=, ?local=, and ?autoconnect= on load.
// Hides the Chat tab in local mode, and auto-fires the join in autoconnect mode.
function checkURLParams() {
  const params      = new URLSearchParams(location.search);
  const code        = params.get('code');
  const type_       = params.get('type') || 'files';
  const isLocal     = params.get('local') === '1';
  const autoconnect = params.get('autoconnect') === '1';

  // Hide Chat tab and ICE settings in local mode (Files only, no external servers).
  if (isLocal) {
    document.getElementById('tab-chat')?.classList.add('hidden');
    document.getElementById('panel-chat')?.classList.add('hidden');
    document.getElementById('ice-settings')?.classList.add('ice-hidden');
    document.getElementById('tab-files')?.click();
  }

  // Remove URL params so they don't persist on refresh.
  history.replaceState({}, '', location.origin + location.pathname);

  if (!code) return;

  if (type_ === 'chat' && !isLocal) {
    document.getElementById('tab-chat')?.click();
    const input = document.getElementById('chat-join-code');
    if (input) input.value = code;
    if (autoconnect) {
      const server = normaliseServerURL(location.origin.replace(/^http/, 'ws') + '/ws');
      if (typeof window.gmmffChatJoin === 'function') window.gmmffChatJoin(code, server, buildIceConfig());
    } else {
      showChatState('join');
      document.getElementById('chat-join-code')?.focus();
    }
  } else {
    document.getElementById('tab-files')?.click();
    const input = document.getElementById('files-join-code');
    if (input) input.value = code;
    if (autoconnect) {
      // Show the code screen briefly so the user sees something while connecting.
      document.getElementById('files-join-code').value = code;
      showFilesState('join');
      // Small delay so the UI renders before Wasm starts.
      const server = normaliseServerURL(location.origin.replace(/^http/, 'ws') + '/ws');
      setTimeout(() => {
        if (typeof window.gmmffJoinSession === 'function') {
          window.gmmffJoinSession(code, server, buildIceConfig());
        }
      }, 100);
    } else {
      showFilesState('join');
      document.getElementById('files-join-code')?.focus();
    }
  }
}

// ── Go ────────────────────────────────────────────────────────────────────────
boot();

// ════════════════════════════════════════════════════════════════════════════
// SCHEDULE FEATURE
// Client-side AES-256-GCM encrypted file upload/download.
// The server never sees plaintext. The decryption key lives in the URL
// fragment (#key=...) which is never sent to the server.
// ════════════════════════════════════════════════════════════════════════════

(function() {
'use strict';

// ── Constants ────────────────────────────────────────────────────────────────
const SCHED_CHUNK_SIZE = 256 * 1024; // 256 KiB — matches server ChunkSize in store.go
const SCHED_NONCE_SIZE = 12;
const SCHED_TAG_SIZE   = 16;

// ── State ─────────────────────────────────────────────────────────────────────
let schedState      = 'landing'; // landing | create | success | join
let schedPassword   = '';        // entered upload password (if required)
let schedCryptoKey  = null;      // CryptoKey for current upload/download
let schedUploadCtrl = null;      // AbortController for in-progress upload
let schedTTLOptions = [];        // [{label, seconds}] from /api/schedule/ttl-options

// ── Boot: show/hide tab based on config ───────────────────────────────────────
function schedInit(cfg) {
  if (!cfg.show_schedule) return;

  const tab = document.getElementById('tab-schedule');
  if (tab) tab.classList.remove('hidden');

  // Load TTL options.
  fetch('/api/schedule/ttl-options')
    .then(r => r.json())
    .then(opts => {
      schedTTLOptions = opts;
      const list    = document.getElementById('schedule-ttl-list');
      const hidden  = document.getElementById('schedule-ttl');
      const btnLabel = document.getElementById('schedule-ttl-label');
      if (!list || !hidden || !btnLabel) return;

      list.innerHTML = '';
      opts.forEach((o, i) => {
        const li = document.createElement('li');
        li.role = 'option';
        li.className = 'custom-select__item' + (i === 0 ? ' selected' : '');
        li.dataset.value = o.seconds;
        li.textContent = o.label;
        li.addEventListener('click', () => {
          list.querySelectorAll('.custom-select__item').forEach(el => el.classList.remove('selected'));
          li.classList.add('selected');
          hidden.value    = o.seconds;
          btnLabel.textContent = li.textContent;
          schedCloseDropdown();
          schedUpdateExpiresHint();
        });
        list.appendChild(li);
      });

      // Set initial value.
      if (opts.length > 0) {
        hidden.value      = opts[0].seconds;
        btnLabel.textContent = list.querySelector('.custom-select__item')?.textContent || opts[0].label;
        schedUpdateExpiresHint();
      }
    })
    .catch(() => {});

  bindScheduleEvents();
}

// ── Bind all schedule DOM events ─────────────────────────────────────────────
function bindScheduleEvents() {

  // Tab click.
  document.getElementById('tab-schedule')?.addEventListener('click', () => schedShowTab());

  // Password gate.
  document.getElementById('schedule-password-btn')?.addEventListener('click', schedPasswordSubmit);
  document.getElementById('schedule-password-input')?.addEventListener('keydown', e => {
    if (e.key === 'Enter') schedPasswordSubmit();
  });

  // Landing buttons.
  document.getElementById('schedule-create-btn')?.addEventListener('click', schedClickCreate);
  document.getElementById('schedule-join-btn')?.addEventListener('click',   () => schedSetState('join'));

  // Create — file pickers.
  document.getElementById('schedule-file-btn')?.addEventListener('click',
    () => document.getElementById('schedule-file-input').click());
  document.getElementById('schedule-folder-btn')?.addEventListener('click',
    () => document.getElementById('schedule-folder-input').click());
  document.getElementById('schedule-file-input')?.addEventListener('change',   schedFileChosen);
  document.getElementById('schedule-folder-input')?.addEventListener('change', schedFileChosen);

  // TTL custom dropdown toggle.
  document.getElementById('schedule-ttl-btn')?.addEventListener('click', schedToggleDropdown);
  document.addEventListener('click', e => {
    if (!document.getElementById('schedule-ttl-wrap')?.contains(e.target)) schedCloseDropdown();
  });

  // Upload.
  document.getElementById('schedule-upload-btn')?.addEventListener('click', schedStartUpload);
  document.getElementById('schedule-create-back-btn')?.addEventListener('click', () => schedSetState('landing'));

  // Success.
  document.getElementById('schedule-copy-url-btn')?.addEventListener('click',    () => schedCopyField('schedule-share-url'));
  document.getElementById('schedule-copy-delete-btn')?.addEventListener('click', () => schedCopyField('schedule-delete-url'));
  document.getElementById('schedule-qr-toggle')?.addEventListener('click',       schedToggleQR);
  document.getElementById('schedule-success-back-btn')?.addEventListener('click', schedResetCreate);

  // Join / download.
  document.getElementById('schedule-download-btn')?.addEventListener('click', schedStartDownload);
  document.getElementById('schedule-join-back-btn')?.addEventListener('click', () => schedSetState('landing'));
  document.getElementById('schedule-join-url')?.addEventListener('input', () => {
    document.getElementById('schedule-join-error').textContent = '';
    // Auto-parse URL from fragment.
    schedAutoFillFromURL();
  });
}

// ── Tab activation ────────────────────────────────────────────────────────────
function schedShowTab() {
  // Deactivate other tabs — same pattern as the main tab handler.
  document.querySelectorAll('.tab').forEach(t => t.setAttribute('aria-selected', 'false'));
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  document.getElementById('tab-schedule').setAttribute('aria-selected', 'true');
  document.getElementById('panel-schedule').classList.add('active');
  // Hide ICE settings — not relevant for schedule transfers.
  document.getElementById('ice-settings')?.classList.add('ice-hidden');

  // Check auth status.
  schedCheckAuth();
}

// ── Auth check ────────────────────────────────────────────────────────────────
function schedCheckAuth() {
  fetch('/api/schedule/auth', { method: 'POST' })
    .then(r => r.json())
    .then(data => {
      if (data.needs_password) {
        schedSetState('password');
      } else {
        schedSetState('landing');
        schedAutoFillFromURL();
      }
    })
    .catch(() => schedSetState('landing'));
}

// ── Password gate ─────────────────────────────────────────────────────────────
function schedPasswordSubmit() {
  const pw  = document.getElementById('schedule-password-input')?.value.trim();
  const err = document.getElementById('schedule-password-error');
  if (!pw) { if (err) err.textContent = t('schedule_password_required') || 'Password required.'; return; }
  // Verify password by attempting upload init with it.
  fetch('/api/schedule/upload/init', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Schedule-Password': pw },
    body: JSON.stringify({ password: pw, chunks_total: 1, total_size: 1, ttl_seconds: 3600, max_downloads: 1 }),
  }).then(r => {
    if (r.status === 403) {
      if (err) err.textContent = t('schedule_password_wrong') || 'Incorrect password.';
    } else {
      // Password accepted — we don't actually need this init slot, but we confirmed auth.
      schedPassword = pw;
      schedSetState('landing');
    }
  }).catch(() => {
    if (err) err.textContent = t('error_connection') || 'Connection failed.';
  });
}

// ── UI state machine ──────────────────────────────────────────────────────────
function schedSetState(state) {
  schedState = state;
  const ids = {
    landing:  'schedule-landing',
    create:   'schedule-create',
    success:  'schedule-success',
    join:     'schedule-join',
    password: 'schedule-password-gate',
  };
  Object.entries(ids).forEach(([s, id]) => {
    const el = document.getElementById(id);
    if (!el) return;
    if (s === state) {
      el.classList.remove('hidden');
    } else {
      el.classList.add('hidden');
    }
  });
}

function schedClickCreate() {
  schedSetState('create');
  schedResetProgress();
}

function schedResetCreate() {
  schedSetState('landing');
  schedCryptoKey = null;
  const fi = document.getElementById('schedule-file-input');
  const fo = document.getElementById('schedule-folder-input');
  if (fi) fi.value = '';
  if (fo) fo.value = '';
  document.getElementById('schedule-file-name').textContent = t('send_no_file') || 'No file chosen';
  document.getElementById('schedule-create-error').textContent = '';
  schedResetProgress();
}

function schedResetProgress() {
  const p = document.getElementById('schedule-progress');
  if (p) p.classList.add('hidden');
  schedUpdateProgressBar(0, null, null, null, null);
}

// ── File picker ───────────────────────────────────────────────────────────────
let schedSelectedFiles = []; // File[]

function schedFileChosen(e) {
  const files = Array.from(e.target.files || []);
  if (!files.length) return;
  schedSelectedFiles = files;
  const name = files.length === 1 ? files[0].name : `${files.length} ${t('files_count') || 'files'}`;
  document.getElementById('schedule-file-name').textContent = name;
  document.getElementById('schedule-create-error').textContent = '';
}

// ── TTL / expires hint ────────────────────────────────────────────────────────
function schedUpdateExpiresHint() {
  const sel     = document.getElementById('schedule-ttl');
  const hint    = document.getElementById('schedule-expires-hint');
  if (!sel || !hint) return;
  const seconds = parseInt(sel.value, 10);
  if (isNaN(seconds)) return;
  const expires = new Date(Date.now() + seconds * 1000);
  hint.textContent = (t('schedule_expires_at') || 'Expires:') + ' ' +
    expires.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

// ── Upload ────────────────────────────────────────────────────────────────────
async function schedStartUpload() {
  const errEl = document.getElementById('schedule-create-error');
  errEl.textContent = '';

  if (!schedSelectedFiles.length) {
    errEl.textContent = t('error_no_file') || 'Please choose a file.';
    return;
  }

  const ttl       = parseInt(document.getElementById('schedule-ttl')?.value || '3600', 10);
  const maxDlRaw  = parseInt(document.getElementById('schedule-max-dl')?.value || '1', 10);
  const maxDl     = isNaN(maxDlRaw) ? 1 : maxDlRaw;
  const uploadBtn = document.getElementById('schedule-upload-btn');
  if (uploadBtn) { uploadBtn.disabled = true; uploadBtn.textContent = t('schedule_uploading') || 'Uploading…'; }

  try {
    // ── 1. Collect file data ──────────────────────────────────────────────────
    let plainBytes;
    let fileName;
    if (schedSelectedFiles.length === 1 && !schedSelectedFiles[0].webkitRelativePath) {
      plainBytes = new Uint8Array(await schedSelectedFiles[0].arrayBuffer());
      fileName   = schedSelectedFiles[0].name;
    } else {
      if (uploadBtn) uploadBtn.textContent = t('schedule_preparing') || 'Preparing…';
      const { bytes, name } = await schedZipFiles(schedSelectedFiles);
      plainBytes = bytes;
      fileName   = name;
    }

    const totalSize = plainBytes.length;

    // ── 2. Probe upload speed and select chunk size ───────────────────────────
    // Skip probe for small files — just use minimum chunk size.
    let chunkSizeToUse = 128 * 1024; // default: minimum
    if (totalSize >= PROBE_THRESHOLD) {
      if (uploadBtn) uploadBtn.textContent = t('schedule_estimating') || 'Estimating…';
      schedUploadCtrl = new AbortController();
      try {
        const speedBps = await schedProbeSpeed(schedUploadCtrl.signal);
        const selected = schedSelectChunkSize(speedBps);
        chunkSizeToUse = selected.chunkSize;
        console.debug(`[schedule] probe speed: ${(speedBps/1024).toFixed(0)} KB/s → chunk size: ${selected.label}`);
      } catch (probeErr) {
        if (probeErr.name === 'AbortError') throw probeErr;
        // Probe failed — fall back to conservative default, don't abort upload.
        console.warn('[schedule] speed probe failed, using 256 KB chunks:', probeErr.message);
        chunkSizeToUse = 256 * 1024;
      }
    }

    if (uploadBtn) uploadBtn.textContent = t('schedule_uploading') || 'Uploading…';

    // ── 3. Generate AES-256-GCM key ──────────────────────────────────────────
    schedCryptoKey = await crypto.subtle.generateKey(
      { name: 'AES-GCM', length: 256 }, true, ['encrypt', 'decrypt']
    );
    const rawKey     = await crypto.subtle.exportKey('raw', schedCryptoKey);
    const keyHex     = bufToHex(rawKey);

    // ── 3. Encrypt filename ───────────────────────────────────────────────────
    const fnNonce    = crypto.getRandomValues(new Uint8Array(SCHED_NONCE_SIZE));
    const fnEnc      = await crypto.subtle.encrypt(
      { name: 'AES-GCM', iv: fnNonce }, schedCryptoKey,
      new TextEncoder().encode(fileName)
    );
    const fileNameEnc   = bufToHex(fnEnc);
    const fileNameNonce = bufToHex(fnNonce);

    // ── 4. Calculate chunks using probed chunk size ───────────────────────────
    const chunksTotal = Math.ceil(totalSize / chunkSizeToUse);

    // ── 5. Init upload on server ──────────────────────────────────────────────
    const initResp = await schedFetch('/api/schedule/upload/init', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(schedPassword ? { 'X-Schedule-Password': schedPassword } : {}),
      },
      body: JSON.stringify({
        password:     schedPassword,
        chunks_total: chunksTotal,
        total_size:   totalSize,
        chunk_size:   chunkSizeToUse,
        ttl_seconds:  ttl,
        max_downloads: maxDl,
      }),
    });
    if (!initResp.ok) {
      const e = await initResp.json();
      throw new Error(e.error || 'Upload init failed');
    }
    const { upload_id: uploadID } = await initResp.json();

    // ── 6. Encrypt all chunks in parallel, then upload sequentially ──────────
    // Encryption is parallelised for speed (especially on mobile CPUs where
    // AES-GCM on large blocks is slow). Uploads are sequential because the
    // server enforces strict chunk ordering — it rejects chunk N+1 before N
    // is written. Encrypting ahead means the network is never idle waiting for
    // the CPU: by the time chunk i finishes uploading, chunk i+1 is already
    // encrypted and ready to send.
    const ENCRYPT_AHEAD = 3; // how many chunks to pre-encrypt ahead of uploads

    document.getElementById('schedule-progress').classList.remove('hidden');
    const noncePrefix = crypto.getRandomValues(new Uint8Array(8));
    let uploadedBytes = 0;
    let startTime     = Date.now();

    // SHA-256 of the full ciphertext (computed in order as chunks are uploaded).
    const cipherParts = new Array(chunksTotal);

    schedUploadCtrl = new AbortController();

    // Encrypt-ahead queue: a sliding window of pre-encrypted chunks.
    // encQueue[i] is a Promise<{chunk, plainLen}> for chunk i.
    const encQueue = [];
    let encNext = 0;

    function enqueueEncryption() {
      while (encQueue.length < ENCRYPT_AHEAD && encNext < chunksTotal) {
        const i     = encNext++;
        const start = i * chunkSizeToUse;
        const end   = Math.min(start + chunkSizeToUse, totalSize);
        const plain = plainBytes.slice(start, end);

        const nonce = new Uint8Array(SCHED_NONCE_SIZE);
        const dv    = new DataView(nonce.buffer);
        dv.setUint32(0, i, false);
        nonce.set(noncePrefix, 4);

        encQueue.push(
          crypto.subtle.encrypt(
            { name: 'AES-GCM', iv: nonce, tagLength: 128 }, schedCryptoKey, plain
          ).then(cipher => {
            const c = new Uint8Array(SCHED_NONCE_SIZE + cipher.byteLength);
            c.set(nonce, 0);
            c.set(new Uint8Array(cipher), SCHED_NONCE_SIZE);
            return { chunk: c, plainLen: end - start };
          })
        );
      }
    }

    // Upload each chunk in strict order, filling the encrypt-ahead queue as we go.
    for (let i = 0; i < chunksTotal; i++) {
      enqueueEncryption();
      const { chunk, plainLen } = await encQueue.shift();
      cipherParts[i] = chunk;

      const resp = await fetch('/api/schedule/upload/chunk?' +
        new URLSearchParams({ upload_id: uploadID, chunk_index: String(i) }), {
        method:  'POST',
        body:    chunk,
        signal:  schedUploadCtrl.signal,
        headers: { 'Content-Type': 'application/octet-stream' },
      });
      if (!resp.ok) {
        const e = await resp.json().catch(() => ({}));
        throw new Error(e.error || `Chunk ${i} upload failed`);
      }

      uploadedBytes += plainLen;
      const elapsed = (Date.now() - startTime) / 1000;
      const speed   = elapsed > 0 ? uploadedBytes / elapsed : 0;
      const eta     = speed > 0 ? (totalSize - uploadedBytes) / speed : null;
      schedUpdateProgressBar(uploadedBytes / totalSize, speed, eta, uploadedBytes, totalSize);
    }

    // ── 7. Compute SHA-256 of full ciphertext ─────────────────────────────────
    const totalCipherLen = cipherParts.reduce((s, c) => s + c.length, 0);
    const fullCipher     = new Uint8Array(totalCipherLen);
    let offset = 0;
    for (const part of cipherParts) { fullCipher.set(part, offset); offset += part.length; }
    const sha256Buf = await crypto.subtle.digest('SHA-256', fullCipher);
    const sha256Hex = bufToHex(sha256Buf);

    // ── 8. Finalize upload ────────────────────────────────────────────────────
    const finResp = await schedFetch('/api/schedule/upload/complete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        upload_id:      uploadID,
        filename_enc:   fileNameEnc,
        filename_nonce: fileNameNonce,
        sha256_cipher:  sha256Hex,
      }),
    });
    if (!finResp.ok) {
      const e = await finResp.json();
      throw new Error(e.error || 'Finalize failed');
    }
    const { file_id: fileID, delete_key: deleteKey, expires_at: expiresAt } = await finResp.json();

    // ── 9. Build share URLs ───────────────────────────────────────────────────
    const base      = location.origin + location.pathname;
    const shareURL  = `${base}?type=schedule&id=${fileID}#key=${keyHex}`;
    const deleteURL = `${base}?type=schedule&id=${fileID}&action=delete&dk=${deleteKey}`;

    document.getElementById('schedule-share-url').value  = shareURL;
    document.getElementById('schedule-delete-url').value = deleteURL;

    const expDate = new Date(expiresAt);
    document.getElementById('schedule-expires-label').textContent =
      (t('schedule_expires_at') || 'Expires:') + ' ' +
      expDate.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });

    schedSetState('success');

  } catch (err) {
    if (err.name === 'AbortError') {
      errEl.textContent = t('status_cancelled_local') || 'Upload cancelled.';
    } else {
      errEl.textContent = err.message || (t('status_error') || 'Upload error.');
    }
    if (uploadBtn) { uploadBtn.disabled = false; uploadBtn.textContent = t('schedule_upload_btn') || 'Upload'; }
  } finally {
    schedUploadCtrl = null;
  }
}

// ── Download + Decrypt ────────────────────────────────────────────────────────
async function schedStartDownload() {
  const urlInput = document.getElementById('schedule-join-url');
  const errEl    = document.getElementById('schedule-join-error');
  errEl.textContent = '';

  const raw = urlInput?.value.trim();
  if (!raw) {
    errEl.textContent = t('schedule_join_url_required') || 'Please paste the share URL.';
    return;
  }

  let fileID, keyHex;
  try {
    const parsed = schedParseShareURL(raw);
    fileID = parsed.fileID;
    keyHex = parsed.keyHex;
  } catch (e) {
    errEl.textContent = e.message;
    return;
  }

  const dlBtn = document.getElementById('schedule-download-btn');
  if (dlBtn) { dlBtn.disabled = true; dlBtn.textContent = t('schedule_downloading') || 'Downloading…'; }
  document.getElementById('schedule-dl-progress').classList.remove('hidden');

  try {
    // ── 1. Fetch file metadata ────────────────────────────────────────────────
    const metaResp = await fetch(`/api/schedule/meta/${fileID}`);
    if (!metaResp.ok) {
      const e = await metaResp.json().catch(() => ({}));
      throw new Error(e.error || 'File not found or expired.');
    }
    const meta = await metaResp.json();

    // ── 2. Import CryptoKey from hex ──────────────────────────────────────────
    const keyBytes  = hexToBuf(keyHex);
    const cryptoKey = await crypto.subtle.importKey(
      'raw', keyBytes, { name: 'AES-GCM' }, false, ['decrypt']
    );

    // ── 3. Download ciphertext ────────────────────────────────────────────────
    const dlResp = await fetch(`/api/schedule/download/${fileID}`);
    if (!dlResp.ok) {
      const e = await dlResp.json().catch(() => ({}));
      throw new Error(e.error || 'Download failed.');
    }

    const reader       = dlResp.body.getReader();
    const encSize      = parseInt(dlResp.headers.get('Content-Length') || '0', 10);
    const chunksTotal  = parseInt(dlResp.headers.get('X-Chunks-Total') || '0', 10);
    const fnEnc        = dlResp.headers.get('X-Filename-Enc')   || '';
    const fnNonce      = dlResp.headers.get('X-Filename-Nonce') || '';
    const encChunkSize = SCHED_NONCE_SIZE + meta.chunk_size + SCHED_TAG_SIZE;

    // Read full ciphertext into buffer (streaming decrypt per chunk below).
    const cipherBuf = new Uint8Array(encSize);
    let received = 0;
    const startTime = Date.now();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      cipherBuf.set(value, received);
      received += value.length;
      const speed = received / ((Date.now() - startTime) / 1000);
      schedUpdateDLProgress(received / encSize, speed, received, encSize);
    }

    // ── 4. Decrypt filename ───────────────────────────────────────────────────
    let fileName = 'download';
    if (fnEnc && fnNonce) {
      try {
        const fnDec = await crypto.subtle.decrypt(
          { name: 'AES-GCM', iv: hexToBuf(fnNonce) }, cryptoKey, hexToBuf(fnEnc)
        );
        fileName = new TextDecoder().decode(fnDec);
      } catch { /* fallback to 'download' */ }
    }

    // ── 5. Decrypt chunks ─────────────────────────────────────────────────────
    const plainParts = [];
    for (let i = 0; i < chunksTotal; i++) {
      const start = i * encChunkSize;
      const end   = Math.min(start + encChunkSize, cipherBuf.length);
      const chunk = cipherBuf.slice(start, end);

      const nonce  = chunk.slice(0, SCHED_NONCE_SIZE);
      const cipher = chunk.slice(SCHED_NONCE_SIZE);

      const plain = await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv: nonce, tagLength: 128 }, cryptoKey, cipher
      );
      plainParts.push(new Uint8Array(plain));
    }

    // ── 6. Reassemble and trigger download ────────────────────────────────────
    const totalPlain = plainParts.reduce((s, p) => s + p.length, 0);
    const plainBuf   = new Uint8Array(totalPlain);
    let off = 0;
    for (const p of plainParts) { plainBuf.set(p, off); off += p.length; }

    const blob = new Blob([plainBuf]);
    const url  = URL.createObjectURL(blob);
    const a    = document.createElement('a');
    a.href     = url;
    a.download = fileName;
    document.body.appendChild(a);
    a.click();
    setTimeout(() => { URL.revokeObjectURL(url); a.remove(); }, 1000);

    schedUpdateDLProgress(1, 0, encSize, encSize);

  } catch (err) {
    errEl.textContent = err.message || (t('status_error') || 'Download error.');
  } finally {
    if (dlBtn) { dlBtn.disabled = false; dlBtn.textContent = t('schedule_download_btn') || 'Download & Decrypt'; }
  }
}

// ── Auto-download from URL params (?type=schedule&id=X#key=Y&dl=1) ────────────
function schedHandleURLParams() {
  const params = new URLSearchParams(location.search);
  if (params.get('type') !== 'schedule') return;

  const fileID = params.get('id');
  const action = params.get('action');
  const dk     = params.get('dk');
  const dl     = params.get('dl') === '1';
  const keyHex = location.hash.startsWith('#key=') ? location.hash.slice(5) : '';

  // Clean URL.
  history.replaceState({}, '', location.origin + location.pathname);

  if (!fileID) return;

  // Delete action.
  if (action === 'delete' && dk) {
    schedHandleDeleteURL(fileID, dk);
    return;
  }

  // Activate schedule tab.
  document.getElementById('tab-schedule')?.click();

  if (keyHex) {
    const shareURL = `${location.origin}${location.pathname}?type=schedule&id=${fileID}#key=${keyHex}`;
    const inp = document.getElementById('schedule-join-url');
    if (inp) inp.value = shareURL;
    schedSetState('join');
    if (dl) {
      setTimeout(schedStartDownload, 200);
    }
  }
}

function schedHandleDeleteURL(fileID, dk) {
  fetch(`/api/schedule/delete/${fileID}/${dk}`, { method: 'DELETE' })
    .then(r => r.json())
    .then(d => {
      document.getElementById('tab-schedule')?.click();
      schedSetState('landing');
      // Show a brief system notice in the landing area.
      const msg = d.deleted
        ? (t('schedule_deleted') || 'File deleted successfully.')
        : (t('schedule_delete_failed') || 'Could not delete file.');
      const el = document.getElementById('schedule-landing');
      if (el) {
        const note = document.createElement('p');
        note.className = 'status';
        note.textContent = msg;
        el.prepend(note);
        setTimeout(() => note.remove(), 5000);
      }
    })
    .catch(() => {});
}

function schedAutoFillFromURL() {
  const raw = document.getElementById('schedule-join-url')?.value.trim();
  if (!raw) return;
  try {
    schedParseShareURL(raw); // validates format
    document.getElementById('schedule-join-error').textContent = '';
  } catch { /* ignore — user still typing */ }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function schedParseShareURL(raw) {
  // Accept full URL or just "id=X#key=Y" fragment.
  let urlStr = raw;
  if (!raw.startsWith('http')) urlStr = location.origin + '/' + raw;

  // Extract fragment before URL constructor strips it.
  const hashIdx = urlStr.indexOf('#');
  const keyHex  = hashIdx !== -1 && urlStr.slice(hashIdx + 1).startsWith('key=')
    ? urlStr.slice(hashIdx + 5)
    : '';

  const url    = new URL(hashIdx !== -1 ? urlStr.slice(0, hashIdx) : urlStr);
  const fileID = url.searchParams.get('id');

  if (!fileID) throw new Error(t('schedule_invalid_url') || 'Invalid share URL — missing file ID.');
  if (!keyHex) throw new Error(t('schedule_invalid_url_key') || 'Invalid share URL — missing decryption key.');
  if (!/^[0-9a-f]+$/i.test(keyHex)) throw new Error(t('schedule_invalid_key') || 'Invalid decryption key format.');

  return { fileID, keyHex };
}

async function schedZipFiles(files) {
  // Minimal ZIP builder — stores files without compression.
  // Uses the File System Access API naming if available, else flat names.
  const entries = [];
  for (const f of files) {
    const name  = f.webkitRelativePath || f.name;
    const bytes = new Uint8Array(await f.arrayBuffer());
    entries.push({ name, bytes });
  }

  // Build a ZIP with stored (no compression) entries.
  const parts = [];
  const cds   = []; // central directory entries
  let offset  = 0;

  for (const { name, bytes } of entries) {
    const nameBytes = new TextEncoder().encode(name);
    const crc       = crc32(bytes);
    const lf        = buildLocalFileHeader(nameBytes, bytes.length, crc);
    parts.push(lf, bytes);
    cds.push(buildCentralDirEntry(nameBytes, bytes.length, crc, offset));
    offset += lf.length + bytes.length;
  }

  const cdBuf  = concat(cds);
  const eocd   = buildEOCD(entries.length, cdBuf.length, offset);
  const zip    = concat([...parts, cdBuf, eocd]);
  const baseName = files[0].webkitRelativePath
    ? files[0].webkitRelativePath.split('/')[0]
    : files[0].name.replace(/\.[^.]+$/, '');
  return { bytes: zip, name: baseName + '.zip' };
}

// Minimal ZIP helpers (STORE, no compression).
function buildLocalFileHeader(nameBytes, size, crc) {
  const buf = new ArrayBuffer(30 + nameBytes.length);
  const v   = new DataView(buf);
  v.setUint32(0,  0x504b0304, false); // signature
  v.setUint16(4,  20, true);          // version needed
  v.setUint16(6,  0,  true);          // flags
  v.setUint16(8,  0,  true);          // compression: STORE
  v.setUint16(10, 0,  true);          // mod time
  v.setUint16(12, 0,  true);          // mod date
  v.setUint32(14, crc,  true);        // CRC-32
  v.setUint32(18, size, true);        // compressed size
  v.setUint32(22, size, true);        // uncompressed size
  v.setUint16(26, nameBytes.length, true);
  v.setUint16(28, 0, true);           // extra field length
  new Uint8Array(buf).set(nameBytes, 30);
  return new Uint8Array(buf);
}

function buildCentralDirEntry(nameBytes, size, crc, offset) {
  const buf = new ArrayBuffer(46 + nameBytes.length);
  const v   = new DataView(buf);
  v.setUint32(0,  0x504b0102, false); // signature
  v.setUint16(4,  20, true);          // version made by
  v.setUint16(6,  20, true);          // version needed
  v.setUint16(8,  0,  true);          // flags
  v.setUint16(10, 0,  true);          // compression: STORE
  v.setUint16(12, 0,  true);          // mod time
  v.setUint16(14, 0,  true);          // mod date
  v.setUint32(16, crc,  true);
  v.setUint32(20, size, true);
  v.setUint32(24, size, true);
  v.setUint16(28, nameBytes.length, true);
  v.setUint16(30, 0, true); // extra
  v.setUint16(32, 0, true); // comment
  v.setUint16(34, 0, true); // disk start
  v.setUint16(36, 0, true); // internal attr
  v.setUint32(38, 0, true); // external attr
  v.setUint32(42, offset, true);
  new Uint8Array(buf).set(nameBytes, 46);
  return new Uint8Array(buf);
}

function buildEOCD(count, cdSize, cdOffset) {
  const buf = new ArrayBuffer(22);
  const v   = new DataView(buf);
  v.setUint32(0, 0x504b0506, false);
  v.setUint16(4, 0, true);
  v.setUint16(6, 0, true);
  v.setUint16(8, count, true);
  v.setUint16(10, count, true);
  v.setUint32(12, cdSize,   true);
  v.setUint32(16, cdOffset, true);
  v.setUint16(20, 0, true);
  return new Uint8Array(buf);
}

function crc32(data) {
  let crc = 0xFFFFFFFF;
  for (const b of data) {
    crc ^= b;
    for (let k = 0; k < 8; k++) {
      crc = (crc & 1) ? (crc >>> 1) ^ 0xEDB88320 : crc >>> 1;
    }
  }
  return (crc ^ 0xFFFFFFFF) >>> 0;
}

function concat(arrays) {
  const len = arrays.reduce((s, a) => s + a.length, 0);
  const out = new Uint8Array(len);
  let off = 0;
  for (const a of arrays) { out.set(a, off); off += a.length; }
  return out;
}

function bufToHex(buf) {
  return Array.from(new Uint8Array(buf)).map(b => b.toString(16).padStart(2,'0')).join('');
}

function hexToBuf(hex) {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i++) bytes[i] = parseInt(hex.slice(i*2, i*2+2), 16);
  return bytes;
}

function schedFetch(url, opts) {
  return fetch(url, opts);
}

// ── Speed probe & adaptive chunk sizing ───────────────────────────────────────

// PROBE_THRESHOLD: files smaller than this skip the probe and use min chunk size.
const PROBE_THRESHOLD = 2 * 1024 * 1024; // 2 MB

// Tier table: [minSpeedBytesPerSec, chunkSizeBytes, label]
const CHUNK_TIERS = [
  [10 * 1024 * 1024, 2 * 1024 * 1024, '2 MB — gigabit / fast LAN'],
  [ 2 * 1024 * 1024, 1 * 1024 * 1024, '1 MB — fast connection'],
  [      500 * 1024,      512 * 1024,  '512 KB — home broadband / good WiFi'],
  [      200 * 1024,      256 * 1024,  '256 KB — moderate connection'],
  [               0,      128 * 1024,  '128 KB — slow / mobile connection'],
];

/**
 * Runs two probe uploads (1 MB weight=2, 512 KB weight=1) to the /api/schedule/probe
 * endpoint and returns the weighted average speed in bytes/sec.
 * The probe endpoint discards data immediately — nothing is written to disk.
 */
async function schedProbeSpeed(signal) {
  const probes = [
    { size: 1 * 1024 * 1024, weight: 2 },
    { size:      512 * 1024, weight: 1 },
  ];

  let weightedSum  = 0;
  let totalWeights = 0;

  for (const { size, weight } of probes) {
    // crypto.getRandomValues is limited to 65536 bytes per call — fill in 64 KB batches.
    const data  = new Uint8Array(size);
    const batch = 65536;
    for (let off = 0; off < size; off += batch) {
      crypto.getRandomValues(data.subarray(off, Math.min(off + batch, size)));
    }
    const start = performance.now();

    const resp = await fetch('/api/schedule/probe', {
      method:  'POST',
      body:    data,
      signal,
      headers: { 'Content-Type': 'application/octet-stream' },
    });
    if (!resp.ok) throw new Error('Probe failed');
    await resp.json(); // drain response

    const elapsedSec = (performance.now() - start) / 1000;
    const speed      = size / elapsedSec; // bytes/sec

    weightedSum  += speed * weight;
    totalWeights += weight;
  }

  return weightedSum / totalWeights; // weighted average bytes/sec
}

/**
 * Select chunk size from the tier table based on measured speed.
 * Returns { chunkSize, label }.
 */
function schedSelectChunkSize(speedBytesPerSec) {
  for (const [minSpeed, chunkSize, label] of CHUNK_TIERS) {
    if (speedBytesPerSec >= minSpeed) return { chunkSize, label };
  }
  return { chunkSize: 128 * 1024, label: '128 KB — slow connection' };
}

function schedToggleDropdown() {
  const wrap = document.getElementById('schedule-ttl-wrap');
  const btn  = document.getElementById('schedule-ttl-btn');
  const list = document.getElementById('schedule-ttl-list');
  if (!wrap) return;
  const open = wrap.classList.toggle('open');
  btn?.setAttribute('aria-expanded', String(open));
  if (open) list?.querySelector('.selected')?.scrollIntoView({ block: 'nearest' });
}

function schedCloseDropdown() {
  const wrap = document.getElementById('schedule-ttl-wrap');
  const btn  = document.getElementById('schedule-ttl-btn');
  wrap?.classList.remove('open');
  btn?.setAttribute('aria-expanded', 'false');
}

function schedCopyField(id) {
  const el = document.getElementById(id);
  if (!el) return;
  el.select();
  navigator.clipboard?.writeText(el.value).catch(() => document.execCommand('copy'));
  el.blur();
}

function schedToggleQR() {
  const container = document.getElementById('schedule-qr-container');
  const btn       = document.getElementById('schedule-qr-toggle');
  if (!container) return;
  const hidden = container.classList.toggle('hidden');
  if (btn) btn.textContent = hidden ? (t('share_show_qr') || 'Show QR') : (t('share_hide_qr') || 'Hide QR');
  if (!hidden && container.children.length === 0) {
    const url = document.getElementById('schedule-share-url')?.value;
    if (url && typeof QRCode !== 'undefined') {
      new QRCode(container, { text: url, width: 200, height: 200, correctLevel: QRCode.CorrectLevel.M });
    }
  }
}

function schedUpdateProgressBar(pct, speed, eta, done, total) {
  const fill = document.getElementById('schedule-progress-fill');
  if (fill) fill.style.width = Math.round(pct * 100) + '%';
  const pctEl = document.getElementById('schedule-progress-pct');
  if (pctEl) pctEl.textContent = Math.round(pct * 100) + '%';
  const speedEl = document.getElementById('schedule-progress-speed');
  if (speedEl && speed !== null) speedEl.textContent = formatBytes(speed) + '/s';
  const etaEl = document.getElementById('schedule-progress-eta');
  if (etaEl && eta !== null) etaEl.textContent = eta > 0 ? 'ETA ' + formatETA(eta) : '';
  const bytesEl = document.getElementById('schedule-progress-bytes');
  if (bytesEl && done !== null) bytesEl.textContent = formatBytes(done) + ' / ' + formatBytes(total);
}

function schedUpdateDLProgress(pct, speed, done, total) {
  const fill = document.getElementById('schedule-dl-progress-fill');
  if (fill) fill.style.width = Math.round(pct * 100) + '%';
  const pctEl = document.getElementById('schedule-dl-progress-pct');
  if (pctEl) pctEl.textContent = Math.round(pct * 100) + '%';
  const speedEl = document.getElementById('schedule-dl-progress-speed');
  if (speedEl && speed > 0) speedEl.textContent = formatBytes(speed) + '/s';
  const bytesEl = document.getElementById('schedule-dl-progress-bytes');
  if (bytesEl) bytesEl.textContent = formatBytes(done) + ' / ' + formatBytes(total);
}

function formatBytes(n) {
  if (n >= 1<<30) return (n/(1<<30)).toFixed(1) + ' GB';
  if (n >= 1<<20) return (n/(1<<20)).toFixed(1) + ' MB';
  if (n >= 1<<10) return (n/(1<<10)).toFixed(1) + ' KB';
  return n + ' B';
}

function formatETA(secs) {
  if (secs >= 3600) return Math.round(secs/3600) + 'h';
  if (secs >= 60)   return Math.round(secs/60)   + 'm';
  return Math.round(secs) + 's';
}

// ── Expose boot hook for main app.js ─────────────────────────────────────────
window.schedInit          = schedInit;
window.schedHandleURLParams = schedHandleURLParams;

}()); // end IIFE
