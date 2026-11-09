"""Núcleo del complemento: protocolo de 3D-Vault sin bpy, para poder probarlo.

Corta archivos con el mismo FastCDC que internal/chunker (Go) y core.js, habla
con la API del servidor y sube o baja solo los trozos que faltan.
"""

import base64
import hashlib
import json
import os
import re
import shutil
import ssl
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor

import numpy as np

MIN_SIZE = 256 << 10
AVG_SIZE = 1 << 20
MAX_SIZE = 4 << 20
MASK_SMALL = (1 << 22) - 1
MASK_LARGE = (1 << 18) - 1
READ_BLOCK = 16 << 20
PARALLEL = 4
MISSING_BATCH = 2000
# Las máscaras miran 22 bits, así que el corte depende de los últimos 22 bytes.
WINDOW = 22

# Los 32 bits bajos de gearTable de Go: bastan, ver core.js.
GEAR = [
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
]
GEAR_NP = np.array(GEAR, dtype=np.uint32)


class VaultError(Exception):
    def __init__(self, message, status=0, body=None):
        super().__init__(message)
        self.status = status
        self.body = body or {}


# ---------- trozos ----------

def _window_hash(data):
    """Para cada posición j, suma de GEAR[data[j-k]] << k con k < 32 (mod 2^32).

    Por duplicación: S_2m[j] = S_m[j] + S_m[j-m] << m. Sus 22 bits bajos son los
    del hash rodante de FastCDC una vez que este lleva 22 bytes o más.
    """
    s = GEAR_NP[data]
    m = 1
    while m < 32:
        # El lado derecho se calcula entero antes de sumar: no pisa lo que lee.
        s[m:] += s[:-m] << np.uint32(m)
        m *= 2
    return s


def _cut_point(buf, win):
    n_all = len(buf)
    if n_all <= MIN_SIZE:
        return n_all
    n = min(n_all, MAX_SIZE)
    # Los primeros bytes tras MIN_SIZE tienen menos historia que la ventana:
    # se calculan uno a uno, igual que en Go.
    fp = 0
    direct_end = min(n, MIN_SIZE + WINDOW)
    for i in range(MIN_SIZE, direct_end):
        fp = ((fp << 1) + GEAR[buf[i]]) & 0xFFFFFFFF
        if fp & (MASK_SMALL if i < AVG_SIZE else MASK_LARGE) == 0:
            return i + 1
    lo = direct_end
    hi = min(n, AVG_SIZE)
    if lo < hi:
        hits = np.flatnonzero((win[lo:hi] & MASK_SMALL) == 0)
        if hits.size:
            return lo + int(hits[0]) + 1
    lo = max(lo, AVG_SIZE)
    if lo < n:
        hits = np.flatnonzero((win[lo:n] & MASK_LARGE) == 0)
        if hits.size:
            return lo + int(hits[0]) + 1
    return n


def chunks(f):
    """Recorre un archivo abierto en binario y devuelve (offset, bytes) por trozo."""
    buf = np.empty(0, dtype=np.uint8)
    win = np.empty(0, dtype=np.uint32)
    offset = 0
    eof = False
    while True:
        if len(buf) < MAX_SIZE and not eof:
            raw = f.read(READ_BLOCK)
            if not raw:
                eof = True
                continue
            new = np.frombuffer(raw, dtype=np.uint8)
            tail = buf[-WINDOW:]
            win_new = _window_hash(np.concatenate([tail, new]))[len(tail):]
            buf = np.concatenate([buf, new])
            win = np.concatenate([win, win_new])
            continue
        if len(buf) == 0:
            return
        n = _cut_point(buf, win)
        yield offset, buf[:n].tobytes()
        buf, win = buf[n:], win[n:]
        offset += n


def scan(path, progress=None):
    """Manifiesto de un archivo y posición de cada trozo distinto."""
    file_hash = hashlib.sha256()
    manifest = {"file_sha256": "", "chunks": []}
    offsets = {}
    with open(path, "rb") as f:
        for off, data in chunks(f):
            file_hash.update(data)
            h = hashlib.sha256(data).hexdigest()
            manifest["chunks"].append({"sha256": h, "size": len(data)})
            offsets.setdefault(h, off)
            if progress:
                progress("Analizando", off + len(data))
    manifest["file_sha256"] = file_hash.hexdigest()
    return manifest, offsets


ZSTD_MAGIC = b"\x28\xb5\x2f\xfd"


def is_compressed(path):
    """Si el .blend se guardó con Comprimir. Medido en Blender 5.2: comprimido,
    una edición de geometría cambia casi todo el archivo; sin comprimir, ~1 %."""
    with open(path, "rb") as f:
        return f.read(4) == ZSTD_MAGIC


def file_sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(8 << 20), b""):
            h.update(block)
    return h.hexdigest()


