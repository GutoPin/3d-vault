"""3D-Vault en Blender: estado del archivo abierto, bloquear, subir y actualizar.

Habla directamente con el servidor, con la misma identidad que la página Mi
carpeta de esa PC (el código de conexión), así que un bloqueo hecho aquí vale
también allí. La lógica está en vault.py; aquí solo está la interfaz.
"""

import os
import shutil
import subprocess
import tempfile
import textwrap
import threading
import traceback
from datetime import datetime

import bpy
from bpy.app.handlers import persistent
from bpy.props import BoolProperty, IntProperty, StringProperty

from . import vault

STATE = {"status": None, "path": None, "error": None, "busy": None, "progress": None, "notice": None}
_job = None
_vault = {"code": None, "obj": None}


# ---------- utilidades ----------

def prefs():
    return bpy.context.preferences.addons[__package__].preferences


def get_vault():
    code = prefs().code.strip()
    if not code:
        raise vault.VaultError("Falta el código de conexión. Pégalo en las preferencias del complemento.")
    if _vault["code"] != code:
        _vault.update(code=code, obj=vault.Vault(vault.Api.from_code(code)))
    return _vault["obj"]


def redraw():
    for window in getattr(bpy.context.window_manager, "windows", []):
        for area in window.screen.areas:
            if area.type == "VIEW_3D":
                area.tag_redraw()


def popup(title, lines, icon="INFO"):
    if bpy.app.background:
        return

    def draw(menu, _context):
        for line in lines:
            menu.layout.label(text=line)

    try:
        bpy.context.window_manager.popup_menu(draw, title=title, icon=icon)
    except (RuntimeError, AttributeError):
        pass  # sin ventana, por ejemplo al cargar desde la línea de comandos


def run_job(label, fn, done=None, keep_notice=False):
    """Ejecuta fn en un hilo para no congelar Blender; done(resultado) corre
    después en el hilo principal. En segundo plano (pruebas) es síncrono."""
    global _job
    if _job and _job.is_alive():
        STATE["notice"] = "Espera a que termine: " + STATE["busy"]
        return False
    if not bpy.app.online_access:
        STATE["error"] = "Blender no tiene permiso para conectarse. Actívalo en Preferencias → Sistema → Red → Permitir acceso en línea."
        return False
    STATE.update(busy=label, error=None, progress=None)
    if not keep_notice:
        STATE["notice"] = None
    result = {}

    def work():
        try:
            result["value"] = fn()
        except vault.VaultError as err:
            result["error"] = str(err)
        except Exception as err:  # noqa: BLE001: se muestra al usuario en el panel
            traceback.print_exc()
            result["error"] = f"Error inesperado: {err}"

    def finish():
        STATE.update(busy=None, progress=None)
        if "error" in result:
            STATE["error"] = result["error"]
        elif done:
            done(result.get("value"))
        redraw()

    if bpy.app.background:
        work()
        finish()
        return True

    _job = threading.Thread(target=work, daemon=True)
    _job.start()

    def poll():
        redraw()
        if _job.is_alive():
            return 0.3
        finish()
        return None

    bpy.app.timers.register(poll, first_interval=0.3)
    return True


def progress(what, done, total=0):
    STATE["progress"] = (what, done, total)


REMOTE_EVERY = 60  # segundos entre revisiones de versiones nuevas


def project_root(st):
    """La carpeta del proyecto en esta PC: la ruta del archivo sin su ruta en el proyecto."""
    full = os.path.normpath(bpy.data.filepath)
    rel = os.path.normpath(st["path"].replace("/", os.sep))
    return full[: len(full) - len(rel)].rstrip(os.sep)


def outside_files(st):
    """Imágenes que no viajan bien con el .blend: fuera del proyecto, o con
    ruta absoluta, que en otra PC apunta a otra carpeta."""
    root = os.path.normcase(project_root(st)) + os.sep
    out = []
    for img in bpy.data.images:
        if img.source not in {"FILE", "SEQUENCE", "TILED"} or img.packed_file or not img.filepath:
            continue
        absolute = os.path.normpath(bpy.path.abspath(img.filepath))
        inside = os.path.normcase(absolute).startswith(root)
        if not inside or not img.filepath.startswith("//"):
            out.append((img, absolute, inside))
    return out


