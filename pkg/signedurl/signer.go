package signedurl

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidToken = errors.New("signedurl: token is invalid or expired")

type Signer struct {
	secret []byte
}

func New(secret []byte) (*Signer, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("signedurl: secret must be at least 16 bytes, got %d", len(secret))
	}
	return &Signer{secret: secret}, nil
}

func NewRandom() (*Signer, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("signedurl: generating random secret: %w", err)
	}
	return &Signer{secret: secret}, nil
}

func NewFromHex(hexSecret string) (*Signer, error) {
	secret, err := hex.DecodeString(hexSecret)
	if err != nil {
		return nil, fmt.Errorf("signedurl: decoding hex secret: %w", err)
	}
	return New(secret)
}

func (s *Signer) Sign(exportID string, expiresAt time.Time) string {
	payload := exportID + "|" + strconv.FormatInt(expiresAt.Unix(), 10)
	return base64.RawURLEncoding.EncodeToString(s.computeHMAC(payload))
}

func (s *Signer) Verify(exportID, token string, expiresAt time.Time) error {
	if time.Now().UTC().After(expiresAt) {
		return ErrInvalidToken
	}
	payload := exportID + "|" + strconv.FormatInt(expiresAt.Unix(), 10)
	expected := base64.RawURLEncoding.EncodeToString(s.computeHMAC(payload))
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return ErrInvalidToken
	}
	return nil
}

func BuildDownloadURL(baseURL, exportID, token string) string {
	return fmt.Sprintf("%s/api/v1/exports/%s/download?token=%s", strings.TrimRight(baseURL, "/"), exportID, token)
}

func (s *Signer) computeHMAC(payload string) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
