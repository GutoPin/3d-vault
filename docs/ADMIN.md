# Administración de 3D-Vault

Para quien instala y mantiene el servidor. La guía de los modeladores está en [`GUIA_USUARIO.md`](GUIA_USUARIO.md).

## Cómo funciona

- **Servidor** (`3dvault-server`): un solo programa en Docker, detrás de Caddy, que pone HTTPS automático. Guarda todo en el volumen `data`: la base de datos y los trozos de los archivos.
- **Modeladores:** usan la web en Edge o Chrome. La página **Mi carpeta** sincroniza una carpeta de su PC con el servidor. No se instala nada en las PCs.
- **HTTPS es obligatorio:** el navegador solo permite trabajar con carpetas locales en páginas seguras. Sin dominio con HTTPS, Mi carpeta no funciona.
- `3dvault-cli` es una herramienta de comandos para pruebas y para el administrador. Los modeladores no la necesitan.

## Instalar

Requisitos: un VPS con Docker, los puertos 80 y 443 abiertos y un dominio apuntando a su IP. El paso a paso, desde la laptop hasta el primer acceso, está en [`DESPLIEGUE_VPS.md`](DESPLIEGUE_VPS.md). En resumen:

```sh
cd deploy
cp .env.example .env          # SECRET_KEY (openssl rand -base64 48), DOMAIN, TZ, BACKUP_PATH
sudo docker compose up -d --build
sudo docker compose exec -it server 3dvault-server create-admin --username admin --name "Admin"
```

## Usuarios y proyectos

- **Usuarios** (menú Administración): crear cuentas, desactivarlas y cambiar contraseñas. Cambiar una contraseña cierra las sesiones de esa persona.
- **Proyectos:** cualquiera puede crearlos desde **Todos los proyectos**. El nombre del proyecto es el nombre de su carpeta en cada PC.
- **Desbloqueo forzado:** la página **Bloqueos** (menú Administración) lista los bloqueos de todos los proyectos, con persona, PC y desde cuándo; los de más de 2 días aparecen destacados. **Forzar desbloqueo** está ahí y en la ficha del archivo, y queda registrado. Úsalo solo si la persona no puede liberarlo; si tenía cambios sin subir, quedan solo en su PC.
- **Descargar un proyecto:** en la página del proyecto, **Descargar todo (.zip)** baja la última versión de cada archivo con sus carpetas; cada carpeta tiene su propio **Descargar .zip**. El .zip va sin comprimir y se arma mientras baja, así que el navegador no muestra el tamaño total.

## Sesiones

`SESSION_HOURS` en `.env` fija cuánto dura una sesión. Con 12 horas, quien deje 3D-Vault abriéndose al iniciar Windows escribirá su contraseña cada día. Para un equipo interno, `SESSION_HOURS=168` (una semana) es más cómodo. Cambiar la contraseña de alguien cierra sus sesiones en cualquier caso.

## Qué entregar a cada modelador

Por un canal privado:

1. La dirección: `https://TU_DOMINIO/sync`
2. Su usuario y su contraseña.
3. La guía [`GUIA_USUARIO.md`](GUIA_USUARIO.md). Que sigan el apartado para instalarla como aplicación y abrirla al iniciar sesión: es lo que hace que la sincronización sea automática.

## Complemento de Blender

El servidor lo entrega en `/blender/3dvault.zip`, siempre en la versión que corresponde al servidor. Tras actualizar el servidor, que cada uno lo vuelva a instalar desde Mi carpeta si cambió.

- Se conecta con un **código** que genera Mi carpeta: lleva la dirección del servidor, un token de API y la identidad de esa PC. Por eso un bloqueo hecho en Blender vale también en Mi carpeta. Blender no guarda contraseñas.
- Cada código es un token. Para cortar el acceso de una PC, cambia la contraseña de esa persona: revoca sus tokens.
- Necesita *Permitir acceso en línea* (Preferencias → Sistema → Red) y Blender 4.2 o posterior.
- Para subir, exige tener el bloqueo: así nunca reemplaza una versión de otro.

## Vistas previas

- **Miniaturas de `.blend`:** salen del propio archivo, si se guardó con *Save Preview Images*. Se generan al subir y se guardan en `DATA_DIR/thumbs`, que no entra en el backup porque se regenera sola.
- **Visor 3D:** `.glb`, `.gltf`, `.fbx`, `.obj` y `.stl`, en la ficha del archivo. Por encima de 200 MB pide confirmación antes de cargarlo.

## Copias de seguridad

