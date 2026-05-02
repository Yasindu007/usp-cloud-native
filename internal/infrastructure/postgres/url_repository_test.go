//go:build integration
// +build integration

// Integration tests require a running PostgreSQL instance.
// Run with: go test -v -tags=integration ./internal/infrastructure/postgres/...
//
// These tests are excluded from the default `go test ./...` run.
// In CI (Phase 4), they run in a separate job with a real Postgres service container.
// Locally: make infra-up before running.

package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/urlshortener/platform/internal/config"
	"github.com/urlshortener/platform/internal/domain/url"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
)

// testClient creates a real DB client for integration tests.
// Uses DB_PRIMARY_DSN from environment (set via .env or CI service).
func testClient(t *testing.T) *postgres.Client {
	t.Helper()

	if err := config.LoadDotEnv(); err != nil {
		t.Fatalf("failed to load .env for integration tests: %v", err)
	}

	dsn := os.Getenv("DB_PRIMARY_DSN")
	if dsn == "" {
		dsn = "postgresql://urlshortener:secret@localhost:5432/urlshortener?sslmode=disable"
	}

	cfg := &config.Config{
		DBPrimaryDSN:       dsn,
		DBReplicaDSN:       dsn,
		DBMaxOpenConns:     5,
		DBMinOpenConns:     1,
		DBConnMaxLifetimeM: 5,
		DBConnMaxIdleTimeM: 2,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := postgres.New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to connect to test database: %v\n"+
			"Is postgres running? Run: make infra-up", err)
	}

	t.Cleanup(func() { client.Close() })
	return client
}

// testRepo creates a URLRepository backed by the test client.
func testRepo(t *testing.T) *postgres.URLRepository {
	t.Helper()
	return postgres.NewURLRepository(testClient(t))
}

// newTestURL creates a URL entity for tests with unique short code.
func newTestURL(workspaceID string) *url.URL {
	id := ulid.Make().String()
	return &url.URL{
		ID:          id,
		WorkspaceID: workspaceID,
		// Use the ULID entropy tail rather than the timestamp prefix.
		// The first ULID characters are time-derived and can collide in fast tests.
		ShortCode:   "t" + id[len(id)-6:],
		OriginalURL: "https://example.com/test/" + id,
		Title:       "Test URL",
		Status:      url.StatusActive,
		CreatedBy:   "user_test",
		CreatedAt:   time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:   time.Now().UTC().Truncate(time.Microsecond),
	}
}

