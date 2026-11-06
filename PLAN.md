# 3D-Vault — Plan de desarrollo (para Claude Code)

> Lee este documento completo antes de escribir código. Trabaja fase por fase y no avances a la siguiente hasta cumplir los criterios de aceptación de la actual. Ante una decisión no cubierta aquí, elige la opción más simple y anótala en `docs/DECISIONES.md`.
>
> Los cambios respecto a la especificación original y sus motivos también están en `docs/DECISIONES.md`.

## 1. Contexto

Somos un equipo pequeño de modeladores 3D (Blender principalmente) en una sola empresa. Necesitamos un sistema **interno, ligero y autohospedado** en nuestro VPS para:

- Guardar los archivos 3D de cada proyecto en un solo lugar, con **historial de versiones**.
- **Bloquear** un archivo mientras alguien lo edita (los binarios 3D no se fusionan).
- **Sincronizar automáticamente** una carpeta local en cada PC con el servidor, para que nadie tenga que descargar a mano, y que **solo viajen los cambios**, no el archivo completo.

No queremos Git ni que los modeladores aprendan comandos. El modelo es el de Perforce/PDM simplificado: los archivos están siempre en tu PC (solo lectura); los **bloqueas** para editarlos y **subes** el cambio con un comentario.

El VPS ya existe y tiene espacio suficiente. Todo se guarda en el VPS.

## 2. Componentes

| Componente | Qué es | Dónde corre |
|---|---|---|
| `3dvault-server` | API + página web (historial, visor 3D, admin). Un solo binario Go. | VPS (Docker) |
| Mi carpeta (`/sync`) | Sincronización desde el navegador: la página trabaja con una carpeta local mediante la File System Access API. JavaScript sin dependencias, servido por `3dvault-server`. | Edge o Chrome en la PC de cada modelador |
| `3dvault-cli` | Herramienta de comandos (`push`, `pull`, `lock`…) para pruebas y para el administrador. | Cualquier PC |
| Caddy | Proxy inverso con HTTPS automático. | VPS (Docker) |

## 3. Principios

- **Go en todo**, binarios únicos sin dependencias externas (sin cgo: SQLite con `modernc.org/sqlite`).
- **Simple primero.** Pocas dependencias, nada de microservicios.
- **Interfaz en español**, pensada para artistas.
- **Nunca perder datos.** Versiones inmutables, borrado suave, escrituras atómicas, y ante un conflicto se guarda una copia, nunca se sobrescribe en silencio.
- **Uso interno.** Sin registro público: el admin crea las cuentas.

## 4. Conceptos

| Concepto | Significado |
|---|---|
| Proyecto | Agrupa los archivos de un trabajo. Es la unidad de sincronización. |
| Archivo | Un asset con ruta dentro del proyecto (ej. `modelos/camiones/camion_797F.blend`). |
| Versión | Una copia concreta: v1, v2… con comentario obligatorio. Inmutable. |
| Bloqueo | "Juan está editando este archivo". Solo quien bloquea puede subir versiones. |
| Trozo (chunk) | Pedazo de un archivo (≈1 MB) identificado por su hash. Las versiones se guardan como listas de trozos. |
| Manifiesto | La lista ordenada de trozos que forman una versión + el hash del archivo completo. |

## 5. Almacenamiento por trozos (clave para no transferir todo)

- Cada archivo se corta con **content-defined chunking (FastCDC)**: tamaño mínimo 256 KiB, promedio 1 MiB, máximo 4 MiB. Adaptado de `github.com/jotfs/fastcdc-go` en `internal/chunker`.
- Id de trozo = SHA-256 del contenido sin comprimir. Se guarda comprimido con zstd (`github.com/klauspost/compress/zstd`) en `/data/chunks/ab/cd/<hash>`.
- Una versión = manifiesto (lista de hashes y tamaños) + SHA-256 del archivo completo.
- Si un trozo ya existe, no se vuelve a guardar ni a transferir. Así, entre dos versiones solo viajan y se guardan los trozos que cambiaron.
- **A validar en Fase 0:** el ahorro depende de cuánto cambian los bytes del `.blend` entre guardados. Blender 5.0 activa *Compress* por defecto. Las primeras mediciones, entre escenas distintas, no muestran que la compresión reduzca el ahorro, pero falta medir guardados consecutivos antes de recomendar nada. Los resultados van en `docs/DECISIONES.md`.
- Escrituras atómicas (temporal, `fsync`, `rename` y `fsync` del directorio); la fila del trozo se inserta después. Antes de crear un manifiesto nuevo, el servidor reconstruye el archivo y verifica su hash, fuera de la transacción de escritura.
- Recolector de basura de trozos sin referencias solo vía comando admin y con el servidor detenido.

