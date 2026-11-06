"""Prueba del complemento dentro de Blender (módulo bpy) contra un servidor real.

Uso: make test-blender-e2e. Necesita bin/3dvault-server y un Python con el
módulo bpy de PyPI (pip install bpy), que trae numpy.
"""

import base64
import glob
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import types
import urllib.parse
import urllib.request

import bpy

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import three_d_vault as addon  # noqa: E402
from three_d_vault import vault  # noqa: E402

REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
BIN = os.path.join(REPO, "bin", "3dvault-server")
PORT = 18090
BASE = f"http://127.0.0.1:{PORT}"


VALID_ICONS = {i.identifier for i in bpy.types.UILayout.bl_rna.functions["label"].parameters["icon"].enum_items}


class FakeLayout:
    """Ejecuta los draw del panel sin interfaz: valida íconos y operadores."""

    def __init__(self, seen):
        self.seen = seen
        self.enabled = True
        self.scale_y = 1

    def _icon(self, icon):
        assert icon in VALID_ICONS, f"ícono inválido {icon}"

    def label(self, text="", icon="NONE"):
        self._icon(icon)
        self.seen.append(text)

    def operator(self, idname, text="", icon="NONE"):
        self._icon(icon)
        group, name = idname.split(".")
        assert hasattr(getattr(bpy.ops, group), name) and getattr(getattr(bpy.ops, group), name).poll is not None, idname
        getattr(getattr(bpy.ops, group), name).get_rna_type()  # falla si no está registrado
        self.seen.append(f"[{text or idname}]")
        return types.SimpleNamespace()

    def prop(self, *_args, **_kw):
        pass

    def box(self, **_kw):
        return self

    row = column = box


def draw_panels():
    seen = []
    addon.THREEDVAULT_PT_panel.draw(types.SimpleNamespace(layout=FakeLayout(seen)), bpy.context)
    if addon.THREEDVAULT_PT_versions.poll(bpy.context):
        addon.THREEDVAULT_PT_versions.draw(types.SimpleNamespace(layout=FakeLayout(seen)), bpy.context)
    return " ".join(seen)


def step(msg):
    print("✔", msg, flush=True)


def call(method, path, body=None, token=None, cookie=None):
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json", **({"Authorization": "Bearer " + token} if token else {})})
    with urllib.request.urlopen(req) as res:
        raw = res.read()
        return json.loads(raw) if raw else None


def code_for(token, client_id, name):
    raw = json.dumps({"s": BASE, "t": token, "c": client_id, "n": name}).encode()
    return "3DV1:" + base64.urlsafe_b64encode(raw).decode().rstrip("=")


def save_scene(path, objects):
    bpy.ops.wm.read_factory_settings(use_empty=True)
    bpy.context.preferences.system.use_online_access = True  # lo restablece lo anterior
    for i in range(objects):
        bpy.ops.mesh.primitive_uv_sphere_add(segments=64, ring_count=32, location=(i * 3, 0, 0))
    bpy.ops.wm.save_as_mainfile(filepath=path)


def main():
    data = tempfile.mkdtemp(prefix="3dvault-blender-")
    env = {**os.environ, "SECRET_KEY": "x" * 48, "DATA_DIR": os.path.join(data, "srv"), "LISTEN_ADDR": f"127.0.0.1:{PORT}", "INSECURE_COOKIES": "true"}
    subprocess.run([BIN, "create-admin", "--username", "guto", "--name", "Guto"], env=env, input=b"clave-larga-123\n", check=True, capture_output=True)
    server = subprocess.Popen([BIN, "serve"], env=env, stderr=subprocess.DEVNULL)
    try:
        for _ in range(50):
            try:
                urllib.request.urlopen(BASE + "/healthz")
                break
            except OSError:
                time.sleep(0.1)
        run(data)
        print("\nTodo bien.")
    finally:
        server.terminate()
        shutil.rmtree(data, ignore_errors=True)


