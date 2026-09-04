package tests

import (
	"testing"

	"github.com/memor-dev/memor/internal/token"
)

func TestCount_Empty(t *testing.T) {
	if token.Count("") != 0 {
		t.Error("empty string should be 0 tokens")
	}
}

func TestCount_SingleWord(t *testing.T) {
	c := token.Count("hello")
	if c < 1 {
		t.Errorf("single word should be at least 1 token, got %d", c)
	}
}

func TestCount_ReasonableEstimate(t *testing.T) {
	// "The quick brown fox jumps over the lazy dog" = ~10 tokens in cl100k_base
	text := "The quick brown fox jumps over the lazy dog"
	c := token.Count(text)
	if c < 5 || c > 20 {
		t.Errorf("expected ~10 tokens for standard sentence, got %d", c)
	}
}

func TestCount_Code(t *testing.T) {
	code := `func main() { fmt.Println("hello world") }`
	c := token.Count(code)
	if c < 5 {
		t.Errorf("code snippet should be at least 5 tokens, got %d", c)
	}
}
