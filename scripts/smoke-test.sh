#!/usr/bin/env bash
set -euo pipefail

# Smoke test the deployed API and redirector. The test intentionally uses the
# workspace-scoped URL creation route that this service exposes without an
# external identity provider, which keeps CI/CD verification independent of WSO2.
API_URL="${1:-${API_URL:-http://api.shortener.local}}"
REDIRECT_URL="${2:-${REDIRECT_URL:-http://r.shortener.local}}"
# The local migrations seed ws_default, so the default smoke test can run
# against a fresh development database without creating extra workspace state.
WORKSPACE_ID="${WORKSPACE_ID:-ws_default}"
USER_ID="${USER_ID:-usr_smoke}"

log() { printf '==> %s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

need curl

API_URL="${API_URL%/}"
REDIRECT_URL="${REDIRECT_URL%/}"
payload="$(mktemp)"
response="$(mktemp)"
trap 'rm -f "$payload" "$response"' EXIT

cat > "$payload" <<JSON
{
  "original_url": "https://example.com/smoke-${RANDOM:-0}",
  "title": "smoke-test"
}
JSON

log "Checking API health at ${API_URL}/healthz"
curl -fsS "${API_URL}/healthz" >/dev/null

log "Checking redirector health at ${REDIRECT_URL}/healthz"
curl -fsS "${REDIRECT_URL}/healthz" >/dev/null

log "Creating a short URL in workspace ${WORKSPACE_ID}"
status="$(
  curl -sS -o "$response" -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    -H "X-User-ID: ${USER_ID}" \
    -X POST \
    --data @"$payload" \
    "${API_URL}/api/v1/workspaces/${WORKSPACE_ID}/urls"
)"

if [ "$status" != "201" ]; then
  cat "$response" >&2
  fail "expected 201 from shorten endpoint, got ${status}"
fi

short_code="$(
  python - "$response" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as fh:
    body = json.load(fh)
print(body.get("data", {}).get("short_code", ""))
PY
)"

[ -n "$short_code" ] || fail "short_code missing from shorten response"

log "Verifying redirect for ${short_code}"
status="$(curl -sS -o /dev/null -w '%{http_code}' "${REDIRECT_URL}/${short_code}")"
case "$status" in
  301|302|307|308) ;;
  *) fail "expected redirect success status, got ${status}" ;;
esac

log "Smoke test passed"
