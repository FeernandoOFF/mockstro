BIN_DIR ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_VERSION ?= dev-$(VERSION)
DEV_LDFLAGS := -s -w -X main.version=$(DEV_VERSION)

.PHONY: build build-dev install install-dev test vet uninstall uninstall-dev

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mockstro ./cmd/mockstro

build-dev:
	go build -ldflags "$(DEV_LDFLAGS)" -o bin/mockstro-dev ./cmd/mockstro

install:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/mockstro ./cmd/mockstro
	@echo "installed $(BIN_DIR)/mockstro ($(VERSION))"

install-dev:
	mkdir -p "$(BIN_DIR)"
	go build -ldflags "$(DEV_LDFLAGS)" -o "$(BIN_DIR)/mockstro-dev" ./cmd/mockstro
	@echo "installed $(BIN_DIR)/mockstro-dev ($(DEV_VERSION))"

test:
	go test ./...

vet:
	go vet ./...

uninstall:
	rm -f $(BIN_DIR)/mockstro

uninstall-dev:
	rm -f "$(BIN_DIR)/mockstro-dev"
