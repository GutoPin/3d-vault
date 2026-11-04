# Decisiones de 3D-Vault

Registro de decisiones, cambios a la especificación original y resultados de las mediciones.

## Estado de la Fase 0

**Abierta, sin bloquear.** Falta medir guardados consecutivos reales de un mismo archivo. El responsable del proyecto pidió avanzar a la Fase 1 sin esa medición (2026-11-04). El diseño funciona con cualquier resultado; lo que queda sin confirmar es cuánto se ahorra entre versiones.

## Mediciones de reutilización

Herramienta: `cmd/cdcmeasure`. "A transferir" es lo que habría que enviar del segundo archivo si el servidor ya tuviera el primero.

### Escenas distintas (2026-11-04)

**Esto no es la medición pendiente.** Son pares de escenas distintas de `~/Descargas`, no guardados consecutivos. Es probable que compartan recursos pesados y se desconoce cómo se generaron. Son una primera señal, no el resultado esperado entre versiones.

| Par (base → nuevo) | Tamaño del nuevo | Reutilizado | A transferir |
|---|---|---:|---:|
| `ESCENA5` → `ESCENA_TOPADORA`, comprimidos | 1.39 GiB | 85.0 % | 213.79 MiB |
| `ESCENA5` → `ESCENA_TOPADORA`, descomprimidos | 2.23 GiB | 76.2 % | 542.85 MiB |
| `tajo-intro-aoz` → `TAJO One Way escena imagen`, comprimidos | 1.92 GiB | 96.1 % | 75.85 MiB |
| `tajo-intro-aoz` → `TAJO One Way escena imagen`, descomprimidos | 1.98 GiB | 93.8 % | 124.97 MiB |

Observaciones:

- **La hipótesis original no se cumple en estos pares.** La especificación suponía que con *Compress* casi no habría ahorro. Aquí la versión comprimida reutiliza un porcentaje mayor y transfiere menos bytes.
- **Repetición dentro de cada archivo (corrección).** Una versión anterior de este registro decía que era 0.0 %, pero la herramienta atribuía a la base todo lo que ya había visto. Medida por separado, es alta y muy variable: `ESCENA_TOPADORA` 42.0 %, `ESCENA5` 35.6 % (38.4 % descomprimido), `tajo-intro-aoz` 5.6 %, `TAJO One Way` 0.2 %. Parte del porcentaje de los dos primeros pares se debe a esto. La columna "A transferir" sí es correcta.
- **Estructura de los archivos:** `zstd -l` muestra miles de marcos independientes, unos 2 000 a 3 300 por archivo, más un marco de salto. La cantidad es compatible con que un cambio local no altere todo el archivo, pero no se ha comprobado que sea la causa.
- **Datos ya comprimidos:** los dos TAJO apenas se reducen con zstd (ratio 1.03 a 1.04), lo que sugiere que predominan datos ya comprimidos. Tampoco se ha comprobado su contenido.
- **Velocidad:** cortar y calcular SHA-256 va a unos 230 a 460 MiB/s en esta máquina. Es orientativo; no incluye descomprimir trozos ni el disco del VPS.

Versiones: las cabeceras de los cuatro archivos son `BLENDER17-01v0501` (2) y `BLENDER17-01v0502` (2), con el formato introducido en Blender 5.0. El equipo usa Blender 5.1.

### Guardados consecutivos

Pendiente. Hace falta medir, con y sin *Compress*:

- Dos guardados en la misma sesión de Blender.
- Dos guardados en sesiones distintas, porque los punteros de memoria guardados pueden variar.
- Una edición de geometría, y no solo de textos o ajustes.

Hasta tener estos datos no se recomienda activar ni desactivar *Compress*. Desactivarlo tiene un coste real: `ESCENA5` pasa de 1.67 a 3.00 GiB en cada PC.

## Prueba de punta a punta de la Fase 1 (2026-11-04)

Binarios nativos en la laptop de desarrollo, sin Docker ni HTTPS, con prioridad mínima y 2 núcleos, para no sobrecargarla. Los datos de prueba se borraron después.

| Prueba | Resultado |
|---|---|
| `ESCENA_TOPADORA` (1.39 GiB) como v1 | Envió 825 MB y tardó 10 s. El resto eran trozos repetidos dentro del archivo |
| `ESCENA5` (1.67 GiB) como v2 | Envió 488 MB, 71.4 % reutilizado |
| Repetir la subida sin cambios | "No hay cambios", 0 trozos enviados |
| `tajo-intro-aoz` (2.10 GiB) cortada a mitad de subida | Cortada con 409 trozos confirmados. El reintento envió solo lo que faltaba y creó v1 |
| Bajar v2 sobre una copia local de v1 | Descargó 579 MB de 1.67 GiB. SHA-256 idéntico al original |
| Bajar sobre un archivo local distinto sin `--overwrite` | Rechazado, archivo intacto |
| `check` con el servidor activo | 2 894 trozos, 0 corruptos, 0 faltantes, 6.7 s |
| `backup` en caliente, `restore` en carpeta limpia y `check` | Correcto, 0 corruptos, 0 faltantes |
| `restore` con el servidor activo | Rechazado |

v2 no es un guardado real de v1: son dos escenas distintas, así que esto valida el protocolo, no el ahorro entre versiones.

**Pendiente de la aceptación de la Fase 1:**

- `docker compose up -d` con HTTPS real. Los archivos de `deploy/` están escritos pero no se han ejecutado: construir la imagen se deja para el VPS, porque es pesado para la laptop.
- Coste de verificación de un archivo de 20 GB en el VPS.
- Guardados consecutivos reales (Fase 0).

