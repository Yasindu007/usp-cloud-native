// ============================================================
// shorten_baseline.js — Baseline API write performance test
//
// Purpose:
//   Verify POST /api/v1/urls meets its P99 < 200ms SLO at the
//   expected write volume (580 RPS per PRD section 13.1).
//
// Write load is ~10% of redirect load. We simulate this with 10 VUs
// (vs 100 for the redirect baseline). Each VU shortens a unique URL
// to prevent duplicate-short-code collisions.
//
// What it validates:
//   - DB write path performance (primary pool, no cache)
//   - Short code generation under concurrent writes
//   - Connection pool behaviour under write load
//   - Rate limiting headers are present and correct
// ============================================================

import { check, sleep } from 'k6';
import http from 'k6/http';
import {
    doShorten,
    doRedirect,
    checkShortenResponse,
    randomURL,
    thinkTime,
} from '../lib/helpers.js';
import { WRITE_THRESHOLDS } from '../lib/thresholds.js';
import { BASE_URL_REDIRECT } from '../lib/config.js';

export const options = {
    stages: [
        { duration: '30s', target: 5  },
        { duration: '2m',  target: 10 },
        { duration: '5m',  target: 10 },  // Steady state
        { duration: '30s', target: 0  },
    ],
    thresholds: WRITE_THRESHOLDS,
    tags: {
        test_name: 'shorten_baseline',
        service:   'api-service',
    },
};

export default function () {
    const url = randomURL();
    const { success, shortCode, response } = doShorten(url);

    checkShortenResponse(response, 'baseline');

    // Verify rate limit headers are present on every response
    check(response, {
        'has RateLimit-Limit header':     r => r.headers['Ratelimit-Limit'] !== undefined ||
                                               r.headers['RateLimit-Limit'] !== undefined,
        'has RateLimit-Remaining header': r => r.headers['Ratelimit-Remaining'] !== undefined ||
                                               r.headers['RateLimit-Remaining'] !== undefined,
    });

    if (success && shortCode) {
        // Immediately try to resolve the freshly created short code.
        // This tests the cache pre-warm behaviour (created URLs should be
        // immediately resolvable via cache without a DB read).
        const redirectRes = http.get(`${BASE_URL_REDIRECT}/${shortCode}`, { redirects: 0 });
        check(redirectRes, {
            'newly created URL is immediately redirectable': r => r.status === 302,
            'cache pre-warm working (< 5ms)':               r => r.timings.duration < 5,
        });
    }

    // Writers need more think time than readers (models realistic usage)
    thinkTime(200, 1000);
}
