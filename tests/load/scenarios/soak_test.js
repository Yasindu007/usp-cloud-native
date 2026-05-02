// ============================================================
// soak_test.js — Extended duration soak test
//
// Purpose:
//   Run sustained moderate load for 30 minutes to detect:
//     - Memory leaks (goroutine accumulation, heap growth)
//     - Connection pool exhaustion (connections not returned)
//     - Redis key TTL correctness (cached entries must expire cleanly)
//     - DB connection recycling (MaxConnLifetime behaviour)
//     - Log file rotation / disk space issues
//
// PRD release criteria section 15.2:
//   "72-hour soak test with no SLO violations"
//   This 30-minute version is the development-cycle version.
//   Full 72-hour soak runs in CI nightly (configured in workflow).
//
// What to monitor DURING this test in Grafana:
//   - go_memstats_heap_alloc_bytes  — should not trend upward
//   - go_goroutines                  — should remain stable
//   - urlshortener_db_pool_connections{state="acquired"} — should not grow
//   - process_resident_memory_bytes  — should be flat
// ============================================================

import { sleep } from 'k6';
import {
    doRedirect,
    doShorten,
    checkRedirectResponse,
    checkShortenResponse,
    randomURL,
    thinkTime,
} from '../lib/helpers.js';
import { SEEDED_SHORT_CODES } from '../lib/config.js';
import { COMBINED_THRESHOLDS }  from '../lib/thresholds.js';

const DURATION = __ENV.K6_SOAK_DURATION || '30m';
const VUS      = parseInt(__ENV.K6_SOAK_VUS || '50');

export const options = {
    // Constant VU count for the full duration — no ramps.
    // We want to see behaviour under sustained load, not ramping behaviour.
    vus:      VUS,
    duration: DURATION,
    thresholds: {
        ...COMBINED_THRESHOLDS,
        // During a soak test, P99 must stay stable. A drifting P99 indicates
        // a resource leak (DB connections, goroutines, heap) degrading performance.
        'http_req_duration{name:redirect}': [
            { threshold: 'p(99) < 50',  abortOnFail: true, delayAbortEval: '60s' },
        ],
    },
    tags: { test_name: 'soak_test' },
};

// Mixed workload: 90% reads, 10% writes (mirrors PRD traffic model)
export default function () {
    const isWrite = Math.random() < 0.10;

    if (isWrite) {
        const { response } = doShorten(randomURL());
        checkShortenResponse(response, 'soak_write');
        thinkTime(500, 2000);
    } else {
        const code = SEEDED_SHORT_CODES[Math.floor(Math.random() * SEEDED_SHORT_CODES.length)];
        const res  = doRedirect(code);
        checkRedirectResponse(res, 'soak_read');
        thinkTime(100, 500);
    }
}