## 6. Reglas de negocio (implementar y testear todas)

1. Cualquier usuario puede ver y descargar cualquier versión.
2. Subir versión de un archivo existente:
   - Bloqueado por **otro** → rechazar: "Bloqueado por X desde <fecha>".
   - Bloqueado por **ti** o **sin bloqueo** → permitir.
   - `base_version` distinta de la última → rechazar con `409`. Vale también para restaurar, mover y borrar.
3. Comentario de versión obligatorio (mínimo 3 caracteres).
4. El bloqueo pertenece a un usuario y a una PC; desde otra PC se toma de forma explícita. Solo quien tiene el bloqueo lo libera. El admin puede forzar el desbloqueo (queda registrado).
5. **Restaurar** vK crea vN+1 con el mismo manifiesto (sin duplicar bytes), comentario "Restaurado desde vK". Mismas condiciones de bloqueo que subir.
6. Si el hash completo coincide con la última versión → "No hay cambios" y no se crea versión.
7. La extensión no cambia entre versiones.
8. Borrar = borrado suave y requiere tenerlo bloqueado o libre. Purga real solo por comando admin.
9. Mover/renombrar un archivo requiere tenerlo bloqueado o libre; se registra como acción y conserva el historial.
10. Toda acción queda en el registro de actividad.
11. Las rutas son únicas sin distinguir mayúsculas y tras normalizar a NFC (`path_key`). Se rechazan nombres reservados de Windows (`CON`, `NUL`…) y puntos o espacios finales.

## 7. Servidor (`3dvault-server`)

**Stack:** Go 1.27, `net/http` + `github.com/go-chi/chi/v5`, `modernc.org/sqlite` (WAL), `html/template` + HTMX, three.js para el visor, plantillas y estáticos incrustados con `embed`. Contraseñas con argon2id (`golang.org/x/crypto`). Tokens de API para el agente.

**Modelo de datos:**
```
users(id, username UNIQUE, display_name, password_hash, is_admin, is_active, created_at)
api_tokens(id, user_id, name, token_hash, created_at, last_used_at, revoked_at)
projects(id, name UNIQUE, description, created_by, created_at, archived)
assets(id, project_id, path, path_key, ext, head_version, created_by, created_at, deleted_at NULL)  UNIQUE(project_id, path_key) activos
versions(id, asset_id, number, file_sha256, size_bytes, manifest_id, comment, uploaded_by, created_at, restored_from NULL)  UNIQUE(asset_id, number)
manifests(id, file_sha256 UNIQUE, chunk_count)        -- reutilizable entre versiones idénticas
manifest_chunks(manifest_id, idx, chunk_sha256, size)  PK(manifest_id, idx), INDEX(chunk_sha256)
chunks(sha256 PK, size, stored_size, created_at)
locks(asset_id PK, user_id, locked_at, client_id, client_name)
activity(id, user_id, action, project_id, asset_id NULL, version_id NULL, detail, created_at)
events(seq PK AUTOINCREMENT, project_id, kind, asset_id, payload_json, created_at)  -- para sincronizar
meta(key PK, value)                                     -- server_epoch
```

**API principal (JSON, auth por cookie o `Authorization: Bearer`):**
```
POST /api/login · POST /api/tokens · DELETE /api/tokens/{id}
GET  /api/projects
GET  /api/projects/{id}/snapshot          → estado actual: cada archivo con su versión, hash, tamaño, bloqueo
GET  /api/projects/{id}/events?since=seq  → cambios desde un número de secuencia (polling)
GET  /api/stream?projects=1,2             → Server-Sent Events con los mismos eventos (tiempo real)
GET  /api/assets/{id}/versions
GET  /api/versions/{id}/manifest
POST /api/chunks/missing                  → recibe lista de hashes, responde cuáles faltan en el servidor
PUT  /api/chunks/{sha256}                 → sube un trozo (el servidor verifica el hash)
GET  /api/chunks/{sha256}                 → descarga un trozo
POST /api/assets                          → crear archivo nuevo (proyecto, ruta, manifiesto, comentario)
POST /api/assets/{id}/versions            → nueva versión (manifiesto + comentario); valida reglas y que existan todos los trozos
POST /api/assets/{id}/lock · DELETE /api/assets/{id}/lock
POST /api/assets/{id}/restore/{n}
POST /api/assets/{id}/move · DELETE /api/assets/{id}
GET  /api/versions/{id}/download          → archivo completo reconstruido en streaming (para la web)
```

