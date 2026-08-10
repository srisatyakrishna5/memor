package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/memor-dev/memor/internal/constants"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.Memory.TokenBudget != constants.DefaultTokenBudget {
		t.Errorf("expected token_budget %d, got %d", constants.DefaultTokenBudget, cfg.Memory.TokenBudget)
	}
	if cfg.Memory.WALMaxEntries != constants.DefaultWALMaxEntries {
		t.Errorf("expected wal_max_entries %d, got %d", constants.DefaultWALMaxEntries, cfg.Memory.WALMaxEntries)
	}
	if cfg.Compaction.TypeWeights.Preference != 1.0 {
		t.Errorf("expected preference weight 1.0, got %f", cfg.Compaction.TypeWeights.Preference)
	}
	if cfg.Knowledge.Enabled != true {
		t.Error("expected knowledge enabled by default")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := Default()
	cfg.Memory.TokenBudget = 5000
	cfg.Memory.WALMaxEntries = 50

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Memory.TokenBudget != 5000 {
		t.Errorf("expected token_budget 5000, got %d", loaded.Memory.TokenBudget)
	}
	if loaded.Memory.WALMaxEntries != 50 {
		t.Errorf("expected wal_max_entries 50, got %d", loaded.Memory.WALMaxEntries)
	}
	if loaded.Compaction.TypeWeights.Semantic != constants.WeightSemantic {
		t.Errorf("expected semantic weight preserved, got %f", loaded.Compaction.TypeWeights.Semantic)
	}
}

func TestLoadNonexistent(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}
	if cfg.Memory.TokenBudget != constants.DefaultTokenBudget {
		t.Errorf("expected default token_budget, got %d", cfg.Memory.TokenBudget)
	}
}

func TestLoadMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(path, []byte("this is not valid toml {{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error on malformed TOML")
	}
}

func TestTypeWeight(t *testing.T) {
	cfg := Default()

	tests := []struct {
		input    string
		expected float64
	}{
		{"s", 0.9},
		{"semantic", 0.9},
		{"e", 0.5},
		{"episodic", 0.5},
		{"p", 0.8},
		{"procedural", 0.8},
		{"f", 1.0},
		{"preference", 1.0},
		{"unknown", 0.5},
	}

	for _, tt := range tests {
		got := cfg.TypeWeight(tt.input)
		if got != tt.expected {
			t.Errorf("TypeWeight(%q) = %f, want %f", tt.input, got, tt.expected)
		}
	}
}
