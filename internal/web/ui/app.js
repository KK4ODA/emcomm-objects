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
  kill:    (name) => fetch(`/api/objects/${encodeURIComponent(name)}/kill`, {method: 'POST'}).then(jsonOrThrow),
  revive:  (name) => fetch(`/api/objects/${encodeURIComponent(name)}/revive`, {method: 'POST'}).then(jsonOrThrow),
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
let stationCallsign = '';  // populated from /api/config; used for CSV auto-naming

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

// markerStateByName remembers Killed and beacon-timing info per marker so
// updateMarkerLabels() can refresh tooltips every second without rerendering
// the whole layer.
const markerStateByName = new Map();

function refreshMarkers() {
  markerLayer.clearLayers();
  markersByName.clear();
  markerStateByName.clear();
  const points = [];
  for (const o of objects) {
    const killed = (o.Status === 'killed');
    const sym = symbolMarkerHtml(o.SymbolTable, o.SymbolID, killed);
    const icon = L.divIcon({
      html: sym,
      className: 'aprs-marker' + (killed ? ' killed' : ''),
      iconSize: [24, 24],
      iconAnchor: [12, 12],
      popupAnchor: [0, -14],
    });
    const m = L.marker([o.Latitude, o.Longitude], {icon, title: o.ObjectName});
    const popup = el('div', {class: 'obj-marker-popup'},
      el('b', {}, o.ObjectName),
      el('div', {class: 'info'}, `${o.SymbolTable}${o.SymbolID} · ${o.Comment || ''}`),
      el('div', {}, el('button', {onclick: () => loadIntoForm(o)}, 'Edit'),
                   ' ',
                   el('button', {class: 'secondary', onclick: () => beaconNow(o.ObjectName)}, 'Beacon now')),
    );
    m.bindPopup(popup);
    m.bindTooltip(beaconLabel(o), {
      permanent: true,
      direction: 'right',
      offset: [14, 0],
      className: 'beacon-label' + (killed ? ' killed' : ''),
    });
    markerLayer.addLayer(m);
    markersByName.set(o.ObjectName, m);
    markerStateByName.set(o.ObjectName, {
      killed,
      enabled: o.Enabled,
      intervalMinutes: o.IntervalMinutes,
      lastBeacon: o.LastBeacon,
      expiresAt: o.ExpiresAt,
    });
    points.push([o.Latitude, o.Longitude]);
  }
  if (points.length > 0) {
    map.fitBounds(L.latLngBounds(points), {padding: [30, 30], maxZoom: 14});
  }
}

// symbolMarkerHtml returns the inline HTML for one map pin. Killed pins get
// a CSS class that handles greyscale + X overlay (style.css).
function symbolMarkerHtml(table, code, killed) {
  const spriteUrl = table === '/' ? 'symbols/aprs-symbols-24-0.png' : 'symbols/aprs-symbols-24-1.png';
  const n = (code && code.length > 0) ? code.charCodeAt(0) - 33 : -1;
  let bg = '';
  if (n >= 0 && n < 96) {
    const row = Math.floor(n / 16);
    const col = n % 16;
    bg = `background-image:url(${spriteUrl});background-position:-${col*24}px -${row*24}px;`;
  }
  return `<span class="marker-sprite" style="${bg}"></span>`;
}

// beaconLabel returns the text shown next to a marker.
//   live + enabled + interval > 0  →  "next: 1m 23s" or "due now"
//   live + disabled or 0 interval  →  "disabled" or "manual"
//   killed                         →  "killed"
function beaconLabel(o) {
  if (o.Status === 'killed') {
    return o.KillBeaconsLeft > 0
      ? `killing (${o.KillBeaconsLeft} left)`
      : 'killed';
  }
  if (!o.Enabled || o.IntervalMinutes <= 0) {
    return 'manual';
  }
  return 'next: ' + nextBeaconText(o.LastBeacon, o.IntervalMinutes, Date.now());
}

// nextBeaconText returns "1m 23s", "now", "due now (12s ago)", etc.
function nextBeaconText(lastBeacon, intervalMinutes, nowMs) {
  if (!lastBeacon || lastBeacon.startsWith('0001-')) return 'now';
  const last = Date.parse(lastBeacon);
  if (isNaN(last)) return '?';
  const due = last + intervalMinutes * 60 * 1000;
  const ms = due - nowMs;
  if (ms <= 0) {
    const overdueSec = Math.round(-ms / 1000);
    return `due now (${formatDuration(overdueSec)} ago)`;
  }
  return formatDuration(Math.round(ms / 1000));
}