**El servidor hace una copia cada día** en `BACKUP_DIR` (en Docker, `/backup`, que es la carpeta `BACKUP_PATH` del `.env`), a la hora `BACKUP_HOUR` (por defecto las 3, en la zona horaria `TZ`). Con `BACKUP_DIR` vacío no hay copias automáticas. La página **Espacio** muestra la última copia correcta, cuánto tardó, la próxima y, si la última falló, el motivo. El registro del servidor (`sudo docker compose logs server`) también lo anota.

Copia primero la base de datos (`VACUUM INTO`) y después los trozos, con el servidor en marcha y sin frenar las subidas. Sobre la copia del día anterior es incremental: solo copia los trozos nuevos y reemplaza la base al final. Si una copia falla a medias, por ejemplo por falta de espacio, la anterior sigue completa.

**Se conserva solo la última copia.** Los trozos nunca se borran de la carpeta de copias, ni siquiera después de `gc`, así que crece con el tiempo. Para empezarla de cero, borra la carpeta después de un `gc`: la siguiente copia será completa. Si quieres varias copias de días distintos, guárdalas en la máquina a la que la traes (ver abajo).

También se puede hacer a mano, por ejemplo antes de purgar:

```sh
sudo docker compose exec -T server 3dvault-server backup --to /backup
```

Si ya la tenías en el crontab, quita esa línea: hace lo mismo que la copia automática.

**Una copia dentro del VPS no sirve si se pierde el VPS.** Tráela a otra máquina, por ejemplo cada noche desde la oficina, después de la hora de la copia:

```sh
rsync -a --rsync-path="sudo rsync" USUARIO@IP_DEL_VPS:/srv/3d-vault-backup/ ~/copias-3d-vault/
```

## Restaurar

Con el servidor detenido. Regenera la época del servidor, así que los navegadores piden el estado completo al reconectar.

```sh
sudo docker compose stop server
sudo docker compose run --rm -v /ruta/de/la/copia:/restore server restore --from /restore
sudo docker compose run --rm server check
sudo docker compose up -d server
```

`check` verifica el hash de todos los trozos. También sirve con el servidor en marcha.

## Espacio en disco

La página **Espacio** (menú Administración) muestra lo guardado, lo libre y el uso por proyecto. Si al disco le queda menos del 15 % o menos de 20 GB libres, los administradores ven un aviso en todas las páginas.

Cada versión solo guarda los trozos que cambiaron, pero con archivos de 1 a 3 GB el disco se llena. Para liberar espacio, con el servidor detenido y después de una copia:

```sh
cd ~/3d-vault/deploy
sudo docker compose stop server
sudo docker compose run --rm server purge --keep-last 10 --older-than 180d          # simula
sudo docker compose run --rm server purge --keep-last 10 --older-than 180d --apply  # borra versiones
sudo docker compose run --rm server gc                                              # simula
sudo docker compose run --rm server gc --apply                                      # borra trozos
sudo docker compose up -d server
```

- `purge` conserva siempre la versión actual de cada archivo, las N más nuevas y las de los últimos días indicados. Quita del todo los archivos borrados hace más de ese tiempo. No borra trozos.
- `gc` borra los trozos que ya no usa ninguna versión.
- Sin `--apply`, ambos solo muestran cuánto liberarían.

Si el disco pasa del 80 %, ajusta `--keep-last` y `--older-than` a la baja, o amplía el disco.

## Actualizar

Desde la laptop, pasa el código nuevo al VPS (ver `DESPLIEGUE_VPS.md`, paso 2) y en el VPS:

```sh
cd ~/3d-vault/deploy && sudo docker compose up -d --build
```

No hay nada que actualizar en las PCs: los modeladores reciben la versión nueva de la página al recargarla.

## Problemas frecuentes

| Síntoma | Causa y solución |
|---|---|
| "Abre esta página en Microsoft Edge o Google Chrome" | Usa Firefox o Safari, que no permiten carpetas locales |
| No aparece el botón para elegir carpeta | La página no está en HTTPS |
| "Ya está abierta en otra pestaña" | Hay otra pestaña de Mi carpeta abierta en ese navegador |
| Un archivo queda en "Error" | El mensaje lo explica. Si dice que el archivo está en uso, se reintenta solo |
| Archivos `*.conflicto-*` en una PC | Alguien editó sin bloquear. La copia es su trabajo: decidan qué conservar y borren la copia |
| Alguien no puede subir: "Bloqueado por X" | X tiene el bloqueo. Que lo libere, o fuerza el desbloqueo |
