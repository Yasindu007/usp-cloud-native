package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
)

// ── Fakes ──────────────────────────────────────────────────────────────────────

type fakeMemberLookup struct {
	members map[string]*domainworkspace.Member
	err     error
}

func newFakeMemberLookup() *fakeMemberLookup {
	return &fakeMemberLookup{members: make(map[string]*domainworkspace.Member)}
}

func (f *fakeMemberLookup) set(wsID, userID string, role domainworkspace.Role) {
	f.members[wsID+":"+userID] = &domainworkspace.Member{
		WorkspaceID: wsID, UserID: userID, Role: role,
	}
}

func (f *fakeMemberLookup) GetMember(_ context.Context, wsID, userID string) (*domainworkspace.Member, error) {
	if f.err != nil {
		return nil, f.err
	}
	m, ok := f.members[wsID+":"+userID]
	if !ok {
		return nil, domainworkspace.ErrMemberNotFound
	}
	return m, nil
}

// withClaims injects auth claims into a request context.
func withClaims(r *http.Request, wsID, userID string) *http.Request {
	claims := &domainauth.Claims{
		WorkspaceID: wsID,
		UserID:      userID,
		Scope:       "read write",
	}
	return r.WithContext(domainauth.WithContext(r.Context(), claims))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestWorkspaceAuth_ValidMember_CallsNext(t *testing.T) {
	lookup := newFakeMemberLookup()
	lookup.set("ws_001", "usr_001", domainworkspace.RoleEditor)

	called := false
	mw := httpmiddleware.WorkspaceAuth(lookup)

	w := httptest.NewRecorder()
	r := withClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil),
		"ws_001", "usr_001",
	)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected next handler called for valid member")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestWorkspaceAuth_MemberStoredInContext(t *testing.T) {
	lookup := newFakeMemberLookup()
	lookup.set("ws_001", "usr_001", domainworkspace.RoleAdmin)

	var captured *domainworkspace.Member
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = httpmiddleware.MemberFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := httpmiddleware.WorkspaceAuth(lookup)

	w := httptest.NewRecorder()
	r := withClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil),
		"ws_001", "usr_001",
	)

	mw(captureHandler).ServeHTTP(w, r)

	if captured == nil {
		t.Fatal("expected member in context, got nil")
	}
	if captured.Role != domainworkspace.RoleAdmin {
		t.Errorf("expected role=admin, got %q", captured.Role)
	}
}

func TestWorkspaceAuth_NonMember_Returns403(t *testing.T) {
	lookup := newFakeMemberLookup()
	// usr_outsider is NOT in ws_001

	called := false
	mw := httpmiddleware.WorkspaceAuth(lookup)

	w := httptest.NewRecorder()
	r := withClaims(
		httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil),
		"ws_001", "usr_outsider",
	)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if called {
		t.Error("next handler must NOT be called for non-members")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestWorkspaceAuth_NoClaims_Returns401(t *testing.T) {
	lookup := newFakeMemberLookup()
	mw := httpmiddleware.WorkspaceAuth(lookup)

	w := httptest.NewRecorder()
	// No claims in context — Authenticate middleware didn't run
	r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing claims, got %d", w.Code)
	}
}

func TestWorkspaceAuth_LookupError_Returns500(t *testing.T) {
	lookup := newFakeMemberLookup()
	lookup.err = errors.New("db: connection refused")
	mw := httpmiddleware.WorkspaceAuth(lookup)

	w := httptest.NewRecorder()
	r := withClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil),
		"ws_001", "usr_001",
	)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	// Fail closed on infrastructure error
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on lookup error, got %d", w.Code)
	}
}

// ── RequireAction tests ───────────────────────────────────────────────────────

func withMember(r *http.Request, role domainworkspace.Role) *http.Request {
	member := &domainworkspace.Member{
		WorkspaceID: "ws_001", UserID: "usr_001", Role: role,
	}
	ctx := context.WithValue(r.Context(),
		struct{ name string }{"workspaceMemberKey"},
		member,
	)
	_ = ctx
	// Inject via WorkspaceAuth fake pipeline instead
	return r
}

func TestRequireAction_PermittedRole_CallsNext(t *testing.T) {
	lookup := newFakeMemberLookup()
	lookup.set("ws_001", "usr_editor", domainworkspace.RoleEditor)

	called := false

	// Chain: WorkspaceAuth → RequireAction → handler
	chain := httpmiddleware.WorkspaceAuth(lookup)(
		httpmiddleware.RequireAction(domainworkspace.ActionCreateURL)(
			nextHandler(&called),
		),
	)

	w := httptest.NewRecorder()
	r := withClaims(
		httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil),
		"ws_001", "usr_editor",
	)

	chain.ServeHTTP(w, r)

	if !called {
		t.Error("expected next called for editor creating URL")
	}
}

func TestRequireAction_InsufficientRole_Returns403(t *testing.T) {
	lookup := newFakeMemberLookup()
	lookup.set("ws_001", "usr_viewer", domainworkspace.RoleViewer)

	called := false

	chain := httpmiddleware.WorkspaceAuth(lookup)(
		httpmiddleware.RequireAction(domainworkspace.ActionCreateURL)(
			nextHandler(&called),
		),
	)

	w := httptest.NewRecorder()
	r := withClaims(
		httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil),
		"ws_001", "usr_viewer",
	)

	chain.ServeHTTP(w, r)

	if called {
		t.Error("next handler must NOT be called for viewer on create URL")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}
