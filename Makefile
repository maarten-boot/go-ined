# go-ined build helpers. Everything also works with plain `go` commands.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/maarten-boot/go-ined/internal/version.Version=$(VERSION)

.PHONY: all build test vet fmt-check examples clean

all: fmt-check vet test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test -race ./...

vet:
	go vet ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-ed:"; echo "$$out"; exit 1; fi

examples:
	scripts/gen-api-examples.sh

clean:
	rm -rf bin/
