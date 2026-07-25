BINARY := context-guru-proxy
PKG := github.com/rossoctl/context-guru
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT)

# CGO is required to compile the tree-sitter binding; a C toolchain (gcc/clang)
# must be present for make test/build/lint.
export CGO_ENABLED=1

.DEFAULT_GOAL := help

.PHONY: help
help: ## Display this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the context-guru-proxy binary into ./bin
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/context-guru-proxy

.PHONY: test
test: ## Run all tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run all tests with race + cross-package coverage; write coverage.out and print total
	go test -race -coverpkg=./... -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .
	go mod tidy

.PHONY: lint
lint: ## Run go vet and gofmt check
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:" && gofmt -l . && exit 1)

.PHONY: pre-commit
pre-commit: ## Install pre-commit hooks
	pre-commit install --install-hooks --hook-type commit-msg

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin
