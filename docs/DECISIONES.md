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

## Cambio de plan: sincronización desde el navegador (2026-11-06)

**Problema.** El responsable del proyecto advirtió que un `.exe` repartido a los modeladores deja de abrirse a los pocos días. Un binario sin firmar choca con SmartScreen, con Smart App Control en Windows 11 (bloquea sin opción de saltarlo) y con falsos positivos de Defender, frecuentes en binarios Go. Firmarlo exige un certificado de pago y una validación de identidad de varios días, y los usuarios no saben usar la terminal.

**Decisión.** La sincronización pasa a la web: la página **Mi carpeta** (`/sync`) usa la File System Access API de Edge y Chrome para leer y escribir una carpeta que el usuario elige una vez. No hay nada que instalar ni que Windows pueda bloquear. Se eligió entre tres opciones: carpeta conectada en la web (elegida), solo subir y bajar archivos sueltos desde la web, o seguir con el `.exe` firmado.

**Qué se conserva.** El protocolo, el corte en trozos y las reglas del servidor no cambian. El JavaScript corta con el mismo FastCDC: solo usa los 32 bits bajos de la tabla, porque las máscaras miran los 22 bits bajos y en `(fp<<1)+g` los acarreos solo suben, así que los cortes son idénticos a los de Go. `TestGoldenCutsWeb` en Go y `web/sync_test.mjs` fijan la misma huella sobre los mismos datos.

**Qué se pierde:**

- La actualización automática solo ocurre con la pestaña abierta. Con ella cerrada, al reabrir se pone al día.
- Sin atributo de solo lectura. Lo compensan el bloqueo, que el servidor exige, y la copia `*.conflicto-*`.
- Solo Edge y Chrome (también en macOS). Firefox y Safari no tienen la API.
- El navegador vuelve a pedir permiso para la carpeta en cada sesión, salvo que el usuario elija "Permitir siempre".

El agente Go (`internal/agent`, sin tests ni interfaz) queda en pausa, no se borra: sirve si algún día se firma un `.exe`.

## Prueba de punta a punta de la Fase 2 (2026-11-06)

`make e2e`: dos Chromium sin pantalla, con usuarios distintos y cada uno con su carpeta (OPFS, la misma API que una carpeta local), contra el servidor real.

| Prueba | Resultado |
|---|---|
| A agrega un archivo de 12 MiB | v1 creada |
| B marca el proyecto | Lo recibe con el mismo SHA-256 |
| A bloquea | B ve el bloqueo sin hacer nada |
| A cambia 64 KiB en medio y sube | v2, 90 % reutilizado |
| B recibe v2 sin hacer nada | En 0,4 s, bajando 1 trozo |
| B edita sin bloqueo e intenta subir | "Bloqueado por Guto desde…" |
| A sube v3 mientras B tiene cambios locales | B conserva `camion.conflicto-ana-<fecha>.blend` y recibe v3 |
| B borra el archivo localmente | No se propaga. Queda "No está en tu carpeta" y se recupera con Descargar |
| B recarga la página | Estado conservado, 0 trozos descargados |

Velocidad del análisis en JavaScript (Node, esta laptop): corte 321 MiB/s, SHA-256 244 MiB/s, análisis completo 143 MiB/s, unos 15 s para un `.blend` de 2 GB.

