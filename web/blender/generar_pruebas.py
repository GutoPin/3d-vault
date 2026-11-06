"""Genera archivos reales para probar 3D-Vault a mano (ver docs/PRUEBAS.md).

Uso: python generar_pruebas.py CARPETA_DE_SALIDA  (Python con el módulo bpy)

Crea un camión minero sobre un terreno, con miniatura renderizada en Cycles,
una segunda versión con más detalle, las mismas guardadas con Comprimir, y
exportaciones .glb, .obj (con .mtl aparte) y .stl para el visor 3D.
"""

import math
import os
import shutil
import struct
import sys

import bpy
import numpy as np

OUT = os.path.abspath(sys.argv[-1])
PROJECT = os.path.join(OUT, "proyecto-camiones")
VERSIONS = os.path.join(OUT, "para-versiones")
THUMB = 128  # tamaño de las miniaturas que guarda Blender


def mat(name, rgba, metal=0.0, rough=0.5):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    bsdf = next(n for n in m.node_tree.nodes if n.type == "BSDF_PRINCIPLED")
    bsdf.inputs["Base Color"].default_value = rgba
    bsdf.inputs["Metallic"].default_value = metal
    bsdf.inputs["Roughness"].default_value = rough
    m.diffuse_color = rgba
    return m


def add(op, name, material, **kw):
    op(**kw)
    obj = bpy.context.object
    obj.name = name
    obj.data.materials.append(material)
    return obj


def smooth(obj, levels, boxy=False):
    """Subdivide. Con boxy, bisela y subdivide sin redondear: sigue siendo una caja."""
    bpy.context.view_layer.objects.active = obj
    if boxy:
        bevel = obj.modifiers.new("bisel", "BEVEL")
        bevel.width, bevel.segments = 0.08, 3
        bpy.ops.object.modifier_apply(modifier=bevel.name)
    mod = obj.modifiers.new("subdiv", "SUBSURF")
    mod.levels = levels
    if boxy:
        mod.subdivision_type = "SIMPLE"
    bpy.ops.object.modifier_apply(modifier=mod.name)
    bpy.ops.object.shade_smooth_by_angle()


def build():
    bpy.ops.wm.read_factory_settings(use_empty=True)
    yellow = mat("Amarillo minero", (0.95, 0.62, 0.05, 1), 0.2, 0.4)
    dark = mat("Neumático", (0.03, 0.03, 0.03, 1), 0.0, 0.9)
    steel = mat("Acero", (0.45, 0.46, 0.48, 1), 0.9, 0.35)
    glass = mat("Vidrio", (0.05, 0.12, 0.18, 1), 0.0, 0.05)
    dirt = mat("Tierra", (0.36, 0.25, 0.16, 1), 0.0, 1.0)

    add(bpy.ops.mesh.primitive_cube_add, "Chasis", steel, size=1, location=(0, 0, 1.3), scale=(7.5, 3.2, 0.6))
    bed = add(bpy.ops.mesh.primitive_cube_add, "Tolva", yellow, size=1, location=(-0.9, 0, 2.9), scale=(5.6, 3.6, 2.2))
    smooth(bed, 3, boxy=True)
    cabin = add(bpy.ops.mesh.primitive_cube_add, "Cabina", yellow, size=1, location=(3.0, -0.9, 2.5), scale=(1.6, 1.4, 1.6))
    smooth(cabin, 2, boxy=True)
    add(bpy.ops.mesh.primitive_cube_add, "Parabrisas", glass, size=1, location=(3.8, -0.9, 2.7), scale=(0.05, 1.2, 0.8))
    for i, (x, y) in enumerate([(2.6, 1.6), (2.6, -1.6), (-1.6, 1.6), (-1.6, -1.6), (-3.0, 1.6), (-3.0, -1.6)]):
        wheel = add(bpy.ops.mesh.primitive_cylinder_add, f"Rueda {i + 1}", dark, vertices=64, radius=1.1, depth=0.8,
                    location=(x, y, 1.1), rotation=(math.pi / 2, 0, 0))
        smooth(wheel, 2)

    # Terreno con relieve: le da al archivo un peso parecido al de una escena real.
    bpy.ops.mesh.primitive_grid_add(x_subdivisions=700, y_subdivisions=700, size=60)
    ground = bpy.context.object
    ground.name = "Terreno"
    ground.data.materials.append(dirt)
    co = np.empty(len(ground.data.vertices) * 3, dtype=np.float32)
    ground.data.vertices.foreach_get("co", co)
    co = co.reshape(-1, 3)
    rng = np.random.default_rng(7)
    co[:, 2] = 0.6 * np.sin(co[:, 0] * 0.35) * np.cos(co[:, 1] * 0.3) + rng.normal(0, 0.03, len(co))
    co[:, 2] -= np.where(np.hypot(co[:, 0], co[:, 1]) < 9, co[:, 2] + 0.05, 0)  # zona plana bajo el camión
    ground.data.vertices.foreach_set("co", co.ravel())
    ground.data.update()

    bpy.ops.object.camera_add(location=(15, -15, 9), rotation=(math.radians(66), 0, math.radians(45)))
    bpy.context.scene.camera = bpy.context.object
    bpy.ops.object.light_add(type="SUN", rotation=(math.radians(40), 0, math.radians(30)))
    bpy.context.object.data.energy = 4
    world = bpy.data.worlds.new("Cielo")
    world.color = (0.55, 0.65, 0.8)
    bpy.context.scene.world = world


