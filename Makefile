# ============================================================
# URL Shortener Platform - Makefile
# ============================================================

.DEFAULT_GOAL := help

ifeq ($(OS),Windows_NT)
  ifneq ($(wildcard C:/Progra~1/Git/bin/bash.exe),)
    SHELL := C:/Progra~1/Git/bin/bash.exe
  else ifneq ($(wildcard C:/msys64/usr/bin/bash.exe),)
    SHELL := C:/msys64/usr/bin/bash.exe
  else
    SHELL := /bin/bash
  endif
else
  SHELL := /bin/bash
endif

BUILD_DIR    := ./bin
REGISTRY     := localhost:5001
CLUSTER_NAME := urlshortener

GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
GIT_TAG  := $(shell git describe --tags --always 2>/dev/null || echo "v0.0.0-dev")
BUILD_TS := $(shell git log -1 --format=%cI 2>/dev/null || echo "unknown")

LDFLAGS := -s -w \
	-X main.version=$(GIT_TAG) \
	-X main.commit=$(GIT_SHA) \
	-X main.buildTime=$(BUILD_TS)

.PHONY: help
help: ## Show available targets
	@python -c "import pathlib,re; [print('\033[36m{:<28}\033[0m {}'.format(m.group(1), m.group(2))) for line in pathlib.Path('Makefile').read_text().splitlines() for m in [re.match(r'^([A-Za-z_-]+):.*?## (.*)$$', line)] if m]"

.PHONY: build build-api build-redirector
build: build-api build-redirector ## Build all service binaries locally

build-api: ## Build api-service binary
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/api ./cmd/api

build-redirector: ## Build redirector binary
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/redirector ./cmd/redirector

.PHONY: run-api run-redirector run-issuer
run-api: ## Run api-service locally
	go run ./cmd/api

run-redirector: ## Run redirector locally
	go run ./cmd/redirector

run-issuer: ## Run local mock JWT issuer
	go run ./cmd/mockissuer

.PHONY: test test-unit test-integration test-coverage
test: test-unit ## Run unit tests

test-unit: ## Run unit tests with race detector
	go test -v -race -count=1 -short ./...

test-integration: ## Run integration tests
	go test -v -race -count=1 -tags=integration ./...

test-coverage: ## Generate coverage report
	go test -v -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: fmt vet lint tidy check
fmt: ## Format Go files
	gofmt -w -s .

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run --timeout=5m ./...

tidy: ## Tidy and verify modules
	go mod tidy
	go mod verify

check: fmt vet lint ## Run formatting, vet, and lint

.PHONY: infra-up infra-down infra-destroy infra-logs monitoring-up monitoring-down
infra-up: ## Start PostgreSQL and Redis
	docker compose -f docker-compose.dev.yml up -d

infra-down: ## Stop PostgreSQL and Redis
	docker compose -f docker-compose.dev.yml down

infra-destroy: ## Stop PostgreSQL and Redis and remove volumes
	docker compose -f docker-compose.dev.yml down -v

infra-logs: ## Tail PostgreSQL and Redis logs
	docker compose -f docker-compose.dev.yml logs -f

monitoring-up: ## Start local Prometheus and Grafana
	docker compose -f docker-compose.monitoring.yml up -d

monitoring-down: ## Stop local Prometheus and Grafana
	docker compose -f docker-compose.monitoring.yml down

.PHONY: migrate-up migrate-down migrate-status
migrate-up: ## Apply all pending migrations
	go run ./cmd/migrate up

migrate-down: ## Roll back last migration
	go run ./cmd/migrate down 1

migrate-status: ## Show migration status
	go run ./cmd/migrate status

.PHONY: registry-up registry-down registry-list
registry-up: ## Start local registry
	@if docker inspect urlshortener-registry >/dev/null 2>&1; then \
		docker start urlshortener-registry >/dev/null 2>&1 || true; \
	else \
		docker compose -f docker-compose.registry.yml up -d; \
	fi

registry-down: ## Stop local registry
	@if docker inspect urlshortener-registry >/dev/null 2>&1; then \
		docker stop urlshortener-registry >/dev/null 2>&1 || true; \
	else \
		docker compose -f docker-compose.registry.yml down; \
	fi

