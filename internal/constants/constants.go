// Package constants defines all shared constants used across memor.
// Centralizing constants prevents drift between config defaults and tests.
package constants

// Storage and budget defaults.
const (
	DefaultTokenBudget    = 15000
	DefaultLogMaxRecords  = 64
	DefaultMaxMemoryNodes = 2000
	DefaultBlobCacheBytes = 262144 // 256 KB hard cap on the body cache
)

// Memory type weights used when scoring memory nodes for retention and ranking.
const (
	WeightPreference = 1.0
	WeightSemantic   = 0.9
	WeightProcedural = 0.8
	WeightEpisodic   = 0.5
)

// Structural node weights. Files outrank the symbols inside them so a broad
// question lands on the file before it fans out to every function in it.
// Documents sit below both: prose describes intent, code answers "where".
const (
	WeightFile = 0.85
	WeightSym  = 0.7
	WeightDoc  = 0.6
	WeightPkg  = 0.5
)

// Time decay applied to memory nodes.
const (
	DefaultDecayRate     = 0.03
	DefaultDecayMinScore = 0.1
)

// Retrieval scoring weights. They sum to 1.0.
const (
	ScoreBM25    = 0.45
	ScoreChanged = 0.20
	ScoreTag     = 0.20
	ScoreRecency = 0.15
)

// Retrieval shape.
const (
	// DefaultMinScore is the precision floor. Returning nothing beats returning
	// a plausible-but-wrong node: a single distractor measurably degrades model
	// output, and four compound it.
	DefaultMinScore = 0.12
)

// Token budgets. The brief is what an agent reads first in every session, so it
// is capped hard enough that reading it is never the expensive choice.
const (
	DefaultBriefBudget   = 600
	DefaultChangesBudget = 800
	DefaultMapBudget     = 2500
)

// Content hashing.
const (
	NodeIDLength   = 12 // hex chars from SHA-256
	FileHashLength = 6  // hex chars for file change detection
)

// StaleRebuildRatio is the fraction of drifted file nodes above which callers
// should rebuild. A stale graph is worse than no graph.
const StaleRebuildRatio = 0.25
