# Decisiones de 3D-Vault

Registro de decisiones, cambios a la especificación original y resultados de las mediciones.

## Estado de la Fase 0

**Cerrada con una primera medición (2026-11-06).** Guardados consecutivos reales con Blender 5.2 (ver "Guardados consecutivos"). Conclusión: conviene guardar **sin Comprimir**. Queda confirmarlo con archivos reales del equipo, en Blender 5.1.

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

### Guardados consecutivos (2026-11-06)

Blender 5.2.3 (módulo `bpy` de PyPI) en esta laptop, sin interfaz. Escena sintética: 14 monos con subdivisión 4 aplicada. Cada guardado parte del primero; "otra sesión" es abrir el primero en un proceso nuevo y guardarlo sin cambios. Medido con `cdcmeasure`.

| Cambio respecto al primer guardado | Comprimido (67,7 MiB) | Sin comprimir (162,1 MiB) |
|---|---:|---:|
| Mover un objeto | 2,6 % a transferir | 0,7 % |
| Editar 2 000 vértices de un objeto | **96,7 %** | **1,4 %** |
| Guardar en otra sesión, sin cambios | 48,2 % | 10,1 % |

- **Con *Compress*, una edición de geometría cambia casi todo el archivo comprimido** desde ese punto. Sin comprimir, solo los trozos de ese objeto. Es la diferencia que decide el uso del disco del VPS: con versiones frecuentes, los archivos comprimidos ocupan casi su tamaño completo por versión.
- El servidor guarda los trozos comprimidos con zstd, así que desactivar *Compress* no aumenta el espacio en el VPS por versión; sí ocupa unas 2,4 veces más en cada PC y en la primera subida.
- Abrir y guardar en otra sesión cambia el 10 % sin comprimir: los punteros guardados cambian entre sesiones.
- **Límites:** escena sintética, una sola edición, Blender 5.2 y no 5.1. Falta repetirlo con un archivo real del equipo y una edición típica.

**Decisión:** se recomienda guardar sin Comprimir. El complemento de Blender lo detecta (cabecera zstd) y ofrece "Guardar sin comprimir"; la guía lo explica. No se fuerza: es una opción de cada archivo.

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

**Complemento de Blender** (`make test-blender-e2e`, Blender 5.2 sin interfaz contra el servidor): sin permiso de red no se conecta y dice dónde activarlo; agregar crea la v1; bloquear usa la identidad de Mi carpeta; guardar sin comprimir; guardar y subir crea la v2; otra usuaria sube la v3 y "Actualizar" deja el archivo idéntico y con su cilindro al abrirlo; bloquear lo que tiene otra falla con su nombre; con cambios locales, actualizar guarda la copia `*.conflicto-*`. Sin probar: el panel y las ventanas emergentes con interfaz, Windows, y Blender 5.1.

**Sin probar:** arrastrar y soltar con un ratón de verdad (la copia que hace sí tiene prueba), instalar como aplicación y abrirla al iniciar sesión, los avisos de escritorio, Edge en Windows con una carpeta real (el selector no se puede automatizar), archivos de varios GB en el navegador y la pantalla en un teléfono, que no tiene la API.

## Web

Estilo `achromatic-ledger` de la skill `design-taste-frontend` (semilla 1914576395), sin rediseños. Rasgo propio: fondo gris azulado de libro de registro y, en cada versión, cuánto cambió respecto a la anterior, único dato con color.

Revisada con capturas de Firefox a 1440 y 390 px. El inspector de la skill no se pudo ejecutar porque requiere Chrome, y el lint da 0 FAIL. La búsqueda con HTMX y el menú de teléfono no se han probado de forma interactiva; las rutas y formularios sí tienen tests HTTP.

## Sincronización selectiva (2026-11-12)

Pedido del equipo: las laptops tienen un solo disco y el proyecto ya ocupa 11 GB, camino de 40–50 GB. Cada modelador necesita los assets comunes y sus escenas, no las de los demás. Se tomó como modelo la "sincronización selectiva" de Diversion, que el equipo ya conoce.

