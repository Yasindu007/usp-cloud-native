package url_test

import (
	"context"
	"errors"
	"testing"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appurl "github.com/urlshortener/platform/internal/application/url"
)

func newDeleteHandler(repo *fakeURLRepo, cache *fakeURLCache) *appurl.DeleteHandler {
	return appurl.NewDeleteHandler(repo, cache, testLog)
}

func TestDeleteHandler_Handle_SoftDeletes(t *testing.T) {
	repo := newFakeURLRepo()
	cache := &fakeURLCache{}
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newDeleteHandler(repo, cache)

	err := h.Handle(context.Background(), appurl.DeleteCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// URL must be soft-deleted (deleted_at set), not hard-deleted
	if len(repo.deletedIDs) == 0 {
		t.Error("expected SoftDelete to be called")
	}
	if repo.deletedIDs[0] != "url_001" {
		t.Errorf("expected url_001 to be deleted, got %q", repo.deletedIDs[0])
	}

	// Row must still exist in repo (soft delete only)
	if _, ok := repo.urls["url_001"]; !ok {
		t.Error("soft delete must not remove row from repository")
	}
}

func TestDeleteHandler_Handle_InvalidatesCache(t *testing.T) {
	repo := newFakeURLRepo()
	cache := &fakeURLCache{}
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newDeleteHandler(repo, cache)

	_ = h.Handle(context.Background(), appurl.DeleteCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
	})

	if len(cache.deletedCodes) == 0 {
		t.Error("expected cache to be invalidated after delete")
	}
	if cache.deletedCodes[0] != "abc1234" {
		t.Errorf("expected cache invalidation for abc1234, got %q", cache.deletedCodes[0])
	}
}

func TestDeleteHandler_Handle_CacheFailure_IsNonFatal(t *testing.T) {
	repo := newFakeURLRepo()
	cache := &fakeURLCache{forceError: true}
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := newDeleteHandler(repo, cache)

	// Must succeed even with cache failure
	err := h.Handle(context.Background(), appurl.DeleteCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
	})

	if err != nil {
		t.Fatalf("expected success despite cache error, got: %v", err)
	}
}

func TestDeleteHandler_Handle_NotFound_ReturnsErrNotFound(t *testing.T) {
	repo := newFakeURLRepo()
	h := newDeleteHandler(repo, &fakeURLCache{})

	err := h.Handle(context.Background(), appurl.DeleteCommand{
		URLID:       "nonexistent",
		WorkspaceID: "ws_001",
	})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestDeleteHandler_Handle_WrongWorkspace_ReturnsNotFound(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_owner", "abc1234"))
	h := newDeleteHandler(repo, &fakeURLCache{})

	err := h.Handle(context.Background(), appurl.DeleteCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_attacker",
	})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-workspace delete, got: %v", err)
	}
}

func TestDeleteHandler_Handle_NilCache_DoesNotPanic(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_001", "abc1234"))
	h := appurl.NewDeleteHandler(repo, nil, testLog) // nil cache

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("DeleteHandler panicked with nil cache: %v", r)
		}
	}()

	err := h.Handle(context.Background(), appurl.DeleteCommand{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
