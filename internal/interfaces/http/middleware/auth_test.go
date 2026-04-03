package middleware_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"

	"log/slog"
	"os"
)

// ── Test fixtures ─────────────────────────────────────────────────────────────

// testKeyPair generates a fresh RSA-2048 key pair for each test run.
// Using a fresh pair per test ensures tests are hermetically isolated
// and don't depend on shared key files on disk.
type testKeyPair struct {
	private *rsa.PrivateKey
	keySet  jwk.Set
	keyID   string
}

func newTestKeyPair(t *testing.T) *testKeyPair {
	t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	// Build JWK Set from the public key for the middleware.
	pubKey, err := jwk.FromRaw(private.Public())
	if err != nil {
		t.Fatalf("failed to build JWK from public key: %v", err)
	}
	keyID := "test-key"
	if err := pubKey.Set(jwk.KeyIDKey, keyID); err != nil {
		t.Fatalf("failed to set JWK key ID: %v", err)
	}
	if err := pubKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("failed to set JWK algorithm: %v", err)
	}
	if err := pubKey.Set(jwk.KeyUsageKey, "sig"); err != nil {
		t.Fatalf("failed to set JWK key usage: %v", err)
	}

	set := jwk.NewSet()
	if err := set.AddKey(pubKey); err != nil {
		t.Fatalf("failed to add key to set: %v", err)
	}

	return &testKeyPair{private: private, keySet: set, keyID: keyID}
}

// issueToken builds and signs a JWT with the test key pair.
// Callers can override defaults via the opts function.
func (kp *testKeyPair) issueToken(t *testing.T, opts ...func(*jwt.Builder)) string {
	t.Helper()

	b := jwt.NewBuilder().
		Issuer("http://localhost:9000").
		Audience([]string{"url-shortener-api"}).
		Subject("usr_test001").
		JwtID("jti-test-"+t.Name()).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(1*time.Hour)).
		Claim("workspace_id", "ws_test001").
		Claim("scope", "read write")

	for _, opt := range opts {
		opt(b)
	}

	tok, err := b.Build()
	if err != nil {
		t.Fatalf("failed to build token: %v", err)
	}

	hdrs := jws.NewHeaders()
	if err := hdrs.Set(jws.KeyIDKey, kp.keyID); err != nil {
		t.Fatalf("failed to set protected headers: %v", err)
	}

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, kp.private, jws.WithProtectedHeaders(hdrs)))
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	return string(signed)
}

var authTestLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

// buildAuthConfig creates a test AuthConfig with the given key pair.
func buildAuthConfig(kp *testKeyPair, denyList httpmiddleware.DenyListChecker) httpmiddleware.AuthConfig {
	return httpmiddleware.AuthConfig{
		Issuer:   "http://localhost:9000",
		Audience: "url-shortener-api",
		KeySet:   kp.keySet,
		DenyList: denyList,
		Log:      authTestLog,
	}
}

// nextHandler is a sentinel handler that marks itself as called.
// Used to verify that the middleware calls (or skips) the downstream handler.
func nextHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

// fakeDenyList is an in-memory deny list for tests.
type fakeDenyList struct {
	revoked map[string]bool
	err     error // force error
}

func newFakeDenyList() *fakeDenyList {
	return &fakeDenyList{revoked: make(map[string]bool)}
}

func (f *fakeDenyList) IsRevoked(_ context.Context, jti string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[jti], nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestAuthenticate_ValidToken_CallsNext(t *testing.T) {
	kp := newTestKeyPair(t)
	cfg := buildAuthConfig(kp, newFakeDenyList())

	called := false
	mw := httpmiddleware.Authenticate(cfg)
	handler := mw(nextHandler(&called))

	token := kp.issueToken(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler to be called with valid token")
	}
}

func TestAuthenticate_ValidToken_ClaimsInContext(t *testing.T) {
	kp := newTestKeyPair(t)
	cfg := buildAuthConfig(kp, newFakeDenyList())

	var capturedClaims *domainauth.Claims
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := domainauth.FromContext(r.Context())
		if !ok {
			t.Error("expected claims in context, got none")
			return
		}
		capturedClaims = claims
		w.WriteHeader(http.StatusOK)
	})

	mw := httpmiddleware.Authenticate(cfg)
	token := kp.issueToken(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(captureHandler).ServeHTTP(w, r)

	if capturedClaims == nil {
		t.Fatal("claims were nil after successful auth")
	}
	if capturedClaims.UserID != "usr_test001" {
		t.Errorf("expected UserID=usr_test001, got %q", capturedClaims.UserID)
	}
	if capturedClaims.WorkspaceID != "ws_test001" {
		t.Errorf("expected WorkspaceID=ws_test001, got %q", capturedClaims.WorkspaceID)
	}
	if !capturedClaims.HasScope("write") {
		t.Error("expected HasScope(write)=true")
	}
	if capturedClaims.TokenID == "" {
		t.Error("expected non-empty TokenID (jti)")
	}
}

