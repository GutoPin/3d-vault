"""3D-Vault en Blender: estado del archivo abierto, bloquear, subir y actualizar.

Habla directamente con el servidor, con la misma identidad que la página Mi
carpeta de esa PC (el código de conexión), así que un bloqueo hecho aquí vale
también allí. La lógica está en vault.py; aquí solo está la interfaz.
"""

import textwrap
import threading
import traceback

import bpy
from bpy.app.handlers import persistent
from bpy.props import BoolProperty, StringProperty

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


def refresh(alert=False, after_save=False):
    path = bpy.data.filepath
    STATE["path"] = path
    if not path or not prefs().code.strip():
        STATE["status"] = None
        return

    def done(st):
        STATE["status"] = st
        if alert:
            alert_on_open(st)
        if after_save:
            alert_on_save(st)

    # Revisar sigue a casi todo: no borra el aviso de lo que acaba de pasar.
    run_job("Revisando", lambda: get_vault().status(path), done, keep_notice=True)


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


class THREEDVAULT_OT_uncompress(bpy.types.Operator):
    bl_idname = "three_d_vault.save_uncompressed"
    bl_label = "Guardar sin comprimir"
    bl_description = "Guardar sin la opción Comprimir: así cada versión sube y ocupa solo lo que cambió"

    def execute(self, _context):
        bpy.ops.wm.save_mainfile(compress=False)
        STATE["notice"] = "Guardado sin comprimir. Las próximas versiones subirán mucho menos."
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

        if st.get("compressed") and (not asset or st.get("locked_by_me")):
            tip = layout.box()
            wrapped(tip, "Este archivo está comprimido: cada cambio de geometría sube casi el archivo entero. Sin comprimir, solo lo que cambió.", "INFO")
            tip.operator("three_d_vault.save_uncompressed", icon="FILE_TICK")

        row = layout.row(align=True)
        row.operator("three_d_vault.refresh", icon="FILE_REFRESH")
        row.operator("three_d_vault.open_web", icon="URL")


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
    THREEDVAULT_OT_update, THREEDVAULT_OT_uncompress, THREEDVAULT_OT_open_web, THREEDVAULT_OT_test, THREEDVAULT_PT_panel,
    THREEDVAULT_Preferences,
)


def register():
    for cls in CLASSES:
        bpy.utils.register_class(cls)
    bpy.app.handlers.load_post.append(on_load)
    bpy.app.handlers.save_post.append(on_save)


def unregister():
    for handlers, fn in ((bpy.app.handlers.load_post, on_load), (bpy.app.handlers.save_post, on_save)):
        if fn in handlers:
            handlers.remove(fn)
    for cls in reversed(CLASSES):
        bpy.utils.unregister_class(cls)
