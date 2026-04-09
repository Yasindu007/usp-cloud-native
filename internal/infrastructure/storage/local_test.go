package storage_test

import (
	"io"
	"testing"

	"github.com/urlshortener/platform/internal/infrastructure/storage"
)

func TestLocalStorageWriteReadDelete(t *testing.T) {
	s, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStorage failed: %v", err)
	}
	w, err := s.Writer("exp_001", ".csv")
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}
	if _, err := io.WriteString(w, "abc"); err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}
	_ = w.Close()

	r, err := s.Reader("exp_001", ".csv")
	if err != nil {
		t.Fatalf("Reader failed: %v", err)
	}
	data, _ := io.ReadAll(r)
	_ = r.Close()
	if string(data) != "abc" {
		t.Fatalf("expected %q, got %q", "abc", string(data))
	}

	if err := s.Delete("exp_001", ".csv"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := s.Reader("exp_001", ".csv"); err != storage.ErrFileNotFound {
		t.Fatalf("expected ErrFileNotFound, got %v", err)
	}
}
