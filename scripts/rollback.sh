#!/usr/bin/env bash
set -euo pipefail

# Roll back both runtime deployments. Optional positional args target exact
# revisions captured before a deploy; without them Kubernetes rolls back one step.
API_REVISION="${1:-${API_REVISION:-}}"
REDIRECTOR_REVISION="${2:-${REDIRECTOR_REVISION:-}}"
NAMESPACE="${NAMESPACE:-urlshortener}"
TIMEOUT="${ROLLOUT_TIMEOUT:-5m}"

log() { printf '==> %s\n' "$*"; }

rollback_deployment() {
  local deployment="$1"
  local revision="$2"

  if [ -n "$revision" ] && [ "$revision" != "0" ]; then
    log "Rolling back ${deployment} to revision ${revision}"
    kubectl rollout undo "deployment/${deployment}" -n "$NAMESPACE" --to-revision="$revision"
  else
    log "Rolling back ${deployment} to the previous revision"
    kubectl rollout undo "deployment/${deployment}" -n "$NAMESPACE"
  fi

  kubectl rollout status "deployment/${deployment}" -n "$NAMESPACE" --timeout="$TIMEOUT"
}

rollback_deployment api "$API_REVISION"
rollback_deployment redirector "$REDIRECTOR_REVISION"

log "Rollback complete"
