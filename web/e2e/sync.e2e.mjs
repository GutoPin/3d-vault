// Prueba de punta a punta de "Mi carpeta": dos navegadores (dos PCs) contra un
// servidor real, cada uno con su carpeta (OPFS, la misma API que una carpeta local).
//
// Uso: make e2e. Requiere bin/3dvault-server compilado y un Chromium; por
// defecto el de Playwright (npx playwright install chromium), o CHROME=/ruta.
import { chromium } from 'playwright-core';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { readdirSync } from 'node:fs';
import assert from 'node:assert/strict';

const REPO = resolve(import.meta.dirname, '../..');
const BIN = join(REPO, 'bin/3dvault-server');
const PW = `${process.env.HOME}/.cache/ms-playwright`;
const CHROME = process.env.CHROME || `${PW}/${readdirSync(PW).filter((d) => /^chromium-\d+$/.test(d)).sort().pop()}/chrome-linux64/chrome`;
const PORT = 18080;
const BASE = `http://127.0.0.1:${PORT}`;
const data = mkdtempSync(join(tmpdir(), '3dvault-e2e-'));
const env = { ...process.env, SECRET_KEY: 'x'.repeat(48), DATA_DIR: data, LISTEN_ADDR: `127.0.0.1:${PORT}`, INSECURE_COOKIES: 'true' };

execFileSync(BIN, ['create-admin', '--username', 'guto', '--name', 'Guto'], { env, input: 'contraseña-larga-1\n' });
const server = spawn(BIN, ['serve'], { env, stdio: ['ignore', 'ignore', 'inherit'] });
for (let i = 0; i < 50; i++) {
  try { if ((await fetch(`${BASE}/healthz`)).ok) break; } catch {}
  await new Promise((r) => setTimeout(r, 100));
}

const browser = await chromium.launch({ executablePath: CHROME });
const errors = [];

async function pc(user, pass) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.on('dialog', (d) => d.accept());
  page.on('pageerror', (e) => errors.push(`${user}: ${e.message}`));
  page.on('console', (m) => m.type() === 'error' && errors.push(`${user}: ${m.text()}`));
  await page.goto(`${BASE}/login`);
  await page.fill('input[name=username]', user);
  await page.fill('input[name=password]', pass);
  await page.click('button[type=submit]');
  await page.waitForURL(`${BASE}/`);
  return page;
}

// En la página: helpers sobre la carpeta OPFS del proyecto.
const helpers = `
  window.dir = async (path, create) => {
    let d = await (await navigator.storage.getDirectory()).getDirectoryHandle('Camiones', { create: true });
    const parts = path.split('/'); const name = parts.pop();
    for (const p of parts) d = await d.getDirectoryHandle(p, { create });
    return { d, name };
  };
  window.writeFile = async (path, bytes) => { const { d, name } = await dir(path, true); const w = await (await d.getFileHandle(name, { create: true })).createWritable(); await w.write(bytes); await w.close(); };
  window.readHash = async (path) => { const { d, name } = await dir(path); const b = await (await d.getFileHandle(name)).getFile(); return [...new Uint8Array(await crypto.subtle.digest('SHA-256', await b.arrayBuffer()))].map(x => x.toString(16).padStart(2,'0')).join(''); };
  window.list = async () => { const out = []; const walk = async (d, p) => { for await (const [n, h] of d.entries()) h.kind === 'directory' ? await walk(h, p + n + '/') : out.push(p + n); }; await walk(await (await navigator.storage.getDirectory()).getDirectoryHandle('Camiones', { create: true }), ''); return out.sort(); };
  window.row = (path) => window.__3dvault.ui.engines.get(1)?.rows.find(r => (r.asset?.path ?? r.local.path) === path);
`;

async function openSync(page) {
  await page.goto(`${BASE}/sync`);
  await page.evaluate(helpers);
  await page.evaluate(async () => window.__3dvault.useFolder(await navigator.storage.getDirectory()));
  await page.check('[data-pick="1"]');
}

const waitRow = (page, path, status, timeout = 20000) =>
  page.waitForFunction(([p, s]) => window.row(p)?.status === s && !window.__3dvault.ui.running, [path, status], { timeout });