func TestAuthenticate_MissingAuthHeader_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	called := false
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	// No Authorization header

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if called {
		t.Error("next handler must NOT be called with missing Authorization header")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("expected problem+json content type, got %q", ct)
	}
}

func TestAuthenticate_NonBearerScheme_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // Basic auth

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for non-Bearer scheme, got %d", w.Code)
	}
}

func TestAuthenticate_ExpiredToken_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil))

	token := kp.issueToken(t, func(b *jwt.Builder) {
		b.Expiration(time.Now().Add(-1 * time.Hour)) // expired 1 hour ago
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", w.Code)
	}
}

func TestAuthenticate_WrongIssuer_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil))

	token := kp.issueToken(t, func(b *jwt.Builder) {
		b.Issuer("http://evil-issuer.example.com")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong issuer, got %d", w.Code)
	}
}

func TestAuthenticate_WrongAudience_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil))

	token := kp.issueToken(t, func(b *jwt.Builder) {
		b.Audience([]string{"some-other-service"})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong audience, got %d", w.Code)
	}
}

func TestAuthenticate_BadSignature_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	// Use a DIFFERENT key pair to sign the token — signature won't verify
	wrongKP := newTestKeyPair(t)
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil)) // verifies with kp

	// Token signed with wrongKP — verification with kp will fail
	token := wrongKP.issueToken(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad signature, got %d", w.Code)
	}
}

func TestAuthenticate_MissingWorkspaceIDClaim_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, nil))

	token := kp.issueToken(t, func(b *jwt.Builder) {
		// Omit workspace_id custom claim
		b.Claim("workspace_id", "")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing workspace_id, got %d", w.Code)
	}
}

func TestAuthenticate_RevokedToken_Returns401(t *testing.T) {
	kp := newTestKeyPair(t)
	dl := newFakeDenyList()
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, dl))

	token := kp.issueToken(t)

	// Pre-revoke the JTI
	jti := "jti-test-" + t.Name()
	dl.revoked[jti] = true

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for revoked token, got %d", w.Code)
	}
}

func TestAuthenticate_DenyListError_FailsOpen(t *testing.T) {
	kp := newTestKeyPair(t)
	dl := newFakeDenyList()
	dl.err = errors.New("redis: connection refused") // simulate Redis down
	mw := httpmiddleware.Authenticate(buildAuthConfig(kp, dl))

	token := kp.issueToken(t)
	called := false

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	// Must NOT reject request when deny list check fails (fail-open)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (fail-open) when deny list errors, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler to be called when deny list is unavailable")
	}
}

func TestAuthenticate_NilDenyList_SkipsCheck(t *testing.T) {
	kp := newTestKeyPair(t)
	// nil DenyList = skip revocation check entirely
	cfg := buildAuthConfig(kp, nil)
	cfg.DenyList = nil

	called := false
	mw := httpmiddleware.Authenticate(cfg)
	token := kp.issueToken(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with nil deny list, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler called with nil deny list")
	}
}

// TestRequireScope verifies scope enforcement middleware.
func TestRequireScope_HasScope_CallsNext(t *testing.T) {
	called := false
	mw := httpmiddleware.RequireScope("write")

	// Inject claims with "write" scope directly into context
	claims := &domainauth.Claims{
		UserID:      "usr_001",
		WorkspaceID: "ws_001",
		Scope:       "read write",
	}
	ctx := domainauth.WithContext(context.Background(), claims)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil).WithContext(ctx)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected next handler called when scope matches")
	}
}

func TestRequireScope_MissingScope_Returns403(t *testing.T) {
	mw := httpmiddleware.RequireScope("admin")

	// Claims with only "read" scope
	claims := &domainauth.Claims{
		UserID:      "usr_001",
		WorkspaceID: "ws_001",
		Scope:       "read",
	}
	ctx := domainauth.WithContext(context.Background(), claims)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/urls/123", nil).WithContext(ctx)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for insufficient scope, got %d", w.Code)
	}
}

// Missing import — add to file
var errors = struct{ New func(string) error }{
	New: func(s string) error {
		return &strErr{s}
	},
}

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }
