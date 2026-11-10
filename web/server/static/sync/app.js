// Interfaz de "Mi carpeta". La lógica de sincronización está en engine.js.
import { Api, ApiError, Project, ignored, keyOf } from './engine.js';

const $ = (id) => document.getElementById(id);
const AUTO_EVERY = 60_000;
const STALE_LOCK = 24 * 3600_000;
const NEEDS_ME = new Set(['changed', 'new', 'moved', 'missing', 'error']);
const EVENT_KINDS = ['asset_created', 'version_created', 'locked', 'unlocked', 'moved', 'deleted', 'folder_created', 'folder_deleted', 'folder_renamed'];

const STATUS = {
  ok: 'Al día',
  locked: 'Bloqueado por ti',
  changed: 'Cambios sin subir',
  new: 'Nuevo, sin subir',
  missing: 'No está en tu carpeta',
  moved: 'Renombrado o movido',
  error: 'Error',
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
};

if (!ui.clientId) {
  ui.clientId = crypto.randomUUID();
  store('3dvault-client-id', ui.clientId);
}

function defaultClientName() {
  const platform = navigator.userAgentData?.platform || navigator.platform || 'PC';
  return `Navegador en ${platform}`;
}

function show(id) {
  for (const s of ['sync-unsupported', 'sync-other-tab', 'sync-setup', 'sync-resume', 'sync-main']) $(s).hidden = s !== id;
  $('sync-actions').hidden = id !== 'sync-main';
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
    if (k === 'class') node.className = v;
    else if (k.startsWith('data-')) node.setAttribute(k, v);
    else node[k] = v;
  }
  node.append(...children.filter((c) => c != null && c !== false));
  return node;
}

function actionButton(label, action, pid, key, primary = false) {
  return el('button', { class: primary ? 'btn primary' : 'btn', type: 'button', 'data-action': action, 'data-project': pid, 'data-key': key }, label);
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
    out.push(actionButton('Descargar', 'restore', pid, row.key));
    out.push(actionButton('Borrar del servidor', 'delete', pid, row.key));
  }
  if (a && row.status !== 'missing') {
    if (row.lockedByMe) out.push(actionButton('Liberar', 'unlock', pid, row.key));
    else if (lock && lock.user_id === ui.me.id) out.push(actionButton('Traer el bloqueo a esta PC', 'takeover', pid, row.key));
    else if (!lock) out.push(actionButton('Bloquear', 'lock', pid, row.key));
    else out.push(actionButton('Pedir que lo libere', 'request', pid, row.key));
  }
  return out;
}

function statusCell(row) {
  const p = ui.progress.get(row.key);
  if (p && p.done < p.total) {
    let rate = '';
    const secs = (performance.now() - p.t0) / 1000;
    if (secs > 1 && p.done > p.d0) {
      const speed = (p.done - p.d0) / secs;
      const left = Math.ceil((p.total - p.done) / speed);
      rate = ` · ${formatBytes(speed)}/s · ${left < 60 ? `${left} s` : `${Math.ceil(left / 60)} min`}`;
    }
    return el('td', {}, `${p.action} ${Math.floor((100 * p.done) / p.total)} %${rate}`, el('progress', { max: p.total, value: p.done }));
  }
  let text = STATUS[row.status] || '';
  if (row.status === 'error') text = `Error: ${row.error}`;
  const lock = row.asset?.lock;
  let owner = lock && !row.lockedByMe ? `Bloqueado por ${lock.display_name}${lock.user_id === ui.me.id ? ` en ${lock.client_name}` : ''}` : null;
  if (row.lockedByMe && row.status !== 'locked') owner = 'Bloqueado por ti';
  if (row.status === 'changed' && !lock) owner = 'Sin bloquear: bloquéalo para que nadie suba encima.';
  if (row.status === 'moved') owner = `Antes: ${row.from.asset.path}. Muévelo en el servidor para conservar su historial.`;
  return el('td', { class: row.status === 'error' ? 'sync-error' : '' }, text, owner && el('span', { class: 'sub' }, owner));
}

function render() {
  const box = $('project-tables');
  box.textContent = '';
  let pending = 0;
  for (const p of ui.projects.filter((p) => ui.selected.has(p.id))) {
    const engine = ui.engines.get(p.id);
    const all = engine?.rows ?? [];
    const query = $('filter').value.trim().toLowerCase();
    const onlyPending = $('only-pending').checked;
    const rows = all.filter((r) => (!query || r.key.includes(query)) && (!onlyPending || NEEDS_ME.has(r.status)));
    const toUpload = all.filter((r) => r.status === 'changed' || r.status === 'new').length;
    pending += toUpload + all.filter((r) => r.status === 'error' || r.status === 'moved').length;
    const body = rows.map((row) => el('tr', {},
      el('td', {}, row.asset ? el('a', { class: 'name', href: `/assets/${row.asset.id}` }, row.asset.path) : el('span', { class: 'name' }, row.local.path)),
      el('td', { class: 'n' }, row.asset ? `v${row.asset.head_version}` : '—'),
      el('td', { class: 'n' }, formatBytes(row.asset?.size_bytes ?? row.from?.asset.size_bytes ?? 0)),
      statusCell(row),
      el('td', { class: 'row-actions' }, ...rowActions(p.id, row)),
    ));
    box.append(
      el('div', { class: 'ledger-head' }, el('h2', {}, p.name),
        el('p', {}, `Carpeta: ${safeName(p.name)}`, toUpload > 1 && el('button', { class: 'btn primary', type: 'button', 'data-action': 'upload-all', 'data-project': p.id }, `Subir todo (${toUpload})`))),
      el('div', { class: 'panel table-panel drop-zone', 'data-drop': p.id }, rows.length
        ? el('div', { class: 'table-wrap' }, el('table', { class: 'stack-table' },
            el('thead', {}, el('tr', {}, el('th', {}, 'Archivo'), el('th', { class: 'n' }, 'Versión'), el('th', { class: 'n' }, 'Tamaño'), el('th', {}, 'Estado'), el('th', {}, ''))),
            el('tbody', {}, ...body)))
        : el('p', { class: 'empty' }, !engine ? 'Sincronizando…'
          : all.length ? 'Ningún archivo coincide con el filtro.'
          : 'Este proyecto todavía no tiene archivos. Arrástralos aquí o cópialos en su carpeta.'),
        engine && rows.length > 0 && el('p', { class: 'drop-hint' }, 'Arrastra aquí archivos o carpetas para agregarlos al proyecto.')),
    );
  }
  if (!ui.selected.size) box.append(el('p', { class: 'empty' }, 'Marca arriba los proyectos que quieres tener en esta PC.'));
  const state = ui.paused ? 'Sincronización en pausa.' : ui.running ? 'Sincronizando…' : 'Sincronización automática activa.';
  $('sync-summary').textContent = pending ? `${state} ${pending} archivo(s) esperan que hagas algo.` : state;
  document.title = `${pending ? `(${pending}) ` : ''}Mi carpeta · 3D-Vault`;

  // Bloqueos propios de más de un día: un olvido frecuente que frena a los demás.
  const old = [...ui.engines.values()].flatMap((e) => e.rows)
    .filter((r) => r.lockedByMe && Date.now() - Date.parse(r.asset.lock.locked_at) > STALE_LOCK);
  $('stale-locks').hidden = !old.length;
  $('stale-locks').textContent = old.length
    ? `Tienes ${old.length === 1 ? 'un archivo bloqueado' : `${old.length} archivos bloqueados`} desde hace más de un día: ${old.map((r) => r.asset.path).join(', ')}. Si ya no los estás editando, súbelos o libéralos para que otros puedan trabajar.`
    : '';
  $('pause-btn').textContent = ui.paused ? 'Reanudar' : 'Pausar';
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

// ---------- sincronización ----------

async function engineFor(p) {
  let engine = ui.engines.get(p.id);
  if (!engine) {
    const dir = await ui.root.getDirectoryHandle(safeName(p.name), { create: true });
    engine = new Project({
      api: ui.api, me: ui.me, clientId: ui.clientId, project: p, root: dir, state: stateFor(p.id),
      log: (text) => {
        log(text);
        notify(text);
      },
      progress: (pr) => {
        const prev = ui.progress.get(pr.key);
        const same = prev?.action === pr.action;
        if (pr.done >= pr.total) ui.progress.delete(pr.key);
        else ui.progress.set(pr.key, { ...pr, t0: same ? prev.t0 : performance.now(), d0: same ? prev.d0 : pr.done });
        scheduleRender();
      },
    });
    ui.engines.set(p.id, engine);
  }
  return engine;
}

let renderQueued = false;
function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => {
    renderQueued = false;
    render();
  });
}

