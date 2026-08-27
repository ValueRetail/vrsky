.PHONY: help build build-filter build-converter docker-build-filter docker-build-converter docker-push-filter docker-push-converter docker-build-ui docker-push-ui clean test test-converter lint fmt vet

# Variables
BIN_DIR=./bin
GO=go
DOCKER_REGISTRY?=vrsky
DOCKER_TAG?=latest
GO_VERSION=$(shell go version | awk '{print $$3}')

# Colors for output
BLUE=\033[0;34m
GREEN=\033[0;32m
RED=\033[0;31m
NC=\033[0m # No Color

help: ## Show this help message
	@echo "$(BLUE)VRSky - Available Commands$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "$(GREEN)  %-20s$(NC) %s\n", $$1, $$2}'
	@echo ""

# The legacy `producer`/`consumer` binaries were retired in #205: the
# orchestrator no longer spawns per-connection workers, and every source and
# destination node type is served by a standing connector service instead
# (src/cmd/{file,http,db,cloud-storage,...}-{consumer,producer}).
build: ## Build every command to ./bin
	@echo "$(BLUE)Building all commands...$(NC)"
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/ ./cmd/...
	@echo "$(GREEN)✓ Binaries built into $(BIN_DIR)$(NC)"

build-filter: clean ## Build filter binary to ./bin/filter
	@echo "$(BLUE)Building filter binary...$(NC)"
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/filter ./cmd/filter
	@echo "$(GREEN)✓ Binary built: $(BIN_DIR)/filter$(NC)"

docker-build-filter: ## Build Docker image: vrsky/filter:latest
	@echo "$(BLUE)Building filter Docker image...$(NC)"
	@docker build -t vrsky/filter:latest -f cmd/filter/Dockerfile .
	@echo "$(GREEN)✓ Docker image built: vrsky/filter:latest$(NC)"

docker-push-filter: docker-build-filter ## Push filter Docker image to registry
	@echo "$(BLUE)Pushing filter Docker image...$(NC)"
	@docker push vrsky/filter:latest
	@echo "$(GREEN)✓ Filter Docker image pushed$(NC)"

build-converter: clean ## Build converter binary to ./bin/converter
	@echo "$(BLUE)Building converter binary...$(NC)"
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/converter ./cmd/converter
	@echo "$(GREEN)✓ Binary built: $(BIN_DIR)/converter$(NC)"

docker-build-converter: ## Build Docker image: vrsky/converter:latest
	@echo "$(BLUE)Building converter Docker image...$(NC)"
	@docker build -t vrsky/converter:latest -f cmd/converter/Dockerfile .
	@echo "$(GREEN)✓ Docker image built: vrsky/converter:latest$(NC)"

docker-push-converter: docker-build-converter ## Push converter Docker image to registry
	@echo "$(BLUE)Pushing converter Docker image...$(NC)"
	@docker push vrsky/converter:latest
	@echo "$(GREEN)✓ Converter Docker image pushed$(NC)"

test: ## Run Go tests
	@echo "$(BLUE)Running tests...$(NC)"
	@$(GO) test -v ./...
	@echo "$(GREEN)✓ Tests passed$(NC)"

test-converter: ## Run converter tests with coverage
	@echo "$(BLUE)Running converter tests...$(NC)"
	@$(GO) test -v -cover ./pkg/converter/...
	@echo "$(GREEN)✓ Converter tests passed$(NC)"

lint: lint-tenant lint-connector lint-openapi ## Run all linters (golangci-lint + tenant-filter + connector + openapi)
	@echo "$(BLUE)Running linter...$(NC)"
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "$(RED)golangci-lint not installed. Install with: brew install golangci-lint$(NC)"; \
	fi

lint-tenant: ## Check that every tenant-scoped query filters by tenant_id (#74)
	@echo "$(BLUE)Running tenant-filter lint...$(NC)"
	@$(GO) run ./cmd/lint-tenant-filter

lint-connector: ## Check that every SDK connector calls sdk.Run* and doesn't bypass the SDK (#98)
	@echo "$(BLUE)Running connector lint...$(NC)"
	@$(GO) run ./cmd/lint-connector

lint-openapi: ## Check that every served management-API route is in the OpenAPI registry (#94)
	@echo "$(BLUE)Running openapi lint...$(NC)"
	@$(GO) run ./cmd/lint-openapi

fmt: ## Format code with gofmt
	@echo "$(BLUE)Formatting code...$(NC)"
	@$(GO) fmt ./...
	@echo "$(GREEN)✓ Code formatted$(NC)"

vet: ## Run go vet
	@echo "$(BLUE)Running go vet...$(NC)"
	@$(GO) vet ./...
	@echo "$(GREEN)✓ Vet passed$(NC)"

mod-tidy: ## Run go mod tidy
	@echo "$(BLUE)Tidying modules...$(NC)"
	@$(GO) mod tidy
	@echo "$(GREEN)✓ Modules tidied$(NC)"

mod-verify: ## Verify module files
	@echo "$(BLUE)Verifying modules...$(NC)"
	@$(GO) mod verify
	@echo "$(GREEN)✓ Modules verified$(NC)"

clean: ## Clean build artifacts
	@echo "$(BLUE)Cleaning build artifacts...$(NC)"
	@rm -rf $(BIN_DIR)
	@$(GO) clean -testcache
	@echo "$(GREEN)✓ Clean complete$(NC)"

info: ## Show build information
	@echo "$(BLUE)Build Information:$(NC)"
	@echo "  Go Version:      $(GO_VERSION)"
	@echo "  Binary Dir:      $(BIN_DIR)"
	@echo "  Docker Registry: $(DOCKER_REGISTRY)"
	@echo ""

docker-build-ui: ## Build UI Docker image: vrsky/ui:latest
	@echo "$(BLUE)Building UI Docker image...$(NC)"
	@docker build -t vrsky/ui:latest ../ui
	@echo "$(GREEN)✓ UI Docker image built: vrsky/ui:latest$(NC)"

docker-push-ui: docker-build-ui ## Push UI Docker image to registry
	@echo "$(BLUE)Pushing UI Docker image...$(NC)"
	@docker push vrsky/ui:latest
	@echo "$(GREEN)✓ UI Docker image pushed$(NC)"

.DEFAULT_GOAL := help
