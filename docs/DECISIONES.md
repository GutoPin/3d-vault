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

- **La hipótesis original no se cumple en estos pares.** La especificación suponía que con *Compress* casi no habría ahorro. Aquí la versión comprimida reutiliza un porcentaje mayor y transfiere menos bytes. La repetición dentro de un mismo archivo no lo explica: es 0.0 % en los cuatro casos.
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

## Decisiones

| Decisión | Motivo |
|---|---|
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