def manifest_version(text):
    """La versión de un blender_manifest.toml."""
    m = re.search(r'(?m)^version\s*=\s*"([^"]+)"', text)
    return m.group(1) if m else ""


def own_version():
    with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "blender_manifest.toml"), encoding="utf-8") as f:
        return manifest_version(f.read())


# ---------- conexión ----------

CODE_PREFIX = "3DV1:"


def parse_code(code):
    """Lee el código de conexión que genera Mi carpeta."""
    code = code.strip()
    if not code.startswith(CODE_PREFIX):
        raise VaultError("El código no es de 3D-Vault. Cópialo de nuevo desde Mi carpeta.")
    try:
        raw = code[len(CODE_PREFIX):]
        data = json.loads(base64.urlsafe_b64decode(raw + "=" * (-len(raw) % 4)))
        return {"server": data["s"].rstrip("/"), "token": data["t"], "client_id": data["c"], "client_name": data["n"]}
    except (ValueError, KeyError) as err:
        raise VaultError("El código está incompleto. Cópialo de nuevo desde Mi carpeta.") from err


def _ssl_context():
    try:
        import certifi  # Blender lo incluye

        return ssl.create_default_context(cafile=certifi.where())
    except ImportError:
        return ssl.create_default_context()


class Api:
    def __init__(self, server, token, client_id, client_name, timeout=60):
        self.server = server.rstrip("/")
        self.headers = {
            "Authorization": "Bearer " + token,
            "X-Client-Id": client_id,
            "X-Client-Name": urllib.parse.quote(client_name),
            "User-Agent": "3D-Vault Blender",
        }
        self.timeout = timeout
        self.ctx = _ssl_context() if self.server.startswith("https:") else None

    @classmethod
    def from_code(cls, code):
        c = parse_code(code)
        return cls(c["server"], c["token"], c["client_id"], c["client_name"])

    def request(self, method, path, body=None, raw=False):
        headers = dict(self.headers)
        data = None
        if isinstance(body, (bytes, bytearray)):
            data = bytes(body)
            headers["Content-Type"] = "application/octet-stream"
        elif body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.server + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout, context=self.ctx) as res:
                payload = res.read()
        except urllib.error.HTTPError as err:
            try:
                info = json.loads(err.read() or b"{}")
            except ValueError:
                info = {}
            if err.code == 401:
                raise VaultError("La conexión ya no es válida. Genera un código nuevo en Mi carpeta.", 401, info) from err
            raise VaultError(info.get("message") or f"Error {err.code} del servidor.", err.code, info) from err
        except (urllib.error.URLError, TimeoutError, OSError) as err:
            raise VaultError(f"No se pudo conectar con {self.server}: {getattr(err, 'reason', err)}") from err
        if raw:
            return payload
        return json.loads(payload) if payload else None


# ---------- archivos del proyecto ----------

def key_of(path):
    """Aproxima path_key del servidor: NFC y sin mayúsculas."""
    return unicodedata.normalize("NFC", path).casefold()


def safe_name(name):
    """El nombre de la carpeta de un proyecto, igual que safeName en app.js."""
    out = "".join("_" if c in '<>:"/\\|?*' or ord(c) < 32 else c for c in name).rstrip(". ")
    return out or "proyecto"


def locate(filepath, projects):
    """Proyecto y ruta dentro de él de un archivo de la carpeta sincronizada.

    El proyecto es la carpeta antepasada más alta que se llama como uno.
    """
    parts = os.path.normpath(os.path.abspath(filepath)).split(os.sep)
    by_folder = {key_of(safe_name(p["name"])): p for p in projects if not p.get("archived")}
    for i, part in enumerate(parts[:-1]):
        project = by_folder.get(key_of(part))
        if project:
            return project, "/".join(parts[i + 1:])
    return None, None


def project_root_of(filepath, asset_path):
    """La carpeta del proyecto en esta PC: la ruta del archivo sin su ruta en el proyecto."""
    full = os.path.normpath(os.path.abspath(filepath))
    rel = os.path.normpath(asset_path.replace("/", os.sep))
    return full[: len(full) - len(rel)].rstrip(os.sep)


def inside(path, folder):
    """Si path está dentro de folder, sin distinguir mayúsculas en Windows."""
    path = os.path.normcase(os.path.normpath(os.path.abspath(path)))
    folder = os.path.normcase(os.path.normpath(os.path.abspath(folder)))
    return path.startswith(folder.rstrip(os.sep) + os.sep)


