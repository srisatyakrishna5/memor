package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/constants"
)

func TestConfigDefault(t *testing.T) {
	cfg := config.Default()

	if cfg.Memory.TokenBudget != constants.DefaultTokenBudget {
		t.Errorf("expected token_budget %d, got %d", constants.DefaultTokenBudget, cfg.Memory.TokenBudget)
	}
	if cfg.Memory.LogMaxRecords != constants.DefaultLogMaxRecords {
		t.Errorf("expected log_max_records %d, got %d", constants.DefaultLogMaxRecords, cfg.Memory.LogMaxRecords)
	}
	if cfg.Memory.TypeWeights.Preference != constants.WeightPreference {
		t.Errorf("expected preference weight %v, got %v", constants.WeightPreference, cfg.Memory.TypeWeights.Preference)
	}
	if !cfg.Graph.Enabled {
		t.Error("expected graph extraction enabled by default")
	}
	if !cfg.Knowledge.Enabled {
		t.Error("expected knowledge indexing enabled by default")
	}
}

func TestConfigSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	cfg := config.Default()
	cfg.Memory.TokenBudget = 5000
	cfg.Memory.LogMaxRecords = 50
	cfg.Graph.Enabled = false

	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Memory.TokenBudget != 5000 {
		t.Errorf("expected token_budget 5000, got %d", loaded.Memory.TokenBudget)
	}
	if loaded.Memory.LogMaxRecords != 50 {
		t.Errorf("expected log_max_records 50, got %d", loaded.Memory.LogMaxRecords)
	}
	if loaded.Graph.Enabled {
		t.Error("expected the graph kill switch to survive a round trip")
	}
	if loaded.Memory.TypeWeights.Semantic != constants.WeightSemantic {
		t.Errorf("expected semantic weight preserved, got %v", loaded.Memory.TypeWeights.Semantic)
	}
}

func TestConfigLoadNonexistent(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "missing", "config.toml"))
	if err != nil {
		t.Fatalf("Load should not error on a missing file: %v", err)
	}
	if cfg.Memory.TokenBudget != constants.DefaultTokenBudget {
		t.Errorf("expected default token_budget, got %d", cfg.Memory.TokenBudget)
	}
}

func TestConfigLoadMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("this is not valid toml {{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("expected an error on malformed TOML")
	}
}

// A partial config.toml must not silently disable the precision gate: a zero
// min_score would let every scored node through.
func TestConfigPartialKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[memory]\ntoken_budget = 4000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.TokenBudget != 4000 {
		t.Errorf("expected the authored value to win, got %d", cfg.Memory.TokenBudget)
	}
	if cfg.Retrieval.MinScore != constants.DefaultMinScore {
		t.Errorf("expected min_score to fall back to %v, got %v", constants.DefaultMinScore, cfg.Retrieval.MinScore)
	}
	if cfg.Memory.Decay.Rate != constants.DefaultDecayRate {
		t.Errorf("expected decay rate to fall back to %v, got %v", constants.DefaultDecayRate, cfg.Memory.Decay.Rate)
	}
	if len(cfg.Graph.Extensions) == 0 {
		t.Error("expected default extensions to survive a partial config")
	}
}

func TestConfigTypeWeight(t *testing.T) {
	cfg := config.Default()

	cases := map[string]float64{
		"f":          constants.WeightPreference,
		"preference": constants.WeightPreference,
		"s":          constants.WeightSemantic,
		"p":          constants.WeightProcedural,
		"e":          constants.WeightEpisodic,
		"unknown":    0.5,
	}
	for input, want := range cases {
		if got := cfg.TypeWeight(input); got != want {
			t.Errorf("TypeWeight(%q) = %v, want %v", input, got, want)
		}
	}
}
