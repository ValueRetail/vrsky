.PHONY: help build docker-build docker-push clean test run lint fmt vet mod-tidy mod-verify build-consumer docker-build-consumer docker-push-consumer run-consumer e2e-test

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

.DEFAULT_GOAL := help
