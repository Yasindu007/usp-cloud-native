package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
	"github.com/urlshortener/platform/pkg/keyutil"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeAPIKeyLookup struct {
	keys        map[string][]*domainapikey.APIKey // prefix → keys
	lastUsedIDs []string
	forceError  bool
}

func newFakeAPIKeyLookup() *fakeAPIKeyLookup {
	return &fakeAPIKeyLookup{keys: make(map[string][]*domainapikey.APIKey)}
}

// addKey inserts a key with a real bcrypt hash so Verify() works.
func (f *fakeAPIKeyLookup) addKey(rawKey string, wsID string, scopes []string) *domainapikey.APIKey {
	hash, _ := keyutil.Hash(rawKey)
	k := &domainapikey.APIKey{
		ID:          "key_" + rawKey[:8],
		WorkspaceID: wsID,
		Name:        "Test Key",
		KeyHash:     hash,
		KeyPrefix:   domainapikey.ExtractPrefix(rawKey),
		Scopes:      scopes,
		CreatedBy:   "usr_creator",
		CreatedAt:   time.Now(),
	}
	prefix := k.KeyPrefix
	f.keys[prefix] = append(f.keys[prefix], k)
	return k
}

func (f *fakeAPIKeyLookup) GetByPrefix(_ context.Context, prefix string) ([]*domainapikey.APIKey, error) {
	if f.forceError {
		return nil, errFake
	}
	return f.keys[prefix], nil
}

func (f *fakeAPIKeyLookup) UpdateLastUsed(_ context.Context, id string) error {
	f.lastUsedIDs = append(f.lastUsedIDs, id)
	return nil
}

var errFake = &fakeError{"fake error"}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestAPIKeyAuth_ValidKeyInXAPIKeyHeader_Authenticates(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	rawKey, _ := keyutil.GenerateRaw("ws_test")
	lookup.addKey(rawKey, "ws_test", []string{"read", "write"})

	called := false
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", rawKey)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected next handler called for valid API key")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPIKeyAuth_ValidKeyInBearerHeader_Authenticates(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	rawKey, _ := keyutil.GenerateRaw("ws_test")
	lookup.addKey(rawKey, "ws_test", []string{"read"})

	called := false
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer "+rawKey)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected next handler called with Bearer API key")
	}
}

func TestAPIKeyAuth_ValidKey_InjectsClaimsInContext(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	rawKey, _ := keyutil.GenerateRaw("ws_abc123")
	lookup.addKey(rawKey, "ws_abc123", []string{"read", "write"})

	var capturedClaims *domainauth.Claims
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedClaims, _ = domainauth.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", rawKey)

	mw(captureHandler).ServeHTTP(w, r)

	if capturedClaims == nil {
		t.Fatal("expected claims in context after API key auth")
	}
	if capturedClaims.WorkspaceID != "ws_abc123" {
		t.Errorf("expected WorkspaceID=ws_abc123, got %q", capturedClaims.WorkspaceID)
	}
	if !capturedClaims.HasScope("write") {
		t.Error("expected HasScope(write)=true")
	}
}

func TestAPIKeyAuth_NoAPIKey_PassesThrough(t *testing.T) {
	// No API key header → middleware should pass through to next
	// handler without injecting claims or returning 401.
	// This enables the dual-auth pattern (JWT + API key).
	lookup := newFakeAPIKeyLookup()
	called := false
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	// No X-API-Key, no Authorization header

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected pass-through when no API key present")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 pass-through, got %d", w.Code)
	}
}

func TestAPIKeyAuth_JWTInBearerHeader_PassesThrough(t *testing.T) {
	// A Bearer token that does NOT start with "urlsk_" is a JWT.
	// API key middleware should pass through, letting JWT middleware handle it.
	lookup := newFakeAPIKeyLookup()
	called := false
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c3JfMDAxIn0.sig")

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected pass-through for JWT Bearer token")
	}
}

func TestAPIKeyAuth_WrongKey_Returns401(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	rawKey, _ := keyutil.GenerateRaw("ws_test")
	// Add a key with the same prefix region but different hash
	lookup.addKey(rawKey, "ws_test", []string{"read"})

	// Use a different key with the same prefix (prefix collision is
	// astronomically unlikely but we simulate it for test coverage).
	wrongKey := rawKey[:14] + "wrongsuffix_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

	called := false
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", wrongKey)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if called {
		t.Error("next handler must NOT be called with wrong key")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong key, got %d", w.Code)
	}
}

func TestAPIKeyAuth_UnknownPrefix_Returns401(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	// No keys registered at all

	rawKey, _ := keyutil.GenerateRaw("ws_unknown")
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", rawKey)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unknown prefix, got %d", w.Code)
	}
}

func TestAPIKeyAuth_DBError_Returns500(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	lookup.forceError = true

	rawKey, _ := keyutil.GenerateRaw("ws_test")
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", rawKey)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on DB error, got %d", w.Code)
	}
}

func TestAPIKeyAuth_TooShortKey_Returns401(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	mw := httpmiddleware.APIKeyAuth(lookup, authTestLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", "urlsk_short") // too short for valid prefix

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for too-short key, got %d", w.Code)
	}
}

func TestAPIKeyAuth_WithAuthenticateChain_SkipsJWTWhenClaimsAlreadyInjected(t *testing.T) {
	lookup := newFakeAPIKeyLookup()
	rawKey, _ := keyutil.GenerateRaw("ws_test")
	lookup.addKey(rawKey, "ws_test", []string{"read", "write"})

	called := false
	kp := newTestKeyPair(t)
	authCfg := buildAuthConfig(kp, nil)

	chain := httpmiddleware.APIKeyAuth(lookup, authTestLog)(
		httpmiddleware.Authenticate(authCfg)(
			nextHandler(&called),
		),
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)
	r.Header.Set("X-API-Key", rawKey)

	chain.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler to be called when API key auth injects claims")
	}
}
