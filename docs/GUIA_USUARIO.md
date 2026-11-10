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

## Que se sincronice solo desde que enciendes la PC (recomendado)

1. En Mi carpeta, baja hasta **Comodidades** y pulsa **Instalar**. Si no aparece el botón: menú **⋯** de Edge → **Aplicaciones** → **Instalar este sitio como aplicación**.
2. 3D-Vault se abre en su propia ventana, con ícono en la barra de tareas y en el menú Inicio.
3. Escribe `edge://apps` en la barra de direcciones de Edge, haz clic derecho sobre 3D-Vault y marca **Iniciar automáticamente al iniciar sesión**.
4. En **Comodidades**, pulsa **Activar avisos**. Windows te avisará cuando un compañero suba una versión nueva, aunque estés en Blender.

Si usas una pestaña en vez de la aplicación: Edge "duerme" las pestañas que no usas y entonces deja de sincronizar. Evítalo en `edge://settings/system` → **Optimizar el rendimiento** → **Nunca poner en suspensión estos sitios** → agrega la dirección de 3D-Vault.

Desde entonces, al encender la PC 3D-Vault se abre solo. Puedes minimizar la ventana y olvidarte. Si la sesión venció, te pedirá tu contraseña y volverá a Mi carpeta.

## Mientras trabajas

**Deja abierta la pestaña de Mi carpeta.** Mientras está abierta, cuando un compañero sube una versión nueva, tu copia se actualiza sola en unos segundos. Si la cierras, no pasa nada: al volver a abrirla se pone al día.

En la tabla de cada proyecto ves el estado de cada archivo:

| Estado | Qué significa | Qué haces |
|---|---|---|
| Al día | Tienes la última versión | Nada |
| Bloqueado por ti | Lo estás editando tú | Trabaja en él y súbelo |
| Bloqueado por Juan | Lo está editando Juan | Ábrelo para mirar, pero no lo edites. Si lo necesitas, **Pedir que lo libere**: a Juan le llega un aviso |
| Cambios sin subir | Lo guardaste y el servidor aún no lo tiene | **Subir…** |
| Nuevo, sin subir | Es un archivo que solo está en tu PC | **Agregar…** si es del proyecto |
| No está en tu carpeta | Lo borraste o lo moviste | **Descargar** para recuperarlo |
| Renombrado o movido | Le cambiaste el nombre o la carpeta en el Explorador | **Mover en el servidor**: conserva su historial y llega así a todos |

## Editar un archivo, paso a paso

1. Pulsa **Bloquear** en ese archivo. Así nadie más lo sube mientras lo editas, y los demás ven que lo tienes tú.
2. Si ya lo tenías abierto en Blender, usa **Archivo → Revertir** para cargar la última versión.
3. Trabaja y guarda en Blender como siempre (Ctrl+S).
4. Vuelve a Mi carpeta. El archivo dice **Cambios sin subir**. Pulsa **Subir…**.
5. Escribe en pocas palabras qué cambiaste, por ejemplo "Más detalle en la cabina". Deja marcado **Liberar el bloqueo al terminar** si ya acabaste.
6. Espera a que diga **Subido como v5**. Solo viaja lo que cambió, así que una versión nueva suele subir mucho más rápido que la primera.

Puedes subir varias versiones seguidas sin liberar: desmarca la casilla y libéralo con **Liberar** al terminar el día.

**Para tener varias variantes** (baja, media y alta en detalle, por ejemplo), guárdalas como archivos distintos: `camion_bajo.blend`, `camion_alto.blend`. Las **versiones** de un mismo archivo son su historia: v1, v2, v3…

## Trabajar desde Blender (recomendado)

Con el complemento de 3D-Vault, todo se hace sin salir de Blender. Se instala una sola vez:

1. En Mi carpeta, baja hasta **Comodidades → Conectar Blender** y pulsa **Descarga el complemento**.
2. En Blender: **Editar → Preferencias → Complementos**, menú **⌄** arriba a la derecha → **Instalar desde el disco**, y elige el archivo descargado.
3. En Mi carpeta pulsa **Generar código**, luego **Copiar**, y pégalo en **Código de conexión**, en las preferencias del complemento. Pulsa **Probar conexión**.
4. Si Blender dice que no tiene permiso para conectarse: **Preferencias → Sistema → Red → Permitir acceso en línea**.

Después, en la vista 3D pulsa **N** y abre la pestaña **3D-Vault**. Con un archivo de tu carpeta abierto verás:

- Su estado: **Al día**, **Tienes la v3 · hay v5 de Juan**, o **Cambios sin subir**, y quién lo tiene bloqueado.
- **Bloquear** y **Liberar**.
- **Guardar y subir**: guarda, te pide el comentario y sube solo lo que cambió.
- **Actualizar a v5**: baja la versión nueva y la abre. Si tenías cambios que el servidor no tiene, primero guarda tu copia como `*.conflicto-*`.
- **Ver historial**: abre la ficha del archivo en la web.
- **Pedir que lo libere**, si lo tiene otra persona.
- **Versiones** (despliégalo): todas las versiones con su autor, fecha y comentario. El botón de ventana de cada una la **abre aparte**, en otra ventana de Blender, para compararla con la tuya sin tocar tu archivo.

