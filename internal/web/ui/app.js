// Emcomm Objects browser UI. Vanilla JS, no build step. All state lives on
// the server; this is a thin, event-driven view (SSE keeps it in sync).
'use strict';

// ---------- API ----------

async function jsonOrThrow(r) {
  const text = await r.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* not JSON */ }
  if (!r.ok) throw new Error((data && data.error) || `HTTP ${r.status}`);
  return data;
}
const req = (method, url, body) => fetch(url, {
  method, headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
  body: body !== undefined ? JSON.stringify(body) : undefined,
}).then(jsonOrThrow);
const enc = encodeURIComponent;
const API = {
  objects: () => req('GET', '/api/objects'),
  upsert: (name, body) => req('PUT', `/api/objects/${enc(name)}`, body),
  remove: (name) => req('DELETE', `/api/objects/${enc(name)}`),
  beacon: (name) => req('POST', `/api/objects/${enc(name)}/beacon`),
  beaconAll: () => req('POST', '/api/objects/beacon-all'),
  kill: (name) => req('POST', `/api/objects/${enc(name)}/kill`),
  revive: (name) => req('POST', `/api/objects/${enc(name)}/revive`),
  status: () => req('GET', '/api/status'),
  config: () => req('GET', '/api/config'),
  saveConfig: (c) => req('PUT', '/api/config', c),
  gwTest: (c) => req('POST', '/api/graywolf/test', c),
  plTest: (c) => req('POST', '/api/planner/test', c),
  plImport: (c) => req('POST', '/api/planner/import-objects', c),
  logs: () => req('GET', '/api/logs'),
  update: () => req('GET', '/api/update'),
  updateCheck: () => req('POST', '/api/update/check'),
  updateApply: () => req('POST', '/api/update/apply'),
  updateSkip: (v) => req('POST', '/api/update/skip', { version: v }),
  version: () => req('GET', '/api/version'),
  quit: () => req('POST', '/api/quit'),
};

// ---------- DOM helpers ----------

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k === 'dataset') Object.assign(e.dataset, v);
    else if (k === 'html') e.innerHTML = v;
    else if (k.startsWith('on') && typeof v === 'function') e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else if (v !== false && v != null) e.setAttribute(k, v);
  }
  for (const c of children) {
    if (c == null) continue;
    e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return e;
}

const ICONS = {
  tx: '<svg viewBox="0 0 24 24"><path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8zm0 2.5a1.5 1.5 0 1 1 0 3 1.5 1.5 0 0 1 0-3zM4.9 4.9 6.3 6.3A8 8 0 0 0 6.3 17.7l-1.4 1.4A10 10 0 0 1 4.9 4.9zm14.2 0a10 10 0 0 1 0 14.2l-1.4-1.4a8 8 0 0 0 0-11.4l1.4-1.4zM7.8 7.8l1.4 1.4a4 4 0 0 0 0 5.6l-1.4 1.4a6 6 0 0 1 0-8.4zm8.4 0a6 6 0 0 1 0 8.4l-1.4-1.4a4 4 0 0 0 0-5.6l1.4-1.4z"/></svg>',
  edit: '<svg viewBox="0 0 24 24"><path d="M3 17.25V21h3.75L17.8 9.94l-3.75-3.75L3 17.25zm17.7-10.2a1 1 0 0 0 0-1.4l-2.35-2.35a1 1 0 0 0-1.4 0l-1.84 1.84 3.75 3.75 1.84-1.84z"/></svg>',
  kill: '<svg viewBox="0 0 24 24"><path d="M12 2a9 9 0 0 0-9 9c0 3.4 1.9 6.3 4.7 7.8V21a1 1 0 0 0 1 1h6.6a1 1 0 0 0 1-1v-2.2C19.1 17.3 21 14.4 21 11a9 9 0 0 0-9-9zM9 12.5a1.5 1.5 0 1 1 0-3 1.5 1.5 0 0 1 0 3zm6 0a1.5 1.5 0 1 1 0-3 1.5 1.5 0 0 1 0 3z"/></svg>',
  revive: '<svg viewBox="0 0 24 24"><path d="M12 5V2L7 6.5 12 11V8a5 5 0 1 1-5 5H5a7 7 0 1 0 7-8z"/></svg>',
  del: '<svg viewBox="0 0 24 24"><path d="M6 7h12l-1 14H7L6 7zm3-4h6l1 2h4v2H4V5h4l1-2z"/></svg>',
};
const icon = (name) => el('span', { class: 'ico', html: ICONS[name] });

function toast(msg, kind = '', ms = 3500) {
  const t = el('div', { class: `toast ${kind}` }, msg);
  $('#toasts').appendChild(t);
  setTimeout(() => t.remove(), ms);
}

// confirmDialog replaces window.confirm with an in-app modal. Resolves true/false.
function confirmDialog(title, text, okLabel = 'Confirm') {
  return new Promise((resolve) => {
    const m = $('#confirm-modal');
    $('#confirm-title').textContent = title;
    $('#confirm-text').textContent = text;
    const ok = $('#confirm-ok'), cancel = $('#confirm-cancel');
    ok.textContent = okLabel;
    const done = (v) => { m.hidden = true; ok.onclick = cancel.onclick = null; resolve(v); };
    ok.onclick = () => done(true);
    cancel.onclick = () => done(false);
    m.hidden = false;
    ok.focus();
  });
}

const isZeroTime = (t) => !t || t.startsWith('0001-');

function fmtAgo(t) {
  if (isZeroTime(t)) return 'never';
  const d = new Date(t);
  if (isNaN(d)) return t;
  const s = Math.round((Date.now() - d.getTime()) / 1000);
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return d.toLocaleString();
}

function fmtDuration(sec) {
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60), s = sec % 60;
  if (m < 60) return s ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

const fmtTime = (when) => (when ? new Date(when) : new Date()).toLocaleTimeString([], { hour12: false });

// ---------- Theme ----------

function applyTheme(t) {
  if (t) document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme;
  try { t ? localStorage.setItem('eo_theme', t) : localStorage.removeItem('eo_theme'); } catch { /* private mode */ }
}
$('#theme-btn').addEventListener('click', () => {
  const dark = document.documentElement.dataset.theme === 'dark' ||
    (!document.documentElement.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches);
  applyTheme(dark ? 'light' : 'dark');
});

// ---------- Tabs ----------

function showTab(name) {
  $$('.tab').forEach((t) => t.classList.toggle('active', t.dataset.tab === name));
  $$('.panel').forEach((p) => p.classList.toggle('active', p.dataset.panel === name));
  if (name === 'activity') { $('#act-badge').hidden = true; unread = 0; }
}
$$('.tab').forEach((btn) => btn.addEventListener('click', () => {
  if (btn.dataset.tab === 'form' && !$('#f-original-name').value) clearForm();
  showTab(btn.dataset.tab);
}));

// ---------- State ----------

let objects = [];
let status = null;
let selected = null;
let unread = 0;
let map, markerLayer;
const markers = new Map();   // name -> marker
let picking = false;

// ---------- Symbols ----------

