#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REGISTRY="${REGISTRY:-localhost:5001}"
TAG="${1:-latest}"

log() { printf '==> %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }; }

for cmd in docker kind kubectl terraform helm sed; do
  need "$cmd"
done

log "Provisioning local registry and Kind cluster"
cd "$ROOT_DIR/deployments/terraform"
terraform init -upgrade
terraform apply -auto-approve
cd "$ROOT_DIR"

kubectl config use-context kind-urlshortener >/dev/null
kubectl wait --for=condition=Ready node --all --timeout=180s

log "Building and pushing images"
"$ROOT_DIR/scripts/push-images.sh" "$TAG"

log "Installing ingress-nginx"
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx --force-update >/dev/null
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx \
  --create-namespace \
  --set controller.hostPort.enabled=true \
  --set controller.service.type=NodePort \
  --set-string controller.nodeSelector.ingress-ready=true \
  --set controller.tolerations[0].key=node-role.kubernetes.io/control-plane \
  --set controller.tolerations[0].operator=Exists \
  --set controller.tolerations[0].effect=NoSchedule \
  --set controller.admissionWebhooks.enabled=false \
  --wait

log "Installing cert-manager"
helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set installCRDs=true \
  --wait

log "Installing metrics-server"
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update >/dev/null
helm upgrade --install metrics-server metrics-server/metrics-server \
  --namespace kube-system \
  --set args[0]=--kubelet-insecure-tls \
  --wait

log "Installing kube-prometheus-stack"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --create-namespace \
  --set grafana.adminPassword=admin \
  --set grafana.service.type=NodePort \
  --set grafana.service.nodePort=30300 \
  --wait

log "Applying application manifests"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/namespaces.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/configmaps/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/secrets/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/postgres/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redis/"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/configmap.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/configmap.yaml"
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/ingress/ingress-nginx.yaml"

kubectl wait --for=condition=Ready pod/postgres-0 -n urlshortener --timeout=180s
kubectl wait --for=condition=Ready pod/redis-0 -n urlshortener --timeout=180s

log "Running migrations"
kubectl delete job migrate -n urlshortener --ignore-not-found
sed "s|localhost:5001/urlshortener/migrate:latest|$REGISTRY/urlshortener/migrate:$TAG|" \
  "$ROOT_DIR/deployments/kubernetes/migrate/job.yaml" | kubectl apply -f -
kubectl wait --for=condition=complete job/migrate -n urlshortener --timeout=180s

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
kubectl apply -f "$ROOT_DIR/deployments/kubernetes/ingress/ingress-routes.yaml"

kubectl rollout status deployment/api -n urlshortener --timeout=5m
kubectl rollout status deployment/redirector -n urlshortener --timeout=5m

log "Cluster is ready"
log "Add to hosts file: 127.0.0.1 api.shortener.local r.shortener.local"
log "API: https://api.shortener.local/healthz"
log "Redirector: https://r.shortener.local/healthz"
log "Grafana: http://localhost:30300 (admin/admin)"
