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

  // Pre-fill the signaling server fields using the current page URL.
  // Converts http(s):// → ws(s):// and appends /ws.
  const serverURL = location.origin.replace(/^http/, 'ws') + '/ws';
  const sendField    = document.getElementById('send-server');
  const receiveField = document.getElementById('receive-server');
  if (sendField    && !sendField.value)    sendField.value    = serverURL;
  if (receiveField && !receiveField.value) receiveField.value = serverURL;
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
    // Auto-focus the code input when switching to the Receive tab.
    if (btn.getAttribute('aria-controls') === 'panel-receive') {
      const codeInput = document.getElementById('receive-code');
      if (codeInput && !codeInput.value) codeInput.focus();
    }
    // Pre-fill chat server field when switching to Chat tab.
    if (btn.getAttribute('aria-controls') === 'panel-chat') {
      const sf = document.getElementById('chat-server');
      if (sf && !sf.value) sf.value = normaliseServerURL(location.origin.replace(/^http/, 'ws') + '/ws');
    }
  });
});

// ── File picker ───────────────────────────────────────────────────────────────
// selectedFiles accumulates File objects from both pickers and drag-and-drop.
// It is always a plain Array (not a FileList) for easy manipulation.
let selectedFiles = [];

function setSelectedFiles(files) {
  selectedFiles = Array.from(files);
  const nameEl = document.getElementById('send-file-name');
  if (selectedFiles.length === 0) {
    nameEl.textContent = t('send_no_file');
    nameEl.classList.remove('has-file');
  } else if (selectedFiles.length === 1) {
    nameEl.textContent = selectedFiles[0].name;
    nameEl.classList.add('has-file');
  } else {
    // Show folder name if all share a common prefix, otherwise count.
    const first = selectedFiles[0].webkitRelativePath;
    const folder = first ? first.split('/')[0] : null;
    const allSame = folder && selectedFiles.every(f => f.webkitRelativePath?.startsWith(folder + '/'));
    nameEl.textContent = allSame
      ? folder + '/ (' + selectedFiles.length + ' ' + t('files_count') + ')'
      : selectedFiles.length + ' ' + t('files_count');
    nameEl.classList.add('has-file');
  }
  document.getElementById('send-error').textContent = '';
}

const fileInput    = document.getElementById('send-file-input');
const folderInput  = document.getElementById('send-folder-input');
const pickBtn      = document.getElementById('send-pick-btn');
const pickFolderBtn = document.getElementById('send-pick-folder-btn');

pickBtn.addEventListener('click', () => fileInput.click());
pickFolderBtn.addEventListener('click', () => folderInput.click());

fileInput.addEventListener('change', () => {
  if (fileInput.files.length > 0) setSelectedFiles(fileInput.files);
});
folderInput.addEventListener('change', () => {
  if (folderInput.files.length > 0) setSelectedFiles(folderInput.files);
});