**Texturas:** si el archivo usa imágenes que están fuera de la carpeta del proyecto (por ejemplo en Descargas) o con ruta absoluta, en las PCs de tus compañeros se verían rosas. El panel lo avisa: pulsa **Arreglar rutas** y las copia a la carpeta `texturas` del proyecto, deja las rutas bien y las sube. Después sube el `.blend`.

Con Blender abierto, cada minuto revisa si alguien subió una versión nueva del archivo que tienes abierto y te avisa, aunque no tengas Mi carpeta abierta.

Si el complemento se queda viejo, el panel lo avisa con un botón para bajar el nuevo: instálalo igual que la primera vez.

Además te avisa **al abrir** un archivo si hay una versión más nueva o si lo tiene otra persona, y **al guardar** si lo estás editando sin bloquearlo.

## Archivos maestros y enlazados

Un **archivo maestro** guarda objetos que se reutilizan en muchas escenas: el camión, la grúa, la cabina. Las escenas no los copian: los **enlazan** (en Blender, **Archivo → Enlazar**). Así, cuando alguien mejora el maestro, todas las escenas que lo usan ven el cambio.

Para que funcione en todas las PCs:

1. **Guarda el maestro dentro de un proyecto** de 3D-Vault, por ejemplo en `maestros/camion.blend`, y súbelo. Si lo comparten varios proyectos, puede vivir en un proyecto propio, como «Biblioteca»: el enlace entre proyectos funciona si tienes los dos marcados en Mi carpeta.
2. **Enlaza desde el panel:** en **Archivos enlazados**, **Enlazar desde 3D-Vault…** abre **Archivo → Enlazar** en la carpeta del proyecto, con ruta relativa. Entra al `.blend`, luego a `Collection` u `Object`, y elige lo que quieras.
3. **Sube la escena** como siempre.

En el panel de 3D-Vault (tecla **N**), **Archivos enlazados** muestra cada maestro que usa la escena:

- **Al día · v4**, o **Tienes la v3 · hay v4 de Ana**: pulsa **Actualizar y recargar** y verás el cambio sin cerrar tu escena.
- **Cambió en el disco:** Mi carpeta bajó una versión nueva. Pulsa **Recargar para ver los cambios**.
- **Falta subirlo:** el maestro está en tu PC pero no en 3D-Vault. Pulsa **Subir a 3D-Vault**, o tus compañeros no lo verán.
- **Ruta absoluta** o **Fuera de la carpeta de 3D-Vault:** el enlace solo funciona en tu PC. **Arreglar rutas** corrige las rutas absolutas. Si el maestro está fuera de 3D-Vault, cópialo a un proyecto y vuelve a enlazarlo.

**Para editar un maestro**, pulsa **Abrir para editar**: guarda tu escena y abre el maestro. Bloquéalo, cambia lo que necesites, guarda y súbelo como cualquier archivo. En el maestro, el panel muestra **Lo usan** con las escenas que lo enlazan: son las que verán tu cambio.

En la web, la ficha de un `.blend` tiene **Archivos enlazados**: lo que usa (maestros, texturas, sonidos, cachés…) y si cada uno está en 3D-Vault, y en un maestro, **Lo usan**. **Descargar con todo lo que usa (.zip)** baja la escena con sus maestros y texturas en sus carpetas, lista para abrir. En la lista del proyecto, los `.blend` con enlaces rotos llevan un aviso y tienen su propia pestaña, **Con enlaces rotos**.

## Guarda sin comprimir

En Blender, **Archivo → Guardar como**, engranaje ⚙, desmarca **Comprimir**, y guarda. Con el archivo comprimido, cualquier cambio en la geometría obliga a subir el archivo casi entero; sin comprimir, solo lo que cambió, y cada versión ocupa mucho menos en el servidor. El complemento lo detecta y lo hace con un clic: **Guardar sin comprimir**. El archivo ocupa más en tu disco, pero sube mucho más rápido.

## Agregar archivos nuevos

La forma más rápida: **arrastra los archivos o carpetas** desde el Explorador de Windows y suéltalos sobre la tabla del proyecto. Se copian a la carpeta del proyecto, con sus subcarpetas, y te pide un solo comentario para subirlos todos.

También puedes copiar o guardar el archivo dentro de la carpeta del proyecto. En Mi carpeta aparece como **Nuevo, sin subir**. Pulsa **Agregar…**, escribe un comentario y listo: desde ese momento lo tienen todos.

