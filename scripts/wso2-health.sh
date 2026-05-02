#!/usr/bin/env bash
# Check WSO2 management, gateway, and local ingress reachability.

set -euo pipefail

WSO2_HOST="${WSO2_HOST:-https://localhost:9443}"
WSO2_HTTP="${WSO2_HTTP:-http://localhost:8280}"
MOCK_ISSUER_URL="${MOCK_ISSUER_URL:-http://127.0.0.1:9000}"
CURL=(curl -sk)
if command -v curl.exe >/dev/null 2>&1; then
  MOCK_CURL=(curl.exe -fsS)
else
  MOCK_CURL=(curl -fsS)
fi
PASS=0
FAIL=0

check() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    printf '  OK   %s\n' "$name"
    PASS=$((PASS + 1))
  else
    printf '  FAIL %s\n' "$name"
    FAIL=$((FAIL + 1))
  fi
}

http_code_matches() {
  local url="$1"
  local pattern="$2"
  local code
  code="$("${CURL[@]}" -o /dev/null -w '%{http_code}' "$url")"
  printf '%s' "$code" | grep -Eq "$pattern"
}

contains() {
  local url="$1"
  local pattern="$2"
  "${CURL[@]}" "$url" | grep -Eq "$pattern"
}

mock_contains() {
  local url="$1"
  local pattern="$2"
  "${MOCK_CURL[@]}" "$url" | grep -Eq "$pattern"
}

printf '==> WSO2 API Manager health check\n'
printf '    Management: %s\n' "$WSO2_HOST"
printf '    Gateway:    %s\n\n' "$WSO2_HTTP"

printf 'Management API:\n'
check "Carbon version endpoint responds" contains "${WSO2_HOST}/services/Version" "version|Version"
check "Publisher API is reachable" http_code_matches "${WSO2_HOST}/api/am/publisher/v4/apis" "^(200|401)$"
check "Developer Portal API is reachable" http_code_matches "${WSO2_HOST}/api/am/devportal/v3/apis" "^(200|401)$"

printf '\nGateway:\n'
check "HTTP gateway port responds" http_code_matches "${WSO2_HTTP}/" "^(200|302|404|401|403)$"
check "Management API route responds" http_code_matches "${WSO2_HTTP}/api/v1/1.0/healthz" "^(200|401|403|404)$"

printf '\nIngress from WSO2 container:\n'
if docker inspect urlshortener-wso2 >/dev/null 2>&1; then
  check "api.shortener.local resolves to ingress" docker exec urlshortener-wso2 curl -fsS http://api.shortener.local/healthz
  check "r.shortener.local resolves to ingress" docker exec urlshortener-wso2 curl -fsS http://r.shortener.local/healthz
else
  printf '  WARN WSO2 container is not present; skipping container-to-ingress checks\n'
fi

printf '\nMock issuer:\n'
check "Mock issuer health endpoint responds" mock_contains "${MOCK_ISSUER_URL}/healthz" "alive"
check "Mock issuer OIDC discovery responds" mock_contains "${MOCK_ISSUER_URL}/.well-known/openid-configuration" "issuer"

printf '\nSummary: %s passed, %s failed\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  printf '\nWSO2 may still be starting. Check: docker logs urlshortener-wso2 --tail=100\n'
  exit 1
fi

printf '\nWSO2 is healthy enough for local development.\n'
