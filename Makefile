# go-ined build helpers. Everything also works with plain `go` commands.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/maarten-boot/go-ined/internal/version.Version=$(VERSION)

.PHONY: all build build-nogui nogui-check test vet fmt-check examples clean

all: fmt-check vet nogui-check test build

# ined-gui needs cgo and the X11/OpenGL dev packages (Fyne).
build:
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

# The server and the CLI only: no cgo, no GUI libraries.
build-nogui:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/ined-server ./cmd/inedctl

# Decision M5-1: ined-server and inedctl must not depend on Fyne or cgo.
nogui-check:
	@if go list -deps ./cmd/ined-server ./cmd/inedctl | grep -q '^fyne.io/'; then \
		echo "ined-server or inedctl imports Fyne"; exit 1; fi
	CGO_ENABLED=0 go build -o /dev/null ./cmd/ined-server ./cmd/inedctl

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
