package workspace

import "context"

// Repository defines the persistence contract for Workspace aggregates.
// Implemented in internal/infrastructure/postgres/workspace_repository.go.
type Repository interface {
	// Create persists a new workspace and its owner membership atomically
	// within a single transaction. The owner is automatically added as
	// a member with role "owner" — this invariant is enforced here, not
	// in the application layer, to prevent orphaned workspaces.
	Create(ctx context.Context, w *Workspace, ownerMember *Member) error

	// GetByID retrieves a workspace by its ULID.
	// Returns ErrNotFound if no workspace with that ID exists.
	GetByID(ctx context.Context, id string) (*Workspace, error)

	// GetBySlug retrieves a workspace by its slug.
	// Returns ErrNotFound if no workspace with that slug exists.
	GetBySlug(ctx context.Context, slug string) (*Workspace, error)

	// ListForUser returns all workspaces the given user is a member of.
	// Ordered by workspace created_at DESC.
	ListForUser(ctx context.Context, userID string) ([]*Workspace, error)

	// CountOwnedByUser returns the number of workspaces owned by the user.
	// Used to enforce the MaxWorkspacesPerUser limit.
	CountOwnedByUser(ctx context.Context, userID string) (int, error)
}

// MemberRepository defines the persistence contract for workspace memberships.
// Kept separate from Repository because member operations are common read paths
// (every authenticated request checks membership) and benefit from being on
// the read replica independently of workspace writes.
type MemberRepository interface {
	// AddMember adds a user to a workspace with the given role.
	// Returns ErrMemberAlreadyExists if the user is already a member.
	// The owner role MUST NOT be assigned via this method — use Repository.Create.
	AddMember(ctx context.Context, m *Member) error

	// GetMember retrieves a specific user's membership in a workspace.
	// Returns ErrNotFound if the user is not a member.
	// This is the hot path — called on every authenticated API request
	// to verify workspace access. Uses the read replica.
	GetMember(ctx context.Context, workspaceID, userID string) (*Member, error)

	// ListMembers returns all members of a workspace ordered by joined_at ASC.
	ListMembers(ctx context.Context, workspaceID string) ([]*Member, error)

	// UpdateRole changes a member's role.
	// Returns ErrNotFound if the member doesn't exist.
	// The application layer must prevent downgrading the last owner.
	UpdateRole(ctx context.Context, workspaceID, userID string, role Role) error

	// RemoveMember removes a user from a workspace.
	// Returns ErrNotFound if the user is not a member.
	// The application layer must prevent removing the last owner.
	RemoveMember(ctx context.Context, workspaceID, userID string) error
}
