#!/usr/bin/env bash
set -euo pipefail

SECRET_NAME="${1:?Usage: $0 <secret-name> <key-name> <value>}"
KEY_NAME="${2:?Usage: $0 <secret-name> <key-name> <value>}"
VALUE="${3:?Usage: $0 <secret-name> <key-name> <value>}"
NAMESPACE="${NAMESPACE:-urlshortener}"
CONTROLLER_NAME="${SEALED_SECRETS_CONTROLLER_NAME:-sealed-secrets-controller}"
CONTROLLER_NS="${SEALED_SECRETS_CONTROLLER_NS:-kube-system}"
CERT_FILE="${CERT_FILE:-/tmp/sealed-secrets-pub.pem}"

if [ ! -f "${CERT_FILE}" ]; then
  echo "==> Fetching sealed-secrets public key..."
  kubeseal --fetch-cert \
    --controller-name="${CONTROLLER_NAME}" \
    --controller-namespace="${CONTROLLER_NS}" \
    > "${CERT_FILE}"
  echo "    Cached at: ${CERT_FILE}"
fi

echo "==> Encrypting ${KEY_NAME} for ${SECRET_NAME} in namespace ${NAMESPACE}..."
TMP_VALUE_FILE="$(mktemp)"
trap 'rm -f "${TMP_VALUE_FILE}"' EXIT
printf '%s' "${VALUE}" > "${TMP_VALUE_FILE}"
ENCRYPTED=$(kubeseal \
  --raw \
  --from-file="${TMP_VALUE_FILE}" \
  --namespace "${NAMESPACE}" \
  --name "${SECRET_NAME}" \
  --cert "${CERT_FILE}" \
  --scope strict)

echo ""
echo "Encrypted value for ${KEY_NAME}:"
echo "${ENCRYPTED}"
