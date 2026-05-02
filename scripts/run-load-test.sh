#!/usr/bin/env bash
# ============================================================
# run-load-test.sh — Wrapper to run k6 load test scenarios
#
# Handles:
#   1. Seeding test data (short codes) before tests
#   2. Starting the InfluxDB + Grafana stack for observability
#   3. Running k6 with correct output flags
#   4. Generating summary JSON in tests/load/results/
#   5. Checking the k6 exit code and reporting pass/fail
#
# Usage:
#   bash scripts/run-load-test.sh redirect_baseline
#   bash scripts/run-load-test.sh shorten_baseline
#   bash scripts/run-load-test.sh spike_test
#   bash scripts/run-load-test.sh soak_test
#   bash scripts/run-load-test.sh stress_test
#   bash scripts/run-load-test.sh slo_validation
#   bash scripts/run-load-test.sh --seed          (seed test data only)
#   bash scripts/run-load-test.sh --all           (run all scenarios)
#   bash scripts/run-load-test.sh --slo           (run SLO gate test only)
# ============================================================

set -euo pipefail

SCENARIO="${1:-redirect_baseline}"
API_URL="${K6_API_URL:-http://localhost:8080}"
REDIRECT_URL="${K6_REDIRECT_URL:-http://localhost:8081}"
INFLUXDB_URL="${INFLUXDB_URL:-http://localhost:8086}"
RESULTS_DIR="tests/load/results"
K6_BIN="${K6_BIN:-k6}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

log()  { echo -e "${GREEN}==>${NC} $*"; }
warn() { echo -e "${YELLOW}WARN:${NC} $*"; }
fail() { echo -e "${RED}FAIL:${NC} $*"; exit 1; }

mkdir -p "${RESULTS_DIR}"

# ── Verify k6 is installed ────────────────────────────────────────────────────
if ! command -v "${K6_BIN}" &>/dev/null; then
    if [ -x "/c/Program Files/k6/k6.exe" ]; then
        K6_BIN="/c/Program Files/k6/k6.exe"
    elif [ -x "/mnt/c/Program Files/k6/k6.exe" ]; then
        K6_BIN="/mnt/c/Program Files/k6/k6.exe"
    fi
fi

if [ "${SCENARIO}" != "--seed" ] && ! command -v "${K6_BIN}" &>/dev/null; then
    fail "k6 not found. Install: brew install k6 | apt install k6 | https://k6.io/docs/getting-started/installation/"
fi

if command -v "${K6_BIN}" &>/dev/null; then
    K6_VERSION=$("${K6_BIN}" version | head -1)
    log "k6: ${K6_VERSION}"
fi

# ── Verify services are running ───────────────────────────────────────────────
log "Checking services..."
if ! curl -sf "${API_URL}/healthz" >/dev/null; then
    fail "API service not reachable at ${API_URL}. Run: make run-api"
fi
if ! curl -sf "${REDIRECT_URL}/healthz" >/dev/null; then
    fail "Redirect service not reachable at ${REDIRECT_URL}. Run: make run-redirector"
fi
log "Services: healthy"

