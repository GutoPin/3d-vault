"""Genera los .blend de prueba de blendrefs.

Uso: blender -b --factory-startup --python generar.py -- CARPETA

Crea un proyecto pequeño: un maestro con objetos, una escena que enlaza una
colección del maestro y usa texturas de varias formas (relativa, absoluta,
empaquetada), y la misma escena guardada con Comprimir.
"""

import os
import sys

import bpy

OUT = os.path.abspath(sys.argv[sys.argv.index("--") + 1])
PROJECT = os.path.join(OUT, "proyecto")


def reset():
    bpy.ops.wm.read_factory_settings(use_empty=True)


def png(path, color):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    img = bpy.data.images.new(os.path.basename(path), 4, 4)
    img.pixels = list(color) * 16
    img.filepath_raw = path
    img.file_format = "PNG"
    img.save()
    bpy.data.images.remove(img)


# Texturas en disco.
png(os.path.join(PROJECT, "texturas", "madera.png"), (0.6, 0.4, 0.2, 1))
png(os.path.join(OUT, "fuera", "cielo.png"), (0.2, 0.4, 0.9, 1))
png(os.path.join(PROJECT, "texturas", "empaquetada.png"), (1, 0, 0, 1))

# Maestro: una colección con un camión y una textura propia.
reset()
coll = bpy.data.collections.new("Camion")
bpy.context.scene.collection.children.link(coll)
mesh = bpy.data.meshes.new("camion")
mesh.from_pydata([(0, 0, 0), (1, 0, 0), (0, 1, 0)], [], [(0, 1, 2)])
coll.objects.link(bpy.data.objects.new("Camion", mesh))
wood = bpy.data.images.load(os.path.join(PROJECT, "texturas", "madera.png"))
wood.filepath = "//../texturas/madera.png"
wood.use_fake_user = True
master = os.path.join(PROJECT, "maestros", "camion_maestro.blend")
os.makedirs(os.path.dirname(master), exist_ok=True)
bpy.ops.wm.save_as_mainfile(filepath=master, relative_remap=False, compress=False)

# Escena: enlaza la colección del maestro, con ruta relativa.
reset()
scene_path = os.path.join(PROJECT, "escenas", "escena_01.blend")
os.makedirs(os.path.dirname(scene_path), exist_ok=True)
bpy.ops.wm.save_as_mainfile(filepath=scene_path)
with bpy.data.libraries.load(master, link=True, relative=True) as (src, dst):
    dst.collections = ["Camion"]
inst = bpy.data.objects.new("Camion", None)
inst.instance_type = "COLLECTION"
inst.instance_collection = dst.collections[0]
bpy.context.scene.collection.objects.link(inst)
for path, rel in ((os.path.join(PROJECT, "texturas", "madera.png"), True), (os.path.join(OUT, "fuera", "cielo.png"), False)):
    img = bpy.data.images.load(path)
    img.use_fake_user = True
    if rel:
        img.filepath = bpy.path.relpath(path)
packed = bpy.data.images.load(os.path.join(PROJECT, "texturas", "empaquetada.png"))
packed.use_fake_user = True
packed.pack()
gen = bpy.data.images.new("generada", 4, 4)
gen.use_fake_user = True
bpy.ops.wm.save_mainfile(filepath=scene_path, relative_remap=False, compress=False)
bpy.ops.wm.save_as_mainfile(filepath=os.path.join(PROJECT, "escenas", "escena_01_comprimida.blend"), copy=True, relative_remap=False, compress=True)

for lib in bpy.data.libraries:
    print("LIB", lib.filepath)
for img in bpy.data.images:
    print("IMG", img.name, img.source, repr(img.filepath), bool(img.packed_file), img.library)
