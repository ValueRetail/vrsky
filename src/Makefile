.PHONY: help build build-consumer docker-build docker-build-consumer docker-push docker-push-consumer clean test run run-consumer lint fmt vet e2e-test

# Variables
BINARY_NAME=producer
BIN_DIR=./bin
GO=go
DOCKER_REGISTRY?=vrsky
DOCKER_IMAGE?=$(DOCKER_REGISTRY)/producer
DOCKER_TAG?=latest
GO_VERSION=$(shell go version | awk '{print $$3}')

# Colors for output
BLUE=\033[0;34m
GREEN=\033[0;32m
RED=\033[0;31m
NC=\033[0m # No Color

help: ## Show this help message
	@echo "$(BLUE)VRSky Producer & Consumer - Available Commands$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "$(GREEN)  %-20s$(NC) %s\n", $$1, $$2}'
	@echo ""

build: clean ## Build producer binary to ./bin/producer
	@echo "$(BLUE)Building producer binary...$(NC)"
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/$(BINARY_NAME) ./cmd/producer
	@echo "$(GREEN)✓ Binary built: $(BIN_DIR)/$(BINARY_NAME)$(NC)"

docker-build: ## Build Docker image: $(DOCKER_IMAGE):$(DOCKER_TAG)
	@echo "$(BLUE)Building Docker image...$(NC)"
	@docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) -t $(DOCKER_IMAGE):latest -f cmd/producer/Dockerfile .
	@echo "$(GREEN)✓ Docker image built: $(DOCKER_IMAGE):$(DOCKER_TAG)$(NC)"

docker-push: docker-build ## Push Docker image to registry
	@echo "$(BLUE)Pushing Docker image to registry...$(NC)"
	@docker push $(DOCKER_IMAGE):$(DOCKER_TAG)
	@docker push $(DOCKER_IMAGE):latest
	@echo "$(GREEN)✓ Docker image pushed$(NC)"

run: build ## Build and run producer locally
	@echo "$(BLUE)Running producer...$(NC)"
	@$(BIN_DIR)/$(BINARY_NAME)

build-consumer: clean ## Build consumer binary to ./bin/consumer
	@echo "$(BLUE)Building consumer binary...$(NC)"
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/consumer ./cmd/consumer/basic
	@echo "$(GREEN)✓ Binary built: $(BIN_DIR)/consumer$(NC)"

docker-build-consumer: ## Build Docker image: vrsky/consumer:latest
	@echo "$(BLUE)Building consumer Docker image...$(NC)"
	@docker build -t vrsky/consumer:latest -f cmd/consumer/Dockerfile .
	@echo "$(GREEN)✓ Docker image built: vrsky/consumer:latest$(NC)"

docker-push-consumer: docker-build-consumer ## Push consumer Docker image to registry
	@echo "$(BLUE)Pushing consumer Docker image...$(NC)"
	@docker push vrsky/consumer:latest
	@echo "$(GREEN)✓ Consumer Docker image pushed$(NC)"

run-consumer: build-consumer ## Build and run consumer locally
	@echo "$(BLUE)Running consumer...$(NC)"
	@INPUT_TYPE=http INPUT_CONFIG='{"port":"8000"}' \
	 OUTPUT_TYPE=nats OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test.messages"}' \
	 ./bin/consumer

test: ## Run Go tests
	@echo "$(BLUE)Running tests...$(NC)"
	@$(GO) test -v ./...
	@echo "$(GREEN)✓ Tests passed$(NC)"

lint: ## Run linter (golangci-lint)
	@echo "$(BLUE)Running linter...$(NC)"
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "$(RED)golangci-lint not installed. Install with: brew install golangci-lint$(NC)"; \
	fi

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
	@echo "  Binary Name:     $(BINARY_NAME)"
	@echo "  Binary Path:     $(BIN_DIR)/$(BINARY_NAME)"
	@echo "  Docker Image:    $(DOCKER_IMAGE):$(DOCKER_TAG)"
	@echo "  Docker Registry: $(DOCKER_REGISTRY)"
	@echo ""

e2e-test: build build-consumer ## Run full end-to-end test (HTTP → NATS → HTTP)
	@echo "$(BLUE)Running end-to-end test...$(NC)"
	@bash ../scripts/e2e-test.sh

.DEFAULT_GOAL := help