def more_detail():
    """La «v2»: faros nuevos y la tolva reforzada (cambia su geometría)."""
    light = mat("Faro", (1, 0.95, 0.7, 1), 0.0, 0.1)
    for y in (-1.3, -0.5):
        add(bpy.ops.mesh.primitive_uv_sphere_add, "Faro", light, radius=0.25, location=(3.85, y, 1.9))
    bed = bpy.data.objects["Tolva"]
    co = np.empty(len(bed.data.vertices) * 3, dtype=np.float32)
    bed.data.vertices.foreach_get("co", co)
    co = co.reshape(-1, 3)
    co[:, 2] += 0.04 * np.sin(co[:, 0] * 18)  # nervios de refuerzo
    bed.data.vertices.foreach_set("co", co.ravel())
    bed.data.update()


def render_thumbnail():
    scene = bpy.context.scene
    scene.render.engine = "CYCLES"
    scene.cycles.device = "CPU"
    scene.cycles.samples = 24
    scene.cycles.use_denoising = False
    scene.render.resolution_x = scene.render.resolution_y = THUMB
    scene.render.resolution_percentage = 100
    path = os.path.join(OUT, "_miniatura.png")
    scene.render.filepath = path
    bpy.ops.render.render(write_still=True)
    img = bpy.data.images.load(path)
    px = np.empty(THUMB * THUMB * 4, dtype=np.float32)
    img.pixels.foreach_get(px)  # de abajo arriba, como el bloque TEST
    bpy.data.images.remove(img)
    os.remove(path)
    return (np.clip(px, 0, 1) * 255 + 0.5).astype(np.uint8).tobytes()


def inject_thumbnail(path, rgba):
    """Escribe el bloque TEST (miniatura) antes de GLOB, donde lo pone Blender.

    Blender sin interfaz no genera miniaturas: no tiene GPU para dibujarlas. Al
    abrir el archivo y volver a guardarlo, Blender conserva este bloque.
    """
    with open(path, "rb") as f:
        data = f.read()
    assert data[:9] == b"BLENDER17", "se espera el formato de Blender 5 sin comprimir"
    pos = 17
    while True:
        code, length = data[pos:pos + 4], struct.unpack_from("<q", data, pos + 16)[0]
        if code in (b"TEST", b"GLOB"):
            break
        pos += 32 + length
    end = pos + 32 + length if code == b"TEST" else pos
    body = struct.pack("<ii", THUMB, THUMB) + rgba
    block = struct.pack("<4siQqq", b"TEST", 0, 1, len(body), 1) + body
    with open(path, "wb") as f:
        f.write(data[:pos] + block + data[end:])


def save(path, compress):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    bpy.ops.wm.save_as_mainfile(filepath=path, compress=compress, copy=True)


def export_selected(names, fn):
    bpy.ops.object.select_all(action="DESELECT")
    for n in names:
        bpy.data.objects[n].select_set(True)
    fn()


def main():
    os.makedirs(OUT, exist_ok=True)
    v1 = os.path.join(PROJECT, "modelos", "camion.blend")
    v2 = os.path.join(VERSIONS, "camion_v2_mas_detalle.blend")

    build()
    thumb1 = render_thumbnail()
    save(v1, False)
    inject_thumbnail(v1, thumb1)

    more_detail()
    thumb2 = render_thumbnail()
    save(v2, False)
    inject_thumbnail(v2, thumb2)

    # Las mismas, con Comprimir: Blender conserva la miniatura al reabrir.
    bpy.ops.wm.open_mainfile(filepath=v1)
    save(os.path.join(VERSIONS, "camion_comprimido.blend"), True)
    truck = [o.name for o in bpy.data.objects if o.type == "MESH" and o.name != "Terreno"]
    exported = os.path.join(PROJECT, "exportados")
    os.makedirs(exported, exist_ok=True)
    export_selected(truck, lambda: bpy.ops.export_scene.gltf(filepath=os.path.join(exported, "camion.glb"), use_selection=True, export_format="GLB"))
    export_selected(["Tolva"], lambda: bpy.ops.wm.obj_export(filepath=os.path.join(exported, "tolva.obj"), export_selected_objects=True))
    export_selected(["Rueda 1"], lambda: bpy.ops.wm.stl_export(filepath=os.path.join(exported, "rueda.stl"), export_selected_objects=True))

    bpy.ops.wm.open_mainfile(filepath=v2)
    save(os.path.join(VERSIONS, "camion_comprimido_v2.blend"), True)

    # Una imagen para la ficha y archivos que 3D-Vault debe ignorar.
    scene = bpy.context.scene
    scene.render.engine = "CYCLES"
    scene.cycles.samples = 16
    scene.cycles.use_denoising = False
    scene.render.resolution_x, scene.render.resolution_y = 960, 540
    scene.render.filepath = os.path.join(PROJECT, "render", "camion_render.png")
    bpy.ops.render.render(write_still=True)
    shutil.copy2(v1, v1 + "1")  # respaldo de Blender: se ignora
    with open(os.path.join(PROJECT, "modelos", "~bloqueo.tmp"), "w") as f:
        f.write("temporal: se ignora")

    for root, _, files in os.walk(OUT):
        for name in sorted(files):
            p = os.path.join(root, name)
            print(f"{os.path.getsize(p) / 2**20:8.1f} MiB  {os.path.relpath(p, OUT)}")


if __name__ == "__main__":
    main()