const SPRITES = { '/': 'symbols/aprs-symbols-24-0.png', '\\': 'symbols/aprs-symbols-24-1.png' };
function spriteStyle(table, code) {
  const n = code ? code.charCodeAt(0) - 33 : -1;
  if (n < 0 || n >= 96) return '';
  const url = SPRITES[table === '/' ? '/' : '\\'];
  return `background-image:url(${url});background-position:-${(n % 16) * 24}px -${Math.floor(n / 16) * 24}px;`;
}
function symbolName(table, code) {
  const t = table === '/' ? '/' : '\\';
  const base = (window.APRS_SYMBOLS[t] || {})[code] || '';
  if (table !== '/' && table !== '\\' && table) return `${base || 'Symbol'} · overlay ${table}`;
  return base;
}
function symSprite(table, code, cls = 'sym-sprite') {
  const s = el('span', { class: cls, title: `${table}${code} ${symbolName(table, code)}`.trim() });
  s.style.cssText = spriteStyle(table, code);
  return s;
}

// ---------- Map ----------

function initMap() {
  map = L.map('map', { zoomControl: true, attributionControl: true }).setView([33.75, -84.39], 10);
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19, attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
  }).addTo(map);
  markerLayer = L.layerGroup().addTo(map);
  map.on('click', (e) => {
    if (!picking) return;
    $('#f-lat').value = e.latlng.lat.toFixed(5);
    $('#f-lon').value = e.latlng.lng.toFixed(5);
    setPicking(false);
    showTab('form');
  });
  try {
    const saved = JSON.parse(localStorage.getItem('eo_map') || 'null');
    if (saved && saved.c && saved.z) map.setView(saved.c, saved.z);
  } catch { /* ignore */ }
  map.on('moveend', () => {
    try { localStorage.setItem('eo_map', JSON.stringify({ c: map.getCenter(), z: map.getZoom() })); } catch { /* ignore */ }
  });
}

function setPicking(on) {
  picking = on;
  $('#map-hint').hidden = !on;
  $('#f-pick-btn').textContent = on ? 'Click on the map…' : 'Pick on map';
  map.getContainer().style.cursor = on ? 'crosshair' : '';
}
$('#f-pick-btn').addEventListener('click', () => setPicking(!picking));
$('#map-hint-cancel').addEventListener('click', (e) => { e.preventDefault(); setPicking(false); });

function markerLabel(o) {
  const name = `<span class="lbl-name">${escapeHtml(o.ObjectName)}</span>`;
  return name + `<span class="lbl-sub">${escapeHtml(shortStatus(o))}</span>`;
}
function escapeHtml(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }

function refreshMarkers(fit = false) {
  markerLayer.clearLayers();
  markers.clear();
  const pts = [];
  for (const o of objects) {
    const killed = o.Status === 'killed';
    const cls = ['aprs-marker', killed ? 'killed' : '', !o.Enabled && !killed ? 'disabled' : '', selected === o.ObjectName ? 'hl' : ''].join(' ');
    const html = `<span class="marker-sprite" style="${spriteStyle(o.SymbolTable, o.SymbolID)}"></span>`;
    const m = L.marker([o.Latitude, o.Longitude], {
      icon: L.divIcon({ html, className: cls, iconSize: [24, 24], iconAnchor: [12, 12], popupAnchor: [0, -14] }),
      title: o.ObjectName,
    });
    const popup = el('div', { class: 'obj-popup' },
      el('b', {}, o.ObjectName),
      el('div', { class: 'info' }, `${o.SymbolTable}${o.SymbolID} ${symbolName(o.SymbolTable, o.SymbolID)} · ${o.Latitude.toFixed(4)}, ${o.Longitude.toFixed(4)}`),
      el('div', {}, o.Comment || ''),
      el('div', { class: 'row', style: 'margin-top:8px' },
        el('button', { class: 'btn', title: 'Open this object in the edit form', onclick: () => loadIntoForm(o) }, 'Edit'),
        killed ? null : el('button', { class: 'btn primary', title: 'Transmit this object right now', onclick: () => beaconNow(o.ObjectName) }, 'Beacon now')));
    m.bindPopup(popup);
    m.bindTooltip(markerLabel(o), { permanent: true, direction: 'right', offset: [13, 0], className: 'beacon-label' + (killed ? ' killed' : '') });
    m.on('click', () => selectObject(o.ObjectName, false));
    markerLayer.addLayer(m);
    markers.set(o.ObjectName, m);
    pts.push([o.Latitude, o.Longitude]);
  }
  if (fit && pts.length) map.fitBounds(L.latLngBounds(pts), { padding: [40, 40], maxZoom: 14 });
}

// Every second: refresh countdown text on labels and cards without re-rendering.
function tick() {
  for (const o of objects) {
    const m = markers.get(o.ObjectName);
    const tt = m && m.getTooltip();
    if (tt) {
      const html = markerLabel(o);
      if (tt.getContent() !== html) tt.setContent(html);
    }
    const chip = document.querySelector(`.card[data-name="${cssEscape(o.ObjectName)}"] .chip.status`);
    if (chip) {
      const { cls, text } = statusChip(o);
      if (chip.textContent !== text) chip.textContent = text;
      if (chip.className !== `chip status ${cls}`) chip.className = `chip status ${cls}`;
    }
  }
}
const cssEscape = (s) => (window.CSS && CSS.escape) ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&');

// ---------- Status texts ----------

function nextDue(o) {
  if (isZeroTime(o.LastBeacon)) return 0;
  const last = Date.parse(o.LastBeacon);
  if (isNaN(last)) return 0;
  return last + o.IntervalMinutes * 60000 - Date.now();
}

function statusChip(o) {
  if (o.Status === 'killed') {
    return o.KillBeaconsLeft > 0 ? { cls: 'killing', text: `killing · ${o.KillBeaconsLeft} left` } : { cls: 'killed', text: 'killed' };
  }
  if (!o.Enabled) return { cls: 'off', text: 'disabled' };
  if (o.IntervalMinutes <= 0) return { cls: 'manual', text: 'manual' };
  if (!isZeroTime(o.ExpiresAt)) {
    const ms = Date.parse(o.ExpiresAt) - Date.now();
    if (ms <= 0) return { cls: 'expiring', text: 'expired · killing soon' };
    if (ms < 3600000) return { cls: 'expiring', text: `expires in ${fmtDuration(ms / 1000)}` };
  }
  const ms = nextDue(o);
  if (ms <= 0) return { cls: 'due', text: status && status.transport.status === 'connected' ? 'due now' : 'due · waiting for radio' };
  return { cls: 'live', text: `next in ${fmtDuration(ms / 1000)}` };
}

// chipTitle explains a status chip in one sentence.
function chipTitle(o) {
  if (o.Status === 'killed') {
    return o.KillBeaconsLeft > 0 ? `Kill packets still to send: ${o.KillBeaconsLeft}. Revive puts the object back on the air` : 'Killed: receivers were told to drop it. Revive to bring it back, Delete to forget it';
  }
  if (!o.Enabled) return 'Disabled: no automatic beacons. Edit to enable, or click Beacon to send once';
  if (o.IntervalMinutes <= 0) return 'Manual: sent only when you click Beacon';
  if (!isZeroTime(o.ExpiresAt)) {
    const ms = Date.parse(o.ExpiresAt) - Date.now();
    if (ms <= 3600000) return `Expires ${new Date(o.ExpiresAt).toLocaleString()}; it is killed automatically after that`;
  }
  return nextDue(o) <= 0 ? 'Due: goes out on the next scheduler tick once the radio transport is connected' : 'Live: countdown to the next automatic beacon';
}

