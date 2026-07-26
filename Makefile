.PHONY: help build run test test-short test-coverage coverage-check lint fmt vet tidy clean \
	test-ui test-e2e test-all ui-install ui-build demo-guard

GO         := go
API_PKG    := ./panel-api/...
AGENT_PKG  := ./panel-agent/...
WIRE_PKG   := ./agentwire/...
ALL_PKG    := $(API_PKG) $(AGENT_PKG) $(WIRE_PKG)
BIN        := ./bin/jabali
AGENT_BIN  := ./bin/jabali-agent
COVER      := coverage.out
MIN_COV    := 80

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile both binaries (panel + agent)
	mkdir -p bin
	$(GO) build -o $(BIN) ./panel-api/cmd/server
	$(GO) build -o $(AGENT_BIN) ./panel-agent/cmd/jabali-agent
	$(GO) build -o bin/jabali-installer ./installer/cmd/jabali-installer

demo-guard: ## JAB-159: prod build must EXCLUDE demo code; demo build must INCLUDE it
	mkdir -p bin
	$(GO) build -tags demo -o bin/jabali-demo ./panel-api/cmd/server
	$(GO) build -o bin/jabali-prod ./panel-api/cmd/server
	@grep -aq 'demo_mode' bin/jabali-demo || { echo 'FAIL: demo build missing demo_mode marker'; exit 1; }
	@if grep -aq 'demo_mode' bin/jabali-prod; then echo 'FAIL: prod build leaked demo_mode symbol'; exit 1; fi
	@echo 'demo-guard OK: prod excludes demo_mode, demo build includes it'
	$(GO) test -tags demo -count=1 ./panel-api/internal/middleware/

run: ## Run the panel server (dev)
	$(GO) run ./panel-api/cmd/server

test: ## Run all Go tests across the workspace
	$(GO) test -race -count=1 $(ALL_PKG)

test-short: ## Run only fast unit tests (skip integration)
	$(GO) test -race -count=1 -short $(ALL_PKG)

test-coverage: ## Run tests with coverage (internal packages only)
	$(GO) test -race -count=1 -coverprofile=$(COVER) -covermode=atomic -coverpkg=./panel-api/internal/... ./panel-api/internal/...
	$(GO) tool cover -func=$(COVER) | tail -n 1

test-integration: ## Run integration tests (requires JABALI_TEST_DATABASE_URL + real MariaDB)
	$(GO) test -race -count=1 -tags=integration -coverprofile=$(COVER) -covermode=atomic -coverpkg=./panel-api/internal/... ./panel-api/internal/...
	$(GO) tool cover -func=$(COVER) | tail -n 1

coverage-check: ## Fail if combined (unit+integration) coverage below MIN_COV
	@if [ -z "$$JABALI_TEST_DATABASE_URL" ]; then \
		echo "coverage-check requires JABALI_TEST_DATABASE_URL (real MariaDB)"; \
		echo "  set it, or run 'make test-coverage' for unit-only (no gate)"; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory test-integration
	@pct=$$($(GO) tool cover -func=$(COVER) | awk '/total:/ {gsub("%","",$$3); print $$3}'); \
	awk -v p="$$pct" -v m="$(MIN_COV)" 'BEGIN { if (p+0 < m+0) { printf "coverage %s%% below %s%%\n", p, m; exit 1 } else { printf "coverage %s%% OK\n", p } }'

lint: ## Run golangci-lint across the workspace + install.sh phantom-function lint
	golangci-lint run $(ALL_PKG)
	tools/lint-install-sh.sh

fmt: ## Format all Go code
	$(GO) fmt $(ALL_PKG)

vet: ## Run go vet
	$(GO) vet $(ALL_PKG)

tidy: ## Tidy module deps
	$(GO) mod tidy

clean: ## Remove build artefacts
	rm -rf bin $(COVER)

# ---------- panel-ui (frontend) ----------

UI_DIR := panel-ui

ui-install: ## Install panel-ui npm deps (clean, reproducible)
	cd $(UI_DIR) && rm -rf node_modules && npm ci --no-audit --no-fund

ui-build: ## Build the panel-ui SPA (required before E2E — tests run against dist/)
	cd $(UI_DIR) && npm run build

test-ui: ## Run panel-ui unit tests (vitest)
	cd $(UI_DIR) && npx vitest run

test-e2e: ui-build ## Run Playwright E2E suite against the built SPA
	cd $(UI_DIR) && npx playwright test --project=chromium --reporter=list

test-all: test test-ui test-e2e ## Run everything: Go tests + vitest + Playwright

# ---------- aa-smoke (M40.1 AppArmor profile verifier) ----------

aa-smoke: ## Verify every loaded jabali AppArmor profile reaches its declared sockets (M40.1)
	bash tools/aa-smoke/run.sh
