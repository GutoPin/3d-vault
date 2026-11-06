// Núcleo de la sincronización en el navegador: corta archivos en trozos con el
// mismo FastCDC que internal/chunker y calcula SHA-256. Sin DOM, para poder
// probarlo con Node (web/sync_test.mjs).

export const MIN_SIZE = 256 << 10;
export const AVG_SIZE = 1 << 20;
export const MAX_SIZE = 4 << 20;
const BUF_SIZE = 2 * MAX_SIZE;
const MASK_SMALL = (1 << 22) - 1;
const MASK_LARGE = (1 << 18) - 1;

// ponytail: solo los 32 bits bajos de gearTable de Go. Las máscaras usan los 22
// bits bajos y en (fp<<1)+g los acarreos solo suben, así que los cortes son los
// mismos que con 64 bits.
const GEAR = new Int32Array([
  0x032474b3, 0xf5924e15, 0x2a9eb669, 0xf107a38c, 0x6f0f57b9, 0x95ec79c7, 0xad42ac0a, 0xa63cec15,
  0x24a4451d, 0x59683605, 0x3fb11ca1, 0xa6ae15b2, 0xb5a89eac, 0x32f0038e, 0x365c0967, 0xa222fc4a,
  0x9a1e577a, 0x1fdbf750, 0x57dfcdcc, 0xe30b4281, 0xc77c9056, 0xc4612a62, 0xf8943eb5, 0x8a8ec8d9,
  0x9fac8074, 0xe0d318df, 0xfd0d8e3a, 0x2669dd25, 0xf4b10285, 0x4b9b6410, 0x57d55810, 0x6ff11b46,
  0x0dc3003e, 0x89b4009a, 0xb529f22a, 0xa1f77e56, 0x802ee65d, 0x718ff813, 0xf30db464, 0xb80b72d1,
  0x9a6b31db, 0x1d69ea18, 0x1aa7b8de, 0x054edd20, 0xeb8b1dbf, 0x4f196a56, 0xec7b6b17, 0xa894a6b7,
  0x7b0d6f91, 0x9de22bc8, 0xef93e0fe, 0xcaed603c, 0x7f63a45e, 0x2bb569d1, 0x6d356b4b, 0x3f3e7a57,
  0xde3596f1, 0x7cd23c1a, 0x78b3f71f, 0xfb93a3f9, 0xa7c9842f, 0x36e433ae, 0xf58be306, 0xaa233fc6,
  0x0b7638e8, 0xbd2406d1, 0x74d0f133, 0x4ccb4be7, 0x383594c8, 0xb7af4429, 0x53799dbb, 0x0a494cbc,
  0xa4cb0d1a, 0x902415a1, 0x8493a9a4, 0xcb448631, 0xb46824b8, 0x0bc94f84, 0xe6b55bb3, 0xf9f6f297,
  0x807b3758, 0xfbca8b4d, 0x38b0c730, 0x3d623316, 0x9242d5e8, 0x29858e30, 0x2aa67386, 0xbfc420d4,
  0xa895f7e1, 0x5f6911ce, 0x95cfb7ce, 0x344c5c86, 0x1839a861, 0x9ed3ed56, 0x0beda160, 0x2fe7f4e2,
  0x5cf6e151, 0xf4af05c8, 0xeb67cc35, 0x0323b28d, 0x5bb0ac2a, 0xbbee77ae, 0x6b3d0a9c, 0xe31dbadb,
  0x226fb986, 0xbfebabfa, 0x6595bc2d, 0x0aa46b8b, 0x939421ee, 0x9b1415da, 0x4fe73631, 0x1fbe3356,
  0xa06c622f, 0x5d272579, 0x50d8ec0d, 0x19580a3b, 0x3a185e42, 0x6868732f, 0x629203a2, 0xdefe56d9,
  0xc78ff9ed, 0x4a499ca7, 0xf9682c70, 0xce993eff, 0x62e0b79a, 0x5712ffe8, 0xbabbd12a, 0x4315954f,
  0x938ab51a, 0x99907cee, 0x0a3523c8, 0xae518332, 0xb23db7b2, 0x6f672a9d, 0xc4903810, 0xbb5f1c4f,
  0xfceca7d0, 0xa4572374, 0x9ee9513d, 0x3c1908c9, 0x76a6339d, 0xfbe9c679, 0x9726f938, 0xdb27c959,
  0x8e4a8c9a, 0x0dfaa499, 0x2ef4ac8b, 0x586259c6, 0x6cb9617d, 0xde2284d2, 0xb3729865, 0xf03e2633,
  0x8f0855a4, 0xacf3a767, 0xbfd6d14a, 0x343fe757, 0x26352f85, 0xb5b2bd76, 0x9fd4a3fd, 0xc36f3460,
  0x88cf4d44, 0xf5f25899, 0x7ce3b1e6, 0x561b3caa, 0xee9f0af7, 0xac49a102, 0x23b332e7, 0xbf94ed1e,
  0xd3046fa0, 0x6b9c2fb9, 0xabebba8e, 0x2747bd58, 0x06c8ec07, 0x828d0628, 0x89ca563d, 0x074d516d,
  0xb423ee99, 0x6f3120a8, 0xf88860b8, 0xf5118e81, 0x7d1bd268, 0xf5e5e88b, 0x54cd1dda, 0x5d7640f4,
  0x920833a2, 0xcebcb044, 0xfa93f7bb, 0x4538f3e6, 0xc486d0b4, 0xb28851d3, 0xa9a956b5, 0x137987e5,
  0xe8ceac2c, 0x2f185cae, 0x735c4c1c, 0xc0570bc7, 0x8f64cbf6, 0x1705dcb9, 0xc950e9f6, 0xed74de02,
  0x705c7fa5, 0x725a6672, 0x7beb3bc6, 0x1cee565b, 0xb0c33e64, 0x3f21fe28, 0x1643e514, 0x3eb01fa4,
  0x12306ed0, 0x38fc9686, 0x3f8507bb, 0xd3fbe4d3, 0xffdcb6f6, 0x746ffde9, 0x2d555266, 0x5ae1406e,
  0x2b5584b3, 0x15d45a60, 0x47d9bc83, 0x1264b23f, 0x7e589ade, 0x54c7815c, 0xb8d67d47, 0xfea0a81c,
  0x10d25bfd, 0xcf9b6ab3, 0x8d87501d, 0x8dee38dc, 0x0ce1c7d7, 0xf343d8f9, 0x9f03398d, 0x730281ed,
  0x750a8e59, 0x71adf736, 0xee2b7def, 0xc937a02a, 0x9fc8c4fc, 0xc7a03f43, 0xd1353ef9, 0x8413a867,
  0xbeda8be8, 0x719842c0, 0xb6d40c1c, 0x2f9738b1, 0x6511929a, 0x8bb226ea, 0x253a3c18, 0x91284c9c,
  0xa3af373d, 0x9a866ffc, 0xd0662799, 0x729e9830, 0x18381b58, 0xa31dc5ff, 0x2f8701f9, 0x471c65fe
]);

