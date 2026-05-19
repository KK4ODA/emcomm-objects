// emcomm-objects browser UI.
// Single-page vanilla JS. State lives server-side; this is a thin view.

const API = {
  objects: () => fetch('/api/objects').then(jsonOrThrow),
  upsert:  (name, body) => fetch(`/api/objects/${encodeURIComponent(name)}`, {
    method: 'PUT', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body),
  }).then(jsonOrThrow),
  remove:  (name) => fetch(`/api/objects/${encodeURIComponent(name)}`, {method: 'DELETE'}).then(noContent),
  beacon:  (name) => fetch(`/api/objects/${encodeURIComponent(name)}/beacon`, {method: 'POST'}).then(jsonOrThrow),
  status:  () => fetch('/api/status').then(jsonOrThrow),
  config:  () => fetch('/api/config').then(jsonOrThrow),
};

async function jsonOrThrow(r) {
  const text = await r.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!r.ok) {
    const msg = (data && data.error) ? data.error : `HTTP ${r.status}`;
    throw new Error(msg);
  }
  return data;
}
async function noContent(r) {
  if (!r.ok && r.status !== 204) {
    const text = await r.text();
    let data; try { data = JSON.parse(text); } catch {}
    throw new Error((data && data.error) || `HTTP ${r.status}`);
  }
  return null;
}

// ---------- State ----------

let objects = [];          // last fetched list
let map, markerLayer;
let markersByName = new Map();
let pickingForForm = false;

// ---------- DOM helpers ----------

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k === 'dataset') Object.assign(e.dataset, v);
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

function fmtLastBeacon(t) {
  if (!t || t.startsWith('0001-')) return 'never';
  const d = new Date(t);
  if (isNaN(d)) return t;
  const ago = Math.round((Date.now() - d.getTime()) / 1000);
  if (ago < 60) return `${ago}s ago`;
  if (ago < 3600) return `${Math.round(ago / 60)}m ago`;
  if (ago < 86400) return `${Math.round(ago / 3600)}h ago`;
  return d.toLocaleString();
}

// ---------- Tabs ----------

$$('.tab').forEach(btn => {
  btn.addEventListener('click', () => {
    const which = btn.dataset.tab;
    $$('.tab').forEach(t => t.classList.toggle('active', t === btn));
    $$('.tab-panel').forEach(p => p.classList.toggle('active', p.dataset.tabPanel === which));
  });
});

function showTab(name) {
  const btn = document.querySelector(`.tab[data-tab="${name}"]`);
  if (btn) btn.click();
}

// ---------- Map ----------

function initMap() {
  // Default center: DeKalb County, GA (roughly). Will recenter on first object.
  map = L.map('map').setView([33.8, -84.3], 11);
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '© OpenStreetMap',
  }).addTo(map);
  markerLayer = L.layerGroup().addTo(map);

  map.on('click', (e) => {
    if (!pickingForForm) return;
    $('#f-lat').value = e.latlng.lat.toFixed(6);
    $('#f-lon').value = e.latlng.lng.toFixed(6);
    pickingForForm = false;
    $('#f-pick-btn').textContent = 'Pick on map';
    map.getContainer().style.cursor = '';
  });
}

function refreshMarkers() {
  markerLayer.clearLayers();
  markersByName.clear();
  const points = [];
  for (const o of objects) {
    const m = L.marker([o.Latitude, o.Longitude], {title: o.ObjectName});
    const popup = el('div', {class: 'obj-marker-popup'},
      el('b', {}, o.ObjectName),
      el('div', {class: 'info'}, `${o.SymbolTable}${o.SymbolID} · ${o.Comment || ''}`),
      el('div', {}, el('button', {onclick: () => loadIntoForm(o)}, 'Edit'),
                   ' ',
                   el('button', {class: 'secondary', onclick: () => beaconNow(o.ObjectName)}, 'Beacon now')),
    );
    m.bindPopup(popup);
    markerLayer.addLayer(m);
    markersByName.set(o.ObjectName, m);
    points.push([o.Latitude, o.Longitude]);
  }
  if (points.length > 0) {
    map.fitBounds(L.latLngBounds(points), {padding: [30, 30], maxZoom: 14});
  }
}

// ---------- Object table ----------

function renderTable() {
  const tbody = $('#objects-tbody');
  tbody.replaceChildren();
  if (objects.length === 0) {
    $('#empty-msg').hidden = false;
    return;
  }
  $('#empty-msg').hidden = true;
  for (const o of objects) {
    const tr = el('tr', {class: o.Enabled ? '' : 'obj-disabled'},
      el('td', {}, o.ObjectName),
      el('td', {class: 'symbol-cell'}, `${o.SymbolTable}${o.SymbolID}`),
      el('td', {}, o.Latitude.toFixed(5)),
      el('td', {}, o.Longitude.toFixed(5)),
      el('td', {}, `${o.IntervalMinutes}m`),
      el('td', {}, fmtLastBeacon(o.LastBeacon)),
      el('td', {}, o.Enabled ? '✓' : ''),
      el('td', {class: 'obj-actions'},
        el('button', {onclick: () => beaconNow(o.ObjectName)}, 'Beacon'),
        el('button', {class: 'secondary', onclick: () => loadIntoForm(o)}, 'Edit'),
      ),
    );
    tbody.appendChild(tr);
  }
}

async function reload() {
  try {
    objects = await API.objects();
    renderTable();
    refreshMarkers();
  } catch (e) {
    logLine('error', e.message);
  }
}

// ---------- Add / edit form ----------

const form = $('#object-form');

