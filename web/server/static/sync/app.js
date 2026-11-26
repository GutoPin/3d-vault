// Interfaz de "Mi carpeta". La lógica de sincronización está en engine.js.
import { Api, ApiError, Project, appUpdated, folderSelection, loadedVersion, markUpdated, folderTree, ignored, keyOf, underAny } from './engine.js';

const $ = (id) => document.getElementById(id);
const AUTO_EVERY = 60_000;
const STALE_LOCK = 24 * 3600_000;
const NEEDS_ME = new Set(['changed', 'new', 'moved', 'missing', 'error']);
const EVENT_KINDS = ['asset_created', 'version_created', 'locked', 'unlocked', 'moved', 'deleted', 'folder_created', 'folder_deleted', 'folder_renamed'];

const STATUS = {
  ok: 'Al día',
  locked: 'Bloqueado por ti',
  changed: 'Modificado, sin subir',
  new: 'Nuevo, sin subir',
  missing: 'Borrado en tu PC: falta confirmar',
  moved: 'Renombrado o movido',
  error: 'Error',
};

// Colores como en git: verde nuevo, amarillo modificado, rojo borrado, azul movido.
const GIT = {
  new: { cls: 'add', mark: '+', tip: 'Está en tu carpeta pero no en 3D-Vault. Nadie más lo ve hasta que pulses «Agregar…».' },
  changed: { cls: 'mod', mark: '~', tip: 'Lo cambiaste en tu PC. Los demás siguen viendo la versión anterior hasta que pulses «Subir…».' },
  missing: { cls: 'del', mark: '−', tip: 'Lo borraste de tu carpeta, pero sigue en 3D-Vault. Confirma el borrado o recupéralo.' },
  moved: { cls: 'mov', mark: '→', tip: 'Lo renombraste o moviste en tu carpeta. Muévelo también en el servidor para no perder su historial.' },
};

// Qué hace cada botón de una fila, para el tooltip.
const ACTION_TIPS = {
  upload: 'Lleva tu archivo a 3D-Vault como versión nueva. Te pedirá un comentario de qué cambió.',
  move: 'Hace el mismo cambio de nombre o carpeta en el servidor y conserva todas sus versiones.',
  delete: 'Lo borra también en 3D-Vault. Queda en la papelera del proyecto por si acaso.',
  restore: 'Vuelve a bajar a tu PC la última versión del servidor.',
  unlock: 'Deja el archivo libre para que otro pueda trabajar en él.',
  takeover: 'Lo tienes bloqueado en otra PC. Pásalo a esta para poder subir desde aquí.',
  lock: 'Avisa a los demás que lo estás editando. Nadie podrá subir encima hasta que lo liberes.',
  request: 'Le llega un aviso a quien lo tiene bloqueado para que lo suba y lo libere.',
};

function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  let exp = -1;
  do { n /= 1024; exp++; } while (n >= 1024 && exp < 3);
  return `${n.toFixed(1).replace('.', ',')} ${'KMGT'[exp]}B`;
}

const safeName = (name) => name.replace(/[<>:"/\\|?*\u0000-\u001f]/g, '_').replace(/[. ]+$/, '') || 'proyecto';

// ---------- almacenamiento local: carpeta elegida y versión de cada archivo ----------

const dbReady = new Promise((resolve, reject) => {
  const req = indexedDB.open('3dvault', 1);
  req.onupgradeneeded = () => {
    req.result.createObjectStore('kv');
    req.result.createObjectStore('files', { keyPath: 'id' }).createIndex('project', 'project');
  };
  req.onsuccess = () => resolve(req.result);
  req.onerror = () => reject(req.error);
});

async function idb(store, mode, fn) {
  const db = await dbReady;
  return new Promise((resolve, reject) => {
    const tx = db.transaction(store, mode);
    const req = fn(tx.objectStore(store));
    tx.oncomplete = () => resolve(req?.result);
    tx.onerror = () => reject(tx.error);
  });
}

const kvGet = (k) => idb('kv', 'readonly', (s) => s.get(k));
const kvSet = (k, v) => idb('kv', 'readwrite', (s) => s.put(v, k));

function stateFor(projectId) {
  const id = (key) => `${projectId}/${key}`;
  return {
    all: () => idb('files', 'readonly', (s) => s.index('project').getAll(projectId)),
    put: (rec) => idb('files', 'readwrite', (s) => s.put({ ...rec, id: id(rec.key) })),
    delete: (key) => idb('files', 'readwrite', (s) => s.delete(id(key))),
    // El análisis (hash y trozos) de un archivo cambiado en esta PC. Guardarlo
    // evita releer un .blend de cientos de MB cada vez que se abre la página.
    getScan: (key) => kvGet(`scan/${id(key)}`).catch(() => null),
    putScan: (key, value) => kvSet(`scan/${id(key)}`, value).catch(() => {}),
    deleteScan: (key) => idb('kv', 'readwrite', (s) => s.delete(`scan/${id(key)}`)).catch(() => {}),
  };
}

function storage(key, fallback) {
  try {
    return JSON.parse(localStorage.getItem(key)) ?? fallback;
  } catch {
    return fallback;
  }
}

function store(key, value) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch { /* sin almacenamiento: se pierde al recargar */ }
}

// ---------- estado de la página ----------

const ui = {
  me: null,
  api: null,
  clientId: storage('3dvault-client-id', null),
  root: null,
  projects: [],
  selected: new Set(storage('3dvault-projects', [])),
  engines: new Map(),
  progress: new Map(),
  paused: false,
  running: false,
  busy: 0,
  stream: null,
  lastSync: null, // cuándo terminó la última pasada completa
  runningSince: 0,
  passStart: 0,
};

if (!ui.clientId) {
  ui.clientId = crypto.randomUUID();
  store('3dvault-client-id', ui.clientId);
}

function defaultClientName() {
  const platform = navigator.userAgentData?.platform || navigator.platform || 'PC';
  return `Navegador en ${platform}`;
}

// browserName da «Chrome 131 en Windows», para el diagnóstico y los avisos.
function browserName() {
  const ua = navigator.userAgent;
  const brands = navigator.userAgentData?.brands?.filter((b) => !/not.?a.?brand|chromium/i.test(b.brand)) ?? [];
  let name = brands.map((b) => `${b.brand} ${b.version}`).join(', ');
  if (!name) name = (/Edg\/(\d+)/.exec(ua) && `Edge ${RegExp.$1}`) || (/OPR\/(\d+)/.exec(ua) && `Opera ${RegExp.$1}`) || (/Chrome\/(\d+)/.exec(ua) && `Chrome ${RegExp.$1}`) || (/Firefox\/(\d+)/.exec(ua) && `Firefox ${RegExp.$1}`) || (/Safari/.test(ua) && 'Safari') || 'desconocido';
  if (navigator.brave) name = `Brave (${name})`;
  const platform = navigator.userAgentData?.platform || (/Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Mac/.test(ua) ? 'Mac' : 'otro');
  return `${name} en ${platform}${navigator.userAgentData?.mobile ? ' (móvil)' : ''}`;
}

// diag cuenta al servidor qué pantalla se mostró y en qué navegador. Solo queda
// en el registro del servidor, para ayudar cuando «en un navegador no funciona».
function diag(state, detail = '') {
  try {
    fetch('/api/diag', {
      method: 'POST', keepalive: true,
      headers: { 'Content-Type': 'application/json', ...ui.api?.headers, 'X-Client-Id': ui.clientId },
      body: JSON.stringify({ state, browser: browserName(), detail: String(detail).slice(0, 500) }),
    }).catch(() => {});
  } catch { /* sin red: no importa */ }
}

function show(id) {
  for (const s of ['sync-unsupported', 'sync-other-tab', 'sync-setup', 'sync-resume', 'sync-main']) $(s).hidden = s !== id;
  $('sync-actions').hidden = id !== 'sync-main';
  // Fuera de la pantalla principal, los avisos de la sincronización quedarían viejos.
  for (const n of ['sync-status', 'pending-banner', 'stale-locks']) if (id !== 'sync-main') $(n).hidden = true;
  if (id === 'sync-resume') {
    const name = ui.root?.name ?? '';
    $('resume-folder').textContent = name;
    $('resume-folder-line').hidden = !name;
    for (const n of document.querySelectorAll('[data-folder-name]')) n.textContent = name ? `«${name}»` : 'la que usabas';
    $('resume-denied').hidden = true;
    $('resume-normal').hidden = false;
  }
  if (id === 'sync-unsupported') $('browser-name').textContent = browserName();
  diag(id, id === 'sync-unsupported' ? `showDirectoryPicker: ${'showDirectoryPicker' in window}, contexto seguro: ${isSecureContext}` : '');
}

function notice(text, ok = false) {
  const el = $('sync-notice');
  el.hidden = !text;
  el.textContent = text || '';
  el.classList.toggle('ok', ok);
}

function log(text) {
  const list = $('sync-log');
  if (list.firstElementChild?.classList.contains('free')) list.textContent = '';
  const li = document.createElement('li');
  const time = new Date().toLocaleTimeString('es', { hour: '2-digit', minute: '2-digit' });
  li.textContent = `${time} · ${text}`;
  list.prepend(li);
  while (list.children.length > 50) list.lastElementChild.remove();
}

function fail(err) {
  if (err instanceof ApiError && err.status === 401) {
    ui.paused = true;
    notice('Tu sesión terminó. Vuelve a entrar para seguir sincronizando.');
    setTimeout(() => location.assign('/login?next=/sync'), 3000);
    return;
  }
  if ((err?.name === 'NotAllowedError' || err?.name === 'SecurityError') && ui.root) {
    // Se perdió el permiso de la carpeta en pleno trabajo: se vuelve a pedir.
    diag('permiso-perdido', `${err.name}: ${err.message}`);
    ui.root.queryPermission({ mode: 'readwrite' }).then((state) => {
      if (state === 'granted') return notice(err.message);
      ui.stream?.close();
      if (state === 'denied') return showDenied();
      show('sync-resume');
      askOnFirstClick();
    }).catch(() => showDenied());
    return;
  }
  if (err?.name === 'NotReadableError') {
    notice('Un archivo está en uso por otro programa. Se reintentará.');
    return;
  }
  notice(err?.message || String(err));
}

// ---------- tablas ----------

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined) continue;
    if (k === 'class') node.className = v;
    else if (k.includes('-')) node.setAttribute(k, v); // data-*, aria-*
    else node[k] = v;
  }
  node.append(...children.filter((c) => c != null && c !== false));
  return node;
}

