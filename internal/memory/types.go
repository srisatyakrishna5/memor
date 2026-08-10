package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/constants"
)

// Type represents the memory classification based on cognitive memory model.
type Type string

const (
	TypeSemantic   Type = "s" // Facts, decisions, architecture
	TypeEpisodic   Type = "e" // Events, bugs fixed, migrations
	TypeProcedural Type = "p" // How-to, commands, workflows
	TypePreference Type = "f" // Developer style preferences
	TypeCode       Type = "c" // Code file summaries
)

// ParseType converts a string to a Type, returning empty string if invalid.
func ParseType(s string) Type {
	switch s {
	case "s", "semantic":
		return TypeSemantic
	case "e", "episodic":
		return TypeEpisodic
	case "p", "procedural":
		return TypeProcedural
	case "f", "preference":
		return TypePreference
	case "c", "code":
		return TypeCode
	default:
		return ""
	}
}

// Prefix returns the compact DSL prefix for this type.
func (t Type) Prefix() string {
	switch t {
	case TypeSemantic:
		return "@s"
	case TypeEpisodic:
		return "@e"
	case TypeProcedural:
		return "@p"
	case TypePreference:
		return "@f"
	case TypeCode:
		return "@c"
	default:
		return "@s"
	}
}

// FullName returns the human-readable name of the type.
func (t Type) FullName() string {
	switch t {
	case TypeSemantic:
		return "semantic"
	case TypeEpisodic:
		return "episodic"
	case TypeProcedural:
		return "procedural"
	case TypePreference:
		return "preference"
	case TypeCode:
		return "code"
	default:
		return "unknown"
	}
}

// SortOrder returns the ordering index for snapshot rendering.
// @s=0, @p=1, @e=2, @f=3, @c=4
func (t Type) SortOrder() int {
	switch t {
	case TypeSemantic:
		return 0
	case TypeProcedural:
		return 1
	case TypeEpisodic:
		return 2
	case TypePreference:
		return 3
	case TypeCode:
		return 4
	default:
		return 99
	}
}

// Entry represents a single memory entry in the WAL or snapshot.
type Entry struct {
	Timestamp  int64     `json:"t"`
	Type       Type      `json:"y"`
	ID         string    `json:"id"`
	Tags       []string  `json:"tags"`
	Content    string    `json:"c"`
	Author     string    `json:"a,omitempty"`
	Session    string    `json:"s,omitempty"`
	Expires    int64     `json:"x,omitempty"`
	Supersedes string    `json:"sup,omitempty"`
	Meta       *CodeMeta `json:"meta,omitempty"`
}

// CodeMeta holds structured metadata for @c (code) entries.
type CodeMeta struct {
	FilePath string   `json:"file"`
	LOC      int      `json:"loc"`
	Hash     string   `json:"hash"`
	Exports  []string `json:"exports,omitempty"`
	Deps     []string `json:"deps,omitempty"`
	Summary  string   `json:"summary"`
	Patterns string   `json:"patterns,omitempty"`
	Logic    string   `json:"logic,omitempty"`
}

// IsExpired returns true if the entry has a set expiry that has passed.
func (e *Entry) IsExpired() bool {
	if e.Expires <= 0 {
		return false
	}
	return time.Now().Unix() > e.Expires
}

// AgeDays returns the number of days since the entry was created.
func (e *Entry) AgeDays() float64 {
	return time.Since(time.Unix(e.Timestamp, 0)).Hours() / 24
}

// ContentID computes the content-addressed ID: sha256(normalized_content)[:12].
func ContentID(content string) string {
	normalized := strings.ToLower(strings.TrimSpace(content))
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])[:constants.ContentIDLength]
}

// ScoredEntry wraps an Entry with a computed relevance score.
type ScoredEntry struct {
	Entry
	Score float64
}
