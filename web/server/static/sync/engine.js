// Motor de la sincronización web: compara una carpeta local (File System Access
// API) con un proyecto del servidor, baja solo los trozos que faltan y sube con
// comentario. Sin DOM: la interfaz está en app.js.
//
// Reglas de PLAN.md sección 8 que aplica: nunca pisar trabajo local (copia
// *.conflicto-*), los borrados y movimientos locales no se propagan, y se
// verifica el SHA-256 antes de reemplazar.

import { hashHex, scan, Sha256 } from './core.js';

const PARALLEL = 4;
const MISSING_BATCH = 2000;

const IGNORE = [/\.blend\d+$/i, /\.blend@$/i, /\.conflicto-/i, /\.tmp$/i, /\.crswap$/i, /^~/, /^\.ds_store$/i, /^thumbs\.db$/i, /^desktop\.ini$/i];

export const ignored = (name) => name.toLowerCase() === '.3dvault-tmp' || IGNORE.some((re) => re.test(name));

// keyOf aproxima path_key del servidor: sin mayúsculas y en NFC.
export const keyOf = (path) => path.normalize('NFC').toLowerCase();

export class ApiError extends Error {
  constructor(status, body) {
    super(body.message || `Error ${status} del servidor.`);
    this.status = status;
    this.body = body;
  }
}

export class Api {
  constructor(clientId, clientName) {
    this.headers = { 'X-Client-Id': clientId, 'X-Client-Name': encodeURIComponent(clientName) };
  }

  async raw(method, url, body) {
    const headers = { ...this.headers };
    if (body !== undefined && !(body instanceof Uint8Array)) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(body);
    }
    const res = await fetch(url, { method, headers, body });
    if (!res.ok) throw new ApiError(res.status, await res.json().catch(() => ({})));
    return res;
  }

  async json(method, url, body) {
    const res = await this.raw(method, url, body);
    return res.status === 204 ? null : res.json();
  }
}

// walk lista los archivos de una carpeta, con ruta relativa y separador "/".
// Si se pasa dirs, también anota cada subcarpeta.
export async function walk(dir, prefix = '', out = new Map(), dirs = null) {
  for await (const [name, handle] of dir.entries()) {
    if (ignored(name)) continue;
    const path = prefix + name;
    if (handle.kind === 'directory') {
      dirs?.set(keyOf(path), { path, handle, parent: dir, name });
      await walk(handle, path + '/', out, dirs);
    } else out.set(keyOf(path), { path, handle, parent: dir, name });
  }
  return out;
}

// parentDirs devuelve las carpetas que contienen una ruta: "a/b/c.png" → a, a/b.
function parentDirs(path) {
  const parts = path.split('/').slice(0, -1);
  return parts.map((_, i) => parts.slice(0, i + 1).join('/'));
}

// FolderMemory recuerda, por proyecto, las carpetas que había a los dos lados en
// la última pasada. Así se distingue una carpeta nueva de una que se borró en el
// otro lado. Vive en localStorage; sin él, nunca se propaga un borrado.
export const folderMemory = {
  get(project) {
    try {
      return new Set(JSON.parse(localStorage.getItem(`3dvault-carpetas-${project}`)) || []);
    } catch {
      return new Set();
    }
  },
  set(project, keys) {
    try {
      localStorage.setItem(`3dvault-carpetas-${project}`, JSON.stringify([...keys]));
    } catch {
      // Sin almacenamiento: las carpetas se siguen creando, pero no se borran.
    }
  },
};

// folderSelection guarda, por proyecto y en esta PC, las carpetas que no se
// sincronizan: se quedan en el servidor y no ocupan disco. Se guardan las
// excluidas, no las incluidas, para que una carpeta nueva se baje por defecto.
export const folderSelection = {
  key: (project) => `3dvault-excluidas-${project}`,
  has(project) {
    try {
      return localStorage.getItem(this.key(project)) !== null;
    } catch {
      return false;
    }
  },
  get(project) {
    try {
      return new Set(JSON.parse(localStorage.getItem(this.key(project))) || []);
    } catch {
      return new Set();
    }
  },
  set(project, keys) {
    try {
      localStorage.setItem(this.key(project), JSON.stringify([...keys]));
    } catch {
      // Sin almacenamiento: se sincroniza todo, como antes.
    }
  },
};

// underAny dice si una ruta es una de las carpetas, o está dentro de alguna.
export function underAny(path, folders) {
  const key = keyOf(path);
  for (const f of folders) if (key === f || key.startsWith(f + '/')) return true;
  return false;
}