function shortStatus(o) {
  if (o.Status === 'killed') return o.KillBeaconsLeft > 0 ? `killing ${o.KillBeaconsLeft}` : 'killed';
  if (!o.Enabled) return 'off';
  if (o.IntervalMinutes <= 0) return 'manual';
  const ms = nextDue(o);
  return ms <= 0 ? 'due' : fmtDuration(ms / 1000);
}

// ---------- Object list ----------

function visibleObjects() {
  const q = $('#obj-search').value.trim().toLowerCase();
  const f = $('#obj-filter').value;
  return objects.filter((o) => {
    if (f === 'live' && (o.Status === 'killed' || !o.Enabled)) return false;
    if (f === 'disabled' && (o.Status === 'killed' || o.Enabled)) return false;
    if (f === 'killed' && o.Status !== 'killed') return false;
    if (q && !(o.ObjectName.toLowerCase().includes(q) || (o.Comment || '').toLowerCase().includes(q))) return false;
    return true;
  });
}

function renderList() {
  const list = $('#obj-list');
  list.replaceChildren();
  $('#obj-count').textContent = objects.length || '';
  $('#obj-empty').hidden = objects.length > 0;
  for (const o of visibleObjects()) {
    const killed = o.Status === 'killed';
    const { cls, text } = statusChip(o);
    const pathTxt = o.Path === '-' ? 'direct' : (o.Path || '');
    const card = el('div', {
      class: ['card', killed ? 'killed' : '', !o.Enabled && !killed ? 'disabled' : '', selected === o.ObjectName ? 'selected' : ''].join(' '),
      dataset: { name: o.ObjectName },
      onclick: (ev) => { if (!ev.target.closest('.actions')) selectObject(o.ObjectName, true); },
      onmouseenter: () => highlightMarker(o.ObjectName, true),
      onmouseleave: () => highlightMarker(o.ObjectName, false),
    },
      el('div', { class: 'sym' }, symSprite(o.SymbolTable, o.SymbolID)),
      el('div', { class: 'name', title: 'Object name as sent on the air. Click the card to centre the map on it' }, o.ObjectName, el('span', { class: `chip status ${cls}`, title: chipTitle(o) }, text)),
      el('div', { class: 'meta' },
        o.Comment ? el('span', { class: 'comment', title: o.Comment }, o.Comment) : null,
        el('span', { title: 'Position (latitude, longitude)' }, `${o.Latitude.toFixed(4)}, ${o.Longitude.toFixed(4)}`),
        el('span', { title: o.IntervalMinutes > 0 ? 'Beacon interval' : 'No automatic beacons; send with the Beacon button' }, o.IntervalMinutes > 0 ? `every ${o.IntervalMinutes}m` : 'manual'),
        pathTxt ? el('span', { class: 'mono', title: 'Digipeater path override for this object' }, pathTxt) : null,
        el('span', { title: 'Time since the last packet for this object was sent' }, `tx ${fmtAgo(o.LastBeacon)}`)),
      el('div', { class: 'actions' }, ...actionButtons(o, killed)));
    list.appendChild(card);
  }
}
$('#obj-search').addEventListener('input', renderList);
$('#obj-filter').addEventListener('change', renderList);

function actionButtons(o, killed) {
  if (killed) {
    return [
      armedButton('revive', 'Revive: put the object back on the air', () => reviveNow(o.ObjectName)),
      el('button', { class: 'btn ghost', title: 'Delete from the local list (does not send anything)', onclick: () => deleteObject(o.ObjectName) }, icon('del')),
    ];
  }
  return [
    el('button', { class: 'btn ghost tx', title: 'Beacon now', onclick: () => beaconNow(o.ObjectName) }, icon('tx')),
    el('button', { class: 'btn ghost', title: 'Edit', onclick: () => loadIntoForm(o) }, icon('edit')),
    armedButton('kill', `Kill: send ${killCount()} kill packets so receivers drop the object`, () => killNow(o.ObjectName)),
  ];
}
const killCount = () => (status && status.kill_beacon_count) || 3;

// armedButton: first click arms (pulsing, 5 s), second click confirms.
function armedButton(kind, title, onConfirm) {
  const btn = el('button', { class: `btn ghost ${kind}`, title }, icon(kind));
  let timer = null;
  const disarm = () => { btn.classList.remove('armed'); btn.replaceChildren(icon(kind)); clearTimeout(timer); timer = null; };
  btn.addEventListener('click', async () => {
    if (!timer) {
      btn.classList.add('armed');
      btn.replaceChildren(kind === 'kill' ? 'Kill?' : 'Revive?');
      timer = setTimeout(disarm, 5000);
      return;
    }
    disarm();
    await onConfirm();
  });
  return btn;
}

function selectObject(name, pan) {
  selected = name;
  $$('.card').forEach((c) => c.classList.toggle('selected', c.dataset.name === name));
  const m = markers.get(name);
  if (m) {
    if (pan) map.panTo(m.getLatLng());
    m.openPopup();
  }
}
function highlightMarker(name, on) {
  const m = markers.get(name);
  if (!m) return;
  const e = m.getElement();
  if (e) e.classList.toggle('hl', on);
}

// ---------- Actions ----------

async function reload(fit = false) {
  try {
    objects = await API.objects();
    renderList();
    refreshMarkers(fit);
  } catch (e) { toast(e.message, 'err'); }
}
async function beaconNow(name) {
  try { await API.beacon(name); toast(`${name}: beacon sent`, 'ok'); }
  catch (e) { toast(`${name}: ${e.message}`, 'err', 6000); }
}
async function killNow(name) {
  try {
    const r = await API.kill(name);
    toast(r && r.note ? `${name}: ${r.note}` : `${name}: kill sequence started (${killCount()} packets)`, r && r.note ? 'warn' : 'ok', 5000);
  } catch (e) { toast(`kill ${name}: ${e.message}`, 'err', 6000); }
}
async function reviveNow(name) {
  try { await API.revive(name); toast(`${name}: revived, live beacon goes out now`, 'ok'); }
  catch (e) { toast(`revive ${name}: ${e.message}`, 'err', 6000); }
}
async function deleteObject(name) {
  if (!await confirmDialog(`Delete ${name}?`, 'Removes it from the local list only. Receivers that already have the object keep it until it times out or is killed.', 'Delete')) return;
  try { await API.remove(name); toast(`${name} deleted`); if ($('#f-original-name').value === name) { clearForm(); showTab('objects'); } }
  catch (e) { toast(`delete ${name}: ${e.message}`, 'err'); }
}
$('#beacon-all-btn').addEventListener('click', async () => {
  const n = objects.filter((o) => o.Enabled && o.Status !== 'killed').length;
  if (!n) { toast('No enabled objects to send'); return; }
  if (!await confirmDialog('Beacon all objects now?', `${n} enabled object(s) will be transmitted immediately, one after another.`, 'Send all')) return;
  try {
    const r = await API.beaconAll();
    toast(`Sent ${r.sent} object(s)${r.failed.length ? `, ${r.failed.length} failed` : ''}`, r.failed.length ? 'warn' : 'ok');
    r.failed.forEach((f) => toast(f, 'err', 6000));
  } catch (e) { toast(e.message, 'err'); }
});

// ---------- Form ----------