function actionButton(label, action, pid, key, primary = false) {
  return el('button', { class: primary ? 'btn primary' : 'btn', type: 'button', 'data-action': action, 'data-project': pid, 'data-key': key, 'data-tip': ACTION_TIPS[action] }, label);
}

function rowActions(pid, row) {
  const a = row.asset;
  const lock = a?.lock;
  const out = [];
  if (row.status === 'moved') {
    out.push(actionButton('Mover en el servidor', 'move', pid, row.key, true));
    out.push(actionButton('Agregar como nuevo…', 'upload', pid, row.key));
    return out;
  }
  if (row.status === 'new') out.push(actionButton('Agregar…', 'upload', pid, row.key, true));
  if (row.status === 'changed') out.push(actionButton('Subir…', 'upload', pid, row.key, true));
  if (row.status === 'missing') {
    out.push(actionButton('Confirmar borrado', 'delete', pid, row.key, true));
    out.push(actionButton('Recuperar en mi PC', 'restore', pid, row.key));
  }
  if (a && row.status !== 'missing') {
    if (row.lockedByMe) out.push(actionButton('Liberar', 'unlock', pid, row.key));
    else if (lock && lock.user_id === ui.me.id) out.push(actionButton('Traer el bloqueo a esta PC', 'takeover', pid, row.key));
    else if (!lock) out.push(actionButton('Bloquear', 'lock', pid, row.key));
    else out.push(actionButton('Pedir que lo libere', 'request', pid, row.key));
  }
  return out;
}

const timeLeft = (secs) => (secs < 60 ? `${Math.max(1, Math.ceil(secs))} s` : `${Math.ceil(secs / 60)} min`);

// rate da « · 4,2 MB/s · 30 s» para un avance {done, total, t0, d0}, o '' si aún no se sabe.
function rate(p) {
  const secs = (performance.now() - p.t0) / 1000;
  if (secs <= 1 || p.done <= p.d0) return '';
  const speed = (p.done - p.d0) / secs;
  return ` · ${formatBytes(speed)}/s · faltan ${timeLeft((p.total - p.done) / speed)}`;
}

function statusCell(row) {
  const p = ui.progress.get(row.key);
  if (p && p.done < p.total) {
    return el('td', {}, `${p.action} ${Math.floor((100 * p.done) / p.total)} %${rate(p)}`, el('progress', { max: p.total, value: p.done }));
  }
  let text = STATUS[row.status] || '';
  if (row.status === 'error') text = `Error: ${row.error}`;
  const git = GIT[row.status];
  if (git) text = el('span', { class: `pill git ${git.cls}`, 'data-tip': git.tip }, el('span', { class: 'git-mark', 'aria-hidden': 'true' }, git.mark), text);
  const lock = row.asset?.lock;
  let owner = lock && !row.lockedByMe ? `Bloqueado por ${lock.display_name}${lock.user_id === ui.me.id ? ` en ${lock.client_name}` : ''}` : null;
  if (row.lockedByMe && row.status !== 'locked') owner = 'Bloqueado por ti';
  if (row.status === 'changed' && !lock) owner = 'Sin bloquear: bloquéalo para que nadie suba encima.';
  if (row.status === 'moved') owner = `Antes: ${row.from.asset.path}. Muévelo en el servidor para conservar su historial.`;
  if (row.outside) owner = `${owner ? `${owner}. ` : ''}Su carpeta no se sincroniza en esta PC: se quitará cuando lo subas y liberes.`;
  return el('td', { class: row.status === 'error' ? 'sync-error' : '' }, text, owner && el('span', { class: 'sub' }, owner));
}

// skippedPanel muestra lo que hay en la carpeta y no se sube, con el motivo. Las
// copias de conflicto van destacadas: son trabajo de la persona que no llegó a
// 3D-Vault. Respaldos y temporales, plegados: no hace falta hacer nada.
function skippedPanel(pid, engine) {
  const skipped = engine?.skipped ?? [];
  if (!skipped.length) return null;
  const conflicts = skipped.filter((f) => f.reason === 'conflict');
  const others = skipped.filter((f) => f.reason !== 'conflict');
  return el('div', { class: 'skipped' },
    conflicts.length > 0 && el('div', { class: 'panel skipped-conflicts' },
      el('h3', {}, `${conflicts.length} ${conflicts.length === 1 ? 'copia de conflicto que no se sube' : 'copias de conflicto que no se suben'}`),
      el('p', { class: 'sub' }, 'Cambiaste estos archivos en tu PC mientras otra persona (o tú desde otro lado) subía una versión más nueva. Para no perder nada, tu trabajo quedó guardado aparte con este nombre, y en su lugar se bajó la versión del servidor. Estas copias nunca se suben solas.'),
      el('p', { class: 'sub' }, 'Ábrelas en Blender y compáralas. Si tu copia es la buena, pulsa «Usar mi copia» y luego Subir cambios. Si no, bórrala.'),
      el('ul', { class: 'pending-list' }, ...conflicts.map((f) => el('li', {},
        el('span', { class: 'pill git mod', 'data-tip': `Tu versión de ${originalOf(f.path)}` }, el('span', { class: 'git-mark', 'aria-hidden': 'true' }, '!'), 'Conflicto'),
        el('span', { class: 'path' }, f.path),
        el('button', { class: 'btn', type: 'button', 'data-action': 'keep-conflict', 'data-project': pid, 'data-skip': f.path,
          'data-tip': `Pone tu copia en lugar de ${originalOf(f.path)} para que puedas subirla. La versión del servidor queda en su historial.` }, 'Usar mi copia…'))))),
    others.length > 0 && el('details', { class: 'skipped-others' },
      el('summary', { class: 'text-action' }, `${others.length} ${others.length === 1 ? 'archivo no se sube' : 'archivos no se suben'} a propósito (respaldos .blend1 y temporales)`),
      el('p', { class: 'sub' }, 'Blender y Windows los crean solos (.blend1, .blend2, .tmp, nombres que empiezan con ~). No son tu trabajo y no hace falta subirlos. Si alguno sí lo es, cámbiale el nombre.'),
      el('ul', { class: 'skipped-list' }, ...others.slice(0, 200).map((f) => el('li', {}, f.path)))));
}

