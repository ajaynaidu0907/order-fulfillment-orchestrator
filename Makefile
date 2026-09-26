# Order Fulfillment Orchestrator - developer tasks.
# Run `make help` for the list.

GO ?= go

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS=":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Resolve and pin module dependencies
	$(GO) mod tidy

.PHONY: build
build: ## Compile all packages and binaries
	$(GO) build ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: test
test: ## Run unit tests (fast, no Docker)
	$(GO) test ./... -short -count=1

.PHONY: test-integration
test-integration: ## Run integration/e2e tests (needs Docker for Testcontainers)
	$(GO) test ./test/... -count=1 -timeout 600s

.PHONY: run
run: ## Build and start the full stack in the background
	docker compose up --build -d

.PHONY: up
up: ## Start the full stack in the foreground
	docker compose up --build

.PHONY: stop
stop: ## Stop the stack (keep data volumes)
	docker compose down

.PHONY: clean
clean: ## Stop the stack and delete data volumes
	docker compose down -v

.PHONY: logs
logs: ## Tail logs for all services
	docker compose logs -f

.PHONY: migrate
migrate: ## Apply database migrations via the one-shot container
	docker compose run --rm migrate

.PHONY: seed
seed: ## Load synthetic mock data into the database
	docker compose run --rm seed

.PHONY: ps
ps: ## Show running services
	docker compose ps
