// ============================================================
// thresholds.js — Reusable k6 threshold configurations
//
// k6 thresholds define pass/fail criteria for a test run.
// If ANY threshold is breached, k6 exits with code 99 (failure).
// This is the mechanism that fails a CI pipeline when SLOs are violated.
//
// Threshold types:
//   p(99) < N  — 99th percentile latency must be under N ms
//   rate < N   — error rate must be below N (0.001 = 0.1%)
//   count > N  — at least N requests must be made (validates test ran)
//
// abortOnFail: true — immediately stop the test if the threshold is
//   breached. Without this, a test that is clearly failing continues
//   until duration completes, wasting time and burning error budget.
// ============================================================

import { SLO } from './config.js';

// ── SLO-aligned thresholds ────────────────────────────────────────────────────

// Applied to redirect scenarios
export const REDIRECT_THRESHOLDS = {
    // SLO-03: Redirect P99 < 50ms
    'http_req_duration{name:redirect}': [
        {
            threshold: `p(99) < ${SLO.REDIRECT_P99_MS}`,
            abortOnFail: true,
            delayAbortEval: '10s',   // wait 10s before aborting (smooths transient spikes)
        },
    ],
    // SLO-01: Redirect availability 99.95%.
    // Use our explicit counter instead of k6 v2 RC's http_req_failed rate,
    // which can report value=0 while failing rate thresholds.
    'redirect_cache_errors': [
        { threshold: 'count < 50', abortOnFail: true, delayAbortEval: '30s' },
    ],
    // Custom trend metric (set in helpers.js)
    'redirect_latency_ms': [
        { threshold: `p(99) < ${SLO.REDIRECT_P99_MS}` },
    ],
};

// Applied to API write scenarios
export const WRITE_THRESHOLDS = {
    // SLO-04: API write P99 < 200ms
    'http_req_duration{name:shorten}': [
        {
            threshold: `p(99) < ${SLO.API_WRITE_P99_MS}`,
            abortOnFail: true,
            delayAbortEval: '10s',
        },
        { threshold: `p(95) < ${Math.floor(SLO.API_WRITE_P99_MS * 0.75)}` },
        { threshold: `p(50) < ${Math.floor(SLO.API_WRITE_P99_MS * 0.4)}` },
    ],
    // SLO-02: API availability 99.9%.
    'urls_shortened_fail': [
        { threshold: 'count < 10', abortOnFail: true, delayAbortEval: '30s' },
    ],
};

// Combined thresholds for mixed workload tests
export const COMBINED_THRESHOLDS = {
    ...REDIRECT_THRESHOLDS,
    ...WRITE_THRESHOLDS,
    'redirect_cache_errors': [
        { threshold: 'count < 50' },
    ],
    'urls_shortened_fail': [
        { threshold: 'count < 10' },
    ],
};

// Relaxed thresholds for stress tests
// (we EXPECT degradation — we want to measure WHERE it degrades)
export const STRESS_THRESHOLDS = {
    'http_req_duration{name:redirect}': [
        { threshold: `p(99) < 500` },  // 10× relaxed
    ],
    'redirect_cache_errors': [
        { threshold: 'count < 1000' },  // relaxed for stress
    ],
};
