package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appurl "github.com/urlshortener/platform/internal/application/url"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
)

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockURLGetter struct {
	result *appurl.URLResult
	err    error
}

func (m *mockURLGetter) Handle(_ context.Context, _ appurl.GetQuery) (*appurl.URLResult, error) {
	return m.result, m.err
}

type mockURLLister struct {
	result *appurl.ListResult
	err    error
}

func (m *mockURLLister) Handle(_ context.Context, _ appurl.ListQuery) (*appurl.ListResult, error) {
	return m.result, m.err
}

type mockURLUpdater struct {
	result      *appurl.URLResult
	err         error
	capturedCmd appurl.UpdateCommand
}

func (m *mockURLUpdater) Handle(_ context.Context, cmd appurl.UpdateCommand) (*appurl.URLResult, error) {
	m.capturedCmd = cmd
	return m.result, m.err
}

type mockURLDeleter struct {
	err         error
	capturedCmd appurl.DeleteCommand
}

func (m *mockURLDeleter) Handle(_ context.Context, cmd appurl.DeleteCommand) error {
	m.capturedCmd = cmd
	return m.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newURLHandler(
	getter handler.URLGetter,
	lister handler.URLLister,
	updater handler.URLUpdater,
	deleter handler.URLDeleter,
) *handler.URLHandler {
	return handler.NewURLHandler(getter, lister, updater, deleter, testLog)
}

func withURLID(r *http.Request, urlID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceID", "ws_001")
	rctx.URLParams.Add("urlID", urlID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func withURLClaims(r *http.Request) *http.Request {
	claims := &domainauth.Claims{
		UserID:      "usr_001",
		WorkspaceID: "ws_001",
		Scope:       "read write",
	}
	return r.WithContext(domainauth.WithContext(r.Context(), claims))
}

func sampleURLResult(id, shortCode string) *appurl.URLResult {
	return &appurl.URLResult{
		ID:          id,
		ShortURL:    "https://s.example.com/" + shortCode,
		ShortCode:   shortCode,
		OriginalURL: "https://example.com/long",
		Title:       "Test",
		Status:      "active",
		WorkspaceID: "ws_001",
		CreatedBy:   "usr_001",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		ClickCount:  42,
	}
}

// ── Get tests ─────────────────────────────────────────────────────────────────

func TestURLHandler_Get_Success(t *testing.T) {
	mock := &mockURLGetter{result: sampleURLResult("url_001", "abc1234")}
	h := newURLHandler(mock, nil, nil, nil)

	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodGet, "/urls/url_001", nil),
		"url_001",
	))

	h.Get(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data handler.URLResponse `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&env)
	if env.Data.ID != "url_001" {
		t.Errorf("expected ID=url_001, got %q", env.Data.ID)
	}
	if env.Data.ShortCode != "abc1234" {
		t.Errorf("expected ShortCode=abc1234, got %q", env.Data.ShortCode)
	}
	if env.Data.ClickCount != 42 {
		t.Errorf("expected ClickCount=42, got %d", env.Data.ClickCount)
	}
}

func TestURLHandler_Get_NotFound_Returns404(t *testing.T) {
	mock := &mockURLGetter{err: apperrors.ErrNotFound}
	h := newURLHandler(mock, nil, nil, nil)

	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodGet, "/urls/ghost", nil),
		"ghost",
	))

	h.Get(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestURLHandler_Get_NoClaims_Returns401(t *testing.T) {
	h := newURLHandler(&mockURLGetter{}, nil, nil, nil)

	w := httptest.NewRecorder()
	r := withURLID(httptest.NewRequest(http.MethodGet, "/urls/url_001", nil), "url_001")
	// No claims

	h.Get(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ── List tests ────────────────────────────────────────────────────────────────

func TestURLHandler_List_Success(t *testing.T) {
	mock := &mockURLLister{
		result: &appurl.ListResult{
			URLs: []*appurl.URLResult{
				sampleURLResult("url_001", "aaa1111"),
				sampleURLResult("url_002", "bbb2222"),
			},
			NextCursor: "",
			HasMore:    false,
		},
	}
	h := newURLHandler(nil, mock, nil, nil)

	w := httptest.NewRecorder()
	r := withURLClaims(withWorkspaceID(
		httptest.NewRequest(http.MethodGet, "/urls", nil),
		"ws_001",
	))

	h.List(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var resp handler.ListURLsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Data) != 2 {
		t.Errorf("expected 2 URLs in response, got %d", len(resp.Data))
	}
	if resp.Meta.HasMore {
		t.Error("expected HasMore=false")
	}
}

func TestURLHandler_List_WithPagination_ReturnsNextCursor(t *testing.T) {
	mock := &mockURLLister{
		result: &appurl.ListResult{
			URLs:       []*appurl.URLResult{sampleURLResult("url_001", "aaa1111")},
			NextCursor: "01HXYZ...",
			HasMore:    true,
		},
	}
	h := newURLHandler(nil, mock, nil, nil)

	w := httptest.NewRecorder()
	r := withURLClaims(withWorkspaceID(
		httptest.NewRequest(http.MethodGet, "/urls?limit=1", nil),
		"ws_001",
	))

	h.List(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp handler.ListURLsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Meta.HasMore {
		t.Error("expected HasMore=true")
	}
	if resp.Meta.Cursor == "" {
		t.Error("expected non-empty NextCursor")
	}
}

func TestURLHandler_List_DBError_Returns500(t *testing.T) {
	mock := &mockURLLister{err: errors.New("db: down")}
	h := newURLHandler(nil, mock, nil, nil)

	w := httptest.NewRecorder()
	r := withURLClaims(withWorkspaceID(
		httptest.NewRequest(http.MethodGet, "/urls", nil),
		"ws_001",
	))

	h.List(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// ── Update tests ──────────────────────────────────────────────────────────────

func TestURLHandler_Update_Success(t *testing.T) {
	result := sampleURLResult("url_001", "abc1234")
	result.OriginalURL = "https://updated.example.com"
	mock := &mockURLUpdater{result: result}
	h := newURLHandler(nil, nil, mock, nil)

	body, _ := json.Marshal(map[string]string{
		"original_url": "https://updated.example.com",
	})
	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodPatch, "/urls/url_001", bytes.NewReader(body)),
		"url_001",
	))
	r.Header.Set("Content-Type", "application/json")

	h.Update(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data handler.URLResponse `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&env)
	if env.Data.OriginalURL != "https://updated.example.com" {
		t.Errorf("expected updated URL in response, got %q", env.Data.OriginalURL)
	}
}