def check_remote():
    """Revisa cada minuto si alguien subió una versión del archivo abierto."""
    st = STATE["status"]
    if st and st.get("asset") and not STATE["busy"] and STATE["path"] == bpy.data.filepath and bpy.app.online_access:
        refresh(remote=True)
    return REMOTE_EVERY


def refresh(alert=False, after_save=False, remote=False):
    path = bpy.data.filepath
    STATE["path"] = path
    if not path or not prefs().code.strip():
        STATE["status"] = None
        return

    before = STATE["status"]

    def done(st):
        STATE["status"] = st
        if remote and before and st.get("asset") and before.get("asset"):
            head, old = st["asset"], before["asset"]
            if head["head_version"] > old["head_version"] and head["uploaded_by"] != get_vault().me()["display_name"]:
                text = f"{head['uploaded_by']} subió la v{head['head_version']} de este archivo."
                STATE["notice"] = text + " Pulsa Actualizar para tenerla."
                popup("3D-Vault", [text, "Pulsa Actualizar en el panel 3D-Vault (tecla N)."], "INFO")
        if alert:
            alert_on_open(st)
        if after_save:
            alert_on_save(st)

    # Revisar sigue a casi todo: no borra el aviso de lo que acaba de pasar.
    def work():
        v = get_vault()
        st = v.status(path)
        if STATE.get("addon_update") is None:  # una vez por sesión
            try:
                STATE["addon_update"] = v.addon_update() or ""
            except vault.VaultError:
                STATE["addon_update"] = ""
        return st

    run_job("Revisando", work, done, keep_notice=True)


def lock_text(st):
    lock = st.get("lock")
    if not lock:
        return "Libre"
    if st.get("locked_by_me"):
        return "Bloqueado por ti"
    if st.get("locked_by_me_elsewhere"):
        return f"Bloqueado por ti en {lock['client_name']}"
    return f"Bloqueado por {lock['display_name']}"


def alert_on_open(st):
    asset = st.get("asset")
    if not asset:
        return
    lines = []
    if st["state"] == "outdated":
        lines.append(f"Tienes la v{st['local_version']} y en el servidor está la v{asset['head_version']} de {asset['uploaded_by']}.")
        lines.append("Pulsa Actualizar en el panel 3D-Vault (tecla N) antes de editar.")
    lock = st.get("lock")
    if lock and not st.get("locked_by_me"):
        lines.append(f"{lock_text(st)}: puedes mirarlo, pero no subir cambios.")
    if lines:
        popup("3D-Vault", lines, "ERROR")


def alert_on_save(st):
    if st.get("asset") and not st.get("locked_by_me"):
        popup("3D-Vault: guardaste sin bloquear", [
            "Este archivo no está bloqueado por ti, así que no podrás subirlo.",
            "Bloquéalo en el panel 3D-Vault (tecla N) antes de seguir editando.",
        ], "ERROR")


# ---------- operadores ----------

class THREEDVAULT_OT_refresh(bpy.types.Operator):
    bl_idname = "three_d_vault.refresh"
    bl_label = "Revisar"
    bl_description = "Consultar el estado del archivo en el servidor"

    def execute(self, _context):
        refresh()
        return {"FINISHED"}


class THREEDVAULT_OT_lock(bpy.types.Operator):
    bl_idname = "three_d_vault.lock"
    bl_label = "Bloquear"
    bl_description = "Bloquear el archivo para editarlo. Nadie más podrá subirlo hasta que lo liberes"

    takeover: BoolProperty(name="Traer desde otra PC", default=False)

    def execute(self, _context):
        st = STATE["status"]

        def done(_):
            STATE["notice"] = "Bloqueado: ya puedes editarlo."
            refresh()

        run_job("Bloqueando", lambda: get_vault().lock(st, self.takeover), done)
        return {"FINISHED"}


class THREEDVAULT_OT_unlock(bpy.types.Operator):
    bl_idname = "three_d_vault.unlock"
    bl_label = "Liberar"
    bl_description = "Liberar el bloqueo para que otros puedan editarlo"

    def invoke(self, context, event):
        st = STATE["status"]
        if st and st["state"] == "modified" or bpy.data.is_dirty:
            return context.window_manager.invoke_confirm(self, event, title="Tienes cambios sin subir", message="Si lo liberas, otro podría subir encima. ¿Liberar igual?")
        return self.execute(context)

    def execute(self, _context):
        st = STATE["status"]

        def done(_):
            STATE["notice"] = "Liberado."
            refresh()

        run_job("Liberando", lambda: get_vault().unlock(st), done)
        return {"FINISHED"}


