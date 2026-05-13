BINARY := procdeck
VERSION ?= dev
BUILD_DIR := tmp
LDFLAGS := -X main.version=$(VERSION)

.PHONY: fmt test test-race build release-darwin-arm64 release-darwin-amd64

fmt:
	go tool goimports -w .
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./internal/process ./internal/supervisor ./internal/tui

build:
	mkdir -p $(BUILD_DIR)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/procdeck

release-darwin-arm64:
	mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/procdeck

release-darwin-amd64:
	mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/procdeck
