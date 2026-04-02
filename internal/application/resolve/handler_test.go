package resolve_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/resolve"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// ── In-memory fakes ───────────────────────────────────────────────────────────

// fakeReadonlyRepo implements domainurl.ReadonlyRepository.
type fakeReadonlyRepo struct {
	mu         sync.Mutex
	urls       map[string]*domainurl.URL
	forceError bool
	getCalls   int
}

func newFakeRepo() *fakeReadonlyRepo {
	return &fakeReadonlyRepo{urls: make(map[string]*domainurl.URL)}
}

func (r *fakeReadonlyRepo) GetByShortCode(_ context.Context, shortCode string) (*domainurl.URL, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.getCalls++
	if r.forceError {
		return nil, errors.New("db: connection refused")
	}
	u, ok := r.urls[shortCode]
	if !ok {
		return nil, domainurl.ErrNotFound
	}
	return u, nil
}

// fakeCache implements domainurl.CachePort for resolve tests.
type fakeCache struct {
	mu         sync.Mutex
	entries    map[string]*domainurl.URL
	notFound   map[string]bool
	forceError bool
	getCalls   int
	setCalls   int
	setNFCalls int
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		entries:  make(map[string]*domainurl.URL),
		notFound: make(map[string]bool),
	}
}

func (c *fakeCache) Get(_ context.Context, shortCode string) (*domainurl.URL, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.getCalls++
	if c.forceError {
		return nil, errors.New("redis: connection refused")
	}
	u, ok := c.entries[shortCode]
	if !ok {
		return nil, nil // cache miss
	}
	return u, nil
}

func (c *fakeCache) Set(_ context.Context, u *domainurl.URL, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.setCalls++
	c.entries[u.ShortCode] = u
	return nil
}

func (c *fakeCache) SetNotFound(_ context.Context, shortCode string, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.setNFCalls++
	c.notFound[shortCode] = true
	return nil
}

func (c *fakeCache) IsNotFound(_ context.Context, shortCode string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.forceError {
		return false, errors.New("redis: connection refused")
	}
	return c.notFound[shortCode], nil
}

func (c *fakeCache) Delete(_ context.Context, shortCode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, shortCode)
	delete(c.notFound, shortCode)
	return nil
}

// ── Test helpers ──────────────────────────────────────────────────────────────

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

func newTestHandler(repo *fakeReadonlyRepo, cache *fakeCache) *resolve.Handler {
	return resolve.NewHandler(repo, cache, 3600, 60, testLog)
}

func activeURL(shortCode string) *domainurl.URL {
	return &domainurl.URL{
		ID:          "01HTEST" + shortCode,
		WorkspaceID: "ws_test",
		ShortCode:   shortCode,
		OriginalURL: "https://example.com/original/" + shortCode,
		Status:      domainurl.StatusActive,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
}

func testQuery(shortCode string) resolve.Query {
	return resolve.Query{ShortCode: shortCode}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestHandler_Handle_CacheHit(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()

	u := activeURL("abc1234")
	cache.entries["abc1234"] = u

	h := newTestHandler(repo, cache)
	result, err := h.Handle(context.Background(), testQuery("abc1234"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.OriginalURL != u.OriginalURL {
		t.Errorf("OriginalURL mismatch: want %q, got %q", u.OriginalURL, result.OriginalURL)
	}
	if result.CacheStatus != "hit" {
		t.Errorf("expected CacheStatus=hit, got %q", result.CacheStatus)
	}

	// DB must NOT be queried on a cache hit — validates the short-circuit.
	if repo.getCalls != 0 {
		t.Errorf("expected 0 DB calls on cache hit, got %d", repo.getCalls)
	}
}

func TestHandler_Handle_CacheMiss_DBHit(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()

	u := activeURL("miss001")
	repo.urls["miss001"] = u

	h := newTestHandler(repo, cache)
	result, err := h.Handle(context.Background(), testQuery("miss001"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.OriginalURL != u.OriginalURL {
		t.Errorf("OriginalURL mismatch: want %q, got %q", u.OriginalURL, result.OriginalURL)
	}
	if result.CacheStatus != "miss" {
		t.Errorf("expected CacheStatus=miss, got %q", result.CacheStatus)
	}

	// DB must have been queried exactly once.
	if repo.getCalls != 1 {
		t.Errorf("expected 1 DB call on cache miss, got %d", repo.getCalls)
	}
}

func TestHandler_Handle_NegativeCacheHit_SkipsDB(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()

	// Pre-populate negative cache.
	cache.notFound["ghost"] = true

	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("ghost"))

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound from negative cache, got: %v", err)
	}
	// DB must NOT be hit — the negative cache short-circuited.
	if repo.getCalls != 0 {
		t.Errorf("expected 0 DB calls on negative cache hit, got %d", repo.getCalls)
	}
}

func TestHandler_Handle_DBMiss_PopulatesNegativeCache(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	// repo has no entry for "nobody"

	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("nobody"))

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	// Give async goroutine time to execute.
	time.Sleep(20 * time.Millisecond)

	// Negative cache must have been populated.
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.setNFCalls == 0 {
		t.Error("expected negative cache to be populated after DB miss, got 0 SetNotFound calls")
	}
}

func TestHandler_Handle_ExpiredURL_ReturnsErrExpired(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()

	past := time.Now().Add(-1 * time.Hour)
	u := activeURL("expired1")
	u.ExpiresAt = &past

	repo.urls["expired1"] = u

	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("expired1"))

	if !errors.Is(err, apperrors.ErrURLExpired) {
		t.Errorf("expected ErrURLExpired, got: %v", err)
	}
}

func TestHandler_Handle_DisabledURL_ReturnsErrDisabled(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()

	u := activeURL("disabled1")
	u.Status = domainurl.StatusDisabled
	repo.urls["disabled1"] = u

	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("disabled1"))

	if !errors.Is(err, apperrors.ErrURLDisabled) {
		t.Errorf("expected ErrURLDisabled, got: %v", err)
	}
}

func TestHandler_Handle_DeletedURL_ReturnsErrNotFound(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()

	// GetByShortCode returns ErrDeleted for soft-deleted URLs (from postgres adapter).
	repo.urls["deleted1"] = nil // signal nil = we'll use forceError to simulate

	// Simulate the postgres adapter returning ErrDeleted (which IsNotFound catches)
	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("notexist_deleted"))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound for deleted/missing URL, got: %v", err)
	}
}

