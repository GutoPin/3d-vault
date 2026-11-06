// Pruebas del núcleo de sincronización web. Se ejecuta con: node --test web/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { chunks, scan, Sha256 } from './server/static/sync/core.js';

// Misma huella que TestGoldenCutsWeb en internal/chunker.
const GOLDEN = '914a77e6310914426999f20244aa1536d24dcba473db4056e9be2b321b537ff9';

function hashData(size) {
  const out = Buffer.alloc(size + 32);
  const n = Buffer.alloc(4);
  for (let i = 0, o = 0; o < size; i++, o += 32) {
    n.writeUInt32LE(i);
    createHash('sha256').update(n).digest().copy(out, o);
  }
  return out.subarray(0, size);
}

test('corta igual que internal/chunker', async () => {
  const fp = createHash('sha256');
  for await (const c of chunks(new Blob([hashData(40 << 20)]))) {
    fp.update(`${c.offset}:${c.data.length}:${createHash('sha256').update(c.data).digest('hex')}\n`);
  }
  assert.equal(fp.digest('hex'), GOLDEN);
});

test('Sha256 coincide con node:crypto en cualquier troceo', () => {
  for (const size of [0, 1, 55, 56, 63, 64, 65, 119, 120, 1000, 100003]) {
    const data = randomBytes(size);
    const h = new Sha256();
    for (let i = 0; i < size; i += 37) h.update(data.subarray(i, i + 37));
    assert.equal(h.hex(), createHash('sha256').update(data).digest('hex'), `tamaño ${size}`);
  }
});

test('scan da el hash del archivo y trozos que lo reconstruyen', async () => {
  const data = randomBytes(9 << 20);
  const { manifest, offsets } = await scan(new Blob([data]));
  assert.equal(manifest.file_sha256, createHash('sha256').update(data).digest('hex'));
  assert.equal(manifest.chunks.reduce((s, c) => s + c.size, 0), data.length);
  for (const c of manifest.chunks) {
    const o = offsets.get(c.sha256);
    assert.equal(createHash('sha256').update(data.subarray(o, o + c.size)).digest('hex'), c.sha256);
  }
});