class THREEDVAULT_OT_upload(bpy.types.Operator):
    bl_idname = "three_d_vault.upload"
    bl_label = "Guardar y subir"
    bl_description = "Guardar el archivo y subirlo como versión nueva. Solo viaja lo que cambió"

    comment: StringProperty(name="¿Qué cambió?", default="")
    release: BoolProperty(name="Liberar el bloqueo al terminar", default=True)

    def invoke(self, context, _event):
        st = STATE["status"]
        if st and not st.get("asset"):
            self.comment = "Primera versión"
        return context.window_manager.invoke_props_dialog(self, title="Subir a 3D-Vault", confirm_text="Subir")

    def draw(self, _context):
        st = STATE["status"]
        self.layout.prop(self, "comment")
        if st and st.get("locked_by_me"):
            self.layout.prop(self, "release")

    def execute(self, _context):
        if len(self.comment.strip()) < 3:
            self.report({"ERROR"}, "Escribe un comentario de al menos 3 letras.")
            return {"CANCELLED"}
        if bpy.data.is_dirty:
            STATE["saving_to_upload"] = True  # la subida ya revisa al terminar
            bpy.ops.wm.save_mainfile()
        st = STATE["status"]
        path = bpy.data.filepath
        comment, release = self.comment, self.release

        def done(r):
            if r["no_changes"]:
                STATE["notice"] = "No hay cambios: el servidor ya tenía este archivo."
            elif r["version"] > 1:
                STATE["notice"] = f"Subido como v{r['version']}. Se reutilizó el {max(0, r['reused']):.0f} % del archivo."
            else:
                STATE["notice"] = "Subido como v1."
            refresh()

        run_job("Subiendo", lambda: get_vault().upload(path, st, comment, release, progress), done)
        return {"FINISHED"}


class THREEDVAULT_OT_update(bpy.types.Operator):
    bl_idname = "three_d_vault.update"
    bl_label = "Actualizar"
    bl_description = "Bajar la última versión del servidor y abrirla. Solo baja lo que cambió"

    def invoke(self, context, event):
        st = STATE["status"]
        if bpy.data.is_dirty:
            return context.window_manager.invoke_confirm(self, event, title="Hay cambios sin guardar en Blender", message="Se perderán al cargar la versión del servidor. ¿Seguir?")
        if st and st["state"] == "modified":
            return context.window_manager.invoke_confirm(self, event, title="Tu archivo tiene cambios que el servidor no tiene", message="Se guardará una copia *.conflicto-* junto al archivo. ¿Seguir?")
        return self.execute(context)

    def execute(self, _context):
        st = STATE["status"]
        path = bpy.data.filepath

        def done(copy):
            if copy:
                STATE["notice"] = "Tu versión quedó guardada como " + copy
            if not bpy.app.background:
                bpy.ops.wm.revert_mainfile()
            else:
                refresh()

        run_job("Actualizando", lambda: get_vault().update(path, st, progress), done)
        return {"FINISHED"}


class THREEDVAULT_OT_request(bpy.types.Operator):
    bl_idname = "three_d_vault.request_unlock"
    bl_label = "Pedir que lo libere"
    bl_description = "Avisar a quien tiene el archivo de que lo necesitas. Le llega en Mi carpeta"

    def execute(self, _context):
        st = STATE["status"]

        def done(_):
            STATE["notice"] = f"Listo: {st['lock']['display_name']} recibirá un aviso de que lo necesitas."

        run_job("Avisando", lambda: get_vault().request_unlock(st), done)
        return {"FINISHED"}


class THREEDVAULT_OT_open_version(bpy.types.Operator):
    bl_idname = "three_d_vault.open_version"
    bl_label = "Abrir aparte"
    bl_description = "Bajar esta versión a una carpeta temporal y abrirla en otra ventana de Blender, para compararla. Tu archivo no cambia"

    version_id: IntProperty()

    def execute(self, _context):
        st = STATE["status"]
        version = next(v for v in st["versions"] if v["id"] == self.version_id)
        path = bpy.data.filepath
        stem = os.path.splitext(os.path.basename(path))[0]
        dest = os.path.join(tempfile.gettempdir(), "3D-Vault", f"{stem} v{version['number']}.blend")

        def done(out):
            STATE["notice"] = f"v{version['number']} abierta aparte: {out}"
            if bpy.app.binary_path and not bpy.app.background:
                subprocess.Popen([bpy.app.binary_path, out])

        run_job(f"Bajando v{version['number']}", lambda: get_vault().download_version(path, st, version, dest, progress), done)
        return {"FINISHED"}


