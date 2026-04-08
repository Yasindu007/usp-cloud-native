package workspace_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appworkspace "github.com/urlshortener/platform/internal/application/workspace"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeWorkspaceRepo struct {
	workspaces    map[string]*domainworkspace.Workspace
	members       map[string]*domainworkspace.Member // key: workspaceID+":"+userID
	forceConflict bool
	forceError    bool
	createCalls   int
}

func newFakeWorkspaceRepo() *fakeWorkspaceRepo {
	return &fakeWorkspaceRepo{
		workspaces: make(map[string]*domainworkspace.Workspace),
		members:    make(map[string]*domainworkspace.Member),
	}
}

func (r *fakeWorkspaceRepo) Create(_ context.Context, w *domainworkspace.Workspace, m *domainworkspace.Member) error {
	r.createCalls++
	if r.forceError {
		return errors.New("db: connection refused")
	}
	if r.forceConflict {
		return domainworkspace.ErrSlugConflict
	}
	for _, existing := range r.workspaces {
		if existing.Slug == w.Slug {
			return domainworkspace.ErrSlugConflict
		}
		if existing.Name == w.Name {
			return domainworkspace.ErrNameConflict
		}
	}
	r.workspaces[w.ID] = w
	r.members[w.ID+":"+m.UserID] = m
	return nil
}

func (r *fakeWorkspaceRepo) GetByID(_ context.Context, id string) (*domainworkspace.Workspace, error) {
	w, ok := r.workspaces[id]
	if !ok {
		return nil, domainworkspace.ErrNotFound
	}
	return w, nil
}

func (r *fakeWorkspaceRepo) GetBySlug(_ context.Context, slug string) (*domainworkspace.Workspace, error) {
	for _, w := range r.workspaces {
		if w.Slug == slug {
			return w, nil
		}
	}
	return nil, domainworkspace.ErrNotFound
}

func (r *fakeWorkspaceRepo) ListForUser(_ context.Context, userID string) ([]*domainworkspace.Workspace, error) {
	var result []*domainworkspace.Workspace
	for key, m := range r.members {
		if m.UserID == userID {
			id := key[:len(key)-len(":"+userID)-1+len(m.UserID)+1]
			_ = id
			// Find workspace for this membership
			for wsID, w := range r.workspaces {
				if wsID == m.WorkspaceID {
					result = append(result, w)
				}
			}
		}
	}
	return result, nil
}

func (r *fakeWorkspaceRepo) CountOwnedByUser(_ context.Context, userID string) (int, error) {
	count := 0
	for _, w := range r.workspaces {
		if w.OwnerID == userID {
			count++
		}
	}
	return count, nil
}

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestCreateHandler_Handle_Success(t *testing.T) {
	repo := newFakeWorkspaceRepo()
	h := appworkspace.NewCreateHandler(repo, testLog)

	result, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name:    "Acme Corp",
		OwnerID: "usr_001",
	})

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.ID == "" {
		t.Error("expected non-empty ID")
	}
	if result.Name != "Acme Corp" {
		t.Errorf("expected Name=Acme Corp, got %q", result.Name)
	}
	// Slug auto-generated from name
	if result.Slug != "acme-corp" {
		t.Errorf("expected Slug=acme-corp, got %q", result.Slug)
	}
	if result.PlanTier != "free" {
		t.Errorf("expected PlanTier=free, got %q", result.PlanTier)
	}
	if result.OwnerID != "usr_001" {
		t.Errorf("expected OwnerID=usr_001, got %q", result.OwnerID)
	}
	if result.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt")
	}
}

func TestCreateHandler_Handle_CustomSlug(t *testing.T) {
	repo := newFakeWorkspaceRepo()
	h := appworkspace.NewCreateHandler(repo, testLog)

	result, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name:    "My Company",
		Slug:    "my-custom-slug",
		OwnerID: "usr_001",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Slug != "my-custom-slug" {
		t.Errorf("expected custom slug, got %q", result.Slug)
	}
}

func TestCreateHandler_Handle_SlugLowercased(t *testing.T) {
	repo := newFakeWorkspaceRepo()
	h := appworkspace.NewCreateHandler(repo, testLog)

	result, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name:    "Test",
		Slug:    "MY-SLUG",
		OwnerID: "usr_001",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Slug != "my-slug" {
		t.Errorf("expected slug lowercased to my-slug, got %q", result.Slug)
	}
}

