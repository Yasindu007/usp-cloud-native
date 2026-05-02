#!/usr/bin/env bash
set -euo pipefail

SHORT_CODE="${1:-abc1234}"
DURATION="${2:-120}"
NAMESPACE="${NAMESPACE:-urlshortener}"
LOAD_NAMESPACE="${LOAD_NAMESPACE:-ingress-nginx}"
LOAD_POD="${LOAD_POD:-hpa-load-generator}"
REDIRECTOR_SVC="redirector.${NAMESPACE}.svc.cluster.local:80"

GREEN='\033[0;32m'; BLUE='\033[0;34m'; NC='\033[0m'
log() { echo -e "${GREEN}==>${NC} $*"; }

cleanup() {
  kubectl delete pod "${LOAD_POD}" -n "${LOAD_NAMESPACE}" --ignore-not-found >/dev/null 2>&1 || true
  if [ -n "${WATCHER_PID:-}" ]; then
    kill "${WATCHER_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

if [ -z "${KUBECONFIG:-}" ] && [ -f "/mnt/c/Users/${USER}/.kube/config" ]; then
  export KUBECONFIG="/mnt/c/Users/${USER}/.kube/config"
fi

kubectl config use-context kind-urlshortener >/dev/null 2>&1 || true

log "Verifying prerequisites..."
kubectl get deployment redirector -n "${NAMESPACE}" >/dev/null
kubectl get hpa redirector-hpa -n "${NAMESPACE}" >/dev/null
kubectl top pods -n "${NAMESPACE}" >/dev/null

INITIAL_REPLICAS=$(kubectl get hpa redirector-hpa -n "${NAMESPACE}" -o jsonpath='{.status.currentReplicas}' 2>/dev/null || echo "unknown")

log "Starting HPA load test"
echo "  Target:           http://${REDIRECTOR_SVC}/${SHORT_CODE}"
echo "  Duration:         ${DURATION}s"
echo "  Load namespace:   ${LOAD_NAMESPACE}"
echo "  Initial replicas: ${INITIAL_REPLICAS}"
echo ""

(
  while true; do
    echo -e "\n${BLUE}---- HPA Status $(date '+%H:%M:%S') ----${NC}"
    kubectl get hpa -n "${NAMESPACE}" 2>/dev/null || true
    echo ""
    kubectl top pods -n "${NAMESPACE}" 2>/dev/null || true
    sleep 15
  done
) &
WATCHER_PID=$!

kubectl delete pod "${LOAD_POD}" -n "${LOAD_NAMESPACE}" --ignore-not-found >/dev/null 2>&1 || true

kubectl run "${LOAD_POD}" \
  -n "${LOAD_NAMESPACE}" \
  --image=curlimages/curl:8.8.0 \
  --restart=Never \
  --command -- sh -c "
    END=\$(( \$(date +%s) + ${DURATION} ))
    while [ \$(date +%s) -lt \$END ]; do
      for i in 1 2 3 4 5 6 7 8 9 10; do
        (while [ \$(date +%s) -lt \$END ]; do
          curl -fsS -o /dev/null -m 2 http://${REDIRECTOR_SVC}/${SHORT_CODE} || true
        done) &
      done
      wait
    done
  "

kubectl wait --for=condition=Ready pod/"${LOAD_POD}" -n "${LOAD_NAMESPACE}" --timeout=60s || true
kubectl logs -f "${LOAD_POD}" -n "${LOAD_NAMESPACE}" || true
kubectl wait --for=condition=Succeeded pod/"${LOAD_POD}" -n "${LOAD_NAMESPACE}" --timeout="$((DURATION + 60))s" || true

FINAL_REPLICAS=$(kubectl get hpa redirector-hpa -n "${NAMESPACE}" -o jsonpath='{.status.currentReplicas}' 2>/dev/null || echo "unknown")
echo ""
log "Load test complete"
echo "  Initial replicas: ${INITIAL_REPLICAS}"
echo "  Final replicas:   ${FINAL_REPLICAS}"