const confirmFirst = (n, bytes) => Promise.resolve(confirm(`Se descargarán ${n} archivo(s), ${formatBytes(bytes)} en total. ¿Empezar?`));

// syncAll pone en cola una pasada completa. La promesa se cumple al terminar una
// pasada que empezó después de la llamada, así quien espera ve lo último.
let current = Promise.resolve();
let waiting = null;
function syncAll(manual = false) {
  if (!ui.root || (ui.paused && !manual)) return Promise.resolve();
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
  render();
  try {
    for (const p of ui.projects.filter((p) => ui.selected.has(p.id))) {
      const engine = await engineFor(p);
      await engine.sync({ confirmFirst });
      render();
    }
    if (manual) notice('Todo sincronizado.', true);
  } catch (err) {
    fail(err);
  } finally {
    ui.running = false;
    ui.busy--;
    render();
  }
}

function openStream() {
  ui.stream?.close();
  ui.stream = null;
  if (!ui.selected.size) return;
  const stream = new EventSource(`/api/stream?projects=${[...ui.selected].join(',')}`);
  for (const kind of [...EVENT_KINDS, 'reset']) stream.addEventListener(kind, () => syncAll());
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
  const rows = () => engine.rows.filter((r) => keys.has(r.key) && (r.status === 'changed' || r.status === 'new'));
  const list = rows();
  if (!list.length) return;
  const allNew = list.every((r) => r.status === 'new');
  const answer = await askComment(`${title} (${list.length})`, allNew ? 'Primera versión' : '', list.some((r) => r.lockedByMe));
  if (!answer) return;
  await run(async () => {
    const failed = [];
    for (const row of list) {
      try {
        const r = await engine.upload(row, answer.comment, answer.release && row.lockedByMe);
        log(`${row.asset?.path ?? row.local.path}: ${r.noChanges ? 'sin cambios' : `subido como v${r.version}`}.`);
      } catch (err) {
        failed.push(`${row.local.path}: ${err.message}`);
      }
    }
    if (failed.length) notice(`No se pudieron subir ${failed.length}: ${failed.join(' · ')}`);
    else notice(`Listo: ${list.length} archivo(s) subidos.`, true);
  });
}

const actions = {
  'sync-now': () => syncAll(true),
  pause: () => {
    ui.paused = !ui.paused;
    render();
    if (!ui.paused) syncAll();
  },
  pick: pickFolder,
  resume: async () => {
    if ((await ui.root.requestPermission({ mode: 'readwrite' })) === 'granted') startMain();
  },
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
  'upload-all': (engine) => uploadMany(engine, new Set(engine.rows.map((r) => r.key)), 'Subir todo'),
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
  restore: (engine, row) => run(() => engine.restoreMissing(row)),
  delete: (engine, row) => run(async () => {
    if (!confirm(`¿Borrar ${row.asset.path} del servidor? Desaparecerá de las PCs de todos. El administrador puede recuperarlo hasta la próxima limpieza.`)) return;
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
  if (!btn.dataset.key) return engine && fn(engine);
  const row = engine?.rows.find((r) => r.key === btn.dataset.key);
  if (row) fn(engine, row);
});

document.addEventListener('change', (e) => {
  const id = e.target.dataset?.pick;
  if (!id) return;
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

function startMain() {
  $('root-name').textContent = ui.root.name;
  renderComforts();
  show('sync-main');
  renderPicks();
  render();
  openStream();
  syncAll();
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
  } catch (err) {
    return fail(err);
  }
  ui.projects = ui.projects.filter((p) => !p.archived);
  ui.selected = new Set([...ui.selected].filter((id) => ui.projects.some((p) => p.id === id)));

  ui.root = await kvGet('root');
  if (!ui.root) return show('sync-setup');
  if ((await ui.root.queryPermission({ mode: 'readwrite' })) === 'granted') return startMain();
  show('sync-resume');
}

setInterval(() => syncAll(), AUTO_EVERY);
const ready = main();

// Para pruebas automáticas: permite usar una carpeta sin el selector.
window.__3dvault = { ui, copyIn, useFolder: async (root) => { await ready; ui.root = root; ui.engines.clear(); startMain(); } };
