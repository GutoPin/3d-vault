.PHONY: test vet build-measure check

test:
	go test -race ./...

vet:
	go vet ./...

build-measure:
	CGO_ENABLED=0 go build -o bin/cdcmeasure ./cmd/cdcmeasure

check: vet test
