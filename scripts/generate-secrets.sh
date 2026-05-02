#!/usr/bin/env bash
set -euo pipefail

DRY_RUN="${1:-}"
NAMESPACE="${NAMESPACE:-urlshortener}"
SECRETS_DIR="${SECRETS_DIR:-deployments/kubernetes/secrets}"
CERT_FILE="${CERT_FILE:-/tmp/sealed-secrets-pub-$(date +%s).pem}"
CONTROLLER_NAME="${SEALED_SECRETS_CONTROLLER_NAME:-sealed-secrets-controller}"
CONTROLLER_NS="${SEALED_SECRETS_CONTROLLER_NS:-kube-system}"
SEALED_SECRETS_VERSION="${SEALED_SECRETS_VERSION:-2.15.3}"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
log() { echo -e "${GREEN}==>${NC} $*"; }
warn() { echo -e "${YELLOW}WARN:${NC} $*"; }
err() { echo -e "${RED}ERROR:${NC} $*" >&2; }

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "Required command not found: $1"
    exit 1
  fi
}

seal_value() {
  local secret_name="$1"
  local value="$2"
  local tmp_value_file
  tmp_value_file="$(mktemp)"
  printf '%s' "${value}" > "${tmp_value_file}"
  kubeseal \
    --raw \
    --from-file="${tmp_value_file}" \
    --namespace "${NAMESPACE}" \
    --name "${secret_name}" \
    --cert "${CERT_FILE}" \
    --scope strict
  rm -f "${tmp_value_file}"
}

write_or_print() {
  local file="$1"
  local content="$2"
  if [ "${DRY_RUN}" = "--dry-run" ]; then
    echo "--- ${file}"
    printf '%s\n' "${content}"
  else
    printf '%s\n' "${content}" > "${file}"
  fi
}

need_cmd kubectl
need_cmd helm
need_cmd kubeseal

if [ -f ".env" ]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
  log "Loaded .env"
else
  warn ".env not found; using local development defaults"
fi

POSTGRES_USER="${POSTGRES_USER:-urlshortener}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-secret}"
POSTGRES_DB="${POSTGRES_DB:-urlshortener}"
REDIS_ADDR="${REDIS_ADDR:-redis.urlshortener.svc.cluster.local:6379}"
REDIS_PASSWORD="${REDIS_PASSWORD:-secret}"
DB_PRIMARY_DSN="${DB_PRIMARY_DSN:-postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres.urlshortener.svc.cluster.local:5432/${POSTGRES_DB}?sslmode=disable}"
DB_REPLICA_DSN="${DB_REPLICA_DSN:-${DB_PRIMARY_DSN}}"
JWT_ISSUER="${JWT_ISSUER:-http://host.docker.internal:9000}"
JWT_AUDIENCE="${JWT_AUDIENCE:-url-shortener-api}"
JWT_PUBLIC_KEY_PATH="${JWT_PUBLIC_KEY_PATH:-/var/run/urlshortener-secrets/jwt_public.pem}"
BASE_URL="${BASE_URL:-https://r.shortener.local}"
EXPORT_SIGN_SECRET="${EXPORT_SIGN_SECRET:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}"
IP_HASH_SALT="${IP_HASH_SALT:-local-dev-salt-change-me}"

JWT_PUBLIC_KEY_VALUE="${JWT_PUBLIC_KEY_VALUE:-}"
if [ -z "${JWT_PUBLIC_KEY_VALUE}" ] && [ -f "${JWT_PUBLIC_KEY_PATH}" ]; then
  JWT_PUBLIC_KEY_VALUE="$(cat "${JWT_PUBLIC_KEY_PATH}")"
elif [ -z "${JWT_PUBLIC_KEY_VALUE}" ] && [ -f "./certs/jwt_public.pem" ]; then
  JWT_PUBLIC_KEY_VALUE="$(cat ./certs/jwt_public.pem)"
else
  JWT_PUBLIC_KEY_VALUE="${JWT_PUBLIC_KEY_VALUE:-placeholder-public-key-generate-certs-first}"
fi

