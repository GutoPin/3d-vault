.PHONY: test test-race vet build-server build-agent-windows build-measure check test-web e2e test-blender test-blender-e2e guia-pdf deploy

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

# Complemento de Blender. PY necesita numpy; la prueba dentro de Blender, además,
# el módulo bpy (pip install bpy, Python 3.13).
PY ?= python3
test-blender:
	$(PY) -m unittest web/blender/test_vault.py

test-blender-e2e: build-server
	nice -n 19 $(PY) web/blender/test_addon_blender.py

# PDF de la guía para modeladores, desde docs/guia-modeladores/guia.html.
guia-pdf:
	cd web/e2e && npm ci --silent && node guia-pdf.mjs

check: vet test

# Sube al VPS lo que está en GitHub (origin/main) y lo reconstruye. Los datos y el
# .env del VPS no se tocan. Uso: make deploy VPS=usuario@ip
deploy:
	@test -n "$(VPS)" || { echo "Uso: make deploy VPS=usuario@ip"; exit 1; }
	git fetch origin main
	git archive origin/main | ssh $(VPS) 'mkdir -p ~/3d-vault && tar -x -C ~/3d-vault'
	ssh -t $(VPS) 'cd ~/3d-vault/deploy && sudo docker compose up -d --build && sudo docker compose ps'
