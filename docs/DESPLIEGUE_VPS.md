# Pasar 3D-Vault a tu VPS

Guía personal: este archivo no se sube a git. El código pasa por SSH, sin GitHub.

**Aviso:** el despliegue con Docker todavía no se ha ejecutado nunca. Si algo falla, copia el error completo. La guía asume Ubuntu o Debian y un usuario con `sudo`.

## Antes de empezar: pregúntale al Claude de tu VPS

Pega esto en Claude Code dentro del VPS y copia su respuesta de vuelta:

```text
Voy a instalar en este servidor una aplicación con Docker Compose: un servidor en Go y Caddy como proxy con HTTPS, que necesita los puertos 80 y 443 y guardará archivos 3D de varios GB.

Revisa el servidor y hazme un informe. SOLO LECTURA: no instales, no cambies ni reinicies nada, no abras puertos y no crees archivos. Si un comando necesita sudo y no está disponible, dilo y sigue. No muestres contraseñas, tokens, claves privadas ni el contenido de archivos .env.

Dime:
1. Sistema: distribución y versión, arquitectura, usuario actual y si tiene sudo sin contraseña.
2. Recursos: núcleos de CPU, RAM total y libre, swap, disco total y libre, y en qué partición quedarían /var/lib/docker y /srv.
3. Docker: si están instalados Docker y Docker Compose, sus versiones, y qué contenedores corren (nombre, imagen y puertos, sin variables de entorno).
4. Puertos: qué procesos o contenedores escuchan en 80 y 443, y si hay Nginx, Apache, Caddy o Traefik instalados o activos.
5. Firewall: estado de ufw o nftables, y si se permiten SSH, 80/tcp, 443/tcp y 443/udp. Avísame si puede haber un firewall externo del proveedor, que desde aquí no se ve.
6. Red: IP pública del servidor y salida de "dig +short NOMBRE_DE_DOMINIO" si te doy un dominio. Dominio: [ESCRIBE AQUÍ TU DOMINIO O "todavía no tengo"].
7. Hora: zona horaria configurada y si el reloj está sincronizado.
8. Copias: si hay otro disco montado o un destino de copias de seguridad configurado.

Termina con una lista corta de lo que podría impedir la instalación: poca RAM para compilar (menos de 2 GB), poco disco, puertos ocupados o un firewall. Si no estás seguro de algo, dilo en lugar de suponer.
```

## 1. Apunta el dominio

En tu proveedor de DNS, crea un registro **A** de tu dominio (por ejemplo `3dvault.tuempresa.com`) hacia la IP del VPS, y espera a que se propague.

## 2. Pasa el código, desde tu laptop

```sh
cd ~/dev/3d-vault
git fetch origin main
git archive origin/main | ssh USUARIO@IP_DEL_VPS 'mkdir -p ~/3d-vault && tar -x -C ~/3d-vault'
```

Solo viajan los archivos de `main` tal como está en GitHub, sin tus skills ni tus `.blend`. Usa siempre `main`: la rama `fase-1` quedó atrás y no tiene la sincronización de carpetas.

## 3. Instala Docker, en el VPS

```sh
ssh USUARIO@IP_DEL_VPS
curl -fsSL https://get.docker.com | sudo sh
```

Si usas `ufw`, abre los puertos, y no bloquees SSH:

```sh
sudo ufw allow OpenSSH
sudo ufw allow 80,443/tcp
sudo ufw allow 443/udp
```

## 4. Configura

```sh
cd ~/3d-vault/deploy
cp .env.example .env
sed -i "s|^SECRET_KEY=.*|SECRET_KEY=$(openssl rand -base64 48)|" .env
sed -i "s|^DOMAIN=.*|DOMAIN=TU_DOMINIO|" .env
sed -i "s|^BACKUP_PATH=.*|BACKUP_PATH=/srv/3d-vault-backup|" .env
sudo mkdir -p /srv/3d-vault-backup
sudo chown 65532:65532 /srv/3d-vault-backup
```

Si quieres las fechas en tu hora local, cambia `TZ` en `.env`, por ejemplo `TZ=America/Santiago`.

## 5. Arranca

```sh
sudo docker compose up -d --build
sudo docker compose ps
```

La primera vez tarda varios minutos porque compila en el VPS. Con 1 GB de RAM o menos puede quedarse sin memoria. Los dos servicios deben quedar activos, y `server` como `healthy`.

## 6. Crea tu cuenta de administrador

```sh
sudo docker compose exec -it server 3dvault-server create-admin --username TU_USUARIO --name "Tu nombre"
```

La contraseña debe tener al menos 10 caracteres. Luego entra a `https://TU_DOMINIO`.

## 7. Crea cuentas y proyectos, en la web

- **Usuarios:** crea una cuenta por persona.
- **Todos los proyectos:** crea el proyecto.

## 8. Prueba antes de invitar a nadie

Desde tu laptop, en Edge o Chrome:

1. Entra a `https://TU_DOMINIO/sync` (**Mi carpeta**) y elige una carpeta de prueba.
2. Marca el proyecto, copia un `.blend` dentro de su carpeta, pulsa **Sincronizar ahora** y luego **Agregar…**.
3. En otra ventana de incógnito, entra con otra cuenta, abre Mi carpeta con otra carpeta y comprueba que el archivo baja solo.