registry-list: ## List repositories in local registry
	curl -fsS http://$(REGISTRY)/v2/_catalog || true

.PHONY: docker-build docker-push docker-build-push
docker-build: ## Build all service images
	$(SHELL) scripts/build-images.sh $(GIT_SHA)

docker-push: ## Push all service images to local registry
	$(SHELL) scripts/push-images.sh $(GIT_SHA)

docker-build-push: docker-build docker-push ## Build and push all images

.PHONY: cluster-up cluster-up-no-deploy cluster-down cluster-status cluster-reset verify
cluster-up: ## Bootstrap kind cluster and deploy manifests when images exist
	$(SHELL) scripts/setup-kind.sh

cluster-up-no-deploy: ## Bootstrap kind cluster only
	$(SHELL) scripts/setup-kind.sh --skip-manifests

cluster-down: ## Delete kind cluster
	kind delete cluster --name $(CLUSTER_NAME)

cluster-status: ## Show cluster node and pod status
	kubectl get nodes -o wide
	kubectl get pods -A

cluster-reset: cluster-down cluster-up ## Recreate the kind cluster

verify: ## Verify registry, cluster, ingress, and metrics
	$(SHELL) scripts/verify-cluster.sh

.PHONY: deploy deploy-api deploy-redirector rollback
deploy: ## Deploy all services to Kubernetes
	$(SHELL) scripts/deploy.sh

deploy-api: ## Restart api deployment
	kubectl rollout restart deployment/api -n urlshortener
	kubectl rollout status deployment/api -n urlshortener --timeout=5m

deploy-redirector: ## Restart redirector deployment
	kubectl rollout restart deployment/redirector -n urlshortener
	kubectl rollout status deployment/redirector -n urlshortener --timeout=5m

rollback: ## Roll back api and redirector deployments
	kubectl rollout undo deployment/api -n urlshortener
	kubectl rollout undo deployment/redirector -n urlshortener

.PHONY: wso2-up wso2-down wso2-logs wso2-wait wso2-seed wso2-health wso2-reset wso2-shell
wso2-up: ## Start WSO2 API Manager
	docker compose -f docker-compose.wso2.yml up -d
	@echo "WSO2 starting; first boot usually takes 2-3 minutes"
	@echo "Monitor with: docker logs urlshortener-wso2 -f"

wso2-down: ## Stop WSO2 API Manager
	docker compose -f docker-compose.wso2.yml down

wso2-logs: ## Tail WSO2 logs
	docker logs urlshortener-wso2 -f

wso2-wait: ## Wait until WSO2 is ready
	@echo "Waiting for WSO2 to be ready..."
	@until curl -sk https://localhost:9443/services/Version | grep -qi version; do \
		echo -n "."; sleep 10; \
	done
	@echo ""
	@echo "WSO2 is ready"

wso2-seed: ## Import URL Shortener APIs into WSO2
	$(SHELL) scripts/seed-wso2.sh

wso2-health: ## Check WSO2 health and ingress reachability
	$(SHELL) scripts/wso2-health.sh

wso2-reset: ## Reconcile WSO2 APIs and recreate the dev app/subscriptions
	$(SHELL) scripts/seed-wso2.sh --reset

wso2-shell: ## Open a shell inside the WSO2 container
	docker exec -it urlshortener-wso2 /bin/bash

.PHONY: setup-hosts
setup-hosts: ## Add required local ingress hostnames to /etc/hosts
ifeq ($(OS),Windows_NT)
	@echo "Add this line to C:\\Windows\\System32\\drivers\\etc\\hosts as Administrator:"
	@echo "127.0.0.1 api.shortener.local r.shortener.local"
else
	@if ! grep -q "api.shortener.local" /etc/hosts; then \
		echo "127.0.0.1 api.shortener.local r.shortener.local" | sudo tee -a /etc/hosts; \
		echo "Added DNS entries"; \
	else \
		echo "DNS entries already present"; \
	fi
endif

.PHONY: wso2-start
wso2-start: wso2-up wso2-wait wso2-seed ## Start WSO2 and seed APIs

.PHONY: bootstrap full-deploy clean
bootstrap: registry-up cluster-up ## Start registry and bootstrap cluster

full-deploy: docker-build-push deploy ## Build, push, and deploy

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR) coverage.out coverage.html
