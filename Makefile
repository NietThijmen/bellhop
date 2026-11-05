# Thin wrapper for Unix users; Taskfile.yml is the canonical task runner.
PKG     := github.com/nietthijmen/bellhop
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/pkg/version.Version=$(VERSION)
CMDS    := bellhopd bellhop bellhop-helper
TARGETS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64
export CGO_ENABLED := 0

.PHONY: all check lint test test-race test-integration build cross
all: check
check: lint test build
lint:
	golangci-lint run ./...
test:
	go test ./...
test-race:
	CGO_ENABLED=1 go test -race ./...
test-integration:
	go test -tags integration ./...
build:
	@for c in $(CMDS); do go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$c ./cmd/$$c || exit 1; done
cross:
	@for t in $(TARGETS); do echo "== $$t"; GOOS=$${t%/*} GOARCH=$${t#*/} go build ./internal/... ./pkg/... ./cmd/bellhopd ./cmd/bellhop ./cmd/bellhop-helper || exit 1; done