// folderTree arma el árbol de carpetas del servidor con lo que ocupa cada una
// (la última versión de sus archivos), para elegir qué sincronizar.
export function folderTree(snap) {
  const nodes = new Map();
  const root = { path: '', key: '', children: [], bytes: 0, files: 0 };
  const node = (path) => {
    const key = keyOf(path);
    if (!nodes.has(key)) {
      const parts = path.split('/');
      const n = { path, key, name: parts[parts.length - 1], bytes: 0, files: 0, children: [] };
      nodes.set(key, n);
      (parts.length > 1 ? node(parts.slice(0, -1).join('/')) : root).children.push(n);
    }
    return nodes.get(key);
  };
  for (const f of snap.folders ?? []) node(f.path);
  for (const a of snap.assets) {
    root.bytes += a.size_bytes;
    root.files++;
    for (const d of parentDirs(a.path)) {
      const n = node(d);
      n.bytes += a.size_bytes;
      n.files++;
    }
  }
  const sort = (n) => {
    n.children.sort((x, y) => x.name.localeCompare(y.name));
    n.children.forEach(sort);
  };
  sort(root);
  return root;
}

async function parentOf(root, path, create) {
  const parts = path.split('/');
  const name = parts.pop();
  let dir = root;
  for (const p of parts) dir = await dir.getDirectoryHandle(p, { create });
  return { dir, name };
}

