#!/usr/bin/env bash
# ============================================================
# slo-report.sh — Query Prometheus and generate an SLO status report
#
# Produces a human-readable and machine-readable (JSON) SLO report
# covering the current 30-day rolling window.
#
# Covers all 5 SLIs from PRD section 12.1:
#   SLI-01: Redirect availability
#   SLI-02: API availability
#   SLI-03: Redirect P99 latency
#   SLI-04: API write P99 latency
#   SLI-05: Cache hit ratio
#
# Usage:
#   bash scripts/slo-report.sh
#   bash scripts/slo-report.sh --json        (machine-readable output)
#   PROMETHEUS_URL=http://my-prometheus:9090 bash scripts/slo-report.sh
# ============================================================

set -euo pipefail

PROMETHEUS_URL="${PROMETHEUS_URL:-http://localhost:9095}"
OUTPUT_FORMAT="${1:-human}"
REPORT_DIR="tests/load/results"
mkdir -p "${REPORT_DIR}"

# Colours
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BOLD='\033[1m'; NC='\033[0m'

# ── Prometheus query helper ───────────────────────────────────────────────────
query_prometheus() {
    local expr="$1"
    local result

    result=$(curl -sf \
        "${PROMETHEUS_URL}/api/v1/query" \
        --data-urlencode "query=${expr}" \
        2>/dev/null) || { echo "ERROR"; return; }

    # Extract the scalar value from the result JSON
    echo "${result}" | python3 -c "
import json, sys
data = json.load(sys.stdin)
results = data.get('data', {}).get('result', [])
if results:
    val = results[0].get('value', [None, None])[1]
    print(val if val else 'N/A')
else:
    print('N/A')
" 2>/dev/null || echo "N/A"
}

# ── Status indicator ──────────────────────────────────────────────────────────
status_indicator() {
    local value="$1"
    local target="$2"
    local higher_is_better="${3:-true}"

    if [ "${value}" = "N/A" ] || [ "${value}" = "ERROR" ]; then
        echo -e "${YELLOW}⚠ N/A${NC}"
        return
    fi

    local met
    if [ "${higher_is_better}" = "true" ]; then
        met=$(python3 -c "print('yes' if float('${value}') >= float('${target}') else 'no')" 2>/dev/null || echo "no")
    else
        met=$(python3 -c "print('yes' if float('${value}') <= float('${target}') else 'no')" 2>/dev/null || echo "no")
    fi

    if [ "${met}" = "yes" ]; then
        echo -e "${GREEN}✓ MET${NC}"
    else
        echo -e "${RED}✗ BREACHED${NC}"
    fi
}

# ── Check Prometheus is reachable ─────────────────────────────────────────────
if ! curl -sf "${PROMETHEUS_URL}/-/ready" >/dev/null 2>&1; then
    echo "ERROR: Prometheus not reachable at ${PROMETHEUS_URL}"
    echo "  Start monitoring: make monitoring-up"
    echo "  Or specify: PROMETHEUS_URL=http://your-prometheus:9090 bash scripts/slo-report.sh"
    exit 1
fi

# ── Query all SLIs ────────────────────────────────────────────────────────────
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

if [ "${OUTPUT_FORMAT}" != "--json" ]; then
    echo -e "${BOLD}Querying Prometheus at ${PROMETHEUS_URL}...${NC}"
    echo ""
fi

# SLI-01: Redirect availability (30d)
REDIRECT_AVAILABILITY=$(query_prometheus "slo:redirect_availability:ratio_30d")
REDIRECT_BUDGET_CONSUMED=$(query_prometheus "slo:redirect_error_budget_consumed:ratio_30d")
REDIRECT_BUDGET_MINUTES=$(query_prometheus "slo:redirect_error_budget_minutes_remaining:30d")

# SLI-02: API availability (30d)
API_AVAILABILITY=$(query_prometheus "slo:api_availability:ratio_30d")
API_BUDGET_CONSUMED=$(query_prometheus "slo:api_error_budget_consumed:ratio_30d")
API_BUDGET_MINUTES=$(query_prometheus "slo:api_error_budget_minutes_remaining:30d")

# SLI-03: Redirect P99 latency (5m window, current) — convert to ms
REDIRECT_P99=$(query_prometheus "slo:redirect_latency_p99:rate5m * 1000")

# SLI-04: API write P99 latency (5m window, current) — convert to ms
API_WRITE_P99=$(query_prometheus "slo:api_write_latency_p99:rate5m * 1000")

# SLI-05: Cache hit ratio (5m window, current)
CACHE_HIT_RATIO=$(query_prometheus "slo:cache_hit_ratio:rate5m")

