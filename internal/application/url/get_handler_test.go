package url_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appurl "github.com/urlshortener/platform/internal/application/url"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// ── Shared fake repository ─────────────────────────────────────────────────────
// All tests in this package share this fake — it's defined once here
// and reused across get, list, update, delete test files.

type fakeURLRepo struct {
	urls            map[string]*domainurl.URL // id → URL
	forceError      bool
	updateCalls     int
	deletedIDs      []string
	clickIncrements []string
}

func newFakeURLRepo() *fakeURLRepo {
	return &fakeURLRepo{urls: make(map[string]*domainurl.URL)}
}

func (r *fakeURLRepo) seed(u *domainurl.URL) {
	r.urls[u.ID] = u
}

func (r *fakeURLRepo) Create(_ context.Context, u *domainurl.URL) error {
	if r.forceError {
		return errors.New("db: error")
	}
	r.urls[u.ID] = u
	return nil
}

func (r *fakeURLRepo) GetByShortCode(_ context.Context, shortCode string) (*domainurl.URL, error) {
	for _, u := range r.urls {
		if u.ShortCode == shortCode && u.DeletedAt == nil {
			return u, nil
		}
	}
	return nil, domainurl.ErrNotFound
}

func (r *fakeURLRepo) GetByID(_ context.Context, id, workspaceID string) (*domainurl.URL, error) {
	if r.forceError {
		return nil, errors.New("db: error")
	}
	u, ok := r.urls[id]
	if !ok || u.WorkspaceID != workspaceID {
		return nil, domainurl.ErrNotFound
	}
	if u.DeletedAt != nil {
		return nil, domainurl.ErrDeleted
	}
	return u, nil
}

func (r *fakeURLRepo) Update(_ context.Context, u *domainurl.URL) error {
	r.updateCalls++
	if r.forceError {
		return errors.New("db: error")
	}
	existing, ok := r.urls[u.ID]
	if !ok || existing.WorkspaceID != u.WorkspaceID {
		return domainurl.ErrNotFound
	}
	r.urls[u.ID] = u
	return nil
}

func (r *fakeURLRepo) SoftDelete(_ context.Context, id, workspaceID string) error {
	if r.forceError {
		return errors.New("db: error")
	}
	u, ok := r.urls[id]
	if !ok || u.WorkspaceID != workspaceID {
		return domainurl.ErrNotFound
	}
	now := time.Now()
	u.DeletedAt = &now
	r.deletedIDs = append(r.deletedIDs, id)
	return nil
}

func (r *fakeURLRepo) List(_ context.Context, filter domainurl.ListFilter) ([]*domainurl.URL, string, error) {
	if r.forceError {
		return nil, "", errors.New("db: error")
	}
	var results []*domainurl.URL
	for _, u := range r.urls {
		if u.WorkspaceID == filter.WorkspaceID && u.DeletedAt == nil {
			results = append(results, u)
		}
	}
	return results, "", nil
}

func (r *fakeURLRepo) IncrementClickCount(_ context.Context, shortCode string) error {
	r.clickIncrements = append(r.clickIncrements, shortCode)
	return nil
}

// fakeCache for cache invalidation testing
type fakeURLCache struct {
	deletedCodes []string
	forceError   bool
}

func (c *fakeURLCache) Get(_ context.Context, _ string) (*domainurl.URL, error) { return nil, nil }
func (c *fakeURLCache) Set(_ context.Context, _ *domainurl.URL, _ int) error    { return nil }
func (c *fakeURLCache) SetNotFound(_ context.Context, _ string, _ int) error    { return nil }
func (c *fakeURLCache) IsNotFound(_ context.Context, _ string) (bool, error)    { return false, nil }
func (c *fakeURLCache) Delete(_ context.Context, code string) error {
	if c.forceError {
		return errors.New("redis: error")
	}
	c.deletedCodes = append(c.deletedCodes, code)
	return nil
}

// newTestURL creates a URL entity for tests
func newTestURL(id, workspaceID, shortCode string) *domainurl.URL {
	return &domainurl.URL{
		ID:          id,
		WorkspaceID: workspaceID,
		ShortCode:   shortCode,
		OriginalURL: "https://example.com/" + shortCode,
		Title:       "Test URL",
		Status:      domainurl.StatusActive,
		CreatedBy:   "usr_test",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
		ClickCount:  0,
	}
}

// ── GetHandler tests ───────────────────────────────────────────────────────────

func TestGetHandler_Handle_Success(t *testing.T) {
	repo := newFakeURLRepo()
	u := newTestURL("url_001", "ws_001", "abc1234")
	repo.seed(u)

	h := appurl.NewGetHandler(repo, "https://s.example.com")

	result, err := h.Handle(context.Background(), appurl.GetQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "url_001" {
		t.Errorf("expected ID=url_001, got %q", result.ID)
	}
	if result.ShortURL != "https://s.example.com/abc1234" {
		t.Errorf("expected ShortURL with base, got %q", result.ShortURL)
	}
	if result.ShortCode != "abc1234" {
		t.Errorf("expected ShortCode=abc1234, got %q", result.ShortCode)
	}
	if result.Status != "active" {
		t.Errorf("expected Status=active, got %q", result.Status)
	}
}

func TestGetHandler_Handle_NotFound(t *testing.T) {
	repo := newFakeURLRepo()
	h := appurl.NewGetHandler(repo, "https://s.example.com")

	_, err := h.Handle(context.Background(), appurl.GetQuery{
		URLID:       "nonexistent",
		WorkspaceID: "ws_001",
	})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestGetHandler_Handle_WrongWorkspace_ReturnsNotFound(t *testing.T) {
	repo := newFakeURLRepo()
	u := newTestURL("url_001", "ws_owner", "abc1234")
	repo.seed(u)

	h := appurl.NewGetHandler(repo, "https://s.example.com")

	_, err := h.Handle(context.Background(), appurl.GetQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_attacker", // different workspace
	})

	// Must return ErrNotFound — not ErrUnauthorized
	// (no information leakage about whether the resource exists)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-workspace access, got: %v", err)
	}
}

func TestGetHandler_Handle_DBError_ReturnsError(t *testing.T) {
	repo := newFakeURLRepo()
	repo.forceError = true
	h := appurl.NewGetHandler(repo, "https://s.example.com")

	_, err := h.Handle(context.Background(), appurl.GetQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
	})

	if err == nil {
		t.Error("expected error when DB fails")
	}
	if errors.Is(err, apperrors.ErrNotFound) {
		t.Error("DB error must not be wrapped as ErrNotFound")
	}
}