function clearForm() {
  $('#f-original-name').value = '';
  $('#f-name').value = '';
  $('#f-name').disabled = false;
  $('#f-lat').value = '';
  $('#f-lon').value = '';
  $('#f-table').value = '/';
  $('#f-sym').value = 'r';
  $('#f-comment').value = '';
  $('#f-path').value = '';
  $('#f-interval').value = '30';
  $('#f-enabled').checked = true;
  $('#f-delete').hidden = true;
  $('#form-error').hidden = true;
  $('#form-error').textContent = '';
}

function loadIntoForm(o) {
  $('#f-original-name').value = o.ObjectName;
  $('#f-name').value = o.ObjectName;
  $('#f-name').disabled = true; // can't rename in v1
  $('#f-lat').value = o.Latitude;
  $('#f-lon').value = o.Longitude;
  $('#f-table').value = o.SymbolTable;
  $('#f-sym').value = o.SymbolID;
  $('#f-comment').value = o.Comment || '';
  $('#f-path').value = o.Path || '';
  $('#f-interval').value = o.IntervalMinutes;
  $('#f-enabled').checked = !!o.Enabled;
  $('#f-delete').hidden = false;
  $('#form-error').hidden = true;
  showTab('add');
}

form.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const body = {
    ObjectName: $('#f-name').value.trim(),
    ShowTooltip: true,
    SymbolTable: $('#f-table').value,
    SymbolID: $('#f-sym').value,
    Comment: $('#f-comment').value,
    IntervalMinutes: parseInt($('#f-interval').value, 10) || 0,
    Latitude: parseFloat($('#f-lat').value),
    Longitude: parseFloat($('#f-lon').value),
    Altitude: 0,
    LastBeacon: "0001-01-01T00:00:00",
    Enabled: $('#f-enabled').checked,
    Path: $('#f-path').value.trim(),
  };
  try {
    await API.upsert(body.ObjectName, body);
    clearForm();
    await reload();
    showTab('objects');
  } catch (e) {
    $('#form-error').textContent = e.message;
    $('#form-error').hidden = false;
  }
});

$('#f-cancel').addEventListener('click', () => { clearForm(); showTab('objects'); });

$('#f-delete').addEventListener('click', async () => {
  const name = $('#f-original-name').value;
  if (!name) return;
  if (!confirm(`Delete object "${name}"?`)) return;
  try {
    await API.remove(name);
    clearForm();
    await reload();
    showTab('objects');
  } catch (e) {
    $('#form-error').textContent = e.message;
    $('#form-error').hidden = false;
  }
});

$('#f-pick-btn').addEventListener('click', () => {
  pickingForForm = true;
  $('#f-pick-btn').textContent = 'Click on map…';
  map.getContainer().style.cursor = 'crosshair';
});

$('#reload-btn').addEventListener('click', reload);

$('#beacon-due-btn').addEventListener('click', async () => {
  // Trigger every enabled object whose interval has elapsed.
  const now = Date.now();
  const due = objects.filter(o => {
    if (!o.Enabled || o.IntervalMinutes <= 0) return false;
    if (!o.LastBeacon || o.LastBeacon.startsWith('0001-')) return true;
    const last = new Date(o.LastBeacon).getTime();
    return (now - last) >= o.IntervalMinutes * 60 * 1000;
  });
  for (const o of due) {
    try { await API.beacon(o.ObjectName); } catch (e) { logLine('error', `${o.ObjectName}: ${e.message}`); }
  }
  await reload();
});

async function beaconNow(name) {
  try {
    await API.beacon(name);
    await reload();
  } catch (e) {
    logLine('error', `${name}: ${e.message}`);
  }
}

// ---------- Activity log + SSE ----------

const logList = $('#log-list');
const MAX_LOG = 200;

function logLine(kind, text, when) {
  const t = (when ? new Date(when) : new Date()).toLocaleTimeString();
  const li = el('li', {},
    el('span', {class: 'log-time'}, t),
    el('span', {class: `log-${kind}`}, text),
  );
  logList.prepend(li);
  while (logList.children.length > MAX_LOG) logList.removeChild(logList.lastChild);
}

function setKissBadge(state) {
  const b = $('#kiss-badge');
  b.dataset.state = state;
  b.textContent = `KISS: ${state}`;
}

function startEvents() {
  const es = new EventSource('/api/events');
  es.addEventListener('packet', (ev) => {
    try {
      const e = JSON.parse(ev.data);
      if (e.packet) {
        logLine('packet', `TX ${e.packet.object}: ${e.packet.info}`, e.when);
        // Refresh the row's "last beacon" in place.
        reload();
      }
    } catch {}
  });
  es.addEventListener('state', (ev) => {
    try {
      const e = JSON.parse(ev.data);
      setKissBadge(e.kiss_state);
      logLine(`state-${e.kiss_state}`, `KISS ${e.kiss_state}`, e.when);
    } catch {}
  });
  es.onerror = () => {
    // Browser auto-reconnects EventSource. Nothing to do.
  };
}

// ---------- Bootstrap ----------

(async function init() {
  initMap();
  try {
    const cfg = await API.config();
    $('#station-info').textContent =
      `${cfg.station_callsign} → ${cfg.station_tocall}${cfg.station_path ? ' via ' + cfg.station_path : ''}  ·  KISS ${cfg.kiss_address}`;
  } catch (e) { logLine('error', `config: ${e.message}`); }
  try {
    const s = await API.status();
    setKissBadge(s.kiss_state || 'disconnected');
    // Replay any recent events as log entries.
    (s.recent || []).forEach((e) => {
      if (e.type === 'packet' && e.packet) {
        logLine('packet', `TX ${e.packet.object}: ${e.packet.info}`, e.when);
      } else if (e.type === 'state') {
        logLine(`state-${e.kiss_state}`, `KISS ${e.kiss_state}`, e.when);
      }
    });
  } catch (e) { logLine('error', `status: ${e.message}`); }
  await reload();
  startEvents();
})();
