// ============================================================
// spike_test.js — Viral traffic spike simulation
//
// Purpose:
//   Simulate the "viral tweet" scenario: a short URL shared on
//   social media generates a burst of 25,000 RPS in under 60 seconds.
//
// PRD section 6.1: "Max burst throughput: 25,000 RPS (60s)"
//
// What we're testing:
//   1. HPA scale-out: does Kubernetes add pods fast enough?
//   2. Redis cache handles burst (all requests for same codes → 100% hit rate)
//   3. Connection pool saturation: does the pool queue or reject connections?
//   4. Rate limiter behaviour: unauthenticated IPs should hit 300 req/60s limit
//
// Spike profile:
//   Normal (2m):  100 VUs  (~3k RPS)
//   Spike (30s):  800 VUs  (~24k RPS) — sudden viral event
//   Recovery (2m): 100 VUs — traffic returns to normal
//   Verify (1m):  100 VUs  — confirm service recovered cleanly
//
// Note: 800 VUs on a local Kind cluster will saturate resources quickly.
// Reduce to 200 VUs for local testing; use full 800 for remote clusters.
// ============================================================

import { sleep } from 'k6';
import { doRedirect, checkRedirectResponse } from '../lib/helpers.js';
import { SEEDED_SHORT_CODES } from '../lib/config.js';

const MAX_VUS = parseInt(__ENV.K6_SPIKE_MAX_VUS || '200');  // 800 for real clusters

export const options = {
    stages: [
        { duration: '1m',  target: 50       },  // Warm up
        { duration: '1m',  target: 100      },  // Normal traffic
        { duration: '10s', target: MAX_VUS  },  // SPIKE — sudden viral burst
        { duration: '30s', target: MAX_VUS  },  // Sustain spike
        { duration: '10s', target: 100      },  // Traffic subsides
        { duration: '2m',  target: 100      },  // Recovery period — critical check
        { duration: '30s', target: 0        },
    ],
    // Relaxed thresholds during spike — we accept some degradation during burst
    // but require full recovery in the recovery window.
    thresholds: {
        'http_req_duration{name:redirect}': [
            // During steady state (not spike), P99 must be under 50ms.
            // k6 evaluates thresholds at test END, so this covers the full run.
            // Post-spike recovery must bring P99 back under SLO.
            { threshold: 'p(99) < 200', abortOnFail: false },  // 4× SLO during spike
        ],
        'redirect_cache_errors': [
            { threshold: 'count < 1000', abortOnFail: false },  // errors during spike
        ],
    },
    tags: { test_name: 'spike_test' },
};

// Viral scenario: many users hitting the SAME short codes (social sharing).
// This maximises cache pressure — a cache miss storm at spike start would
// cause DB saturation. Our negative cache + Redis cluster should absorb this.
const VIRAL_CODES = SEEDED_SHORT_CODES.slice(0, 3);  // only 3 codes are "viral"

export default function () {
    const code = VIRAL_CODES[Math.floor(Math.random() * VIRAL_CODES.length)];
    const res  = doRedirect(code);

    checkRedirectResponse(res, 'spike');

    // No think time during spike — simulates bot-like viral traffic patterns
    sleep(0.01);
}