// cutPoint devuelve la longitud del trozo que empieza en b[start].
function cutPoint(b, start, end) {
  const len = end - start;
  if (len <= MIN_SIZE) return len;
  const n = Math.min(len, MAX_SIZE);
  let fp = 0;
  let i = MIN_SIZE;
  for (const lim = Math.min(n, AVG_SIZE); i < lim; i++) {
    fp = ((fp << 1) + GEAR[b[start + i]]) | 0;
    if ((fp & MASK_SMALL) === 0) return i + 1;
  }
  for (; i < n; i++) {
    fp = ((fp << 1) + GEAR[b[start + i]]) | 0;
    if ((fp & MASK_LARGE) === 0) return i + 1;
  }
  return n;
}

// chunks recorre un Blob (o File) y devuelve {offset, data} por trozo. Mantiene
// al menos MAX_SIZE bytes en el búfer, igual que Go, para que los cortes no
// dependan de cómo se lee.
export async function* chunks(blob) {
  let buf = new Uint8Array(0);
  let start = 0;
  let pos = 0;
  let offset = 0;
  for (;;) {
    if (buf.length - start < MAX_SIZE && pos < blob.size) {
      const next = new Uint8Array(await blob.slice(pos, pos + BUF_SIZE).arrayBuffer());
      pos += next.length;
      const merged = new Uint8Array(buf.length - start + next.length);
      merged.set(buf.subarray(start));
      merged.set(next, buf.length - start);
      buf = merged;
      start = 0;
      continue;
    }
    if (start === buf.length) return;
    const n = cutPoint(buf, start, buf.length);
    yield { offset, data: buf.subarray(start, start + n) };
    start += n;
    offset += n;
  }
}

