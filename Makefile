APP_NAME    := gogogo-fullstack-template
APP_DIR     := cmd/web
PORT        ?= 8080
VERSION     := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME   := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS     := -ldflags="-w -X main.Version=$(VERSION) -X main.CommitHash=$(COMMIT) -X main.BuildTime=$(BUILDTIME)"

.PHONY: all build build-jetstream build-dagnats build-all desktop wails-build run clean restart templ fmt css css-install datastar-lint test lint vet check-sizes deadcode check deps dev docker-image setup help

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

build-dagnats: templ
	@echo "→ Building $(APP_NAME) (with DagNats durable workflows) v$(VERSION)..."
	@go build -tags dagnats $(LDFLAGS) -o $(APP_NAME) ./$(APP_DIR)

build-all: templ
	@echo "→ Building $(APP_NAME) (JetStream + DagNats) v$(VERSION)..."
	@go build -tags "jetstream dagnats" $(LDFLAGS) -o $(APP_NAME) ./$(APP_DIR)

desktop: templ
	@echo "→ Building desktop shell (Wails v3 + Leaf Node) v$(VERSION)..."
	@go build -tags jetstream $(LDFLAGS) -o gogogo-desktop ./cmd/desktop

wails-build: templ
	@echo "→ wails build (requires wails CLI: go install github.com/wailsapp/wails/v3/cmd/wails@latest)"...
	@wails build -app ./cmd/desktop -config ./wails.json -tags jetstream

run:
	@echo "→ Starting $(APP_NAME) on port $(PORT)..."
	@PORT=$(PORT) ./$(APP_NAME)

clean:
	@echo "→ Cleaning..."
	@rm -f $(APP_NAME)
	@find . -name '*.log' -delete

test:
	@go test -race ./... -count=1

# css-install installs the npm dev dependencies (Tailwind CLI + DaisyUI
# v5). Idempotent. Run once after cloning; CI calls this in the
# Docker build stage so contributors don't need to.
css-install:
	@echo "→ Installing CSS build dependencies (tailwindcss v4 + daisyui v5)..."
	@npm install --silent
	@echo "  ✓ installed"

# css runs the Tailwind v4 CLI to build web/resources/static/app.min.css
# from src/css/input.css. Run this whenever you add new utility
# classes in .templ files; the pre-commit hook also runs it on
# .templ changes to catch stale CSS before commit.
css: css-install
	@echo "→ Building CSS (Tailwind v4 + DaisyUI v5)..."
	@npm run build --silent
	@echo "  ✓ built web/resources/static/app.min.css"

# css-check fails if the generated CSS is out-of-date. Used by the
# pre-commit hook and CI to catch forgotten rebuilds.
css-check: css
	@git diff --quiet --exit-code web/resources/static/app.min.css || (echo "  ❌ CSS out of date. Run \`make css\` and re-commit."; exit 1)
	@echo "  ✓ CSS is up to date"

test-jetstream:
	@go test -race -tags jetstream ./... -count=1

# DagNats boots an embedded NATS + durable-workflow engine per package
# that tests it; running those packages in parallel under -race starves
# the engine and causes flaky timeouts. -p 1 serializes packages so the
# engine always gets enough CPU to complete runs within the test timeout.
test-dagnats:
	@go test -race -tags dagnats -p 1 ./... -count=1

test-combined:
	@go test -race -tags "jetstream dagnats" -p 1 ./... -count=1

# fmt checks formatting with gofumpt + goimports (no --fast shortcuts).
fmt:
	@echo "→ Checking formatting (gofumpt + goimports)..."
	@test -z "$$(gofumpt -l .)" || (echo "  ❌ gofumpt issues:"; gofumpt -l .; exit 1)
	@test -z "$$(goimports -l -local github.com/calionauta/gogogo-fullstack-template $$(find . -name '*.go' ! -name '*_templ.go'))" || (echo "  ❌ goimports issues"; goimports -l -local github.com/calionauta/gogogo-fullstack-template $$(find . -name '*.go' ! -name '*_templ.go'); exit 1)
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
# scans dead code, runs the full race test suite, and verifies the
# generated CSS is up to date. make setup installs the blocking
# pre-commit hook that enforces the same gate on every commit.
check: fmt datastar-lint css-check lint check-sizes deadcode test test-combined
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
	@echo "  build-dagnats  Build with DagNats durable workflow support"
	@echo "  build-all      Build with JetStream + DagNats"
	@echo "  fmt            Check formatting (gofumpt + goimports)"
	@echo "  datastar-lint  Lint .templ files for Datastar anti-patterns"
	@echo "  test           Run tests with race detector"
	@echo "  lint           Run go vet + golangci-lint (full)"
	@echo "  check-sizes    Check file/function size limits"
	@echo "  deadcode       Scan for dead code"
	@echo "  check          Run all checks (fmt + datastar-lint + css-check + lint + sizes + deadcode + test)"
	@echo "  css            Build app.min.css from src/css/input.css (Tailwind v4 + DaisyUI v5)"
	@echo "  css-install    Install CSS build dependencies (npm)"
	@echo "  dev            Live reload with Air"
	@echo "  templ          Generate Templ components"
	@echo "  deps           go mod tidy"
	@echo "  setup          Install git hooks"
	@echo "  docker-image   Build and push Docker image"
