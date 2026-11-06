.PHONY: test test-race vet build-server build-agent-windows build-measure check test-web e2e

# Limitado a 2 núcleos y prioridad baja: la máquina de desarrollo es modesta.
LIMITS := GOMAXPROCS=2 nice -n 19
GOBUILD := CGO_ENABLED=0 $(LIMITS) go build -p 1

test:
	$(LIMITS) go test -p 1 -parallel 2 ./...

# Pesado: compila todo con el detector de carreras. Pasa PKG=./internal/vault para
# limitarlo a un paquete.
PKG ?= ./...
test-race:
	$(LIMITS) go test -race -p 1 -parallel 2 $(PKG)

vet:
	$(LIMITS) go vet ./...

build-server:
	$(GOBUILD) -o bin/3dvault-server ./cmd/3dvault-server

build-agent-windows:
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o bin/3dvault-sync.exe ./cmd/3dvault-sync

build-measure:
	$(GOBUILD) -o bin/cdcmeasure ./cmd/cdcmeasure

# Núcleo de la sincronización web (cortes idénticos a Go, SHA-256). Requiere Node.
test-web:
	node --test web/sync_test.mjs

# Dos navegadores contra un servidor real. Requiere Node y Chromium (ver web/e2e).
e2e: build-server
	cd web/e2e && npm ci --silent && nice -n 19 node sync.e2e.mjs

check: vet test
