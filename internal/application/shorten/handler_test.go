package shorten_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/shorten"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
	"github.com/urlshortener/platform/pkg/shortcode"
)

// ── In-memory fakes ───────────────────────────────────────────────────────────
// These fakes implement the domain interfaces without any real infrastructure.
// They give us full behavioral control in tests:
//   - fakeRepo.forceConflict simulates DB unique constraint violations
//   - fakeRepo.forceError simulates network/DB failures
//   - fakeCache tracks Set calls to verify cache pre-warming
//
// This is why we code to interfaces: these 50 lines of fake replace
// a running PostgreSQL + Redis cluster in tests.

type fakeRepo struct {
	urls          map[string]*domainurl.URL // shortCode → URL
	forceConflict bool                      // next Create returns ErrConflict
	forceError    bool                      // next Create returns generic error
	createCalls   int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{urls: make(map[string]*domainurl.URL)}
}

func (r *fakeRepo) Create(_ context.Context, u *domainurl.URL) error {
	r.createCalls++
	if r.forceError {
		return errors.New("db: connection refused")
	}
	if r.forceConflict {
		r.forceConflict = false // reset after one conflict (simulate transient)
		return domainurl.ErrConflict
	}
	if _, exists := r.urls[u.ShortCode]; exists {
		return domainurl.ErrConflict
	}
	r.urls[u.ShortCode] = u
	return nil
}

func (r *fakeRepo) GetByShortCode(_ context.Context, shortCode string) (*domainurl.URL, error) {
	u, ok := r.urls[shortCode]
	if !ok {
		return nil, domainurl.ErrNotFound
	}
	return u, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id, _ string) (*domainurl.URL, error) {
	for _, u := range r.urls {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, domainurl.ErrNotFound
}

func (r *fakeRepo) Update(_ context.Context, u *domainurl.URL) error {
	if _, ok := r.urls[u.ShortCode]; !ok {
		return domainurl.ErrNotFound
	}
	r.urls[u.ShortCode] = u
	return nil
}

func (r *fakeRepo) SoftDelete(_ context.Context, id, _ string) error {
	for k, u := range r.urls {
		if u.ID == id {
			delete(r.urls, k)
			return nil
		}
	}
	return domainurl.ErrNotFound
}

func (r *fakeRepo) List(_ context.Context, _ domainurl.ListFilter) ([]*domainurl.URL, string, error) {
	var urls []*domainurl.URL
	for _, u := range r.urls {
		urls = append(urls, u)
	}
	return urls, "", nil
}

func (r *fakeRepo) IncrementClickCount(_ context.Context, _ string) error {
	return nil
}

// fakeCache tracks interactions with the cache layer.
type fakeCache struct {
	entries     map[string]*domainurl.URL
	notFound    map[string]bool
	setCalls    int
	deleteCalls int
	forceError  bool
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		entries:  make(map[string]*domainurl.URL),
		notFound: make(map[string]bool),
	}
}

func (c *fakeCache) Get(_ context.Context, shortCode string) (*domainurl.URL, error) {
	u, ok := c.entries[shortCode]
	if !ok {
		return nil, nil // cache miss
	}
	return u, nil
}

func (c *fakeCache) Set(_ context.Context, u *domainurl.URL, _ int) error {
	c.setCalls++
	if c.forceError {
		return errors.New("redis: connection refused")
	}
	c.entries[u.ShortCode] = u
	return nil
}

func (c *fakeCache) SetNotFound(_ context.Context, shortCode string, _ int) error {
	c.notFound[shortCode] = true
	return nil
}

func (c *fakeCache) IsNotFound(_ context.Context, shortCode string) (bool, error) {
	return c.notFound[shortCode], nil
}

func (c *fakeCache) Delete(_ context.Context, shortCode string) error {
	c.deleteCalls++
	delete(c.entries, shortCode)
	delete(c.notFound, shortCode)
	return nil
}

// ── Test helpers ──────────────────────────────────────────────────────────────

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError, // Suppress logs in tests unless debugging
}))

