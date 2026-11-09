// Subir desde la ficha de un archivo o desde un proyecto, en cualquier navegador
// y sin carpeta conectada. Solo envía los trozos que el servidor no tiene.
//
// En un proyecto se pueden subir archivos sueltos o carpetas enteras, eligiéndolas
// o arrastrándolas: se conservan las subcarpetas, para que las rutas relativas de
// un .blend (texturas, archivos enlazados) sigan funcionando en otras PCs. Lo que
// ya existe recibe una versión nueva, si está libre o bloqueado por ti.
import { Api, ignored, keyOf, pushFile } from './sync/engine.js';

const form = document.getElementById('web-upload');

function stored(key) {
  try {
    return JSON.parse(localStorage.getItem(key)) || '';
  } catch {
    return '';
  }
}

const fmtBytes = (n) => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i ? n.toFixed(n < 10 ? 1 : 0) : n} ${units[i]}`.replace('.', ',');
};

const count = (n, one, many) => `${n} ${n === 1 ? one : many}`;

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === false || v == null) continue;
    node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) node.append(c);
  return node;
}

// ---------- ficha de un archivo: una versión nueva ----------

function assetUpload(ds) {
  // Si el bloqueo es de Mi carpeta en este navegador, se sube como esa PC. Si no,
  // como la web, igual que los botones Bloquear y Liberar de esta página.
  const clientId = stored('3dvault-client-id');
  const api = new Api(clientId && clientId === ds.lockClient ? clientId : '', stored('3dvault-client-name'));
  const status = form.querySelector('[data-status]');
  const button = form.querySelector('button[type=submit]');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const [file] = form.elements.file.files;
    const comment = form.elements.comment.value.trim();
    button.disabled = true;
    status.classList.remove('sync-error');
    try {
      await pushFile(api, file, {
        assetId: Number(ds.asset),
        baseVersion: Number(ds.base),
        comment,
        progress: (action, done, total) => {
          status.textContent = `${action} ${total ? Math.floor((100 * done) / total) : 100} %`;
        },
      });
      status.textContent = 'Listo.';
      location.reload();
    } catch (err) {
      status.textContent = err.message;
      status.classList.add('sync-error');
      button.disabled = false;
    }
  });
}

// ---------- proyecto: archivos y carpetas ----------

// readDropped recorre lo arrastrado, carpetas incluidas, como {file, rel}.
async function readDropped(items) {
  const out = [];
  const readAll = (reader) => new Promise((resolve, reject) => {
    const all = [];
    const next = () => reader.readEntries((batch) => {
      if (!batch.length) return resolve(all);
      all.push(...batch);
      next();
    }, reject);
    next();
  });
  const visit = async (entry, prefix) => {
    if (entry.isFile) {
      const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
      out.push({ file, rel: prefix + entry.name });
    } else if (entry.isDirectory) {
      for (const child of await readAll(entry.createReader())) await visit(child, `${prefix}${entry.name}/`);
    }
  };
  const entries = [...items].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
  for (const entry of entries) await visit(entry, '');
  return out;
}

function projectUpload(ds) {
  const status = form.querySelector('[data-status]');
  const picked = form.querySelector('[data-picked]');
  const button = form.querySelector('button[type=submit]');
  const myClient = stored('3dvault-client-id');
  const clientName = stored('3dvault-client-name');
  const webApi = new Api('', clientName);
  const folderApi = myClient ? new Api(myClient, clientName) : webApi;
  let items = [];

  const describe = () => {
    if (!items.length) {
      picked.textContent = 'Nada elegido. También puedes arrastrar archivos o carpetas aquí.';
      return;
    }
    const folders = new Set(items.filter((i) => i.rel.includes('/')).map((i) => i.rel.split('/')[0]));
    const bytes = items.reduce((s, i) => s + i.file.size, 0);
    const what = folders.size ? `${count(folders.size, 'carpeta', 'carpetas')}: ${[...folders].join(', ')}` : count(items.length, 'archivo', 'archivos');
    picked.textContent = `${what} · ${count(items.length, 'archivo', 'archivos')}, ${fmtBytes(bytes)}`;
  };

  const choose = (list) => {
    items = list;
    status.textContent = '';
    status.classList.remove('sync-error');
    describe();
  };

  form.elements.file.addEventListener('change', (e) => choose([...e.target.files].map((file) => ({ file, rel: file.name }))));
  form.elements.dir.addEventListener('change', (e) => choose([...e.target.files].map((file) => ({ file, rel: file.webkitRelativePath || file.name }))));

  // Arrastrar sobre toda la página, no solo sobre el formulario.
  let depth = 0;
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
  document.addEventListener('dragenter', (e) => {
    if (!hasFiles(e)) return;
    depth++;
    form.classList.add('dragging');
  });
  document.addEventListener('dragleave', () => {
    depth = Math.max(0, depth - 1);
    if (!depth) form.classList.remove('dragging');
  });
  document.addEventListener('dragover', (e) => {
    if (hasFiles(e)) e.preventDefault();
  });
  document.addEventListener('drop', async (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth = 0;
    form.classList.remove('dragging');
    form.scrollIntoView({ behavior: 'smooth', block: 'center' });
    picked.textContent = 'Leyendo lo que arrastraste…';
    try {
      choose(await readDropped(e.dataTransfer.items));
    } catch (err) {
      picked.textContent = `No se pudo leer lo que arrastraste: ${err.message}`;
    }
  });

  // «Agregar aquí» en cada carpeta de la lista.
  document.addEventListener('click', (e) => {
    const here = e.target.closest('[data-upload-here]');
    if (!here) return;
    form.elements.folder.value = here.dataset.uploadHere.replace(/\/+$/, '');
    document.getElementById('agregar').scrollIntoView({ behavior: 'smooth' });
    form.elements.folder.focus({ preventScroll: true });
  });

  // plan clasifica lo elegido frente al proyecto, antes de subir nada.
  async function plan() {
    const folder = (form.elements.folder.value || '').trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
    const [me, snap] = await Promise.all([webApi.json('GET', '/api/me'), webApi.json('GET', `/api/projects/${ds.project}/snapshot`)]);
    const byKey = new Map(snap.assets.map((a) => [keyOf(a.path), a]));
    const out = { add: [], update: [], locked: [], skipped: [] };
    const seen = new Set();
    for (const item of items) {
      const parts = item.rel.split('/');
      if (parts.some((p) => ignored(p))) {
        out.skipped.push(item);
        continue;
      }
      const path = folder ? `${folder}/${item.rel}` : item.rel;
      const key = keyOf(path);
      if (seen.has(key)) continue;
      seen.add(key);
      const asset = byKey.get(key);
      const job = { ...item, path, asset };
      if (!asset) out.add.push(job);
      else if (asset.lock && asset.lock.user_id !== me.id) out.locked.push(job);
      else out.update.push(job);
    }
    return out;
  }

  function confirmPlan(p) {
    return new Promise((resolve) => {
      const bytes = [...p.add, ...p.update].reduce((s, i) => s + i.file.size, 0);
      const lines = [];
      if (p.add.length) lines.push(count(p.add.length, 'archivo nuevo', 'archivos nuevos'));
      if (p.update.length) lines.push(`${count(p.update.length, 'archivo que ya existe', 'archivos que ya existen')}: se suben como versión nueva si cambiaron`);
      if (p.locked.length) lines.push(`${count(p.locked.length, 'archivo bloqueado', 'archivos bloqueados')} por otra persona: no se suben (${p.locked.slice(0, 3).map((j) => j.path).join(', ')}${p.locked.length > 3 ? '…' : ''})`);
      if (p.skipped.length) lines.push(`${count(p.skipped.length, 'archivo temporal', 'archivos temporales')} de Blender o del sistema: se omiten`);
      const list = el('ul', { class: 'upload-plan' }, lines.map((l) => el('li', {}, l)));
      const dialog = el('dialog', { class: 'panel' },
        el('h2', {}, 'Agregar al proyecto'),
        list,
        el('p', { class: 'sub' }, `En total, hasta ${fmtBytes(bytes)}. Solo viaja lo que el servidor no tenga.`),
        el('div', { class: 'form-foot' },
          el('button', { class: 'btn', type: 'button', value: 'no' }, 'Cancelar'),
          el('button', { class: 'btn primary', type: 'button', value: 'si' }, 'Subir')));
      dialog.addEventListener('click', (e) => {
        const b = e.target.closest('button');
        if (!b) return;
        dialog.close();
        dialog.remove();
        resolve(b.value === 'si');
      });
      dialog.addEventListener('cancel', () => {
        dialog.remove();
        resolve(false);
      });
      document.body.append(dialog);
      dialog.showModal();
    });
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    status.classList.remove('sync-error');
    if (!items.length) {
      status.textContent = 'Elige archivos o una carpeta, o arrástralos aquí.';
      status.classList.add('sync-error');
      return;
    }
    const comment = form.elements.comment.value.trim();
    button.disabled = true;
    try {
      status.textContent = 'Comparando con el proyecto…';
      const p = await plan();
      const jobs = [...p.add, ...p.update];
      if (!jobs.length) {
        status.textContent = 'No hay nada que subir: lo elegido está bloqueado por otra persona o son temporales.';
        button.disabled = false;
        return;
      }
      if (!(await confirmPlan(p))) {
        status.textContent = '';
        button.disabled = false;
        return;
      }

      const done = { added: 0, updated: 0, same: 0 };
      const failed = [];
      for (const [i, job] of jobs.entries()) {
        const label = `${i + 1} de ${jobs.length} · ${job.path}`;
        const progress = (action, n, total) => {
          status.textContent = `${label}: ${action.toLowerCase()} ${total ? Math.floor((100 * n) / total) : 100} %`;
        };
        try {
          const a = job.asset;
          const api = a?.lock && a.lock.client_id === myClient ? folderApi : webApi;
          const { result } = await pushFile(api, job.file, a
            ? { assetId: a.id, baseVersion: a.head_version, comment, progress }
            : { projectId: Number(ds.project), path: job.path, comment, progress });
          if (result.no_changes) done.same++;
          else if (a) done.updated++;
          else done.added++;
        } catch (err) {
          failed.push(`${job.path}: ${err.message}`);
        }
      }

      const parts = [];
      if (done.added) parts.push(count(done.added, 'nuevo', 'nuevos'));
      if (done.updated) parts.push(count(done.updated, 'con versión nueva', 'con versión nueva'));
      if (done.same) parts.push(count(done.same, 'sin cambios', 'sin cambios'));
      if (p.locked.length) parts.push(`${p.locked.length} bloqueados por otra persona`);
      if (!failed.length) {
        status.textContent = `Listo: ${parts.join(', ')}.`;
        location.reload();
        return;
      }
      const again = el('button', { class: 'btn', type: 'button' }, 'Ver el proyecto');
      again.addEventListener('click', () => location.reload());
      status.replaceChildren(
        `Terminado con errores${parts.length ? ` (${parts.join(', ')})` : ''}. No se subieron:`,
        el('ul', { class: 'upload-plan' }, failed.map((f) => el('li', {}, f))),
        again);
      status.classList.add('sync-error');
    } catch (err) {
      status.textContent = err.message;
      status.classList.add('sync-error');
    }
    button.disabled = false;
  });
}

if (form) {
  if (form.dataset.asset) assetUpload(form.dataset);
  else projectUpload(form.dataset);
}