func TestURLHandler_Update_ValidationError_Returns422(t *testing.T) {
	mock := &mockURLUpdater{
		err: apperrors.NewValidationError("original_url is not valid", nil),
	}
	h := newURLHandler(nil, nil, mock, nil)

	body, _ := json.Marshal(map[string]string{"original_url": "not-a-url"})
	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodPatch, "/urls/url_001", bytes.NewReader(body)),
		"url_001",
	))

	h.Update(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}
}

func TestURLHandler_Update_NotFound_Returns404(t *testing.T) {
	mock := &mockURLUpdater{err: apperrors.ErrNotFound}
	h := newURLHandler(nil, nil, mock, nil)

	body, _ := json.Marshal(map[string]string{"title": "new"})
	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodPatch, "/urls/ghost", bytes.NewReader(body)),
		"ghost",
	))

	h.Update(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestURLHandler_Update_URLIDPassedToUseCase(t *testing.T) {
	result := sampleURLResult("url_target", "xyz")
	mock := &mockURLUpdater{result: result}
	h := newURLHandler(nil, nil, mock, nil)

	body, _ := json.Marshal(map[string]string{"title": "Updated"})
	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodPatch, "/urls/url_target", bytes.NewReader(body)),
		"url_target",
	))

	h.Update(w, r)

	if mock.capturedCmd.URLID != "url_target" {
		t.Errorf("expected URLID=url_target in use case, got %q", mock.capturedCmd.URLID)
	}
	if mock.capturedCmd.WorkspaceID != "ws_001" {
		t.Errorf("expected WorkspaceID=ws_001 from claims, got %q", mock.capturedCmd.WorkspaceID)
	}
}

// ── Delete tests ──────────────────────────────────────────────────────────────

func TestURLHandler_Delete_Success_Returns204(t *testing.T) {
	mock := &mockURLDeleter{}
	h := newURLHandler(nil, nil, nil, mock)

	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodDelete, "/urls/url_001", nil),
		"url_001",
	))

	h.Delete(w, r)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d — body: %s", w.Code, w.Body.String())
	}
}

func TestURLHandler_Delete_NotFound_Returns404(t *testing.T) {
	mock := &mockURLDeleter{err: apperrors.ErrNotFound}
	h := newURLHandler(nil, nil, nil, mock)

	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodDelete, "/urls/ghost", nil),
		"ghost",
	))

	h.Delete(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestURLHandler_Delete_NoClaims_Returns401(t *testing.T) {
	h := newURLHandler(nil, nil, nil, &mockURLDeleter{})

	w := httptest.NewRecorder()
	r := withURLID(
		httptest.NewRequest(http.MethodDelete, "/urls/url_001", nil),
		"url_001",
	)
	// No claims

	h.Delete(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestURLHandler_Delete_InfraError_Returns500(t *testing.T) {
	mock := &mockURLDeleter{err: errors.New("db: down")}
	h := newURLHandler(nil, nil, nil, mock)

	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodDelete, "/urls/url_001", nil),
		"url_001",
	))

	h.Delete(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestURLHandler_Delete_URLIDAndWorkspacePassedToUseCase(t *testing.T) {
	mock := &mockURLDeleter{}
	h := newURLHandler(nil, nil, nil, mock)

	w := httptest.NewRecorder()
	r := withURLClaims(withURLID(
		httptest.NewRequest(http.MethodDelete, "/urls/url_target", nil),
		"url_target",
	))

	h.Delete(w, r)

	if mock.capturedCmd.URLID != "url_target" {
		t.Errorf("expected URLID=url_target, got %q", mock.capturedCmd.URLID)
	}
	if mock.capturedCmd.WorkspaceID != "ws_001" {
		t.Errorf("expected WorkspaceID=ws_001 from claims, got %q", mock.capturedCmd.WorkspaceID)
	}
}