// pendingCounts cuenta lo que hay en la PC y no en 3D-Vault: lo que se sube o
// se borra con «Subir cambios».
function pendingCounts(rows) {
  const c = { nuevos: 0, cambiados: 0, borrados: 0 };
  for (const r of rows) {
    if (r.status === 'new') c.nuevos++;
    else if (r.status === 'changed') c.cambiados++;
    else if (r.status === 'missing') c.borrados++;
  }
  c.total = c.nuevos + c.cambiados + c.borrados;
  return c;
}

function describeCounts(c) {
  return [
    c.nuevos && `${c.nuevos} ${c.nuevos === 1 ? 'nuevo' : 'nuevos'}`,
    c.cambiados && `${c.cambiados} ${c.cambiados === 1 ? 'modificado' : 'modificados'}`,
    c.borrados && `${c.borrados} ${c.borrados === 1 ? 'borrado' : 'borrados'}`,
  ].filter(Boolean).join(', ');
}

// publishPending deja en este navegador un resumen de los cambios sin subir,
// para que el menú y la página del proyecto lo avisen en otras pestañas.
function publishPending(summary) {
  try {
    localStorage.setItem('3dvault-pendientes', JSON.stringify({ at: Date.now(), projects: summary }));
  } catch {
    // Sin almacenamiento, el aviso solo se ve en esta página.
  }
}

let lastFullRender = 0;
function render() {
  lastFullRender = performance.now();
  const box = $('project-tables');
  box.textContent = '';
  let pending = 0;
  const summary = {};
  for (const p of ui.projects.filter((p) => ui.selected.has(p.id))) {
    const engine = ui.engines.get(p.id);
    const all = engine?.rows ?? [];
    const query = $('filter').value.trim().toLowerCase();
    const onlyPending = $('only-pending').checked;
    const rows = all.filter((r) => (!query || r.key.includes(query)) && (!onlyPending || NEEDS_ME.has(r.status)));
    const counts = pendingCounts(all);
    const toUpload = counts.total;
    pending += toUpload + all.filter((r) => r.status === 'error' || r.status === 'moved').length;
    if (toUpload) summary[p.id] = { name: p.name, ...counts };
    const body = rows.map((row) => el('tr', { class: GIT[row.status] ? `git-row ${GIT[row.status].cls}` : '' },
      el('td', {}, row.asset ? el('a', { class: 'name', href: `/assets/${row.asset.id}` }, row.asset.path) : el('span', { class: 'name' }, row.local.path)),
      el('td', { class: 'n' }, row.asset ? `v${row.asset.head_version}` : '—'),
      el('td', { class: 'n' }, formatBytes(row.asset?.size_bytes ?? row.from?.asset.size_bytes ?? 0)),
      statusCell(row),
      el('td', { class: 'row-actions' }, ...rowActions(p.id, row)),
    ));
    box.append(
      el('div', { class: 'ledger-head' }, el('h2', {}, p.name),
        el('p', {}, `Carpeta: ${safeName(p.name)}`,
          engine?.remote.files > 0 && el('span', { class: 'remote-note' }, ` · Solo en el servidor: ${engine.remote.files} archivo(s), ${formatBytes(engine.remote.bytes)}`),
          el('button', { class: 'btn', type: 'button', 'data-action': 'folders', 'data-project': p.id, 'data-tip': 'Elige qué carpetas de este proyecto se bajan a esta PC. Las demás se quedan solo en el servidor.' }, 'Carpetas…'),
          toUpload > 0 && el('button', { class: 'btn primary', type: 'button', 'data-action': 'upload-all', 'data-project': p.id, 'data-tip': 'Sube de una vez todo lo nuevo, lo modificado y los borrados de este proyecto. Antes te muestra la lista.' }, `Subir cambios (${toUpload})`))),
      el('div', { class: 'panel table-panel drop-zone', 'data-drop': p.id }, rows.length
        ? el('div', { class: 'table-wrap' }, el('table', { class: 'stack-table' },
            el('thead', {}, el('tr', {}, el('th', {}, 'Archivo'), el('th', { class: 'n', 'data-tip': 'La última versión que hay en 3D-Vault' }, 'Versión'), el('th', { class: 'n' }, 'Tamaño'), el('th', { 'data-tip': 'Cómo está tu copia comparada con 3D-Vault' }, 'Estado'), el('th', {}, ''))),
            el('tbody', {}, ...body)))
        : el('p', { class: 'empty' }, !engine ? 'Sincronizando…'
          : all.length ? 'Ningún archivo coincide con el filtro.'
          : ui.running && engine.pass?.files ? 'Bajando los archivos de este proyecto. Aparecen aquí al terminar; el avance está arriba.'
          : 'Este proyecto todavía no tiene archivos. Arrástralos aquí o cópialos en su carpeta.'),
        engine && rows.length > 0 && el('p', { class: 'drop-hint' }, 'Arrastra aquí archivos o carpetas para agregarlos al proyecto.')),
      skippedPanel(p.id, engine) ?? '',
    );
  }
  if (!ui.selected.size) box.append(el('p', { class: 'empty' }, 'Marca arriba los proyectos que quieres tener en esta PC.'));
  renderTeam();
  ui.lastPending = pending;
  renderStatus(pending);
  document.title = `${pending ? `(${pending}) ` : ''}Mi carpeta · 3D-Vault`;
  publishPending(summary);

  // Barra fija arriba: el «commit». Siempre visible, para que se sepa dónde se
  // sube lo que se copió o cambió en la carpeta, aunque ahora no haya nada.
  $('pending-banner').hidden = true;
  const entries = Object.entries(summary);
  const bar = $('commit-bar');
  bar.classList.toggle('has-changes', entries.length > 0);
  const total = entries.reduce((n, [, c]) => n + c.total, 0);
  remindUpload(total);
  const since = ui.pendingSince && Date.now() - ui.pendingSince > 10 * 60_000 ? ` desde ${agoFrom(ui.pendingSince)}` : '';
  bar.replaceChildren(...(entries.length ? [
    el('div', { class: 'commit-text' },
      el('b', {}, `Tienes ${total} ${total === 1 ? 'cambio' : 'cambios'} en esta PC sin subir a 3D-Vault${since}`),
      el('span', { class: 'sub' }, 'Los demás no reciben estos archivos hasta que pulses Subir cambios. Te mostrará la lista y te pedirá un comentario, como un commit.')),
    ...entries.map(([id, c]) => el('button', { class: 'btn primary big', type: 'button', 'data-action': 'upload-all', 'data-project': id,
      'data-tip': `${c.name}: ${describeCounts(c)}` }, entries.length > 1 ? `Subir cambios de ${c.name} (${c.total})` : `Subir cambios (${c.total})`)),
  ] : [
    el('div', { class: 'commit-text' },
      el('b', {}, ui.running && !ui.lastSync ? 'Revisando tu carpeta…' : 'Todo subido: no tienes cambios pendientes'),
      el('span', { class: 'sub' }, 'Cuando copies, cambies o borres archivos en tu carpeta, aparecerán abajo en color y aquí saldrá el botón Subir cambios.')),
  ]));

  // Bloqueos propios de más de un día: un olvido frecuente que frena a los demás.
  const old = [...ui.engines.values()].flatMap((e) => e.rows)
    .filter((r) => r.lockedByMe && Date.now() - Date.parse(r.asset.lock.locked_at) > STALE_LOCK);
  $('stale-locks').hidden = !old.length;
  $('stale-locks').textContent = old.length
    ? `Tienes ${old.length === 1 ? 'un archivo bloqueado' : `${old.length} archivos bloqueados`} desde hace más de un día: ${old.map((r) => r.asset.path).join(', ')}. Si ya no los estás editando, súbelos o libéralos para que otros puedan trabajar.`
    : '';
  $('pause-btn').textContent = ui.paused ? 'Reanudar' : 'Pausar';
}