async function upload(page, path, comment, release = true) {
  await page.click(`button[data-action=upload][data-key="${path.toLowerCase()}"]`);
  await page.fill('#upload-comment', comment);
  if (!release) await page.uncheck('#upload-release').catch(() => {});
  await page.click('#upload-dialog button[value=ok]');
  await page.waitForFunction(() => /Subido como|No hay cambios|Bloqueado|409|no es la última/i.test(document.getElementById('sync-notice').textContent), null, { timeout: 30000 });
  return page.textContent('#sync-notice');
}

const step = (msg) => console.log(`✔ ${msg}`);
try {
  const a = await pc('guto', 'contraseña-larga-1');
  // Proyecto y segunda usuaria, como lo haría el admin en la web.
  await a.evaluate(async () => {
    await fetch('/api/projects', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'Camiones', description: '' }) });
    const f = new URLSearchParams({ username: 'ana', display_name: 'Ana', password: 'contraseña-larga-2' });
    await fetch('/admin/users', { method: 'POST', body: f });
  });
  await openSync(a);

  // 12 MiB pseudoaleatorios: varios trozos.
  await a.evaluate(async () => {
    const b = new Uint8Array(12 << 20);
    for (let i = 0; i < b.length; i += 65536) crypto.getRandomValues(b.subarray(i, i + 65536));
    window.v1 = b;
    await writeFile('modelos/camion.blend', b);
  });
  await a.click('[data-action=sync-now]');
  await waitRow(a, 'modelos/camion.blend', 'new');
  const n1 = await upload(a, 'modelos/camion.blend', 'Primera versión');
  assert.match(n1, /Subido como v1/);
  step(`PC A agrega un archivo nuevo: ${n1.trim()}`);

  const b = await pc('ana', 'contraseña-larga-2');
  await openSync(b);
  await waitRow(b, 'modelos/camion.blend', 'ok');
  const hashA1 = await a.evaluate(() => readHash('modelos/camion.blend'));
  assert.equal(await b.evaluate(() => readHash('modelos/camion.blend')), hashA1);
  step('PC B lo recibe idéntico al marcar el proyecto');

  // A bloquea, cambia 300 KiB en medio y sube con liberar.
  await a.click('button[data-action=lock][data-key="modelos/camion.blend"]');
  await waitRow(a, 'modelos/camion.blend', 'locked');
  await b.waitForFunction(() => window.row('modelos/camion.blend')?.asset.lock?.username === 'guto', null, { timeout: 15000 });
  step('PC B ve el bloqueo de A sin hacer nada');

  await a.evaluate(async () => {
    const b = window.v1.slice();
    crypto.getRandomValues(b.subarray(6 << 20, (6 << 20) + 65536));
    await writeFile('modelos/camion.blend', b);
  });
  await a.click('[data-action=sync-now]');
  await waitRow(a, 'modelos/camion.blend', 'changed');
  const chunkGets = [];
  b.on('request', (r) => r.url().includes('/api/chunks/') && r.method() === 'GET' && chunkGets.push(r.url()));
  const t0 = Date.now();
  const n2 = await upload(a, 'modelos/camion.blend', 'Cambio en medio');
  assert.match(n2, /Subido como v2/);
  const reused = Number(/reutilizó el (\d+)/.exec(n2)[1]);
  // Un cambio de 64 KiB toca uno o dos trozos de hasta 4 MiB en un archivo de 12 MiB.
  assert.ok(reused >= 50, `reutilizado ${reused}`);
  step(`PC A sube v2: ${n2.trim()}`);

  await b.waitForFunction(() => window.row('modelos/camion.blend')?.asset.head_version === 2 && window.row('modelos/camion.blend').status === 'ok', null, { timeout: 60000 });
  const hashA2 = await a.evaluate(() => readHash('modelos/camion.blend'));
  assert.equal(await b.evaluate(() => readHash('modelos/camion.blend')), hashA2);
  assert.ok(chunkGets.length <= 3, `B bajó ${chunkGets.length} trozos`);
  step(`PC B tiene v2 en ${((Date.now() - t0) / 1000).toFixed(1)} s, bajando solo ${chunkGets.length} trozo(s)`);

  // A bloquea de nuevo. B edita sin bloqueo y no puede subir.
  await a.click('button[data-action=lock][data-key="modelos/camion.blend"]');
  await waitRow(a, 'modelos/camion.blend', 'locked');
  await b.evaluate(async () => {
    const f = await (await (await dir('modelos/camion.blend')).d.getFileHandle('camion.blend')).getFile();
    const bytes = new Uint8Array(await f.arrayBuffer()); bytes[100] ^= 1;
    await writeFile('modelos/camion.blend', bytes);
  });
  await b.click('[data-action=sync-now]');
  await waitRow(b, 'modelos/camion.blend', 'changed');
  const n3 = await upload(b, 'modelos/camion.blend', 'Intento de B');
  assert.match(n3, /Bloqueado por/);
  step(`PC B no puede subir lo bloqueado por A: «${n3.trim()}»`);

  await b.click('button[data-action=request][data-key="modelos/camion.blend"]');
  await b.waitForFunction(() => /recibirá un aviso/.test(document.getElementById('sync-notice').textContent), null, { timeout: 15000 });
  await a.waitForFunction(() => /Ana necesita modelos\/camion\.blend/.test(document.getElementById('sync-notice').textContent), null, { timeout: 15000 });
  step('B pide el archivo y A recibe el aviso al momento');

  await a.evaluate(() => { window.realNow = Date.now; Date.now = () => window.realNow() + 2 * 86400_000; });
  await a.click('[data-action=sync-now]');
  await a.waitForFunction(() => !document.getElementById('stale-locks').hidden && /desde hace más de un día: modelos\/camion\.blend/.test(document.getElementById('stale-locks').textContent), null, { timeout: 15000 });
  await a.evaluate(() => { Date.now = window.realNow; });
  step('Un bloqueo de más de un día muestra el recordatorio');

  // Conflicto: A sube v3 mientras B tiene cambios locales. B guarda copia y baja v3.
  await a.evaluate(async () => {
    const b = window.v1.slice(); b[5000] ^= 1;
    await writeFile('modelos/camion.blend', b);
  });
  await a.click('[data-action=sync-now]');
  await waitRow(a, 'modelos/camion.blend', 'changed');
  assert.match(await upload(a, 'modelos/camion.blend', 'Tercera'), /Subido como v3/);
  await b.waitForFunction(() => window.row('modelos/camion.blend')?.asset.head_version === 3 && window.row('modelos/camion.blend').status === 'ok', null, { timeout: 60000 });
  const filesB = await b.evaluate(() => list());
  assert.equal(filesB.length, 2, filesB.join(', '));
  assert.ok(filesB.some((f) => /camion\.conflicto-ana-\d{8}-\d{4}\.blend$/.test(f)), filesB.join(', '));
  assert.equal(await b.evaluate(() => readHash('modelos/camion.blend')), await a.evaluate(() => readHash('modelos/camion.blend')));
  step(`Conflicto sin pérdida en B: ${filesB.join(', ')}`);

  // Borrar en B no se propaga: queda como "No está en tu carpeta" y se recupera.
  await b.evaluate(async () => { const { d, name } = await dir('modelos/camion.blend'); await d.removeEntry(name); });
  await b.click('[data-action=sync-now]');
  await waitRow(b, 'modelos/camion.blend', 'missing');
  await b.click('button[data-action=restore][data-key="modelos/camion.blend"]');
  await waitRow(b, 'modelos/camion.blend', 'ok');
  const assets = await a.evaluate(() => fetch('/api/projects/1/snapshot').then((r) => r.json()));
  assert.equal(assets.assets.length, 1);
  step('Un borrado local no se propaga y se recupera con Descargar');

  // Recargar conserva el estado: nada se vuelve a bajar.
  const before = chunkGets.length;
  await b.reload();
  await b.evaluate(helpers);
  await b.evaluate(async () => window.__3dvault.useFolder(await navigator.storage.getDirectory()));
  await waitRow(b, 'modelos/camion.blend', 'ok');
  assert.equal(chunkGets.length, before);
  step('Al recargar la página el estado se conserva sin descargar de nuevo');

  // Subir todo: dos archivos nuevos con un solo comentario. B los recibe.
  await a.evaluate(async () => {
    await writeFile('texturas/chapa.png', crypto.getRandomValues(new Uint8Array(40000)));
    await writeFile('modelos/pala.blend', crypto.getRandomValues(new Uint8Array(60000)));
  });
  await a.click('[data-action=sync-now]');
  await waitRow(a, 'modelos/pala.blend', 'new');
  await a.click('button[data-action=upload-all]');
  await a.fill('#upload-comment', 'Agrego pala y textura');
  await a.click('#upload-dialog button[value=ok]');
  await a.waitForFunction(() => /Listo: 2 archivo/.test(document.getElementById('sync-notice').textContent), null, { timeout: 30000 });
  await b.waitForFunction(() => window.row('texturas/chapa.png')?.status === 'ok' && window.row('modelos/pala.blend')?.status === 'ok', null, { timeout: 60000 });
  step('Subir todo agrega dos archivos con un comentario y B los recibe');

  await a.fill('#filter', 'PALA');
  await a.waitForFunction(() => [...document.querySelectorAll('#project-tables tbody tr')].length === 1, null, { timeout: 5000 });
  assert.match(await a.textContent('#project-tables tbody'), /modelos\/pala\.blend/);
  await a.fill('#filter', '');
  await a.check('#only-pending');
  await a.waitForFunction(() => /Ningún archivo coincide/.test(document.getElementById('project-tables').textContent), null, { timeout: 5000 });
  await a.uncheck('#only-pending');
  step('El buscador y "Solo lo que necesita algo de mí" filtran la tabla');

  // Lo que hace soltar una carpeta: copiarla con su estructura e ignorar respaldos.
  const copied = await a.evaluate(async () => {
    const src = await (await navigator.storage.getDirectory()).getDirectoryHandle('fuera', { create: true });
    const sub = await src.getDirectoryHandle('ruedas', { create: true });
    for (const [d, n] of [[sub, 'rueda.blend'], [sub, 'rueda.blend1'], [src, 'nota.txt']]) {
      const w = await (await d.getFileHandle(n, { create: true })).createWritable(); await w.write('x'); await w.close();
    }
    const keys = new Set();
    await window.__3dvault.copyIn(window.__3dvault.ui.engines.get(1).root, src, '', keys);
    return [...keys].sort();
  });
  assert.deepEqual(copied, ['fuera/nota.txt', 'fuera/ruedas/rueda.blend']);
  step(`Soltar una carpeta copia su estructura sin respaldos: ${copied.join(', ')}`);

  // Renombrar en el Explorador de B: se detecta y se mueve en el servidor sin perder historial.
  await b.evaluate(async () => {
    const { d } = await dir('modelos/camion.blend');
    const f = await (await d.getFileHandle('camion.blend')).getFile();
    const w = await (await d.getFileHandle('camion_797F.blend', { create: true })).createWritable();
    await f.stream().pipeTo(w);
    await d.removeEntry('camion.blend');
  });
  await b.click('[data-action=sync-now]');
  await waitRow(b, 'modelos/camion_797F.blend', 'moved');
  assert.equal(await b.evaluate(() => window.row('modelos/camion.blend')), undefined);
  await b.click('button[data-action=move][data-key="modelos/camion_797f.blend"]');
  await waitRow(b, 'modelos/camion_797F.blend', 'ok');
  const moved = await a.evaluate(() => fetch('/api/projects/1/snapshot').then((r) => r.json()).then((s) => s.assets.find((x) => x.path === 'modelos/camion_797F.blend')));
  assert.equal(moved?.head_version, 3);
  await a.waitForFunction(async () => (await list()).includes('modelos/camion_797F.blend') && !(await list()).includes('modelos/camion.blend'), null, { timeout: 60000, polling: 500 });
  step('Renombrar en el Explorador se mueve en el servidor con su historial (v3) y llega a A');

  // Subir desde la ficha del archivo, sin carpeta conectada.
  await a.goto(`${BASE}/assets/${moved.id}`);
  await a.setInputFiles('#file', { name: 'camion_797F.blend', mimeType: 'application/octet-stream', buffer: Buffer.from('versión subida desde la ficha'.repeat(1000)) });
  await a.fill('#comment', 'Desde la ficha');
  await a.click('#web-upload button[type=submit]');
  await a.waitForSelector('text=Desde la ficha', { timeout: 30000 });
  await b.waitForFunction(() => window.row('modelos/camion_797F.blend')?.asset.head_version === 4 && window.row('modelos/camion_797F.blend').status === 'ok', null, { timeout: 60000 });
  step('Subir desde la ficha crea la v4 y B la recibe');

  assert.deepEqual(errors.filter((e) => !/409|423|Failed to load resource/.test(e)), []);
  console.log('\nTodo bien.');
} finally {
  await browser.close();
  server.kill();
  rmSync(data, { recursive: true, force: true });
}
