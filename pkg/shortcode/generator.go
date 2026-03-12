// Package shortcode generates URL-safe, Base62-encoded short codes.
//
// Design requirements (from PRD):
//   - Default length: 7 characters
//   - Character set: Base62 [a-zA-Z0-9] — no special chars, URL-safe
//   - Uniqueness: cryptographically random (not sequential) to prevent
//     enumeration attacks (a user cannot guess adjacent short codes)
//   - Collision resistance: at 7 chars, Base62 gives 62^7 ≈ 3.5 trillion
//     combinations, making collisions negligible at launch scale
//
// Why not sequential IDs?
//   Sequential short codes (abc0001, abc0002) are trivially enumerable.
//   An attacker could scrape all URLs in a workspace. Random Base62 codes
//   require brute-forcing 3.5T possibilities — effectively infeasible.
//
// Why not UUIDs directly?
//   A UUID is 36 chars. We want 7. Base62 encoding of random bytes
//   gives us short, URL-safe codes at the length we control.
package shortcode

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	// alphabet is the Base62 character set.
	// Only alphanumeric characters — no hyphens, underscores, or special chars.
	// This ensures short codes are safe in URLs, QR codes, and printed media.
	alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// alphabetLen is the size of the Base62 alphabet.
	alphabetLen = int64(len(alphabet))

	// DefaultLength is the default short code length per PRD section 5.1.1.
	DefaultLength = 7
)

// Generator generates short codes of a configurable length.
type Generator struct {
	length int
}

// New creates a Generator with the specified code length.
// Panics if length is less than MinShortCodeLength to catch
// misconfiguration at startup rather than at request time.
func New(length int) *Generator {
	if length < 3 {
		panic("shortcode: length must be at least 3")
	}
	return &Generator{length: length}
}

// NewDefault creates a Generator with the PRD-specified default length of 7.
func NewDefault() *Generator {
	return New(DefaultLength)
}

// Generate produces a cryptographically random Base62 short code.
//
// Uses crypto/rand (not math/rand) because:
//   1. Predictability attack: math/rand is seeded deterministically;
//      an attacker who observes enough codes can predict future ones.
//   2. Enumeration attack: even with random seeding, math/rand's PRNG
//      has statistical patterns exploitable by adversaries.
//   3. crypto/rand uses the OS CSPRNG (Linux: getrandom syscall / /dev/urandom)
//      which provides true cryptographic randomness.
//
// Performance note: crypto/rand is ~10x slower than math/rand but still
// generates millions of codes per second — not a bottleneck at 10k RPS.
func (g *Generator) Generate() (string, error) {
	result := make([]byte, g.length)
	alphabetBig := big.NewInt(alphabetLen)

	for i := range result {
		n, err := rand.Int(rand.Reader, alphabetBig)
		if err != nil {
			return "", fmt.Errorf("shortcode: generating random byte: %w", err)
		}
		result[i] = alphabet[n.Int64()]
	}

	return string(result), nil
}

// MustGenerate generates a short code and panics on error.
// Use only in tests or initialization code — not in request handlers.
func (g *Generator) MustGenerate() string {
	code, err := g.Generate()
	if err != nil {
		panic(err)
	}
	return code
}