const PATH_PRESETS = ['', '-', 'WIDE1-1', 'WIDE1-1,WIDE2-1', 'WIDE2-1', 'WIDE2-2'];
function getPath() {
  const v = $('#f-path-preset').value;
  return v === '__custom__' ? $('#f-path-custom').value.trim().toUpperCase() : v;
}
function setPath(val) {
  const custom = !PATH_PRESETS.includes(val);
  $('#f-path-preset').value = custom ? '__custom__' : val;
  $('#f-path-custom-wrap').hidden = !custom;
  $('#f-path-custom').value = custom ? val : '';
}
$('#f-path-preset').addEventListener('change', () => {
  const custom = $('#f-path-preset').value === '__custom__';
  $('#f-path-custom-wrap').hidden = !custom;
  if (custom) $('#f-path-custom').focus();
});

function clearForm() {
  $('#form-title').textContent = 'New object';
  $('#tab-form').textContent = 'New';
  $('#f-original-name').value = '';
  $('#f-name').value = '';
  $('#f-name').disabled = false;
  $('#f-lat').value = '';
  $('#f-lon').value = '';
  $('#f-alt').value = '';
  setSymbol('/', 'r');
  $('#f-comment').value = '';
  setPath('');
  $('#f-expires').value = '';
  $('#f-interval').value = '30';
  $('#f-enabled').checked = true;
  $('#f-delete').hidden = true;
  $('#form-error').hidden = true;
}

function loadIntoForm(o) {
  $('#form-title').textContent = `Edit ${o.ObjectName}`;
  $('#tab-form').textContent = 'Edit';
  $('#f-original-name').value = o.ObjectName;
  $('#f-name').value = o.ObjectName;
  $('#f-name').disabled = true; // renaming = new object on the air; create a new one instead
  $('#f-lat').value = o.Latitude;
  $('#f-lon').value = o.Longitude;
  $('#f-alt').value = o.Altitude || '';
  setSymbol(o.SymbolTable, o.SymbolID);
  $('#f-comment').value = o.Comment || '';
  setPath(o.Path || '');
  $('#f-expires').value = expiresToInput(o.ExpiresAt);
  $('#f-interval').value = o.IntervalMinutes;
  $('#f-enabled').checked = !!o.Enabled;
  $('#f-delete').hidden = false;
  $('#form-error').hidden = true;
  showTab('form');
  selectObject(o.ObjectName, true);
}

// ExpiresAt: server stores UTC RFC3339 (or the zero sentinel); the input is local time.
function expiresToInput(s) {
  if (isZeroTime(s)) return '';
  const d = new Date(s);
  if (isNaN(d)) return '';
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}
function expiresFromInput(s) {
  if (!s) return '0001-01-01T00:00:00';
  const d = new Date(s);
  return isNaN(d) ? '0001-01-01T00:00:00' : d.toISOString();
}

$('#object-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const name = $('#f-name').value.trim().toUpperCase();
  const existing = objects.find((o) => o.ObjectName === name);
  const body = {
    ObjectName: name,
    ShowTooltip: true,
    SymbolTable: $('#f-table').value,
    SymbolID: $('#f-sym').value,
    Comment: $('#f-comment').value.trim(),
    IntervalMinutes: parseInt($('#f-interval').value, 10) || 0,
    Latitude: parseFloat($('#f-lat').value),
    Longitude: parseFloat($('#f-lon').value),
    Altitude: parseFloat($('#f-alt').value) || 0,
    LastBeacon: existing ? existing.LastBeacon : '0001-01-01T00:00:00',
    Enabled: $('#f-enabled').checked,
    Path: getPath(),
    ExpiresAt: expiresFromInput($('#f-expires').value),
  };
  if (!$('#f-original-name').value && existing) {
    if (!await confirmDialog(`Overwrite ${name}?`, 'An object with this name already exists. Saving replaces its position, symbol and settings.', 'Overwrite')) return;
  }
  try {
    await API.upsert(name, body);
    toast(`${name} saved`, 'ok');
    clearForm();
    showTab('objects');
    selectObject(name, true);
  } catch (e) {
    $('#form-error').textContent = e.message;
    $('#form-error').hidden = false;
  }
});
$('#f-cancel').addEventListener('click', () => { clearForm(); setPicking(false); showTab('objects'); });
$('#f-delete').addEventListener('click', () => deleteObject($('#f-original-name').value));

// ---------- Symbol picker ----------

function setSymbol(table, code) {
  $('#f-table').value = table;
  $('#f-sym').value = code;
  $('#f-sym-preview').style.cssText = spriteStyle(table, code);
  $('#f-sym-name').textContent = symbolName(table, code) || 'Symbol';
  $('#f-sym-code').textContent = `${table}${code}`;
  if (document.activeElement !== $('#f-sym-manual')) $('#f-sym-manual').value = `${table}${code}`;
}
$('#f-sym-manual').addEventListener('input', () => {
  const v = $('#f-sym-manual').value;
  if (v.length === 2 && v.charCodeAt(1) >= 33 && v.charCodeAt(1) <= 126) setSymbol(v[0], v[1]);
});

function renderSymGrid(div, table) {
  div.replaceChildren();
  for (let n = 0; n < 96; n++) {
    const code = String.fromCharCode(33 + n);
    const name = symbolName(table, code);
    const cell = el('button', { type: 'button', class: 'sym-cell', title: `${table}${code} ${name}`.trim(), dataset: { table, code, name: name.toLowerCase() } },
      symSprite(table, code));
    cell.addEventListener('click', () => { setSymbol(table, code); closeModal('#sym-modal'); });
    div.appendChild(cell);
  }
}
function openSymModal() {
  if (!$('#sym-grid-primary').firstChild) {
    renderSymGrid($('#sym-grid-primary'), '/');
    renderSymGrid($('#sym-grid-alternate'), '\\');
  }
  const cur = `${$('#f-table').value}${$('#f-sym').value}`;
  $$('.sym-cell').forEach((c) => c.classList.toggle('selected', `${c.dataset.table}${c.dataset.code}` === cur));
  $('#sym-search').value = '';
  filterSymbols('');
  openModal('#sym-modal');
  $('#sym-search').focus();
}
function filterSymbols(q) {
  q = q.trim().toLowerCase();
  $$('.sym-cell').forEach((c) => c.classList.toggle('dim', !!q && !c.dataset.name.includes(q) && !(`${c.dataset.table}${c.dataset.code}`).includes(q)));
}
$('#f-sym-pick').addEventListener('click', openSymModal);
$('#sym-search').addEventListener('input', () => filterSymbols($('#sym-search').value));

// ---------- Modals ----------

function openModal(sel) { $(sel).hidden = false; }
function closeModal(sel) { $(sel).hidden = true; }
$$('.modal').forEach((m) => {
  m.addEventListener('click', (ev) => { if (ev.target === m && m.id !== 'confirm-modal') m.hidden = true; });
  m.querySelectorAll('.modal-close').forEach((b) => b.addEventListener('click', () => { m.hidden = true; }));
});
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape') {
    $$('.modal').forEach((m) => { if (m.id !== 'confirm-modal') m.hidden = true; });
    if (picking) setPicking(false);
  }
});

// ---------- Settings ----------