log "Step 1: Ensuring Sealed Secrets controller is installed..."
if ! helm status sealed-secrets -n "${CONTROLLER_NS}" >/dev/null 2>&1; then
  helm repo add sealed-secrets https://bitnami-labs.github.io/sealed-secrets >/dev/null 2>&1 || true
  helm repo update sealed-secrets
  helm install sealed-secrets sealed-secrets/sealed-secrets \
    --namespace "${CONTROLLER_NS}" \
    --version "${SEALED_SECRETS_VERSION}" \
    --set fullnameOverride="${CONTROLLER_NAME}" \
    --wait --timeout=3m
  sleep 10
else
  log "Sealed Secrets controller already installed"
fi

log "Step 2: Fetching cluster public sealing key..."
kubeseal --fetch-cert \
  --controller-name="${CONTROLLER_NAME}" \
  --controller-namespace="${CONTROLLER_NS}" \
  > "${CERT_FILE}"

mkdir -p "${SECRETS_DIR}"

log "Step 3: Sealing postgres-credentials..."
DB_PRIMARY_ENC="$(seal_value postgres-credentials "${DB_PRIMARY_DSN}")"
DB_REPLICA_ENC="$(seal_value postgres-credentials "${DB_REPLICA_DSN}")"
DB_USER_ENC="$(seal_value postgres-credentials "${POSTGRES_USER}")"
DB_PASS_ENC="$(seal_value postgres-credentials "${POSTGRES_PASSWORD}")"

log "Step 4: Sealing redis-credentials..."
REDIS_ADDR_ENC="$(seal_value redis-credentials "${REDIS_ADDR}")"
REDIS_PASS_ENC="$(seal_value redis-credentials "${REDIS_PASSWORD}")"

log "Step 5: Sealing jwt-keys..."
JWT_PUBLIC_ENC="$(seal_value jwt-keys "${JWT_PUBLIC_KEY_VALUE}")"

log "Step 6: Sealing app-secrets..."
JWT_ISSUER_ENC="$(seal_value app-secrets "${JWT_ISSUER}")"
JWT_AUDIENCE_ENC="$(seal_value app-secrets "${JWT_AUDIENCE}")"
BASE_URL_ENC="$(seal_value app-secrets "${BASE_URL}")"

log "Step 7: Sealing compatibility secret urlshortener-secrets..."
LEGACY_POSTGRES_USER_ENC="$(seal_value urlshortener-secrets "${POSTGRES_USER}")"
LEGACY_POSTGRES_PASSWORD_ENC="$(seal_value urlshortener-secrets "${POSTGRES_PASSWORD}")"
LEGACY_POSTGRES_DB_ENC="$(seal_value urlshortener-secrets "${POSTGRES_DB}")"
LEGACY_REDIS_PASS_ENC="$(seal_value urlshortener-secrets "${REDIS_PASSWORD}")"
LEGACY_DB_PRIMARY_ENC="$(seal_value urlshortener-secrets "${DB_PRIMARY_DSN}")"
LEGACY_DB_REPLICA_ENC="$(seal_value urlshortener-secrets "${DB_REPLICA_DSN}")"
LEGACY_EXPORT_ENC="$(seal_value urlshortener-secrets "${EXPORT_SIGN_SECRET}")"
LEGACY_IP_HASH_ENC="$(seal_value urlshortener-secrets "${IP_HASH_SALT}")"
LEGACY_JWT_ISSUER_ENC="$(seal_value urlshortener-secrets "${JWT_ISSUER}")"
LEGACY_JWT_AUDIENCE_ENC="$(seal_value urlshortener-secrets "${JWT_AUDIENCE}")"
LEGACY_JWT_PATH_ENC="$(seal_value urlshortener-secrets "${JWT_PUBLIC_KEY_PATH}")"
LEGACY_JWT_PUBLIC_ENC="$(seal_value urlshortener-secrets "${JWT_PUBLIC_KEY_VALUE}")"

write_or_print "${SECRETS_DIR}/db-credentials.yaml" "apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: postgres-credentials
  namespace: ${NAMESPACE}
  annotations:
    sealedsecrets.bitnami.com/managed: \"true\"
