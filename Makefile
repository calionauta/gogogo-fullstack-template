APP_NAME    := gogogo-template
APP_DIR     := cmd/web
PORT        ?= 8080
VERSION     := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME   := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS     := -ldflags="-w -X main.Version=$(VERSION) -X main.CommitHash=$(COMMIT) -X main.BuildTime=$(BUILDTIME)"

.PHONY: all build build-jetstream build-turbine build-all run clean restart templ fmt datastar-lint test lint vet check-sizes deadcode check deps dev docker-image setup help

all: build

templ:
	@echo "→ Generating templ files..."
	@go tool templ generate

build: templ
	@echo "→ Building $(APP_NAME) v$(VERSION)..."
	@go build $(LDFLAGS) -o $(APP_NAME) ./$(APP_DIR)

build-jetstream: templ
	@echo "→ Building $(APP_NAME) (with JetStream) v$(VERSION)..."
	@go build -tags jetstream $(LDFLAGS) -o $(APP_NAME) ./$(APP_DIR)

build-turbine: templ
	@echo "→ Building $(APP_NAME) (with Turbine workflows) v$(VERSION)..."
	@go build -tags turbine $(LDFLAGS) -o $(APP_NAME) ./$(APP_DIR)

build-all: templ
	@echo "→ Building $(APP_NAME) (JetStream + Turbine) v$(VERSION)..."
	@go build -tags "jetstream turbine" $(LDFLAGS) -o $(APP_NAME) ./$(APP_DIR)

run:
	@echo "→ Starting $(APP_NAME) on port $(PORT)..."
	@PORT=$(PORT) ./$(APP_NAME)

clean:
	@echo "→ Cleaning..."
	@rm -f $(APP_NAME)
	@find . -name '*.log' -delete

test:
	@go test -race ./... -count=1

test-jetstream:
	@go test -race -tags jetstream ./... -count=1

test-turbine:
	@go test -race -tags turbine ./... -count=1

# fmt checks formatting with gofumpt + goimports (no --fast shortcuts).
fmt:
	@echo "→ Checking formatting (gofumpt + goimports)..."
	@test -z "$$(gofumpt -l .)" || (echo "  ❌ gofumpt issues:"; gofumpt -l .; exit 1)
	@test -z "$$(goimports -l -local github.com/calionauta/gogogo-template $$(find . -name '*.go' ! -name '*_templ.go'))" || (echo "  ❌ goimports issues"; goimports -l -local github.com/calionauta/gogogo-template $$(find . -name '*.go' ! -name '*_templ.go'); exit 1)
	@echo "  ✅ formatting clean"

# datastar-lint checks .templ files for Datastar anti-patterns.
datastar-lint:
	@echo "→ Running datastar-lint..."
	@bin/datastar-lint

lint:
	@echo "→ go vet..."
	@go vet ./...
	@echo "→ golangci-lint (full, no --fast)..."
	@if which golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "  ❌ golangci-lint not installed (brew install golangci-lint)"; exit 1; fi

check-sizes:
	@mkdir -p .githooks
	@[ -f .githooks/check-sizes.sh ] || bin/setup-hooks.sh >/dev/null 2>&1
	@.githooks/check-sizes.sh

deadcode:
	@which deadcode >/dev/null 2>&1 && deadcode -test ./... || echo "  (deadcode not installed, run: go install golang.org/x/tools/cmd/deadcode@latest)"

# check is the single quality gate. Run it after EVERY significant edit,
# not just before commit. It formats, lints .templ, vets, lints, sizes,
# scans dead code, and runs the full race test suite. make setup installs
# the blocking pre-commit hook that enforces the same gate on every commit.
check: fmt datastar-lint lint check-sizes deadcode test
	@echo "✅ All checks passed"

deps:
	@go mod tidy

setup:
	@bin/setup-hooks.sh

dev:
	@echo "→ Starting Air live reload..."
	@air

docker-image: templ
	@echo "→ Building Docker image..."
	docker buildx build --platform=linux/amd64,linux/arm64 \
		-t ghcr.io/calionauta/$(APP_NAME):latest \
		-t ghcr.io/calionauta/$(APP_NAME):$(VERSION) \
		--push .

help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@echo "  build          Build binary (runs templ generate first)"
	@echo "  build-jetstream Build with JetStream support"
	@echo "  build-turbine  Build with Turbine workflow support"
	@echo "  build-all      Build with JetStream + Turbine"
	@echo "  fmt            Check formatting (gofumpt + goimports)"
	@echo "  datastar-lint  Lint .templ files for Datastar anti-patterns"
	@echo "  test           Run tests with race detector"
	@echo "  lint           Run go vet + golangci-lint (full)"
	@echo "  check-sizes    Check file/function size limits"
	@echo "  deadcode       Scan for dead code"
	@echo "  check          Run all checks (fmt + datastar-lint + lint + sizes + deadcode + test)"
	@echo "  dev            Live reload with Air"
	@echo "  templ          Generate Templ components"
	@echo "  deps           go mod tidy"
	@echo "  setup          Install git hooks"
	@echo "  docker-image   Build and push Docker image"
