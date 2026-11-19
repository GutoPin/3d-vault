# Pasar 3D-Vault a tu VPS

La guía asume Ubuntu o Debian. Si algo falla, copia el error completo.

## Cómo está instalado hoy (producción)

| Qué | Dónde |
|---|---|
| Dominio | `3drepositorio.codeadevelopment.com` |
| Servidor | `187.77.227.177` (`srv2124970`), usuario `root`. **No es `31.97.18.59`**: ese es otro VPS con otras ~40 apps y Traefik en los puertos 80 y 443, y desplegar ahí las rompería |
| Código | `/root/3d-vault`: un clon de GitHub (remoto `git@github-3dvault:GutoPin/3d-vault.git`, con llave de despliegue), en la rama `main` |
| Configuración | `/root/3d-vault/deploy/.env` y `/root/3d-vault/deploy/docker-compose.override.yml`. Los dos solo existen en el VPS y git no los toca |
| Proxy HTTPS | El Caddy del servidor, que ya ocupa 80 y 443, reenvía el dominio a `127.0.0.1:18090`. Por eso el override desactiva el Caddy del proyecto y publica el servidor solo en localhost |
| Datos | Volumen de Docker del proyecto `3d-vault` |
| Copias | `/srv/3d-vault-backup`, cada noche a las 3 (hora de Lima) |

El override de producción es este:

```yaml
# Solo en este VPS: el Caddy del host ya usa 80/443, así que no se levanta el Caddy
# del proyecto y el servidor se publica solo en localhost.
services:
  server:
    ports:
      - "127.0.0.1:18090:8080"
  caddy:
    profiles: ["nunca"]
```

Para actualizarlo, ve a [Para actualizar más adelante](#para-actualizar-más-adelante). Los pasos 1 a 9 son para instalarlo desde cero en un servidor nuevo.

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

## 2. Clona el código, en el VPS

El VPS baja el código de GitHub con una llave de despliegue de solo lectura, así que después se actualiza con `git pull`.

```sh
ssh root@IP_DEL_VPS
ssh-keygen -t ed25519 -f ~/.ssh/3dvault -N '' -C 3dvault-vps
cat ~/.ssh/3dvault.pub
```

Copia esa línea en GitHub → el repositorio → **Settings → Deploy keys → Add deploy key**, sin marcar **Allow write access**. Después, en el VPS:

```sh
printf 'Host github-3dvault\n  HostName github.com\n  IdentityFile ~/.ssh/3dvault\n' >> ~/.ssh/config
git clone git@github-3dvault:GutoPin/3d-vault.git ~/3d-vault
```

Usa siempre `main`: la rama `fase-1` quedó atrás y no tiene la sincronización de carpetas.

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

**Si el VPS ya tiene otro proxy en 80 y 443** (Caddy, Nginx o Traefik de otras apps), no levantes el Caddy del proyecto: crea `deploy/docker-compose.override.yml` como el de producción (arriba) y apunta tu proxy a `127.0.0.1:18090`. Docker Compose lee ese archivo solo.

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

Primero sube tus cambios a `main` en GitHub (`git push`). Luego, un solo comando desde tu laptop (pide la contraseña de `root` si no entras con llave SSH):

```sh
cd ~/dev/3d-vault
make deploy
```

En el VPS hace tres cosas, y se detiene si una falla: una copia de seguridad, `git pull` de `main` y la reconstrucción del servidor. El `.env`, el override y los datos no se tocan. Al final muestra el estado: `server` debe quedar `healthy`.

Si no tienes `make` a mano, es lo mismo que correr esto en el VPS:

```sh
cd /root/3d-vault/deploy
docker compose exec -T server 3dvault-server backup --to /backup
git pull --ff-only origin main
docker compose up -d --build
docker compose ps
```

Para otro servidor: `make deploy VPS=usuario@ip`, después de instalarlo con esta guía desde el paso 2.

**Los modeladores no tienen que hacer nada.** Mi carpeta revisa el servidor cada minuto y, cuando ve que se actualizó, se recarga sola en cuanto no hay nada a medias (ni una subida ni un diálogo abierto). Hace falta que ya tengan una versión del 19 de noviembre de 2026 o posterior: la primera vez después de esa actualización, que recarguen la página una vez (F5) o cierren y abran la aplicación.

Para comprobar qué versión responde, desde cualquier PC:

```sh
curl -s -D - -o /dev/null https://3drepositorio.codeadevelopment.com/healthz | grep -i x-app-version
```

Cambia con cada actualización que toque la web.

## Arreglo del 19 de noviembre de 2026: carpetas y archivos que faltaban

**Qué pasaba.** Mi carpeta recuerda, en cada navegador, qué archivos y carpetas había en la última pasada. Si un modelador pulsaba **Cambiar** carpeta, o movía o borraba la carpeta del proyecto en el Explorador, lo que faltaba en la carpeta nueva se tomaba como borrado por él:

- Los archivos quedaban en «Borrado en tu PC: falta confirmar» y no se bajaban. Si luego pulsaba **Subir cambios**, se borraban para todos (quedan en la Papelera).
- Las carpetas **vacías** se borraban del servidor, y desaparecían para todos.

Además, si el VPS se instaló con la rama `fase-1`, no tenía la sincronización de carpetas vacías ni el árbol de carpetas.

**Qué hace ahora.** Si la carpeta del proyecto no es la de la última pasada, lo que falta se baja en vez de darse por borrado, y no se borra ninguna carpeta del servidor. Las PCs que ya estaban en ese estado se reparan solas en su primera pasada con la versión nueva.

**Qué haces tú, una vez:**

1. Actualiza: `make deploy`. Ya se hizo el 19 de noviembre de 2026 (versión `4765056`).
2. Pide a cada modelador que recargue Mi carpeta una vez (F5, o cerrar y abrir la aplicación). Las próximas actualizaciones ya se aplican solas.
3. Recupera lo que se haya borrado por error:
   - **Archivos:** abre cada proyecto → **Papelera** y pulsa **Recuperar**. En **Actividad** ves quién borró qué y cuándo.
   - **Carpetas vacías:** no van a la Papelera. Pregunta qué carpetas faltan y créalas con **Nueva carpeta** en el proyecto. Se crean solas en todas las PCs.
4. Si alguien todavía no ve algo, revisa con esa persona **Carpetas…** en Mi carpeta: puede tenerla desmarcada.