let cfgView = null;
function fillSettings(c) {
  cfgView = c;
  $('#s-callsign').value = c.station.callsign || '';
  $('#s-tocall').value = c.station.tocall || 'APZEMC';
  const p = c.station.path || '';
  const known = ['', 'WIDE1-1', 'WIDE1-1,WIDE2-1', 'WIDE2-1'].includes(p);
  $('#s-path').value = known ? p : '__custom__';
  $('#s-path-custom-wrap').hidden = known;
  $('#s-path-custom').value = known ? '' : p;
  $$('input[name="s-transport"]').forEach((r) => { r.checked = r.value === c.transport; });
  toggleTransport(c.transport);
  $('#s-gw-url').value = c.graywolf.url || '';
  $('#s-gw-user').value = c.graywolf.username || '';
  $('#s-gw-pass').value = '';
  $('#s-gw-pass').placeholder = c.graywolf.password_set ? '•••••••• (saved)' : '';
  $('#s-gw-pass-hint').textContent = c.graywolf.password_set ? 'leave blank to keep' : '';
  $('#s-gw-sendpath').value = c.graywolf.send_path || 'rf';
  setChannelOptions([], c.graywolf.channel);
  $('#s-kiss-addr').value = c.kiss.address || '';
  $('#s-kiss-port').value = c.kiss.port || 0;
  $('#s-web-listen').value = c.web.listen || '';
  $('#s-objects-file').value = c.storage.objects_file || '';
  $('#s-open-browser').checked = !!c.web.open_browser;
  const pl = c.planner || {};
  $('#s-pl-enabled').checked = !!pl.enabled;
  $('#s-pl-url').value = pl.url || '';
  $('#s-pl-token').value = '';
  $('#s-pl-token').placeholder = pl.token_set ? '\u2022\u2022\u2022\u2022 (saved)' : 'ebt_...';
  $('#s-pl-token-hint').textContent = pl.token_set ? 'leave blank to keep' : '';
  $('#s-pl-stations').checked = pl.forward_stations !== false;
  $('#s-pl-messages').checked = pl.send_messages !== false;
  $('#s-pl-interval').value = pl.interval_seconds || 30;
  $('#s-pl-result').textContent = '';
  $('#s-upd-check').checked = !!c.updates.check;
  $('#s-upd-hours').value = c.updates.interval_hours || 12;
  $('#s-gw-result').hidden = true;
  $('#settings-error').hidden = true;
  if (status) {
    $('#settings-paths').textContent = `Config: ${status.config_path} · Objects: ${status.objects_file} · Version ${status.version} (${status.install_mode})`;
  }
}
function setChannelOptions(channels, selectedId) {
  const sel = $('#s-gw-channel');
  sel.replaceChildren(el('option', { value: '0' }, 'Graywolf default'));
  for (const ch of channels) {
    const backing = ch.backing && ch.backing.summary ? ` · ${ch.backing.summary}` : '';
    sel.appendChild(el('option', { value: String(ch.id), title: ch.backing && ch.backing.health ? `Backing: ${ch.backing.summary} (${ch.backing.health})` : '' }, `${ch.id}: ${ch.name || 'channel'}${backing}`));
  }
  if (selectedId && !channels.some((c) => c.id === selectedId)) sel.appendChild(el('option', { value: String(selectedId) }, `Channel ${selectedId}`));
  sel.value = String(selectedId || 0);
}
function toggleTransport(t) {
  $('#s-graywolf').hidden = t !== 'graywolf';
  $('#s-kiss').hidden = t !== 'kiss';
}
$$('input[name="s-transport"]').forEach((r) => r.addEventListener('change', () => toggleTransport(r.value)));
$('#s-path').addEventListener('change', () => {
  const custom = $('#s-path').value === '__custom__';
  $('#s-path-custom-wrap').hidden = !custom;
  if (custom) $('#s-path-custom').focus();
});

async function openSettings() {
  try { fillSettings(await API.config()); } catch (e) { toast(`settings: ${e.message}`, 'err'); return; }
  openModal('#settings-modal');
  $('#s-callsign').focus();
  if (cfgView.transport === 'graywolf' && cfgView.graywolf.username) testGraywolf(true);
}
$('#settings-btn').addEventListener('click', openSettings);
$('#setup-open-btn').addEventListener('click', openSettings);

async function testGraywolf(quiet = false) {
  const out = $('#s-gw-result');
  if (!quiet) { out.hidden = false; out.className = 'result'; out.textContent = 'Testing…'; }
  try {
    const r = await API.gwTest({ url: $('#s-gw-url').value, username: $('#s-gw-user').value, password: $('#s-gw-pass').value });
    if (r.ok) {
      setChannelOptions(r.channels || [], parseInt($('#s-gw-channel').value, 10) || (cfgView && cfgView.graywolf.channel) || 0);
      out.className = 'result ok';
      out.textContent = `Connected to Graywolf ${r.version} (${r.platform}) at ${r.url}. Logged in. ${(r.channels || []).length} channel(s), ${r.beacons} beacon(s) configured.`;
      out.hidden = false;
    } else {
      out.className = 'result bad';
      out.textContent = r.error || 'Connection failed';
      out.hidden = false;
    }
  } catch (e) { out.className = 'result bad'; out.textContent = e.message; out.hidden = false; }
}
$('#s-gw-test').addEventListener('click', () => testGraywolf(false));

$('#s-pl-test').addEventListener('click', async () => {
  const out = $('#s-pl-result');
  out.textContent = 'Testing...';
  try {
    const r = await API.plTest({ url: $('#s-pl-url').value.trim(), token: $('#s-pl-token').value.trim() });
    out.textContent = r.ok ? `Linked to bridge "${r.bridge}"` : `Failed: ${r.error}`;
  } catch (e) { out.textContent = `Failed: ${e.message}`; }
});
$('#s-pl-import').addEventListener('click', async () => {
  const out = $('#s-pl-result');
  out.textContent = 'Importing...';
  try {
    const r = await API.plImport({ deployment: 'active' });
    out.textContent = `${r.imported} object(s) imported from "${r.deployment}", disabled until you enable them`;
    toast(`${r.imported} objects imported from EmComm Planner`, 'ok');
  } catch (e) { out.textContent = `Failed: ${e.message}`; }
});

$('#settings-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const c = JSON.parse(JSON.stringify(cfgView));
  c.station = { callsign: $('#s-callsign').value.trim().toUpperCase(), tocall: $('#s-tocall').value.trim().toUpperCase(),
    path: $('#s-path').value === '__custom__' ? $('#s-path-custom').value.trim().toUpperCase() : $('#s-path').value };
  c.transport = ($$('input[name="s-transport"]').find((r) => r.checked) || {}).value || 'graywolf';
  c.graywolf = { url: $('#s-gw-url').value.trim(), username: $('#s-gw-user').value.trim(), password: $('#s-gw-pass').value,
    channel: parseInt($('#s-gw-channel').value, 10) || 0, send_path: $('#s-gw-sendpath').value };
  c.kiss = { address: $('#s-kiss-addr').value.trim(), port: parseInt($('#s-kiss-port').value, 10) || 0 };
  c.web = { enabled: true, listen: $('#s-web-listen').value.trim(), open_browser: $('#s-open-browser').checked };
  c.storage = { objects_file: $('#s-objects-file').value.trim() };
  c.updates = { check: $('#s-upd-check').checked, interval_hours: parseFloat($('#s-upd-hours').value) || 12, skip_version: cfgView.updates.skip_version || '' };
  c.planner = { enabled: $('#s-pl-enabled').checked, url: $('#s-pl-url').value.trim(), token: $('#s-pl-token').value.trim(),
    forward_stations: $('#s-pl-stations').checked, send_messages: $('#s-pl-messages').checked, interval_seconds: parseInt($('#s-pl-interval').value, 10) || 30 };
  try {
    const r = await API.saveConfig(c);
    closeModal('#settings-modal');
    toast('Settings saved', 'ok');
    (r.notes || []).forEach((n) => toast(n, 'warn', 7000));
    await refreshStatus();
  } catch (e) {
    $('#settings-error').textContent = e.message;
    $('#settings-error').hidden = false;
  }
});
$('#s-upd-now').addEventListener('click', async () => {
  $('#s-upd-status').textContent = 'Checking…';
  try {
    const u = await API.updateCheck();
    $('#s-upd-status').textContent = u.error ? `Check failed: ${u.error}` : (u.available ? `Version ${u.latest} is available` : `Up to date (${u.current})`);
    applyUpdateState(u);
  } catch (e) { $('#s-upd-status').textContent = e.message; }
});
$('#quit-btn').addEventListener('click', async () => {
  if (!await confirmDialog('Quit Emcomm Objects?', 'Beaconing stops until the app is started again.', 'Quit')) return;
  try { await API.quit(); } catch { /* server is going away */ }
  document.body.innerHTML = '<div class="empty" style="padding:60px"><h2>Emcomm Objects has stopped.</h2><p>Start it again from the Start menu or the executable, then reload this page.</p></div>';
});

