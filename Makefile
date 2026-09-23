.PHONY: help build docker-build docker-push clean test run lint fmt vet mod-tidy mod-verify build-consumer docker-build-consumer docker-push-consumer run-consumer e2e-test up-core down-core

# Delegate all targets to src folder
help:
	@$(MAKE) -C src help

build:
	@$(MAKE) -C src build

docker-build:
	@$(MAKE) -C src docker-build

docker-push:
	@$(MAKE) -C src docker-push

run:
	@$(MAKE) -C src run

build-consumer:
	@$(MAKE) -C src build-consumer

docker-build-consumer:
	@$(MAKE) -C src docker-build-consumer

docker-push-consumer:
	@$(MAKE) -C src docker-push-consumer

run-consumer:
	@$(MAKE) -C src run-consumer

e2e-test:
	@$(MAKE) -C src e2e-test

test:
	@$(MAKE) -C src test

fmt:
	@$(MAKE) -C src fmt

vet:
	@$(MAKE) -C src vet

lint:
	@$(MAKE) -C src lint

mod-tidy:
	@$(MAKE) -C src mod-tidy

mod-verify:
	@$(MAKE) -C src mod-verify

clean:
	@$(MAKE) -C src clean

info:
	@$(MAKE) -C src info

# The smallest compose set that runs a pipeline end to end: control plane,
# NATS, the generic webhook/http/file connectors, filter + converter, the
# Business Central pair, and httpbin — the sink a first pipeline points its
# HTTP destination at, without which every delivery 404s on DNS and lands in
# the DLQ. Fits a laptop with 8 GB; the full stack is 68 containers and does
# not. `make up-core` on a clean clone is the same as
# `docker compose up -d --build <list>`.
CORE_SERVICES := nats postgres-management management-api \
	webhook-consumer http-producer file-producer data-filter data-converter \
	business-central-consumer business-central-producer httpbin

up-core:
	docker compose up -d --build $(CORE_SERVICES)

down-core:
	docker compose stop $(CORE_SERVICES)

.DEFAULT_GOAL := help
