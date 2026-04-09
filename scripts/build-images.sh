#!/usr/bin/env bash
set -euo pipefail

REGISTRY="${REGISTRY:-localhost:5001}"
TAG="${1:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for candidate in "$HOME/go/bin" "/c/Program Files/Docker/Docker/resources/bin"; do
  if [ -d "$candidate" ] && [[ ":$PATH:" != *":$candidate:"* ]]; then
    PATH="$candidate:$PATH"
  fi
done

git_sha="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo dev)"
git_tag="$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || echo "$TAG")"
build_time="$(git -C "$ROOT_DIR" log -1 --format=%cI 2>/dev/null || date -u +"%Y-%m-%dT%H:%M:%SZ")"

log() { printf '==> %s\n' "$*"; }

cd "$ROOT_DIR"

for service in api redirector migrate; do
  log "Building ${service}:${TAG}"
  DOCKER_BUILDKIT=1 docker build \
    --build-arg SERVICE="$service" \
    --build-arg VERSION="$git_tag" \
    --build-arg COMMIT="$git_sha" \
    --build-arg BUILD_TIME="$build_time" \
    -t "$REGISTRY/urlshortener/$service:$TAG" \
    -t "$REGISTRY/urlshortener/$service:latest" \
    .
done
