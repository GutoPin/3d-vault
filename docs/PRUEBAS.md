# Cómo probar 3D-Vault

Guía para probarlo a mano antes de dárselo al equipo. Cada prueba dice qué hacer y qué debe pasar. Al final están los errores posibles y las pruebas automáticas.

## Preparación

1. **Servidor local**, en WSL, desde la carpeta del repositorio:

   ```sh
   make build-server
   set -a; . data/.env-local; set +a
   DATA_DIR=./data LISTEN_ADDR=127.0.0.1:8080 INSECURE_COOKIES=true SESSION_HOURS=168 ./bin/3dvault-server serve
   ```

   Si `data/.env-local` no existe, créalo con `echo "SECRET_KEY=$(openssl rand -base64 48)" > data/.env-local`, y crea el admin con `./bin/3dvault-server create-admin --username admin` (con las mismas variables).

2. **Archivos de prueba:** `C:\Users\Augusto\Documents\3D-Vault-archivos-prueba`. Para generarlos de nuevo: `python web/blender/generar_pruebas.py CARPETA`, con el módulo `bpy` (ver "Pruebas automáticas").

3. **Dos "PCs" en una sola:** una ventana normal de Edge como **PC A** y una ventana InPrivate (Ctrl+Shift+N) como **PC B**, cada una con su usuario y su carpeta. InPrivate no comparte sesión, carpeta ni estado con la ventana normal.

4. Abre `http://localhost:8080`, entra como `admin` y:
   - En **Usuarios**, crea una segunda cuenta, por ejemplo `ana`.
   - En **Todos los proyectos**, crea el proyecto `Camiones`.

5. Crea dos carpetas vacías: `C:\Users\Augusto\Documents\3D-Vault-A` y `...\3D-Vault-B`.

## 1. Primera sincronización

| Paso | Debe pasar |
|---|---|
| PC A: **Mi carpeta → Elegir carpeta** → `3D-Vault-A` → aceptar los dos permisos | Aparece "Proyectos en esta PC" |
| Marcar `Camiones` | Se crea `3D-Vault-A\Camiones`. La tabla dice que el proyecto no tiene archivos |
| Abrir `3D-Vault-archivos-prueba\proyecto-camiones`, seleccionar `modelos`, `exportados` y `render`, y **arrastrarlas sobre la tabla** | Se copian a `3D-Vault-A\Camiones` y se abre el diálogo "Agregar lo que arrastraste (6)" |
| Aceptar con el comentario propuesto | "Listo: 6 archivo(s) subidos." Aparecen 6 archivos **Al día**, sin `camion.blend1` ni `~bloqueo.tmp` |
| PC B (InPrivate, usuario `ana`): Mi carpeta → `3D-Vault-B` → marcar `Camiones` | Pregunta "Se descargarán 6 archivo(s)" con el tamaño total. Al aceptar, los 6 quedan Al día en `3D-Vault-B\Camiones` |

## 1b. Elegir carpetas (sincronización selectiva)

| Paso | Debe pasar |
|---|---|
| PC B marca un proyecto por primera vez | Antes de bajar nada pregunta qué carpetas quiere, con el peso de cada una y el total que quedará en la PC |
| Desmarcar una subcarpeta | Su carpeta madre queda con marca intermedia; el total baja |
| PC B: **Carpetas…** → desmarcar `texturas` → **Guardar** | Se borra de la carpeta de B. Junto al proyecto: "Solo en el servidor: N archivo(s)". En el servidor y en A sigue igual |
| PC A sube un archivo a `texturas` | No se baja en B |
| PC B edita un archivo, sin subirlo, y desmarca su carpeta | Ese archivo se queda, con el aviso "Su carpeta no se sincroniza en esta PC"; los demás de la carpeta se borran de B |
| Desmarcar una carpeta vacía | Se quita de B, pero no del servidor |
| Volver a marcar `texturas` | Se baja otra vez |

## 2. Vistas previas