**Reglas comunes de la API:**
- Las operaciones de escritura envían `X-Client-Id` y `base_version`.
- `POST /api/assets/{id}/versions` añade `size_bytes` y `release_lock`, que libera el bloqueo en la misma transacción.
- Los `409` devuelven `{code, head_version, lock}`.
- "No hay cambios" devuelve la versión actual, de modo que reintentar es seguro.
- `POST /api/assets/{id}/lock` acepta `takeover` y devuelve la versión actual.
- Snapshot y eventos devuelven `seq` y `server_epoch`, leídos en una sola transacción. `/api/stream` acepta `since`. Eventos ya eliminados responden `410`.

**Web (para consultar y administrar, no para el día a día):** login; proyectos; árbol de archivos con estado de bloqueo; ficha del archivo con visor 3D, historial, descargar/restaurar, bloquear/liberar; actividad; admin (usuarios, desbloqueos forzados, borrados, uso de disco); "Mi cuenta" (contraseña, tokens, enlace de descarga del agente).

**Seguridad:** solo HTTPS (Caddy); CSRF en formularios web; límite de intentos de login; validación estricta de rutas (sin `..`, sin absolutas, caracteres seguros, máximo 255 por segmento); nunca servir rutas construidas por el usuario, solo por hash; `ALLOWED_IPS` opcional.

**Comandos admin (mismo binario):**
```
3dvault-server create-admin
3dvault-server purge --keep-last 10 --older-than 180d [--apply]   # por defecto solo simula y muestra cuánto libera
3dvault-server gc [--apply]                                         # borra trozos sin referencias
3dvault-server check                                                # verifica hashes de todos los trozos
3dvault-server backup --to /ruta                                    # VACUUM INTO de la BD y después copia de trozos, sin rsync
3dvault-server restore --from /ruta                                 # restaura y regenera server_epoch
```

## 8. Sincronización (página Mi carpeta)

> **Cambio de plan (2026-11-06).** El agente `.exe` con ícono en la bandeja se reemplaza por la página **Mi carpeta** de la web. Un `.exe` sin firmar lo bloquean SmartScreen, Smart App Control o Defender, y firmarlo cuesta dinero y días de validación. El navegador (Edge viene con Windows) puede leer y escribir una carpeta elegida por el usuario. El protocolo, los trozos y las reglas de esta sección no cambian; lo que no aplica en el navegador se indica abajo. El código del agente Go queda en `internal/agent`, en pausa. Detalle en `docs/DECISIONES.md`.
>
> **No aplica en el navegador:** ícono de bandeja (en su lugar, la página se instala como aplicación y Edge la abre al iniciar sesión; la sincronización automática funciona con su ventana abierta), atributo de solo lectura, `fsnotify` (se revisa al volver a la pestaña y cada minuto), API local en 127.0.0.1 y caché de trozos (se reutilizan los de la versión local). El estado va en IndexedDB y la carpeta elegida se recuerda, con un permiso que el navegador vuelve a pedir.

### Diseño original del agente (referencia)

**Stack:** Go, `fyne.io/systray` (ícono en bandeja), `github.com/fsnotify/fsnotify`, `modernc.org/sqlite` para el estado local, panel local servido en `http://127.0.0.1:<puerto>` (html/template + HTMX) que se abre desde el ícono. Compilar para `windows/amd64` primero; `darwin/arm64` después, en un Mac, porque `systray` requiere cgo en macOS.

**Configuración inicial (asistente en el panel):** URL del servidor → usuario y contraseña (el agente obtiene un token y no guarda la contraseña) → carpeta raíz (ej. `D:\3D-Vault`) → elegir qué proyectos sincronizar (sincronización selectiva).

**Estructura local:** `D:\3D-Vault\<Proyecto>\<ruta del archivo>`. Estado en `%APPDATA%\3D-Vault\state.db` (por archivo: versión local, hash, tamaño, mtime, estado). Caché de trozos opcional con tamaño máximo configurable.