function formatDuration(sec) {
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

// updateMarkerLabels refreshes every marker's tooltip text in place. Cheap
// (just innerHTML on the tooltip element); safe to call once per second.
function updateMarkerLabels() {
  const now = Date.now();
  for (const [name, m] of markersByName) {
    const st = markerStateByName.get(name);
    if (!st) continue;
    let txt;
    if (st.killed) {
      txt = 'killed';
    } else if (!st.enabled || st.intervalMinutes <= 0) {
      txt = 'manual';
    } else {
      txt = 'next: ' + nextBeaconText(st.lastBeacon, st.intervalMinutes, now);
    }
    const tt = m.getTooltip();
    if (tt && tt.getContent() !== txt) tt.setContent(txt);
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
    const killed = (o.Status === 'killed');
    const rowClass = killed ? 'obj-killed' : (o.Enabled ? '' : 'obj-disabled');
    const tr = el('tr', {class: rowClass},
      el('td', {}, o.ObjectName),
      el('td', {class: 'symbol-cell'}, symSprite(o.SymbolTable, o.SymbolID)),
      el('td', {}, o.Latitude.toFixed(5)),
      el('td', {}, o.Longitude.toFixed(5)),
      el('td', {}, `${o.IntervalMinutes}m`),
      el('td', {}, fmtLastBeacon(o.LastBeacon)),
      el('td', {}, statusBadge(o)),
      el('td', {class: 'obj-actions'},
        actionButtons(o, killed),
      ),
    );
    tbody.appendChild(tr);
  }
}

// statusBadge returns an inline element describing the object's lifecycle
// state: live / kill-pending / killed / disabled / expires-in-X.
function statusBadge(o) {
  if (o.Status === 'killed') {
    if (o.KillBeaconsLeft > 0) {
      return el('span', {class: 'badge badge-killing'},
        `killing (${o.KillBeaconsLeft} left)`);
    }
    return el('span', {class: 'badge badge-killed'}, 'killed');
  }
  if (!o.Enabled) {
    return el('span', {class: 'badge badge-off'}, 'disabled');
  }
  if (o.ExpiresAt && !o.ExpiresAt.startsWith('0001-')) {
    const exp = new Date(o.ExpiresAt);
    if (!isNaN(exp)) {
      const ms = exp.getTime() - Date.now();
      if (ms <= 0) {
        return el('span', {class: 'badge badge-expiring'}, 'expired (pending kill)');
      }
      const mins = Math.round(ms / 60000);
      if (mins < 60) return el('span', {class: 'badge badge-warn'}, `expires in ${mins}m`);
      if (mins < 24 * 60) return el('span', {class: 'badge badge-live'}, `expires in ${Math.round(mins/60)}h`);
      return el('span', {class: 'badge badge-live'}, `expires ${exp.toLocaleDateString()}`);
    }
  }
  return el('span', {class: 'badge badge-live'}, 'live');
}

// actionButtons returns the per-row action buttons. Killed objects show
// Revive (put back on the air) + Delete (remove from local store — does
// NOT un-kill on the network).
function actionButtons(o, killed) {
  if (killed) {
    return el('span', {},
      twoStepButton('Revive', 'primary', () => reviveNow(o.ObjectName)),
      el('button', {class: 'secondary', onclick: () => deleteRow(o.ObjectName)}, 'Delete'),
    );
  }
  return el('span', {},
    el('button', {onclick: () => beaconNow(o.ObjectName)}, 'Beacon'),
    el('button', {class: 'secondary', onclick: () => loadIntoForm(o)}, 'Edit'),
    twoStepButton('Kill', 'danger', () => killNow(o.ObjectName)),
  );
}

// twoStepButton: first click reveals "Click to confirm" for 5s with pulse
// animation; second click runs onConfirm. Used for any state-changing action
// where an accidental single click would be costly.
//
// `variant`: 'danger' (red) or 'primary' (green). Both pulse when armed.
function twoStepButton(label, variant, onConfirm) {
  const btn = el('button', {class: variant}, label);
  let armed = false;
  let timer = null;
  const disarm = () => {
    armed = false;
    btn.textContent = label;
    btn.classList.remove('armed');
    if (timer) { clearTimeout(timer); timer = null; }
  };
  btn.addEventListener('click', async () => {
    if (!armed) {
      armed = true;
      btn.textContent = 'Click to confirm';
      btn.classList.add('armed');
      timer = setTimeout(disarm, 5000);
      return;
    }
    disarm();
    await onConfirm();
  });
  return btn;
}

async function killNow(name) {
  try {
    const resp = await API.kill(name);
    if (resp && resp.note) {
      logLine('warn', `${name}: ${resp.note}`);
    } else {
      logLine('info', `${name}: kill sequence started (3 packets, ~35s apart)`);
    }
    await reload();
  } catch (e) {
    logLine('error', `kill ${name}: ${e.message}`);
  }
}

async function reviveNow(name) {
  try {
    await API.revive(name);
    logLine('info', `${name}: revived — live beacon on next tick (within ~10s)`);
    await reload();
  } catch (e) {
    logLine('error', `revive ${name}: ${e.message}`);
  }
}

async function deleteRow(name) {
  if (!confirm(`Delete ${name} from local store? (Does NOT un-kill on the APRS network.)`)) return;
  try {
    await API.remove(name);
    await reload();
  } catch (e) {
    logLine('error', `delete ${name}: ${e.message}`);
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

// Path field has a preset dropdown + custom text input that appears when
// "Custom…" is chosen. Values mapped into Object.Path:
//   ""               → use station default (server config)
//   "-"              → explicit direct (no digipeater)
//   "WIDE1-1" etc.   → that path
// PATH_PRESETS lists every value that has a dedicated <option> in the select;
// anything else is shown via the Custom… input.
const PATH_PRESETS = ['', '-', 'WIDE1-1', 'WIDE1-1,WIDE2-1', 'WIDE2-1', 'WIDE2-2'];

function getPath() {
  const preset = $('#f-path-preset').value;
  if (preset === '__custom__') return $('#f-path-custom').value.trim();
  return preset;
}

function setPath(val) {
  const customWrap = $('#f-path-custom-wrap');
  const customInput = $('#f-path-custom');
  if (PATH_PRESETS.includes(val)) {
    $('#f-path-preset').value = val;
    customWrap.hidden = true;
    customInput.value = '';
  } else {
    $('#f-path-preset').value = '__custom__';
    customWrap.hidden = false;
    customInput.value = val;
  }
}

$('#f-path-preset').addEventListener('change', () => {
  const isCustom = $('#f-path-preset').value === '__custom__';
  $('#f-path-custom-wrap').hidden = !isCustom;
  if (isCustom) $('#f-path-custom').focus();
});

function clearForm() {
  $('#f-original-name').value = '';
  $('#f-name').value = '';
  $('#f-name').disabled = false;
  $('#f-lat').value = '';
  $('#f-lon').value = '';
  $('#f-table').value = '/';
  $('#f-sym').value = 'r';
  updateSymPreview();
  $('#f-comment').value = '';
  setPath('');
  $('#f-expires').value = '';
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
  updateSymPreview();
  $('#f-comment').value = o.Comment || '';
  setPath(o.Path || '');
  $('#f-expires').value = expiresAtToInput(o.ExpiresAt);
  $('#f-interval').value = o.IntervalMinutes;
  $('#f-enabled').checked = !!o.Enabled;
  $('#f-delete').hidden = false;
  $('#form-error').hidden = true;
  showTab('add');
}

// ExpiresAt round-trip:
//  Server stores UTC RFC3339 (or our zero sentinel "0001-...").
//  <input type="datetime-local"> uses "YYYY-MM-DDTHH:MM" in *local* time.
// expiresAtToInput converts server → input; expiresAtFromInput goes the other way.
function expiresAtToInput(s) {
  if (!s || s.startsWith('0001-')) return '';
  const d = new Date(s);
  if (isNaN(d)) return '';
  // Local-time YYYY-MM-DDTHH:MM (no seconds, no timezone — datetime-local wants this exact shape).
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function expiresAtFromInput(s) {
  if (!s) return '0001-01-01T00:00:00'; // server's "no expiry" sentinel
  const d = new Date(s); // browser interprets datetime-local as local time
  if (isNaN(d)) return '0001-01-01T00:00:00';
  return d.toISOString(); // RFC3339 UTC
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
    Path: getPath(),
    ExpiresAt: expiresAtFromInput($('#f-expires').value),
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

// ---------- Symbol picker ----------

// Names for the most common APRS symbols (APRS Protocol Reference v1.0.1 ch.5).
// Sprite cell at index n maps to symbol code String.fromCharCode(33+n);
// 96 cells per table arranged 16-wide. Unnamed cells display the code only.
const SYM_NAMES_PRIMARY = {
  '!':'Police/Sheriff','"':'reserved','#':'Digi','$':'Phone','%':'DX cluster',
  '&':'HF Gateway',"'":'Small aircraft','(':'Mobile satellite','(':'Mobile satellite',
  ')':'Wheelchair','*':'Snowmobile','+':'Red Cross',',':'Boy Scouts','-':'House (VHF)',
  '.':'X','/':'Red dot',':':'Fire','`':'Dish antenna',
  '0':'Number 0',';':'Park / picnic area','<':'Motorcycle','=':'Railroad engine',
  '>':'Car','?':'File server','@':'HC FUTURE predict','A':'Aid station','B':'BBS',
  'C':'Canoe','E':'Eyeball','F':'Tractor','G':'Grid Square','H':'Hotel','I':'TCP/IP',
  'K':'School','L':'PC user','M':'MacAPRS','N':'NTS station','O':'Balloon','P':'Police',
  'R':'REC vehicle','S':'Shuttle','T':'SSTV','U':'Bus','V':'ATV','W':'Weather station',
  'X':'Helicopter','Y':'Yacht (sail)','Z':'WinAPRS','[':'Jogger','\\':'Triangle',
  ']':'PBBS','^':'Large aircraft','_':'Weather site','a':'Ambulance','b':'Bike',
  'c':'Incident Cmd Post','d':'Fire station','e':'Horse','f':'Fire truck','g':'Glider',
  'h':'Hospital','i':'IOTA','j':'Jeep','k':'Truck','l':'Laptop','m':'Mic-E repeater',
  'n':'Node','o':'Emerg Op Center','p':'Rover (dog)','q':'Grid square shown','r':'Antenna',
  's':'Power boat','t':'Truck stop','u':'18-wheeler','v':'Van','w':'Water station',
  'x':'xAPRS','y':'Yagi','z':'Shelter','{':'reserved','|':'TNC stream',
  '}':'reserved','~':'TNC stream',
};
const SYM_NAMES_ALTERNATE = {
  '!':'Emergency','"':'reserved','#':'Digi (green star)','$':'Bank or ATM','%':'reserved',
  '&':'Crossing','(':'Cloudy',')':'Firenet MEO','*':'Snow','+':'Church',',':'Girl Scouts',
  '-':'House (HF)','.':'Ambiguous','/':'Waypoint','0':'Circle','3':'Triangle',
  ':':'Hail',';':'Park / picnic','<':'Advisory','>':'Car (alt)','?':'Info Kiosk',
  '@':'Hurricane','A':'Avalanche','B':'reserved','C':'Coast Guard','D':'Drizzle',
  'E':'Smoke','F':'Freezing rain','G':'Snow shower','H':'Haze','I':'Rain shower',
  'J':'Lightning','K':'Kenwood HT','L':'Lighthouse','M':'Military','N':'Nav buoy',
  'O':'Rocket','P':'Parking','Q':'Quake','R':'Restaurant','S':'Satellite','T':'Thunderstorm',
  'U':'Sunny','V':'VORTAC','W':'NWS site','X':'Pharmacy','Y':'reserved','Z':'reserved',
  '[':'Wall cloud','^':'Aircraft (alt)','_':'Weather flag','`':'Rain','a':'ARRL',
  'b':'Blowing dust','c':'Civil Defense','d':'DX spot','e':'Sleet','f':'Funnel cloud',
  'g':'Gale','h':'Store','i':'Black diamond','j':'Work zone','k':'4WD','l':'Area locations',
  'm':'Value sign','n':'Triangle (alt)','o':'Small circle','p':'Partly cloudy',
  'r':'Restrooms','s':'Ship','t':'Tornado','u':'Truck (alt)','v':'Van (alt)','w':'Flooding',
  'y':'Skywarn','z':'Shelter (alt)',
};

function symbolName(table, code) {
  const m = table === '/' ? SYM_NAMES_PRIMARY : SYM_NAMES_ALTERNATE;
  return m[code] || '';
}

// symSprite returns a span element with the right sprite painted in.
// Used in both the form preview and the objects table.
function symSprite(table, code) {
  const spriteUrl = table === '/' ? 'symbols/aprs-symbols-24-0.png' : 'symbols/aprs-symbols-24-1.png';
  const n = (code && code.length > 0) ? code.charCodeAt(0) - 33 : -1;
  const name = symbolName(table, code);
  const title = name ? `${table}${code} — ${name}` : `${table}${code}`;
  const span = el('span', {class: 'sym-sprite', title});
  if (n >= 0 && n < 96) {
    const row = Math.floor(n / 16);
    const col = n % 16;
    span.style.backgroundImage = `url(${spriteUrl})`;
    span.style.backgroundPosition = `-${col * 24}px -${row * 24}px`;
  }
  return span;
}

// renderSymGrid populates a grid div with 96 sprite cells for one table.
// onClick fires with (table, code).
function renderSymGrid(div, table) {
  div.replaceChildren();
  const spriteUrl = table === '/' ? 'symbols/aprs-symbols-24-0.png' : 'symbols/aprs-symbols-24-1.png';
  for (let n = 0; n < 96; n++) {
    const code = String.fromCharCode(33 + n);
    const row = Math.floor(n / 16);
    const col = n % 16;
    const name = symbolName(table, code);
    const title = name ? `${table}${code} — ${name}` : `${table}${code}`;
    const cell = el('button', {
      type: 'button',
      class: 'sym-cell',
      title,
      dataset: {table, code},
    });
    cell.style.backgroundImage = `url(${spriteUrl})`;
    cell.style.backgroundPosition = `-${col * 24}px -${row * 24}px`;
    cell.addEventListener('click', () => pickSymbol(table, code));
    div.appendChild(cell);
  }
}

// updateSymPreview re-paints the form's symbol preview from current hidden values.
function updateSymPreview() {
  const table = $('#f-table').value;
  const code = $('#f-sym').value;
  const preview = $('#f-sym-preview');
  const codeEl = $('#f-sym-code');
  const spriteUrl = table === '/' ? 'symbols/aprs-symbols-24-0.png' : 'symbols/aprs-symbols-24-1.png';
  const n = code.charCodeAt(0) - 33;
  if (n < 0 || n >= 96) {
    preview.style.backgroundImage = '';
    codeEl.textContent = `${table}${code}  (out of range)`;
    return;
  }
  const row = Math.floor(n / 16);
  const col = n % 16;
  preview.style.backgroundImage = `url(${spriteUrl})`;
  preview.style.backgroundPosition = `-${col * 24}px -${row * 24}px`;
  const name = symbolName(table, code);
  codeEl.textContent = name ? `${table}${code} — ${name}` : `${table}${code}`;
  // Keep the advanced manual field in sync (for users who prefer to type).
  $('#f-sym-manual').value = `${table}${code}`;
}

function pickSymbol(table, code) {
  $('#f-table').value = table;
  $('#f-sym').value = code;
  updateSymPreview();
  closeSymModal();
}

function openSymModal() {
  $('#sym-modal').hidden = false;
  document.body.style.overflow = 'hidden';
  // Lazy-render the grids the first time.
  if (!$('#sym-grid-primary').firstChild) {
    renderSymGrid($('#sym-grid-primary'), '/');
    renderSymGrid($('#sym-grid-alternate'), '\\');
  }
}
function closeSymModal() {
  $('#sym-modal').hidden = true;
  document.body.style.overflow = '';
}

$('#f-sym-pick').addEventListener('click', openSymModal);
$('#f-sym-display').addEventListener('click', (ev) => {
  // Click the preview/code area also opens the modal (button still works on its own).
  if (ev.target.id !== 'f-sym-pick') openSymModal();
});
$('#sym-modal .modal-close').addEventListener('click', closeSymModal);
$('#sym-modal').addEventListener('click', (ev) => {
  if (ev.target.id === 'sym-modal') closeSymModal(); // backdrop click
});
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && !$('#sym-modal').hidden) closeSymModal();
});

// Manual override: if user types a 2-char code, parse and update.
$('#f-sym-manual').addEventListener('input', () => {
  const v = $('#f-sym-manual').value;
  if (v.length === 2 && (v[0] === '/' || v[0] === '\\')) {
    $('#f-table').value = v[0];
    $('#f-sym').value = v[1];
    updateSymPreview();
  } else if (v.length === 1) {
    // Just symbol code; keep current table.
    $('#f-sym').value = v;
    updateSymPreview();
  }
});

// ---------- CSV import ----------

// Headers we accept in any case, mapped to canonical Object field names.
// Required: ObjectName + Latitude + Longitude. Others default.
const CSV_HEADER_MAP = {
  objectname: 'ObjectName', name: 'ObjectName',
  latitude: 'Latitude', lat: 'Latitude',
  longitude: 'Longitude', lon: 'Longitude', lng: 'Longitude',
  symboltable: 'SymbolTable', table: 'SymbolTable',
  symbolid: 'SymbolID', symbol: 'SymbolID', sym: 'SymbolID',
  comment: 'Comment',
  intervalminutes: 'IntervalMinutes', interval: 'IntervalMinutes',
};

// parseCSV handles quoted fields, commas-in-quotes, CR/LF/CRLF line endings.
// Returns {headers, rows} or throws Error with line/col on malformed input.
function parseCSV(text) {
  const out = [];
  let row = [];
  let field = '';
  let inQuotes = false;
  let i = 0;
  const n = text.length;
  while (i < n) {
    const c = text[i];
    if (inQuotes) {
      if (c === '"') {
        if (i + 1 < n && text[i + 1] === '"') { field += '"'; i += 2; continue; }
        inQuotes = false; i++; continue;
      }
      field += c; i++; continue;
    }
    if (c === '"') { inQuotes = true; i++; continue; }
    if (c === ',') { row.push(field); field = ''; i++; continue; }
    if (c === '\r') { i++; continue; } // ignore; handled at \n or alone
    if (c === '\n') { row.push(field); out.push(row); row = []; field = ''; i++; continue; }
    field += c; i++;
  }
  // Last field/row (no trailing newline).
  if (field.length > 0 || row.length > 0) {
    row.push(field);
    out.push(row);
  }
  // Drop empty trailing rows (e.g. trailing blank line).
  while (out.length > 0 && out[out.length - 1].length === 1 && out[out.length - 1][0] === '') {
    out.pop();
  }
  if (out.length === 0) throw new Error('empty CSV');
  const headers = out[0].map((h) => h.trim());
  const rows = out.slice(1);
  return { headers, rows };
}

// deriveStationTag returns a short uppercase tag derived from the operator's
// callsign — used to make auto-generated names less likely to collide with
// the same name beaconed by another station. e.g. "KK4ODA-12" → "OD".
// Returns "" if callsign is empty/odd-shaped.
function deriveStationTag(callsign) {
  if (!callsign) return '';
  const base = callsign.split('-')[0].replace(/[^A-Z0-9]/gi, '').toUpperCase();
  return base.slice(-2);
}

// generateAprsName produces a 1..9-char APRS-compatible object name from a
// long human name. Tries (in order) two abbreviation strategies, then
// truncation. Appends stationTag at the end (eating into the 9-char budget).
// Dedupes against existingNames by substituting a digit suffix on collision.
//
// Examples (with stationTag="OD"):
//   "DeKalb County Fire Station 1"        → "DCFS1OD"
//   "DeKalb County Fire Rescue Admin (HQ)"→ "DCFRAHQOD"  (9 chars, truncated)
//   "Red Cross Shelter Avondale"          → "RCSAOD"
//   "Atlanta Airport"                     → "AAOD"
function generateAprsName(longName, existingNames, stationTag) {
  const tag = stationTag || '';
  const budget = 9 - tag.length;
  if (budget < 1) return ''; // shouldn't happen

  // Strategy 1: take all uppercase letters and digits (preserves "DCFS12" style).
  let abbrev = (longName.match(/[A-Z0-9]/g) || []).join('');

  // Strategy 2: if S1 gave nothing usable, take first letter of each word
  // plus any standalone digit-only words.
  if (abbrev.length === 0) {
    abbrev = longName.split(/[\s_\-]+/).map(w => {
      if (/^\d+$/.test(w)) return w;
      return w.charAt(0).toUpperCase();
    }).join('');
  }

  // Sanitize: only A-Z, 0-9 in APRS object names (printable-ASCII spec, but
  // we keep to alnum for clarity).
  abbrev = abbrev.replace(/[^A-Z0-9]/g, '');

  // Truncate to budget.
  let base = abbrev.slice(0, budget);
  if (base === '') base = 'OBJ'; // fallback for purely non-alphanumeric inputs

  let candidate = base + tag;
  if (!existingNames.has(candidate)) return candidate;

  // Collision: substitute last char of base with 2..9, then A..Z.
  // 34 attempts before giving up.
  for (let i = 2; i < 36; i++) {
    const suffix = i < 10 ? String(i) : String.fromCharCode(65 + i - 10); // 2-9, then A-Z
    const trimmedBase = base.slice(0, Math.max(1, budget - 1));
    candidate = trimmedBase + suffix + tag;
    if (!existingNames.has(candidate)) return candidate;
  }
  // Total exhaustion (very unlikely): use first 9 chars of original and hope.
  return abbrev.slice(0, 9) || 'OBJ';
}

// validateImportRow returns { ok, object, errors[], originalName, autoNamed,
// collides } for one parsed CSV row.
//   storeNames: names that already exist in the persisted store (drives the
//     "collides" flag, so the UI shows skip/overwrite/rename for that row).
//   takenInImport: names taken by earlier rows of THIS import (mutated; auto-
//     naming uses storeNames ∪ takenInImport to avoid picking duplicates).
//   stationTag: 2-char suffix appended to auto-generated names; pass "" to skip.
function validateImportRow(headers, raw, storeNames, takenInImport, stationTag) {
  const obj = {
    ShowTooltip: true,
    SymbolTable: '/',
    SymbolID: 'r',
    Comment: '',
    IntervalMinutes: 30,
    Enabled: false, // import default: do not auto-beacon
    Path: '',
    LastBeacon: '0001-01-01T00:00:00',
  };
  const errs = [];
  for (let i = 0; i < headers.length; i++) {
    const key = CSV_HEADER_MAP[headers[i].toLowerCase()];
    if (!key) continue; // ignore unknown columns
    const v = (raw[i] || '').trim();
    if (v === '') continue;
    if (key === 'Latitude' || key === 'Longitude') {
      const f = parseFloat(v);
      if (isNaN(f)) { errs.push(`${key} not a number: "${v}"`); continue; }
      obj[key] = f;
    } else if (key === 'IntervalMinutes') {
      const n = parseInt(v, 10);
      if (isNaN(n) || n < 0) { errs.push(`IntervalMinutes invalid: "${v}"`); continue; }
      obj[key] = n;
    } else {
      obj[key] = v;
    }
  }

  // Auto-name long names. Keep the original for display + Comment.
  const originalName = obj.ObjectName || '';
  let autoNamed = false;
  // generateAprsName needs to avoid both store names AND names already taken
  // by earlier import rows in the same batch.
  const allTaken = new Set([...storeNames, ...takenInImport]);
  if (obj.ObjectName && obj.ObjectName.length > 9) {
    obj.ObjectName = generateAprsName(obj.ObjectName, allTaken, stationTag);
    autoNamed = true;
  } else if (obj.ObjectName && takenInImport.has(obj.ObjectName)) {
    // Two CSV rows share an explicit short name — dedupe the second.
    obj.ObjectName = generateAprsName(obj.ObjectName, allTaken, '');
    autoNamed = true; // operator should review the rename
  }
  if (obj.ObjectName) takenInImport.add(obj.ObjectName);

  // If Comment is empty and we auto-named, drop the original (truncated) in.
  // APRS spec recommends keeping object comments under ~43 chars for old TNCs.
  if (autoNamed && !obj.Comment && originalName) {
    obj.Comment = originalName.slice(0, 40);
  }

  // Validate required fields.
  if (!obj.ObjectName) errs.push('missing ObjectName');
  if (obj.ObjectName && obj.ObjectName.length > 9) errs.push('ObjectName > 9 chars');
  if (typeof obj.Latitude !== 'number') errs.push('missing Latitude');
  if (typeof obj.Longitude !== 'number') errs.push('missing Longitude');
  if (typeof obj.Latitude === 'number' && (obj.Latitude < -90 || obj.Latitude > 90)) errs.push('Latitude out of range');
  if (typeof obj.Longitude === 'number' && (obj.Longitude < -180 || obj.Longitude > 180)) errs.push('Longitude out of range');
  if (obj.SymbolTable.length !== 1) errs.push(`SymbolTable must be 1 char (got "${obj.SymbolTable}")`);
  if (obj.SymbolID.length !== 1) errs.push(`SymbolID must be 1 char (got "${obj.SymbolID}")`);
  return {
    ok: errs.length === 0,
    object: obj,
    errors: errs,
    originalName,
    autoNamed,
    // collides = the picked (possibly auto-generated) name matches something
    // already in the persisted store. Triggers per-row skip/overwrite/rename.
    collides: storeNames.has(obj.ObjectName),
  };
}

let importPreview = []; // [{object, ok, errors, collides, action, selected}]

function renderImportPreview() {
  const tbody = $('#import-preview-tbody');
  tbody.replaceChildren();
  let okCount = 0, errCount = 0, collisionCount = 0;
  importPreview.forEach((row, idx) => {
    if (row.ok) okCount++; else errCount++;
    if (row.collides) collisionCount++;
    const o = row.object;
    const selBox = el('input', {type: 'checkbox', dataset: {idx: String(idx)}});
    selBox.checked = row.selected;
    selBox.disabled = !row.ok;
    selBox.addEventListener('change', () => {
      importPreview[idx].selected = selBox.checked;
      updateCommitButton();
    });
    let actionCell;
    if (!row.ok) {
      actionCell = el('span', {class: 'muted'}, '—');
    } else if (row.collides) {
      const sel = el('select',
        {dataset: {idx: String(idx)}},
        el('option', {value: 'skip'}, 'Skip'),
        el('option', {value: 'overwrite'}, 'Overwrite'),
        el('option', {value: 'rename'}, 'Rename (suffix _2)'),
      );
      sel.value = row.action || 'skip';
      sel.addEventListener('change', () => { importPreview[idx].action = sel.value; });
      row.action = sel.value;
      actionCell = sel;
    } else {
      actionCell = el('span', {class: 'muted'}, 'create');
    }
    const statusCell = row.ok
      ? (row.collides
          ? el('span', {class: 'badge badge-warn'}, 'collision')
          : el('span', {class: 'badge badge-live'}, 'ready'))
      : el('span', {class: 'badge badge-killing', title: row.errors.join('; ')}, 'invalid');
    tbody.appendChild(el('tr', {class: row.ok ? '' : 'obj-disabled'},
      el('td', {}, selBox),
      el('td', {class: 'import-name-cell'}, importNameCell(idx, row)),
      el('td', {}, typeof o.Latitude === 'number' ? o.Latitude.toFixed(5) : '—'),
      el('td', {}, typeof o.Longitude === 'number' ? o.Longitude.toFixed(5) : '—'),
      el('td', {class: 'symbol-cell'}, symSprite(o.SymbolTable, o.SymbolID)),
      el('td', {}, `${o.IntervalMinutes}m`),
      el('td', {}, o.Comment || ''),
      el('td', {}, statusCell),
      el('td', {}, actionCell),
    ));
  });
  $('#import-preview-wrap').hidden = false;
  $('#import-summary').textContent =
    `${importPreview.length} row(s) parsed · ${okCount} valid · ${errCount} invalid · ${collisionCount} collisions. Hover an "invalid" badge for the reason.`;
  updateCommitButton();
}

// importNameCell renders an editable APRS name field with the long original
// shown subtly below (so the operator sees both the source-of-truth name
// and the short identifier going on the air). Editing the input live-updates
// the row's chosen ObjectName; tag indicates auto-generated rows.
function importNameCell(idx, row) {
  const input = el('input', {
    type: 'text',
    maxlength: 9,
    size: 9,
    class: 'import-name-input',
    value: row.object.ObjectName || '',
    title: 'Edit to override the auto-generated name (max 9 APRS chars)',
  });
  input.addEventListener('input', () => {
    // Update row state.
    row.object.ObjectName = input.value.trim();
    // Re-check collision against the persisted store only — intra-import
    // dedup was the parser's job; if the operator types a new collision
    // with another import row they can deal with it themselves.
    const storeNames = new Set(objects.map((o) => o.ObjectName));
    row.collides = storeNames.has(row.object.ObjectName);
    // Re-validate length.
    const goodLen = row.object.ObjectName.length >= 1 && row.object.ObjectName.length <= 9;
    if (!goodLen) {
      row.ok = false;
      row.errors = ['ObjectName must be 1-9 chars'];
    } else {
      row.ok = row.errors.filter((e) => !e.startsWith('ObjectName')).length === 0;
      if (row.ok) row.errors = [];
    }
    renderImportPreview(); // re-render to reflect status / commit-button changes
  });
  const wrap = el('div', {class: 'import-name-wrap'}, input);
  if (row.autoNamed && row.originalName) {
    wrap.appendChild(el('div', {
      class: 'import-name-original muted',
      title: row.originalName,
    }, `auto from: ${row.originalName.slice(0, 32)}${row.originalName.length > 32 ? '…' : ''}`));
  }
  return wrap;
}

function updateCommitButton() {
  const n = importPreview.filter((r) => r.ok && r.selected).length;
  const btn = $('#import-commit');
  btn.disabled = n === 0;
  btn.textContent = n === 0 ? 'Import selected rows' : `Import ${n} row(s)`;
}

async function doImportCommit() {
  const existing = new Set(objects.map((o) => o.ObjectName));
  let created = 0, overwritten = 0, renamed = 0, skipped = 0, failed = 0;
  for (const row of importPreview) {
    if (!row.ok || !row.selected) continue;
    let target = row.object.ObjectName;
    if (row.collides) {
      if (row.action === 'skip') { skipped++; continue; }
      if (row.action === 'rename') {
        // Find an unused suffix _2, _3, ...
        let suffix = 2;
        let candidate;
        do {
          candidate = (target + '_' + suffix).slice(0, 9); // APRS limit
          suffix++;
        } while (existing.has(candidate) && suffix < 100);
        existing.add(candidate);
        target = candidate;
      }
    }
    try {
      const body = {...row.object, ObjectName: target};
      await API.upsert(target, body);
      if (row.collides && row.action === 'overwrite') overwritten++;
      else if (row.collides && row.action === 'rename') renamed++;
      else created++;
    } catch (e) {
      failed++;
      logLine('error', `import ${target}: ${e.message}`);
    }
  }
  logLine('info', `Import complete: ${created} created · ${renamed} renamed · ${overwritten} overwritten · ${skipped} skipped${failed ? ' · ' + failed + ' failed' : ''}`);
  resetImport();
  await reload();
  showTab('objects');
}

function resetImport() {
  importPreview = [];
  $('#import-text').value = '';
  $('#import-file').value = '';
  $('#import-preview-wrap').hidden = true;
  $('#import-error').hidden = true;
  $('#import-commit').disabled = true;
  $('#import-commit').textContent = 'Import selected rows';
}

$('#import-parse').addEventListener('click', async () => {
  $('#import-error').hidden = true;
  let text = $('#import-text').value.trim();
  if (!text) {
    const f = $('#import-file').files[0];
    if (!f) {
      $('#import-error').textContent = 'No CSV file or pasted text.';
      $('#import-error').hidden = false;
      return;
    }
    text = await f.text();
  }
  try {
    const {headers, rows} = parseCSV(text);
    const known = Object.keys(CSV_HEADER_MAP);
    const lower = headers.map((h) => h.toLowerCase());
    if (!lower.some((h) => CSV_HEADER_MAP[h] === 'ObjectName')) {
      throw new Error(`CSV must have an "ObjectName" (or "Name") column. Got: ${headers.join(', ')}`);
    }
    const storeNames = new Set(objects.map((o) => o.ObjectName));
    const takenInImport = new Set();
    const stationTag = deriveStationTag(stationCallsign);
    importPreview = rows.map((r) => {
      const v = validateImportRow(headers, r, storeNames, takenInImport, stationTag);
      v.selected = v.ok;
      v.action = v.collides ? 'skip' : null;
      return v;
    });
    renderImportPreview();
  } catch (e) {
    $('#import-error').textContent = e.message;
    $('#import-error').hidden = false;
    $('#import-preview-wrap').hidden = true;
  }
});

$('#import-commit').addEventListener('click', doImportCommit);
$('#import-cancel').addEventListener('click', resetImport);
$('#import-select-all').addEventListener('change', () => {
  const checked = $('#import-select-all').checked;
  importPreview.forEach((r) => { if (r.ok) r.selected = checked; });
  renderImportPreview();
});

// CSV template download (data URL, no server roundtrip).
$('#import-template-link').addEventListener('click', (ev) => {
  ev.preventDefault();
  const tmpl = 'ObjectName,Latitude,Longitude,SymbolTable,SymbolID,Comment,IntervalMinutes\nDCFR_3,33.79,-84.32,/,r,DCFR Station 3,30\nSHELTER1,33.81,-84.34,/,h,Red Cross Shelter,15\n';
  const blob = new Blob([tmpl], {type: 'text/csv'});
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'emcomm-objects-template.csv';
  a.click();
  URL.revokeObjectURL(a.href);
});

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
        logLine(e.packet.killed ? 'kill' : 'packet',
          `${e.packet.killed ? 'KILL' : 'TX'} ${e.packet.object}: ${e.packet.info}`, e.when);
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
  updateSymPreview(); // paint initial /r preview
  try {
    const cfg = await API.config();
    stationCallsign = cfg.station_callsign || '';
    $('#station-info').textContent =
      `${cfg.station_callsign} → ${cfg.station_tocall}${cfg.station_path ? ' via ' + cfg.station_path : ''}  ·  KISS ${cfg.kiss_address}`;
  } catch (e) { logLine('error', `config: ${e.message}`); }
  try {
    const s = await API.status();
    setKissBadge(s.kiss_state || 'disconnected');
    // Replay any recent events as log entries.
    (s.recent || []).forEach((e) => {
      if (e.type === 'packet' && e.packet) {
        logLine(e.packet.killed ? 'kill' : 'packet',
          `${e.packet.killed ? 'KILL' : 'TX'} ${e.packet.object}: ${e.packet.info}`, e.when);
      } else if (e.type === 'state') {
        logLine(`state-${e.kiss_state}`, `KISS ${e.kiss_state}`, e.when);
      }
    });
  } catch (e) { logLine('error', `status: ${e.message}`); }
  await reload();
  startEvents();
  // Refresh marker countdown labels every second. Cheap: just text updates.
  setInterval(updateMarkerLabels, 1000);
})();
