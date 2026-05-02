package url_test

import (
	"testing"
	"time"

	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

func TestURL_IsExpired(t *testing.T) {
	t.Run("nil expiry never expires", func(t *testing.T) {
		u := &domainurl.URL{ExpiresAt: nil}
		if u.IsExpired() {
			t.Error("expected IsExpired()=false for nil ExpiresAt")
		}
	})

	t.Run("future expiry is not expired", func(t *testing.T) {
		future := time.Now().Add(24 * time.Hour)
		u := &domainurl.URL{ExpiresAt: &future}
		if u.IsExpired() {
			t.Error("expected IsExpired()=false for future ExpiresAt")
		}
	})

	t.Run("past expiry is expired", func(t *testing.T) {
		past := time.Now().Add(-1 * time.Hour)
		u := &domainurl.URL{ExpiresAt: &past}
		if !u.IsExpired() {
			t.Error("expected IsExpired()=true for past ExpiresAt")
		}
	})
}

func TestURL_CanRedirect(t *testing.T) {
	t.Run("active URL with no expiry can redirect", func(t *testing.T) {
		u := &domainurl.URL{Status: domainurl.StatusActive, ExpiresAt: nil}
		if !u.CanRedirect() {
			t.Error("expected CanRedirect()=true for active URL with no expiry")
		}
	})

	t.Run("active URL with future expiry can redirect", func(t *testing.T) {
		future := time.Now().Add(time.Hour)
		u := &domainurl.URL{Status: domainurl.StatusActive, ExpiresAt: &future}
		if !u.CanRedirect() {
			t.Error("expected CanRedirect()=true for active URL with future expiry")
		}
	})

	t.Run("active URL with past expiry cannot redirect", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		u := &domainurl.URL{Status: domainurl.StatusActive, ExpiresAt: &past}
		if u.CanRedirect() {
			t.Error("expected CanRedirect()=false for active URL with past expiry")
		}
	})

	t.Run("disabled URL cannot redirect", func(t *testing.T) {
		u := &domainurl.URL{Status: domainurl.StatusDisabled, ExpiresAt: nil}
		if u.CanRedirect() {
			t.Error("expected CanRedirect()=false for disabled URL")
		}
	})

	t.Run("deleted URL cannot redirect", func(t *testing.T) {
		u := &domainurl.URL{Status: domainurl.StatusDeleted, ExpiresAt: nil}
		if u.CanRedirect() {
			t.Error("expected CanRedirect()=false for deleted URL")
		}
	})
}

func TestURL_Validate(t *testing.T) {
	validURL := &domainurl.URL{
		ShortCode:   "abc1234",
		OriginalURL: "https://example.com/path?query=1",
		WorkspaceID: "01HXYZ",
	}

	t.Run("valid URL passes validation", func(t *testing.T) {
		if err := validURL.Validate(); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("missing short code fails", func(t *testing.T) {
		u := *validURL
		u.ShortCode = ""
		if err := u.Validate(); err == nil {
			t.Error("expected error for empty ShortCode")
		}
	})

	t.Run("missing original URL fails", func(t *testing.T) {
		u := *validURL
		u.OriginalURL = ""
		if err := u.Validate(); err == nil {
			t.Error("expected error for empty OriginalURL")
		}
	})

	t.Run("invalid URL fails", func(t *testing.T) {
		u := *validURL
		u.OriginalURL = "not-a-url"
		if err := u.Validate(); err == nil {
			t.Error("expected error for invalid OriginalURL")
		}
	})

	t.Run("missing workspace ID fails", func(t *testing.T) {
		u := *validURL
		u.WorkspaceID = ""
		if err := u.Validate(); err == nil {
			t.Error("expected error for empty WorkspaceID")
		}
	})

	t.Run("short code too short fails", func(t *testing.T) {
		u := *validURL
		u.ShortCode = "ab" // less than MinShortCodeLength (3)
		if err := u.Validate(); err == nil {
			t.Error("expected error for short code below minimum length")
		}
	})
}
