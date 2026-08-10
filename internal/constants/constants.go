// Package constants defines all shared constants used across memor.
// Centralizing constants prevents drift between config defaults and tests.
package constants

// Memory defaults
const (
	DefaultTokenBudget   = 15000
	DefaultWALMaxEntries = 2
)

// Compaction weights
const (
	WeightPreference = 1.0
	WeightSemantic   = 0.9
	WeightProcedural = 0.8
	WeightCode       = 0.7
	WeightEpisodic   = 0.5
)

// Decay
const (
	DefaultDecayRate     = 0.03
	DefaultDecayMinScore = 0.1
)

// Content hashing
const (
	ContentIDLength = 12 // hex chars from SHA-256
	FileHashLength  = 6  // hex chars for file change detection
)
