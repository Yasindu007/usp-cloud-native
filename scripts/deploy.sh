#!/usr/bin/env bash
set -euo pipefail

IMAGE_TAG="${OVERRIDE_TAG:-latest}"
REGISTRY="${REGISTRY:-localhost:5001}"
NAMESPACE="${NAMESPACE:-urlshortener}"
SKIP_WAIT="${SKIP_WAIT:-false}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image-tag)
      IMAGE_TAG="${2:?--image-tag requires a value}"
      shift 2
      ;;
    *)
      IMAGE_TAG="$1"
      shift
      ;;
  esac
done

for candidate in "$HOME/go/bin" "/c/Program Files/Docker/Docker/resources/bin"; do
  if [ -d "$candidate" ] && [[ ":$PATH:" != *":$candidate:"* ]]; then
    PATH="$candidate:$PATH"
  fi
done

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log() { echo -e "${GREEN}==>${NC} $*"; }
warn() { echo -e "${YELLOW}WARN:${NC} $*"; }

apply_dir_if_exists() {
  local dir="$1"
  if [ -d "${dir}" ]; then
    kubectl apply -f "${dir}"
  fi
}

apply_secret_file() {
  local file="$1"
  local name
  name="$(basename "${file}")"

  if [ "${name}" = "sealed-secrets-controller.yaml" ]; then
    kubectl apply -f "${file}"
    return
  fi

  if grep -q "PLACEHOLDER_ENCRYPT_WITH_KUBESEAL" "${file}" 2>/dev/null; then
    warn "Skipping ${file}; contains placeholder SealedSecret values"
    return
  fi

  if [ "${name}" = "urlshortener-secrets.yaml" ] && kubectl get secret urlshortener-secrets -n "${NAMESPACE}" >/dev/null 2>&1; then
    warn "Skipping ${file}; urlshortener-secrets already exists in this cluster"
    return
  fi

  # Keep the legacy local Secret as a fallback only until a generated
  # urlshortener-secrets SealedSecret exists.
  if [ "${name}" = "sealed-placeholder.yaml" ] && [ -f "${ROOT_DIR}/deployments/kubernetes/secrets/urlshortener-secrets.yaml" ]; then
    if ! grep -q "PLACEHOLDER_ENCRYPT_WITH_KUBESEAL" "${ROOT_DIR}/deployments/kubernetes/secrets/urlshortener-secrets.yaml"; then
      warn "Skipping ${file}; generated urlshortener-secrets.yaml exists"
      return
    fi
  fi

  kubectl apply -f "${file}"
}

log "Deploying URL Shortener Platform"
echo "  Registry:  ${REGISTRY}"
echo "  Image tag: ${IMAGE_TAG}"
echo "  Namespace: ${NAMESPACE}"
echo ""

kubectl config use-context kind-urlshortener >/dev/null 2>&1 || true

log "Step 1: Applying namespaces"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/namespaces.yaml"

log "Step 2: Applying secrets"
if [ -d "${ROOT_DIR}/deployments/kubernetes/secrets" ]; then
  while IFS= read -r -d '' file; do
    apply_secret_file "${file}"
  done < <(find "${ROOT_DIR}/deployments/kubernetes/secrets" -maxdepth 1 -name "*.yaml" -print0 | sort -z)
fi

log "Step 3: Applying ConfigMaps"
apply_dir_if_exists "${ROOT_DIR}/deployments/kubernetes/configmaps"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/api/configmap.yaml"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/redirector/configmap.yaml"

log "Step 4: Applying data stores"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/postgres/"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/redis/"

if [ "${SKIP_WAIT}" != "true" ]; then
  kubectl wait --for=condition=Ready pod/postgres-0 -n "${NAMESPACE}" --timeout=180s
  kubectl wait --for=condition=Ready pod/redis-0 -n "${NAMESPACE}" --timeout=180s
fi

log "Step 5: Running migrations"
kubectl delete job migrate -n "${NAMESPACE}" --ignore-not-found
sed "s|localhost:5001/urlshortener/migrate:latest|${REGISTRY}/urlshortener/migrate:${IMAGE_TAG}|g" \
  "${ROOT_DIR}/deployments/kubernetes/migrate/job.yaml" | kubectl apply -f -
if [ "${SKIP_WAIT}" != "true" ]; then
  kubectl wait --for=condition=complete job/migrate -n "${NAMESPACE}" --timeout=180s
fi

log "Step 6: Deploying services"
sed "s|localhost:5001/urlshortener/api:latest|${REGISTRY}/urlshortener/api:${IMAGE_TAG}|g" \
  "${ROOT_DIR}/deployments/kubernetes/api/deployment.yaml" | kubectl apply -f -
sed "s|localhost:5001/urlshortener/redirector:latest|${REGISTRY}/urlshortener/redirector:${IMAGE_TAG}|g" \
  "${ROOT_DIR}/deployments/kubernetes/redirector/deployment.yaml" | kubectl apply -f -
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/api/service.yaml"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/redirector/service.yaml"

log "Step 7: Applying HPA and PDB"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/api/hpa.yaml"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/api/pdb.yaml"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/redirector/hpa.yaml"
kubectl apply -f "${ROOT_DIR}/deployments/kubernetes/redirector/pdb.yaml"

log "Step 8: Applying network and ingress"
apply_dir_if_exists "${ROOT_DIR}/deployments/kubernetes/network"
apply_dir_if_exists "${ROOT_DIR}/deployments/kubernetes/ingress"

if [ "${SKIP_WAIT}" != "true" ]; then
  kubectl rollout status deployment/api -n "${NAMESPACE}" --timeout=5m
  kubectl rollout status deployment/redirector -n "${NAMESPACE}" --timeout=5m
fi

log "Deployment complete"