Luego verifica los datos en el VPS:

```sh
cd ~/3d-vault/deploy
sudo docker compose exec server 3dvault-server check
```

## 9. Comprueba las copias de seguridad

El servidor ya hace una copia cada noche en `/srv/3d-vault-backup`, a la hora `BACKUP_HOUR` del `.env` (las 3 por defecto, en tu `TZ`). No hace falta crontab. Haz ahora la primera, a mano, para no esperar a la noche:

```sh
sudo docker compose exec -T server 3dvault-server backup --to /backup
```

En **Espacio** verás la última copia correcta y la próxima. Si una falla, ahí aparece el motivo.

Opcional: si prefieres programarla tú, deja `BACKUP_DIR=` vacío en `.env` y añade esto con `sudo crontab -e`:

```text
0 3 * * * cd /home/USUARIO/3d-vault/deploy && docker compose exec -T server 3dvault-server backup --to /backup
```

Esa copia sigue dentro del VPS y en el mismo disco, así que también ocupa espacio en él. Para que sobreviva si lo pierdes, tráela cada día desde otra máquina, después de la hora de la copia (necesitas `rsync` en ambas):

```sh
rsync -a --rsync-path="sudo rsync" USUARIO@IP_DEL_VPS:/srv/3d-vault-backup/ ~/copias-3d-vault/
```

## Qué le das a cada persona

Por un canal privado:

1. El enlace: `https://TU_DOMINIO/sync`
2. Su usuario y su contraseña
3. La guía `docs/GUIA_USUARIO.md`

No hay nada que instalar: todo se hace en Edge o Chrome, así que Windows no tiene ningún programa que bloquear.

**Nunca compartas:** el archivo `.env`, el acceso SSH ni tu cuenta de administrador.

## Avísales de esto antes de empezar

Úsenlo como **piloto** las primeras semanas, sin que sea la única copia de su trabajo:

- **Siempre hay que bloquear antes de editar.** Si dos editan a la vez sin bloquear, no se pierde nada (queda una copia `*.conflicto-*`), pero hay que unir los cambios a mano.
- **La actualización automática funciona con la pestaña de Mi carpeta abierta.** Con la pestaña cerrada, los archivos siguen ahí y se ponen al día al volver a abrirla.
- **Que cada uno cambie su contraseña** al entrar la primera vez, en **Mi cuenta** (su nombre, abajo a la izquierda). Si alguien la olvida, la cambias tú en **Usuarios**.

## Para actualizar más adelante

Un solo comando, desde tu laptop (pide la contraseña de `sudo` del VPS):

```sh
cd ~/dev/3d-vault
make deploy VPS=USUARIO@IP_DEL_VPS
```

Sube lo que está en `main` en GitHub y reconstruye el servidor. El `.env` y los datos no se tocan. Los dos servicios deben quedar activos, y `server` como `healthy`.

**Los modeladores no tienen que hacer nada.** Mi carpeta revisa el servidor cada minuto y, cuando ve que se actualizó, se recarga sola en cuanto no hay nada a medias (ni una subida ni un diálogo abierto). Hace falta que ya tengan una versión del 19 de noviembre de 2026 o posterior: la primera vez después de esa actualización, que recarguen la página una vez (F5) o cierren y abran la aplicación.

Para comprobar qué versión responde, desde cualquier PC:

```sh
curl -sI https://TU_DOMINIO/healthz | grep -i x-app-version
```

Cambia con cada actualización que toque la web.

## Arreglo del 19 de noviembre de 2026: carpetas y archivos que faltaban

**Qué pasaba.** Mi carpeta recuerda, en cada navegador, qué archivos y carpetas había en la última pasada. Si un modelador pulsaba **Cambiar** carpeta, o movía o borraba la carpeta del proyecto en el Explorador, lo que faltaba en la carpeta nueva se tomaba como borrado por él:

- Los archivos quedaban en «Borrado en tu PC: falta confirmar» y no se bajaban. Si luego pulsaba **Subir cambios**, se borraban para todos (quedan en la Papelera).
- Las carpetas **vacías** se borraban del servidor, y desaparecían para todos.

Además, si el VPS se instaló con la rama `fase-1`, no tenía la sincronización de carpetas vacías ni el árbol de carpetas.

**Qué hace ahora.** Si la carpeta del proyecto no es la de la última pasada, lo que falta se baja en vez de darse por borrado, y no se borra ninguna carpeta del servidor. Las PCs que ya estaban en ese estado se reparan solas en su primera pasada con la versión nueva.

**Qué haces tú, una vez:**

1. Actualiza: `make deploy VPS=USUARIO@IP_DEL_VPS`.
2. Pide a cada modelador que recargue Mi carpeta una vez (F5, o cerrar y abrir la aplicación). Las próximas actualizaciones ya se aplican solas.
3. Recupera lo que se haya borrado por error:
   - **Archivos:** abre cada proyecto → **Papelera** y pulsa **Recuperar**. En **Actividad** ves quién borró qué y cuándo.
   - **Carpetas vacías:** no van a la Papelera. Pregunta qué carpetas faltan y créalas con **Nueva carpeta** en el proyecto. Se crean solas en todas las PCs.
4. Si alguien todavía no ve algo, revisa con esa persona **Carpetas…** en Mi carpeta: puede tenerla desmarcada.
