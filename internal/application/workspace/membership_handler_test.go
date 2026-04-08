package workspace_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appworkspace "github.com/urlshortener/platform/internal/application/workspace"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

// ── Fake member repository ────────────────────────────────────────────────────

type fakeMemberRepo struct {
	members    map[string]*domainworkspace.Member // key: wsID:userID
	forceError bool
}

func newFakeMemberRepo() *fakeMemberRepo {
	return &fakeMemberRepo{members: make(map[string]*domainworkspace.Member)}
}

func (r *fakeMemberRepo) key(wsID, userID string) string { return wsID + ":" + userID }

func (r *fakeMemberRepo) AddMember(_ context.Context, m *domainworkspace.Member) error {
	if r.forceError {
		return errors.New("db: error")
	}
	k := r.key(m.WorkspaceID, m.UserID)
	if _, exists := r.members[k]; exists {
		return domainworkspace.ErrMemberAlreadyExists
	}
	r.members[k] = m
	return nil
}

func (r *fakeMemberRepo) GetMember(_ context.Context, wsID, userID string) (*domainworkspace.Member, error) {
	m, ok := r.members[r.key(wsID, userID)]
	if !ok {
		return nil, domainworkspace.ErrMemberNotFound
	}
	return m, nil
}

func (r *fakeMemberRepo) ListMembers(_ context.Context, wsID string) ([]*domainworkspace.Member, error) {
	var list []*domainworkspace.Member
	for _, m := range r.members {
		if m.WorkspaceID == wsID {
			list = append(list, m)
		}
	}
	return list, nil
}

func (r *fakeMemberRepo) UpdateRole(_ context.Context, wsID, userID string, role domainworkspace.Role) error {
	k := r.key(wsID, userID)
	m, ok := r.members[k]
	if !ok {
		return domainworkspace.ErrMemberNotFound
	}
	m.Role = role
	return nil
}

func (r *fakeMemberRepo) RemoveMember(_ context.Context, wsID, userID string) error {
	k := r.key(wsID, userID)
	if _, ok := r.members[k]; !ok {
		return domainworkspace.ErrMemberNotFound
	}
	delete(r.members, k)
	return nil
}

// seedMember adds a member directly to the fake repo (test setup helper).
func seedMember(r *fakeMemberRepo, wsID, userID string, role domainworkspace.Role) {
	r.members[r.key(wsID, userID)] = &domainworkspace.Member{
		WorkspaceID: wsID,
		UserID:      userID,
		Role:        role,
		JoinedAt:    time.Now().UTC(),
	}
}

// ── AddMember tests ───────────────────────────────────────────────────────────

func TestAddMemberHandler_Handle_AdminCanAddEditor(t *testing.T) {
	wsRepo := newFakeWorkspaceRepo()
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewAddMemberHandler(wsRepo, memberRepo, testLog)

	// Requesting user is an admin in the workspace
	seedMember(memberRepo, "ws_001", "usr_admin", domainworkspace.RoleAdmin)

	result, err := h.Handle(context.Background(), appworkspace.AddMemberCommand{
		WorkspaceID:      "ws_001",
		InvitedUserID:    "usr_newbie",
		Role:             "editor",
		RequestingUserID: "usr_admin",
	})

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Role != "editor" {
		t.Errorf("expected role=editor, got %q", result.Role)
	}
	if result.UserID != "usr_newbie" {
		t.Errorf("expected UserID=usr_newbie, got %q", result.UserID)
	}
}

func TestAddMemberHandler_Handle_EditorCannotAddMember(t *testing.T) {
	wsRepo := newFakeWorkspaceRepo()
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewAddMemberHandler(wsRepo, memberRepo, testLog)

	// Requesting user is only an editor
	seedMember(memberRepo, "ws_001", "usr_editor", domainworkspace.RoleEditor)

	_, err := h.Handle(context.Background(), appworkspace.AddMemberCommand{
		WorkspaceID:      "ws_001",
		InvitedUserID:    "usr_newbie",
		Role:             "viewer",
		RequestingUserID: "usr_editor",
	})

	if !errors.Is(err, apperrors.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for editor adding member, got: %v", err)
	}
}