// ---------- Updates ----------

let updateState = null;
function applyUpdateState(u) {
  updateState = u;
  const pill = $('#update-pill');
  pill.hidden = !(u && u.available);
  if (u && u.available) { pill.textContent = `Update ${u.latest}`; pill.title = `Version ${u.latest} is available (running ${u.current}). Click for release notes and to install`; }
}
$('#update-pill').addEventListener('click', () => {
  const u = updateState;
  if (!u) return;
  $('#upd-title').textContent = `Emcomm Objects ${u.latest} is available`;
  $('#upd-summary').textContent = `You are running ${u.current} (${u.mode}). ` + (u.can_self_update
    ? 'Install now downloads the release, verifies its checksum, closes the app, installs and reopens it. Your settings and objects are kept.'
    : 'Self-update is available on the Windows builds; on this platform download the release and replace the files.');
  $('#upd-notes').textContent = u.notes || '';
  $('#upd-install').hidden = !u.can_self_update;
  $('#upd-download').href = u.url || 'https://github.com/KK4ODA/emcomm-objects/releases';
  $('#upd-progress').hidden = true;
  openModal('#update-modal');
});
$('#upd-skip').addEventListener('click', async () => {
  try { applyUpdateState(await API.updateSkip(updateState.latest)); closeModal('#update-modal'); toast(`Version ${updateState.latest} skipped`); }
  catch (e) { toast(e.message, 'err'); }
});
$('#upd-install').addEventListener('click', async () => {
  const btn = $('#upd-install');
  btn.disabled = true;
  $('#upd-progress').hidden = false;
  $('#upd-progress').textContent = 'Downloading and verifying…';
  try {
    const r = await API.updateApply();
    $('#upd-progress').textContent = r.message;
    waitForRestart();
  } catch (e) {
    $('#upd-progress').textContent = `Update failed: ${e.message}`;
    btn.disabled = false;
  }
});
function waitForRestart() {
  const from = updateState.current;
  let gone = false;
  const poll = async () => {
    try {
      const v = await API.version();
      if (gone && v.version !== from) { location.reload(); return; }
      $('#upd-progress').textContent = gone ? 'Starting the new version…' : 'Closing for the update…';
    } catch { gone = true; $('#upd-progress').textContent = 'Installing… this page reloads when the app is back.'; }
    setTimeout(poll, 1500);
  };
  setTimeout(poll, 1500);
}

// ---------- Status / link ----------

function applyPlannerState(p) {
  const pill = $('#planner-pill');
  if (!pill) return;
  if (!p || !p.enabled) { pill.hidden = true; return; }
  pill.hidden = false;
  pill.className = `pill ${p.connected ? 'ok' : (p.last_error ? 'err' : 'warn')}`;
  const when = p.last_ok ? new Date(p.last_ok).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : 'never';
  $('#planner-text').textContent = p.connected ? `Planner linked · ${p.stations_sent} stations · ${p.messages_sent} msgs` : `Planner: ${p.last_error || 'connecting'}`;
  pill.title = `EmComm Planner link · last success ${when}${p.bridge_name ? ' · bridge ' + p.bridge_name : ''}${p.last_error ? ' · ' + p.last_error : ''}`;
}

function applyLinkState(s) {
  const pill = $('#link-pill');
  const cls = { connected: 'ok', connecting: 'warn', disconnected: 'err' }[s.status] || 'off';
  pill.className = `pill ${cls}`;
  const name = s.transport === 'graywolf' ? 'Graywolf' : s.transport === 'kiss' ? 'KISS' : 'No transport';
  $('#link-text').textContent = s.status === 'connected' ? (s.detail || `${name} connected`) : `${name}: ${s.detail || s.status}`;
  pill.title = `${name} · ${s.status}${s.detail ? ' · ' + s.detail : ''}`;
}

async function refreshStatus() {
  try {
    status = await API.status();
    applyLinkState(status.transport);
    $('#setup-banner').hidden = !status.setup_needed;
    const st = status.station;
    $('#station-chip').textContent = st.callsign ? `${st.callsign} › ${st.tocall}${st.path ? ' via ' + st.path : ' direct'}` : 'no callsign set';
    applyPlannerState(status.planner);
    $('#station-chip').title = st.callsign ? `Beacons are sent from ${st.callsign} to ${st.tocall}${st.path ? ' via ' + st.path : ' with no digipeater'} (Settings > Station)` : 'No callsign set yet: open Settings';
    $('#sb-version').textContent = `emcomm-objects ${status.version}`;
    $('#sb-mode').textContent = status.install_mode;
    $('#sb-data').textContent = status.data_dir;
    $('#sb-data').title = `Data folder: config.yaml, the objects file and emcomm-objects.log live here`;
    $('#kill-hint').textContent = `${status.kill_beacon_count} kill packets ${Math.round(status.kill_beacon_interval_seconds)} s apart`;
    if (status.update) applyUpdateState(status.update);
  } catch (e) { toast(`status: ${e.message}`, 'err'); }
}

// ---------- Activity ----------

const MAX_FEED = 300;
function feedItem(when, cls, headNodes, infoText) {
  const li = el('li', { class: cls },
    el('span', { class: 't', title: new Date(when || Date.now()).toLocaleString() }, fmtTime(when)),
    el('div', { class: 'body' }, el('div', { class: 'head' }, ...headNodes), infoText ? el('div', { class: 'info', title: 'APRS info field exactly as transmitted' }, infoText) : null));
  return li;
}
function pushFeed(list, li) {
  list.prepend(li);
  while (list.children.length > MAX_FEED) list.lastChild.remove();
}
function addPacket(p, when) {
  const li = feedItem(when, p.killed ? 'kill' : 'tx',
    [el('span', { class: `chip ${p.killed ? 'killp' : 'tx'}`, title: p.killed ? 'Kill packet: tells receivers to drop the object' : 'Live object beacon' }, p.killed ? 'KILL' : 'TX'), el('span', { class: 'obj', title: 'Object name' }, p.object),
      el('span', { class: 'via', title: 'Source > destination, digipeater path, and the transport used' }, `${p.source}>${p.dest}${p.path ? ',' + p.path : ''} via ${p.transport}`)],
    p.info);
  pushFeed($('#packet-list'), li);
  bumpUnread();
}
function addStateLine(s, when) {
  pushFeed($('#packet-list'), feedItem(when, `state-${s.status}`, [`${s.transport} ${s.status}${s.detail ? ' · ' + s.detail : ''}`]));
}
function addLog(e) {
  pushFeed($('#log-list'), feedItem(e.when, `lvl-${e.level}`, [e.message]));
  if (e.level === 'warn' || e.level === 'error') { toast(e.message, e.level === 'error' ? 'err' : 'warn', 6000); bumpUnread(); }
}
function bumpUnread() {
  if ($('.panel[data-panel="activity"]').classList.contains('active')) return;
  unread++;
  $('#act-badge').textContent = unread;
  $('#act-badge').hidden = false;
}
$$('.seg button').forEach((b) => b.addEventListener('click', () => {
  $$('.seg button').forEach((x) => x.classList.toggle('active', x === b));
  $('#packet-list').hidden = b.dataset.act !== 'packets';
  $('#log-list').hidden = b.dataset.act !== 'log';
}));
$('#act-clear').addEventListener('click', () => { $('#packet-list').replaceChildren(); $('#log-list').replaceChildren(); });