Abre cada archivo desde **Camiones** en el menú, o con su nombre en la tabla.

| Archivo | Debe pasar |
|---|---|
| `modelos/camion.blend` | Miniatura del camión amarillo y el historial |
| `exportados/camion.glb` | Visor 3D con el camión en color. Arrastrar gira, la rueda acerca y **Encuadrar** vuelve a la vista inicial |
| `exportados/tolva.obj` | Visor 3D, con el aviso de que usa archivos aparte (el `.mtl`) |
| `exportados/rueda.stl` | Visor 3D con la rueda |
| `render/camion_render.png` | La imagen |

## 3. Versión nueva: bloquear, editar, subir

| Paso | Debe pasar |
|---|---|
| PC A: **Bloquear** en `modelos/camion.blend` | "Bloqueado por ti". En PC B, en segundos y sin tocar nada: "Bloqueado por Guto" (o tu nombre) |
| PC A: copiar `para-versiones\camion_v2_mas_detalle.blend` sobre `3D-Vault-A\Camiones\modelos\camion.blend`, reemplazándolo | Al volver a la pestaña: **Cambios sin subir** y el título de la pestaña empieza por "(1)" |
| **Subir…**, comentario "Más detalle", dejar marcado Liberar | "Subido como v2. Se reutilizó el 93 % del archivo." |
| PC B, sin hacer nada | En unos segundos, `camion.blend` pasa a v2. La ficha muestra la miniatura con faros y "3,5 MB (7,4 %)" en el historial |

Comparación opcional con Comprimir: agrega `para-versiones\camion_comprimido.blend` y luego reemplázalo por `camion_comprimido_v2.blend`. Debe reutilizar menos (en esta pareja, cerca del 85 % frente al 93 %). En una edición de geometría más repartida, medimos que comprimido sube el 97 % del archivo y sin comprimir el 1,4 %.

## 4. Bloqueos y conflictos

| Paso | Debe pasar |
|---|---|
| PC A bloquea `camion.blend`. PC B modifica su copia (por ejemplo, copia encima `camion_v2…` o ábrelo en Blender y guárdalo con un cambio) | En B: **Cambios sin subir**, con "Bloqueado por …" |
| PC B: **Subir…** | Error: "Bloqueado por … desde …". No se crea versión |
| PC A sube una versión nueva (paso 3 con otro archivo) | En B: el archivo de B queda como `camion.conflicto-ana-AAAAMMDD-HHMM.blend` junto al original, que pasa a la versión de A. No se pierde nada |
| PC B: **Liberar** en un archivo bloqueado por A | No aparece el botón: solo A puede liberar. El admin puede **Forzar desbloqueo** en la ficha |
| PC B: **Pedir que lo libere** | En A, al momento: "Ana necesita …" y aviso de escritorio aunque esté a la vista. Repetirlo antes de 5 minutos da error |

## 5. Renombrar, borrar, recuperar

| Paso | Debe pasar |
|---|---|
| PC B: renombrar en el Explorador `rueda.stl` → `rueda_trasera.stl` | **Renombrado o movido**, con "Antes: exportados/rueda.stl" |
| **Mover en el servidor** | La ficha conserva el historial. En PC A, `rueda.stl` desaparece y aparece `rueda_trasera.stl` |
| PC B: borrar `render\camion_render.png` | En segundos: **Borrado en tu PC: falta confirmar**, aviso arriba y número junto a Mi carpeta en el menú. En el servidor y en PC A sigue estando |
| **Descargar** | Vuelve a la carpeta |
| **Subir cambios** | El diálogo dice qué se borra. Desaparece para todos, las carpetas lo quitan si no tenía cambios y queda en la Papelera |

## 6. Facilidades

