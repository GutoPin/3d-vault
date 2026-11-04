.PHONY: test test-race vet build-server build-agent-windows build-measure check

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

check: vet test
