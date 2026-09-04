package config

import (
	"os"
	"path/filepath"

	"github.com/memor-dev/memor/internal/constants"
	toml "github.com/pelletier/go-toml/v2"
)

// Config represents the full config.toml structure.
type Config struct {
	Memory    MemoryConfig    `toml:"memory"`
	Graph     GraphConfig     `toml:"graph"`
	Retrieval RetrievalConfig `toml:"retrieval"`
	Knowledge KnowledgeConfig `toml:"knowledge"`
}

// MemoryConfig holds budget, retention, and compaction-trigger settings.
type MemoryConfig struct {
	TokenBudget    int         `toml:"token_budget"`
	LogMaxRecords  int         `toml:"log_max_records"`
	MaxMemoryNodes int         `toml:"max_memory_nodes"`
	TypeWeights    TypeWeights `toml:"type_weights"`
	Decay          DecayConfig `toml:"decay"`
}

// TypeWeights maps memory subtypes to their retention multipliers.
type TypeWeights struct {
	Preference float64 `toml:"preference"`
	Semantic   float64 `toml:"semantic"`
	Procedural float64 `toml:"procedural"`
	Episodic   float64 `toml:"episodic"`
}

// DecayConfig controls time-based decay for memory nodes.
type DecayConfig struct {
	Rate     float64 `toml:"rate"`
	MinScore float64 `toml:"min_score"`
}

// GraphConfig controls structural extraction.
type GraphConfig struct {
	// Enabled is the kill switch. With it off, memor stores and retrieves
	// agent-authored memories only and performs no repository extraction.
	Enabled     bool        `toml:"enabled"`
	Symbols     bool        `toml:"symbols"`
	MaxFileKB   int         `toml:"max_file_kb"`
	Exclude     []string    `toml:"exclude"`
	Extensions  []string    `toml:"extensions"`
	Cache       CacheConfig `toml:"cache"`
	AutoRebuild bool        `toml:"auto_rebuild"`
}

// CacheConfig bounds the lazily populated body cache under .memor/blobs/.
type CacheConfig struct {
	Enabled  bool   `toml:"enabled"`
	MaxBytes int64  `toml:"max_bytes"`
	Policy   string `toml:"policy"`
}

// RetrievalConfig tunes the single ranking pipeline.
type RetrievalConfig struct {
	MaxHops    int     `toml:"max_hops"`
	MinScore   float64 `toml:"min_score"`
	BM25       float64 `toml:"w_bm25"`
	Proximity  float64 `toml:"w_proximity"`
	Rank       float64 `toml:"w_rank"`
	Tag        float64 `toml:"w_tag"`
	Recency    float64 `toml:"w_recency"`
	MaxSymbols int     `toml:"max_symbols_per_file"`
}

// KnowledgeConfig controls which prose documents are indexed as KindDoc nodes.
type KnowledgeConfig struct {
	Enabled   bool     `toml:"enabled"`
	ScanPaths []string `toml:"scan_paths"`
}

// Default returns a Config with sane defaults matching the ADR.
func Default() Config {
	return Config{
		Memory: MemoryConfig{
			TokenBudget:    constants.DefaultTokenBudget,
			LogMaxRecords:  constants.DefaultLogMaxRecords,
			MaxMemoryNodes: constants.DefaultMaxMemoryNodes,
			TypeWeights: TypeWeights{
				Preference: constants.WeightPreference,
				Semantic:   constants.WeightSemantic,
				Procedural: constants.WeightProcedural,
				Episodic:   constants.WeightEpisodic,
			},
			Decay: DecayConfig{
				Rate:     constants.DefaultDecayRate,
				MinScore: constants.DefaultDecayMinScore,
			},
		},
		Graph: GraphConfig{
			Enabled:     true,
			Symbols:     true,
			MaxFileKB:   512,
			AutoRebuild: true,
			Exclude: []string{
				".git", ".memor", "node_modules", "vendor", "dist", "build", "out",
				"target", ".venv", "venv", "__pycache__", ".next", ".nuxt", ".turbo",
				"coverage", "bin", "obj", ".idea", ".vscode-test",
			},
			Extensions: []string{
				".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".py",
				".java", ".cs", ".rs", ".rb", ".php", ".kt", ".swift", ".c",
				".h", ".cc", ".cpp", ".hpp",
			},
			Cache: CacheConfig{
				Enabled:  true,
				MaxBytes: constants.DefaultBlobCacheBytes,
				Policy:   "lru",
			},
		},
		Retrieval: RetrievalConfig{
			MaxHops:    constants.DefaultMaxHops,
			MinScore:   constants.DefaultMinScore,
			BM25:       constants.ScoreBM25,
			Proximity:  constants.ScoreProximity,
			Rank:       constants.ScoreRank,
			Tag:        constants.ScoreTag,
			Recency:    constants.ScoreRecency,
			MaxSymbols: 8,
		},
		Knowledge: KnowledgeConfig{
			Enabled: true,
			ScanPaths: []string{
				"README.md",
				"CONTRIBUTING.md",
				"AGENTS.md",
				"CLAUDE.md",
				"docs/**/*.md",
				".github/**/*.md",
				"**/*.instructions.md",
				"**/SKILL.md",
			},
		},
	}
}

