#!/usr/bin/env bash
# Requests a JWT access token from the local mock issuer.
# Use this token in API requests during local development.
#
# Usage:
#   bash scripts/get-token.sh
#   bash scripts/get-token.sh ws_myworkspace usr_myuser read,write

set -euo pipefail

ISSUER_URL="${MOCK_ISSUER_URL:-http://localhost:9000}"
WORKSPACE_ID="${1:-ws_default}"
USER_ID="${2:-usr_default}"
SCOPE="${3:-read write}"

echo "==> Requesting token from $ISSUER_URL"
echo "    workspace_id: $WORKSPACE_ID"
echo "    user_id:      $USER_ID"
echo "    scope:        $SCOPE"
echo ""

RESPONSE=$(curl -s -X POST "$ISSUER_URL/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=dev-client" \
  -d "client_secret=dev-secret" \
  -d "workspace_id=$WORKSPACE_ID" \
  -d "user_id=$USER_ID" \
  -d "scope=$SCOPE")

TOKEN=$(echo "$RESPONSE" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)

if [ -z "$TOKEN" ]; then
  echo "❌ Failed to get token. Response:"
  echo "$RESPONSE"
  exit 1
fi

echo "✅ Token:"
echo "$TOKEN"
echo ""
echo "Use with curl:"
echo "  curl -H \"Authorization: Bearer $TOKEN\" \\"
echo "       -H \"Content-Type: application/json\" \\"
echo "       http://localhost:8080/api/v1/urls"