**Comportamiento:**
1. **Bajada automática:** el agente escucha `/api/stream` (SSE) y, si se corta, hace polling a `/events?since=` cada 30 s. Ante una versión nueva: pide el manifiesto, calcula qué trozos ya tiene (de la versión local de ese archivo y de la caché) y **descarga solo los que faltan**; reconstruye en un temporal, verifica el SHA-256 y reemplaza de forma atómica. Notificación: "camion_797F.blend actualizado a v8 por Juan".
2. **Solo lectura por defecto:** los archivos no bloqueados por ti quedan con atributo de solo lectura. Al bloquear, el agente los vuelve editables; al liberar, de nuevo solo lectura. (Es una ayuda visual, no seguridad real.)
3. **Detección de cambios:** con fsnotify + espera de 5 s sin cambios de tamaño/mtime, marca los archivos bloqueados por ti como "Cambios sin subir".
4. **Subida:** desde el panel, botón "Subir" con comentario obligatorio. El agente corta el archivo, pregunta `/chunks/missing`, sube solo los trozos nuevos (en paralelo, máx. 4) y confirma la versión. Reanudable: si se corta, al reintentar solo faltan los trozos no subidos.
5. **Archivos nuevos:** los que aparecen en la carpeta y no existen en el servidor se listan como "Nuevos sin subir"; el usuario los agrega con comentario.
6. **Conflictos:** si un archivo no bloqueado por ti cambió localmente y llega una versión nueva, el agente renombra tu copia a `nombre.conflicto-<usuario>-<fecha>.ext`, descarga la versión del servidor y avisa. Nunca sobrescribe trabajo local sin dejar copia.
7. **Ignorar:** `*.blend[0-9]*`, `*.blend@`, `*.conflicto-*`, `.3dvault-tmp\`, `*.tmp`, `~*`, `.DS_Store`, `Thumbs.db` y patrones de un archivo `.3dvaultignore` en la raíz del proyecto.
8. **Archivo abierto en Blender:** Blender no mantiene el archivo abierto, así que el reemplazo funciona; el agente avisa: "Si lo tienes abierto, usa Archivo → Revertir".
9. **Primera sincronización:** descarga todo lo seleccionado, mostrando progreso y tamaño total antes de empezar.
10. **Nunca pisar trabajo local:** justo antes de reemplazar, recalcula el hash. Solo reemplaza si el archivo no existe o coincide con la base registrada; si no, crea la copia `*.conflicto-*`. Vale también para borrados que llegan del servidor y desbloqueos forzados.
11. **Sin puntos ciegos:** escaneo al iniciar y periódico, porque `fsnotify` no vigila carpetas de forma recursiva y puede perder eventos.
12. **Estado consistente:** los eventos son solo avisos. El cursor y la versión deseada se guardan en una sola transacción de `state.db`. Si `server_epoch` no coincide, pide un snapshot completo.
13. **Reemplazo en Windows:** quita solo lectura antes de reemplazar y reintenta si el archivo está en uso. Los temporales van en `.3dvault-tmp\`, en el mismo volumen, con comprobación previa de espacio.
14. **Borrados y movimientos locales:** nunca se propagan. El archivo aparece como "Falta", con opciones de recuperar o borrar en el servidor.
15. **Aviso al bloquear:** si el archivo cambió hace poco, recuerda usar Archivo → Revertir, porque Blender podría tener una versión anterior en memoria.

**Panel local (pantallas):** Estado (sincronizado / descargando / errores); Mis bloqueos (con "Subir" y "Liberar"); Cambios pendientes; Nuevos sin subir; Buscar archivo y bloquear; Conflictos; Proyectos sincronizados; Ajustes (carpeta, caché, iniciar con Windows).

**Menú del ícono:** Abrir panel · Abrir carpeta 3D-Vault · Pausar sincronización · Salir. Ícono con estado (verde sincronizado, azul transfiriendo, naranja cambios pendientes, rojo error).

**API local** en 127.0.0.1 (para el panel y el futuro complemento de Blender): estado de un archivo por ruta local, bloquear, subir, liberar. Solo escucha en localhost, comprueba la cabecera `Host` y exige un token local aleatorio guardado en el perfil del usuario. El panel se abre con un enlace de un solo uso.

## 9. Vistas previas

- `.glb`, `.gltf`, `.fbx`, `.obj`, `.stl` → visor three.js en la web (rotar, zoom, encuadrar; aviso si supera 200 MB). Un `.gltf` u `.obj` con archivos externos muestra la geometría con un aviso.
- `.blend` → miniatura embebida (bloque `TEST`, presente con *Save Preview Images*). Parser en Go, sin Blender, compatible con zstd y con el formato anterior y el de Blender 5.0. Si no hay, ícono genérico.
- Imágenes → se muestran directamente.
- Las miniaturas se generan en segundo plano al confirmar una versión.

## 10. Estructura del repositorio

```
3d-vault/
  cmd/3dvault-server/   cmd/3dvault-sync/   cmd/cdcmeasure/
  internal/
    chunker/   manifest/   store/      (trozos en disco)
    db/        auth/       rules/      (reglas de negocio puras, muy testeadas)
    server/    (rutas API + web)       events/   thumbs/ (incluye parser .blend)
    client/    (protocolo, compartido por push/pull y el agente)
    agent/     (sync, watcher, transfer, conflicts, localapi, panel, tray)
  web/server/  web/agent/              (plantillas y estáticos, embebidos)
  deploy/  Dockerfile  docker-compose.yml  Caddyfile  .env.example
  docs/  GUIA_USUARIO.md  ADMIN.md  DECISIONES.md
  Makefile   (build-server, build-agent-windows, build-agent-mac, test)