// ── Drag and drop ────────────────────────────────────────────────────────────
(function initDragAndDrop() {
  const overlay  = document.getElementById('drop-overlay');
  const picker   = document.querySelector('.file-picker');
  let dragDepth  = 0; // track enter/leave pairs across child elements

  // Show overlay when a file enters the window.
  window.addEventListener('dragenter', e => {
    if (!e.dataTransfer?.types?.includes('Files')) return;
    e.preventDefault();
    dragDepth++;
    overlay.classList.remove('hidden');
  });

  window.addEventListener('dragleave', e => {
    dragDepth--;
    if (dragDepth <= 0) {
      dragDepth = 0;
      overlay.classList.add('hidden');
    }
  });

  // Must prevent default on dragover to allow drop.
  window.addEventListener('dragover', e => {
    if (!e.dataTransfer?.types?.includes('Files')) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });

  window.addEventListener('drop', e => {
    e.preventDefault();
    dragDepth = 0;
    overlay.classList.add('hidden');
    if (picker) picker.classList.remove('drag-over');

    const droppedFiles = e.dataTransfer?.files;
    if (!droppedFiles?.length) return;

    // Switch to Send tab if not already active.
    const sendTab = document.getElementById('tab-send');
    if (sendTab?.getAttribute('aria-selected') !== 'true') sendTab?.click();

    // Populate via setSelectedFiles — works for single or multiple dropped files.
    setSelectedFiles(droppedFiles);
  });
}());

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
  const server = normaliseServerURL(document.getElementById('send-server').value.trim());
  const errEl  = document.getElementById('send-error');

  errEl.textContent = '';
  if (selectedFiles.length === 0) { errEl.textContent = t('error_no_file');   return; }
  if (!server)                    { errEl.textContent = t('error_no_server'); return; }

  // Hand off to Go/Wasm — selectedFiles is a plain JS Array.
  if (typeof window.gmmffSend === 'function') {
    window.gmmffSend(selectedFiles, server);
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
  if (!lastProgress.send.startTime) lastProgress.send.startTime = Date.now();
  lastProgress.send.total = totalBytes;
  lastProgress.send.speed = speed;
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
  if (!lastProgress.receive.startTime) lastProgress.receive.startTime = Date.now();
  lastProgress.receive.total = totalBytes;
  lastProgress.receive.speed = speed;
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
  const lp = lastProgress[panel];
  const elapsedSec = lp.startTime ? (Date.now() - lp.startTime) / 1000 : 0;

  // Lock the bar at 100%.
  const barId  = panel === 'send' ? 'send-bar'  : 'receive-bar';
  const bar    = document.getElementById(barId);
  if (bar) {
    bar.style.width = '100%';
    bar.closest('[role="progressbar"]')?.setAttribute('aria-valuenow', 100);
  }

  // Completion summary below the bar:
  //   left:  "X MB of X MB"  (total of total — both equal at 100%)
  //   right: last known speed
  const bytesId = panel === 'send' ? 'send-progress-bytes'  : 'receive-progress-bytes';
  const spdId   = panel === 'send' ? 'send-progress-speed'  : 'receive-progress-speed';
  const bytesEl = document.getElementById(bytesId);
  const spdEl   = document.getElementById(spdId);
  if (bytesEl && lp.total > 0) {
    const totalStr = fmtBytes(lp.total);
    const timeStr  = fmtElapsed(elapsedSec);
    bytesEl.textContent = totalStr + ' of ' + totalStr + ' — ' + timeStr;
  }
  if (spdEl && lp.speed > 0) {
    spdEl.textContent = fmtBytes(lp.speed) + '/s';
  }

  // Status message (kept as-is per spec).
  window.uiStatusRaw(message || t(panel === 'send' ? 'status_done_send' : 'status_done_receive'),
                     'success', panel);

  // Hide cancel button.
  const cancelBtn = panel === 'send'
    ? document.getElementById('send-cancel-progress-btn')
    : document.getElementById('receive-cancel-btn');
  if (cancelBtn) cancelBtn.classList.add('hidden');

  // Reset state for a potential next transfer.
  lastProgress[panel] = { total: 0, speed: 0, startTime: null };
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
// fmtElapsed formats a completed duration as "Xm Ys" or just "Ys".
// Per spec: only show minutes if the transfer took >= 60 seconds.
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

// ── Chat UI ──────────────────────────────────────────────────────────────────

// ── Chat state machine ────────────────────────────────────────────────────────
// States: idle | code | join | active
function showChatState(state) {
  ['chat-form','chat-code','chat-join','chat-active'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.classList.toggle('hidden', id !== 'chat-' + state);
  });
}

// ── Start session button ──────────────────────────────────────────────────────
document.getElementById('chat-start-btn')?.addEventListener('click', () => {
  const server = normaliseServerURL(document.getElementById('chat-server').value.trim());
  const errEl  = document.getElementById('chat-error');
  errEl.textContent = '';
  if (!server) { errEl.textContent = t('error_no_server'); return; }
  if (typeof window.gmmffChat === 'function') window.gmmffChat(server);
});