// renderTeam muestra lo último que subió cada persona. Responde a «no me llega lo
// que hizo X»: si X no aparece o su última subida es vieja, el cambio sigue en
// su PC y le falta pulsar Subir cambios.
function renderTeam() {
  const box = $('team-uploads');
  if (!box) return;
  const people = new Map();
  for (const p of ui.projects.filter((p) => ui.selected.has(p.id))) {
    for (const a of ui.engines.get(p.id)?.snap?.assets ?? []) {
      const at = Date.parse(a.updated_at);
      const who = a.uploaded_by || '—';
      const cur = people.get(who) ?? { who, at: 0, path: '', project: p.name, week: 0 };
      if (Date.now() - at < 7 * 86400_000) cur.week++;
      if (at > cur.at) Object.assign(cur, { at, path: a.path, project: p.name, id: a.id });
      people.set(who, cur);
    }
  }
  const list = [...people.values()].sort((a, b) => b.at - a.at);
  box.hidden = !list.length;
  $('team-list').replaceChildren(...list.map((x) => el('li', {},
    el('b', {}, x.who === ui.me?.display_name ? `${x.who} (tú)` : x.who),
    ` subió por última vez ${agoFrom(x.at)}: `,
    el('a', { href: `/assets/${x.id}` }, x.path.split('/').pop()),
    el('span', { class: 'sub' }, ` · ${x.week ? `${x.week} ${x.week === 1 ? 'archivo' : 'archivos'} esta semana` : 'nada esta semana'}`))));
}

function agoFrom(t) {
  const m = Math.round((Date.now() - t) / 60_000);
  if (m < 1) return 'hace un momento';
  if (m < 60) return `hace ${m} min`;
  const h = Math.round(m / 60);
  if (h < 24) return `hace ${h} ${h === 1 ? 'hora' : 'horas'}`;
  const d = Math.round(h / 24);
  return `hace ${d} ${d === 1 ? 'día' : 'días'}`;
}

// renderStatus llena el recuadro de arriba: qué hace la sincronización y cuánto
// falta, o que ya está todo al día.
function renderStatus(pending) {
  const box = $('sync-status');
  box.hidden = !ui.selected.size;
  const engines = ui.projects.filter((p) => ui.selected.has(p.id)).map((p) => ui.engines.get(p.id)).filter(Boolean);
  const active = [...ui.progress.values()].filter((p) => p.done < p.total);
  const passes = ui.running ? engines.map((e) => e.pass).filter(Boolean) : [];
  const sum = (k) => passes.reduce((s, x) => s + x[k], 0);
  const doneFiles = sum('doneFiles');
  const files = Math.max(sum('files'), doneFiles);
  // Lo bajado: archivos terminados más lo que va de los que están bajando.
  const done = sum('doneBytes') + active.filter((p) => p.action === 'Descargando').reduce((s, p) => s + p.done, 0);
  const bytes = Math.max(sum('bytes'), done);
  const now = active[0];
  const nowText = now && `${now.path ?? now.key}: ${now.action.toLowerCase()} ${Math.floor((100 * now.done) / now.total)} %`;
  const at = ui.lastSync && ui.lastSync.toLocaleTimeString('es', { hour: '2-digit', minute: '2-digit' });
  let kind = 'ok';
  let title;
  let detail;
  let bar = null;

  if (files && doneFiles < files) {
    kind = 'busy';
    title = `Bajando archivos: ${doneFiles} de ${files} listos`;
    const secs = (performance.now() - ui.passStart) / 1000;
    const speed = secs > 1 && done > 0 ? done / secs : 0;
    detail = `${formatBytes(done)} de ${formatBytes(bytes)}${speed ? ` · ${formatBytes(speed)}/s · faltan ${timeLeft((bytes - done) / speed)}` : ''}${nowText ? ` · ${nowText}` : ''}`;
    bar = [done, bytes || 1];
  } else if (now) {
    kind = 'busy';
    title = { Subiendo: 'Subiendo', Analizando: 'Revisando cambios en', Recuperando: 'Recuperando' }[now.action] ?? now.action;
    title += ` ${now.path ?? now.key}`;
    detail = `${Math.floor((100 * now.done) / now.total)} %${rate(now)}${active.length > 1 ? ` · y ${active.length - 1} más` : ''}`;
    bar = [now.done, now.total];
  } else if (ui.running && (!ui.lastSync || performance.now() - ui.runningSince > 800)) {
    kind = 'busy';
    title = 'Buscando novedades…';
    const c = engines.map((e) => e.checking).filter(Boolean);
    const checked = c.reduce((n, x) => n + x.done, 0);
    const toCheck = c.reduce((n, x) => n + x.total, 0);
    detail = toCheck ? `Revisando tu carpeta: ${checked} de ${toCheck} archivos.` : 'Leyendo tu carpeta y lo nuevo del servidor.';
    if (toCheck) bar = [checked, toCheck];
  } else if (ui.paused) {
    kind = 'paused';
    title = 'Sincronización en pausa';
    detail = 'Tus archivos no se actualizan hasta que pulses Reanudar.';
  } else if (pending) {
    kind = 'warn';
    title = `${pending} ${pending === 1 ? 'archivo espera' : 'archivos esperan'} que hagas algo`;
    detail = `Están abajo, con el botón de lo que falta.${at ? ` Revisado a las ${at}.` : ''}`;
  } else {
    const rows = engines.flatMap((e) => e.rows).filter((r) => r.status === 'ok' || r.status === 'locked');
    const size = rows.reduce((s, r) => s + r.asset.size_bytes, 0);
    title = 'Todo al día';
    detail = `${rows.length} ${rows.length === 1 ? 'archivo' : 'archivos'}, ${formatBytes(size)} en esta PC.${at ? ` Revisado a las ${at}; se revisa solo cada minuto.` : ''}`;
  }

  box.dataset.kind = kind;
  if ($('status-title').textContent !== title) $('status-title').textContent = title;
  $('status-detail').textContent = detail;
  $('status-bar').hidden = !bar;
  if (bar) [$('status-bar').value, $('status-bar').max] = bar;
  $('sync-now').disabled = ui.running;
  $('sync-now').textContent = ui.running ? 'Sincronizando…' : 'Sincronizar ahora';
}

function renderPicks() {
  const box = $('project-picks');
  box.textContent = '';
  if (!ui.projects.length) {
    box.append(el('p', { class: 'free' }, 'Todavía no hay proyectos. Créalos en '), el('a', { class: 'text-action', href: '/' }, 'Todos los proyectos'));
    return;
  }
  for (const p of ui.projects) {
    const input = el('input', { type: 'checkbox', checked: ui.selected.has(p.id), 'data-pick': p.id });
    box.append(el('label', { class: 'check' }, input, `${p.name}`));
  }
}

// ---------- carpetas que se sincronizan en esta PC ----------

