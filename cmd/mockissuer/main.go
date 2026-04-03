// Command mockissuer is a local development OAuth2 token issuer.
// It issues RS256-signed JWTs for use with the URL Shortener API.
//
// This is NOT a production OAuth2 server. It exists solely to enable
// local development and integration testing without an external
// identity provider. It is replaced by WSO2 API Manager in Phase 4.
//
// Endpoints:
//
//	POST /token           — Issue a JWT access token
//	GET  /.well-known/jwks.json — Expose the public key as a JWKS
//	GET  /healthz         — Health check
//
// Usage:
//
//	go run ./cmd/mockissuer
//
// Environment variables:
//
//	MOCK_ISSUER_PORT         Port to listen on (default: 9000)
//	MOCK_ISSUER_PRIVATE_KEY  Path to RSA private key PEM (default: ./certs/jwt_private.pem)
//	JWT_ISSUER               Issuer claim value (default: http://localhost:9000)
//	JWT_AUDIENCE             Audience claim value (default: url-shortener-api)
//
// Token request (application/x-www-form-urlencoded):
//
//	grant_type=client_credentials
//	client_id=<any>
//	client_secret=<any>
//	workspace_id=ws_myworkspace    (custom — maps to workspace_id claim)
//	user_id=usr_myuser             (custom — maps to sub claim)
//	scope=read write               (space-separated)
package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/oklog/ulid/v2"

	"github.com/urlshortener/platform/pkg/jwtutil"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	port := getEnv("MOCK_ISSUER_PORT", "9000")
	privateKeyPath := getEnv("MOCK_ISSUER_PRIVATE_KEY", "./certs/jwt_private.pem")
	issuer := getEnv("JWT_ISSUER", "http://localhost:9000")
	audience := getEnv("JWT_AUDIENCE", "url-shortener-api")
	tokenTTL := 1 * time.Hour

	log.Info("starting mock JWT issuer",
		slog.String("port", port),
		slog.String("issuer", issuer),
		slog.String("audience", audience),
	)

	// ── Load or generate RSA key pair ─────────────────────────────────────────
	var privateKey *rsa.PrivateKey
	var err error

	if _, statErr := os.Stat(privateKeyPath); statErr == nil {
		// Key file exists — load it.
		privateKey, err = jwtutil.LoadPrivateKey(privateKeyPath)
		if err != nil {
			log.Error("failed to load private key",
				slog.String("path", privateKeyPath),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}
		log.Info("loaded private key from file", slog.String("path", privateKeyPath))
	} else {
		// Key file not found — generate an ephemeral key pair.
		// Tokens issued with this key are only valid for the current process lifetime.
		// Run scripts/gen-jwt-keys.sh for persistent keys.
		log.Warn("private key file not found, generating ephemeral key pair",
			slog.String("path", privateKeyPath),
			slog.String("hint", "run: bash scripts/gen-jwt-keys.sh for persistent keys"),
		)
		privateKey, err = jwtutil.GenerateKeyPair()
		if err != nil {
			log.Error("failed to generate key pair", slog.String("error", err.Error()))
			os.Exit(1)
		}

		// Save the public key so the API service can verify tokens.
		pubPEM, err := jwtutil.PublicKeyToPEM(privateKey)
		if err != nil {
			log.Error("failed to serialize public key", slog.String("error", err.Error()))
			os.Exit(1)
		}
		if err := os.MkdirAll("./certs", 0755); err == nil {
			if writeErr := os.WriteFile("./certs/jwt_public.pem", pubPEM, 0644); writeErr == nil {
				log.Info("wrote ephemeral public key to ./certs/jwt_public.pem")
			}
		}
	}

	// Build JWK Set for the JWKS endpoint.
	// The API service (and WSO2 in Phase 4) fetches this to get
	// the public key for verifying tokens.
	jwkKey, err := jwk.FromRaw(privateKey.Public())
	if err != nil {
		log.Error("failed to build JWK from public key", slog.String("error", err.Error()))
		os.Exit(1)
	}
	// Set key ID so clients can do key rotation via kid lookup.
	_ = jwkKey.Set(jwk.KeyIDKey, "mock-key-1")

	publicKeySet := jwk.NewSet()
	_ = publicKeySet.AddKey(jwkKey)

	// ── HTTP Routes ───────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// POST /token — issue a JWT
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}

		grantType := r.FormValue("grant_type")
		if grantType != "client_credentials" {
			writeOAuthError(w, "unsupported_grant_type",
				"Only client_credentials grant is supported")
			return
		}

		// In a real OAuth2 server, client_id and client_secret would be
		// validated against a registry. For local dev, any non-empty values pass.
		clientID := r.FormValue("client_id")
		if clientID == "" {
			writeOAuthError(w, "invalid_client", "client_id is required")
			return
		}

		// Extract custom parameters for workspace and user identity.
		workspaceID := r.FormValue("workspace_id")
		if workspaceID == "" {
			workspaceID = "ws_default"
		}
		userID := r.FormValue("user_id")
		if userID == "" {
			userID = "usr_default"
		}
		scope := r.FormValue("scope")
		if scope == "" {
			scope = "read write"
		}

		now := time.Now().UTC()
		jti := ulid.Make().String() // Unique token ID for deny list

		// Build the JWT with standard and custom claims.
		tok, err := jwt.NewBuilder().
			Issuer(issuer).
			Audience([]string{audience}).
			Subject(userID).
			JwtID(jti).
			IssuedAt(now).
			Expiration(now.Add(tokenTTL)).
			Claim("workspace_id", workspaceID).
			Claim("scope", scope).
			Claim("client_id", clientID).
			Build()
		if err != nil {
			log.Error("failed to build token", slog.String("error", err.Error()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		// Sign with RS256 using the private key.
		signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, privateKey))
		if err != nil {
			log.Error("failed to sign token", slog.String("error", err.Error()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		log.Info("token issued",
			slog.String("sub", userID),
			slog.String("workspace_id", workspaceID),
			slog.String("scope", scope),
			slog.String("jti", jti),
			slog.Duration("ttl", tokenTTL),
		)

		// RFC 6749 token response format.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": string(signed),
			"token_type":   "Bearer",
			"expires_in":   int(tokenTTL.Seconds()),
			"scope":        scope,
		})
	})

	// GET /.well-known/jwks.json — expose public key
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600") // Cache JWKS for 1h
		_ = json.NewEncoder(w).Encode(publicKeySet)
	})

	// GET /healthz
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"alive","service":"mock-issuer"}`))
	})

	// ── HTTP Server ───────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("mock issuer listening",
			slog.String("addr", srv.Addr),
			slog.String("token_endpoint", fmt.Sprintf("http://localhost:%s/token", port)),
			slog.String("jwks_endpoint", fmt.Sprintf("http://localhost:%s/.well-known/jwks.json", port)),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal", slog.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Info("mock issuer stopped")
}

// writeOAuthError writes an RFC 6749 error response.
func writeOAuthError(w http.ResponseWriter, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