| Decisión | Motivo |
|---|---|
| La elección es por PC y por proyecto, en `localStorage`, no en el servidor | Es una decisión sobre el disco de esa PC. La misma persona puede querer cosas distintas en la laptop y en la PC de escritorio |
| Se guardan las carpetas **excluidas**, no las incluidas | Una carpeta nueva que crea otro se baja por defecto: nadie se queda sin un asset nuevo sin darse cuenta |
| Al excluir, un archivo se borra de la PC solo si es igual al del servidor y no está bloqueado por ti; si no, se queda y sigue sincronizándose, con aviso | Desmarcar una carpeta nunca pierde trabajo |
| Excluir no se interpreta como borrar: la lógica de carpetas vacías y la de borrados confirmados ignoran las carpetas excluidas, y el registro local del archivo se quita a la vez que el archivo | Si no, quitar una carpeta de la PC la borraría del servidor. La prueba lo comprueba con una carpeta vacía |
| La primera vez que se marca un proyecto se eligen las carpetas antes de bajar nada | Si no, la laptop se llenaría primero y se liberaría después |
| Las carpetas excluidas que quedan vacías se quitan de la PC; si tienen algo (cambios, copias de conflicto), se quedan | Liberar espacio sin tocar lo que el usuario dejó ahí |
| Si se renombra en la web una carpeta excluida, la exclusión no la sigue y se baja con su nombre nuevo | Caso raro. Se vuelve a desmarcar |
| El helper `el()` de Mi carpeta pone como atributo cualquier clave con guion (`aria-*`, `data-*`) | `aria-label` asignado como propiedad no llegaba al DOM: las casillas del árbol quedaban sin nombre accesible |

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
| El complemento avisa si el archivo está comprimido y ofrece guardarlo sin comprimir | Ver "Guardados consecutivos": comprimido, una edición de geometría sube casi todo el archivo |
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
| Mi carpeta se puede instalar como aplicación (manifiesto, sin service worker) y Edge la abre al iniciar sesión | Es lo más parecido al agente de bandeja sin instalar un `.exe`. `launch_handler: focus-existing` evita una segunda ventana, que chocaría con el candado de pestaña |
| Arrastrar y soltar copia a la carpeta del proyecto y luego sube con un solo comentario | Usa `getAsFileSystemHandle`, que conserva las carpetas. Si el destino existe, pregunta antes de reemplazar |
| Subir todo usa un comentario para todos los archivos y los sube de uno en uno | Más simple de seguir y de reintentar que en paralelo |
| Avisos de escritorio solo con la ventana en segundo plano | Con la página a la vista ya se ve en la actividad |
| `syncAll` es una cola: cada llamada espera una pasada que empezó después | Quien acaba de copiar archivos (soltar) necesita verlos en la siguiente pasada |
| Al entrar, `?next=` devuelve a la página pedida; solo rutas de este sitio | La app abre `/sync` y, si la sesión venció, debe volver ahí. `localPath` rechaza `//` y `\\` para no redirigir a otro sitio |
| `SESSION_HOURS=168` en `.env.example` | Quien abre la app al iniciar Windows no debería escribir la contraseña cada día. Sigue siendo configurable |
| Miniaturas en caché por SHA-256 del contenido, en `DATA_DIR/thumbs`, fuera del backup | Restaurar o repetir contenido no regenera nada, y se reconstruyen solas |
| El parser de `.blend` lee como máximo 8 MiB y se rinde en GLOB, DNA1 o ENDB. Soporta zstd, gzip, cabecera antigua y la de Blender 5.0 (`LargeBHead8`, del código de Blender) | La miniatura va al principio del archivo |
| Una miniatura a la vez, en segundo plano al subir, y bajo demanda si falta | No compite con las subidas |
| Imágenes inline solo PNG, JPEG y WebP. SVG nunca | Un SVG puede llevar scripts |
| three 0.170.0 incluido en el binario, con los `from 'three'` reescritos a rutas relativas | Sin CDN y sin relajar la CSP para scripts. La CSP solo añade `data:` y `blob:` en imágenes y conexiones, que necesitan los cargadores de `.glb` y `.fbx` |
| Sin miniatura en la lista del proyecto | Probada: descuadraba los nombres y a 32 px no se distinguía |
| Complemento de Blender sin agente local: habla con el servidor con un token | No hay programa instalado con el que hablar. El código de conexión lo genera Mi carpeta con la sesión del navegador, así Blender no ve la contraseña |
| El código lleva la identidad de PC (`X-Client-Id`) de Mi carpeta | Un bloqueo hecho en Blender vale en Mi carpeta y al revés: son la misma PC |
| El complemento corta los trozos en Python con numpy (incluido en Blender), con los mismos cortes que Go y JavaScript | Una ventana de 22 bytes calculada por duplicación en 5 pasadas: 85 MiB/s, unos 25 s para 2 GB. `test_vault.py` fija la misma huella y lo compara con FastCDC byte a byte |
| Subir desde Blender exige tener el bloqueo, y la versión base es la última | Blender no sabe qué versión bajó Mi carpeta. Con el bloqueo nadie pudo subir encima |
| El estado en Blender compara el SHA-256 del archivo con el historial del servidor | Distingue "al día", "versión anterior" (se puede actualizar copiando trozos) y "cambios sin subir" (se guarda copia de conflicto antes de actualizar) |
| Las tareas de red del complemento van en un hilo y un temporizador de Blender las recoge | No congela Blender. En segundo plano (pruebas) son síncronas |
| El complemento respeta "Permitir acceso en línea" de Blender | Lo exige Blender para las extensiones |
| `/blender/3dvault.zip` se arma desde los archivos incluidos en el binario | El complemento siempre corresponde a la versión del servidor |
| Licencia del complemento: GPL-3.0-or-later | Usa la API de Blender, que es GPL. Solo afecta al complemento |
| Un archivo nuevo con el mismo contenido que uno que falta es un renombrado; se ofrece "Mover en el servidor" | Cumple "renombrar en el Explorador no bifurca el historial" sin propagar nada solo |
| "Pedir que lo libere": evento `unlock_requested` y registro de actividad, sin estado nuevo | Llega al momento por el mismo canal SSE. Una petición por persona y archivo cada 5 minutos, comprobada en la actividad: sin estado en memoria y sobrevive a reinicios |
| Recordatorio de bloqueos propios de más de un día en Mi carpeta | Un bloqueo olvidado frena a todo el equipo. Se calcula en el navegador con las filas que ya tiene |
| En Blender, "Abrir aparte" baja una versión a la carpeta temporal y la abre en otra instancia de Blender | Comparar versiones sin tocar el archivo de trabajo ni la carpeta sincronizada. Reutiliza los trozos del archivo local |
| El complemento compara su versión con `/blender/version` una vez por sesión | Avisa de actualizaciones sin un sistema de actualizaciones propio |
| La prueba del complemento ejecuta los `draw` del panel con una maqueta de layout que valida íconos y operadores | Sin interfaz, un nombre de ícono u operador equivocado solo fallaría en la Blender real |
| "Arreglar texturas" en Blender: copia a `texturas/` del proyecto las imágenes de fuera, pone rutas relativas y sube las nuevas | Cada PC tiene la carpeta raíz en otro sitio: una ruta absoluta o una imagen en Descargas se ve rosa en las demás. Si ya existe una textura con ese nombre y otro contenido, se añade un sufijo |
| El complemento revisa cada minuto el archivo abierto y avisa si otra persona subió una versión | Funciona sin Mi carpeta abierta. Reutiliza la revisión normal: unas pocas peticiones por minuto |
| Mi cuenta: la contraseña actual se comprueba con el mismo límite de intentos que el login; tras cambiarla se emite una cookie nueva | Adivinar desde Mi cuenta cuenta igual que desde el login. Quien la cambia sigue dentro; las demás sesiones y los tokens dejan de valer |
| Desde Mi cuenta solo se revocan las conexiones propias, también siendo admin | Evita revocar la de otro por error |
| Liberar desde Mi cuenta usa la misma acción que la ficha, con `next` (pasado por `localPath`) para volver | Sin una acción nueva |
| La actividad muestra solo acciones de proyectos y archivos, en español; los archivos borrados sin enlace | Las cuentas y tokens son ruido y en parte privados. El texto sale de `vault.describeActivity`, junto a donde se escribe el `detail` |
| Actividad paginada con `before=<id>`, 50 por página, sin índices nuevos | Se añaden si se vuelve lenta |
| La búsqueda global ignora mayúsculas y acentos, filtrando en Go, máximo 100 resultados | Un modelador escribe "camion" para "Camión". `LIKE '%x%'` también recorre toda la tabla, así que filtrar en Go no cuesta más |
| `serve` hace una copia diaria si hay `BACKUP_DIR`, a la hora `BACKUP_HOUR` (por defecto 3, hora de `TZ`), con la misma función que el comando `backup` | Sin crontab. Las copias manuales también se ven en Espacio |
| La base de la copia se escribe con nombre temporal y reemplaza `vault.db` solo al final | Antes, una copia que fallaba a medias (disco lleno) dejaba una base que apuntaba a trozos no copiados y se perdía la copia anterior |
| Solo se guarda la última copia, sin `BACKUP_KEEP` | Los trozos nunca se borran de la copia, así que crece sola. Varias copias por fecha corresponden a la máquina que la trae fuera del VPS |
| `VACUUM INTO` con su propia conexión de solo lectura | Dentro de `serve`, usar la conexión de escritura detendría las subidas durante toda la copia |
| Estado de la copia (última correcta, duración, último error) en `DATA_DIR/ultima-copia.json` | Sobrevive a reinicios y cubre también las copias manuales |
| Aviso de disco a los admins por debajo del 15 % o de 20 GB libres, con caché de un minuto | `FreeBytes` pasa a `DiskSpace` para saber el tamaño total |
| Bloqueos en su propia página, `/admin/locks`, y no dentro de Espacio | Liberar un bloqueo olvidado es frecuente; Espacio se visita poco |
| `.zip` del proyecto o de una carpeta, método Store, en streaming trozo a trozo | Los `.blend` pesan GB. Si un trozo falla, se corta la conexión, como al descargar una versión |
| Miniatura de cada versión en el historial, ampliable con `popover` nativo | Sin JavaScript, compatible con la CSP. Las que faltan se generan una vez |
| Una sola constante de antigüedad de bloqueo (48 h) para Mi cuenta y Bloqueos | Los dos agentes la definieron por separado con el mismo valor |
| Modo oscuro que sigue la preferencia del sistema, redefiniendo los tokens de color | Toda la interfaz usa variables. Sin interruptor propio: el sistema ya lo tiene. Revisado con capturas de Mi carpeta y de la ficha con visor |
| Buscador y "Solo lo que necesita algo de mí" en Mi carpeta, en el navegador | Filtra las filas que ya están en memoria |
| Subir desde la ficha o el proyecto con `pushFile`, compartida con Mi carpeta | Sirve en cualquier navegador. Usa la identidad de Mi carpeta solo si el bloqueo es de ella; si no, la de la web, igual que los botones de esa página |
| Subir carpetas desde el proyecto: selector `webkitdirectory` y arrastrar con `webkitGetAsEntry`, con resumen antes de subir (2026-11-09) | Las escenas viven con sus texturas y maestros en subcarpetas, y sus rutas relativas solo funcionan si llegan igual. Lo que ya existe recibe versión nueva si está libre o es tuyo; lo bloqueado por otro no se toca. Las carpetas vacías no existen: una carpeta aparece cuando tiene un archivo |
| Los enlaces de un `.blend` se leen en el servidor, sin Blender, con `internal/blendrefs` (2026-11-09) | Funciona con cualquier forma de subir: web, Mi carpeta o Blender. Lee los bloques `LI`, `IM`, `SO`, `MC`, `VF`, `VO` y `CF` y sus campos con el DNA del propio archivo, así que no depende de la versión de Blender. Omite lo empaquetado y lo que viene de otra biblioteca |
| El análisis va por contenido (`blend_scans`, `blend_refs` por `file_sha256`), en segundo plano y de a uno | Lee el archivo entero: un `.blend` de 20 GB tarda unos minutos. Una restauración no se vuelve a leer. Al arrancar se analizan las últimas versiones que falten; al detener, se corta y se repite después |
| Los enlaces se resuelven al mostrarlos, no al guardarlos | Un maestro que se sube después arregla las escenas que lo usaban sin volver a leerlas. En la PC, cada proyecto es una carpeta de la carpeta de 3D-Vault, así que `//../../Biblioteca/…` cruza de proyecto. Una ruta absoluta se reconoce si contiene la carpeta de un proyecto, pero sigue contando como rota |
| Probado con escenas de Blender 4.0, con el formato de cabecera de 5.x simulado y con los dos `.blend` reales del equipo que ya estaban en el servidor | Los dos se leyeron sin error: cuatro bibliotecas en `ASSETS/`, dos texturas y un sonido, con rutas de Windows (`//..\ASSETS\…`). Si una versión futura cambiara el formato, el archivo queda como «no se pudo leer» y no rompe nada |
| Carpetas vacías en una tabla propia, `folders`, y árbol de cada proyecto en el menú lateral (2026-11-10) | Las carpetas con archivos se deducen de las rutas; solo las vacías necesitan guardarse. El árbol se carga al desplegar el proyecto, con la misma foto (`snapshot`) que usa Mi carpeta, y se refresca al volver a la pestaña y cada 30 s |
| Los eventos de carpetas no tienen archivo: la migración 0003 reconstruye `events` con `asset_id` opcional | SQLite no cambia una columna. Se copian los eventos con su número y se mantiene `sqlite_sequence`, para que ningún agente pierda ni repita cambios |
| Mi carpeta sincroniza carpetas en los dos sentidos y recuerda en `localStorage` las que había a los dos lados | Sin esa memoria no se distingue una carpeta nueva en un lado de una borrada en el otro. Solo se propaga el borrado de carpetas vacías; los archivos conservan sus reglas y nunca se borran por una carpeta |
| Renombrar y borrar carpetas con archivos, y Papelera por proyecto con Recuperar (2026-11-10) | Los modeladores se confundían con las carpetas. Renombrar mueve todos los archivos en una transacción, con un evento `moved` por archivo. Si otra persona bloquea alguno, no se mueve nada; los bloqueos propios, de cualquier PC, viajan con el archivo. Borrar manda los archivos a la papelera (borrado suave, regla 8): sin una forma de recuperarlos, borrar una carpeta entera era demasiado fácil de lamentar |
| Antes de renombrar o borrar, `GET …/folders/info` dice cuántos archivos, cuáles bloquea otro y qué `.blend` de fuera usan algo de dentro | Renombrar una carpeta de maestros rompe los enlaces de las escenas. Se avisa en vez de impedirlo |
| Mi carpeta sigue en el disco los movimientos del servidor: mueve el archivo local (`move()`, o copia si no está disponible) en vez de bajarlo de nuevo | Renombrar una carpeta con `.blend` de varios GB no debe volver a descargar todo. Solo si el archivo local no tiene cambios sin subir |
| Borrar proyecto = archivarlo (`archived`, con `deleted_at` y `deleted_by`, migración 0004), y Recuperar en Proyectos borrados (2026-11-10) | Cualquier modelador puede borrar un proyecto, así que no puede ser irreversible. Archivado, no admite cambios y no sale en el menú ni en Mi carpeta; sus archivos y versiones siguen ahí. Lo impide un archivo bloqueado por otra persona. El nombre sigue ocupado hasta recuperarlo. Mi carpeta no borra la carpeta local: puede tener trabajo sin subir |
| Un borrado en la PC es un cambio pendiente, como un archivo nuevo: «Borrado en tu PC: falta confirmar», aviso arriba de Mi carpeta y **Subir cambios** para confirmarlo, como un commit (2026-11-10) | Los modeladores borraban una imagen o un `.fbx` y no veían nada en la plataforma. Sigue sin propagarse solo, para que un borrado por error no quite el archivo a todos; pero ya no pasa desapercibido. Va a la papelera |
| Mi carpeta publica en `localStorage` el resumen de cambios sin subir; el menú y la página del proyecto lo muestran en otras pestañas, si tiene menos de 30 minutos | Sin servidor de por medio: lo que hay en una PC solo lo sabe esa PC |
| `FileSystemObserver`, donde existe (Chrome y Edge recientes), revisa la carpeta en cuanto cambia algo | Antes, un cambio se veía al volver a la pestaña o al minuto |
| «Enviar a render»: 3D-Vault arma el .zip de la escena con lo que usa mientras lo sube a la página de render, que lo guarda sin alquilar nada y lo abre con un token (2026-11-10) | Un .blend enlazado no lleva sus objetos. La página de render es de otro servidor y otras cuentas, y alquilar cuesta dinero: el último clic es de la persona, en esa página. Falta un .blend enlazado: no se envía. Falta una textura: se avisa y se puede enviar |
| En el .zip cada archivo va con la ruta exacta que escribe el .blend que lo usa | Se renderiza en Linux, que distingue mayúsculas; las escenas se guardan en Windows, que no |
| No se puede crear un archivo donde hay una carpeta, ni una carpeta dentro de un archivo | En el disco no pueden convivir un archivo y una carpeta con el mismo nombre |
| En Blender, el panel Archivos enlazados revisa cada maestro contra el servidor y su fecha en el disco cada 5 s | Mi carpeta puede bajar un maestro nuevo con la escena abierta: Blender no lo recarga solo. **Actualizar y recargar** baja solo lo que cambió y usa `Library.reload()`, sin cerrar la escena |

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