# Current burn rates
BURN_RATE_1H=$(query_prometheus "slo:redirect_error_ratio:rate1h / 0.0005")
BURN_RATE_6H=$(query_prometheus "slo:redirect_error_ratio:rate6h / 0.0005")

# ── Human-readable report ─────────────────────────────────────────────────────
if [ "${OUTPUT_FORMAT}" != "--json" ]; then
    echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║        URL Shortener Platform — SLO Status Report        ║${NC}"
    echo -e "${BOLD}║               30-Day Rolling Window                      ║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "  Generated: ${TIMESTAMP}"
    echo "  Source:    ${PROMETHEUS_URL}"
    echo ""

    echo -e "${BOLD}┌─────────────────────────────────────────────────────────┐${NC}"
    echo -e "${BOLD}│  AVAILABILITY SLOs                                      │${NC}"
    echo -e "${BOLD}└─────────────────────────────────────────────────────────┘${NC}"

    printf "  %-30s %-12s %-10s %s\n" "SLI" "Value" "Target" "Status"
    printf "  %-30s %-12s %-10s %s\n" "───" "─────" "──────" "──────"

    FMT_REDIRECT=$(python3 -c "v='${REDIRECT_AVAILABILITY}'; print(f'{float(v)*100:.4f}%' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${REDIRECT_AVAILABILITY}")
    FMT_API=$(python3 -c "v='${API_AVAILABILITY}'; print(f'{float(v)*100:.4f}%' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${API_AVAILABILITY}")

    printf "  %-30s %-12s %-10s " "SLI-01 Redirect Availability" "${FMT_REDIRECT}" "99.9500%"
    echo -e "$(status_indicator "${REDIRECT_AVAILABILITY}" "0.9995")"

    printf "  %-30s %-12s %-10s " "SLI-02 API Availability" "${FMT_API}" "99.9000%"
    echo -e "$(status_indicator "${API_AVAILABILITY}" "0.999")"

    echo ""
    echo -e "${BOLD}┌─────────────────────────────────────────────────────────┐${NC}"
    echo -e "${BOLD}│  LATENCY SLOs (current 5-minute window)                 │${NC}"
    echo -e "${BOLD}└─────────────────────────────────────────────────────────┘${NC}"

    FMT_R_P99=$(python3 -c "v='${REDIRECT_P99}'; print(f'{float(v):.1f}ms' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${REDIRECT_P99}")
    FMT_W_P99=$(python3 -c "v='${API_WRITE_P99}'; print(f'{float(v):.1f}ms' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${API_WRITE_P99}")

    printf "  %-30s %-12s %-10s " "SLI-03 Redirect P99 Latency" "${FMT_R_P99}" "< 50ms"
    echo -e "$(status_indicator "${REDIRECT_P99}" "50" "false")"

    printf "  %-30s %-12s %-10s " "SLI-04 API Write P99 Latency" "${FMT_W_P99}" "< 200ms"
    echo -e "$(status_indicator "${API_WRITE_P99}" "200" "false")"

    echo ""
    echo -e "${BOLD}┌─────────────────────────────────────────────────────────┐${NC}"
    echo -e "${BOLD}│  CACHE SLO                                              │${NC}"
    echo -e "${BOLD}└─────────────────────────────────────────────────────────┘${NC}"

    FMT_CACHE=$(python3 -c "v='${CACHE_HIT_RATIO}'; print(f'{float(v)*100:.1f}%' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${CACHE_HIT_RATIO}")

    printf "  %-30s %-12s %-10s " "SLI-05 Cache Hit Ratio" "${FMT_CACHE}" "> 85%"
    echo -e "$(status_indicator "${CACHE_HIT_RATIO}" "0.85")"

    echo ""
    echo -e "${BOLD}┌─────────────────────────────────────────────────────────┐${NC}"
    echo -e "${BOLD}│  ERROR BUDGET STATUS                                    │${NC}"
    echo -e "${BOLD}└─────────────────────────────────────────────────────────┘${NC}"

    FMT_R_CONSUMED=$(python3 -c "v='${REDIRECT_BUDGET_CONSUMED}'; print(f'{float(v)*100:.1f}%' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${REDIRECT_BUDGET_CONSUMED}")
    FMT_A_CONSUMED=$(python3 -c "v='${API_BUDGET_CONSUMED}'; print(f'{float(v)*100:.1f}%' if v not in ('N/A','ERROR') else v)" 2>/dev/null || echo "${API_BUDGET_CONSUMED}")

    echo "  Redirect error budget consumed (30d): ${FMT_R_CONSUMED} of 21.6 min"
    echo "  API error budget consumed (30d):      ${FMT_A_CONSUMED} of 43.2 min"
    echo ""
    echo "  Current burn rates:"
    printf "    1h:  %.2f×  " "$(python3 -c "v='${BURN_RATE_1H}'; print(float(v) if v not in ('N/A','ERROR') else 0)" 2>/dev/null || echo "0")"
    python3 -c "