| Prueba | Debe pasar |
|---|---|
| Copiar a la carpeta 2 o 3 archivos nuevos y pulsar **Subir cambios (N)** | Un solo comentario para todos |
| **Pausar** y copiar un archivo nuevo | No cambia nada hasta **Reanudar** |
| Escribir en el buscador o marcar **Solo lo que necesita algo de mí** | La tabla se filtra |
| Cerrar la pestaña y volver a abrir `/sync` | Pide **Permitir y sincronizar**. Al aceptar, el estado sigue igual y no se vuelve a bajar nada |
| **Instalar** (o menú ⋯ → Aplicaciones → Instalar) | Se abre en su ventana. En `edge://apps`, clic derecho → Iniciar automáticamente al iniciar sesión |
| **Activar avisos**, minimizar la ventana y subir una versión desde la otra PC | Aviso de Windows: "… actualizado a vN por …" |
| Abrir `/sync` en otra pestaña de la misma ventana | "Ya está abierta en otra pestaña" |
| Ficha de un archivo → **Subir nueva versión**, en Firefox si lo tienes | Sube sin carpeta conectada. Mi carpeta baja la versión nueva |
| Proyecto → **Agregar archivos**, con carpeta `pruebas` | Aparecen como `pruebas/…` |

## 7. Complemento de Blender

| Paso | Debe pasar |
|---|---|
| PC A: Mi carpeta → Comodidades → Conectar Blender → **Descarga el complemento** | Se baja `3dvault-blender.zip` |
| Blender: Editar → Preferencias → Complementos → ⌄ → **Instalar desde el disco** → el `.zip` | Aparece "3D-Vault" activado |
| Mi carpeta: **Generar código** → **Copiar** → pegar en "Código de conexión" → **Probar conexión** | "Conectado como …". Si dice que no tiene permiso: Preferencias → Sistema → Red → Permitir acceso en línea |
| Abrir `3D-Vault-A\Camiones\modelos\camion.blend`, tecla **N** → pestaña **3D-Vault** | "Camiones", la ruta, "Al día · v2" y el estado del bloqueo |
| Si en PC B hay una versión más nueva o el archivo está bloqueado por B | Al abrirlo, una ventana avisa |
| **Bloquear**, mover un objeto, **Guardar y subir** | Pide el comentario, guarda y sube. Mi carpeta de PC A lo muestra como suyo (misma PC) y PC B recibe la versión |
| Guardar (Ctrl+S) sin tener el bloqueo | Aviso: "guardaste sin bloquear" |
| Con una versión más nueva en el servidor: **Actualizar a vN** | Baja la versión y la abre. Si tenías cambios propios, antes guarda la copia `*.conflicto-*` |
| Archivo comprimido (ábrelo desde `para-versiones` copiado al proyecto) | Sugiere **Guardar sin comprimir** |
| Desplegar **Versiones** y pulsar el botón de ventana de una anterior | Se abre otra ventana de Blender con esa versión; tu archivo no cambia |
| Con el archivo bloqueado por otro: **Pedir que lo libere** | Le llega el aviso en su Mi carpeta |
| Con el archivo abierto en Blender, subir una versión desde la otra PC y esperar un minuto | Ventana en Blender: "… subió la vN de este archivo" |
| Bloquear, añadir a un material una imagen de Descargas y guardar | Aviso de imágenes que no viajarán bien; **Arreglar rutas** las copia a `texturas/`, deja la ruta relativa y las sube |

## 8. Carpetas y archivos enlazados