func TestCreateHandler_Handle_EmptyName_ReturnsValidationError(t *testing.T) {
	h := appworkspace.NewCreateHandler(newFakeWorkspaceRepo(), testLog)

	_, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name:    "",
		OwnerID: "usr_001",
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for empty name, got: %v", err)
	}
}

func TestCreateHandler_Handle_NameTooLong_ReturnsValidationError(t *testing.T) {
	h := appworkspace.NewCreateHandler(newFakeWorkspaceRepo(), testLog)

	longName := string(make([]byte, 101))
	for i := range longName {
		longName = longName[:i] + "a" + longName[i+1:]
	}

	_, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name:    longName,
		OwnerID: "usr_001",
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for long name, got: %v", err)
	}
}

func TestCreateHandler_Handle_WorkspaceLimitReached(t *testing.T) {
	repo := newFakeWorkspaceRepo()
	h := appworkspace.NewCreateHandler(repo, testLog)

	// Create MaxWorkspacesPerUser workspaces first
	for i := 0; i < domainworkspace.MaxWorkspacesPerUser; i++ {
		_, err := h.Handle(context.Background(), appworkspace.CreateCommand{
			Name:    "Workspace " + string(rune('A'+i)),
			OwnerID: "usr_limited",
		})
		if err != nil {
			t.Fatalf("setup: create workspace %d failed: %v", i, err)
		}
	}

	// The next one must fail
	_, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name:    "One Too Many",
		OwnerID: "usr_limited",
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for workspace limit, got: %v", err)
	}
}

func TestCreateHandler_Handle_SlugConflict(t *testing.T) {
	repo := newFakeWorkspaceRepo()
	h := appworkspace.NewCreateHandler(repo, testLog)

	// First workspace
	if _, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name: "Acme", OwnerID: "usr_001",
	}); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Same slug (generated from same name)
	_, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name: "Acme", OwnerID: "usr_002",
	})

	if err == nil {
		t.Error("expected conflict error for duplicate slug, got nil")
	}
}

func TestCreateHandler_Handle_DBError(t *testing.T) {
	repo := newFakeWorkspaceRepo()
	repo.forceError = true
	h := appworkspace.NewCreateHandler(repo, testLog)

	_, err := h.Handle(context.Background(), appworkspace.CreateCommand{
		Name: "Test", OwnerID: "usr_001",
	})

	if err == nil {
		t.Error("expected error when DB fails")
	}
}

// ── GenerateSlug tests ────────────────────────────────────────────────────────

func TestGenerateSlug(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "Acme Corp", "acme-corp"},
		{"all-lower", "hello world", "hello-world"},
		{"special-chars", "Acme Corp!", "acme-corp"},
		{"numbers", "Company123", "company123"},
		{"multiple-spaces", "a  b  c", "a-b-c"},
		{"leading-trailing-spaces", "  hello  ", "hello"},
		{"punctuation", "Hello, World!", "hello-world"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainworkspace.GenerateSlug(tc.input)
			if got != tc.expected {
				t.Errorf("GenerateSlug(%q): want %q, got %q", tc.input, tc.expected, got)
			}
		})
	}
}

func TestRole_Can(t *testing.T) {
	cases := []struct {
		role     domainworkspace.Role
		action   domainworkspace.Action
		expected bool
	}{
		{domainworkspace.RoleOwner, domainworkspace.ActionDeleteWorkspace, true},
		{domainworkspace.RoleAdmin, domainworkspace.ActionDeleteWorkspace, false},
		{domainworkspace.RoleEditor, domainworkspace.ActionCreateURL, true},
		{domainworkspace.RoleEditor, domainworkspace.ActionDeleteURL, false},
		{domainworkspace.RoleViewer, domainworkspace.ActionViewURL, true},
		{domainworkspace.RoleViewer, domainworkspace.ActionCreateURL, false},
		{domainworkspace.RoleAdmin, domainworkspace.ActionManageMembers, true},
		{domainworkspace.RoleViewer, domainworkspace.ActionManageMembers, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.role)+":"+string(tc.action), func(t *testing.T) {
			got := tc.role.Can(tc.action)
			if got != tc.expected {
				t.Errorf("Role(%q).Can(%q): want %v, got %v",
					tc.role, tc.action, tc.expected, got)
			}
		})
	}
}

// Suppress unused import
var _ = time.Now