// ---------- SSE ----------

let reloadTimer = null;
function scheduleReload() {
  clearTimeout(reloadTimer);
  reloadTimer = setTimeout(() => reload(false), 150);
}
function startEvents() {
  const es = new EventSource('/api/events');
  const on = (type, fn) => es.addEventListener(type, (ev) => { try { fn(JSON.parse(ev.data)); } catch { /* ignore */ } });
  on('packet', (e) => { if (e.packet) addPacket(e.packet, e.when); scheduleReload(); });
  on('state', (e) => { if (e.state) { applyLinkState(e.state); addStateLine(e.state, e.when); if (status) status.transport = e.state; } });
  on('log', (e) => { if (e.log) addLog(e.log); });
  on('objects', scheduleReload);
  on('config', refreshStatus);
  on('update', refreshStatus);
  es.onerror = () => { $('#link-pill').className = 'pill off'; $('#link-text').textContent = 'app unreachable…'; };
  es.onopen = () => refreshStatus();
}

// ---------- CSV import ----------

const CSV_HEADER_MAP = {
  objectname: 'ObjectName', name: 'ObjectName', object: 'ObjectName',
  latitude: 'Latitude', lat: 'Latitude', longitude: 'Longitude', lon: 'Longitude', lng: 'Longitude', long: 'Longitude',
  symboltable: 'SymbolTable', table: 'SymbolTable', symbolid: 'SymbolID', symbol: 'SymbolID', sym: 'SymbolID',
  comment: 'Comment', intervalminutes: 'IntervalMinutes', interval: 'IntervalMinutes', path: 'Path', enabled: 'Enabled',
};

function parseCSV(text) {
  const rows = [];
  let row = [], field = '', q = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (q) {
      if (c === '"') { if (text[i + 1] === '"') { field += '"'; i++; } else q = false; }
      else field += c;
      continue;
    }
    if (c === '"') q = true;
    else if (c === ',') { row.push(field); field = ''; }
    else if (c === '\r') { /* skip */ }
    else if (c === '\n') { row.push(field); rows.push(row); row = []; field = ''; }
    else field += c;
  }
  if (field.length || row.length) { row.push(field); rows.push(row); }
  while (rows.length && rows[rows.length - 1].every((f) => f.trim() === '')) rows.pop();
  if (!rows.length) throw new Error('The CSV is empty.');
  return { headers: rows[0].map((h) => h.trim()), rows: rows.slice(1) };
}

function deriveStationTag(callsign) {
  if (!callsign) return '';
  return callsign.split('-')[0].replace(/[^A-Z0-9]/gi, '').toUpperCase().slice(-2);
}

// generateAprsName shortens a long human name to 1–9 APRS-safe characters,
// e.g. "DeKalb County Fire Station 1" → "DCFS1OD" (with station tag "OD").
function generateAprsName(longName, taken, tag) {
  const budget = 9 - tag.length;
  let abbrev = (longName.match(/[A-Z0-9]/g) || []).join('');
  if (!abbrev) abbrev = longName.split(/[\s_\-]+/).map((w) => (/^\d+$/.test(w) ? w : w.charAt(0).toUpperCase())).join('');
  abbrev = abbrev.replace(/[^A-Z0-9]/g, '');
  let base = abbrev.slice(0, budget) || 'OBJ';
  let cand = base + tag;
  if (!taken.has(cand)) return cand;
  for (let i = 2; i < 36; i++) {
    const suffix = i < 10 ? String(i) : String.fromCharCode(55 + i);
    cand = base.slice(0, Math.max(1, budget - 1)) + suffix + tag;
    if (!taken.has(cand)) return cand;
  }
  return abbrev.slice(0, 9) || 'OBJ';
}

function validateImportRow(headers, raw, storeNames, takenInImport, tag) {
  const obj = { ShowTooltip: true, SymbolTable: '/', SymbolID: 'r', Comment: '', IntervalMinutes: 30, Enabled: false, Path: '', Altitude: 0, LastBeacon: '0001-01-01T00:00:00' };
  const errs = [];
  headers.forEach((h, i) => {
    const key = CSV_HEADER_MAP[h.toLowerCase().replace(/[^a-z]/g, '')];
    const v = (raw[i] || '').trim();
    if (!key || v === '') return;
    if (key === 'Latitude' || key === 'Longitude') { const f = parseFloat(v); if (isNaN(f)) errs.push(`${key} is not a number: "${v}"`); else obj[key] = f; }
    else if (key === 'IntervalMinutes') { const n = parseInt(v, 10); if (isNaN(n) || n < 0) errs.push(`IntervalMinutes invalid: "${v}"`); else obj[key] = n; }
    else if (key === 'Enabled') obj.Enabled = /^(1|true|yes|y|on)$/i.test(v);
    else obj[key] = v;
  });
  const originalName = obj.ObjectName || '';
  let autoNamed = false;
  const all = new Set([...storeNames, ...takenInImport]);
  if (obj.ObjectName && obj.ObjectName.length > 9) { obj.ObjectName = generateAprsName(obj.ObjectName, all, tag); autoNamed = true; }
  else if (obj.ObjectName && takenInImport.has(obj.ObjectName)) { obj.ObjectName = generateAprsName(obj.ObjectName, all, ''); autoNamed = true; }
  if (obj.ObjectName) takenInImport.add(obj.ObjectName);
  if (autoNamed && !obj.Comment && originalName) obj.Comment = originalName.slice(0, 40);
  if (!obj.ObjectName) errs.push('missing ObjectName');
  if (typeof obj.Latitude !== 'number') errs.push('missing Latitude');
  if (typeof obj.Longitude !== 'number') errs.push('missing Longitude');
  if (typeof obj.Latitude === 'number' && Math.abs(obj.Latitude) > 90) errs.push('Latitude out of range');
  if (typeof obj.Longitude === 'number' && Math.abs(obj.Longitude) > 180) errs.push('Longitude out of range');
  if (obj.SymbolTable.length !== 1) errs.push('SymbolTable must be 1 char');
  if (obj.SymbolID.length !== 1) errs.push('SymbolID must be 1 char');
  return { ok: !errs.length, object: obj, errors: errs, originalName, autoNamed, collides: storeNames.has(obj.ObjectName) };
}