class THREEDVAULT_OT_fix_textures(bpy.types.Operator):
    bl_idname = "three_d_vault.fix_textures"
    bl_label = "Arreglar texturas"
    bl_description = "Copiar al proyecto (carpeta texturas) las imágenes que están fuera, usar rutas relativas, guardar y subir las texturas nuevas"

    def execute(self, _context):
        st = STATE["status"]
        root = project_root(st)
        copied, missing = [], []
        for img, absolute, inside in outside_files(st):
            if not inside:
                if not os.path.isfile(absolute):
                    missing.append(img.name)
                    continue
                folder = os.path.join(root, "texturas")
                os.makedirs(folder, exist_ok=True)
                stem, ext = os.path.splitext(os.path.basename(absolute))
                dest, n = os.path.join(folder, stem + ext), 1
                while os.path.exists(dest) and vault.file_sha256(dest) != vault.file_sha256(absolute):
                    n += 1
                    dest = os.path.join(folder, f"{stem}-{n}{ext}")
                if not os.path.exists(dest):
                    shutil.copy2(absolute, dest)
                copied.append(dest)
                absolute = dest
            img.filepath = bpy.path.relpath(absolute)
        STATE["saving_to_upload"] = True
        bpy.ops.wm.save_mainfile()
        name = os.path.basename(bpy.data.filepath)

        def work():
            v = get_vault()
            sent = 0
            for path in copied:
                tst = v.status(path)
                if tst["state"] == "new":
                    v.upload(path, tst, f"Textura de {name}", False, progress)
                    sent += 1
            return sent

        def done(sent):
            text = f"Texturas arregladas: {len(copied)} copiadas a texturas/ ({sent} subidas) y rutas relativas."
            if missing:
                text += f" No se encontraron: {', '.join(missing)}."
            STATE["notice"] = text + " Ahora sube el .blend."
            refresh()

        run_job("Subiendo texturas", work, done)
        return {"FINISHED"}


class THREEDVAULT_OT_uncompress(bpy.types.Operator):
    bl_idname = "three_d_vault.save_uncompressed"
    bl_label = "Guardar sin comprimir"
    bl_description = "Guardar sin la opción Comprimir: así cada versión sube y ocupa solo lo que cambió"

    def execute(self, _context):
        bpy.ops.wm.save_mainfile(compress=False)
        STATE["notice"] = "Guardado sin comprimir. Las próximas versiones subirán mucho menos."
        return {"FINISHED"}


class THREEDVAULT_OT_get_addon(bpy.types.Operator):
    bl_idname = "three_d_vault.get_addon"
    bl_label = "Descargar la versión nueva"
    bl_description = "Bajar la versión nueva del complemento. Luego instálala igual que la primera vez"

    def execute(self, _context):
        bpy.ops.wm.url_open(url=vault.parse_code(prefs().code)["server"] + "/blender/3dvault.zip")
        return {"FINISHED"}


class THREEDVAULT_OT_open_web(bpy.types.Operator):
    bl_idname = "three_d_vault.open_web"
    bl_label = "Ver historial"
    bl_description = "Abrir la ficha del archivo en 3D-Vault: versiones, vista previa y comentarios"

    def execute(self, _context):
        st = STATE["status"]
        server = vault.parse_code(prefs().code)["server"]
        url = f"{server}/assets/{st['asset']['id']}" if st and st.get("asset") else f"{server}/sync"
        bpy.ops.wm.url_open(url=url)
        return {"FINISHED"}


class THREEDVAULT_OT_test(bpy.types.Operator):
    bl_idname = "three_d_vault.test_connection"
    bl_label = "Probar conexión"

    def execute(self, _context):
        def done(me):
            STATE["notice"] = f"Conectado como {me['display_name']}."

        run_job("Conectando", lambda: get_vault().me(), done)
        return {"FINISHED"}


# ---------- interfaz ----------

def wrapped(layout, text, icon="NONE", width=42):
    for i, line in enumerate(textwrap.wrap(text, width) or [""]):
        layout.label(text=line, icon=icon if i == 0 else "BLANK1")


