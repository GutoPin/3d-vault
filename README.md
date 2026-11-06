# 3D-Vault

Sistema interno y autohospedado para guardar archivos 3D con historial de versiones, bloqueos y sincronización automática que solo transfiere lo que cambió.

- **Especificación:** [`PLAN.md`](PLAN.md)
- **Decisiones y mediciones:** [`docs/DECISIONES.md`](docs/DECISIONES.md)

## Estado

**Fases 1 a 3 listas para el piloto; falta probar el despliegue con Docker y HTTPS en el VPS.**

- **Fase 2, cambio de plan:** los modeladores sincronizan desde la web, en la página **Mi carpeta** (`/sync`) de Edge o Chrome. No se instala nada, así que Windows no tiene un `.exe` que bloquear. Probado de punta a punta con dos navegadores (`make e2e`).
- **Fase 3:** `purge`, `gc` y la página Espacio para administrar el disco; visor 3D y miniaturas de `.blend`.
- **Facilidades:** instalar como aplicación que se abre al iniciar Windows, arrastrar y soltar, Subir todo, avisos de escritorio, detección de renombrados y subida desde cualquier navegador.
- **Colaboración y administración:** pedir que liberen un archivo, Mi cuenta, Actividad, búsqueda global, descargar proyectos como `.zip`, miniaturas por versión, copias de seguridad automáticas, aviso de disco, página de bloqueos y modo oscuro.
- **Fase 4:** complemento de Blender (pestaña 3D-Vault, tecla N) para bloquear, guardar y subir, y actualizar sin salir de Blender. Se descarga desde Mi carpeta.

Guías: [`docs/GUIA_USUARIO.md`](docs/GUIA_USUARIO.md) para modeladores y [`docs/ADMIN.md`](docs/ADMIN.md) para el administrador. El detalle y los resultados están en `docs/DECISIONES.md`.

## Desplegar

```sh
cd deploy
cp .env.example .env    # completa SECRET_KEY y DOMAIN
docker compose up -d
docker compose exec -it server 3dvault-server create-admin --username admin
```

## Probar sin Docker

```sh
make build-server
SECRET_KEY=$(openssl rand -base64 32) DATA_DIR=./data LISTEN_ADDR=127.0.0.1:8080 \
  INSECURE_COOKIES=true ./bin/3dvault-server serve
```

## Medir dos guardados de un archivo

Requiere Go 1.27.

```sh
make build-measure
./bin/cdcmeasure guardado-1.blend guardado-2.blend
```

## Desarrollo

```sh
make check                              # go vet y tests, con 2 núcleos y prioridad baja
make test-race PKG=./internal/vault     # detector de carreras: pesado, mejor por paquete
make test-web                           # núcleo de la sincronización web, con Node
make e2e                                # dos navegadores contra un servidor real (Node y Chromium)
make test-blender PY=...                # núcleo del complemento (Python con numpy)
make test-blender-e2e PY=...            # el complemento dentro de Blender (módulo bpy) contra el servidor
```