func TestHandler_Handle_CacheHit_ExpiredURL_ReturnsErrExpired(t *testing.T) {
	// Even when served from cache, expired URLs must return 410.
	// This tests the CanRedirect() check on cached entries.
	repo := newFakeRepo()
	cache := newFakeCache()

	past := time.Now().Add(-1 * time.Hour)
	u := activeURL("cachedexp")
	u.ExpiresAt = &past
	cache.entries["cachedexp"] = u // stale cache entry

	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("cachedexp"))

	if !errors.Is(err, apperrors.ErrURLExpired) {
		t.Errorf("expected ErrURLExpired from stale cache entry, got: %v", err)
	}
	// DB must NOT be queried — we serve the cached result and evaluate it.
	if repo.getCalls != 0 {
		t.Errorf("expected 0 DB calls, got %d", repo.getCalls)
	}
}

func TestHandler_Handle_CacheError_DegradeToDatabase(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	cache.forceError = true // Cache always errors

	u := activeURL("dbfallback")
	repo.urls["dbfallback"] = u

	h := newTestHandler(repo, cache)
	result, err := h.Handle(context.Background(), testQuery("dbfallback"))

	// Must succeed — cache failure degrades to DB, not to 500.
	if err != nil {
		t.Fatalf("expected success on cache error degradation, got: %v", err)
	}
	if result.OriginalURL != u.OriginalURL {
		t.Errorf("OriginalURL mismatch: want %q, got %q", u.OriginalURL, result.OriginalURL)
	}
	if repo.getCalls != 1 {
		t.Errorf("expected 1 DB call after cache error, got %d", repo.getCalls)
	}
}

func TestHandler_Handle_DBError_ReturnsError(t *testing.T) {
	repo := newFakeRepo()
	repo.forceError = true
	cache := newFakeCache()

	h := newTestHandler(repo, cache)
	_, err := h.Handle(context.Background(), testQuery("anything"))

	if err == nil {
		t.Error("expected error when DB fails, got nil")
	}
	// Must not be a domain-level not-found — it's an infrastructure error.
	if errors.Is(err, apperrors.ErrNotFound) {
		t.Error("expected infrastructure error, not ErrNotFound")
	}
}

func TestHandler_Handle_NilCache_FallsBackToDatabase(t *testing.T) {
	repo := newFakeRepo()
	u := activeURL("nocache1")
	repo.urls["nocache1"] = u

	// nil cache = running without Redis (DB-only mode)
	h := resolve.NewHandler(repo, nil, 3600, 60, testLog)

	result, err := h.Handle(context.Background(), testQuery("nocache1"))
	if err != nil {
		t.Fatalf("expected success with nil cache, got: %v", err)
	}
	if result.OriginalURL != u.OriginalURL {
		t.Errorf("OriginalURL mismatch: want %q, got %q", u.OriginalURL, result.OriginalURL)
	}
}