def link_problem(raw, absolute, sync_root):
    """Por qué un archivo enlazado no se verá en otra PC, o None si está bien.

    raw es la ruta tal como la guarda Blender ("//" si es relativa) y absolute,
    la misma ya resuelta. sync_root es la carpeta de 3D-Vault de esta PC, que
    contiene una carpeta por proyecto: un enlace a otro proyecto también vale.
    """
    if not inside(absolute, sync_root):
        return "outside"
    if not os.path.isfile(absolute):
        return "missing"
    if not raw.startswith("//"):
        return "absolute"
    return None


def conflict_name(path, username):
    folder, name = os.path.split(path)
    stem, ext = os.path.splitext(name)
    return os.path.join(folder, f"{stem}.conflicto-{username}-{time.strftime('%Y%m%d-%H%M')}{ext}")


class Vault:
    """Operaciones sobre un archivo local, con la misma identidad que Mi carpeta."""

    def __init__(self, api):
        self.api = api
        self._me = None
        self._sha = {}  # ruta → (tamaño, fecha, sha): un .blend grande no se recalcula sin cambios

    def me(self):
        if self._me is None:
            self._me = self.api.request("GET", "/api/me")
        return self._me

    def local_sha(self, path):
        st = os.stat(path)
        hit = self._sha.get(path)
        if hit and hit[:2] == (st.st_size, st.st_mtime_ns):
            return hit[2]
        sha = file_sha256(path)
        self._sha[path] = (st.st_size, st.st_mtime_ns, sha)
        return sha

    def status(self, path):
        """Estado de un archivo frente al servidor."""
        projects = self.api.request("GET", "/api/projects")["projects"]
        project, asset_path = locate(path, projects)
        if not project:
            names = ", ".join(safe_name(p["name"]) for p in projects) or "ninguno"
            raise VaultError(f"Este archivo no está en la carpeta de un proyecto. Proyectos: {names}.")
        snap = self.api.request("GET", f"/api/projects/{project['id']}/snapshot")
        key = key_of(asset_path)
        asset = next((a for a in snap["assets"] if key_of(a["path"]) == key), None)
        st = {"project": project, "path": asset_path, "asset": asset, "lock": None, "local_version": None,
              "compressed": path.lower().endswith(".blend") and is_compressed(path)}
        if not asset:
            st["state"] = "new"
            return st
        me = self.me()
        lock = asset.get("lock")
        st["lock"] = lock
        st["locked_by_me"] = bool(lock and lock["user_id"] == me["id"] and lock["client_id"] == self.api.headers["X-Client-Id"])
        st["locked_by_me_elsewhere"] = bool(lock and lock["user_id"] == me["id"] and not st["locked_by_me"])
        sha = self.local_sha(path)
        versions = self.api.request("GET", f"/api/assets/{asset['id']}/versions")["versions"]
        st["versions"] = sorted(versions, key=lambda v: -v["number"])
        if sha == asset["file_sha256"]:
            st["state"] = "current"
            st["local_version"] = asset["head_version"]
            st["local_version_id"] = asset["head_version_id"]
            return st
        older = next((v for v in versions if v["file_sha256"] == sha), None)
        if older:
            st["state"] = "outdated"
            st["local_version"] = older["number"]
            st["local_version_id"] = older["id"]
        else:
            st["state"] = "modified"
        return st

    def links(self, asset_id):
        """Lo que usa un archivo y quién lo usa, según el servidor."""
        return self.api.request("GET", f"/api/assets/{asset_id}/links")

    def libraries(self, st, filepath, libs):
        """Estado de los .blend enlazados por el archivo abierto.

        libs es una lista de (nombre, ruta como la guarda Blender, ruta absoluta).
        Cada resultado lleva "problem" (ver link_problem) y, si el archivo está
        en una carpeta de 3D-Vault, "status" con su estado frente al servidor.
        """
        sync_root = os.path.dirname(project_root_of(filepath, st["path"]))
        out = []
        for name, raw, absolute in libs:
            item = {"name": name, "raw": raw, "path": absolute, "problem": link_problem(raw, absolute, sync_root), "status": None, "error": None}
            if item["problem"] in (None, "absolute"):
                try:
                    item["status"] = self.status(absolute)
                except VaultError as err:
                    item["error"] = str(err)
            out.append(item)
        return out

    def addon_update(self):
        """La versión del complemento que entrega el servidor, si es otra."""
        server = self.api.request("GET", "/blender/version", raw=True).decode().strip()
        return server if server and server != own_version() else None

    def request_unlock(self, st):
        return self.api.request("POST", f"/api/assets/{st['asset']['id']}/request-unlock")

    def lock(self, st, takeover=False):
        return self.api.request("POST", f"/api/assets/{st['asset']['id']}/lock", {"takeover": takeover})

    def unlock(self, st):
        return self.api.request("DELETE", f"/api/assets/{st['asset']['id']}/lock")

    def upload(self, path, st, comment, release, progress=None):
        """Sube el archivo enviando solo los trozos que faltan."""
        if len(comment.strip()) < 3:
            raise VaultError("Escribe un comentario de al menos 3 letras.")
        asset = st["asset"]
        if asset and not st.get("locked_by_me"):
            raise VaultError("Bloquea el archivo antes de subirlo, para no reemplazar el trabajo de otro.")
        size = os.path.getsize(path)
        manifest, offsets = scan(path, lambda what, n: progress and progress(what, n, size))
        sizes = {c["sha256"]: c["size"] for c in manifest["chunks"]}
        unique = list(offsets)
        missing = []
        for i in range(0, len(unique), MISSING_BATCH):
            missing += self.api.request("POST", "/api/chunks/missing", {"hashes": unique[i:i + MISSING_BATCH]})["missing"]

        total = sum(sizes[h] for h in missing)
        sent = 0

        def put(h):
            with open(path, "rb") as f:
                f.seek(offsets[h])
                data = f.read(sizes[h])
            self.api.request("PUT", f"/api/chunks/{h}", data, raw=True)
            return len(data)

        with ThreadPoolExecutor(PARALLEL) as pool:
            for n in pool.map(put, missing):
                sent += n
                if progress:
                    progress("Subiendo", sent, total)

        if asset:
            # Con el bloqueo nadie pudo subir encima, así que la base es la última.
            result = self.api.request("POST", f"/api/assets/{asset['id']}/versions", {
                "base_version": asset["head_version"], "manifest": manifest, "size_bytes": size,
                "comment": comment.strip(), "release_lock": release,
            })
        else:
            result = self.api.request("POST", "/api/assets", {
                "project_id": st["project"]["id"], "path": st["path"], "manifest": manifest, "comment": comment.strip(),
            })
        self._sha[path] = (os.stat(path).st_size, os.stat(path).st_mtime_ns, manifest["file_sha256"])
        reused = 100 * (1 - total / size) if size else 100
        return {"version": result["version"]["number"], "no_changes": result["no_changes"], "reused": reused}

    def update(self, path, st, progress=None):
        """Baja la última versión en lugar del archivo local, copiando de él los
        trozos que no cambiaron. Si el local tiene cambios que el servidor no
        tiene, antes lo guarda como *.conflicto-*. Devuelve la ruta de esa copia."""
        asset = st["asset"]
        new = self.api.request("GET", f"/api/versions/{asset['head_version_id']}/manifest")
        have = {}
        copy = None
        if st["state"] == "outdated":
            old = self.api.request("GET", f"/api/versions/{st['local_version_id']}/manifest")
            off = 0
            for c in old["chunks"]:
                have.setdefault(c["sha256"], off)
                off += c["size"]
        elif st["state"] == "modified":
            copy = conflict_name(path, self.me()["username"])
            shutil.copy2(path, copy)

        self._write(new, have, path, path, progress)
        return copy

    def download_version(self, path, st, version, dest, progress=None):
        """Baja una versión cualquiera a dest, copiando del archivo local los
        trozos que ya tiene, para abrirla aparte sin tocar el archivo de trabajo."""
        new = self.api.request("GET", f"/api/versions/{version['id']}/manifest")
        have = {}
        if st.get("local_version_id"):
            old = self.api.request("GET", f"/api/versions/{st['local_version_id']}/manifest")
            off = 0
            for c in old["chunks"]:
                have.setdefault(c["sha256"], off)
                off += c["size"]
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        self._write(new, have, path, dest, progress)
        return dest

    def _write(self, new, have, base_path, dest, progress):
        """Escribe el archivo de un manifiesto en dest, de forma atómica,
        tomando de base_path los trozos de have y el resto del servidor."""
        total = sum(c["size"] for c in new["chunks"])
        tmp = dest + ".descarga.tmp"
        file_hash = hashlib.sha256()
        done = 0
        try:
            with open(base_path, "rb") as base, open(tmp, "wb") as out:
                def get(c):
                    off = have.get(c["sha256"])
                    if off is not None:
                        base.seek(off)
                        return base.read(c["size"])
                    data = self.api.request("GET", f"/api/chunks/{c['sha256']}", raw=True)
                    if hashlib.sha256(data).hexdigest() != c["sha256"]:
                        raise VaultError("Un trozo llegó dañado. Inténtalo de nuevo.")
                    return data

                for c in new["chunks"]:
                    data = get(c)
                    file_hash.update(data)
                    out.write(data)
                    done += len(data)
                    if progress:
                        progress("Descargando", done, total)
                out.flush()
                os.fsync(out.fileno())
            if file_hash.hexdigest() != new["file_sha256"]:
                raise VaultError("El archivo descargado no coincide con el del servidor.")
            os.replace(tmp, dest)
        finally:
            if os.path.exists(tmp):
                os.remove(tmp)