```

## 11. Configuración (`.env` del servidor)

```
SECRET_KEY=
DATA_DIR=/data
DOMAIN=3dvault.miempresa.com
MAX_FILE_SIZE_GB=20
ALLOWED_IPS=
SESSION_HOURS=12
```

## 12. Fases y criterios de aceptación

### Fase 0 — Validar el ahorro
`internal/chunker` y `cmd/cdcmeasure`. Medir guardados consecutivos reales: con y sin *Compress*, en la misma sesión de Blender y en sesiones distintas. Comprobar si un archivo conserva su opción de compresión al volver a guardarlo.

**Aceptación:**
- Resultados en `docs/DECISIONES.md`.
- Umbral propuesto: al menos 50 % de reutilización sin *Compress* para seguir tal cual. Si no se alcanza, se decide antes de continuar.

### Fase 1 — Servidor núcleo
Auth y tokens, proyectos, almacenamiento por trozos, manifiestos, versiones, bloqueos, restaurar, mover, borrado suave, actividad, eventos (polling + SSE), web básica (árbol, ficha, historial, admin de usuarios), Docker Compose con Caddy, herramienta de prueba `3dvault-sync push/pull` en modo comando para validar el protocolo, `backup` y `restore`.

**Aceptación:**
- `docker compose up -d` levanta todo con HTTPS.
- Todas las reglas de la sección 6 con tests (`go test ./...` en verde).
- Subir dos versiones de un `.blend` real de >1 GB: la segunda solo transfiere los trozos nuevos y se reporta el % reutilizado.
- Una subida cortada a la mitad se completa al reintentar sin repetir trozos.
- Dos subidas simultáneas desde la misma versión: una gana y la otra recibe `409`, nunca dos con el mismo número.
- Backup restaurado en un entorno limpio y validado con `check`.
- Coste de verificación medido con un archivo de 20 GB en el VPS. Si es excesivo, se verifica en segundo plano y la versión no se publica hasta terminar.

### Fase 2 — Sincronización desde el navegador
Página Mi carpeta: elegir carpeta, sincronización selectiva por proyecto, bajada automática por trozos (SSE con la pestaña abierta, y revisión cada minuto), bloquear/subir/liberar, nuevos sin subir, conflictos, ignorados, una sola pestaña sincronizando. Sin instalador.

**Aceptación:**
- PC A sube v2; PC B la tiene en su carpeta en menos de 1 minuto sin hacer nada.
- PC B no puede subir un archivo bloqueado por A.
- Provocar un conflicto deja la copia `*.conflicto-*` y la versión del servidor, sin pérdida.
- Pausar y reanudar la sincronización funciona; cerrar y reabrir el navegador conserva el estado.
- Editar un archivo con el agente apagado y recibir una versión nueva al encenderlo no pierde nada.
- Renombrar un archivo en el Explorador no bifurca su historial.
- Antes de pasar al equipo: guías escritas y backups automáticos fuera del VPS.

### Fase 3 — Vistas previas y mantenimiento
Visor 3D, miniaturas `.blend`, `purge`, `gc`, `check`, uso de disco por proyecto. macOS queda cubierto por Chrome.

### Fase 4 — Opcional
Primero, el complemento de Blender (panel: estado, bloquear, subir con comentario, liberar; aviso al guardar si hay una versión más nueva). Sin agente local, hablaría con el servidor directamente. Menú contextual en el Explorador de Windows. Superposición de íconos de estado.

## 13. Fuera de alcance (por ahora)

Fusión de archivos, permisos por proyecto, LDAP/SSO, almacenamiento en la nube, archivos "bajo demanda" (placeholders tipo OneDrive), comentarios por versión.

## 14. Instrucciones para Claude Code

- Trabaja fase por fase; al terminar cada una: tests en verde, binarios compilando para los destinos indicados, README actualizado.
- Empieza la Fase 1 por `internal/db`, `internal/manifest`, `internal/store` y `internal/rules`, con tests, antes de la API.
- Código idiomático y legible; manejo de errores explícito; contextos con timeout en red.
- Todo texto visible en español claro.
- Escribe `docs/GUIA_USUARIO.md` (modeladores, sin jerga) y `docs/ADMIN.md` (instalación, usuarios, backups, restauración, purga, actualización del agente).
