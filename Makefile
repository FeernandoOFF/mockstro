BIN_DIR ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet uninstall

build:
	go build -ldflags "$(LDFLAGS)" -o bin/trun ./cmd/trun

install:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/trun ./cmd/trun
	@echo "installed $(BIN_DIR)/trun ($(VERSION))"

test:
	go test ./...

vet:
	go vet ./...

uninstall:
	rm -f $(BIN_DIR)/trun
