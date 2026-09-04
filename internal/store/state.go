package store

import (
	"encoding/json"
	"os"
)

// StateSchema is bumped when state.json gains a field a previous binary would
// misread. A mismatch is treated as "not indexed" rather than an error.
const StateSchema = 3

// State records what the last successful build saw. It is the anchor for every
// "what changed since then?" question, and the only file memor rewrites in
// full outside of compaction.
type State struct {
	Schema        int    `json:"schema"`
	IndexedCommit string `json:"indexed_commit,omitempty"`
	IndexedAt     int64  `json:"indexed_at,omitempty"`
	FileCount     int    `json:"file_count,omitempty"`
	SymbolCount   int    `json:"symbol_count,omitempty"`
}

// ReadState loads state.json. A missing, unreadable or future-schema file
// yields a zero State, which every caller already handles as "never built".
func ReadState(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var s State
	if json.Unmarshal(data, &s) != nil || s.Schema != StateSchema {
		return State{}
	}
	return s
}

// WriteState persists state.json atomically.
func WriteState(path string, s State) error {
	s.Schema = StateSchema
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

// StateModTime reports when state.json last changed, used to invalidate a
// cached in-memory graph without re-reading it.
func StateModTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}

// FileSize returns a file's size, or 0 if it does not exist. graph.log only
// grows between compactions, so its size doubles as a cheap write counter.
func FileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