// Show join form
document.getElementById('chat-form')?.addEventListener('dblclick', () => showChatState('join'));

// A separate 'Join' button below the start button (added via a listener)
// We add a join button to chat-form dynamically after i18n loads:
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

// ── Back button ───────────────────────────────────────────────────────────────
document.getElementById('chat-back-btn')?.addEventListener('click', () => {
  showChatState('form');
});

// ── Cancel during code display ────────────────────────────────────────────────
document.getElementById('chat-cancel-code-btn')?.addEventListener('click', () => {
  if (cancel) { cancel(); cancel = null; }
  showChatState('form');
});

// ── Copy chat code ────────────────────────────────────────────────────────────
document.getElementById('chat-copy-btn')?.addEventListener('click', async () => {
  const code = document.getElementById('chat-code-value')?.textContent;
  const btn  = document.getElementById('chat-copy-btn');
  try {
    await navigator.clipboard.writeText(code);
    btn.textContent = t('code_copied');
    setTimeout(() => { btn.textContent = t('code_copy'); }, 2000);
  } catch(_) {}
});

// ── Join with code ────────────────────────────────────────────────────────────
document.getElementById('chat-join-btn')?.addEventListener('click', () => {
  const code   = document.getElementById('chat-join-code').value.trim();
  const server = normaliseServerURL(document.getElementById('chat-server').value.trim()
    || location.origin.replace(/^http/, 'ws') + '/ws');
  const errEl  = document.getElementById('chat-join-error');
  errEl.textContent = '';
  if (!code)   { errEl.textContent = t('error_no_code');   return; }
  if (!server) { errEl.textContent = t('error_no_server'); return; }
  if (typeof window.gmmffChatJoin === 'function') window.gmmffChatJoin(code, server);
});

// ── Send chat message ─────────────────────────────────────────────────────────
document.getElementById('chat-send-btn')?.addEventListener('click', sendChatMessage);
document.getElementById('chat-input')?.addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendChatMessage(); }
});

function sendChatMessage() {
  const input = document.getElementById('chat-input');
  const text  = input?.value.trim();
  if (!text) return;
  // Initiator typing \q ends the session for everyone.
  // Responder typing \q leaves quietly.
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

// ── Close chat ───────────────────────────────────────────────────────────────
document.getElementById('chat-close-btn')?.addEventListener('click', () => {
  if (typeof window.gmmffChatLeave === 'function') window.gmmffChatLeave();
  appendChatSystem(t('chat_you_left') || 'You left the session.');
  chatDisableInput();
});

// ── Bubble helpers ────────────────────────────────────────────────────────────
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

// ── Wasm → JS callbacks ───────────────────────────────────────────────────────

window.uiChatShowCode = function(code) {
  document.getElementById('chat-code-value').textContent = code;
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
  // Always display as 'Participant' regardless of underlying role.
  // Future: 'Participant 1', 'Participant 2', etc.
  appendChatBubble('them', t('chat_participant') || 'Participant', text);
};

// uiChatClosed — called when session ends for everyone (TagChatClose).
window.uiChatClosed = function(reason) {
  appendChatSystem(reason);
  chatDisableInput();
};

// uiChatParticipantLeft — called when a participant leaves quietly (TagParticipantLeave).
// Input stays enabled for the local user (they remain in the session).
window.uiChatParticipantLeft = function(who) {
  appendChatSystem((who || 'Participant') + ' left the session.');
  // Do NOT disable input — local user stays connected.
};

window.uiChatError = function(message) {
  document.getElementById('chat-error').textContent = t('status_error', { message });
  showChatState('form');
};

// ── Go ────────────────────────────────────────────────────────────────────────
boot();