// Load reads config from a TOML file, falling back to defaults for missing
// fields. A missing file is not an error: memor must work before `memor init`.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	return cfg.normalized(), nil
}

// normalized replaces zero values a partial config.toml leaves behind. Without
// this an author who sets only token_budget silently gets a zero min_score and
// loses the precision gate.
func (c Config) normalized() Config {
	d := Default()
	if c.Memory.TokenBudget <= 0 {
		c.Memory.TokenBudget = d.Memory.TokenBudget
	}
	if c.Memory.LogMaxRecords <= 0 {
		c.Memory.LogMaxRecords = d.Memory.LogMaxRecords
	}
	if c.Memory.MaxMemoryNodes <= 0 {
		c.Memory.MaxMemoryNodes = d.Memory.MaxMemoryNodes
	}
	if c.Memory.Decay.Rate <= 0 {
		c.Memory.Decay.Rate = d.Memory.Decay.Rate
	}
	if c.Memory.Decay.MinScore <= 0 {
		c.Memory.Decay.MinScore = d.Memory.Decay.MinScore
	}
	if c.Memory.TypeWeights == (TypeWeights{}) {
		c.Memory.TypeWeights = d.Memory.TypeWeights
	}
	if c.Retrieval.MaxHops <= 0 {
		c.Retrieval.MaxHops = d.Retrieval.MaxHops
	}
	if c.Retrieval.MinScore <= 0 {
		c.Retrieval.MinScore = d.Retrieval.MinScore
	}
	if c.Retrieval.MaxSymbols <= 0 {
		c.Retrieval.MaxSymbols = d.Retrieval.MaxSymbols
	}
	if c.Retrieval.BM25+c.Retrieval.Proximity+c.Retrieval.Rank+c.Retrieval.Tag+c.Retrieval.Recency <= 0 {
		c.Retrieval.BM25 = d.Retrieval.BM25
		c.Retrieval.Proximity = d.Retrieval.Proximity
		c.Retrieval.Rank = d.Retrieval.Rank
		c.Retrieval.Tag = d.Retrieval.Tag
		c.Retrieval.Recency = d.Retrieval.Recency
	}
	if len(c.Graph.Exclude) == 0 {
		c.Graph.Exclude = d.Graph.Exclude
	}
	if len(c.Graph.Extensions) == 0 {
		c.Graph.Extensions = d.Graph.Extensions
	}
	if c.Graph.MaxFileKB <= 0 {
		c.Graph.MaxFileKB = d.Graph.MaxFileKB
	}
	if c.Graph.Cache.MaxBytes <= 0 {
		c.Graph.Cache.MaxBytes = d.Graph.Cache.MaxBytes
	}
	if len(c.Knowledge.ScanPaths) == 0 {
		c.Knowledge.ScanPaths = d.Knowledge.ScanPaths
	}
	return c
}

// Save writes the config to a TOML file.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// TypeWeight returns the retention weight for a memory subtype code.
func (c *Config) TypeWeight(t string) float64 {
	switch t {
	case "s", "semantic":
		return c.Memory.TypeWeights.Semantic
	case "e", "episodic":
		return c.Memory.TypeWeights.Episodic
	case "p", "procedural":
		return c.Memory.TypeWeights.Procedural
	case "f", "preference":
		return c.Memory.TypeWeights.Preference
	default:
		return 0.5
	}
}