// chooseFolders muestra el árbol de carpetas del proyecto y guarda las que esta
// PC no sincroniza. Devuelve true si se guardó. snap puede faltar: se pide.
async function chooseFolders(project, snap, first = false) {
  snap ??= await ui.api.json('GET', `/api/projects/${project.id}/snapshot`);
  const tree = folderTree(snap);
  if (!tree.children.length) {
    folderSelection.set(project.id, []);
    return true; // sin carpetas no hay nada que elegir
  }
  const excluded = folderSelection.get(project.id);
  const checked = new Map();
  const parent = new Map();
  const all = [];
  const visit = (n, up) => {
    for (const c of n.children) {
      checked.set(c.key, !underAny(c.path, excluded));
      parent.set(c.key, up);
      all.push(c);
      visit(c, c);
    }
  };
  visit(tree, null);
  const below = (n) => n.children.flatMap((c) => [c, ...below(c)]);

  const boxes = new Map();
  const draw = () => {
    for (const n of all) {
      const box = boxes.get(n.key);
      const mixed = checked.get(n.key) && below(n).some((c) => !checked.get(c.key));
      box.checked = checked.get(n.key) && !mixed;
      box.indeterminate = mixed;
    }
    const out = all.filter((n) => !checked.get(n.key) && (!parent.get(n.key) || checked.get(parent.get(n.key).key)));
    const left = tree.bytes - out.reduce((s, n) => s + n.bytes, 0);
    $('folders-total').textContent = `En esta PC: ${formatBytes(left)} de ${formatBytes(tree.bytes)}.`;
    const every = all.every((n) => checked.get(n.key));
    $('folders-all').checked = every;
    $('folders-all').indeterminate = !every && all.some((n) => checked.get(n.key));
    return out;
  };
  const branch = (n) => {
    const box = el('input', { type: 'checkbox', 'aria-label': n.path });
    box.addEventListener('change', () => {
      for (const c of [n, ...below(n)]) checked.set(c.key, box.checked);
      // Marcar una subcarpeta marca también las que la contienen.
      if (box.checked) for (let up = parent.get(n.key); up; up = parent.get(up.key)) checked.set(up.key, true);
      draw();
    });
    boxes.set(n.key, box);
    const label = el('span', { class: 'folder-line' }, box, el('span', { class: 'folder-name' }, n.name),
      el('span', { class: 'sub' }, `${formatBytes(n.bytes)} · ${n.files} archivo(s)`));
    if (!n.children.length) return el('li', {}, label);
    return el('li', {}, el('details', { open: n.path.split('/').length < 2 }, el('summary', {}, label), el('ul', {}, ...n.children.map(branch))));
  };
  $('folders-tree').replaceChildren(el('ul', {}, ...tree.children.map(branch)));
  $('folders-all').onchange = () => {
    for (const n of all) checked.set(n.key, $('folders-all').checked);
    draw();
  };
  draw();

  $('folders-title').textContent = first ? `${project.name}: ¿qué carpetas quieres en esta PC?` : `${project.name}: carpetas que se sincronizan`;
  const dialog = $('folders-dialog');
  dialog.returnValue = '';
  dialog.showModal();
  await new Promise((resolve) => dialog.addEventListener('close', resolve, { once: true }));
  if (dialog.returnValue !== 'ok') return false;
  const out = draw();
  folderSelection.set(project.id, out.map((n) => n.key));
  if (out.length) log(`${project.name}: sin sincronizar en esta PC: ${out.map((n) => n.path).join(', ')}.`);
  else log(`${project.name}: se sincronizan todas las carpetas.`);
  return true;
}

// ---------- sincronización ----------

async function engineFor(p) {
  let engine = ui.engines.get(p.id);
  if (!engine) {
    const name = safeName(p.name);
    let existed = true;
    const dir = await ui.root.getDirectoryHandle(name).catch(() => {
      existed = false;
      return ui.root.getDirectoryHandle(name, { create: true });
    });
    // Si la carpeta no es la de la última pasada, lo recordado no vale para ella.
    const last = await kvGet(`dir/${p.id}`);
    const fresh = !existed || !last || !(await last.isSameEntry(dir).catch(() => false));
    engine = new Project({
      api: ui.api, me: ui.me, clientId: ui.clientId, project: p, root: dir, state: stateFor(p.id), fresh,
      log: (text) => {
        log(text);
        notify(text);
      },
      progress: (pr) => {
        const prev = ui.progress.get(pr.key);
        const same = prev?.action === pr.action;
        if (pr.done >= pr.total) ui.progress.delete(pr.key);
        else ui.progress.set(pr.key, { ...pr, t0: same ? prev.t0 : performance.now(), d0: same ? prev.d0 : pr.done });
        // El recuadro de estado en cada avance; la tabla, como mucho cada 1,5 s.
        const full = performance.now() - lastFullRender > 1500;
        scheduleRender(full);
      },
    });
    ui.engines.set(p.id, engine);
  }
  return engine;
}

// scheduleRender redibuja en el próximo cuadro. Sin full, solo el recuadro de
// estado: la tabla tiene cientos de filas y rehacerla con cada avance de una
// descarga le quitaba tiempo a la sincronización.
let renderQueued = null;
function scheduleRender(full = true) {
  if (renderQueued === 'full' || renderQueued === (full ? 'full' : 'status')) return;
  const first = !renderQueued;
  renderQueued = full ? 'full' : 'status';
  if (!first) return;
  requestAnimationFrame(() => {
    const what = renderQueued;
    renderQueued = null;
    if (what === 'full') render();
    else renderStatus(ui.lastPending ?? 0);
  });
}

const confirmFirst = (n, bytes) => Promise.resolve(confirm(`Se descargarán ${n} archivo(s), ${formatBytes(bytes)} en total. ¿Empezar?`));

// syncAll pone en cola una pasada completa. La promesa se cumple al terminar una
// pasada que empezó después de la llamada, así quien espera ve lo último.
let current = Promise.resolve();
let waiting = null;
function syncAll(manual = false) {
  if (!ui.root || $('sync-main').hidden || (ui.paused && !manual)) return Promise.resolve(); // sin permiso no se revisa
  if (!waiting) {
    waiting = current.then(() => {
      waiting = null;
      return syncOnce(manual);
    });
    current = waiting;
  }
  return waiting;
}

async function syncOnce(manual) {
  ui.running = true;
  ui.busy++;
  ui.runningSince = ui.passStart = performance.now();
  for (const e of ui.engines.values()) e.pass = null;
  setTimeout(() => scheduleRender(false), 900); // si la pasada tarda, se muestra «Buscando novedades…»
  const ticker = setInterval(() => scheduleRender(false), 500); // con su avance
  render();
  try {
    for (const p of ui.projects.filter((p) => ui.selected.has(p.id))) {
      const engine = await engineFor(p);
      const wasFresh = engine.fresh;
      const started = performance.now();
      await engine.sync({ confirmFirst });
      reportSlow(p, engine, performance.now() - started);
      reportPending(p.id, engine.rows);
      if (wasFresh && !engine.fresh) await kvSet(`dir/${p.id}`, engine.root);
      const got = engine.pass;
      if (got?.doneFiles) log(`${p.name}: se ${got.doneFiles === 1 ? 'bajó 1 archivo' : `bajaron ${got.doneFiles} archivos`} (${formatBytes(got.doneBytes)}).`);
      render();
    }
    ui.lastSync = new Date();
    if (manual) notice('Listo: todo sincronizado.', true);
  } catch (err) {
    fail(err);
  } finally {
    clearInterval(ticker);
    ui.running = false;
    ui.busy--;
    render();
  }
  // Se actualizó el servidor: se recarga para usar el código nuevo, si no hay nada a medias.
  if (appUpdated && !ui.busy && !document.querySelector('dialog[open]')) location.reload();
}

// remindUpload recuerda subir lo que lleva rato sin subirse: lo guardado en la
// PC no les llega a los demás hasta pulsar Subir cambios, y eso es lo que más
// confunde («subí y no le aparece»).
function remindUpload(total) {
  if (!total) {
    ui.pendingSince = null;
    ui.remindedAt = 0;
    return;
  }
  ui.pendingSince ??= Date.now();
  const waited = Date.now() - ui.pendingSince;
  if (waited > 30 * 60_000 && Date.now() - (ui.remindedAt || 0) > 60 * 60_000) {
    ui.remindedAt = Date.now();
    notify(`Tienes ${total} ${total === 1 ? 'cambio' : 'cambios'} sin subir desde ${agoFrom(ui.pendingSince)}. Tus compañeros no los ven hasta que pulses Subir cambios.`);
  }
}