export function toHex(bytes) {
  let s = '';
  for (const b of new Uint8Array(bytes)) s += b.toString(16).padStart(2, '0');
  return s;
}

// hashHex usa el SHA-256 nativo: sirve para trozos, que caben en memoria.
export async function hashHex(data) {
  return toHex(await crypto.subtle.digest('SHA-256', data));
}

const K = new Int32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

// Sha256 es incremental: crypto.subtle no lo es y un .blend no cabe en memoria.
export class Sha256 {
  constructor() {
    this.h = new Int32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    this.w = new Int32Array(64);
    this.block = new Uint8Array(64);
    this.fill = 0;
    this.total = 0;
  }

  update(data) {
    let i = 0;
    this.total += data.length;
    if (this.fill > 0) {
      const take = Math.min(64 - this.fill, data.length);
      this.block.set(data.subarray(0, take), this.fill);
      this.fill += take;
      i = take;
      if (this.fill < 64) return this;
      this.compress(this.block, 0);
      this.fill = 0;
    }
    for (; i + 64 <= data.length; i += 64) this.compress(data, i);
    if (i < data.length) {
      this.block.set(data.subarray(i));
      this.fill = data.length - i;
    }
    return this;
  }

  compress(p, o) {
    const w = this.w;
    for (let t = 0; t < 16; t++, o += 4) w[t] = (p[o] << 24) | (p[o + 1] << 16) | (p[o + 2] << 8) | p[o + 3];
    for (let t = 16; t < 64; t++) {
      const a = w[t - 15], b = w[t - 2];
      const s0 = ((a >>> 7) | (a << 25)) ^ ((a >>> 18) | (a << 14)) ^ (a >>> 3);
      const s1 = ((b >>> 17) | (b << 15)) ^ ((b >>> 19) | (b << 13)) ^ (b >>> 10);
      w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0;
    }
    const h = this.h;
    let a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], k = h[7];
    for (let t = 0; t < 64; t++) {
      const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
      const t1 = (k + S1 + ((e & f) ^ (~e & g)) + K[t] + w[t]) | 0;
      const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
      const t2 = (S0 + ((a & b) ^ (a & c) ^ (b & c))) | 0;
      k = g; g = f; f = e; e = (d + t1) | 0;
      d = c; c = b; b = a; a = (t1 + t2) | 0;
    }
    h[0] += a; h[1] += b; h[2] += c; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += k;
  }

  hex() {
    const bits = this.total * 8;
    const pad = new Uint8Array(((this.fill < 56 ? 56 : 120) - this.fill) + 8);
    pad[0] = 0x80;
    const view = new DataView(pad.buffer);
    view.setUint32(pad.length - 8, Math.floor(bits / 0x100000000));
    view.setUint32(pad.length - 4, bits >>> 0);
    this.update(pad);
    const out = new Uint8Array(32);
    const v = new DataView(out.buffer);
    for (let i = 0; i < 8; i++) v.setInt32(i * 4, this.h[i]);
    return toHex(out);
  }
}

// scan corta un archivo y devuelve su manifiesto y dónde está cada trozo
// distinto, como client.ScanFile. onProgress recibe los bytes leídos.
export async function scan(blob, onProgress = () => {}) {
  const file = new Sha256();
  const manifest = { file_sha256: '', chunks: [] };
  const offsets = new Map();
  for await (const c of chunks(blob)) {
    file.update(c.data);
    const sha256 = await hashHex(c.data);
    manifest.chunks.push({ sha256, size: c.data.length });
    if (!offsets.has(sha256)) offsets.set(sha256, c.offset);
    onProgress(c.offset + c.data.length);
  }
  manifest.file_sha256 = file.hex();
  return { manifest, offsets };
}