def run(data):
    guto = call("POST", "/api/login", {"username": "guto", "password": "clave-larga-123", "token_name": "prueba"})["token"]
    call("POST", "/api/projects", {"name": "Camiones", "description": ""}, token=guto)
    # Ana: otra usuaria en otra PC.
    req = urllib.request.Request(BASE + "/api/login", data=json.dumps({"username": "guto", "password": "clave-larga-123"}).encode(), headers={"Content-Type": "application/json"})
    cookie = urllib.request.urlopen(req).headers["Set-Cookie"].split(";")[0]
    form = urllib.parse.urlencode({"username": "ana", "display_name": "Ana", "password": "clave-larga-456"}).encode()
    urllib.request.urlopen(urllib.request.Request(BASE + "/admin/users", data=form, headers={"Cookie": cookie}))
    ana = call("POST", "/api/login", {"username": "ana", "password": "clave-larga-456", "token_name": "prueba"})["token"]

    addon.register()
    bpy.context.preferences.system.use_online_access = False
    addon.prefs = lambda: types.SimpleNamespace(code="x")
    bpy.ops.wm.save_as_mainfile(filepath=os.path.join(data, "sin-red.blend"))
    addon.refresh()
    assert "acceso en línea" in addon.STATE["error"], addon.STATE
    step("Sin permiso de red, el complemento no se conecta y dice dónde activarlo")
    bpy.context.preferences.system.use_online_access = True

    code = code_for(guto, "pc-guto", "PC de Guto")
    addon.prefs = lambda: types.SimpleNamespace(code=code)
    other = vault.Vault(vault.Api(BASE, ana, "pc-ana", "PC de Ana"))
    st = addon.STATE

    root = os.path.join(data, "3D-Vault", "Camiones", "modelos")
    os.makedirs(root)
    path = os.path.join(root, "camion.blend")
    save_scene(path, 1)
    addon.refresh()
    assert st["status"] and st["status"]["state"] == "new", (st["error"], st["path"], bpy.data.filepath)
    assert "[Agregar a 3D-Vault]" in draw_panels()
    bpy.ops.three_d_vault.upload(comment="Primera versión")
    assert st["status"]["state"] == "current" and st["status"]["asset"]["head_version"] == 1, (st["error"], st["status"])
    assert st["notice"] == "Subido como v1.", st["notice"]
    sha_v1 = vault.file_sha256(path)
    assert st["addon_update"] == "", "el servidor entrega la misma versión del complemento"
    step(f"Agregar desde Blender: {st['notice']}")

    bpy.ops.three_d_vault.lock()
    assert st["status"]["locked_by_me"], st
    lock = st["status"]["lock"]
    assert lock["client_id"] == "pc-guto", lock
    assert "[three_d_vault.unlock]" in draw_panels() and "Bloqueado por ti" in draw_panels(), draw_panels()
    step("Bloquear desde Blender usa la identidad de Mi carpeta (misma PC)")

    assert st["status"]["compressed"], "Blender 5 guarda comprimido por defecto"
    bpy.ops.mesh.primitive_monkey_add(location=(0, 4, 0))
    bpy.ops.three_d_vault.save_uncompressed()
    addon.refresh()
    assert not st["status"]["compressed"] and not vault.is_compressed(path), st["status"]
    assert st["status"]["state"] == "modified", st["status"]
    step("Guardar sin comprimir desde el panel")
    bpy.ops.three_d_vault.upload(comment="Agrego un mono", release=True)
    assert st["status"]["asset"]["head_version"] == 2 and not st["status"]["lock"], (st["error"], st["status"])
    assert st["notice"].startswith("Subido como v2. Se reutilizó"), st["notice"]
    step(f"Guardar y subir con delta: {st['notice']}")

    # Ana sube la v3 desde su PC.
    other_path = os.path.join(data, "ana", "Camiones", "modelos", "camion.blend")
    os.makedirs(os.path.dirname(other_path))
    shutil.copy2(path, other_path)
    bpy.ops.wm.open_mainfile(filepath=other_path)
    bpy.ops.mesh.primitive_cylinder_add(location=(0, -4, 0))
    bpy.ops.wm.save_mainfile()
    ost = other.status(other_path)
    other.lock(ost)
    ost = other.status(other_path)
    other.upload(other_path, ost, "Cilindro de Ana", True)
    ana_sha = vault.file_sha256(other_path)

    bpy.ops.wm.open_mainfile(filepath=path)
    addon.refresh()
    assert st["status"]["state"] == "outdated" and st["status"]["local_version"] == 2, st["status"]
    shown = draw_panels()
    assert "[Actualizar a v3]" in shown and "(la que tienes)" in shown, shown
    bpy.ops.three_d_vault.update()
    assert vault.file_sha256(path) == ana_sha, "el archivo no quedó igual al de Ana"
    assert st["status"]["state"] == "current", st["status"]
    bpy.ops.wm.open_mainfile(filepath=path)
    assert any(o.name.startswith("Cylinder") for o in bpy.data.objects), [o.name for o in bpy.data.objects]
    step("Actualizar baja la v3 de Ana y se abre con su cilindro")

    addon.refresh()
    assert [v["number"] for v in st["status"]["versions"]] == [3, 2, 1], st["status"]["versions"]
    v1 = st["status"]["versions"][-1]
    bpy.ops.three_d_vault.open_version(version_id=v1["id"])
    copy = st["notice"].split(": ", 1)[1]
    assert vault.file_sha256(copy) == sha_v1 and vault.file_sha256(path) == ana_sha, st["notice"]
    step(f"Versiones: abrir aparte la v1 la baja idéntica a {os.path.basename(copy)} sin tocar el archivo de trabajo")

    ost = other.status(other_path)
    other.lock(ost)
    addon.refresh()
    bpy.ops.three_d_vault.lock()
    assert st["error"] and "Ana" in st["error"], st["error"]
    step(f"Bloquear lo que tiene Ana falla con: «{st['error']}»")

    assert "[three_d_vault.request_unlock]" in draw_panels(), draw_panels()
    bpy.ops.three_d_vault.request_unlock()
    assert st["notice"] and "Ana recibirá un aviso" in st["notice"], (st["notice"], st["error"])
    events = call("GET", "/api/projects/1/events?since=0", token=ana)["events"]
    assert any(e["kind"] == "unlock_requested" and e["payload"]["actor_name"] == "Guto" for e in events)
    step("Pedir que lo libere desde Blender le llega a Ana como evento")

    # Conflicto: guto cambia sin bloquear; Ana sube la v4; al actualizar, copia.
    bpy.ops.mesh.primitive_torus_add(location=(4, 4, 0))
    bpy.ops.wm.save_mainfile()
    bpy.ops.wm.open_mainfile(filepath=other_path)
    bpy.ops.mesh.primitive_cone_add(location=(-4, 0, 0))
    bpy.ops.wm.save_mainfile()
    other.upload(other_path, other.status(other_path), "Cono de Ana", True)
    bpy.ops.wm.open_mainfile(filepath=path)
    addon.refresh()
    assert st["status"]["state"] == "modified", st["status"]
    bpy.ops.three_d_vault.update()
    copies = glob.glob(os.path.join(root, "camion.conflicto-guto-*.blend"))
    assert len(copies) == 1 and vault.file_sha256(path) == vault.file_sha256(other_path), copies
    step(f"Conflicto sin pérdida: queda {os.path.basename(copies[0])} y la v4 de Ana")

    vault.own_version = lambda: "0.9.0"
    st["addon_update"] = None
    addon.refresh()
    assert st["addon_update"] == "1.1.0", st["addon_update"]
    assert "[three_d_vault.get_addon]" in draw_panels()
    step("Avisa cuando el servidor tiene una versión más nueva del complemento")

    addon.unregister()


if __name__ == "__main__":
    main()