func TestAddMemberHandler_Handle_CannotAssignOwnerRole(t *testing.T) {
	wsRepo := newFakeWorkspaceRepo()
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewAddMemberHandler(wsRepo, memberRepo, testLog)

	seedMember(memberRepo, "ws_001", "usr_admin", domainworkspace.RoleAdmin)

	_, err := h.Handle(context.Background(), appworkspace.AddMemberCommand{
		WorkspaceID:      "ws_001",
		InvitedUserID:    "usr_target",
		Role:             "owner", // must be rejected
		RequestingUserID: "usr_admin",
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for assigning owner role, got: %v", err)
	}
}

func TestAddMemberHandler_Handle_NonMemberCannotAdd(t *testing.T) {
	wsRepo := newFakeWorkspaceRepo()
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewAddMemberHandler(wsRepo, memberRepo, testLog)
	// Requesting user has NO membership in the workspace

	_, err := h.Handle(context.Background(), appworkspace.AddMemberCommand{
		WorkspaceID:      "ws_001",
		InvitedUserID:    "usr_target",
		Role:             "viewer",
		RequestingUserID: "usr_outsider",
	})

	if !errors.Is(err, apperrors.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for non-member, got: %v", err)
	}
}

func TestAddMemberHandler_Handle_DuplicateMember(t *testing.T) {
	wsRepo := newFakeWorkspaceRepo()
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewAddMemberHandler(wsRepo, memberRepo, testLog)

	seedMember(memberRepo, "ws_001", "usr_admin", domainworkspace.RoleAdmin)
	seedMember(memberRepo, "ws_001", "usr_existing", domainworkspace.RoleEditor)

	_, err := h.Handle(context.Background(), appworkspace.AddMemberCommand{
		WorkspaceID:      "ws_001",
		InvitedUserID:    "usr_existing", // already a member
		Role:             "viewer",
		RequestingUserID: "usr_admin",
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for duplicate member, got: %v", err)
	}
}

func TestAddMemberHandler_Handle_InvalidRole(t *testing.T) {
	wsRepo := newFakeWorkspaceRepo()
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewAddMemberHandler(wsRepo, memberRepo, testLog)

	seedMember(memberRepo, "ws_001", "usr_admin", domainworkspace.RoleAdmin)

	_, err := h.Handle(context.Background(), appworkspace.AddMemberCommand{
		WorkspaceID:      "ws_001",
		InvitedUserID:    "usr_new",
		Role:             "superuser", // invalid
		RequestingUserID: "usr_admin",
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for invalid role, got: %v", err)
	}
}

// ── ListMembers tests ─────────────────────────────────────────────────────────

func TestListMembersHandler_Handle_MemberCanList(t *testing.T) {
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewListMembersHandler(memberRepo)

	seedMember(memberRepo, "ws_001", "usr_owner", domainworkspace.RoleOwner)
	seedMember(memberRepo, "ws_001", "usr_editor", domainworkspace.RoleEditor)
	seedMember(memberRepo, "ws_001", "usr_viewer", domainworkspace.RoleViewer)

	results, err := h.Handle(context.Background(), appworkspace.ListMembersQuery{
		WorkspaceID:      "ws_001",
		RequestingUserID: "usr_viewer", // viewer can list members
	})

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 members, got %d", len(results))
	}
}

func TestListMembersHandler_Handle_NonMemberCannotList(t *testing.T) {
	memberRepo := newFakeMemberRepo()
	h := appworkspace.NewListMembersHandler(memberRepo)

	_, err := h.Handle(context.Background(), appworkspace.ListMembersQuery{
		WorkspaceID:      "ws_001",
		RequestingUserID: "usr_outsider",
	})

	if !errors.Is(err, apperrors.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for non-member, got: %v", err)
	}
}
