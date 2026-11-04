# 3D-Vault

Sistema interno y autohospedado para guardar archivos 3D con historial de versiones, bloqueos y sincronización automática que solo transfiere lo que cambió.

- **Especificación:** [`PLAN.md`](PLAN.md)
- **Decisiones y mediciones:** [`docs/DECISIONES.md`](docs/DECISIONES.md)

## Estado

**Fase 1, servidor núcleo: falta probar el despliegue con Docker y HTTPS.** Ya funcionan la API, la web, `3dvault-sync push/pull` y los backups. Todavía no hay agente con ícono en la bandeja: es la Fase 2. El detalle y los resultados están en `docs/DECISIONES.md`.

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
```
