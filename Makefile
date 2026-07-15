APP_NAME    := gogogo-fullstack-template
APP_DIR     := cmd/web
PORT        ?= 8080
VERSION     := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME   := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS     := -ldflags="-w -X main.Version=$(VERSION) -X main.CommitHash=$(COMMIT) -X main.BuildTime=$(BUILDTIME)"
TAGS        := -tags "no_default_driver"

.PHONY: all build desktop wails-build run clean restart templ fmt css css-install datastar-lint test lint vet check-sizes deadcode ci-local signoff deps dev docker-image setup help

all: build

templ:
	@echo "→ Generating templ files..."
	@go tool templ generate

build: templ
	@echo "→ Building $(APP_NAME) v$(VERSION)..."
	@go build $(LDFLAGS) $(TAGS) -o $(APP_NAME) ./$(APP_DIR)

desktop: templ
	@echo "→ Building desktop shell (Wails v3 + Leaf Node) v$(VERSION)..."
	@go build $(LDFLAGS) $(TAGS) -o gogogo-desktop ./cmd/desktop

wails-build: templ
	@echo "→ wails build (requires wails CLI: go install github.com/wailsapp/wails/v3/cmd/wails@latest)"...
	@wails3 build

run:
	@echo "→ Starting $(APP_NAME) on port $(PORT)..."
	@PORT=$(PORT) ./$(APP_NAME)

clean:
	@echo "→ Cleaning..."
	@rm -f $(APP_NAME)
	@find . -name '*.log' -delete

test:
	# DagNats boots an embedded NATS + durable-workflow engine per package
	# that tests it; running those packages in parallel under -race starves
	# the engine and causes flaky timeouts. -p 1 serializes packages so the
	# engine always gets enough CPU to complete runs within the test timeout.
	@go test -race -p 1 ./... -count=1

# css-install installs the npm dev dependencies (Tailwind CLI + DaisyUI
# v5). Idempotent. Run once after cloning; CI calls this in the
# Docker build stage so contributors don't need to.
css-install:
	@echo "→ Installing CSS build dependencies (tailwindcss v4 + daisyui v5)..."
	@npm ci --silent
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

# fmt checks formatting with gofumpt + goimports (no --fast shortcuts).
fmt:
	@echo "→ Checking formatting (gofumpt + goimports)..."
	@test -z "$$(gofumpt -l .)" || (echo "  ❌ gofumpt issues:"; gofumpt -l .; exit 1)
	@test -z "$$(goimports -l -local github.com/calionauta/gogogo-fullstack-template $$(find . -name '*.go' ! -name '*_templ.go'))" || (echo "  ❌ goimports issues"; goimports -l -local github.com/calionauta/gogogo-fullstack-template $$(find . -name '*.go' ! -name '*_templ.go'); exit 1)
	@echo "  ✅ formatting clean"

# datastar-lint checks .templ files for Datastar anti-patterns. Runs with
# -only-errors so real issues fail the gate while intentional custom
# attributes (e.g. data-tool/data-doc-id, read by our whiteboard JS) are
# whitelisted via .datastar-lint.json instead of being flagged.
# Scoped to ./features (where .templ live) so the every-save Air pre_cmd
# does not walk node_modules.
datastar-lint:
	@echo "→ Running datastar-lint..."
	@bin/datastar-lint -only-errors -r ./features

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
# ci-local runs the same quality gate as CI but locally, so you can
# catch issues before pushing. Runs lint, tests (-p 1 for DagNats
# engine stability), and a single unified build — no more tag matrix.
ci-local: templ datastar-lint css-check
	@echo "→ lint (golangci-lint, same as CI)"
	@if which golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "  ❌ golangci-lint not installed (brew install golangci-lint)"; exit 1; fi
	@echo "→ tests (unified, -p 1 for DagNats engine stability)"
	@go test -race -p 1 ./... -count=1
	@echo "→ build"
	@go build $(LDFLAGS) $(TAGS) -o /dev/null ./cmd/web/
	@echo "✅ ci-local passed"

# signoff runs the full local CI then stamps the current commit green via
# the gh-signoff extension (basecamp/gh-signoff). This lets you skip
# waiting on remote runners for the common case. NOTE: our deploy triggers
# on push to master (not PR merge), so the signoff status is ADVISORY
# here — we deliberately do NOT run `gh signoff install` (which would
# require the status for PR merge and is meaningless for push-to-deploy).
# If/when we move to a PR-based flow, enable `gh signoff install` too.
signoff: ci-local
	@echo "→ stamping commit green via gh signoff..."
	@gh signoff -f
	@echo "✅ signed off — safe to push"

.PHONY: ci-local signoff

deps:
	@go mod tidy

setup:
	@bin/setup-hooks.sh

dev:
	@echo "→ Starting Air live reload (ENVIRONMENT=development unless already set)..."
	@ENVIRONMENT=$${ENVIRONMENT:-development} air

docker-image: templ
	@echo "→ Building Docker image..."
	docker buildx build --platform=linux/amd64,linux/arm64 \
		-t ghcr.io/calionauta/$(APP_NAME):latest \
		-t ghcr.io/calionauta/$(APP_NAME):$(VERSION) \
		--push .

coverage:
	@echo "→ Running tests with coverage..."
	@go test -race -p 1 ./... -count=1 -coverprofile=coverage.out -covermode=atomic
	@go tool cover -func=coverage.out | sort -k3 -r | head -30
	@echo "---"
	@go tool cover -html=coverage.out -o coverage.html
	@echo "→ Full report: coverage.html"
	@rm -f coverage.out

help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@echo "  build          Build binary (unified: everything included)"
	@echo "  desktop        Build desktop shell (Wails v3)"
	@echo "  fmt            Check formatting (gofumpt + goimports)"
	@echo "  datastar-lint  Lint .templ files for Datastar anti-patterns"
	@echo "  test           Run tests with race detector"
	@echo "  coverage       Run tests with coverage report (HTML)"
	@echo "  lint           Run go vet + golangci-lint (full)"
	@echo "  check-sizes    Check file/function size limits"
	@echo "  deadcode       Scan for dead code"

	@echo "  css            Build app.min.css from src/css/input.css (Tailwind v4 + DaisyUI v5)"
	@echo "  css-install    Install CSS build dependencies (npm)"
	@echo "  dev            Live reload with Air"
	@echo "  templ          Generate Templ components"
	@echo "  deps           go mod tidy"
	@echo "  setup          Install git hooks"
	@echo "  docker-image   Build and push Docker image"
