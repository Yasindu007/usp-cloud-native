// Package jwtutil provides RSA key management helpers used by both the
// mock issuer (for signing) and the auth middleware (for verification).
//
// Key format: PKCS#8 PEM (standard OpenSSL output for RSA keys).
// Signing algorithm: RS256 (RSA + SHA-256) per PRD section 10.1.
//
// Why RS256 over HS256?
//
//	HS256 uses a single shared secret for both signing and verification.
//	Any service that can verify tokens can also forge them — a compromise
//	of the API service means an attacker can issue arbitrary tokens.
//	RS256 uses a private/public key pair: only the issuer (mock issuer /
//	WSO2) can sign tokens. The API service only holds the public key and
//	can verify but never forge tokens. This is the PRD requirement.
//
// Why RS256 over ES256?
//
//	ES256 (ECDSA P-256) is faster and produces smaller tokens.
//	We use RS256 because WSO2 API Manager's default JWT profile uses RS256,
//	simplifying Phase 4 integration. The PRD permits either.
package jwtutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/lestrrat-go/jwx/v2/jwk"
)

// LoadPrivateKey reads an RSA private key from a PEM file.
// Used by the mock issuer to sign tokens.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jwtutil: reading private key file %q: %w", path, err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("jwtutil: no PEM block found in %q", path)
	}

	// Support both PKCS#1 (RSAPrivateKey) and PKCS#8 (PrivateKey) formats.
	// openssl genrsa produces PKCS#1; openssl genpkey produces PKCS#8.
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("jwtutil: parsing PKCS#1 private key: %w", err)
		}
		return key, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("jwtutil: parsing PKCS#8 private key: %w", err)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("jwtutil: private key is not RSA (got %T)", key)
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("jwtutil: unsupported PEM block type %q", block.Type)
	}
}

// LoadPublicKeyAsJWKSet reads an RSA public key from a PEM file and returns
// it as a JWK Set. The auth middleware uses a JWK Set for token verification
// because jwx's jwt.Parse expects one — this keeps the verification code
// consistent whether the key comes from a local file or a remote JWKS endpoint.
func LoadPublicKeyAsJWKSet(path string) (jwk.Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jwtutil: reading public key file %q: %w", path, err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("jwtutil: no PEM block found in %q", path)
	}

	var source any = data
	if block.Type == "CERTIFICATE" {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("jwtutil: parsing certificate from %q: %w", path, err)
		}
		source = cert.PublicKey
	}

	var key jwk.Key
	if block.Type == "CERTIFICATE" {
		key, err = jwk.FromRaw(source)
	} else {
		// jwk.ParseKey handles PEM-encoded RSA public keys directly.
		key, err = jwk.ParseKey(data, jwk.WithPEM(true))
	}
	if err != nil {
		return nil, fmt.Errorf("jwtutil: parsing public key from %q: %w", path, err)
	}

	// Wrap the single key in a JWK Set.
	// A JWK Set supports multiple keys (key rotation), which is the
	// production pattern even when only one key is currently active.
	set := jwk.NewSet()
	if err := set.AddKey(key); err != nil {
		return nil, fmt.Errorf("jwtutil: adding key to JWK set: %w", err)
	}

	return set, nil
}

// GenerateKeyPair generates a new RSA-2048 key pair in memory.
// Used by the mock issuer when no key file exists on disk.
// In production, keys are pre-generated and stored in Vault.
func GenerateKeyPair() (*rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("jwtutil: generating RSA key pair: %w", err)
	}
	return key, nil
}

// PrivateKeyToPEM serializes an RSA private key to PKCS#1 PEM format.
func PrivateKeyToPEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// PublicKeyToPEM serializes an RSA public key to PKIX PEM format.
func PublicKeyToPEM(key *rsa.PrivateKey) ([]byte, error) {
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("jwtutil: marshaling public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	}), nil
}
