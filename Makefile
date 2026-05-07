# Makefile — top-level menu for radius-manager-api work.
#
# Run `make help` for the list. All targets are POSIX-compatible; no
# bashisms, no GNU-specific make features.

GO ?= go
COMPOSE_DEV  := docker compose -f docker-compose.dev.yml
COMPOSE_TEST := docker compose -f docker-compose.test.yml

.DEFAULT_GOAL := help

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ---- Build / test ----------------------------------------------------------

build: ## Build the radius-manager-api binary into ./bin/
	mkdir -p bin
	$(GO) build -o bin/radius-manager-api ./cmd/radius-manager-api
	@echo "→ bin/radius-manager-api"

test: ## Run unit tests (no infra dependencies).
	$(GO) test ./...

test-race: ## Run unit tests with the race detector.
	$(GO) test -race ./...

test-integration: ## Run MariaDB integration tests via testcontainers (requires Docker).
	$(GO) test -tags=integration -timeout=180s ./internal/manager/...

test-integration-compose: ## Run integration tests against docker-compose.test.yml MariaDB.
	$(COMPOSE_TEST) up -d
	@printf "waiting for mariadb..."
	@until docker exec rm-api-mariadb-test healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1; do printf "."; sleep 1; done
	@echo " ready."
	RM_TEST_DB_DSN='root:testrootpw@tcp(127.0.0.1:13306)/' \
	  $(GO) test -tags=integration -timeout=60s ./internal/manager/...
	$(COMPOSE_TEST) down -v

vet: ## Run go vet.
	$(GO) vet ./...

# ---- Docker dev stack ------------------------------------------------------

docker-up: ## Build + start the full Docker dev stack (mariadb + rm-api).
	$(COMPOSE_DEV) up -d --build

docker-down: ## Stop the dev stack but keep volumes.
	$(COMPOSE_DEV) down

docker-clean: ## Stop the dev stack and wipe all volumes.
	$(COMPOSE_DEV) down -v

docker-logs: ## Tail rm-api logs from the dev stack.
	$(COMPOSE_DEV) logs -f rm-api

docker-shell: ## Open a shell inside the rm-api container.
	$(COMPOSE_DEV) exec rm-api bash

docker-token: ## Print the API token from the running rm-api container.
	@$(COMPOSE_DEV) exec -T rm-api cat /etc/radius-manager-api/token

docker-supervisor: ## Show supervisord status inside the container.
	$(COMPOSE_DEV) exec rm-api supervisorctl status

# ---- End-to-end ------------------------------------------------------------

e2e: ## Bring up the dev stack, run the smoke script, and tear down.
	./scripts/e2e-smoke.sh

# ---- Hygiene ---------------------------------------------------------------

tidy: ## go mod tidy.
	$(GO) mod tidy

fmt: ## go fmt.
	$(GO) fmt ./...

clean: ## Remove build artifacts.
	rm -rf bin
	$(GO) clean -testcache

.PHONY: help build test test-race test-integration test-integration-compose vet \
        docker-up docker-down docker-clean docker-logs docker-shell docker-token docker-supervisor \
        e2e tidy fmt clean
