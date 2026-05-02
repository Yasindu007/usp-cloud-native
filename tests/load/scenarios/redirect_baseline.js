// ============================================================
// redirect_baseline.js — Baseline redirect performance test
//
// Purpose:
//   Verify the redirect service meets its P99 < 50ms SLO at the
//   expected steady-state traffic volume (2,900 RPS per launch
//   assumptions from PRD section 13.1).
//
// What it measures:
//   - P50/P95/P99 redirect latency under sustained load
//   - Cache hit ratio (proxy: % of responses < 5ms)
//   - Error rate (5xx responses)
//   - Throughput (requests/second achieved)
//
// Prerequisites:
//   - Short codes must exist in the database.
//     Run: bash scripts/run-load-test.sh --seed
//   - Both services must be running.
//
// Execution stages:
//   Ramp up (2m):   0 → 100 VUs  — validates behaviour under increasing load
//   Steady state (5m): 100 VUs   — the SLO measurement window
//   Ramp down (1m): 100 → 0 VUs  — validates graceful drain
//
// Expected results (if SLOs are met):
//   P50:  < 5ms   (cache hit)
//   P99:  < 50ms  (SLO boundary)
//   RPS:  ~2,500-3,000
//   Errors: < 0.05%
// ============================================================

import { sleep, check } from 'k6';
import { htmlReport } from 'https://raw.githubusercontent.com/benc-uk/k6-reporter/main/dist/bundle.js';
import {
    doRedirect,
    checkRedirectResponse,
    thinkTime,
} from '../lib/helpers.js';
import { REDIRECT_THRESHOLDS } from '../lib/thresholds.js';
import { SEEDED_SHORT_CODES } from '../lib/config.js';

// ── Test configuration ────────────────────────────────────────────────────────
export const options = {
    // Stages define the VU (virtual user) ramp profile.
    // Each VU runs the default function in a tight loop.
    // At 100 VUs with ~30ms average latency: ~3,300 RPS
    stages: [
        { duration: '30s', target: 10  },  // Warm-up
        { duration: '1m',  target: 50  },  // Ramp to half load
        { duration: '1m',  target: 100 },  // Ramp to full load
        { duration: '5m',  target: 100 },  // Steady state — SLO measurement window
        { duration: '30s', target: 0   },  // Ramp down
    ],
    thresholds: REDIRECT_THRESHOLDS,

    // Tag all metrics from this script for filtering in Grafana
    tags: {
        test_name: 'redirect_baseline',
        service:   'redirect-service',
    },
};

// Short codes are distributed across VUs. Each VU picks a code from the
// seeded list based on its VU number modulo the list length. This spreads
// load across codes and tests the cache behaviour for each code independently.
export default function () {
    const codeIndex = (__VU - 1) % SEEDED_SHORT_CODES.length;
    const shortCode  = SEEDED_SHORT_CODES[codeIndex];

    const res = doRedirect(shortCode);
    checkRedirectResponse(res, 'baseline');

    // Minimal think time: redirect users typically follow links in rapid
    // succession (from a tweet, for example). 50-200ms models this.
    thinkTime(50, 200);
}

// ── Post-test HTML report ─────────────────────────────────────────────────────
// Generates a self-contained HTML report in tests/load/results/.
// Requires k6 to be run with the --out flag (handled in run-load-test.sh).
export function handleSummary(data) {
    const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
    return {
        [`tests/load/results/redirect_baseline_${timestamp}.html`]: htmlReport(data),
        'stdout': textSummary(data, { indent: '  ', enableColors: true }),
    };
}

function textSummary(data, opts) {
    // Inline minimal summary since k6 textSummary is available in k6 cloud
    // but not always in OSS. Fall back to JSON.
    return JSON.stringify({
        test: 'redirect_baseline',
        p50:  data.metrics.http_req_duration?.values?.['p(50)'],
        p95:  data.metrics.http_req_duration?.values?.['p(95)'],
        p99:  data.metrics.http_req_duration?.values?.['p(99)'],
        rps:  data.metrics.http_reqs?.values?.rate,
        errors: data.metrics.http_req_failed?.values?.rate,
    }, null, 2);
}
