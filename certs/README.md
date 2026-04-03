# certs/

This directory holds RSA key pairs used for JWT signing and verification
in local development. **Never commit private keys to source control.**

The `.gitignore` excludes `*.pem` and `*.key` files from this directory.

## Generate keys
```bash
bash scripts/gen-jwt-keys.sh
```

This produces:
- `certs/jwt_private.pem` — RSA-2048 private key (mock issuer uses this to sign tokens)
- `certs/jwt_public.pem`  — RSA-2048 public key  (API service uses this to verify tokens)

## Key rotation

In production, key rotation is handled by the identity provider (Keycloak, Auth0).
For local development, re-run `gen-jwt-keys.sh` and restart all services.
Both the mock issuer and the API service must use the same key pair.

## In production (Phase 4)

The public key is served as a JWKS endpoint by WSO2 API Manager.
The API service fetches it on startup and caches it in memory.
Private keys are stored in HashiCorp Vault — never on disk.