class THREEDVAULT_PT_panel(bpy.types.Panel):
    bl_label = "3D-Vault"
    bl_space_type = "VIEW_3D"
    bl_region_type = "UI"
    bl_category = "3D-Vault"

    def draw(self, _context):
        layout = self.layout
        if not prefs().code.strip():
            wrapped(layout, "Conecta Blender: en la web, abre Mi carpeta → Conectar Blender, copia el código y pégalo en las preferencias de este complemento.", "INFO")
            layout.operator("preferences.addon_show", text="Abrir preferencias").module = __package__
            return
        if not bpy.data.filepath:
            wrapped(layout, "Guarda o abre un archivo dentro de tu carpeta de 3D-Vault.", "INFO")
            return
        if STATE["path"] != bpy.data.filepath and not STATE["busy"]:
            layout.operator("three_d_vault.refresh", icon="FILE_REFRESH")
            return

        if STATE.get("addon_update"):
            box = layout.box()
            wrapped(box, f"Hay una versión nueva del complemento ({STATE['addon_update']}).", "INFO")
            box.operator("three_d_vault.get_addon", icon="IMPORT")
        if STATE["busy"]:
            p = STATE["progress"]
            text = STATE["busy"] + "…"
            if p and p[2]:
                text = f"{p[0]} {100 * p[1] // max(p[2], 1)} %"
            layout.label(text=text, icon="SORTTIME")
        if STATE["error"]:
            wrapped(layout.box(), STATE["error"], "ERROR")
        if STATE["notice"]:
            wrapped(layout.box(), STATE["notice"], "CHECKMARK")

        st = STATE["status"]
        if not st:
            layout.operator("three_d_vault.refresh", icon="FILE_REFRESH")
            return

        box = layout.box()
        box.label(text=st["project"]["name"], icon="FILE_FOLDER")
        wrapped(box, st["path"])
        asset = st.get("asset")
        busy = bool(STATE["busy"])

        if not asset:
            wrapped(box, "Archivo nuevo: todavía no está en el servidor.", "ADD")
            row = layout.row()
            row.enabled = not busy
            row.scale_y = 1.4
            row.operator("three_d_vault.upload", text="Agregar a 3D-Vault", icon="EXPORT")
        else:
            versions = {
                "current": (f"Al día · v{asset['head_version']}", "CHECKMARK"),
                "outdated": (f"Tienes la v{st['local_version']} · hay v{asset['head_version']} de {asset['uploaded_by']}", "ERROR"),
                "modified": ("Cambios sin subir", "MODIFIER"),
            }
            text, icon = versions[st["state"]]
            wrapped(box, text, icon)
            box.label(text=lock_text(st), icon="LOCKED" if st.get("lock") else "UNLOCKED")
            if bpy.data.is_dirty:
                box.label(text="Hay cambios sin guardar (Ctrl+S)", icon="FILE_BLEND")

            col = layout.column(align=True)
            col.enabled = not busy
            col.scale_y = 1.3
            if st["state"] == "outdated":
                col.operator("three_d_vault.update", text=f"Actualizar a v{asset['head_version']}", icon="IMPORT")
            if st.get("locked_by_me"):
                if st["state"] == "modified" or bpy.data.is_dirty:
                    col.operator("three_d_vault.upload", icon="EXPORT")
                col.operator("three_d_vault.unlock", icon="UNLOCKED")
            elif st.get("locked_by_me_elsewhere"):
                col.operator("three_d_vault.lock", text="Traer el bloqueo a esta PC", icon="LOCKED").takeover = True
            elif not st.get("lock"):
                col.operator("three_d_vault.lock", icon="LOCKED")
            else:
                col.operator("three_d_vault.request_unlock", icon="HAND")

        outside = outside_files(st)
        if outside and (not asset or st.get("locked_by_me")):
            tip = layout.box()
            away = sum(1 for _, _, inside in outside if not inside)
            text = f"{len(outside)} imagen(es) no viajarán bien con el archivo"
            text += f" ({away} fuera del proyecto)" if away else " (rutas absolutas)"
            wrapped(tip, text + ": en otras PCs se verían rosas.", "ERROR")
            tip.operator("three_d_vault.fix_textures", icon="TEXTURE")

        if st.get("compressed") and (not asset or st.get("locked_by_me")):
            tip = layout.box()
            wrapped(tip, "Este archivo está comprimido: cada cambio de geometría sube casi el archivo entero. Sin comprimir, solo lo que cambió.", "INFO")
            tip.operator("three_d_vault.save_uncompressed", icon="FILE_TICK")

        row = layout.row(align=True)
        row.operator("three_d_vault.refresh", icon="FILE_REFRESH")
        row.operator("three_d_vault.open_web", icon="URL")