// reportSlow avisa al servidor de una pasada lenta, con lo que tardó cada etapa,
// para saber en qué PC y por qué «se demora buscando novedades».
let lastSlow = 0;
function reportSlow(p, engine, ms) {
  if (ms < 8000 || Date.now() - lastSlow < 10 * 60_000) return;
  lastSlow = Date.now();
  const t = engine.timing ?? {};
  const etapas = Object.entries(t).map(([k, v]) => `${k} ${k === 'archivos' ? v : `${(v / 1000).toFixed(1)} s`}`).join(', ');
  const got = engine.pass;
  diag('pasada-lenta', `${p.name}: ${(ms / 1000).toFixed(1)} s (${etapas}); bajó ${got?.doneFiles ?? 0} archivos, ${formatBytes(got?.doneBytes ?? 0)}`);
}

// reportPending cuenta al servidor qué tiene esta PC sin subir en un proyecto,
// para que los demás lo vean en la web con sus colores. Solo envía si cambió o
// cada 5 minutos, para que el aviso no caduque mientras la página siga abierta.
const reported = new Map(); // proyecto → {body, at}
function reportPending(pid, rows) {
  const items = rows.filter((r) => GIT[r.status] || r.status === 'error').map((r) => ({ path: r.local?.path ?? r.asset.path, status: r.status, detail: r.error ?? '' }));
  for (const f of ui.engines.get(pid)?.skipped ?? []) if (f.reason === 'conflict') items.push({ path: f.path, status: 'conflict', detail: `Copia de ${originalOf(f.path)}` });
  const body = JSON.stringify(items);
  const last = reported.get(pid);
  if (last && last.body === body && Date.now() - last.at < 5 * 60_000) return;
  reported.set(pid, { body, at: Date.now() });
  ui.api.json('PUT', `/api/projects/${pid}/pending`, { items }).catch(() => reported.delete(pid));
}

function openStream() {
  ui.stream?.close();
  ui.stream = null;
  if (!ui.selected.size) return;
  const stream = new EventSource(`/api/stream?projects=${[...ui.selected].join(',')}`);
  for (const kind of [...EVENT_KINDS, 'reset']) stream.addEventListener(kind, () => syncAll());
  // Un proyecto borrado o recuperado cambia la lista de proyectos: se vuelve a cargar.
  for (const kind of ['project_deleted', 'project_restored']) stream.addEventListener(kind, () => location.reload());
  stream.addEventListener('unlock_requested', (e) => {
    const { payload } = JSON.parse(e.data);
    if (payload.lock?.user_id !== ui.me.id) return;
    const text = `${payload.actor_name} necesita ${payload.path}. Súbelo y libéralo cuando puedas.`;
    notice(text, true);
    log(text);
    notify(text, true);
  });
  ui.stream = stream;
}

// run hace una acción del usuario y vuelve a sincronizar.
async function run(fn) {
  ui.busy++;
  try {
    await fn();
  } catch (err) {
    fail(err);
  } finally {
    ui.busy--;
    await syncAll();
  }
}

// askComment pide el comentario de una subida. Devuelve null si se cancela.
function askComment(title, comment, canRelease) {
  const dialog = $('upload-dialog');
  $('upload-title').textContent = title;
  $('upload-comment').value = comment;
  $('upload-release-row').hidden = !canRelease;
  dialog.returnValue = '';
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener('close', () => {
      resolve(dialog.returnValue === 'ok' ? { comment: $('upload-comment').value.trim(), release: canRelease && $('upload-release').checked } : null);
    }, { once: true });
  });
}

const askUpload = (row) => askComment(row.asset ? `Subir ${row.asset.path}` : `Agregar ${row.local.path}`, row.asset ? '' : 'Primera versión', row.lockedByMe);

// uploadMany sube varios archivos con el mismo comentario, uno tras otro.
async function uploadMany(engine, keys, title) {
  const rows = () => engine.rows.filter((r) => keys.has(r.key) && (r.status === 'changed' || r.status === 'new' || r.status === 'missing'));
  const list = rows();
  if (!list.length) return;
  const uploads = list.filter((r) => r.status !== 'missing');
  const deletes = list.filter((r) => r.status === 'missing');
  const allNew = uploads.length && uploads.every((r) => r.status === 'new');
  const plan = el('ul', { class: 'upload-plan' },
    uploads.length ? el('li', {}, `Se suben: ${uploads.map((r) => r.asset?.path ?? r.local.path).join(', ')}`) : null,
    deletes.length ? el('li', {}, `Se borran en 3D-Vault, porque los borraste en esta PC: ${deletes.map((r) => r.asset.path).join(', ')}. Quedan en la Papelera del proyecto.`) : null);
  $('upload-plan').replaceChildren(plan);
  const answer = await askComment(`${title} (${list.length})`, allNew ? 'Primera versión' : uploads.length ? '' : 'Borro archivos que ya no se usan', uploads.some((r) => r.lockedByMe));
  $('upload-plan').replaceChildren();
  if (!answer) return;
  await run(async () => {
    const failed = [];
    for (const row of deletes) {
      try {
        await engine.deleteOnServer(row);
        log(`${row.asset.path}: borrado en 3D-Vault (está en la Papelera).`);
      } catch (err) {
        failed.push(`${row.asset.path}: ${err.message}`);
      }
    }
    for (const row of uploads) {
      try {
        const r = await engine.upload(row, answer.comment, answer.release && row.lockedByMe);
        log(`${row.asset?.path ?? row.local.path}: ${r.noChanges ? 'sin cambios' : `subido como v${r.version}`}.`);
      } catch (err) {
        failed.push(`${row.local.path}: ${err.message}`);
      }
    }
    if (failed.length) notice(`No se pudieron completar ${failed.length}: ${failed.join(' · ')}`);
    else notice(`Listo: ${[uploads.length && `${uploads.length} subido(s)`, deletes.length && `${deletes.length} borrado(s)`].filter(Boolean).join(' y ')}.`, true);
  });
}

// originalOf quita «.conflicto-usuario-fecha» del nombre: la copia de conflicto
// de «camion.blend» es «camion.conflicto-ana-20261124-1650.blend».
const originalOf = (path) => path.replace(/\.conflicto-[^/]*?-\d{8}-\d{4}(?=\.[^./]*$|$)/i, '');

async function fileAt(root, path, create = false) {
  const parts = path.split('/');
  const name = parts.pop();
  let dir = root;
  for (const p of parts) dir = await dir.getDirectoryHandle(p, { create });
  return { dir, name };
}