## Web

Estilo `achromatic-ledger` de la skill `design-taste-frontend` (semilla 1914576395), sin rediseños. Rasgo propio: fondo gris azulado de libro de registro y, en cada versión, cuánto cambió respecto a la anterior, único dato con color.

Revisada con capturas de Firefox a 1440 y 390 px. El inspector de la skill no se pudo ejecutar porque requiere Chrome, y el lint da 0 FAIL. La búsqueda con HTMX y el menú de teléfono no se han probado de forma interactiva; las rutas y formularios sí tienen tests HTTP.

## Decisiones

| Decisión | Motivo |
|---|---|
| Nuevo paquete `internal/vault`, además de los de la sección 10 | La lógica de negocio con estado necesita un sitio sin HTTP, para probar la concurrencia directamente |
| `database/sql` y migraciones propias, sin `sqlx` ni `golang-migrate` | La especificación pide pocas dependencias. Son 30 líneas con `PRAGMA user_version`, y el esquema es el de `PLAN.md` |
| CSRF con `http.CrossOriginProtection`, más `SameSite=Lax` | Viene en la biblioteca estándar, sin tokens que mantener |
| Sesión firmada e incluyendo el hash de la contraseña | Cambiar la contraseña cierra las sesiones sin una tabla de sesiones |
| Cambiar una contraseña revoca también los tokens | Si se cambia, es porque la anterior pudo verse comprometida |
| Cualquier usuario puede crear proyectos | No hay permisos por proyecto (sección 13) |
| Liberar un bloqueo propio se permite desde cualquier PC; subir, solo desde la PC del bloqueo | Liberar no pisa trabajo: lo protege el agente. Subir desde otra PC sí podría |
| "No hay cambios" se evalúa antes que la versión de partida | El reintento de una subida ya confirmada debe tener éxito |
| Los trozos llevan un byte de formato | El contenido de un `.blend` comprimido también empieza por la firma de zstd |
| `POST /api/login` con `token_name` devuelve un token | El agente no guarda contraseña. Es una ampliación de la API de `PLAN.md` |
| `POST /api/projects`, `GET /api/me` y `POST /api/logout` añadidos | Hacen falta y `PLAN.md` no los listaba |
| "Mi cuenta", actividad, `purge` y `gc` no están en la Fase 1 | No forman parte de su alcance. El admin cambia contraseñas desde Usuarios |
| HTMX y Geist incluidos en el binario, sin CDN | Sistema interno. HTMX 2.0.4 y Geist con licencia OFL |
| El producto se llama 3D-Vault | Petición del usuario. Binarios `3dvault-server` y `3dvault-sync` |
| Módulo Go `3d-vault` | Aún no hay repositorio remoto; cambiará cuando exista |
| Go 1.27 | Es la versión estable actual |
| Estructura de la sección 10 de `PLAN.md`, sin librería de inyección de dependencias | La especificación ya fija la estructura y pide lo más simple: constructores manuales |
| FastCDC adaptado en `internal/chunker`, sin depender de `jotfs/fastcdc-go` | Licencia Apache 2.0. Última versión de 2020 y un error de concurrencia abierto: modifica una tabla global al crear cada instancia |
| Parámetros fijos y sin semilla en FastCDC | La semilla protege frente a ataques de huella por tamaño de trozo, algo que no aplica a un sistema interno. Los cortes quedan fijados por `TestGoldenCuts` |
| El aviso de *Compress* en el agente queda en suspenso | Las primeras mediciones no justifican avisar. Se decide con los guardados consecutivos |

## Cambios a la especificación

Ya incorporados en `PLAN.md`.

| Problema | Cambio |
|---|---|
| Una subida sin bloqueo basada en una versión vieja desplaza trabajo más nuevo | `base_version` obligatorio al subir, restaurar, mover y borrar; `409` si no es la última |
| El mismo usuario en dos PCs: ambas quedan editables | El bloqueo pertenece a usuario y PC; en otra PC se usa "tomar el bloqueo" explícito |
| Cualquiera puede borrar un archivo bloqueado por otro | Borrar exige las mismas condiciones de bloqueo que mover |
| `Camion.blend` y `camion.blend` chocan en Windows; los acentos difieren entre macOS y Windows | `path_key` único (NFC, sin mayúsculas). Se rechazan nombres reservados y puntos o espacios finales |
| Un manifiesto erróneo queda reutilizado para siempre por `file_sha256 UNIQUE` | El servidor verifica el hash antes de crear un manifiesto nuevo |
| `gc` compite con subidas en curso | `gc` y `purge` no corren con el servidor activo |
| El backup podía dejar referencias rotas y llegaba tarde | `VACUUM INTO` primero y copia de trozos después, en Go y sin `rsync`. Se adelanta a la Fase 1 |
| Tras restaurar un backup, `seq` retrocede | `server_epoch` regenerado al restaurar; si no coincide, snapshot completo |
| El agente podía pisar ediciones hechas con el agente apagado | Recalcular el hash antes de reemplazar y escanear al iniciar y de forma periódica |
| Renombrar en el Explorador bifurcaría el historial | Los borrados y movimientos locales no se propagan; el archivo aparece como "Falta" |

## Riesgos abiertos

- **Versión vieja en la memoria de Blender:** B tiene v7 abierta, el agente instala v8, B bloquea y guarda. Se sube una v9 sin los cambios de v8. El agente avisa al bloquear, pero solo el complemento de Blender puede cerrarlo del todo. Por eso es lo primero de la Fase 4.
