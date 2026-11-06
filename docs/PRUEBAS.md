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

## 5. Renombrar, borrar, recuperar

| Paso | Debe pasar |
|---|---|
| PC B: renombrar en el Explorador `rueda.stl` → `rueda_trasera.stl` | **Renombrado o movido**, con "Antes: exportados/rueda.stl" |
| **Mover en el servidor** | La ficha conserva el historial. En PC A, `rueda.stl` desaparece y aparece `rueda_trasera.stl` |
| PC B: borrar `render\camion_render.png` | **No está en tu carpeta**. En el servidor y en PC A sigue estando |
| **Descargar** | Vuelve a la carpeta |
| **Borrar del servidor**, en otro archivo que borraste | Pide confirmación. Desaparece para todos y las carpetas lo quitan si no tenía cambios |

## 6. Facilidades

| Prueba | Debe pasar |
|---|---|
| Copiar a la carpeta 2 o 3 archivos nuevos y pulsar **Subir todo (N)** | Un solo comentario para todos |
| **Pausar** y copiar un archivo nuevo | No cambia nada hasta **Reanudar** |
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

## 8. Administración

| Prueba | Debe pasar |
|---|---|
| **Espacio** | Guardado, libre y uso por proyecto |
| Detener el servidor (Ctrl+C) y ejecutar `./bin/3dvault-server purge --keep-last 1 --older-than 0d`, con `DATA_DIR=./data` | Solo simula: dice cuántas versiones quitaría. Con `--apply` las quita; luego `gc --apply` libera los trozos |
| `./bin/3dvault-server check` | 0 corruptos, 0 faltantes |
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