spec:
  encryptedData:
    primary-dsn: ${DB_PRIMARY_ENC}
    replica-dsn: ${DB_REPLICA_ENC}
    username: ${DB_USER_ENC}
    password: ${DB_PASS_ENC}
  template:
    metadata:
      name: postgres-credentials
      namespace: ${NAMESPACE}
    type: Opaque"

write_or_print "${SECRETS_DIR}/redis-credentials.yaml" "apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: redis-credentials
  namespace: ${NAMESPACE}
  annotations:
    sealedsecrets.bitnami.com/managed: \"true\"
spec:
  encryptedData:
    addr: ${REDIS_ADDR_ENC}
    password: ${REDIS_PASS_ENC}
  template:
    metadata:
      name: redis-credentials
      namespace: ${NAMESPACE}
    type: Opaque"

write_or_print "${SECRETS_DIR}/jwt-keys.yaml" "apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: jwt-keys
  namespace: ${NAMESPACE}
  annotations:
    sealedsecrets.bitnami.com/managed: \"true\"
spec:
  encryptedData:
    public-key: ${JWT_PUBLIC_ENC}
  template:
    metadata:
      name: jwt-keys
      namespace: ${NAMESPACE}
    type: Opaque"

write_or_print "${SECRETS_DIR}/app-secrets.yaml" "apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: app-secrets
  namespace: ${NAMESPACE}
  annotations:
    sealedsecrets.bitnami.com/managed: \"true\"
spec:
  encryptedData:
    jwt-issuer: ${JWT_ISSUER_ENC}
    jwt-audience: ${JWT_AUDIENCE_ENC}
    base-url: ${BASE_URL_ENC}
  template:
    metadata:
      name: app-secrets
      namespace: ${NAMESPACE}
    type: Opaque"

write_or_print "${SECRETS_DIR}/urlshortener-secrets.yaml" "apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: urlshortener-secrets
  namespace: ${NAMESPACE}
  annotations:
    sealedsecrets.bitnami.com/patch: \"true\"
spec:
  encryptedData:
    POSTGRES_USER: ${LEGACY_POSTGRES_USER_ENC}
    POSTGRES_PASSWORD: ${LEGACY_POSTGRES_PASSWORD_ENC}
    POSTGRES_DB: ${LEGACY_POSTGRES_DB_ENC}
    REDIS_PASSWORD: ${LEGACY_REDIS_PASS_ENC}
    DB_PRIMARY_DSN: ${LEGACY_DB_PRIMARY_ENC}
    DB_REPLICA_DSN: ${LEGACY_DB_REPLICA_ENC}
    EXPORT_SIGN_SECRET: ${LEGACY_EXPORT_ENC}
    IP_HASH_SALT: ${LEGACY_IP_HASH_ENC}
    JWT_ISSUER: ${LEGACY_JWT_ISSUER_ENC}
    JWT_AUDIENCE: ${LEGACY_JWT_AUDIENCE_ENC}
    JWT_PUBLIC_KEY_PATH: ${LEGACY_JWT_PATH_ENC}
    jwt_public.pem: ${LEGACY_JWT_PUBLIC_ENC}
  template:
    metadata:
      name: urlshortener-secrets
      namespace: ${NAMESPACE}
      annotations:
        sealedsecrets.bitnami.com/patch: \"true\"
    type: Opaque"

if [ "${DRY_RUN}" = "--dry-run" ]; then
  log "Dry run complete; no files written or resources applied"
  exit 0
fi

log "Step 8: Applying generated SealedSecrets..."
kubectl apply -f "${SECRETS_DIR}/db-credentials.yaml"
kubectl apply -f "${SECRETS_DIR}/redis-credentials.yaml"
kubectl apply -f "${SECRETS_DIR}/jwt-keys.yaml"
kubectl apply -f "${SECRETS_DIR}/app-secrets.yaml"
if kubectl get secret urlshortener-secrets -n "${NAMESPACE}" >/dev/null 2>&1; then
  warn "urlshortener-secrets already exists; leaving existing compatibility Secret in place"
else
  kubectl apply -f "${SECRETS_DIR}/urlshortener-secrets.yaml"
fi

log "Sealed secrets generated and applied"
