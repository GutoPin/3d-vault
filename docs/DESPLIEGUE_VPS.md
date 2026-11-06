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
git archive fase-1 | ssh USUARIO@IP_DEL_VPS 'mkdir -p ~/3d-vault && tar -x -C ~/3d-vault'
```

Solo viajan los archivos del commit, sin tus skills ni tus `.blend`.

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

Comprueba desde tu laptop que el servidor recibe archivos, y luego verifica en el VPS:

```sh
cd ~/dev/3d-vault
GOMAXPROCS=2 nice -n 19 go build -p 1 -o bin/ ./cmd/3dvault-sync
./bin/3dvault-sync login --server https://TU_DOMINIO --user TU_USUARIO
./bin/3dvault-sync push --project "NOMBRE" --path pruebas/ESCENA5.blend --comment "Prueba inicial" ~/Descargas/ESCENA5.blend
```

```sh
cd ~/3d-vault/deploy
sudo docker compose exec server 3dvault-server check
```

## 9. Activa las copias de seguridad

Primero haz una copia manual:

```sh
sudo docker compose exec -T server 3dvault-server backup --to /backup
```

Para que corra todas las noches, añade esto con `sudo crontab -e`:

```text
0 3 * * * cd /home/USUARIO/3d-vault/deploy && docker compose exec -T server 3dvault-server backup --to /backup
```

Esa copia sigue dentro del VPS. Para que sobreviva si lo pierdes, tráela desde otra máquina (necesitas `rsync` en ambas):

```sh
rsync -a --rsync-path="sudo rsync" USUARIO@IP_DEL_VPS:/srv/3d-vault-backup/ ~/copias-3d-vault/
```

## Qué le das a cada persona

Por un canal privado:

1. El enlace: `https://TU_DOMINIO`
2. Su usuario y su contraseña
3. El archivo `bin/3dvault-sync.exe` de tu laptop, ya compilado
4. Estos comandos, para PowerShell:

```powershell
# Solo la primera vez
.\3dvault-sync.exe login --server https://TU_DOMINIO --user SU_USUARIO

# Para trabajar en un archivo, en este orden
.\3dvault-sync.exe lock --project "NOMBRE" --path modelos/camion.blend
.\3dvault-sync.exe pull --project "NOMBRE" --path modelos/camion.blend camion.blend
# ...editar en Blender...
.\3dvault-sync.exe push --project "NOMBRE" --path modelos/camion.blend --comment "Qué cambió" --release camion.blend
```

Si `pull` dice que ya existe un archivo distinto, hay que guardar esa copia aparte antes de usar `--overwrite`, porque esa opción la reemplaza.

**Nunca compartas:** el archivo `.env`, el acceso SSH ni tu cuenta de administrador.

## Avísales de esto antes de empezar

Esto es la Fase 1, así que úsenlo como **piloto**, sin que sea la única copia de su trabajo:

- **No hay sincronización automática:** eso llega con el agente de la Fase 2. Por ahora se sube y se baja a mano.
- **Siempre hay que bloquear antes de editar:** `push` parte de la última versión del servidor, así que sin bloqueo alguien podría reemplazar el trabajo de otro. Queda en el historial, pero el cambio del otro deja de ser la versión actual.
- **Hay que bloquear desde la herramienta, no desde la web:** un bloqueo hecho en la web impide subir desde el `.exe`. Se resuelve añadiendo `--takeover` al comando `lock`.
- **Nadie puede cambiar su propia contraseña:** lo haces tú en **Usuarios**, y eso desconecta a esa persona, que tendrá que volver a hacer `login`.
- **El `.exe` nunca se ha ejecutado en Windows:** solo está compilado, y como no está firmado, SmartScreen puede advertir al abrirlo. Pruébalo en una PC antes de repartirlo.

## Para actualizar más adelante

Repite el paso 2 con la rama que corresponda. Tu `.env` se conserva. Después, en el VPS:

```sh
cd ~/3d-vault/deploy && sudo docker compose up -d --build
```
