#!/usr/bin/env bash
set -euo pipefail

TAG="${1:-latest}"
REGISTRY="${REGISTRY:-localhost:5001}"
NAMESPACE="${NAMESPACE:-urlshortener}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '==> %s\n' "$*"; }

kubectl config use-context kind-urlshortener >/dev/null

log "Applying base manifests"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/namespaces.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/configmaps/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/secrets/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/postgres/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redis/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/configmap.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/configmap.yaml"

kubectl wait --for=condition=Ready pod/postgres-0 -n "$NAMESPACE" --timeout=180s
kubectl wait --for=condition=Ready pod/redis-0 -n "$NAMESPACE" --timeout=180s

log "Running migrations"
kubectl delete job migrate -n "$NAMESPACE" --ignore-not-found
sed "s|localhost:5001/urlshortener/migrate:latest|$REGISTRY/urlshortener/migrate:$TAG|" \
  "$ROOT_DIR/deployments/kubernetes/migrate/job.yaml" | kubectl apply -f -
kubectl wait --for=condition=complete job/migrate -n "$NAMESPACE" --timeout=180s

log "Deploying services"
sed "s|localhost:5001/urlshortener/api:latest|$REGISTRY/urlshortener/api:$TAG|" \
  "$ROOT_DIR/deployments/kubernetes/api/deployment.yaml" | kubectl apply -f -
sed "s|localhost:5001/urlshortener/redirector:latest|$REGISTRY/urlshortener/redirector:$TAG|" \
  "$ROOT_DIR/deployments/kubernetes/redirector/deployment.yaml" | kubectl apply -f -
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/service.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/hpa.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/pdb.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/service.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/hpa.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/pdb.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/network/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/ingress/"

kubectl rollout status deployment/api -n "$NAMESPACE" --timeout=5m
kubectl rollout status deployment/redirector -n "$NAMESPACE" --timeout=5m