| Paso | Debe pasar |
|---|---|
| En la página de `Camiones`, **Elegir una carpeta** → una carpeta con una escena, `texturas/` y `maestros/` | Resumen con los archivos nuevos y los temporales que se omiten (`.blend1`). Al subir, aparecen con sus subcarpetas |
| Repetirlo tras cambiar una textura | El resumen dice que ya existen; la textura cambiada queda en v2 y el resto "sin cambios" |
| Arrastrar una carpeta desde el Explorador sobre la página del proyecto | Igual que elegirla. **Agregar aquí** en una carpeta de la lista la pone como destino |
| En Blender: maestro `maestros/camion.blend` con una colección, subido. Escena nueva → **Enlazar desde 3D-Vault…** → la colección del maestro | El panel **Archivos enlazados** muestra `camion.blend` **Al día** |
| Enlazar también un `.blend` de Descargas | **Fuera de la carpeta de 3D-Vault**. Al subir la escena, el diálogo lo advierte |
| Subir la escena y abrir su ficha en la web | **Archivos enlazados**: el maestro **En 3D-Vault**, el de Descargas **Fuera de 3D-Vault**. En la lista, "1 enlace roto" y la pestaña **Con enlaces rotos** |
| Ficha del maestro | **Lo usan**: la escena |
| **Descargar con todo lo que usa (.zip)** y abrir la escena descomprimida | Se ve el camión enlazado |
| PC B cambia el maestro y lo sube, con la escena abierta en PC A y Mi carpeta abierta | En A, en unos segundos: **Cambió en el disco** → **Recargar para ver los cambios** muestra el cambio sin cerrar la escena |
| Lo mismo con Mi carpeta cerrada en A | **Tienes la v1 · hay v2 de …** → **Actualizar y recargar** |
| Menú de la izquierda: flecha junto a `Camiones` | Se despliegan sus carpetas y archivos; el archivo abierto queda marcado |
| Ícono de carpeta con + sobre `Camiones` → `ASSETS` → Enter | `ASSETS` aparece "vacía" en el menú y en la tabla, y en segundos en la carpeta de PC A y PC B |
| PC B: crear en el Explorador `Camiones\RENDERS` | Aparece en la web como carpeta vacía |
| Web: **Renombrar** `ASSETS` → `MAQUINAS` | El diálogo dice cuántos archivos se mueven y qué escenas pierden sus enlaces. En PC A y B se mueve la carpeta sin descargar nada |
| Web: **Borrar** `MAQUINAS` | Los archivos van a la **Papelera** del proyecto y se quitan de las PCs. **Recuperar** uno lo devuelve a su carpeta y a las PCs |
| Con un archivo de dentro bloqueado por otra persona: **Renombrar** o **Borrar** | El diálogo lo avisa y no deja continuar |
| PC B: borrar `RENDERS` en el Explorador | Desaparece de la web y de PC A. Una carpeta con archivos no se borra |

## 9. Cuenta, actividad y búsqueda

| Prueba | Debe pasar |
|---|---|
| Como `ana`: tu nombre abajo → **Mi cuenta** → cambiar contraseña con la actual mal | Error. Con 5 fallos seguidos, espera obligatoria |
| Cambiarla bien | Sigues dentro. Avisa de generar un código nuevo para Blender, y el de antes deja de funcionar |
| **Mis conexiones** → Revocar | Desaparece. Blender con ese código dice que la conexión ya no es válida |
| **Mis bloqueos** | Todos tus bloqueos de todos los proyectos; "Liberar" vuelve a Mi cuenta |
| **Actividad**, filtrar por proyecto y por persona; "Ver más" | Subidas, bloqueos, movimientos, pedidos de liberar… en español |
| Buscar `CAMION` y `camion` | Encuentra `camion.blend` y cualquier `Camión…` |

## 10. Administración

| Prueba | Debe pasar |
|---|---|
| **Espacio** | Guardado, libre y uso por proyecto |
| Detener el servidor (Ctrl+C) y ejecutar `./bin/3dvault-server purge --keep-last 1 --older-than 0d`, con `DATA_DIR=./data` | Solo simula: dice cuántas versiones quitaría. Con `--apply` las quita; luego `gc --apply` libera los trozos |
| `./bin/3dvault-server check` | 0 corruptos, 0 faltantes |
| **Bloqueos** (admin) | Todos los bloqueos, con "Hace N días" en los viejos. **Forzar desbloqueo** lo libera y queda en Actividad |
| Proyecto → **Descargar todo (.zip)** y el **Descargar .zip** de una carpeta | El .zip abre en Windows con las carpetas y la última versión de cada archivo |
| Historial de `camion.blend` | Miniatura en cada versión; al pulsarla se ve grande |
| Arrancar el servidor con `BACKUP_DIR=/tmp/copias BACKUP_HOUR=<la hora siguiente>` | A esa hora hace la copia; Espacio muestra la última copia correcta y cuánto tardó |
| `./bin/3dvault-server backup --to /tmp/copia` y luego `restore` en otra carpeta | Restaura y `check` sale limpio |