# ── Seed test data ────────────────────────────────────────────────────────────
seed_test_data() {
    log "Seeding test short codes for load testing..."

    local WORKSPACE_ID="${K6_WORKSPACE_ID:-ws_loadtest}"
    local TOKEN="${K6_AUTH_TOKEN:-}"
    local API_KEY_HEADER="${K6_API_KEY:-}"

    local AUTH_HEADER
    if [ -n "${API_KEY_HEADER}" ]; then
        AUTH_HEADER="X-API-Key: ${API_KEY_HEADER}"
    elif [ -n "${TOKEN}" ]; then
        AUTH_HEADER="Authorization: Bearer ${TOKEN}"
    else
        AUTH_HEADER="X-Workspace-ID: ${WORKSPACE_ID}"
    fi

    # The API enforces the urls.workspace_id foreign key. For local load tests,
    # create a dedicated workspace/member row directly in the dev Postgres
    # container so repeated runs do not depend on UI/API setup.
    local HAS_POSTGRES=0
    if command -v docker &>/dev/null && docker ps --format '{{.Names}}' | grep -qx 'urlshortener-postgres'; then
        HAS_POSTGRES=1
        docker exec -i urlshortener-postgres psql -U "${POSTGRES_USER:-urlshortener}" -d "${POSTGRES_DB:-urlshortener}" >/dev/null <<SQL
INSERT INTO workspaces (id, name, slug, plan_tier, owner_id)
VALUES ('${WORKSPACE_ID}', 'Load Test Workspace', 'load-test', 'free', 'usr_loadtest')
ON CONFLICT (id) DO NOTHING;

INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ('${WORKSPACE_ID}', 'usr_loadtest', 'owner')
ON CONFLICT DO NOTHING;
SQL
    else
        warn "Postgres container urlshortener-postgres not found; assuming workspace ${WORKSPACE_ID} already exists"
    fi

    local SHORT_CODES=()

    # Create 10 seeded URLs with known short codes
    for i in $(seq 1 10); do
        local CODE="test$(printf '%03d' ${i})"
        local PAYLOAD="{\"original_url\":\"https://load-test-target.example.com/seed/${i}\",\"custom_code\":\"${CODE}\"}"

        if [ "${HAS_POSTGRES}" -eq 1 ]; then
            docker exec -i urlshortener-postgres psql -U "${POSTGRES_USER:-urlshortener}" -d "${POSTGRES_DB:-urlshortener}" >/dev/null <<SQL
UPDATE urls
SET workspace_id = '${WORKSPACE_ID}',
    original_url = 'https://load-test-target.example.com/seed/${i}',
    status = 'active',
    expires_at = NULL,
    deleted_at = NULL,
    created_by = 'usr_loadtest'
WHERE short_code = '${CODE}';
SQL
        fi

        if command -v docker &>/dev/null && docker ps --format '{{.Names}}' | grep -qx 'urlshortener-redis'; then
            docker exec urlshortener-redis redis-cli -a "${REDIS_PASSWORD:-secret}" DEL "url:v1:${CODE}" "url:v1:notfound:${CODE}" >/dev/null 2>&1 || true
        fi

        local RESPONSE
        RESPONSE=$(curl -sf -X POST "${API_URL}/api/v1/workspaces/${WORKSPACE_ID}/urls" \
            -H "${AUTH_HEADER}" \
            -H "X-User-ID: usr_loadtest" \
            -H "Content-Type: application/json" \
            -d "${PAYLOAD}" 2>/dev/null) || true

        if echo "${RESPONSE}" | grep -q '"short_code"'; then
            SHORT_CODES+=("${CODE}")
            echo -n "."
        elif curl -sf -o /dev/null -X GET "${REDIRECT_URL}/${CODE}" --max-time 2; then
            # Idempotent local runs: if the custom code already exists and
            # resolves, keep it in the seeded set instead of treating the
            # duplicate create response as a seeding failure.
            SHORT_CODES+=("${CODE}")
            echo -n "."
        fi
    done
    echo ""

    if [ ${#SHORT_CODES[@]} -eq 0 ]; then
        warn "No short codes seeded — tests will use default codes from config.js"
        warn "Check authentication: set K6_AUTH_TOKEN or K6_API_KEY"
    else
        log "Seeded ${#SHORT_CODES[@]} short codes: ${SHORT_CODES[*]}"
        export K6_SHORT_CODES="$(IFS=,; echo "${SHORT_CODES[*]}")"
    fi
}

# ── Start k6 observability stack ──────────────────────────────────────────────
start_k6_stack() {
    log "Starting k6 observability stack (InfluxDB + Grafana)..."

    if ! docker compose -f docker-compose.k6.yml ps 2>/dev/null | grep -q "running"; then
        docker compose -f docker-compose.k6.yml up -d
        log "Waiting for InfluxDB to be ready..."
        local attempts=0
        until curl -sf "${INFLUXDB_URL}/ping" >/dev/null 2>&1; do
            sleep 2
            attempts=$((attempts + 1))
            if [ $attempts -gt 30 ]; then
                warn "InfluxDB not ready — running without real-time dashboard"
                return
            fi
        done
        log "InfluxDB ready at ${INFLUXDB_URL}"
        log "Grafana k6 dashboard: http://localhost:3001 (admin/admin)"
    fi
}

# ── Run k6 scenario ───────────────────────────────────────────────────────────
run_scenario() {
    local scenario="$1"
    local script="tests/load/scenarios/${scenario}.js"

    if [ ! -f "${script}" ]; then
        fail "Test scenario not found: ${script}"
    fi

    log "Running scenario: ${scenario}"
    log "API:      ${API_URL}"
    log "Redirect: ${REDIRECT_URL}"

    local TIMESTAMP
    TIMESTAMP=$(date +"%Y%m%d_%H%M%S")

    local OUT_FLAGS=""
    if curl -sf "${INFLUXDB_URL}/ping" >/dev/null 2>&1; then
        OUT_FLAGS="--out influxdb=${INFLUXDB_URL}/k6"
        log "Output: InfluxDB + summary JSON"
    else
        log "Output: summary JSON only (InfluxDB not running)"
    fi

    local EXIT_CODE=0

    # Run k6 with all configured outputs
    K6_API_URL="${API_URL}" \
    K6_REDIRECT_URL="${REDIRECT_URL}" \
    GIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')" \
    "${K6_BIN}" run \
        ${OUT_FLAGS} \
        --summary-export="${RESULTS_DIR}/${scenario}_${TIMESTAMP}_summary.json" \
        "${script}" || EXIT_CODE=$?

    echo ""
    if [ ${EXIT_CODE} -eq 0 ]; then
        echo -e "${GREEN}════════════════════════════════${NC}"
        echo -e "${GREEN}  SCENARIO PASSED: ${scenario}${NC}"
        echo -e "${GREEN}════════════════════════════════${NC}"
    else
        echo -e "${RED}════════════════════════════════${NC}"
        echo -e "${RED}  SCENARIO FAILED: ${scenario}${NC}"
        echo -e "${RED}  Exit code: ${EXIT_CODE}${NC}"
        echo -e "${RED}════════════════════════════════${NC}"
    fi

    echo "  Results: ${RESULTS_DIR}/${scenario}_${TIMESTAMP}_summary.json"

    return ${EXIT_CODE}
}

# ── Main ──────────────────────────────────────────────────────────────────────
case "${SCENARIO}" in
    --seed)
        seed_test_data
        ;;
    --all)
        seed_test_data
        start_k6_stack
        OVERALL=0
        for s in redirect_baseline shorten_baseline spike_test; do
            run_scenario "${s}" || OVERALL=1
            sleep 10  # Cool-down between scenarios
        done
        exit ${OVERALL}
        ;;
    --slo)
        seed_test_data
        start_k6_stack
        run_scenario "slo_validation"
        ;;
    *)
        seed_test_data
        start_k6_stack
        run_scenario "${SCENARIO}"
        ;;
esac
