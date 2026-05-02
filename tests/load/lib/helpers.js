// ============================================================
// helpers.js — Shared utilities for all load test scenarios
// ============================================================

import http   from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { BASE_URL_API, BASE_URL_REDIRECT, getAuthHeaders, TEST_WORKSPACE_ID } from './config.js';

// ── Custom metrics ────────────────────────────────────────────────────────────
// k6 built-in metrics (http_req_duration, http_req_failed) give overall numbers.
// Custom metrics allow us to track SLI-specific dimensions:
//   - Redirect cache hits vs misses (from X-Cache-Status response header)
//   - Business operation success rates (not just HTTP success)

export const redirectCacheHits   = new Counter('redirect_cache_hits');
export const redirectCacheMisses = new Counter('redirect_cache_misses');
export const redirectCacheErrors = new Counter('redirect_cache_errors');
export const urlsShortenedOK     = new Counter('urls_shortened_ok');
export const urlsShortenedFail   = new Counter('urls_shortened_fail');
export const redirectLatency     = new Trend('redirect_latency_ms', true);
export const apiWriteLatency     = new Trend('api_write_latency_ms', true);
export const errorRate           = new Rate('error_rate');

function loadClientIP() {
    const vu = typeof __VU === 'number' ? __VU : 0;
    const iter = typeof __ITER === 'number' ? __ITER : 0;
    return `10.${Math.floor(vu / 250) % 255}.${vu % 250}.${(iter % 250) + 1}`;
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

/**
 * Perform a redirect resolution request.
 * Returns the response object.
 * Records cache status from the response header (set by our service).
 */
export function doRedirect(shortCode, params = {}) {
    const url  = `${BASE_URL_REDIRECT}/${shortCode}`;
    const opts = {
        redirects: 0,    // Do NOT follow redirect — we test the 302 response itself.
                         // Following it would measure the target server, not our service.
        tags: { name: 'redirect', shortcode: shortCode.substring(0, 8) },
        headers: { 'X-Forwarded-For': loadClientIP() },
        ...params,
    };
    opts.headers = { 'X-Forwarded-For': loadClientIP(), ...(params.headers || {}) };

    const res = http.get(url, opts);

    redirectLatency.add(res.timings.duration);
    errorRate.add(res.status >= 500 ? 1 : 0);

    // The redirect service sets a custom X-Cache-Status header (or we can read
    // from our Prometheus metrics post-test). For now, we infer from timing:
    // sub-5ms responses are almost certainly cache hits.
    if (res.status === 302) {
        if (res.timings.duration < 5) {
            redirectCacheHits.add(1);
        } else {
            redirectCacheMisses.add(1);
        }
    } else if (res.status >= 500) {
        redirectCacheErrors.add(1);
    } else {
        redirectCacheErrors.add(1);
    }

    return res;
}

/**
 * Shorten a URL via the API service.
 * Returns { success: bool, shortCode: string|null, response: Response }
 */
export function doShorten(originalURL, customCode = null) {
    const payload = JSON.stringify({
        original_url: originalURL,
        ...(customCode ? { custom_code: customCode } : {}),
    });

    const res = http.post(
        `${BASE_URL_API}/api/v1/workspaces/${TEST_WORKSPACE_ID}/urls`,
        payload,
        {
            headers: { ...getAuthHeaders(), 'X-Forwarded-For': loadClientIP() },
            tags: { name: 'shorten' },
        }
    );

    apiWriteLatency.add(res.timings.duration);

    if (res.status === 201) {
        urlsShortenedOK.add(1);
        try {
            const body = JSON.parse(res.body);
            return { success: true, shortCode: body.data?.short_code, response: res };
        } catch {
            return { success: true, shortCode: null, response: res };
        }
    } else {
        urlsShortenedFail.add(1);
        errorRate.add(1);
        return { success: false, shortCode: null, response: res };
    }
}

/**
 * Shorten a URL scoped to the test workspace.
 */
export function doShortenInWorkspace(workspaceID, originalURL) {
    const payload = JSON.stringify({ original_url: originalURL });

    return http.post(
        `${BASE_URL_API}/api/v1/workspaces/${workspaceID}/urls`,
        payload,
        {
            headers: { ...getAuthHeaders(), 'X-Forwarded-For': loadClientIP() },
            tags: { name: 'shorten_workspace' },
        }
    );
}

/**
 * Generate a random URL for use as an original URL in shorten tests.
 * Using random paths prevents caching at any intermediate layer.
 */
export function randomURL() {
    const id = Math.random().toString(36).substring(2, 12);
    return `https://load-test-target.example.com/path/${id}?ts=${Date.now()}`;
}

/**
 * Assert standard checks on a redirect response.
 * Returns true if all checks pass.
 */
export function checkRedirectResponse(res, tag = '') {
    return check(res, {
        [`${tag} status is 302`]:             r => r.status === 302,
        [`${tag} has Location header`]:       r => r.headers['Location'] !== undefined,
        [`${tag} response time < 50ms`]:      r => r.timings.duration < 50,
        [`${tag} Cache-Control is no-store`]: r =>
            (r.headers['Cache-Control'] || '').includes('no-store'),
    });
}

/**
 * Assert standard checks on a shorten response.
 */
export function checkShortenResponse(res, tag = '') {
    return check(res, {
        [`${tag} status is 201`]:          r => r.status === 201,
        [`${tag} has short_url`]:          r => {
            try { return JSON.parse(r.body).data?.short_url !== undefined; }
            catch { return false; }
        },
        [`${tag} response time < 200ms`]: r => r.timings.duration < 200,
    });
}

/**
 * Think time — simulates realistic user pacing between requests.
 * Real users don't hammer endpoints as fast as possible.
 * Including think time makes RPS numbers more realistic.
 */
export function thinkTime(minMs = 100, maxMs = 500) {
    sleep((minMs + Math.random() * (maxMs - minMs)) / 1000);
}
