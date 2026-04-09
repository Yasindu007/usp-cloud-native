package webhooksig_test

import (
	"strings"
	"testing"

	"github.com/urlshortener/platform/pkg/webhooksig"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	secret, err := webhooksig.GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	payload := []byte(`{"event":"url.created","id":"01TEST"}`)
	sig := webhooksig.Sign(secret, payload)
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("expected sha256 prefix, got %q", sig)
	}
	if !webhooksig.Verify(secret, payload, sig) {
		t.Fatal("expected signature verification to succeed")
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	sig := webhooksig.Sign("secret", []byte(`{"event":"url.created"}`))
	if webhooksig.Verify("secret", []byte(`{"event":"url.deleted"}`), sig) {
		t.Fatal("expected tampered payload to fail verification")
	}
}
