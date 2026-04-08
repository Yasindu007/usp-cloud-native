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

	"github.com/urlshortener/platform/internal/application/apperrors"
	appworkspace "github.com/urlshortener/platform/internal/application/workspace"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
)

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockWorkspaceCreator struct {
	result *appworkspace.CreateResult
	err    error
}

func (m *mockWorkspaceCreator) Handle(_ context.Context, _ appworkspace.CreateCommand) (*appworkspace.CreateResult, error) {
	return m.result, m.err
}

type mockMemberAdder struct {
	result *appworkspace.AddMemberResult
	err    error
}

func (m *mockMemberAdder) Handle(_ context.Context, _ appworkspace.AddMemberCommand) (*appworkspace.AddMemberResult, error) {
	return m.result, m.err
}

type mockWorkspaceGetter struct {
	result *appworkspace.GetResult
	err    error
}

func (m *mockWorkspaceGetter) Handle(_ context.Context, _ appworkspace.GetQuery) (*appworkspace.GetResult, error) {
	return m.result, m.err
}

type mockWorkspaceLister struct {
	results []*appworkspace.ListResult
	err     error
}

func (m *mockWorkspaceLister) Handle(_ context.Context, _ appworkspace.ListQuery) ([]*appworkspace.ListResult, error) {
	return m.results, m.err
}

type mockMemberLister struct {
	results []*appworkspace.MemberResult
	err     error
}

func (m *mockMemberLister) Handle(_ context.Context, _ appworkspace.ListMembersQuery) ([]*appworkspace.MemberResult, error) {
	return m.results, m.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newWorkspaceHandler(
	creator handler.WorkspaceCreator,
	getter handler.WorkspaceGetter,
	lister handler.WorkspaceLister,
	memberAdder handler.MemberAdder,
	memberLister handler.MemberLister,
) *handler.WorkspaceHandler {
	return handler.NewWorkspaceHandler(creator, getter, lister, memberAdder, memberLister, testLog)
}

func withWorkspaceID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceID", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func withAuthClaims(r *http.Request, wsID, userID string) *http.Request {
	claims := &domainauth.Claims{
		UserID:      userID,
		WorkspaceID: wsID,
		Scope:       "read write",
	}
	return r.WithContext(domainauth.WithContext(r.Context(), claims))
}

func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// ── Create workspace tests ────────────────────────────────────────────────────

func TestWorkspaceHandler_Create_Success(t *testing.T) {
	creator := &mockWorkspaceCreator{
		result: &appworkspace.CreateResult{
			ID: "ws_001", Name: "Acme", Slug: "acme",
			PlanTier: "free", OwnerID: "usr_001", CreatedAt: "2026-01-01T00:00:00Z",
		},
	}
	h := newWorkspaceHandler(creator, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := withAuthClaims(
		httptest.NewRequest(http.MethodPost, "/api/v1/workspaces",
			jsonBody(t, map[string]string{"name": "Acme"})),
		"ws_001", "usr_001",
	)
	r.Header.Set("Content-Type", "application/json")

	h.Create(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d — body: %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceHandler_Create_NoClaims_Returns401(t *testing.T) {
	h := newWorkspaceHandler(&mockWorkspaceCreator{}, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces",
		jsonBody(t, map[string]string{"name": "Test"}))
	// No claims in context

	h.Create(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestWorkspaceHandler_Create_ValidationError_Returns422(t *testing.T) {
	creator := &mockWorkspaceCreator{
		err: apperrors.NewValidationError("name is required", nil),
	}
	h := newWorkspaceHandler(creator, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := withAuthClaims(
		httptest.NewRequest(http.MethodPost, "/api/v1/workspaces",
			jsonBody(t, map[string]string{"name": ""})),
		"ws_001", "usr_001",
	)

	h.Create(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}
}

// ── AddMember tests ───────────────────────────────────────────────────────────

func TestWorkspaceHandler_AddMember_Success(t *testing.T) {
	adder := &mockMemberAdder{
		result: &appworkspace.AddMemberResult{
			WorkspaceID: "ws_001", UserID: "usr_new",
			Role: "editor", JoinedAt: "2026-01-01T00:00:00Z",
		},
	}
	h := newWorkspaceHandler(nil, nil, nil, adder, nil)

	w := httptest.NewRecorder()
	r := withAuthClaims(
		withWorkspaceID(
			httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws_001/members",
				jsonBody(t, map[string]string{"user_id": "usr_new", "role": "editor"})),
			"ws_001",
		),
		"ws_001", "usr_admin",
	)

	h.AddMember(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d — body: %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceHandler_AddMember_Unauthorized_Returns403(t *testing.T) {
	adder := &mockMemberAdder{err: apperrors.ErrUnauthorized}
	h := newWorkspaceHandler(nil, nil, nil, adder, nil)

	w := httptest.NewRecorder()
	r := withAuthClaims(
		withWorkspaceID(
			httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws_001/members",
				jsonBody(t, map[string]string{"user_id": "usr_new", "role": "viewer"})),
			"ws_001",
		),
		"ws_001", "usr_viewer",
	)

	h.AddMember(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestWorkspaceHandler_AddMember_InfraError_Returns500(t *testing.T) {
	adder := &mockMemberAdder{err: errors.New("db: connection refused")}
	h := newWorkspaceHandler(nil, nil, nil, adder, nil)

	w := httptest.NewRecorder()
	r := withAuthClaims(
		withWorkspaceID(
			httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws_001/members",
				jsonBody(t, map[string]string{"user_id": "usr_new", "role": "editor"})),
			"ws_001",
		),
		"ws_001", "usr_admin",
	)

	h.AddMember(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}
