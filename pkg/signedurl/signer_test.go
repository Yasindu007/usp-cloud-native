package signedurl_test

import (
	"testing"
	"time"

	"github.com/urlshortener/platform/pkg/signedurl"
)

func TestSignerRoundTrip(t *testing.T) {
	signer, err := signedurl.New([]byte("this-is-a-32-byte-secret-for-test"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	expiresAt := time.Now().Add(time.Hour)
	token := signer.Sign("exp_001", expiresAt)
	if err := signer.Verify("exp_001", token, expiresAt); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
}

func TestSignerRejectsWrongID(t *testing.T) {
	signer, _ := signedurl.New([]byte("this-is-a-32-byte-secret-for-test"))
	expiresAt := time.Now().Add(time.Hour)
	token := signer.Sign("exp_001", expiresAt)
	if err := signer.Verify("exp_002", token, expiresAt); err == nil {
		t.Fatal("expected wrong export ID to fail")
	}
}

func TestBuildDownloadURL(t *testing.T) {
	got := signedurl.BuildDownloadURL("http://localhost:8080/", "exp_001", "tok")
	want := "http://localhost:8080/api/v1/exports/exp_001/download?token=tok"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
