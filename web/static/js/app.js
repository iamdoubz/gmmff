'use strict';

// ── Config ──────────────────────────────────────────────────────────────────
const THEME_URL      = 'themes/default.json';
const LANGUAGES_URL  = 'i18n/languages.json';
const WASM_URL       = 'gmmff.wasm';

// ── State ────────────────────────────────────────────────────────────────────
let currentLang    = 'en';
let availableLangs = [];

// Track the last known progress values for each panel so uiDone can
// snapshot them for the completion summary line.
const lastProgress = {
  send:    { total: 0, speed: 0, startTime: null },
  receive: { total: 0, speed: 0, startTime: null },
};

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
    const [theme, langs] = await Promise.all([
      fetch(THEME_URL).then(r => r.json()),
      fetch(LANGUAGES_URL).then(r => r.json()),
    ]);
    applyTheme(theme);
    availableLangs = langs;
    currentLang = detectLanguage(langs);
    await switchLanguage(currentLang);
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
    // Pre-fill server fields when switching tabs.
    const ctrl = btn.getAttribute('aria-controls');
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
  appendFilesMessage('me', 'You', text);
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
  if (peerCount && maxPeers) {
    window.uiFilesPeerCount(peerCount, maxPeers);
  }
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
  appendFilesMessage('them', t('chat_participant') || 'Participant', text);
};

window.uiFilesPeerCount = function(peerCount, maxPeers) {
  const el = document.getElementById('files-peer-count');
  if (!el) return;
  el.textContent = peerCount + '/' + maxPeers;
  el.classList.toggle('hidden', peerCount <= 0 || maxPeers <= 0);
};

window.uiFilesParticipantLeft = function(msg) {
  appendFilesSystem(msg);
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
  const server = normaliseServerURL(document.getElementById('chat-server').value.trim());
  const errEl  = document.getElementById('chat-error');
  errEl.textContent = '';
  if (!server) { errEl.textContent = t('error_no_server'); return; }
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
  appendChatBubble('me', 'You', text);
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
  appendChatBubble('them', t('chat_participant') || 'Participant', text);
};

window.uiChatClosed = function(reason) {
  appendChatSystem(reason);
  chatDisableInput();
};

window.uiChatParticipantLeft = function(who) {
  appendChatSystem((who || 'Participant') + ' left the session.');
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

  // Hide Chat tab in local mode (Files only).
  if (isLocal) {
    const chatTab   = document.getElementById('tab-chat');
    const chatPanel = document.getElementById('panel-chat');
    if (chatTab)   chatTab.style.display   = 'none';
    if (chatPanel) chatPanel.style.display = 'none';
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
