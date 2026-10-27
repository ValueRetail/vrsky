.PHONY: help build build-agent clean test test-db lint lint-tenant lint-connector lint-openapi fmt vet mod-tidy mod-verify info docker-build-ui up-core up-core-remote down-core

BLUE=\033[0;34m
GREEN=\033[0;32m
NC=\033[0m

# Delegate the Go targets to src. `help` prints src's list, then the
# local-stack targets this file owns (the ones with a `##` comment below).
help:
	@$(MAKE) -C src help
	@echo "$(BLUE)Local stack (repo root only)$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "$(GREEN)  %-20s$(NC)  %s\n", $$1, $$2}'
	@echo ""

build:
	@$(MAKE) -C src build

build-agent:
	@$(MAKE) -C src build-agent

test:
	@$(MAKE) -C src test

test-db:
	@$(MAKE) -C src test-db

fmt:
	@$(MAKE) -C src fmt

vet:
	@$(MAKE) -C src vet

lint:
	@$(MAKE) -C src lint

lint-tenant:
	@$(MAKE) -C src lint-tenant

lint-connector:
	@$(MAKE) -C src lint-connector

lint-openapi:
	@$(MAKE) -C src lint-openapi

mod-tidy:
	@$(MAKE) -C src mod-tidy

mod-verify:
	@$(MAKE) -C src mod-verify

clean:
	@$(MAKE) -C src clean

info:
	@$(MAKE) -C src info

docker-build-ui:
	@$(MAKE) -C src docker-build-ui

# The smallest compose set that runs a pipeline end to end: control plane,
# NATS, the generic webhook/http/file connectors, filter + converter, the
# Business Central pair, and httpbin — the sink a first pipeline points its
# HTTP destination at, without which every delivery 404s on DNS and lands in
# the DLQ. Fits a laptop with 8 GB; the full stack is 68 containers and does
# not. `make up-core` on a clean clone is the same as
# `docker compose up -d --build <list>`.
CORE_SERVICES := nats postgres-management management-api \
	webhook-consumer http-producer file-producer data-filter data-converter \
	business-central-consumer business-central-producer remote-agent httpbin

up-core: ## Start the core compose set (12 services, fits an 8 GB laptop)
	docker compose up -d --build $(CORE_SERVICES)

# The same set, with the webhook ingress reachable from other machines.
# VRSKY_BIND picks the address — a Tailscale 100.x address keeps it to your
# tailnet; unset answers on every interface. See docker-compose.remote.yml.
up-core-remote: ## The core set, with the webhook ingress reachable from other machines (VRSKY_BIND)
	docker compose -f docker-compose.yml -f docker-compose.remote.yml up -d --build $(CORE_SERVICES)

down-core: ## Stop the core compose set
	docker compose stop $(CORE_SERVICES)

.DEFAULT_GOAL := help
