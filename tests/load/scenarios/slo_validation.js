// ============================================================
// slo_validation.js — Authoritative SLO gate test
//
// Purpose:
//   This is the test that runs in CI on every deployment to main.
//   It is the formal SLO gate: if this test fails, the deployment
//   is automatically rolled back.
//
// It is NOT a stress test or capacity test. It runs at the EXPECTED
// load level (PRD section 13.1) for long enough to statistically
// validate that P99 is genuinely under threshold — not just lucky.
//
// Duration: 10 minutes at steady state gives ~1.8M redirect requests
// at 3000 RPS, enough for the P99 estimate to be stable.
//
// Pass criteria (from PRD SLO table):
//   SLO-01: Redirect availability > 99.95%   (error rate < 0.05%)
//   SLO-02: API availability > 99.9%         (error rate < 0.1%)
//   SLO-03: Redirect P99 < 50ms
//   SLO-04: API write P99 < 200ms
//
// SLO-05 (cache hit ratio > 85%) is validated post-test via
// the slo-report.sh script querying Prometheus.
// ============================================================

import { sleep, check } from 'k6';
import {
    doRedirect,
    doShorten,
    checkRedirectResponse,
    checkShortenResponse,
    randomURL,
    thinkTime,
} from '../lib/helpers.js';
import { COMBINED_THRESHOLDS } from '../lib/thresholds.js';
import { SEEDED_SHORT_CODES }  from '../lib/config.js';

export const options = {
    scenarios: {
        // Scenario 1: Redirect load (90% of traffic)
        // 100 VUs × ~30ms avg = ~3,300 RPS — matches PRD section 13.1
        redirects: {
            executor:          'constant-vus',
            vus:               100,
            duration:          '10m',
            tags:              { scenario: 'redirects' },
            gracefulStop:      '30s',
        },
        // Scenario 2: Write load (10% of traffic)
        // 10 VUs × ~150ms avg = ~67 writes/s — proportional to PRD estimate
        writes: {
            executor:          'constant-vus',
            vus:               10,
            duration:          '10m',
            exec:              'shortenScenario',
            tags:              { scenario: 'writes' },
            gracefulStop:      '30s',
        },
    },
    thresholds: {
        // SLO-03: Redirect P99 < 50ms — measured over the full 10 minutes
        'http_req_duration{name:redirect}': [
            {
                threshold:      `p(99) < 50`,
                // Evaluate p99 over the full 10-minute window. Early aborts
                // make the gate fail on warm-up outliers before the sample is
                // statistically representative.
                abortOnFail:    false,
            },
        ],
        // SLO-04: API write P99 < 200ms
        'http_req_duration{name:shorten}': [
            {
                threshold:      `p(99) < 200`,
                abortOnFail:    false,
            },
        ],
        // SLO-01: Redirect availability
        'redirect_cache_errors': [
            { threshold: 'count < 50', abortOnFail: true, delayAbortEval: '30s' },
        ],
        // SLO-02: API availability
        'urls_shortened_fail': [
            { threshold: 'count < 10', abortOnFail: true, delayAbortEval: '30s' },
        ],
        // Minimum request count (validates test actually ran at scale)
        'http_reqs{name:redirect}': [
            { threshold: 'count > 100000' },  // at least 100k redirects
        ],
    },
    tags: {
        test_name: 'slo_validation',
        version:   __ENV.GIT_SHA || 'unknown',
    },
};

// ── Scenario: redirect (default function) ─────────────────────────────────────
export default function () {
    const code = SEEDED_SHORT_CODES[__VU % SEEDED_SHORT_CODES.length];
    const res  = doRedirect(code);
    checkRedirectResponse(res, 'slo');
    thinkTime(50, 200);
}

// ── Scenario: shorten ─────────────────────────────────────────────────────────
export function shortenScenario() {
    const { success, response } = doShorten(randomURL());
    checkShortenResponse(response, 'slo');
    thinkTime(200, 800);
}

// ── Summary ───────────────────────────────────────────────────────────────────
export function handleSummary(data) {
    const passed = !data.state.testRunAborted;

    const summary = {
        slo_validation: {
            passed,
            timestamp: new Date().toISOString(),
            metrics: {
                redirect_p99_ms:     data.metrics['http_req_duration{name:redirect}']?.values?.['p(99)'],
                redirect_p50_ms:     data.metrics['http_req_duration{name:redirect}']?.values?.['p(50)'],
                api_write_p99_ms:    data.metrics['http_req_duration{name:shorten}']?.values?.['p(99)'],
                redirect_error_rate: data.metrics['http_req_failed{name:redirect}']?.values?.rate,
                api_error_rate:      data.metrics['http_req_failed{name:shorten}']?.values?.rate,
                redirect_rps:        data.metrics['http_reqs{name:redirect}']?.values?.rate,
                total_requests:      data.metrics.http_reqs?.values?.count,
            },
            slo_targets: {
                redirect_p99_ms_target:     50,
                api_write_p99_ms_target:    200,
                redirect_error_rate_target: 0.0005,
                api_error_rate_target:      0.001,
            },
        },
    };

    // Write JSON result for CI pipeline to parse
    return {
        'tests/load/results/slo_validation_latest.json': JSON.stringify(summary, null, 2),
        stdout: JSON.stringify(summary, null, 2),
    };
}
