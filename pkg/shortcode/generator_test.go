package shortcode_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/urlshortener/platform/pkg/shortcode"
)

const testIterations = 1000

func TestGenerator_Generate_Length(t *testing.T) {
	for _, length := range []int{3, 5, 7, 10, 16, 32} {
		g := shortcode.New(length)
		code, err := g.Generate()
		if err != nil {
			t.Fatalf("length=%d: unexpected error: %v", length, err)
		}
		if len(code) != length {
			t.Errorf("length=%d: expected code length %d, got %d (code: %q)", length, length, len(code), code)
		}
	}
}

func TestGenerator_Generate_AlphanumericOnly(t *testing.T) {
	g := shortcode.NewDefault()

	for i := 0; i < testIterations; i++ {
		code, err := g.Generate()
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		for _, r := range code {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				t.Errorf("non-alphanumeric character %q found in code %q", r, code)
			}
		}
	}
}

func TestGenerator_Generate_Uniqueness(t *testing.T) {
	// Statistical test: generate N codes and verify no collisions.
	// At 7 chars Base62 (3.5T combinations), 1000 codes have negligible
	// collision probability. A collision would indicate RNG failure.
	g := shortcode.NewDefault()
	seen := make(map[string]bool, testIterations)

	for i := 0; i < testIterations; i++ {
		code, err := g.Generate()
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if seen[code] {
			t.Errorf("collision detected after %d iterations: duplicate code %q", i, code)
		}
		seen[code] = true
	}
}

func TestGenerator_Generate_NoUpperCaseOnly(t *testing.T) {
	// Verify the generator uses mixed case (full Base62, not Base36).
	// If all generated codes were lowercase, the keyspace would be
	// reduced from 62^7 to 36^7 — a 17x reduction.
	g := shortcode.NewDefault()
	codes := make([]string, testIterations)
	for i := range codes {
		code, _ := g.Generate()
		codes[i] = code
	}

	allLower := true
	for _, code := range codes {
		if code != strings.ToLower(code) {
			allLower = false
			break
		}
	}
	if allLower {
		t.Error("all generated codes were lowercase — expected mixed case Base62")
	}
}

func TestGenerator_New_PanicOnInvalidLength(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for length < 3, got none")
		}
	}()
	shortcode.New(2) // Should panic
}

func TestGenerator_MustGenerate(t *testing.T) {
	g := shortcode.NewDefault()
	code := g.MustGenerate()
	if len(code) != shortcode.DefaultLength {
		t.Errorf("expected length %d, got %d", shortcode.DefaultLength, len(code))
	}
}

// BenchmarkGenerator_Generate measures short code generation throughput.
// Run with: go test -bench=. -benchmem ./pkg/shortcode/
// Target: must not be a bottleneck at 10k RPS (shorten endpoint).
func BenchmarkGenerator_Generate(b *testing.B) {
	g := shortcode.NewDefault()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.Generate()
	}
}