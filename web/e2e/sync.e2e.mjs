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
  assert.ok(reused >= 70, `reutilizado ${reused}`);
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

  assert.deepEqual(errors.filter((e) => !/409|423|Failed to load resource/.test(e)), []);
  console.log('\nTodo bien.');
} finally {
  await browser.close();
  server.kill();
  rmSync(data, { recursive: true, force: true });
}
