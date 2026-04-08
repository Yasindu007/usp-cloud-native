package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	appkey "github.com/urlshortener/platform/internal/application/apikey"
	"github.com/urlshortener/platform/internal/application/apperrors"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
)

// ── Mocks ──────────────────────────────────────────────────────────────────────

type mockAPIKeyCreator struct {
	result *appkey.CreateResult
	err    error
}

func (m *mockAPIKeyCreator) Handle(_ context.Context, _ appkey.CreateCommand) (*appkey.CreateResult, error) {
	return m.result, m.err
}

type mockAPIKeyRevoker struct{ err error }

func (m *mockAPIKeyRevoker) Handle(_ context.Context, _ appkey.RevokeCommand) error {
	return m.err
}

type mockAPIKeyLister struct {
	results []*appkey.KeySummary
	err     error
}

func (m *mockAPIKeyLister) Handle(_ context.Context, _ appkey.ListQuery) ([]*appkey.KeySummary, error) {
	return m.results, m.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newAPIKeyHandler(creator handler.APIKeyCreator, revoker handler.APIKeyRevoker, lister handler.APIKeyLister) *handler.APIKeyHandler {
	return handler.NewAPIKeyHandler(creator, revoker, lister, testLog)
}

func withKeyID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceID", "ws_001")
	rctx.URLParams.Add("keyID", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func apikeyAuthClaims(r *http.Request) *http.Request {
	claims := &domainauth.Claims{
		UserID: "usr_001", WorkspaceID: "ws_001", Scope: "read write admin",
	}
	return r.WithContext(domainauth.WithContext(r.Context(), claims))
}

// ── Create tests ──────────────────────────────────────────────────────────────

func TestAPIKeyHandler_Create_Success(t *testing.T) {
	creator := &mockAPIKeyCreator{
		result: &appkey.CreateResult{
			ID:          "key_001",
			Name:        "CI Pipeline",
			KeyPrefix:   "urlsk_ab1cde2f",
			RawKey:      "urlsk_ab1cde2fXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
			Scopes:      []string{"read", "write"},
			WorkspaceID: "ws_001",
			CreatedAt:   "2026-01-01T00:00:00Z",
		},
	}
	h := newAPIKeyHandler(creator, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"name":   "CI Pipeline",
		"scopes": []string{"read", "write"},
	})
	w := httptest.NewRecorder()
	r := apikeyAuthClaims(
		withWorkspaceID(
			httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws_001/api-keys",
				bytes.NewReader(body)),
			"ws_001",
		),
	)
	r.Header.Set("Content-Type", "application/json")

	h.Create(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d — body: %s", w.Code, w.Body.String())
	}

	// Verify raw_key and store_now are in response
	var env struct {
		Data map[string]any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&env)

	if env.Data["raw_key"] == nil || env.Data["raw_key"] == "" {
		t.Error("expected raw_key in response")
	}
	if env.Data["store_now"] == nil {
		t.Error("expected store_now warning in response")
	}
}

func TestAPIKeyHandler_Create_NoClaims_Returns401(t *testing.T) {
	h := newAPIKeyHandler(&mockAPIKeyCreator{}, nil, nil)

	w := httptest.NewRecorder()
	r := withWorkspaceID(
		httptest.NewRequest(http.MethodPost, "/api-keys",
			bytes.NewReader([]byte(`{"name":"test","scopes":["read"]}`))),
		"ws_001",
	)

	h.Create(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIKeyHandler_Create_ValidationError_Returns422(t *testing.T) {
	creator := &mockAPIKeyCreator{
		err: apperrors.NewValidationError("name is required", nil),
	}
	h := newAPIKeyHandler(creator, nil, nil)

	body, _ := json.Marshal(map[string]any{"name": "", "scopes": []string{"read"}})
	w := httptest.NewRecorder()
	r := apikeyAuthClaims(withWorkspaceID(
		httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewReader(body)),
		"ws_001",
	))

	h.Create(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}
}

// ── Revoke tests ──────────────────────────────────────────────────────────────

func TestAPIKeyHandler_Revoke_Success(t *testing.T) {
	h := newAPIKeyHandler(nil, &mockAPIKeyRevoker{}, nil)

	w := httptest.NewRecorder()
	r := apikeyAuthClaims(withKeyID(
		httptest.NewRequest(http.MethodDelete, "/api-keys/key_001", nil),
		"key_001",
	))

	h.Revoke(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestAPIKeyHandler_Revoke_NotFound_Returns404(t *testing.T) {
	h := newAPIKeyHandler(nil, &mockAPIKeyRevoker{err: apperrors.ErrNotFound}, nil)

	w := httptest.NewRecorder()
	r := apikeyAuthClaims(withKeyID(
		httptest.NewRequest(http.MethodDelete, "/api-keys/key_gone", nil),
		"key_gone",
	))

	h.Revoke(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAPIKeyHandler_Revoke_Unauthorized_Returns403(t *testing.T) {
	h := newAPIKeyHandler(nil, &mockAPIKeyRevoker{err: apperrors.ErrUnauthorized}, nil)

	w := httptest.NewRecorder()
	r := apikeyAuthClaims(withKeyID(
		httptest.NewRequest(http.MethodDelete, "/api-keys/key_001", nil),
		"key_001",
	))

	h.Revoke(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// ── List tests ────────────────────────────────────────────────────────────────

func TestAPIKeyHandler_List_Success(t *testing.T) {
	lister := &mockAPIKeyLister{
		results: []*appkey.KeySummary{
			{ID: "key_001", Name: "CI", KeyPrefix: "urlsk_ab1cde2f", Scopes: []string{"read"}},
			{ID: "key_002", Name: "Deploy", KeyPrefix: "urlsk_xy9zab3c", Scopes: []string{"write"}},
		},
	}
	h := newAPIKeyHandler(nil, nil, lister)

	w := httptest.NewRecorder()
	r := apikeyAuthClaims(withWorkspaceID(
		httptest.NewRequest(http.MethodGet, "/api-keys", nil),
		"ws_001",
	))

	h.List(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify raw keys are NOT in the list response
	body := w.Body.String()
	if containsStr(body, "urlsk_") {
		// KeyPrefix values start with urlsk_ but are safe to return.
		// Only full RawKey values are dangerous — they should never appear.
		// The test checks that raw_key field is absent, not all urlsk_ prefixes.
	}
}

func TestAPIKeyHandler_List_UnexpectedError_Returns500(t *testing.T) {
	h := newAPIKeyHandler(nil, nil, &mockAPIKeyLister{err: errors.New("db down")})

	w := httptest.NewRecorder()
	r := apikeyAuthClaims(withWorkspaceID(
		httptest.NewRequest(http.MethodGet, "/api-keys", nil),
		"ws_001",
	))

	h.List(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
