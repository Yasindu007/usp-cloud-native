//go:build integration
// +build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
)

// testWorkspaceRepo creates a WorkspaceRepository backed by the test DB client.
func testWorkspaceRepo(t *testing.T) *postgres.WorkspaceRepository {
	t.Helper()
	return postgres.NewWorkspaceRepository(testClient(t))
}

// newTestWorkspace builds a workspace entity for tests with a unique slug.
func newTestWorkspace(ownerID string) *domainworkspace.Workspace {
	id := ulid.Make().String()
	suffix := strings.ToLower(id[len(id)-8:])
	slug := "test-" + suffix
	return &domainworkspace.Workspace{
		ID:        id,
		Name:      "Test Workspace " + suffix,
		Slug:      slug,
		PlanTier:  "free",
		OwnerID:   ownerID,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func newOwnerMember(wsID, ownerID string) *domainworkspace.Member {
	return &domainworkspace.Member{
		WorkspaceID: wsID,
		UserID:      ownerID,
		Role:        domainworkspace.RoleOwner,
		JoinedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestWorkspaceRepository_Create_And_GetByID(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()
	ownerID := ulid.Make().String()

	w := newTestWorkspace(ownerID)
	m := newOwnerMember(w.ID, ownerID)

	if err := repo.Create(ctx, w, m); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := repo.GetByID(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if got.ID != w.ID {
		t.Errorf("ID mismatch: want %q, got %q", w.ID, got.ID)
	}
	if got.Slug != w.Slug {
		t.Errorf("Slug mismatch: want %q, got %q", w.Slug, got.Slug)
	}
	if got.OwnerID != ownerID {
		t.Errorf("OwnerID mismatch: want %q, got %q", ownerID, got.OwnerID)
	}
}

func TestWorkspaceRepository_Create_DuplicateSlug_ReturnsConflict(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()

	w1 := newTestWorkspace(ulid.Make().String())
	m1 := newOwnerMember(w1.ID, w1.OwnerID)
	if err := repo.Create(ctx, w1, m1); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	w2 := newTestWorkspace(ulid.Make().String())
	w2.Slug = w1.Slug // force slug collision
	w2.Name = w1.Name + " copy"
	m2 := newOwnerMember(w2.ID, w2.OwnerID)

	err := repo.Create(ctx, w2, m2)
	if !domainworkspace.IsConflict(err) {
		t.Errorf("expected conflict error for duplicate slug, got: %v", err)
	}
}

func TestWorkspaceRepository_GetBySlug(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()

	w := newTestWorkspace(ulid.Make().String())
	if err := repo.Create(ctx, w, newOwnerMember(w.ID, w.OwnerID)); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := repo.GetBySlug(ctx, w.Slug)
	if err != nil {
		t.Fatalf("GetBySlug failed: %v", err)
	}
	if got.ID != w.ID {
		t.Errorf("ID mismatch: want %q, got %q", w.ID, got.ID)
	}
}

func TestWorkspaceRepository_GetMember_OwnerCreatedOnCreate(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()

	ownerID := ulid.Make().String()
	w := newTestWorkspace(ownerID)
	if err := repo.Create(ctx, w, newOwnerMember(w.ID, ownerID)); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	member, err := repo.GetMember(ctx, w.ID, ownerID)
	if err != nil {
		t.Fatalf("GetMember failed: %v", err)
	}
	if member.Role != domainworkspace.RoleOwner {
		t.Errorf("expected role=owner, got %q", member.Role)
	}
}

func TestWorkspaceRepository_AddMember_And_GetMember(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()

	ownerID := ulid.Make().String()
	w := newTestWorkspace(ownerID)
	if err := repo.Create(ctx, w, newOwnerMember(w.ID, ownerID)); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	editorID := ulid.Make().String()
	m := &domainworkspace.Member{
		WorkspaceID: w.ID,
		UserID:      editorID,
		Role:        domainworkspace.RoleEditor,
		InvitedBy:   ownerID,
		JoinedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}

	if err := repo.AddMember(ctx, m); err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}

	got, err := repo.GetMember(ctx, w.ID, editorID)
	if err != nil {
		t.Fatalf("GetMember failed: %v", err)
	}
	if got.Role != domainworkspace.RoleEditor {
		t.Errorf("expected role=editor, got %q", got.Role)
	}
}

func TestWorkspaceRepository_ListForUser(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()

	userID := ulid.Make().String()

	for i := 0; i < 3; i++ {
		w := newTestWorkspace(userID)
		if err := repo.Create(ctx, w, newOwnerMember(w.ID, userID)); err != nil {
			t.Fatalf("Create failed on iteration %d: %v", i, err)
		}
	}

	workspaces, err := repo.ListForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListForUser failed: %v", err)
	}
	if len(workspaces) < 3 {
		t.Errorf("expected at least 3 workspaces, got %d", len(workspaces))
	}
}

func TestWorkspaceRepository_GetMember_NotFound(t *testing.T) {
	repo := testWorkspaceRepo(t)
	ctx := context.Background()

	_, err := repo.GetMember(ctx, "ws_nonexistent", "usr_nonexistent")
	if !domainworkspace.IsNotFound(err) {
		t.Errorf("expected ErrMemberNotFound, got: %v", err)
	}
}
