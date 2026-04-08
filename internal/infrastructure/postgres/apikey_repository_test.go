//go:build integration
// +build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	"github.com/urlshortener/platform/pkg/keyutil"
)

func testAPIKeyRepo(t *testing.T) *postgres.APIKeyRepository {
	t.Helper()
	return postgres.NewAPIKeyRepository(testClient(t))
}

// createTestWorkspace creates a workspace + owner member for API key tests.
func createTestWorkspaceForAPIKeys(t *testing.T) string {
	t.Helper()
	wsRepo := postgres.NewWorkspaceRepository(testClient(t))
	ownerID := ulid.Make().String()
	wsID := ulid.Make().String()
	suffix := strings.ToLower(wsID[len(wsID)-8:])
	ws := &domainworkspace.Workspace{
		ID:        wsID,
		Name:      "APIKey Test WS " + suffix,
		Slug:      "apikey-test-" + suffix,
		PlanTier:  "free",
		OwnerID:   ownerID,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	m := &domainworkspace.Member{
		WorkspaceID: ws.ID, UserID: ownerID,
		Role: domainworkspace.RoleOwner, JoinedAt: time.Now().UTC(),
	}
	if err := wsRepo.Create(context.Background(), ws, m); err != nil {
		t.Fatalf("createTestWorkspace failed: %v", err)
	}
	return ws.ID
}

func newTestAPIKey(t *testing.T, wsID string) *domainapikey.APIKey {
	t.Helper()
	rawKey, _ := keyutil.GenerateRaw(wsID)
	hash, _ := keyutil.Hash(rawKey)
	return &domainapikey.APIKey{
		ID:          ulid.Make().String(),
		WorkspaceID: wsID,
		Name:        "Test Key " + ulid.Make().String()[:6],
		KeyHash:     hash,
		KeyPrefix:   domainapikey.ExtractPrefix(rawKey),
		Scopes:      []string{"read", "write"},
		CreatedBy:   "usr_test",
		CreatedAt:   time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestAPIKeyRepository_Create_And_GetByPrefix(t *testing.T) {
	repo := testAPIKeyRepo(t)
	ctx := context.Background()
	wsID := createTestWorkspaceForAPIKeys(t)

	key := newTestAPIKey(t, wsID)
	if err := repo.Create(ctx, key); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	found, err := repo.GetByPrefix(ctx, key.KeyPrefix)
	if err != nil {
		t.Fatalf("GetByPrefix failed: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("expected at least one key, got none")
	}
	if found[0].KeyHash != key.KeyHash {
		t.Errorf("KeyHash mismatch")
	}
	// RawKey must NOT be returned from DB
	if found[0].RawKey != "" {
		t.Error("RawKey must be empty when loaded from DB")
	}
}

func TestAPIKeyRepository_Revoke(t *testing.T) {
	repo := testAPIKeyRepo(t)
	ctx := context.Background()
	wsID := createTestWorkspaceForAPIKeys(t)

	key := newTestAPIKey(t, wsID)
	_ = repo.Create(ctx, key)

	if err := repo.Revoke(ctx, key.ID, wsID); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// After revocation, GetByPrefix should not return the key
	found, _ := repo.GetByPrefix(ctx, key.KeyPrefix)
	for _, k := range found {
		if k.ID == key.ID {
			t.Error("revoked key should not appear in GetByPrefix results")
		}
	}
}

func TestAPIKeyRepository_Revoke_AlreadyRevoked(t *testing.T) {
	repo := testAPIKeyRepo(t)
	ctx := context.Background()
	wsID := createTestWorkspaceForAPIKeys(t)

	key := newTestAPIKey(t, wsID)
	_ = repo.Create(ctx, key)
	_ = repo.Revoke(ctx, key.ID, wsID)

	err := repo.Revoke(ctx, key.ID, wsID)
	if err != domainapikey.ErrAlreadyRevoked {
		t.Errorf("expected ErrAlreadyRevoked, got: %v", err)
	}
}

func TestAPIKeyRepository_List_ExcludesRevoked(t *testing.T) {
	repo := testAPIKeyRepo(t)
	ctx := context.Background()
	wsID := createTestWorkspaceForAPIKeys(t)

	// Create 2 keys
	k1 := newTestAPIKey(t, wsID)
	k2 := newTestAPIKey(t, wsID)
	_ = repo.Create(ctx, k1)
	_ = repo.Create(ctx, k2)

	// Revoke k2
	_ = repo.Revoke(ctx, k2.ID, wsID)

	keys, err := repo.List(ctx, wsID)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	for _, k := range keys {
		if k.ID == k2.ID {
			t.Error("revoked key must not appear in List results")
		}
	}
}