func createWorkspaceFixture(t *testing.T, ctx context.Context, client *postgres.Client, workspaceID string) {
	t.Helper()

	repo := postgres.NewWorkspaceRepository(client)
	ownerID := ulid.Make().String()
	now := time.Now().UTC().Truncate(time.Microsecond)
	slugSuffix := strings.ToLower(workspaceID)
	if len(slugSuffix) > 12 {
		slugSuffix = slugSuffix[:12]
	}

	w := &domainworkspace.Workspace{
		ID:        workspaceID,
		Name:      "Test Workspace " + slugSuffix,
		Slug:      "test-" + slugSuffix,
		PlanTier:  "free",
		OwnerID:   ownerID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m := &domainworkspace.Member{
		WorkspaceID: workspaceID,
		UserID:      ownerID,
		Role:        domainworkspace.RoleOwner,
		JoinedAt:    now,
	}

	if err := repo.Create(ctx, w, m); err != nil {
		t.Fatalf("failed to create workspace fixture: %v", err)
	}
}

func TestURLRepository_Create_And_GetByShortCode(t *testing.T) {
	client := testClient(t)
	repo := postgres.NewURLRepository(client)
	ctx := context.Background()
	workspaceID := ulid.Make().String()
	createWorkspaceFixture(t, ctx, client, workspaceID)

	u := newTestURL(workspaceID)

	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := repo.GetByShortCode(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("GetByShortCode failed: %v", err)
	}

	if got.ID != u.ID {
		t.Errorf("expected ID %q, got %q", u.ID, got.ID)
	}
	if got.OriginalURL != u.OriginalURL {
		t.Errorf("expected OriginalURL %q, got %q", u.OriginalURL, got.OriginalURL)
	}
	if got.Status != url.StatusActive {
		t.Errorf("expected status active, got %q", got.Status)
	}
}

func TestURLRepository_Create_DuplicateShortCode(t *testing.T) {
	client := testClient(t)
	repo := postgres.NewURLRepository(client)
	ctx := context.Background()
	workspaceID := ulid.Make().String()
	createWorkspaceFixture(t, ctx, client, workspaceID)

	u := newTestURL(workspaceID)

	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	// Second insert with same short_code must return ErrConflict.
	u2 := newTestURL(workspaceID)
	u2.ShortCode = u.ShortCode // Force collision

	err := repo.Create(ctx, u2)
	if !url.IsConflict(err) {
		t.Errorf("expected ErrConflict for duplicate short_code, got: %v", err)
	}
}

func TestURLRepository_GetByShortCode_NotFound(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	_, err := repo.GetByShortCode(ctx, "doesnotexist")
	if !url.IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestURLRepository_SoftDelete(t *testing.T) {
	client := testClient(t)
	repo := postgres.NewURLRepository(client)
	ctx := context.Background()
	workspaceID := ulid.Make().String()
	createWorkspaceFixture(t, ctx, client, workspaceID)

	u := newTestURL(workspaceID)
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := repo.SoftDelete(ctx, u.ID, workspaceID); err != nil {
		t.Fatalf("SoftDelete failed: %v", err)
	}

	// After soft delete, GetByShortCode must return ErrDeleted.
	_, err := repo.GetByShortCode(ctx, u.ShortCode)
	if !url.IsNotFound(err) {
		t.Errorf("expected ErrDeleted (IsNotFound), got: %v", err)
	}
}

func TestURLRepository_SoftDelete_WrongWorkspace(t *testing.T) {
	client := testClient(t)
	repo := postgres.NewURLRepository(client)
	ctx := context.Background()
	workspaceID := ulid.Make().String()
	createWorkspaceFixture(t, ctx, client, workspaceID)

	u := newTestURL(workspaceID)
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Attempt soft delete with a different workspace ID.
	// Must return ErrNotFound (no rows affected, no information leakage).
	err := repo.SoftDelete(ctx, u.ID, "wrong_workspace_id")
	if !url.IsNotFound(err) {
		t.Errorf("expected ErrNotFound for wrong workspace, got: %v", err)
	}
}

func TestURLRepository_IncrementClickCount(t *testing.T) {
	client := testClient(t)
	repo := postgres.NewURLRepository(client)
	ctx := context.Background()
	workspaceID := ulid.Make().String()
	createWorkspaceFixture(t, ctx, client, workspaceID)

	u := newTestURL(workspaceID)
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := repo.IncrementClickCount(ctx, u.ShortCode); err != nil {
			t.Fatalf("IncrementClickCount failed on iteration %d: %v", i, err)
		}
	}

	got, _ := repo.GetByShortCode(ctx, u.ShortCode)
	if got.ClickCount != 3 {
		t.Errorf("expected click_count=3, got %d", got.ClickCount)
	}
}

func TestURLRepository_List_CursorPagination(t *testing.T) {
	client := testClient(t)
	repo := postgres.NewURLRepository(client)
	ctx := context.Background()
	workspaceID := ulid.Make().String()
	createWorkspaceFixture(t, ctx, client, workspaceID)

	// Insert 5 URLs.
	for i := 0; i < 5; i++ {
		u := newTestURL(workspaceID)
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // Ensure distinct ULID ordering
	}

	// Fetch page 1 (2 items).
	page1, cursor, err := repo.List(ctx, url.ListFilter{
		WorkspaceID: workspaceID,
		Limit:       2,
	})
	if err != nil {
		t.Fatalf("List page 1 failed: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("expected 2 items on page 1, got %d", len(page1))
	}
	if cursor == "" {
		t.Error("expected non-empty cursor after page 1")
	}

	// Fetch page 2 using cursor.
	page2, cursor2, err := repo.List(ctx, url.ListFilter{
		WorkspaceID: workspaceID,
		Limit:       2,
		Cursor:      cursor,
	})
	if err != nil {
		t.Fatalf("List page 2 failed: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("expected 2 items on page 2, got %d", len(page2))
	}

	// Fetch page 3 — only 1 item remaining.
	page3, cursor3, err := repo.List(ctx, url.ListFilter{
		WorkspaceID: workspaceID,
		Limit:       2,
		Cursor:      cursor2,
	})
	if err != nil {
		t.Fatalf("List page 3 failed: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("expected 1 item on page 3, got %d", len(page3))
	}
	if cursor3 != "" {
		t.Errorf("expected empty cursor on last page, got %q", cursor3)
	}

	// Verify no duplicates across pages.
	seen := map[string]bool{}
	for _, u := range append(append(page1, page2...), page3...) {
		if seen[u.ID] {
			t.Errorf("duplicate ID across pages: %s", u.ID)
		}
		seen[u.ID] = true
	}
}

func TestURLRepository_Ping(t *testing.T) {
	client := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}
