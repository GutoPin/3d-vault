# 3D-Vault

Sistema interno y autohospedado para guardar archivos 3D con historial de versiones, bloqueos y sincronización automática que solo transfiere lo que cambió.

- **Especificación:** [`PLAN.md`](PLAN.md)
- **Decisiones y mediciones:** [`docs/DECISIONES.md`](docs/DECISIONES.md)

## Estado

**Fase 0, validar el ahorro.** Todavía no hay servidor ni agente. Por ahora el repositorio contiene:

- `internal/chunker`: corte de archivos en trozos con FastCDC.
- `cmd/cdcmeasure`: mide qué parte de un archivo ya no haría falta transferir.

## Medir dos guardados de un archivo

Requiere Go 1.27.

```sh
make build-measure
./bin/cdcmeasure guardado-1.blend guardado-2.blend
```

## Desarrollo

```sh
make check   # go vet y tests con el detector de carreras
```
