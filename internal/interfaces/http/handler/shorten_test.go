package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/shorten"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	"github.com/urlshortener/platform/internal/interfaces/http/response"

	"log/slog"
	"os"
)

// ── Mock ──────────────────────────────────────────────────────────────────────

type mockShortener struct {
	result *shorten.Result
	err    error
	// capturedCmd stores the last Command passed to Handle for assertion.
	capturedCmd shorten.Command
}

func (m *mockShortener) Handle(_ context.Context, cmd shorten.Command) (*shorten.Result, error) {
	m.capturedCmd = cmd
	return m.result, m.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

func successResult(shortCode string) *shorten.Result {
	return &shorten.Result{
		ID:          "01HTEST" + shortCode,
		ShortURL:    "https://s.example.com/" + shortCode,
		ShortCode:   shortCode,
		OriginalURL: "https://example.com/original",
		WorkspaceID: "ws_test",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
}

func shortenRequest(t *testing.T, body map[string]any) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Workspace-ID", "ws_test")
	r.Header.Set("X-User-ID", "usr_test")
	return r
}

func decodeJSON(t *testing.T, body *bytes.Buffer, v any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(v); err != nil {
		t.Fatalf("failed to decode response body: %v\nbody: %s", err, body.String())
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestShortenHandler_Handle_Success(t *testing.T) {
	mock := &mockShortener{result: successResult("abc1234")}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "https://example.com/long/path",
	})

	h.Handle(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d — body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	// Decode and verify response envelope.
	var env struct {
		Data handler.ShortenResponse `json:"data"`
	}
	decodeJSON(t, w.Body, &env)

	if env.Data.ShortCode != "abc1234" {
		t.Errorf("expected ShortCode=abc1234, got %q", env.Data.ShortCode)
	}
	if env.Data.ShortURL != "https://s.example.com/abc1234" {
		t.Errorf("expected ShortURL=https://s.example.com/abc1234, got %q", env.Data.ShortURL)
	}
	if env.Data.OriginalURL != "https://example.com/original" {
		t.Errorf("expected OriginalURL echoed back, got %q", env.Data.OriginalURL)
	}
	if env.Data.ID == "" {
		t.Error("expected non-empty ID in response")
	}
	if env.Data.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt in response")
	}
}

func TestShortenHandler_Handle_WithCustomCode(t *testing.T) {
	mock := &mockShortener{result: successResult("my-brand")}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "https://example.com/",
		"custom_code":  "my-brand",
	})

	h.Handle(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	// Verify custom code was passed through to use case
	if mock.capturedCmd.CustomCode != "my-brand" {
		t.Errorf("expected CustomCode=my-brand in use case command, got %q", mock.capturedCmd.CustomCode)
	}
}

func TestShortenHandler_Handle_WithExpiry(t *testing.T) {
	mock := &mockShortener{result: successResult("exp001")}
	h := handler.NewShortenHandler(mock, testLog)

	expiry := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "https://example.com/",
		"expires_at":   expiry.Format(time.RFC3339),
	})

	h.Handle(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	// ExpiresAt must reach the use case
	if mock.capturedCmd.ExpiresAt == nil {
		t.Error("expected ExpiresAt to be set in use case command")
	}
}

func TestShortenHandler_Handle_WorkspaceAndUserFromHeaders(t *testing.T) {
	mock := &mockShortener{result: successResult("hdr001")}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls",
		bytes.NewReader([]byte(`{"original_url":"https://example.com"}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Workspace-ID", "ws_custom_123")
	r.Header.Set("X-User-ID", "usr_custom_456")

	h.Handle(w, r)

	if mock.capturedCmd.WorkspaceID != "ws_custom_123" {
		t.Errorf("expected WorkspaceID=ws_custom_123, got %q", mock.capturedCmd.WorkspaceID)
	}
	if mock.capturedCmd.CreatedBy != "usr_custom_456" {
		t.Errorf("expected CreatedBy=usr_custom_456, got %q", mock.capturedCmd.CreatedBy)
	}
}

func TestShortenHandler_Handle_BadJSON_Returns400(t *testing.T) {
	h := handler.NewShortenHandler(&mockShortener{}, testLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls",
		strings.NewReader(`{not valid json}`))
	r.Header.Set("Content-Type", "application/json")

	h.Handle(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	// Response must be a Problem Details object
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("expected Content-Type application/problem+json, got %q", ct)
	}
}

func TestShortenHandler_Handle_UnknownField_Returns400(t *testing.T) {
	h := handler.NewShortenHandler(&mockShortener{}, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url":  "https://example.com",
		"unknown_field": "value", // DisallowUnknownFields catches this
	})

	h.Handle(w, r)

	// Unknown fields return 400
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown field, got %d", w.Code)
	}
}

func TestShortenHandler_Handle_ValidationError_Returns422(t *testing.T) {
	mock := &mockShortener{
		err: apperrors.NewValidationError("original_url is not a valid URL", nil),
	}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "ftp://invalid-scheme.com",
	})

	h.Handle(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d — body: %s", w.Code, w.Body.String())
	}

	var prob response.Problem
	decodeJSON(t, w.Body, &prob)
	if prob.Status != http.StatusUnprocessableEntity {
		t.Errorf("expected problem status 422, got %d", prob.Status)
	}
	if prob.Type != response.ProblemTypeValidation {
		t.Errorf("expected problem type %q, got %q", response.ProblemTypeValidation, prob.Type)
	}
}

func TestShortenHandler_Handle_ShortCodeConflict_Returns409(t *testing.T) {
	mock := &mockShortener{err: apperrors.ErrShortCodeConflict}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "https://example.com",
		"custom_code":  "taken",
	})

	h.Handle(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}

	var prob response.Problem
	decodeJSON(t, w.Body, &prob)
	if prob.Type != response.ProblemTypeConflict {
		t.Errorf("expected conflict problem type, got %q", prob.Type)
	}
}

func TestShortenHandler_Handle_URLBlocked_Returns422(t *testing.T) {
	mock := &mockShortener{err: apperrors.ErrURLBlocked}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "https://malicious.example.com",
	})

	h.Handle(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}

	var prob response.Problem
	decodeJSON(t, w.Body, &prob)
	if prob.Type != response.ProblemTypeURLBlocked {
		t.Errorf("expected url-blocked problem type, got %q", prob.Type)
	}
}

func TestShortenHandler_Handle_UnexpectedError_Returns500(t *testing.T) {
	mock := &mockShortener{err: errors.New("db: connection pool exhausted")}
	h := handler.NewShortenHandler(mock, testLog)

	w := httptest.NewRecorder()
	r := shortenRequest(t, map[string]any{
		"original_url": "https://example.com",
	})

	h.Handle(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}

	// Response body must NOT contain the internal error message.
	if strings.Contains(w.Body.String(), "connection pool exhausted") {
		t.Error("response body must not expose internal error details")
	}
}

func TestShortenHandler_Handle_EmptyBody_Returns400(t *testing.T) {
	h := handler.NewShortenHandler(&mockShortener{}, testLog)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls",
		strings.NewReader(""))
	r.Header.Set("Content-Type", "application/json")

	h.Handle(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d", w.Code)
	}
}
