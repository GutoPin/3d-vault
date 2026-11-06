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
export async function walk(dir, prefix = '', out = new Map()) {
  for await (const [name, handle] of dir.entries()) {
    if (ignored(name)) continue;
    const path = prefix + name;
    if (handle.kind === 'directory') await walk(handle, path + '/', out);
    else out.set(keyOf(path), { path, handle, parent: dir, name });
  }
  return out;
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
  constructor({ api, me, clientId, project, root, state, log = () => {}, progress = () => {} }) {
    Object.assign(this, { api, me, clientId, project, root, state, log, progress });
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
    const local = await walk(this.root);
    const states = new Map((await this.state.all(this.project.id)).map((r) => [r.key, r]));
    const rows = [];

    if (confirmFirst && states.size === 0) {
      const todo = snap.assets.filter((a) => !local.has(keyOf(a.path)));
      const bytes = todo.reduce((s, a) => s + a.size_bytes, 0);
      if (todo.length && !(await confirmFirst(todo.length, bytes))) return this.rows;
    }

    for (const asset of snap.assets) {
      const key = keyOf(asset.path);
      const row = { key, asset, local: local.get(key), rec: states.get(key) };
      local.delete(key);
      states.delete(key);
      try {
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

    for (const [key, l] of local) rows.push({ key, local: l, status: 'new' });
    await this.findMoves(rows);
    rows.sort((a, b) => (a.asset?.path ?? a.local.path).localeCompare(b.asset?.path ?? b.local.path));
    this.rows = rows.filter((r) => !r.movedTo);
    return this.rows;
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
