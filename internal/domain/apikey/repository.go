package apikey

import "context"

// Repository defines the persistence contract for API keys.
// Implemented in internal/infrastructure/postgres/apikey_repository.go.
type Repository interface {
	// Create persists a new API key record.
	// The RawKey field on the entity is NOT persisted — only KeyHash.
	Create(ctx context.Context, key *APIKey) error

	// GetByPrefix returns all active (non-revoked) keys that match
	// the given key_prefix. Used during authentication to find key
	// candidates before the expensive bcrypt comparison.
	// Returns an empty slice (not an error) if no matches found.
	GetByPrefix(ctx context.Context, prefix string) ([]*APIKey, error)

	// GetByID returns a specific API key by its ULID.
	// Returns ErrNotFound if no key with that ID exists in the workspace.
	GetByID(ctx context.Context, id, workspaceID string) (*APIKey, error)

	// List returns all non-revoked API keys for a workspace.
	// Ordered by created_at DESC.
	List(ctx context.Context, workspaceID string) ([]*APIKey, error)

	// Revoke soft-deletes an API key by setting revoked_at = now().
	// Returns ErrNotFound if the key doesn't exist or is already revoked.
	Revoke(ctx context.Context, id, workspaceID string) error

	// UpdateLastUsed sets last_used_at = now() for the given key ID.
	// Called asynchronously after successful auth — never blocks requests.
	UpdateLastUsed(ctx context.Context, id string) error
}