function conflictName(name, username, now = new Date()) {
  const pad = (n) => String(n).padStart(2, '0');
  const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}`;
  const dot = name.lastIndexOf('.');
  const [stem, ext] = dot > 0 ? [name.slice(0, dot), name.slice(dot)] : [name, ''];
  return `${stem}.conflicto-${username}-${stamp}${ext}`;
}

const sameFile = (rec, file) => rec && rec.size === file.size && rec.mtime === file.lastModified;

// pushFile sube un archivo enviando solo los trozos que faltan y confirma la
// versión (o crea el archivo si no hay assetId). La usan Mi carpeta y la subida
// desde la ficha o el proyecto, en cualquier navegador.
export async function pushFile(api, file, { scan: doScan, assetId, baseVersion, projectId, path, comment, releaseLock, progress = () => {} }) {
  const { manifest, offsets } = doScan ? await doScan() : await scan(file, (n) => progress('Analizando', n, file.size));
  const sizes = new Map(manifest.chunks.map((c) => [c.sha256, c.size]));
  const unique = [...offsets.keys()];
  const missing = [];
  for (let i = 0; i < unique.length; i += MISSING_BATCH) {
    const res = await api.json('POST', '/api/chunks/missing', { hashes: unique.slice(i, i + MISSING_BATCH) });
    missing.push(...res.missing);
  }

  const total = missing.reduce((s, h) => s + sizes.get(h), 0);
  let sent = 0;
  let next = 0;
  const worker = async () => {
    while (next < missing.length) {
      const h = missing[next++];
      const off = offsets.get(h);
      const data = new Uint8Array(await file.slice(off, off + sizes.get(h)).arrayBuffer());
      await api.raw('PUT', `/api/chunks/${h}`, data);
      sent += data.length;
      progress('Subiendo', sent, total);
    }
  };
  try {
    await Promise.all(Array.from({ length: PARALLEL }, worker));
  } finally {
    progress('Subiendo', total, total);
  }

  const result = assetId
    ? await api.json('POST', `/api/assets/${assetId}/versions`, {
        base_version: baseVersion, manifest, size_bytes: file.size, comment, release_lock: Boolean(releaseLock),
      })
    : await api.json('POST', '/api/assets', { project_id: projectId, path, manifest, comment });
  return { result, sha: manifest.file_sha256, reused: 100 * (1 - total / (file.size || 1)) };
}

// Project sincroniza un proyecto con su carpeta. state guarda, por archivo, la
// versión que hay en disco: {get, put, delete, all} sobre IndexedDB.
export class Project {
  constructor({ api, me, clientId, project, root, state, log = () => {}, progress = () => {}, folders = folderMemory, selection = folderSelection }) {
    Object.assign(this, { api, me, clientId, project, root, state, log, progress, folders, selection });
    this.snap = null; // la última foto del servidor, para elegir carpetas
    this.remote = { files: 0, bytes: 0 }; // lo que se queda solo en el servidor
    this.scans = new Map(); // key → {size, mtime, scan}: evita recalcular archivos que no cambian
    this.rows = [];
  }

  async scanCached(key, file) {
    const hit = this.scans.get(key);
    if (hit && sameFile(hit, file)) return hit.scan;
    const result = await scan(file, (n) => this.progress({ key, action: 'Analizando', done: n, total: file.size }));
    this.scans.set(key, { size: file.size, mtime: file.lastModified, scan: result });
    return result;
  }

  mine(lock) {
    return lock && lock.user_id === this.me.id && lock.client_id === this.clientId;
  }

  async record(key, asset, file, sha) {
    const rec = {
      key, project: this.project.id, path: asset.path, assetId: asset.id,
      version: asset.head_version, versionId: asset.head_version_id, sha, size: file.size, mtime: file.lastModified,
    };
    await this.state.put(rec);
    return rec;
  }

  // sync compara y aplica lo automático: bajar archivos nuevos y versiones
  // nuevas, y resolver conflictos guardando la copia local. Devuelve las filas
  // para la interfaz. confirmFirst recibe el total a bajar en la primera vez.
  async sync({ confirmFirst } = {}) {
    const snap = await this.api.json('GET', `/api/projects/${this.project.id}/snapshot`);
    this.snap = snap;
    const excluded = this.selection.get(this.project.id);
    const outside = (path) => underAny(path, excluded);
    const localDirs = new Map();
    const local = await walk(this.root, '', new Map(), localDirs);
    const states = new Map((await this.state.all(this.project.id)).map((r) => [r.key, r]));
    const rows = [];

    if (confirmFirst && states.size === 0) {
      const todo = snap.assets.filter((a) => !outside(a.path) && !local.has(keyOf(a.path)));
      const bytes = todo.reduce((s, a) => s + a.size_bytes, 0);
      if (todo.length && !(await confirmFirst(todo.length, bytes))) return this.rows;
    }

    await this.followServerMoves(snap, local, states, outside);

    this.remote = { files: 0, bytes: 0 };
    let freed = 0;
    for (const asset of snap.assets) {
      const key = keyOf(asset.path);
      const row = { key, asset, local: local.get(key), rec: states.get(key) };
      local.delete(key);
      states.delete(key);
      try {
        if (outside(asset.path)) {
          // Carpeta que esta PC no sincroniza: se libera el disco, salvo trabajo sin subir.
          freed += await this.leaveOnServer(row);
          if (!row.local) {
            this.remote.files++;
            this.remote.bytes += asset.size_bytes;
            continue;
          }
          row.outside = true;
        }
        await this.reconcile(row);
      } catch (err) {
        row.status = 'error';
        row.error = err.message;
      }
      rows.push(row);
    }

    // Lo que queda en el estado ya no existe en el servidor: borrado o movido.
    for (const rec of states.values()) {
      const l = local.get(rec.key);
      if (l && sameFile(rec, await l.handle.getFile())) {
        await l.parent.removeEntry(l.name);
        local.delete(rec.key);
        this.log(`${rec.path} se quitó porque ya no está en el servidor.`);
      }
      await this.state.delete(rec.key);
    }

    for (const [key, l] of local) rows.push({ key, local: l, status: 'new', outside: outside(l.path) });
    await this.dropEmptyExcluded(localDirs, outside);
    if (freed) this.log(`Se liberaron ${(freed / 2 ** 30).toFixed(2).replace('.', ',')} GB en esta PC. Esas carpetas siguen en el servidor.`);
    try {
      await this.syncFolders(snap, localDirs, outside);
    } catch (err) {
      this.log(`No se pudieron sincronizar las carpetas: ${err.message}`);
    }
    await this.findMoves(rows);
    rows.sort((a, b) => (a.asset?.path ?? a.local.path).localeCompare(b.asset?.path ?? b.local.path));
    this.rows = rows.filter((r) => !r.movedTo);
    return this.rows;
  }

  // followServerMoves aplica en el disco lo que se movió en el servidor, por
  // ejemplo al renombrar una carpeta: el archivo local se cambia de lugar en vez
  // de bajarlo de nuevo. Solo si no tiene cambios sin subir; si los tiene, queda
  // donde estaba y se ve como "Nuevo" junto al del servidor, sin perder nada.
  async followServerMoves(snap, local, states, outside = () => false) {
    const byAsset = new Map([...states.values()].map((r) => [r.assetId, r]));
    for (const asset of snap.assets) {
      const key = keyOf(asset.path);
      const rec = byAsset.get(asset.id);
      if (!rec || rec.key === key || states.has(key) || local.has(key) || outside(asset.path)) continue;
      const old = local.get(rec.key);
      if (!old) continue;
      const file = await old.handle.getFile();
      if (!sameFile(rec, file)) continue;
      const { dir, name } = await parentOf(this.root, asset.path, true);
      let handle;
      if (typeof old.handle.move === 'function') {
        try {
          await old.handle.move(dir, name);
          handle = old.handle;
        } catch {
          handle = null; // sin move() en esta carpeta: se copia
        }
      }
      if (!handle) {
        handle = await dir.getFileHandle(name, { create: true });
        const out = await handle.createWritable();
        await file.stream().pipeTo(out);
        await old.parent.removeEntry(old.name);
      }
      const moved = await handle.getFile();
      await this.state.delete(rec.key);
      const next = { ...rec, key, path: asset.path, size: moved.size, mtime: moved.lastModified };
      await this.state.put(next);
      states.delete(rec.key);
      states.set(key, next);
      local.delete(rec.key);
      local.set(key, { path: asset.path, handle, parent: dir, name });
      this.scans.delete(rec.key);
      this.log(`${rec.path} se movió a ${asset.path}.`);
    }
  }

  // syncFolders iguala las carpetas, también las vacías. Una carpeta nueva en un
  // lado se crea en el otro. Una que existía en los dos y desaparece de uno se
  // borra del otro, pero solo si está vacía: los archivos tienen sus propias
  // reglas y nunca se borran por una carpeta.
  // leaveOnServer trata un archivo de una carpeta que esta PC no sincroniza. Si
  // está igual que en el servidor y no lo tienes bloqueado, lo quita del disco y
  // devuelve los bytes liberados. Si tiene cambios sin subir o lo tienes
  // bloqueado, lo deja (devuelve 0) y sigue sincronizándose: nunca se pierde
  // trabajo por desmarcar una carpeta.
  async leaveOnServer(row) {
    const { asset, local: l, rec } = row;
    if (!l) {
      if (rec) await this.state.delete(row.key);
      return 0;
    }
    if (this.mine(asset.lock)) return 0;
    const file = await l.handle.getFile();
    let same = rec ? sameFile(rec, file) : false;
    if (!same) {
      const { manifest } = await this.scanCached(row.key, file);
      same = manifest.file_sha256 === asset.file_sha256 || Boolean(rec && manifest.file_sha256 === rec.sha);
    }
    if (!same) return 0;
    await l.parent.removeEntry(l.name);
    if (rec) await this.state.delete(row.key);
    this.scans.delete(row.key);
    row.local = null;
    row.rec = null;
    return file.size;
  }

  // dropEmptyExcluded quita de la PC las carpetas excluidas que quedaron vacías,
  // de la más profunda a la más alta. Si tienen algo, se quedan.
  async dropEmptyExcluded(localDirs, outside) {
    for (const [key, d] of [...localDirs].sort((a, b) => b[1].path.length - a[1].path.length)) {
      if (!outside(d.path)) continue;
      let empty = true;
      for await (const _ of d.handle.entries()) {
        empty = false;
        break;
      }
      if (empty) {
        await d.parent.removeEntry(d.name).catch(() => {});
        localDirs.delete(key);
      }
    }
  }

  async syncFolders(snap, localDirs, outside = () => false) {
    const server = new Map(); // clave → ruta
    const explicit = new Set(snap.folders.map((f) => keyOf(f.path)));
    const withFiles = new Set();
    for (const f of snap.folders) for (const d of [...parentDirs(f.path), f.path]) server.set(keyOf(d), d);
    for (const a of snap.assets) {
      for (const d of parentDirs(a.path)) {
        server.set(keyOf(d), d);
        withFiles.add(keyOf(d));
      }
    }
    const known = this.folders.get(this.project.id);
    const now = new Set();

    // Del servidor a la carpeta. Las excluidas no se crean, y que falten aquí
    // no significa que se hayan borrado: nunca se borran en el servidor.
    for (const [key, path] of [...server].sort((a, b) => a[1].length - b[1].length)) {
      if (outside(path)) continue;
      if (localDirs.has(key)) {
        now.add(key);
        continue;
      }
      if (known.has(key) && explicit.has(key) && !withFiles.has(key)) {
        // Estaba en esta PC y alguien la borró aquí: se borra en el servidor.
        await this.api.json('DELETE', `/api/projects/${this.project.id}/folders?path=${encodeURIComponent(path)}`).catch((err) => {
          if (err.status !== 404) throw err;
        });
        this.log(`Carpeta ${path} borrada también en el servidor.`);
        continue;
      }
      if (known.has(key) && !withFiles.has(key)) continue; // su padre se borró aquí
      let dir = this.root;
      for (const part of path.split('/')) dir = await dir.getDirectoryHandle(part, { create: true });
      now.add(key);
      if (!withFiles.has(key)) this.log(`Carpeta ${path} creada en esta PC.`);
    }

    // De la carpeta al servidor. Las más profundas primero, para borrar.
    for (const [key, d] of [...localDirs].sort((a, b) => b[1].path.length - a[1].path.length)) {
      if (server.has(key) || outside(d.path)) continue;
      if (known.has(key)) {
        // Se borró en el servidor: se quita aquí si quedó vacía.
        let empty = true;
        for await (const [name] of d.handle.entries()) {
          if (!ignored(name)) {
            empty = false;
            break;
          }
        }
        if (empty) {
          await d.parent.removeEntry(d.name, { recursive: true });
          this.log(`Carpeta ${d.path} quitada: se borró en el servidor.`);
          continue;
        }
      }
      await this.api.json('POST', `/api/projects/${this.project.id}/folders`, { path: d.path });
      now.add(key);
      this.log(`Carpeta ${d.path} creada en el servidor.`);
    }
    this.folders.set(this.project.id, now);
  }

  // findMoves empareja un archivo nuevo con uno que falta y tiene el mismo
  // contenido: es un renombrado o un movimiento hecho en el Explorador.
  async findMoves(rows) {
    const missing = rows.filter((r) => r.status === 'missing');
    if (!missing.length) return;
    for (const row of rows.filter((r) => r.status === 'new')) {
      const file = await row.local.handle.getFile();
      if (!missing.some((m) => !m.movedTo && m.asset.size_bytes === file.size)) continue;
      const { manifest } = await this.scanCached(row.key, file);
      const from = missing.find((m) => !m.movedTo && m.asset.file_sha256 === manifest.file_sha256);
      if (from) {
        row.status = 'moved';
        row.from = from;
        from.movedTo = row;
      }
    }
  }

  async reconcile(row) {
    const { asset, local: l } = row;
    let rec = row.rec;
    row.lockedByMe = this.mine(asset.lock);

    if (!l) {
      if (rec) {
        row.status = 'missing';
        return;
      }
      await this.download(row, null);
      row.status = 'ok';
      return;
    }

    const file = await l.handle.getFile();
    let changed = !sameFile(rec, file);
    if (changed) {
      const { manifest } = await this.scanCached(row.key, file);
      if (manifest.file_sha256 === asset.file_sha256) {
        row.rec = await this.record(row.key, asset, file, manifest.file_sha256);
        row.status = row.lockedByMe ? 'locked' : 'ok';
        return;
      }
      if (rec && manifest.file_sha256 === rec.sha) {
        rec = row.rec = { ...rec, size: file.size, mtime: file.lastModified };
        await this.state.put(rec);
        changed = false;
      }
    }

    if (!changed) {
      if (rec.version < asset.head_version) {
        await this.download(row, { file, versionId: rec.versionId });
        this.log(`${asset.path} actualizado a v${asset.head_version} por ${asset.uploaded_by}.`);
      }
      row.status = row.lockedByMe ? 'locked' : 'ok';
      return;
    }

    if (rec && rec.version === asset.head_version) {
      row.status = 'changed';
      return;
    }

    // Cambió aquí y también en el servidor (o nunca se sincronizó): se guarda la
    // copia local con otro nombre y se baja la del servidor.
    const copy = conflictName(l.name, this.me.username);
    const out = await (await l.parent.getFileHandle(copy, { create: true })).createWritable();
    await file.stream().pipeTo(out);
    this.scans.delete(row.key);
    await this.download(row, null, true);
    row.status = row.lockedByMe ? 'locked' : 'ok';
    this.log(`Conflicto en ${asset.path}: tu copia quedó como ${copy} y se bajó la v${asset.head_version}.`);
  }

  // download escribe la última versión. base es el archivo local con la versión
  // anterior: de él se copian los trozos que no cambiaron.
  async download(row, base, overwrite = false, reuse = true) {
    const { asset } = row;
    const m = await this.api.json('GET', `/api/versions/${asset.head_version_id}/manifest`);
    const have = new Map();
    if (base && reuse) {
      // Si purge borró esa versión, no hay de dónde copiar: se baja completo.
      const old = await this.api.json('GET', `/api/versions/${base.versionId}/manifest`).catch((err) => {
        if (err.status === 404) return { chunks: [] };
        throw err;
      });
      let off = 0;
      for (const c of old.chunks) {
        if (!have.has(c.sha256)) have.set(c.sha256, off);
        off += c.size;
      }
    }

    const { dir, name } = await parentOf(this.root, asset.path, true);
    let existed = true;
    const handle = await dir.getFileHandle(name).catch(async () => {
      existed = false;
      return dir.getFileHandle(name, { create: true });
    });

    // Justo antes de reemplazar: si alguien guardó mientras tanto, no se toca.
    if (existed && !overwrite) {
      const now = await handle.getFile();
      if (!base || !sameFile({ size: base.file.size, mtime: base.file.lastModified }, now)) {
        throw new Error('El archivo cambió mientras se sincronizaba. Se revisará de nuevo.');
      }
    }

    const total = m.chunks.reduce((s, c) => s + c.size, 0);
    let done = 0;
    let fetched = 0;
    const get = async (c) => {
      const off = have.get(c.sha256);
      if (off !== undefined) return new Uint8Array(await base.file.slice(off, off + c.size).arrayBuffer());
      const data = new Uint8Array(await (await this.api.raw('GET', `/api/chunks/${c.sha256}`)).arrayBuffer());
      if ((await hashHex(data)) !== c.sha256) throw new Error('Un trozo llegó dañado. Se reintentará.');
      fetched += data.length;
      return data;
    };

    const out = await handle.createWritable();
    try {
      const sha = new Sha256();
      const pending = [];
      let next = 0;
      const fill = () => {
        while (pending.length < PARALLEL && next < m.chunks.length) {
          const p = get(m.chunks[next++]);
          p.catch(() => {});
          pending.push(p);
        }
      };
      fill();
      while (pending.length) {
        const data = await pending.shift();
        fill();
        sha.update(data);
        await out.write(data);
        done += data.length;
        this.progress({ key: row.key, action: 'Descargando', done, total });
      }
      if (sha.hex() !== m.file_sha256) throw new Error('El archivo descargado no coincide con el del servidor.');
      await out.close();
    } catch (err) {
      await out.abort().catch(() => {});
      if (!existed) await dir.removeEntry(name).catch(() => {});
      // Si la copia local no era la que se creía, se baja completo.
      if (have.size && err.message.startsWith('El archivo descargado')) return this.download(row, base, overwrite, false);
      throw err;
    } finally {
      this.progress({ key: row.key, done: total, total });
    }

    const file = await handle.getFile();
    row.local = { path: asset.path, handle, parent: dir, name };
    row.rec = await this.record(row.key, asset, file, m.file_sha256);
    this.scans.delete(row.key);
    return { total, fetched };
  }

  // upload sube un archivo con comentario. Si es nuevo, lo crea en el servidor.
  async upload(row, comment, releaseLock) {
    const file = await row.local.handle.getFile();
    const r = await pushFile(this.api, file, {
      scan: () => this.scanCached(row.key, file),
      assetId: row.asset?.id, baseVersion: row.rec?.version, projectId: this.project.id, path: row.local.path, comment, releaseLock,
      progress: (action, done, total) => this.progress({ key: row.key, action, done, total }),
    });
    await this.record(row.key, r.result.asset, file, r.sha);
    return { noChanges: r.result.no_changes, version: r.result.version.number, reused: r.reused };
  }

  // move cambia la ruta en el servidor de un archivo que se renombró o movió en
  // la carpeta, para que conserve su historial.
  async move(row) {
    const { from } = row;
    await this.api.json('POST', `/api/assets/${from.asset.id}/move`, { base_version: from.asset.head_version, path: row.local.path });
    await this.state.delete(from.key);
    const file = await row.local.handle.getFile();
    await this.record(row.key, { ...from.asset, path: row.local.path }, file, from.asset.file_sha256);
  }

  lock(row, takeover = false) {
    return this.api.json('POST', `/api/assets/${row.asset.id}/lock`, { takeover });
  }

  unlock(row) {
    return this.api.json('DELETE', `/api/assets/${row.asset.id}/lock`);
  }

  async restoreMissing(row) {
    await this.state.delete(row.key);
    row.rec = null;
    await this.download(row, null);
  }

  async deleteOnServer(row) {
    await this.api.json('DELETE', `/api/assets/${row.asset.id}?base_version=${row.rec.version}`);
    await this.state.delete(row.key);
  }
}