¿Estás en otra PC o sin Mi carpeta? En la página del proyecto, **Agregar archivos o carpetas** sube desde cualquier navegador, y en la ficha de un archivo, **Subir nueva versión**:

- **Elegir una carpeta** sube la carpeta entera con sus subcarpetas: por ejemplo una escena con su carpeta `texturas` y sus maestros. También puedes arrastrar archivos o carpetas sobre la página.
- Cada carpeta de la lista tiene **Agregar aquí**, para subir dentro de ella.
- **Carpetas vacías:** en el menú de la izquierda, la flecha junto al proyecto despliega sus carpetas y archivos. El ícono de carpeta con + crea una carpeta nueva, y la papelera borra una vacía. Mi carpeta las crea o borra también en tu PC, y al revés: una carpeta que crees en el Explorador aparece en la web. Una carpeta con archivos nunca se borra sola.
- Antes de subir te muestra qué es nuevo, qué ya existía (recibe una versión nueva si cambió) y qué está bloqueado por otra persona (no se toca).

Si tienes varios archivos nuevos o cambiados, el botón **Subir todo** junto al nombre del proyecto los sube de una vez con el mismo comentario.

En proyectos grandes, el buscador de arriba filtra por nombre y **Solo lo que necesita algo de mí** deja solo lo pendiente.

Si tienes un archivo bloqueado desde hace más de un día, Mi carpeta te lo recuerda: libéralo si ya no lo estás editando.

El número entre paréntesis en la pestaña del navegador, por ejemplo **(2) Mi carpeta**, son los archivos que esperan que los subas.

Los archivos de respaldo de Blender (`.blend1`, `.blend2`…) y los temporales se ignoran solos.

## Si algo choca

Si cambias un archivo que no habías bloqueado y mientras tanto otra persona sube una versión nueva, 3D-Vault **no borra tu trabajo**: guarda tu copia con un nombre como `camion.conflicto-ana-20261106-1043.blend`, junto al original, y baja la versión del servidor. Avisa a tu compañero para decidir qué cambios quedan.

Para evitarlo: **bloquea siempre antes de editar**.

## Ver versiones anteriores

Pulsa el nombre de un archivo para abrir su ficha. Ahí ves la miniatura de los `.blend` (si se guardaron con vista previa), las imágenes, y los `.glb`, `.gltf`, `.fbx`, `.obj` y `.stl` en 3D: arrastra para girar, rueda para acercar y **Encuadrar** para volver a la vista inicial. También está el historial con quién subió cada versión, cuándo, su comentario y cuánto cambió. Puedes **Descargar** cualquier versión o **Restaurarla**, lo que crea una versión nueva igual a la antigua, sin borrar nada.

## Descargar y comparar desde la web

- En la página de un proyecto, **Descargar todo (.zip)** baja la última versión de cada archivo con sus carpetas, por ejemplo para entregar a un cliente. Cada carpeta tiene su propio **Descargar .zip**.
- En el historial de un `.blend`, cada versión muestra su miniatura: púlsala para verla grande y comparar.

## Mi cuenta, actividad y búsqueda

- **Mi cuenta** (tu nombre, abajo a la izquierda): cambia tu contraseña la primera vez que entres. También ves tus **conexiones de Blender** (puedes revocar la de una PC que ya no uses) y **todos tus bloqueos**, de todos los proyectos, con un botón para liberarlos. Al cambiar la contraseña, Blender deja de estar conectado: genera un código nuevo en Mi carpeta.
- **Actividad**: quién subió, bloqueó, liberó o movió qué y cuándo. Puedes filtrar por proyecto y por persona.
- **Buscar** (arriba a la izquierda): encuentra un archivo en todos los proyectos por parte de su nombre o carpeta, sin importar mayúsculas ni acentos.

## Preguntas frecuentes

**¿Por qué no es un programa instalable?** Windows bloquea los programas que no están firmados con un certificado pagado. Desde el navegador no hace falta instalar nada y no hay nada que bloquear.

**¿Puedo cerrar el navegador?** Sí. Tus archivos siguen en tu carpeta y funcionan sin conexión. Lo que no pasa con la pestaña cerrada es la actualización automática.

**Dice que está abierta en otra pestaña.** Solo una pestaña sincroniza a la vez. Cierra la otra.

**Dice que mi sesión terminó.** Vuelve a entrar con tu usuario y contraseña.

**Le cambié el nombre a un archivo.** Mi carpeta lo detecta y te ofrece **Mover en el servidor**, que conserva todas sus versiones.

**Borré un archivo sin querer.** Pulsa **Descargar** en su fila. Borrar en tu PC nunca lo borra del servidor.

**Quiero borrarlo de verdad.** Borra el archivo de tu carpeta y luego pulsa **Borrar del servidor**. Desaparece para todos, pero el administrador puede recuperarlo durante un tiempo.

**¿Funciona en Mac?** Sí, con Google Chrome.
