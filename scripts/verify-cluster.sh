#!/usr/bin/env bash
set -euo pipefail

REGISTRY="${REGISTRY:-localhost:5001}"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-urlshortener}"

for candidate in "$HOME/go/bin" "/c/Program Files/Docker/Docker/resources/bin"; do
  if [ -d "$candidate" ] && [[ ":$PATH:" != *":$candidate:"* ]]; then
    PATH="$candidate:$PATH"
  fi
done

PASS=0
FAIL=0

green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
red() { printf '\033[0;31m%s\033[0m\n' "$*"; }
blue() { printf '\033[0;34m%s\033[0m\n' "$*"; }

check() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    green "PASS  $name"
    PASS=$((PASS + 1))
  else
    red "FAIL  $name"
    FAIL=$((FAIL + 1))
  fi
}

blue "Registry"
check "registry v2 endpoint" curl -fsS "http://${REGISTRY}/v2/"

blue "Cluster"
check "kind cluster exists" bash -lc "kind get clusters 2>/dev/null | grep -qx '$CLUSTER_NAME'"
check "kubectl context" bash -lc "kubectl config current-context | grep -qx 'kind-${CLUSTER_NAME}'"
check "nodes ready" bash -lc "[ \"\$(kubectl get nodes --no-headers 2>/dev/null | awk '\$2 != \"Ready\" {print \$1}' | wc -l | tr -d ' ')\" = '0' ]"

blue "Platform"
check "urlshortener namespace" kubectl get ns urlshortener
check "postgres ready" kubectl get pod/postgres-0 -n urlshortener
check "redis ready" kubectl get pod/redis-0 -n urlshortener
check "api deployment available" kubectl rollout status deployment/api -n urlshortener --timeout=5s
check "redirector deployment available" kubectl rollout status deployment/redirector -n urlshortener --timeout=5s
check "api hpa present" kubectl get hpa/api-hpa -n urlshortener
check "redirector hpa present" kubectl get hpa/redirector-hpa -n urlshortener

blue "Ingress"
check "api health" curl -sk --resolve api.shortener.local:443:127.0.0.1 https://api.shortener.local/healthz
check "redirector health" curl -sk --resolve r.shortener.local:443:127.0.0.1 https://r.shortener.local/healthz

blue "Observability"
check "grafana login" bash -lc "[ \"\$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30300/login)\" = '200' ]"
check "metrics available" kubectl top pods -n urlshortener

printf '\n'
printf 'Passed: %s   Failed: %s\n' "$PASS" "$FAIL"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
