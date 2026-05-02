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
NAMESPACE    := urlshortener

GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
GIT_TAG  := $(shell git describe --tags --always 2>/dev/null || echo "v0.0.0-dev")
BUILD_TS := $(shell git log -1 --format=%cI 2>/dev/null || date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X main.version=$(GIT_TAG) \
	-X main.commit=$(GIT_SHA) \
	-X main.buildTime=$(BUILD_TS)

.PHONY: help
help: ## Show available targets
	@python -c "import pathlib,re; [print('\033[36m{:<28}\033[0m {}'.format(m.group(1), m.group(2))) for line in pathlib.Path('Makefile').read_text().splitlines() for m in [re.match(r'^([A-Za-z0-9_-]+):.*?## (.*)$$', line)] if m]"

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

.PHONY: test test-unit test-integration test-all test-coverage
test: test-unit ## Run unit tests

test-unit: ## Run unit tests with race detector
	go test -v -race -count=1 -short ./...

test-integration: ## Run integration tests
	go test -v -race -count=1 -tags=integration ./...

test-all: ## Run unit and integration tests
	go test -v -race -count=1 ./...
	go test -v -race -count=1 -tags=integration ./...

test-coverage: ## Generate coverage report
	go test -v -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Report: coverage.html"

.PHONY: fmt vet lint tidy check
fmt: ## Format Go files
	gofmt -w -s .

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run --config=.golangci.yml --timeout=5m ./...

tidy: ## Tidy and verify modules
	go mod tidy
	go mod verify

check: fmt vet lint ## Run formatting, vet, and lint

.PHONY: security govulncheck trivy
security: govulncheck trivy ## Run Go and container security checks

govulncheck: ## Check Go modules for known vulnerabilities
	go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

trivy: ## Scan service images with Trivy
	trivy image --severity CRITICAL,HIGH $(REGISTRY)/urlshortener/api:latest
	trivy image --severity CRITICAL,HIGH $(REGISTRY)/urlshortener/redirector:latest
	trivy image --severity CRITICAL,HIGH $(REGISTRY)/urlshortener/migrate:latest

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
	@echo "Prometheus: http://localhost:9095"
	@echo "Grafana:    http://localhost:3000"

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
	@echo "Registry: http://$(REGISTRY)"

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

.PHONY: secrets-generate secrets-seal secrets-list secrets-backup
secrets-generate: ## Generate and apply SealedSecrets
	$(SHELL) scripts/generate-secrets.sh

secrets-seal: ## Encrypt one value with kubeseal
	@read -p "Secret name: " SNAME; \
	read -p "Key name: " KNAME; \
	read -sp "Value: " SVAL; echo; \
	$(SHELL) scripts/seal-secrets.sh "$$SNAME" "$$KNAME" "$$SVAL"

secrets-list: ## List Kubernetes secrets
	kubectl get secrets -n $(NAMESPACE)

secrets-backup: ## Export the Sealed Secrets controller key
	kubectl get secret -n kube-system \
		-l sealedsecrets.bitnami.com/sealed-secrets-key \
		-o yaml > sealing-key-backup-$(GIT_SHA).yaml
	@echo "Sealing key backed up to sealing-key-backup-$(GIT_SHA).yaml"

.PHONY: deploy deploy-api deploy-redirector rollback rollback-api rollback-redirector smoke-test
deploy: ## Deploy all services to Kubernetes
	$(SHELL) scripts/deploy.sh --image-tag $(GIT_SHA)

deploy-api: ## Rolling update api-service image
	kubectl set image deployment/api \
		api=$(REGISTRY)/urlshortener/api:$(GIT_SHA) \
		-n $(NAMESPACE)
	kubectl rollout status deployment/api -n $(NAMESPACE) --timeout=5m

deploy-redirector: ## Rolling update redirector image
	kubectl set image deployment/redirector \
		redirector=$(REGISTRY)/urlshortener/redirector:$(GIT_SHA) \
		-n $(NAMESPACE)
	kubectl rollout status deployment/redirector -n $(NAMESPACE) --timeout=5m

rollback: ## Roll back api and redirector deployments
	$(SHELL) scripts/rollback.sh

rollback-api: ## Roll back api deployment
	kubectl rollout undo deployment/api -n $(NAMESPACE)

rollback-redirector: ## Roll back redirector deployment
	kubectl rollout undo deployment/redirector -n $(NAMESPACE)

smoke-test: ## Run smoke tests against the local cluster ingress
	$(SHELL) scripts/smoke-test.sh http://api.shortener.local http://r.shortener.local

.PHONY: hpa-status pdb-status scale-status load-test-hpa
hpa-status: ## Show HorizontalPodAutoscaler status
	kubectl get hpa -n $(NAMESPACE)

pdb-status: ## Show PodDisruptionBudget status
	kubectl get pdb -n $(NAMESPACE)

scale-status: ## Show pod counts and HPA state
	@echo "==> Pods:"
	@kubectl get pods -n $(NAMESPACE) -l 'app in (api,redirector)' --no-headers | awk '{print $$1, $$3}'
	@echo ""
	@echo "==> HPA:"
	@kubectl get hpa -n $(NAMESPACE)

load-test-hpa: ## Trigger redirector HPA scale-up with an in-cluster load test
	@read -p "Short code to use (for example abc1234): " CODE; \
	$(SHELL) scripts/load-test-hpa.sh "$$CODE" 120

.PHONY: load-test load-seed load-redirect load-shorten load-spike load-soak load-stress load-slo load-stack-up load-stack-down slo-report slo-report-json
load-stack-up: ## Start InfluxDB and Grafana for k6 load-test observability
	docker compose -f docker-compose.k6.yml up -d
	@echo "InfluxDB: http://localhost:8086"
	@echo "Grafana:  http://localhost:3001 (admin/admin)"

load-stack-down: ## Stop the k6 observability stack
	docker compose -f docker-compose.k6.yml down

load-seed: ## Seed test short codes required before redirect tests
	$(SHELL) -lc "mkdir -p tests/load/results"
	$(SHELL) scripts/run-load-test.sh --seed

load-redirect: load-seed load-stack-up ## Run redirect baseline test (SLO gate: P99 < 50ms)
	$(SHELL) scripts/run-load-test.sh redirect_baseline

load-shorten: load-seed load-stack-up ## Run shorten baseline test (SLO gate: P99 < 200ms)
	$(SHELL) scripts/run-load-test.sh shorten_baseline

load-spike: load-seed load-stack-up ## Run viral spike simulation
	$(SHELL) scripts/run-load-test.sh spike_test

load-soak: load-seed load-stack-up ## Run 30-minute soak test
	$(SHELL) scripts/run-load-test.sh soak_test

load-stress: load-seed load-stack-up ## Run stress test to find capacity limits
	$(SHELL) scripts/run-load-test.sh stress_test

load-slo: load-seed load-stack-up ## Run full SLO validation test
	$(SHELL) scripts/run-load-test.sh slo_validation

load-test: load-seed load-stack-up ## Run redirect, shorten, and spike scenarios
	$(SHELL) scripts/run-load-test.sh --all

slo-report: ## Query Prometheus and print SLO status report
	$(SHELL) scripts/slo-report.sh

slo-report-json: ## Output SLO report as machine-readable JSON
	$(SHELL) scripts/slo-report.sh --json

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
	@echo "127.0.0.1 api.shortener.local r.shortener.local api.staging.shortener.local r.staging.shortener.local"
else
	@if ! grep -q "api.shortener.local" /etc/hosts; then \
		echo "127.0.0.1 api.shortener.local r.shortener.local api.staging.shortener.local r.staging.shortener.local" | sudo tee -a /etc/hosts; \
		echo "Added DNS entries"; \
	else \
		echo "DNS entries already present"; \
	fi
endif

.PHONY: wso2-start
wso2-start: wso2-up wso2-wait wso2-seed ## Start WSO2 and seed APIs

.PHONY: bootstrap full-deploy ci-local sre-local clean clean-load
bootstrap: registry-up cluster-up ## Start registry and bootstrap cluster

full-deploy: docker-build-push deploy smoke-test ## Build, push, deploy, and smoke test

ci-local: ## Simulate the CI pipeline locally
	@echo "==> Lint and static checks"
	@$(MAKE) check
	@echo "==> Unit tests"
	@$(MAKE) test-unit
	@echo "==> Docker build"
	@$(MAKE) docker-build
	@echo "==> Security scan"
	@$(MAKE) security || true
	@echo "CI simulation complete"

sre-local: ## Full SRE local workflow: monitoring, load SLO gate, and SLO report
	@echo "==> Starting monitoring stack"
	@$(MAKE) monitoring-up
	@echo "==> Running SLO validation test"
	@$(MAKE) load-slo
	@echo "==> Generating SLO report"
	@$(MAKE) slo-report
	@echo "SRE workflow complete"

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR) coverage.out coverage.html

clean-load: ## Remove load test results
	rm -rf tests/load/results/*.json tests/load/results/*.html
	@echo "Load test results cleared"
