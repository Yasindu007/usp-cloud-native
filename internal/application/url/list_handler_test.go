package url_test

import (
	"context"
	"testing"
	"time"

	appurl "github.com/urlshortener/platform/internal/application/url"
)

func TestListHandler_Handle_ReturnsURLsForWorkspace(t *testing.T) {
	repo := newFakeURLRepo()
	h := appurl.NewListHandler(repo, "https://s.example.com")

	repo.seed(newTestURL("url_001", "ws_001", "aaa1111"))
	repo.seed(newTestURL("url_002", "ws_001", "bbb2222"))
	repo.seed(newTestURL("url_003", "ws_other", "ccc3333")) // other workspace

	result, err := h.Handle(context.Background(), appurl.ListQuery{
		WorkspaceID: "ws_001",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.URLs) != 2 {
		t.Errorf("expected 2 URLs for ws_001, got %d", len(result.URLs))
	}
	for _, u := range result.URLs {
		if u.WorkspaceID != "ws_001" {
			t.Errorf("expected all results from ws_001, got %q", u.WorkspaceID)
		}
	}
}

func TestListHandler_Handle_ShortURLContainsBaseURL(t *testing.T) {
	repo := newFakeURLRepo()
	repo.seed(newTestURL("url_001", "ws_001", "xyz9876"))
	h := appurl.NewListHandler(repo, "https://short.example.com")

	result, err := h.Handle(context.Background(), appurl.ListQuery{WorkspaceID: "ws_001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.URLs) == 0 {
		t.Fatal("expected at least one URL")
	}
	if result.URLs[0].ShortURL != "https://short.example.com/xyz9876" {
		t.Errorf("ShortURL mismatch: got %q", result.URLs[0].ShortURL)
	}
}

func TestListHandler_Handle_EmptyWorkspace_ReturnsEmptySlice(t *testing.T) {
	repo := newFakeURLRepo()
	h := appurl.NewListHandler(repo, "https://s.example.com")

	result, err := h.Handle(context.Background(), appurl.ListQuery{WorkspaceID: "ws_empty"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.URLs) != 0 {
		t.Errorf("expected 0 URLs, got %d", len(result.URLs))
	}
	if result.HasMore {
		t.Error("expected HasMore=false for empty result")
	}
}

func TestListHandler_Handle_DBError_ReturnsError(t *testing.T) {
	repo := newFakeURLRepo()
	repo.forceError = true
	h := appurl.NewListHandler(repo, "https://s.example.com")

	_, err := h.Handle(context.Background(), appurl.ListQuery{WorkspaceID: "ws_001"})
	if err == nil {
		t.Error("expected error when DB fails")
	}
}

func TestListHandler_Handle_DeletedURLsExcluded(t *testing.T) {
	repo := newFakeURLRepo()
	h := appurl.NewListHandler(repo, "https://s.example.com")

	active := newTestURL("url_active", "ws_001", "active1")
	deleted := newTestURL("url_deleted", "ws_001", "deleted1")
	now := time.Now()
	deleted.DeletedAt = &now
	repo.seed(active)
	repo.seed(deleted)

	result, err := h.Handle(context.Background(), appurl.ListQuery{WorkspaceID: "ws_001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, u := range result.URLs {
		if u.ID == "url_deleted" {
			t.Error("deleted URL must not appear in list results")
		}
	}
}