const actions = {
  'check-version': () => checkVersion(true),
  'sync-now': () => syncAll(true),
  // Usar mi copia de conflicto: reemplaza el original en esta PC con la copia y
  // la borra. La versión del servidor no se pierde: sigue en su historial.
  'keep-conflict': (engine, _row, btn) => run(async () => {
    const copy = btn.dataset.skip;
    const original = originalOf(copy);
    if (original === copy) return;
    if (!confirm(`Tu copia reemplazará a ${original} en esta PC y quedará como «Modificado» para que la subas.\n\nLa versión que está en 3D-Vault no se pierde: queda en su historial.\n\n¿Continuar?`)) return;
    const from = await fileAt(engine.root, copy);
    const file = await (await from.dir.getFileHandle(from.name)).getFile();
    const to = await fileAt(engine.root, original, true);
    const out = await (await to.dir.getFileHandle(to.name, { create: true })).createWritable();
    await file.stream().pipeTo(out);
    await from.dir.removeEntry(from.name);
    log(`${original}: se usó tu copia de conflicto. Pulsa Subir cambios para llevarla a 3D-Vault.`);
    notice(`Listo: ${original} tiene tu versión. Bloquéalo y pulsa Subir cambios.`, true);
    await syncAll(true);
  }),
  pause: () => {
    ui.paused = !ui.paused;
    render();
    if (!ui.paused) syncAll();
  },
  pick: pickFolder,
  resume: () => grantAccess(),
  'cancel-upload': () => $('upload-dialog').close(),
  lock: (engine, row) => run(async () => {
    await engine.lock(row);
    notice('Bloqueado: ya puedes editarlo. Si lo tenías abierto en Blender, usa Archivo → Revertir antes de editar, por si cambió.', true);
  }),
  takeover: (engine, row) => run(async () => {
    if (!confirm('El bloqueo está en otra PC tuya. Si allí hay cambios sin subir, quedarán solo en esa PC. ¿Traerlo aquí?')) return;
    await engine.lock(row, true);
    notice('El bloqueo ahora es de esta PC.', true);
  }),
  unlock: (engine, row) => run(async () => {
    if (row.status === 'changed' && !confirm('Tienes cambios sin subir en este archivo. Si lo liberas, otro podría subir encima. ¿Liberar igual?')) return;
    await engine.unlock(row);
    notice('Liberado.', true);
  }),
  upload: async (engine, row) => {
    const answer = await askUpload(row);
    if (!answer) return;
    await run(async () => {
      const r = await engine.upload(row, answer.comment, answer.release);
      let msg = r.noChanges ? 'No hay cambios: el servidor ya tenía este archivo.' : `Subido como v${r.version}.`;
      if (!r.noChanges && r.version > 1) msg += ` Se reutilizó el ${Math.max(0, r.reused).toFixed(0)} % del archivo.`;
      notice(msg, true);
      log(`${row.asset?.path ?? row.local.path}: ${msg}`);
    });
  },
  folders: async (engine) => {
    const saved = await chooseFolders(engine.project, engine.snap);
    if (saved) await syncAll(true);
  },
  'cancel-folders': () => $('folders-dialog').close(),
  'upload-all': (engine) => uploadMany(engine, new Set(engine.rows.map((r) => r.key)), 'Subir cambios'),
  install: async () => {
    if (!installPrompt) return;
    installPrompt.prompt();
    installPrompt = null;
    renderComforts();
  },
  notify: async () => {
    await Notification.requestPermission();
    renderComforts();
  },
  'blender-code': async () => {
    try {
      const name = `Blender · ${$('client-name').value.trim() || defaultClientName()}`;
      const { token } = await ui.api.json('POST', '/api/tokens', { name });
      const data = { s: location.origin, t: token, c: ui.clientId, n: $('client-name').value.trim() || defaultClientName() };
      const raw = btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(data))));
      $('blender-code').value = `3DV1:${raw.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`;
      $('blender-code-row').hidden = false;
      $('blender-code').select();
    } catch (err) {
      fail(err);
    }
  },
  'copy-code': async () => {
    await navigator.clipboard.writeText($('blender-code').value).catch(() => $('blender-code').select());
    notice('Código copiado. Pégalo en las preferencias del complemento en Blender.', true);
  },
  request: (engine, row) => run(async () => {
    await ui.api.json('POST', `/api/assets/${row.asset.id}/request-unlock`);
    notice(`Listo: ${row.asset.lock.display_name} recibirá un aviso de que necesitas ${row.asset.path}.`, true);
  }),
  move: (engine, row) => run(async () => {
    await engine.move(row);
    notice(`Movido en el servidor: ${row.from.asset.path} → ${row.local.path}. Conserva su historial.`, true);
  }),
  restore: (engine, row) => {
    // La barra aparece al pulsar, antes de que llegue el primer trozo.
    ui.progress.set(row.key, { key: row.key, path: row.asset.path, action: 'Recuperando', done: 0, total: row.asset.size_bytes || 1, t0: performance.now(), d0: 0 });
    scheduleRender();
    return run(async () => {
      try {
        await engine.restoreMissing(row);
      } finally {
        ui.progress.delete(row.key);
      }
      notice(`Recuperado en tu PC: ${row.asset.path}.`, true);
      log(`${row.asset.path} recuperado en tu PC.`);
    });
  },
  delete: (engine, row) => run(async () => {
    if (!confirm(`¿Borrar ${row.asset.path} también en 3D-Vault? Desaparecerá de las PCs de todos. Queda en la Papelera del proyecto, desde donde se puede recuperar.`)) return;
    await engine.deleteOnServer(row);
    log(`${row.asset.path} se borró del servidor.`);
  }),
};

document.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-action]');
  if (!btn) return;
  const fn = actions[btn.dataset.action];
  if (!fn) return;
  if (!btn.dataset.project) return fn();
  const engine = ui.engines.get(Number(btn.dataset.project));
  if (!btn.dataset.key) return engine && fn(engine, undefined, btn);
  const row = engine?.rows.find((r) => r.key === btn.dataset.key);
  if (row) fn(engine, row);
});

document.addEventListener('change', async (e) => {
  const id = e.target.dataset?.pick;
  if (!id) return;
  const project = ui.projects.find((p) => p.id === Number(id));
  // La primera vez, se eligen las carpetas antes de bajar nada.
  if (e.target.checked && project && !folderSelection.has(project.id)) {
    const box = e.target;
    box.disabled = true;
    try {
      if (!(await chooseFolders(project, null, true))) {
        box.checked = false;
        return;
      }
    } catch (err) {
      fail(err);
      box.checked = false;
      return;
    } finally {
      box.disabled = false;
    }
  }
  if (e.target.checked) ui.selected.add(Number(id));
  else ui.selected.delete(Number(id));
  store('3dvault-projects', [...ui.selected]);
  openStream();
  render();
  syncAll();
});

$('filter').addEventListener('input', () => render());
$('only-pending').addEventListener('change', () => render());
$('filter-form').addEventListener('submit', (e) => e.preventDefault());

$('client-form').addEventListener('submit', (e) => {
  e.preventDefault();
  store('3dvault-client-name', $('client-name').value.trim());
  ui.api = new Api(ui.clientId, $('client-name').value.trim());
  for (const engine of ui.engines.values()) engine.api = ui.api;
  notice('Guardado.', true);
});

addEventListener('beforeunload', (e) => {
  if (ui.busy) e.preventDefault();
});

// Al volver de Blender se revisa enseguida si guardaste algo.
addEventListener('focus', () => syncAll());

// ---------- comodidades: instalar como app, avisos y arrastrar y soltar ----------

let installPrompt = null;
addEventListener('beforeinstallprompt', (e) => {
  e.preventDefault();
  installPrompt = e;
  renderComforts();
});

const installed = () => matchMedia('(display-mode: standalone)').matches;

function renderComforts() {
  $('install-btn').hidden = !installPrompt;
  $('install-done').hidden = !installed();
  $('install-how').hidden = Boolean(installPrompt) || installed();
  const perm = 'Notification' in window ? Notification.permission : 'denied';
  $('notify-btn').hidden = perm !== 'default';
  $('notify-state').textContent = perm === 'granted' ? 'Activados.' : perm === 'denied' ? 'Bloqueados en este navegador.' : '';
}

// notify muestra un aviso de escritorio. Si no es urgente, solo con la ventana
// en segundo plano: a la vista ya se ve en la página.
function notify(text, urgent = false) {
  if (!('Notification' in window) || Notification.permission !== 'granted') return;
  if (!urgent && document.visibilityState === 'visible') return;
  new Notification('3D-Vault', { body: text, icon: '/static/app/icon-192.png', tag: text });
}

// copyIn copia lo soltado dentro de la carpeta del proyecto, carpetas incluidas.
// Devuelve las claves de los archivos copiados.
async function copyIn(dir, handle, prefix, keys) {
  if (handle.kind === 'directory') {
    const sub = await dir.getDirectoryHandle(handle.name, { create: true });
    for await (const child of handle.values()) await copyIn(sub, child, `${prefix}${handle.name}/`, keys);
    return;
  }
  if (ignored(handle.name)) return;
  const exists = await dir.getFileHandle(handle.name).then(() => true, () => false);
  if (exists && !confirm(`Ya hay un ${prefix}${handle.name} en la carpeta. ¿Reemplazarlo por el que arrastraste? Si tenía cambios sin subir, se perderán.`)) return;
  const file = await handle.getFile();
  const out = await (await dir.getFileHandle(handle.name, { create: true })).createWritable();
  await file.stream().pipeTo(out);
  keys.add(keyOf(prefix + handle.name));
}

document.addEventListener('dragover', (e) => {
  const zone = e.target.closest?.('[data-drop]');
  if (!zone) return;
  e.preventDefault();
  zone.classList.add('dragging');
});

document.addEventListener('dragleave', (e) => e.target.closest?.('[data-drop]')?.classList.remove('dragging'));

