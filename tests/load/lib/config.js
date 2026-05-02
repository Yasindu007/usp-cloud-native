// ============================================================
// config.js — Centralised test configuration
//
// All environment-variable-driven so the same test files work
// against local Docker, Kind cluster, or a remote environment
// without code changes.
//
// Usage in test files:
//   import { BASE_URL_API, BASE_URL_REDIRECT, getAuthHeaders } from '../lib/config.js';
// ============================================================

// ── Target URLs ───────────────────────────────────────────────────────────────
// Default: local Docker services
// Override: K6_API_URL=http://api.shortener.local k6 run ...
export const BASE_URL_API      = __ENV.K6_API_URL      || 'http://localhost:8080';
export const BASE_URL_REDIRECT = __ENV.K6_REDIRECT_URL || 'http://localhost:8081';

// ── Authentication ────────────────────────────────────────────────────────────
// For load testing we use a pre-minted long-lived JWT from the mock issuer,
// or a static API key. Both are injected via environment variables so secrets
// never appear in test files committed to source control.
//
// To generate a test token:
//   TOKEN=$(bash scripts/get-token.sh ws_loadtest usr_loadtest "read write")
//   k6 run -e K6_AUTH_TOKEN=$TOKEN tests/load/scenarios/redirect_baseline.js
export const AUTH_TOKEN      = __ENV.K6_AUTH_TOKEN      || '';
export const API_KEY         = __ENV.K6_API_KEY         || '';

// ── Test workspace ────────────────────────────────────────────────────────────
// A dedicated workspace for load test data — keeps test data isolated
// from development data and allows easy cleanup.
export const TEST_WORKSPACE_ID = __ENV.K6_WORKSPACE_ID || 'ws_loadtest';

// ── Pre-seeded short codes ────────────────────────────────────────────────────
// The redirect baseline test needs existing short codes to resolve.
// Pre-seed them with: bash scripts/run-load-test.sh --seed
// Then provide the codes via this env var (comma-separated).
export const SEEDED_SHORT_CODES = (__ENV.K6_SHORT_CODES || 'test001,test002,test003')
    .split(',')
    .map(s => s.trim())
    .filter(Boolean);

// ── Build auth headers ────────────────────────────────────────────────────────
export function getAuthHeaders() {
    if (API_KEY) {
        return {
            'X-API-Key': API_KEY,
            'Content-Type': 'application/json',
        };
    }
    if (AUTH_TOKEN) {
        return {
            'Authorization': `Bearer ${AUTH_TOKEN}`,
            'Content-Type': 'application/json',
        };
    }
    // Phase 1 compatibility: header-based identity (no JWT required)
    return {
        'X-Workspace-ID': TEST_WORKSPACE_ID,
        'X-User-ID':      'usr_loadtest',
        'Content-Type':   'application/json',
    };
}

// ── SLO thresholds (must match PRD-USP-001) ───────────────────────────────────
// These are the absolute limits that map directly to our SLOs.
// Any test that violates these causes k6 to exit with a non-zero code,
// which fails the CI pipeline.
export const SLO = {
    // SLO-03: Redirect P99 < 50ms
    REDIRECT_P99_MS:  parseInt(__ENV.K6_REDIRECT_P99_MS  || '50'),
    // SLO-04: API write P99 < 200ms
    API_WRITE_P99_MS: parseInt(__ENV.K6_API_WRITE_P99_MS || '200'),
    // SLO-01: Redirect availability 99.95%
    REDIRECT_ERROR_RATE: parseFloat(__ENV.K6_REDIRECT_ERROR_RATE || '0.0005'),
    // SLO-02: API availability 99.9%
    API_ERROR_RATE: parseFloat(__ENV.K6_API_ERROR_RATE || '0.001'),
    // SLO-05: Cache hit ratio > 85% (validated post-test via Prometheus)
    CACHE_HIT_RATIO: parseFloat(__ENV.K6_CACHE_HIT_RATIO || '0.85'),
};
