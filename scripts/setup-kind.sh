#!/usr/bin/env bash
# Bootstrap the local Kind cluster and print the DNS/WSO2 steps needed for
# the Story 4.3 gateway topology.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-urlshortener}"
REGISTRY="${REGISTRY:-localhost:5001}"
SKIP_MANIFESTS="${1:-}"

for candidate in \
  "$HOME/go/bin" \
  "/c/Program Files/Docker/Docker/resources/bin" \
  "$HOME/AppData/Local/Microsoft/WinGet/Packages/Hashicorp.Terraform_Microsoft.Winget.Source_8wekyb3d8bbwe"
do
  if [ -d "$candidate" ] && [[ ":$PATH:" != *":$candidate:"* ]]; then
    PATH="$candidate:$PATH"
  fi
done

log() { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || { printf 'missing required command: %s\n' "$1" >&2; exit 1; }; }

for cmd in docker kubectl helm curl kind terraform; do
  need "$cmd"
done

registry_has_image() {
  local service="$1"
  curl -fsS "http://${REGISTRY}/v2/urlshortener/${service}/tags/list" >/dev/null 2>&1
}

deploy_platform() {
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/namespaces.yaml"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/configmaps/"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/secrets/"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/postgres/"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redis/"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/configmap.yaml"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/configmap.yaml"
  kubectl apply -f "$ROOT_DIR/deployments/kubernetes/ingress/ingress-nginx.yaml"

  kubectl wait --for=condition=Ready pod/postgres-0 -n urlshortener --timeout=300s
  kubectl wait --for=condition=Ready pod/redis-0 -n urlshortener --timeout=300s

  if registry_has_image api && registry_has_image redirector && registry_has_image migrate; then
    log "Registry has application images; applying app workloads"
    kubectl delete job migrate -n urlshortener --ignore-not-found
    kubectl apply -f "$ROOT_DIR/deployments/kubernetes/migrate/job.yaml"
    kubectl wait --for=condition=complete job/migrate -n urlshortener --timeout=300s

    kubectl apply -f "$ROOT_DIR/deployments/kubernetes/api/"
    kubectl apply -f "$ROOT_DIR/deployments/kubernetes/redirector/"
    kubectl apply -f "$ROOT_DIR/deployments/kubernetes/network/"
    kubectl apply -f "$ROOT_DIR/deployments/kubernetes/ingress/ingress-routes.yaml"

    kubectl rollout status deployment/api -n urlshortener --timeout=600s
    kubectl rollout status deployment/redirector -n urlshortener --timeout=600s
  else
    warn "registry images are missing; skipping app deployments"
    warn "run scripts/build-images.sh && scripts/push-images.sh, then scripts/deploy.sh"
  fi
}

print_next_steps() {
  cat <<EOF

Kind cluster bootstrap complete.

Cluster:  ${CLUSTER_NAME}
Registry: http://${REGISTRY}
Grafana:  http://localhost:30300

Required local DNS entries for NGINX ingress and WSO2:
  127.0.0.1 api.shortener.local r.shortener.local

Linux/macOS:
  echo '127.0.0.1 api.shortener.local r.shortener.local' | sudo tee -a /etc/hosts

Windows:
  Add the same line to:
  C:\\Windows\\System32\\drivers\\etc\\hosts

Verify ingress before starting WSO2:
  curl http://api.shortener.local/healthz
  curl http://r.shortener.local/healthz

Start and seed WSO2 separately:
  make wso2-up
  make wso2-wait
  make wso2-health
  make wso2-seed
EOF
}

log "Phase 1: ensuring local registry is running"
if docker inspect urlshortener-registry >/dev/null 2>&1; then
  docker start urlshortener-registry >/dev/null 2>&1 || true
else
  docker compose -f "$ROOT_DIR/docker-compose.registry.yml" up -d
fi
curl -fsS "http://${REGISTRY}/v2/" >/dev/null

log "Phase 2: provisioning kind cluster via terraform"
cd "$ROOT_DIR/deployments/terraform"
terraform init -upgrade
terraform apply -auto-approve
cd "$ROOT_DIR"

kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null
kubectl wait --for=condition=Ready node --all --timeout=180s

log "Phase 3: installing ingress-nginx"
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

log "Phase 4: installing metrics-server"
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update >/dev/null
helm upgrade --install metrics-server metrics-server/metrics-server \
  --namespace kube-system \
  --set args[0]=--kubelet-insecure-tls \
  --wait

log "Phase 5: installing cert-manager"
helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set installCRDs=true \
  --wait

log "Phase 6: installing kube-prometheus-stack"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --create-namespace \
  --set grafana.adminPassword=admin \
  --set grafana.service.type=NodePort \
  --set grafana.service.nodePort=30300 \
  --wait

if [ "$SKIP_MANIFESTS" = "--skip-manifests" ]; then
  log "Skipping Kubernetes manifest application"
  print_next_steps
  exit 0
fi

log "Phase 7: applying platform manifests"
deploy_platform
print_next_steps