func newTestHandler(repo *fakeRepo, cache *fakeCache) *shorten.Handler {
	return shorten.NewHandler(
		repo,
		cache,
		shortcode.NewDefault(),
		"https://s.example.com",
		3600,
		testLog,
	)
}

func validCommand() shorten.Command {
	return shorten.Command{
		OriginalURL: "https://example.com/some/long/path?utm=test",
		WorkspaceID: "ws_01HTEST",
		CreatedBy:   "usr_01HTEST",
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestHandler_Handle_Success_GeneratedCode(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	h := newTestHandler(repo, cache)

	result, err := h.Handle(context.Background(), validCommand())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Result must be populated correctly
	if result.ShortURL == "" {
		t.Error("expected non-empty ShortURL")
	}
	if result.ShortCode == "" {
		t.Error("expected non-empty ShortCode")
	}
	if result.ID == "" {
		t.Error("expected non-empty ID")
	}
	if result.OriginalURL != validCommand().OriginalURL {
		t.Errorf("OriginalURL mismatch: want %q, got %q", validCommand().OriginalURL, result.OriginalURL)
	}
	if result.WorkspaceID != validCommand().WorkspaceID {
		t.Errorf("WorkspaceID mismatch: want %q, got %q", validCommand().WorkspaceID, result.WorkspaceID)
	}
	if result.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt")
	}

	// ShortURL must be prefixed with baseURL
	expectedPrefix := "https://s.example.com/"
	if len(result.ShortURL) <= len(expectedPrefix) || result.ShortURL[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("ShortURL %q does not start with %q", result.ShortURL, expectedPrefix)
	}

	// URL must be persisted in repo
	if _, err := repo.GetByShortCode(context.Background(), result.ShortCode); err != nil {
		t.Errorf("URL not found in repo after Handle: %v", err)
	}

	// Cache must have been pre-warmed
	if cache.setCalls != 1 {
		t.Errorf("expected 1 cache Set call, got %d", cache.setCalls)
	}
}

func TestHandler_Handle_Success_CustomCode(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	h := newTestHandler(repo, cache)

	cmd := validCommand()
	cmd.CustomCode = "my-brand"

	result, err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.ShortCode != "my-brand" {
		t.Errorf("expected ShortCode=my-brand, got %q", result.ShortCode)
	}
	if result.ShortURL != "https://s.example.com/my-brand" {
		t.Errorf("expected ShortURL=https://s.example.com/my-brand, got %q", result.ShortURL)
	}
}

func TestHandler_Handle_Success_WithExpiry(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	h := newTestHandler(repo, cache)

	expiry := time.Now().Add(24 * time.Hour)
	cmd := validCommand()
	cmd.ExpiresAt = &expiry

	result, err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u, _ := repo.GetByShortCode(context.Background(), result.ShortCode)
	if u.ExpiresAt == nil {
		t.Error("expected ExpiresAt to be set in persisted entity")
	}
}

func TestHandler_Handle_CollisionRetry_Success(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	h := newTestHandler(repo, cache)

	// Force one collision then succeed on the second attempt.
	repo.forceConflict = true

	result, err := h.Handle(context.Background(), validCommand())
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if result.ShortCode == "" {
		t.Error("expected non-empty ShortCode after collision retry")
	}
	// Should have taken exactly 2 Create calls (1 collision + 1 success)
	if repo.createCalls != 2 {
		t.Errorf("expected 2 Create calls (1 collision + 1 success), got %d", repo.createCalls)
	}
}

func TestHandler_Handle_CustomCodeConflict_NoRetry(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	h := newTestHandler(repo, cache)

	cmd := validCommand()
	cmd.CustomCode = "taken"

	// First call succeeds
	if _, err := h.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("first call unexpected error: %v", err)
	}

	// Second call with same custom code must return ErrShortCodeConflict immediately.
	// Must NOT retry (would be pointless — same code, same constraint).
	_, err := h.Handle(context.Background(), cmd)
	if !errors.Is(err, apperrors.ErrShortCodeConflict) {
		t.Errorf("expected ErrShortCodeConflict, got: %v", err)
	}
	// Only 1 additional Create call (no retries for custom codes)
	if repo.createCalls != 2 {
		t.Errorf("expected exactly 2 total Create calls (1 success + 1 fail, no retries), got %d", repo.createCalls)
	}
}