let importPreview = [];
function renderImportPreview() {
  const tbody = $('#import-preview-tbody');
  tbody.replaceChildren();
  let okN = 0, badN = 0, colN = 0;
  importPreview.forEach((row, idx) => {
    row.ok ? okN++ : badN++;
    if (row.collides) colN++;
    const o = row.object;
    const box = el('input', { type: 'checkbox', title: 'Include this row in the import' });
    box.checked = row.selected; box.disabled = !row.ok;
    box.addEventListener('change', () => { row.selected = box.checked; updateCommitButton(); });
    let action;
    if (!row.ok) action = el('span', { class: 'muted' }, '—');
    else if (row.collides) {
      action = el('select', { title: 'An object with this name already exists: skip the row, overwrite the existing object, or import under a _2 suffix' }, el('option', { value: 'skip' }, 'Skip'), el('option', { value: 'overwrite' }, 'Overwrite'), el('option', { value: 'rename' }, 'Rename (_2)'));
      action.value = row.action || 'skip';
      action.addEventListener('change', () => { row.action = action.value; });
    } else action = el('span', { class: 'muted', title: 'New object; it will be created disabled' }, 'create');
    const statusCell = row.ok ? (row.collides ? el('span', { class: 'chip due', title: 'Name already in your list; choose what to do in the last column' }, 'exists') : el('span', { class: 'chip live', title: 'Valid row' }, 'ready'))
      : el('span', { class: 'chip killp', title: row.errors.join('; ') }, 'invalid');
    const nameInput = el('input', { type: 'text', maxlength: 9, class: 'import-name-input', value: o.ObjectName || '', title: 'Edit the on-air name (max 9 characters)' });
    nameInput.addEventListener('change', () => {
      o.ObjectName = nameInput.value.trim().toUpperCase();
      row.collides = objects.some((x) => x.ObjectName === o.ObjectName);
      row.errors = row.errors.filter((e) => !e.startsWith('ObjectName') && !e.startsWith('missing ObjectName'));
      if (!o.ObjectName || o.ObjectName.length > 9) row.errors.push('ObjectName must be 1-9 chars');
      row.ok = !row.errors.length;
      renderImportPreview();
    });
    const nameCell = el('div', {}, nameInput, row.autoNamed && row.originalName ? el('div', { class: 'import-name-original', title: row.originalName }, `from: ${row.originalName}`) : null);
    tbody.appendChild(el('tr', { class: row.ok ? '' : 'invalid' },
      el('td', {}, box), el('td', {}, nameCell), el('td', {}, symSprite(o.SymbolTable, o.SymbolID)),
      el('td', {}, typeof o.Latitude === 'number' ? o.Latitude.toFixed(4) : '—'),
      el('td', {}, typeof o.Longitude === 'number' ? o.Longitude.toFixed(4) : '—'),
      el('td', {}, `${o.IntervalMinutes}m`), el('td', { title: o.Comment }, (o.Comment || '').slice(0, 24)),
      el('td', {}, statusCell), el('td', {}, action)));
  });
  $('#import-preview-wrap').hidden = false;
  $('#import-summary').textContent = `${importPreview.length} row(s) · ${okN} valid · ${badN} invalid · ${colN} already exist. Hover “invalid” for the reason.`;
  updateCommitButton();
}
function updateCommitButton() {
  const n = importPreview.filter((r) => r.ok && r.selected).length;
  $('#import-commit').disabled = n === 0;
  $('#import-commit').textContent = n ? `Import ${n} row(s)` : 'Import';
}
async function doImportCommit() {
  const existing = new Set(objects.map((o) => o.ObjectName));
  let created = 0, over = 0, renamed = 0, skipped = 0, failed = 0;
  for (const row of importPreview) {
    if (!row.ok || !row.selected) continue;
    let target = row.object.ObjectName;
    if (row.collides) {
      if (row.action === 'skip' || !row.action) { skipped++; continue; }
      if (row.action === 'rename') {
        let n = 2, cand;
        do { cand = (target.slice(0, 9 - String(n).length - 1) + '_' + n); n++; } while (existing.has(cand) && n < 100);
        target = cand;
      }
    }
    try {
      await API.upsert(target, { ...row.object, ObjectName: target });
      existing.add(target);
      if (row.collides && row.action === 'overwrite') over++; else if (row.collides) renamed++; else created++;
    } catch (e) { failed++; toast(`import ${target}: ${e.message}`, 'err', 6000); }
  }
  toast(`Imported: ${created} created · ${renamed} renamed · ${over} overwritten · ${skipped} skipped${failed ? ` · ${failed} failed` : ''}`, failed ? 'warn' : 'ok', 6000);
  resetImport();
  showTab('objects');
}
function resetImport() {
  importPreview = [];
  $('#import-text').value = '';
  $('#import-file').value = '';
  $('#import-preview-wrap').hidden = true;
  $('#import-error').hidden = true;
  updateCommitButton();
}
$('#import-parse').addEventListener('click', async () => {
  $('#import-error').hidden = true;
  let text = $('#import-text').value.trim();
  if (!text) {
    const f = $('#import-file').files[0];
    if (!f) { $('#import-error').textContent = 'Choose a CSV file or paste CSV text first.'; $('#import-error').hidden = false; return; }
    text = await f.text();
  }
  try {
    const { headers, rows } = parseCSV(text);
    if (!headers.some((h) => CSV_HEADER_MAP[h.toLowerCase().replace(/[^a-z]/g, '')] === 'ObjectName')) {
      throw new Error(`The CSV needs an "ObjectName" (or "Name") column. Found: ${headers.join(', ')}`);
    }
    const storeNames = new Set(objects.map((o) => o.ObjectName));
    const taken = new Set();
    const tag = deriveStationTag(status && status.station.callsign);
    importPreview = rows.filter((r) => r.some((f) => f.trim())).map((r) => {
      const v = validateImportRow(headers, r, storeNames, taken, tag);
      v.selected = v.ok; v.action = v.collides ? 'skip' : null;
      return v;
    });
    renderImportPreview();
  } catch (e) { $('#import-error').textContent = e.message; $('#import-error').hidden = false; $('#import-preview-wrap').hidden = true; }
});
$('#import-commit').addEventListener('click', doImportCommit);
$('#import-cancel').addEventListener('click', resetImport);
$('#import-select-all').addEventListener('change', () => { importPreview.forEach((r) => { if (r.ok) r.selected = $('#import-select-all').checked; }); renderImportPreview(); });
$('#import-template-link').addEventListener('click', (ev) => {
  ev.preventDefault();
  const tmpl = 'ObjectName,Latitude,Longitude,SymbolTable,SymbolID,Comment,IntervalMinutes\nSHELTER1,33.81,-84.34,/,h,Red Cross Shelter,15\nDeKalb County Fire Station 3,33.79,-84.32,/,d,,30\n';
  const a = el('a', { href: URL.createObjectURL(new Blob([tmpl], { type: 'text/csv' })), download: 'emcomm-objects-template.csv' });
  document.body.appendChild(a); a.click(); a.remove();
});

// ---------- Bootstrap ----------

(async function init() {
  initMap();
  clearForm();
  await refreshStatus();
  try {
    (status.recent || []).forEach((e) => {
      if (e.type === 'packet' && e.packet) addPacket(e.packet, e.when);
      else if (e.type === 'state' && e.state) addStateLine(e.state, e.when);
    });
    (await API.logs()).forEach((e) => pushFeed($('#log-list'), feedItem(e.when, `lvl-${e.level}`, [e.message])));
    unread = 0; $('#act-badge').hidden = true;
  } catch { /* history is optional */ }
  await reload(true);
  startEvents();
  setInterval(tick, 1000);
  if (status && status.setup_needed) openSettings();
})();
