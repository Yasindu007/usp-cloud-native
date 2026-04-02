package shortcode

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	alphabet      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	alphabetLen   = int64(len(alphabet))
	DefaultLength = 7
)

type Generator struct {
	length int
}

func New(length int) *Generator {
	if length < 3 {
		panic("shortcode: length must be at least 3")
	}
	return &Generator{length: length}
}

func NewDefault() *Generator {
	return New(DefaultLength)
}

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

func (g *Generator) MustGenerate() string {
	code, err := g.Generate()
	if err != nil {
		panic(err)
	}
	return code
}