def short_date(iso):
    try:
        return datetime.fromisoformat(iso).astimezone().strftime("%d/%m %H:%M")
    except ValueError:
        return ""


class THREEDVAULT_PT_versions(bpy.types.Panel):
    bl_label = "Versiones"
    bl_space_type = "VIEW_3D"
    bl_region_type = "UI"
    bl_category = "3D-Vault"
    bl_parent_id = "THREEDVAULT_PT_panel"
    bl_options = {"DEFAULT_CLOSED"}

    @classmethod
    def poll(cls, _context):
        st = STATE["status"]
        return bool(st and st.get("versions") and STATE["path"] == bpy.data.filepath)

    def draw(self, _context):
        st = STATE["status"]
        current = st.get("local_version")
        for v in st["versions"][:10]:
            box = self.layout.box()
            row = box.row()
            mark = "  (la que tienes)" if v["number"] == current else ""
            row.label(text=f"v{v['number']} · {v['uploaded_by']} · {short_date(v['created_at'])}{mark}")
            if v["number"] != current:
                op = row.operator("three_d_vault.open_version", text="", icon="WINDOW")
                op.version_id = v["id"]
            wrapped(box, v["comment"], width=40)


class THREEDVAULT_Preferences(bpy.types.AddonPreferences):
    bl_idname = __package__

    code: StringProperty(
        name="Código de conexión",
        description="Cópialo de la web: Mi carpeta → Conectar Blender",
        subtype="PASSWORD",
        update=lambda _self, _context: _vault.update(code=None, obj=None),
    )

    def draw(self, _context):
        layout = self.layout
        wrapped(layout, "1. En la web de 3D-Vault, abre Mi carpeta y pulsa Conectar Blender.", width=90)
        wrapped(layout, "2. Copia el código y pégalo aquí. Blender no guarda tu contraseña.", width=90)
        layout.prop(self, "code")
        row = layout.row()
        row.operator("three_d_vault.test_connection", icon="LINKED")
        if STATE["notice"] or STATE["error"]:
            row.label(text=STATE["error"] or STATE["notice"])
        if not bpy.app.online_access:
            wrapped(layout, "Activa Permitir acceso en línea en Preferencias → Sistema → Red.", "ERROR", 90)


# ---------- avisos al abrir y al guardar ----------

@persistent
def on_load(_dummy):
    STATE.update(status=None, error=None, notice=None)
    if bpy.data.filepath:
        bpy.app.timers.register(lambda: refresh(alert=True), first_interval=0.5)


@persistent
def on_save(_dummy):
    if STATE.pop("saving_to_upload", False):
        return
    refresh(after_save=True)


CLASSES = (
    THREEDVAULT_OT_refresh, THREEDVAULT_OT_lock, THREEDVAULT_OT_unlock, THREEDVAULT_OT_upload,
    THREEDVAULT_OT_update, THREEDVAULT_OT_uncompress, THREEDVAULT_OT_open_web, THREEDVAULT_OT_test, THREEDVAULT_OT_request,
    THREEDVAULT_OT_open_version, THREEDVAULT_OT_get_addon, THREEDVAULT_OT_fix_textures, THREEDVAULT_PT_panel, THREEDVAULT_PT_versions,
    THREEDVAULT_Preferences,
)


def register():
    for cls in CLASSES:
        bpy.utils.register_class(cls)
    bpy.app.handlers.load_post.append(on_load)
    bpy.app.handlers.save_post.append(on_save)
    bpy.app.timers.register(check_remote, first_interval=REMOTE_EVERY, persistent=True)


def unregister():
    if bpy.app.timers.is_registered(check_remote):
        bpy.app.timers.unregister(check_remote)
    for handlers, fn in ((bpy.app.handlers.load_post, on_load), (bpy.app.handlers.save_post, on_save)):
        if fn in handlers:
            handlers.remove(fn)
    for cls in reversed(CLASSES):
        bpy.utils.unregister_class(cls)
