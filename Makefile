APP := easy-proxies
CMD := ./cmd/easy_proxies
DIST_DIR := dist
BUILD_TAGS := with_utls with_quic with_grpc with_wireguard with_gvisor

GO ?= go
HOST_OS ?= $(shell $(GO) env GOHOSTOS)
HOST_ARCH ?= $(shell $(GO) env GOHOSTARCH)

ifeq ($(HOST_OS),windows)
HOST_EXT := .exe
else
HOST_EXT :=
endif

HOST_BIN := $(DIST_DIR)/$(APP)-$(HOST_OS)-$(HOST_ARCH)$(HOST_EXT)

.PHONY: help build test run fmt vet clean \
	build-linux-amd64 build-linux-arm64 \
	build-darwin-amd64 build-darwin-arm64 \
	build-windows-amd64 build-all

help:
	@echo "Targets:"
	@echo "  make build               - Build current host binary to $(HOST_BIN)"
	@echo "  make test                - Run all tests"
	@echo "  make run                 - Run with config.yaml (with tags)"
	@echo "  make fmt                 - Format Go code"
	@echo "  make vet                 - Run go vet"
	@echo "  make clean               - Remove build artifacts"
	@echo "  make build-linux-amd64   - Build linux amd64 binary"
	@echo "  make build-linux-arm64   - Build linux arm64 binary"
	@echo "  make build-darwin-amd64  - Build macOS amd64 binary"
	@echo "  make build-darwin-arm64  - Build macOS arm64 binary"
	@echo "  make build-windows-amd64 - Build windows amd64 binary"
	@echo "  make build-all           - Build all release binaries"

build:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) $(GO) build -tags "$(BUILD_TAGS)" -o $(HOST_BIN) $(CMD)

test:
	$(GO) test ./...

run:
	$(GO) run -tags "$(BUILD_TAGS)" $(CMD) -config config.yaml

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

build-linux-amd64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -tags "$(BUILD_TAGS)" -o $(DIST_DIR)/$(APP)-linux-amd64 $(CMD)

build-linux-arm64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -tags "$(BUILD_TAGS)" -o $(DIST_DIR)/$(APP)-linux-arm64 $(CMD)

build-darwin-amd64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -tags "$(BUILD_TAGS)" -o $(DIST_DIR)/$(APP)-darwin-amd64 $(CMD)

build-darwin-arm64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -tags "$(BUILD_TAGS)" -o $(DIST_DIR)/$(APP)-darwin-arm64 $(CMD)

build-windows-amd64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -tags "$(BUILD_TAGS)" -o $(DIST_DIR)/$(APP)-windows-amd64.exe $(CMD)

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64

clean:
	rm -rf $(DIST_DIR)
