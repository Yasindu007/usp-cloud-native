package url_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appurl "github.com/urlshortener/platform/internal/application/url"
)

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

func newUpdateHandler(repo *fakeURLRepo, cache *fakeURLCache) *appurl.UpdateHandler {
	return appurl.NewUpdateHandler(repo, cache, "https://s.example.com", testLog)
}

func strPtr(s string) *string { return &s }

func TestUpdateHandler_Handle_UpdatesOriginalURL(t *testing.T) {
	repo := newFakeURLRepo()
	cache := &fakeURLCache{}
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newUpdateHandler(repo, cache)

	newURL := "https://updated.example.com/new-path"
	result, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		OriginalURL: &newURL,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OriginalURL != newURL {
		t.Errorf("expected updated URL %q, got %q", newURL, result.OriginalURL)
	}
}

func TestUpdateHandler_Handle_UpdatesTitle(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newUpdateHandler(repo, &fakeURLCache{})

	newTitle := "My Updated Title"
	result, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Title:       &newTitle,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Title != "My Updated Title" {
		t.Errorf("expected Title=%q, got %q", newTitle, result.Title)
	}
}

func TestUpdateHandler_Handle_InvalidatesCache(t *testing.T) {
	repo := newFakeURLRepo()
	cache := &fakeURLCache{}
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newUpdateHandler(repo, cache)

	newURL := "https://updated.example.com"
	_, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		OriginalURL: &newURL,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cache.deletedCodes) == 0 {
		t.Error("expected cache.Delete to be called after URL update")
	}
	if cache.deletedCodes[0] != "abc1234" {
		t.Errorf("expected cache to be invalidated for short_code=abc1234, got %q",
			cache.deletedCodes[0])
	}
}

func TestUpdateHandler_Handle_CacheFailure_IsNonFatal(t *testing.T) {
	repo := newFakeURLRepo()
	cache := &fakeURLCache{forceError: true}
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newUpdateHandler(repo, cache)

	newURL := "https://updated.example.com"
	result, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		OriginalURL: &newURL,
	})

	// Update must succeed even if cache invalidation fails
	if err != nil {
		t.Fatalf("expected success despite cache error, got: %v", err)
	}
	if result.OriginalURL != newURL {
		t.Error("update did not persist despite cache error")
	}
}

func TestUpdateHandler_Handle_NilFields_NoChangeApplied(t *testing.T) {
	repo := newFakeURLRepo()
	u := newTestURL("url_001", "ws_001", "abc1234")
	u.Title = "Original Title"
	u.OriginalURL = "https://original.example.com"
	repo.seed(u)
	h := newUpdateHandler(repo, &fakeURLCache{})

	// All fields nil = no changes
	result, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		// OriginalURL, Title, ExpiresAt all nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Title != "Original Title" {
		t.Errorf("expected title unchanged, got %q", result.Title)
	}
	if result.OriginalURL != "https://original.example.com" {
		t.Errorf("expected original_url unchanged, got %q", result.OriginalURL)
	}
}

func TestUpdateHandler_Handle_NotFound_ReturnsErrNotFound(t *testing.T) {
	repo := newFakeURLRepo()
	h := newUpdateHandler(repo, &fakeURLCache{})

	newURL := "https://updated.example.com"
	_, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "nonexistent",
		WorkspaceID: "ws_001",
		OriginalURL: &newURL,
	})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpdateHandler_Handle_WrongWorkspace_ReturnsNotFound(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_owner", "abc1234"))
	h := newUpdateHandler(repo, &fakeURLCache{})

	newURL := "https://updated.example.com"
	_, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_attacker",
		OriginalURL: &newURL,
	})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-workspace update, got: %v", err)
	}
}

func TestUpdateHandler_Handle_InvalidURL_ReturnsValidationError(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newUpdateHandler(repo, &fakeURLCache{})

	for _, badURL := range []string{"not-a-url", "ftp://bad-scheme.com", ""} {
		_, err := h.Handle(context.Background(), appurl.UpdateCommand{
			URLID:       "url_001",
			WorkspaceID: "ws_001",
			OriginalURL: strPtr(badURL),
		})
		if !apperrors.IsValidationError(err) {
			t.Errorf("URL %q: expected validation error, got: %v", badURL, err)
		}
	}
}

func TestUpdateHandler_Handle_SetExpiry(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newUpdateHandler(repo, &fakeURLCache{})

	expiry := time.Now().Add(24 * time.Hour)
	expiryPtr := &expiry

	result, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		ExpiresAt:   &expiryPtr,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExpiresAt == nil {
		t.Error("expected ExpiresAt to be set after update")
	}
}

func TestUpdateHandler_Handle_RemoveExpiry(t *testing.T) {
	repo := newFakeURLRepo()
	u := newTestURL("url_001", "ws_001", "abc1234")
	expiry := time.Now().Add(24 * time.Hour)
	u.ExpiresAt = &expiry
	repo.seed(u)
	h := newUpdateHandler(repo, &fakeURLCache{})

	// Double pointer with inner nil = remove expiry
	var nilTime *time.Time // nil
	result, err := h.Handle(context.Background(), appurl.UpdateCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		ExpiresAt:   &nilTime, // outer pointer non-nil = apply change; inner nil = remove
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExpiresAt != nil {
		t.Error("expected ExpiresAt to be nil after removing expiry")
	}
}