document.addEventListener('drop', (e) => {
  const zone = e.target.closest?.('[data-drop]');
  if (!zone) return;
  e.preventDefault();
  zone.classList.remove('dragging');
  const engine = ui.engines.get(Number(zone.dataset.drop));
  // Hay que pedir los handles antes de cualquier await: después el evento caduca.
  const handles = [...e.dataTransfer.items].filter((i) => i.kind === 'file').map((i) => i.getAsFileSystemHandle());
  if (!engine || !handles.length) return;
  (async () => {
    const keys = new Set();
    ui.busy++;
    try {
      for (const h of await Promise.all(handles)) if (h) await copyIn(engine.root, h, '', keys);
    } catch (err) {
      fail(err);
    } finally {
      ui.busy--;
    }
    if (!keys.size) return;
    await syncAll(true);
    await uploadMany(engine, keys, 'Agregar lo que arrastraste');
  })();
});

// grantAccess consigue el permiso de la carpeta como sea. Primero lo pide; si el
// navegador lo tiene bloqueado (ya no pregunta), abre el selector en esa misma
// carpeta: elegirla de nuevo también concede el permiso. Si eligen la misma, se
// sigue como siempre, sin volver a bajar nada.
let granting = false;
async function grantAccess() {
  if (granting || !ui.root) return;
  granting = true;
  try {
    let state = 'denied';
    try {
      state = await ui.root.requestPermission({ mode: 'readwrite' });
    } catch (err) {
      diag('permiso-error', `${err.name}: ${err.message}`);
    }
    if (state === 'granted') return resumeWith(ui.root);
    diag('permiso-denegado', state);
    let picked;
    try {
      picked = await showDirectoryPicker({ id: '3dvault', mode: 'readwrite', startIn: ui.root });
    } catch (err) {
      if (err.name !== 'AbortError') diag('selector-error', `${err.name}: ${err.message}`);
      return showDenied();
    }
    if (await picked.isSameEntry(ui.root)) return resumeWith(picked);
    if (!confirm(`Elegiste «${picked.name}», que no es tu carpeta de 3D-Vault («${ui.root.name}»).\n\n¿Usar «${picked.name}» a partir de ahora? Se bajarán ahí tus proyectos.`)) return showDenied();
    ui.root = picked;
    ui.engines.clear();
    await kvSet('root', picked);
    startMain();
  } finally {
    granting = false;
  }
}

async function resumeWith(root) {
  ui.root = root;
  await kvSet('root', root);
  startMain();
}

function showDenied() {
  show('sync-resume');
  $('resume-normal').hidden = true;
  $('resume-denied').hidden = false;
}

async function pickFolder() {
  try {
    const root = await showDirectoryPicker({ id: '3dvault', mode: 'readwrite' });
    ui.root = root;
    ui.engines.clear();
    await kvSet('root', root);
    startMain();
  } catch (err) {
    if (err.name !== 'AbortError') fail(err);
  }
}

// watchFolder revisa la carpeta en cuanto cambia algo en el disco (borrar,
// agregar o guardar), en los navegadores que tienen FileSystemObserver (Chrome
// y Edge recientes). En los demás, se revisa al volver a la pestaña y cada
// minuto. Lo que escribe la propia sincronización también avisa, pero esa
// pasada extra no encuentra nada que hacer.
let watcher = null;
let watchTimer = null;
async function watchFolder() {
  watcher?.disconnect();
  watcher = null;
  if (!('FileSystemObserver' in window) || !ui.root) return;
  try {
    watcher = new FileSystemObserver(() => {
      clearTimeout(watchTimer);
      watchTimer = setTimeout(() => syncAll(), 1500);
    });
    await watcher.observe(ui.root, { recursive: true });
  } catch {
    watcher = null; // sin permiso o sin soporte para esta carpeta
  }
}

function startMain() {
  $('root-name').textContent = ui.root.name;
  renderComforts();
  show('sync-main');
  renderPicks();
  render();
  openStream();
  syncAll();
  watchFolder();
}

// En la pantalla de permiso, cualquier clic en la página sirve para pedirlo: el
// navegador solo deja pedirlo tras un clic, y muchos no veían el botón.
function askOnFirstClick() {
  document.title = '(!) Falta permiso · Mi carpeta · 3D-Vault';
  const ask = async (e) => {
    if ($('sync-resume').hidden) return document.removeEventListener('click', ask, true); // ya se permitió
    if (e.target.closest('[data-action], a, nav, input, label')) return; // los botones hacen lo suyo
    document.removeEventListener('click', ask, true);
    await grantAccess();
  };
  document.addEventListener('click', ask, true);
}

// checkVersion compara esta página con la versión que entrega el servidor. Si
// hay una nueva, recarga para usarla (o en cuanto termine lo que está haciendo).
async function checkVersion(manual = false) {
  const state = document.querySelector('[data-version-state]');
  const btn = document.querySelector('[data-action="check-version"]');
  const say = (text, cls = '') => { if (state) { state.textContent = text; state.className = cls; } };
  if (btn) btn.disabled = true;
  say(manual ? 'Buscando…' : '');
  try {
    const res = await fetch('/api/version', { cache: 'no-store', headers: { ...ui.api?.headers } });
    if (!res.ok) throw new Error(`error ${res.status}`);
    const v = await res.json();
    const blender = $('blender-version');
    if (blender) blender.textContent = v.blender || '—';
    if (!loadedVersion || v.web === loadedVersion) {
      say('Estás al día.', 'pill pos');
      if (manual) notice('Mi carpeta está al día: tienes la última versión.', true);
      return;
    }
    markUpdated();
    if (!ui.busy && !document.querySelector('dialog[open]')) {
      say('Actualizando…', 'pill warn');
      location.reload();
      return;
    }
    say('Hay una versión nueva: se actualiza al terminar lo que está haciendo.', 'pill warn');
  } catch (err) {
    say('No se pudo comprobar. ¿Hay conexión?', 'pill neg');
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function main() {
  if (!('showDirectoryPicker' in window)) return show('sync-unsupported');

  // Una sola pestaña sincroniza: dos a la vez pisarían las mismas descargas.
  const gotLock = await new Promise((resolve) => {
    navigator.locks.request('3dvault-sync', { ifAvailable: true }, (lock) => {
      resolve(Boolean(lock));
      return lock ? new Promise(() => {}) : undefined;
    });
  });
  if (!gotLock) return show('sync-other-tab');

  const name = storage('3dvault-client-name', '') || defaultClientName();
  $('client-name').value = name;
  ui.api = new Api(ui.clientId, name);
  try {
    [ui.me, { projects: ui.projects }] = await Promise.all([ui.api.json('GET', '/api/me'), ui.api.json('GET', '/api/projects')]);
    checkVersion();
  } catch (err) {
    return fail(err);
  }
  ui.projects = ui.projects.filter((p) => !p.archived);
  ui.selected = new Set([...ui.selected].filter((id) => ui.projects.some((p) => p.id === id)));

  ui.root = await kvGet('root');
  if (!ui.root) return show('sync-setup');
  const state = await ui.root.queryPermission({ mode: 'readwrite' });
  if (state === 'granted') return startMain();
  show('sync-resume');
  if (state === 'denied') return showDenied(); // bloqueado: ya no pregunta solo
  askOnFirstClick();
}

setInterval(() => syncAll(), AUTO_EVERY);
// Si algo falla al arrancar, se dice en la página (antes quedaba en blanco) y se
// anota en el servidor para poder ver qué pasó.
const ready = main().catch((err) => {
  diag('error-al-iniciar', `${err?.name}: ${err?.message}`);
  notice(`Mi carpeta no pudo arrancar en este navegador (${browserName()}): ${err?.message || err}. Prueba recargar la página. Si sigue, avisa al administrador.`);
});
addEventListener('error', (e) => diag('error', `${e.message} @ ${e.filename?.split('/').pop()}:${e.lineno}`));
addEventListener('unhandledrejection', (e) => diag('error', `${e.reason?.name}: ${e.reason?.message || e.reason}`));

// Para pruebas automáticas: permite usar una carpeta sin el selector.
window.__3dvault = { ui, copyIn, fail, useFolder: async (root) => { await ready; ui.root = root; ui.engines.clear(); startMain(); } };
