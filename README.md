# usp-cloud-native
Production-grade URL Shortener Platform built in Go,  designed with SRE principles, clean architecture, Kubernetes orchestration,  WSO2 API Manager integration, and full observability (Prometheus, OpenTelemetry).  Implements a formal PRD with SLIs/SLOs and enterprise reliability standards.

# URL Shortener Platform

Production-grade, cloud-native URL shortener built in Go.
Designed per PRD-USP-001 v1.0.0. Runs 100% locally using free tools.

## Architecture
WSO2 APIM (Gateway)
│
▼
NGINX Ingress (Kubernetes)
│
├── api-service      (Port 8080) — CRUD, auth, workspace management
└── redirect-service (Port 8081) — Short code resolution, HTTP 302
Shared Data Layer:
PostgreSQL 16  — Authoritative store
Redis 7.2      — Redirect cache, rate limit counters

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.22+ | Runtime |
| Docker | 24+ | Infrastructure containers |
| Docker Compose | v2 | Local infra orchestration |
| Kind | 0.22+ | Local Kubernetes (Phase 4) |
| kubectl | 1.29+ | Kubernetes CLI (Phase 4) |
| Terraform | 1.7+ | Infra simulation (Phase 4) |
| golangci-lint | 1.57+ | Static analysis |
| k6 | 0.49+ | Load testing (Phase 4) |

## Quick Start
```bash
# 1. Clone repository
git clone https://github.com/your-org/url-shortener
cd url-shortener

# 2. Copy environment config
cp .env.example .env

# 3. Start infrastructure
make infra-up

# 4. Install dependencies
make deps

# 5. Run database migrations
make migrate-up

# 6. Run API service
make run-api

# 7. Run redirect service (separate terminal)
make run-redirector
```

## Project Structure
.
├── cmd/                    Entry points for each service binary
│   ├── api/                API service (shorten, CRUD, auth)
│   └── redirector/         Redirect service (resolve + HTTP 302)
├── internal/               Private application code
│   ├── config/             Environment-based configuration loader
│   ├── domain/             Enterprise business rules (no external deps)
│   │   └── url/            URL entity, repository interface, domain errors
│   ├── application/        Use cases / command handlers (CQRS)
│   ├── infrastructure/     Driven adapters (postgres, redis, metrics)
│   └── interfaces/         Driving adapters (HTTP handlers, middleware)
├── pkg/                    Shared, importable packages
│   ├── logger/             Structured slog-based logger
│   ├── telemetry/          OpenTelemetry initialization
│   └── shortcode/          Base62 short code generator
├── deployments/            Infrastructure-as-code
│   ├── kubernetes/         K8s manifests (Phase 4)
│   └── terraform/          Terraform modules (Phase 4)
├── scripts/                Operational and bootstrap scripts
└── .github/workflows/      CI/CD pipelines (Phase 4)

## Architecture Layers

This project implements Clean Architecture (Ports & Adapters):
┌────────────────────────────────────┐
│         interfaces/http            │  HTTP handlers, middleware, router
├────────────────────────────────────┤
│           application/             │  Use cases, command/query handlers
├────────────────────────────────────┤
│             domain/                │  Entities, repository interfaces (PURE)
├────────────────────────────────────┤
│          infrastructure/           │  postgres, redis, metrics adapters
└────────────────────────────────────┘

Dependency rule: outer layers depend on inner layers. The domain has zero external dependencies.

## Development Workflow
```bash
make test           # Unit tests with race detector
make lint           # golangci-lint
make build          # Compile all binaries
make infra-up       # Start PostgreSQL + Redis
make infra-down     # Stop infrastructure
make infra-logs     # Tail logs
```

## SLO Targets (from PRD-USP-001)

| SLO | Target |
|-----|--------|
| Redirect availability (30d) | 99.95% |
| API availability (30d) | 99.9% |
| Redirect P99 latency | < 50ms |
| API write P99 latency | < 200ms |