## Errores posibles y qué significan

| Lo que ves | Causa | Qué hacer |
|---|---|---|
| No se puede elegir Documentos o el Escritorio | El navegador no deja dar acceso a carpetas del sistema | Elige una carpeta dentro |
| "Abre esta página en Microsoft Edge o Google Chrome" | Firefox y Safari no tienen acceso a carpetas | Usa Edge, o la subida desde la ficha |
| Desde otra PC de la red (`http://192.168.x.x:8080`) no aparece Mi carpeta | Sin HTTPS, el navegador solo permite carpetas en `localhost` | Para probar con compañeros hace falta HTTPS: el VPS con dominio |
| `http://localhost:8080` no abre desde Windows | WSL no reenvía el puerto | Reinicia WSL (`wsl --shutdown`) o revisa `localhostForwarding` en `.wslconfig` |
| Pide permiso cada vez que abres la página | Es lo normal del navegador | **Permitir siempre** si lo ofrece, o instalarla como aplicación |
| "Bloqueado por ti en Navegador" en Mi carpeta | Bloqueaste desde la **ficha** web, que cuenta como otra PC | **Traer el bloqueo a esta PC**. Para el día a día, bloquea desde Mi carpeta o Blender |
| La sincronización se detiene tras un rato sin usar Edge | Las pestañas inactivas de Edge "se duermen" | Instálala como aplicación, o en `edge://settings/system` añade el sitio a "Nunca poner en suspensión" |
| Un archivo en "Error: … caracteres no permitidos / nombre reservado" | Nombres que Windows o el servidor rechazan: terminados en punto o espacio, `CON`, `NUL`… | Renómbralo |
| Dos archivos que solo difieren en mayúsculas | El servidor los trata como el mismo | Renombra uno |
| "Un archivo está en uso" | Otro programa lo tiene abierto mientras se reemplaza | Se reintenta solo |
| Se corta la subida (cerraste la pestaña) | — | Al volver a subir, solo envía los trozos que faltaban |
| "Tu sesión terminó" | Venció `SESSION_HOURS` | Entrar de nuevo; vuelve a Mi carpeta |
| Blender: "La conexión ya no es válida" | Se cambió la contraseña de ese usuario (revoca los tokens) | Generar un código nuevo |
| El visor pide "Cargar de todos modos" | El modelo pasa de 200 MB | Es un aviso, no un error |
| Un `.gltf` muestra error "la geometría está en un archivo .bin aparte" | El visor no pide archivos aparte | Exporta como `.glb` |

## Pruebas automáticas

```sh
make check                    # go vet y todos los tests de Go
make test-web                 # cortes en JavaScript idénticos a Go, SHA-256
make e2e                      # dos navegadores contra un servidor real (Node y Chromium de Playwright)
make test-blender PY=...      # núcleo del complemento: cortes idénticos (Python con numpy)
make test-blender-e2e PY=...  # el complemento dentro de Blender contra el servidor
```

Para las de Blender: `uv venv -p 3.13 bpyenv && VIRTUAL_ENV=bpyenv uv pip install bpy==5.2.3` y `PY=bpyenv/bin/python`. En WSL, `bpy` necesita librerías de X11 (`libsm6`, `libxi6`, `libxxf86vm1`, `libxkbcommon0`, `libgl1`…): con `sudo apt install` o, sin sudo, extrayendo los `.deb` y usando `LD_LIBRARY_PATH`.