v='${BURN_RATE_1H}'
if v in ('N/A','ERROR'): print('(no data)')
elif float(v) >= 14.4: print('\033[0;31m⚠ CRITICAL: page on-call\033[0m')
elif float(v) >= 6:    print('\033[1;33m⚠ WARNING: investigate\033[0m')
else:                  print('\033[0;32m✓ normal\033[0m')
" 2>/dev/null || echo ""

    printf "    6h:  %.2f×\n" "$(python3 -c "v='${BURN_RATE_6H}'; print(float(v) if v not in ('N/A','ERROR') else 0)" 2>/dev/null || echo "0")"
    echo ""
fi

# ── JSON output ───────────────────────────────────────────────────────────────
JSON_REPORT=$(python3 -c "
import json, sys

def safe_float(v, scale=1.0):
    try: return round(float(v) * scale, 6)
    except: return None

def met_high(v, target):
    f = safe_float(v)
    return f is not None and f >= target

def met_low(v, target):
    f = safe_float(v)
    return f is not None and f <= target

report = {
    'generated_at': '${TIMESTAMP}',
    'prometheus_url': '${PROMETHEUS_URL}',
    'window': '30d',
    'slos': {
        'SLO-01_redirect_availability': {
            'value': safe_float('${REDIRECT_AVAILABILITY}'),
            'target': 0.9995,
            'met': met_high('${REDIRECT_AVAILABILITY}', 0.9995),
            'error_budget_consumed_pct': safe_float('${REDIRECT_BUDGET_CONSUMED}', 100),
            'error_budget_minutes_remaining': safe_float('${REDIRECT_BUDGET_MINUTES}'),
        },
        'SLO-02_api_availability': {
            'value': safe_float('${API_AVAILABILITY}'),
            'target': 0.999,
            'met': met_high('${API_AVAILABILITY}', 0.999),
            'error_budget_consumed_pct': safe_float('${API_BUDGET_CONSUMED}', 100),
            'error_budget_minutes_remaining': safe_float('${API_BUDGET_MINUTES}'),
        },
        'SLO-03_redirect_p99_ms': {
            'value_ms': safe_float('${REDIRECT_P99}'),
            'target_ms': 50,
            'met': met_low('${REDIRECT_P99}', 50),
        },
        'SLO-04_api_write_p99_ms': {
            'value_ms': safe_float('${API_WRITE_P99}'),
            'target_ms': 200,
            'met': met_low('${API_WRITE_P99}', 200),
        },
        'SLO-05_cache_hit_ratio': {
            'value': safe_float('${CACHE_HIT_RATIO}'),
            'target': 0.85,
            'met': met_high('${CACHE_HIT_RATIO}', 0.85),
        },
    },
    'burn_rates': {
        '1h': safe_float('${BURN_RATE_1H}'),
        '6h': safe_float('${BURN_RATE_6H}'),
    },
    'all_slos_met': all([
        met_high('${REDIRECT_AVAILABILITY}', 0.9995),
        met_high('${API_AVAILABILITY}', 0.999),
        met_low('${REDIRECT_P99}', 50),
        met_low('${API_WRITE_P99}', 200),
        met_high('${CACHE_HIT_RATIO}', 0.85),
    ]),
}

print(json.dumps(report, indent=2))
" 2>/dev/null || echo '{"error": "failed to generate JSON report"}')

# Always write the JSON report to disk
REPORT_FILE="${REPORT_DIR}/slo_report_$(date +%Y%m%d_%H%M%S).json"
echo "${JSON_REPORT}" > "${REPORT_FILE}"

if [ "${OUTPUT_FORMAT}" = "--json" ]; then
    echo "${JSON_REPORT}"
else
    echo "  Full report: ${REPORT_FILE}"
    echo ""

    # Exit code: 1 if any SLO is breached
    ALL_MET=$(echo "${JSON_REPORT}" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print('yes' if data.get('all_slos_met', False) else 'no')
" 2>/dev/null || echo "no")

    if [ "${ALL_MET}" = "yes" ]; then
        echo -e "${GREEN}${BOLD}All SLOs are currently MET ✓${NC}"
        exit 0
    else
        echo -e "${RED}${BOLD}One or more SLOs are BREACHED ✗${NC}"
        exit 1
    fi
fi
