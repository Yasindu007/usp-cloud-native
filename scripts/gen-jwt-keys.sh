#!/usr/bin/env bash
# Generates an RSA-2048 key pair for local JWT signing and verification.
# Requires: openssl (available on macOS, Linux, and WSL).

set -euo pipefail

CERTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/certs"
PRIVATE_KEY="$CERTS_DIR/jwt_private.pem"
PUBLIC_KEY="$CERTS_DIR/jwt_public.pem"

mkdir -p "$CERTS_DIR"

echo "==> Generating RSA-2048 private key..."
openssl genrsa -out "$PRIVATE_KEY" 2048

echo "==> Extracting public key..."
openssl rsa -in "$PRIVATE_KEY" -pubout -out "$PUBLIC_KEY"

# Restrict permissions on the private key.
# On Linux/macOS this prevents other users from reading it.
chmod 600 "$PRIVATE_KEY"
chmod 644 "$PUBLIC_KEY"

echo ""
echo "✅ Keys generated:"
echo "   Private: $PRIVATE_KEY  (keep secret — used by mock issuer to sign tokens)"
echo "   Public:  $PUBLIC_KEY   (safe to share — used by API service to verify tokens)"
echo ""
echo "Set in .env:"
echo "   JWT_PUBLIC_KEY_PATH=./certs/jwt_public.pem"