func TestHandler_Handle_InvalidURL_EmptyString(t *testing.T) {
	h := newTestHandler(newFakeRepo(), newFakeCache())
	cmd := validCommand()
	cmd.OriginalURL = ""

	_, err := h.Handle(context.Background(), cmd)
	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for empty URL, got: %v", err)
	}
}

func TestHandler_Handle_InvalidURL_BadScheme(t *testing.T) {
	h := newTestHandler(newFakeRepo(), newFakeCache())

	for _, bad := range []string{
		"ftp://example.com",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,hello",
		"//no-scheme.com",
	} {
		cmd := validCommand()
		cmd.OriginalURL = bad

		_, err := h.Handle(context.Background(), cmd)
		if !apperrors.IsValidationError(err) {
			t.Errorf("expected validation error for URL %q, got: %v", bad, err)
		}
	}
}

func TestHandler_Handle_InvalidURL_TooLong(t *testing.T) {
	h := newTestHandler(newFakeRepo(), newFakeCache())
	cmd := validCommand()
	cmd.OriginalURL = "https://example.com/" + strings.Repeat("x", 8192)

	_, err := h.Handle(context.Background(), cmd)
	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for URL exceeding max length, got: %v", err)
	}
}

func TestHandler_Handle_InvalidCustomCode_TooShort(t *testing.T) {
	h := newTestHandler(newFakeRepo(), newFakeCache())
	cmd := validCommand()
	cmd.CustomCode = "ab" // below minimum length of 3

	_, err := h.Handle(context.Background(), cmd)
	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for short code too short, got: %v", err)
	}
}

func TestHandler_Handle_InvalidCustomCode_ReservedPath(t *testing.T) {
	h := newTestHandler(newFakeRepo(), newFakeCache())

	for _, reserved := range []string{"healthz", "readyz", "metrics", "api", "admin"} {
		cmd := validCommand()
		cmd.CustomCode = reserved

		_, err := h.Handle(context.Background(), cmd)
		if !apperrors.IsValidationError(err) {
			t.Errorf("expected validation error for reserved path %q, got: %v", reserved, err)
		}
	}
}

func TestHandler_Handle_InvalidCustomCode_SpecialChars(t *testing.T) {
	h := newTestHandler(newFakeRepo(), newFakeCache())

	for _, bad := range []string{"has space", "has.dot", "has/slash", "has@at"} {
		cmd := validCommand()
		cmd.CustomCode = bad

		_, err := h.Handle(context.Background(), cmd)
		if !apperrors.IsValidationError(err) {
			t.Errorf("expected validation error for code %q, got: %v", bad, err)
		}
	}
}

func TestHandler_Handle_CacheFailureIsNonFatal(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	cache.forceError = true // Cache always errors
	h := newTestHandler(repo, cache)

	// Handle must succeed even when cache writes fail.
	// The URL is persisted; the cache failure is logged but not propagated.
	result, err := h.Handle(context.Background(), validCommand())
	if err != nil {
		t.Fatalf("expected success despite cache failure, got: %v", err)
	}
	if result.ShortCode == "" {
		t.Error("expected non-empty ShortCode")
	}
}

func TestHandler_Handle_DBFailure_ReturnsError(t *testing.T) {
	repo := newFakeRepo()
	repo.forceError = true
	h := newTestHandler(repo, newFakeCache())

	_, err := h.Handle(context.Background(), validCommand())
	if err == nil {
		t.Error("expected error when DB fails, got nil")
	}
}

// Missing import for strings — add at top of the test file
var _ = strings.Repeat
