# Guía de 3D-Vault para modeladores

3D-Vault guarda los archivos de cada proyecto en el servidor de la empresa, con todas sus versiones, y los mantiene al día en una carpeta de tu PC. No hay que instalar nada: se usa desde el navegador.

## Lo que necesitas

- **Microsoft Edge o Google Chrome** en tu PC. Edge ya viene con Windows. Firefox y Safari no sirven para la carpeta, solo para consultar.
- La dirección de 3D-Vault, tu usuario y tu contraseña. Te los da el administrador.

## La primera vez

1. Crea una carpeta para 3D-Vault, por ejemplo `D:\3D-Vault` o `Documentos\3D-Vault`. Tiene que ser una carpeta propia: el navegador no deja usar Documentos ni el Escritorio directamente.
2. Entra a la dirección de 3D-Vault y escribe tu usuario y contraseña.
3. En el menú de la izquierda, abre **Mi carpeta**.
4. Pulsa **Elegir carpeta**, elige la que creaste y acepta los dos avisos del navegador (**Ver archivos** y **Guardar cambios**).
5. Marca los proyectos que quieres tener. Te dirá cuánto va a bajar; acepta.

Dentro de tu carpeta aparece una carpeta por proyecto, con todos sus archivos.

**Consejo:** guarda la página en favoritos. Cada vez que la abras, el navegador te pedirá permiso para usar la carpeta: pulsa **Permitir y sincronizar**. Si te ofrece **Permitir siempre**, elígelo.

## Mientras trabajas

**Deja abierta la pestaña de Mi carpeta.** Mientras está abierta, cuando un compañero sube una versión nueva, tu copia se actualiza sola en unos segundos. Si la cierras, no pasa nada: al volver a abrirla se pone al día.

En la tabla de cada proyecto ves el estado de cada archivo:

| Estado | Qué significa | Qué haces |
|---|---|---|
| Al día | Tienes la última versión | Nada |
| Bloqueado por ti | Lo estás editando tú | Trabaja en él y súbelo |
| Bloqueado por Juan | Lo está editando Juan | Ábrelo para mirar, pero no lo edites |
| Cambios sin subir | Lo guardaste y el servidor aún no lo tiene | **Subir…** |
| Nuevo, sin subir | Es un archivo que solo está en tu PC | **Agregar…** si es del proyecto |
| No está en tu carpeta | Lo borraste o lo moviste | **Descargar** para recuperarlo |

## Editar un archivo, paso a paso

1. Pulsa **Bloquear** en ese archivo. Así nadie más lo sube mientras lo editas, y los demás ven que lo tienes tú.
2. Si ya lo tenías abierto en Blender, usa **Archivo → Revertir** para cargar la última versión.
3. Trabaja y guarda en Blender como siempre (Ctrl+S).
4. Vuelve a Mi carpeta. El archivo dice **Cambios sin subir**. Pulsa **Subir…**.
5. Escribe en pocas palabras qué cambiaste, por ejemplo "Más detalle en la cabina". Deja marcado **Liberar el bloqueo al terminar** si ya acabaste.
6. Espera a que diga **Subido como v5**. Solo viaja lo que cambió, así que una versión nueva suele subir mucho más rápido que la primera.

Puedes subir varias versiones seguidas sin liberar: desmarca la casilla y libéralo con **Liberar** al terminar el día.

**Para tener varias variantes** (baja, media y alta en detalle, por ejemplo), guárdalas como archivos distintos: `camion_bajo.blend`, `camion_alto.blend`. Las **versiones** de un mismo archivo son su historia: v1, v2, v3…

## Agregar archivos nuevos

Copia o guarda el archivo dentro de la carpeta del proyecto, en la subcarpeta que quieras. En Mi carpeta aparece como **Nuevo, sin subir**. Pulsa **Agregar…**, escribe un comentario y listo: desde ese momento lo tienen todos.

Los archivos de respaldo de Blender (`.blend1`, `.blend2`…) y los temporales se ignoran solos.

## Si algo choca

Si cambias un archivo que no habías bloqueado y mientras tanto otra persona sube una versión nueva, 3D-Vault **no borra tu trabajo**: guarda tu copia con un nombre como `camion.conflicto-ana-20261106-1043.blend`, junto al original, y baja la versión del servidor. Avisa a tu compañero para decidir qué cambios quedan.

Para evitarlo: **bloquea siempre antes de editar**.

## Ver versiones anteriores

Pulsa el nombre de un archivo para abrir su ficha. Ahí está el historial con quién subió cada versión, cuándo, su comentario y cuánto cambió. Puedes **Descargar** cualquier versión o **Restaurarla**, lo que crea una versión nueva igual a la antigua, sin borrar nada.

## Preguntas frecuentes

**¿Por qué no es un programa instalable?** Windows bloquea los programas que no están firmados con un certificado pagado. Desde el navegador no hace falta instalar nada y no hay nada que bloquear.

**¿Puedo cerrar el navegador?** Sí. Tus archivos siguen en tu carpeta y funcionan sin conexión. Lo que no pasa con la pestaña cerrada es la actualización automática.

**Dice que está abierta en otra pestaña.** Solo una pestaña sincroniza a la vez. Cierra la otra.

**Dice que mi sesión terminó.** Vuelve a entrar con tu usuario y contraseña.

**Borré un archivo sin querer.** Pulsa **Descargar** en su fila. Borrar en tu PC nunca lo borra del servidor.

**Quiero borrarlo de verdad.** Borra el archivo de tu carpeta y luego pulsa **Borrar del servidor**. Desaparece para todos, pero el administrador puede recuperarlo durante un tiempo.

**¿Funciona en Mac?** Sí, con Google Chrome.
