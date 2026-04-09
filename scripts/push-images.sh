#!/usr/bin/env bash
set -euo pipefail

REGISTRY="${REGISTRY:-localhost:5001}"
TAG="${1:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-urlshortener}"

for candidate in "$HOME/go/bin" "/c/Program Files/Docker/Docker/resources/bin"; do
  if [ -d "$candidate" ] && [[ ":$PATH:" != *":$candidate:"* ]]; then
    PATH="$candidate:$PATH"
  fi
done

log() { printf '==> %s\n' "$*"; }

for service in api redirector migrate; do
  for ref in "$REGISTRY/urlshortener/$service:$TAG" "$REGISTRY/urlshortener/$service:latest"; do
    docker image inspect "$ref" >/dev/null 2>&1 || {
      echo "missing local image: $ref" >&2
      echo "run scripts/build-images.sh ${TAG} first" >&2
      exit 1
    }
  done

  log "Pushing ${service}:${TAG}"
  docker push "$REGISTRY/urlshortener/$service:$TAG"
  docker push "$REGISTRY/urlshortener/$service:latest"
done

if command -v kind >/dev/null 2>&1 && kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  log "Loading images into kind cluster ${CLUSTER_NAME}"
  kind load docker-image \
    "$REGISTRY/urlshortener/api:$TAG" \
    "$REGISTRY/urlshortener/api:latest" \
    "$REGISTRY/urlshortener/redirector:$TAG" \
    "$REGISTRY/urlshortener/redirector:latest" \
    "$REGISTRY/urlshortener/migrate:$TAG" \
    "$REGISTRY/urlshortener/migrate:latest" \
    --name "$CLUSTER_NAME"
fi
