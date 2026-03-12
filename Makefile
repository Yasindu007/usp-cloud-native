# ============================================================
# URL Shortener Platform — Makefile
# ============================================================

.DEFAULT_GOAL := help
SHELL         := /bin/bash

# Build configuration
MODULE          := github.com/urlshortener/platform
BUILD_DIR       := ./bin
API_BINARY      := $(BUILD_DIR)/api
REDIRECT_BINARY := $(BUILD_DIR)/redirector

# Versioning (injected at build time)
GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
GIT_TAG  := $(shell git describe --tags --always 2>/dev/null || echo "v0.0.0-dev")
BUILD_TS := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X main.version=$(GIT_TAG) \
	-X main.commit=$(GIT_SHA) \
	-X main.buildTime=$(BUILD_TS)

# ============================================================
# HELP
# ============================================================

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-25s\033[0m %s\n", $$1, $$2}'

# ============================================================
# BUILD
# ============================================================

.PHONY: build build-api build-redirector

build: build-api build-redirector ## Build all service binaries

build-api: ## Build api-service binary
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(API_BINARY) ./cmd/api

build-redirector: ## Build redirector binary
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(REDIRECT_BINARY) ./cmd/redirector

# ============================================================
# RUN (local development)
# ============================================================

.PHONY: run-api run-redirector

run-api: ## Run api-service locally (requires infra-up)
	go run ./cmd/api

run-redirector: ## Run redirector locally (requires infra-up)
	go run ./cmd/redirector

# ============================================================
# TEST
# ============================================================

.PHONY: test test-unit test-integration test-coverage

test: test-unit ## Run unit tests (default)

test-unit: ## Run unit tests with race detector
	go test -v -race -count=1 -short ./...

test-integration: ## Run integration tests (requires infra-up)
	go test -v -race -count=1 -tags=integration ./...

test-coverage: ## Generate test coverage report
	go test -v -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# ============================================================
# CODE QUALITY
# ============================================================

.PHONY: lint vet fmt check

fmt: ## Format all Go source files
	gofmt -w -s .
	goimports -w .

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint (must be installed)
	golangci-lint run --timeout=5m ./...

check: fmt vet lint ## Run all code quality checks

# ============================================================
# DEPENDENCIES
# ============================================================

.PHONY: deps tidy

deps: ## Download dependencies
	go mod download

tidy: ## Tidy and verify modules
	go mod tidy
	go mod verify

# ============================================================
# LOCAL INFRASTRUCTURE (Docker Compose)
# ============================================================

.PHONY: infra-up infra-down infra-logs infra-ps

infra-up: ## Start local infrastructure (PostgreSQL + Redis)
	docker compose -f docker-compose.dev.yml up -d
	@echo "Waiting for services to be healthy..."
	@docker compose -f docker-compose.dev.yml ps

infra-down: ## Stop and remove local infrastructure
	docker compose -f docker-compose.dev.yml down

infra-destroy: ## Stop infrastructure and remove volumes
	docker compose -f docker-compose.dev.yml down -v

infra-logs: ## Tail infrastructure logs
	docker compose -f docker-compose.dev.yml logs -f

infra-ps: ## Show infrastructure status
	docker compose -f docker-compose.dev.yml ps

# ============================================================
# DATABASE
# ============================================================

.PHONY: migrate-up migrate-down migrate-status

migrate-up: ## Run all pending migrations
	go run ./cmd/migrate up

migrate-down: ## Roll back last migration
	go run ./cmd/migrate down 1

migrate-status: ## Show migration status
	go run ./cmd/migrate status

# ============================================================
# DOCKER
# ============================================================

.PHONY: docker-build docker-build-api docker-build-redirector

docker-build: docker-build-api docker-build-redirector ## Build all Docker images

docker-build-api: ## Build api-service Docker image
	docker build \
		--build-arg SERVICE=api \
		--build-arg VERSION=$(GIT_TAG) \
		-t urlshortener/api:$(GIT_TAG) \
		-t urlshortener/api:latest \
		.

docker-build-redirector: ## Build redirector Docker image
	docker build \
		--build-arg SERVICE=redirector \
		--build-arg VERSION=$(GIT_TAG) \
		-t urlshortener/redirector:$(GIT_TAG) \
		-t urlshortener/redirector:latest \
		.

# ============================================================
# CLEAN
# ============================================================

.PHONY: clean

clean: ## Remove build artifacts and coverage files
	rm -rf $(BUILD_DIR) coverage.out coverage.html