**Sin probar:** Edge en Windows con una carpeta real (el selector no se puede automatizar), archivos de varios GB en el navegador y la pantalla en un teléfono, que no tiene la API.

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
| "Mi cuenta" y actividad no están en la Fase 1; `purge` y `gc` llegaron en la Fase 3 | No forman parte de su alcance. El admin cambia contraseñas desde Usuarios |
| HTMX y Geist incluidos en el binario, sin CDN | Sistema interno. HTMX 2.0.4 y Geist con licencia OFL |
| El producto se llama 3D-Vault | Petición del usuario. Binarios `3dvault-server` y `3dvault-sync` |
| Módulo Go `3d-vault` | Aún no hay repositorio remoto; cambiará cuando exista |
| Go 1.27 | Es la versión estable actual |
| Estructura de la sección 10 de `PLAN.md`, sin librería de inyección de dependencias | La especificación ya fija la estructura y pide lo más simple: constructores manuales |
| FastCDC adaptado en `internal/chunker`, sin depender de `jotfs/fastcdc-go` | Licencia Apache 2.0. Última versión de 2020 y un error de concurrencia abierto: modifica una tabla global al crear cada instancia |
| Parámetros fijos y sin semilla en FastCDC | La semilla protege frente a ataques de huella por tamaño de trozo, algo que no aplica a un sistema interno. Los cortes quedan fijados por `TestGoldenCuts` |
| El aviso de *Compress* en el agente queda en suspenso | Las primeras mediciones no justifican avisar. Se decide con los guardados consecutivos |
| Sincronización en el navegador con File System Access API, en lugar del agente `.exe` | Ver "Cambio de plan". Sin instalar nada, sin firma |
| JavaScript propio sin dependencias ni compilación: `core.js` (corte y SHA-256), `engine.js` (sincronización, sin DOM) y `app.js` (interfaz) | Pocas líneas. `engine.js` funciona con cualquier carpeta del navegador, lo que permite probarlo con OPFS |
| SHA-256 incremental en JavaScript para el archivo completo y nativo (`crypto.subtle`) para cada trozo | `crypto.subtle` no es incremental y un `.blend` no cabe en memoria |
| Estado local en IndexedDB: versión, hash, tamaño y fecha de cada archivo. Un archivo con el mismo tamaño y fecha se da por igual | Igual que el agente. Si cambia, se recalcula el hash antes de decidir |
| Al bajar una versión se copian de la copia local los trozos de la versión anterior, según el manifiesto del servidor | No hace falta releer la copia local para saber qué tiene. Si el resultado no coincide, se baja completo. Si `purge` borró esa versión, también |
| Una sola pestaña sincroniza, con Web Locks | Dos pestañas bajarían el mismo archivo a la vez |
| El bloqueo pertenece a esta pestaña del navegador: un id aleatorio en `localStorage` hace de PC | La regla de bloqueo por PC sigue valiendo. El nombre de la PC se puede cambiar en Ajustes |
| Un archivo que desaparece del servidor se borra de la carpeta solo si no cambió localmente; si cambió, queda como "Nuevo, sin subir" | Nunca se pierde trabajo local |
| Un movimiento en el servidor se ve como borrado más archivo nuevo, y se baja completo | Es raro y no pierde nada. Optimizarlo puede esperar |
| `.3dvaultignore` no se implementa en el navegador | Los patrones fijos cubren los casos conocidos. Se añade si hace falta |
| `[hidden]` con `display: none !important` en `app.css` | Las clases de la web fijan `display` y anulaban el atributo |
| Simulación de `purge` y `gc`: la misma transacción, deshecha al final | La simulación coincide siempre con lo real |
| `purge` conserva la versión actual, las N más nuevas y las posteriores al corte. A un archivo borrado antes del corte le quita todas | Reglas de `PLAN.md`. La lista se fija en una tabla temporal antes de borrar |
| La fila de un archivo purgado se conserva, sin versiones | Los eventos y la actividad apuntan a ella |
| Un manifiesto solo se borra si ninguna versión lo usa | Protege a las versiones restauradas, que comparten el de su origen |
| `gc` borra primero las filas y después los archivos | Si se corta, quedan archivos sin fila, inofensivos, que recoge el siguiente `gc` |
| `purge` y `gc` solo con el servidor detenido | Una subida en curso tiene trozos que aún no referencia ningún manifiesto |

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

- **Versión vieja en la memoria de Blender:** B tiene v7 abierta, Mi carpeta instala v8, B bloquea y guarda. Se sube una v9 sin los cambios de v8. La página avisa al bloquear, pero solo el complemento de Blender puede cerrarlo del todo. Por eso es lo primero de la Fase 4.
- **Pestaña cerrada:** quien cierre Mi carpeta trabaja con archivos que pueden quedar atrasados. El bloqueo lo protege: al bloquear, la página baja la última versión antes de que edite.
- **Agentes muy atrasados y `purge`:** una versión purgada ya no tiene manifiesto. Mi carpeta lo tolera bajando el archivo completo.
