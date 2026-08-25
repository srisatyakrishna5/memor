package engine

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"

	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
)

// Freshness values reported by CodeStatus.
const (
	CodeFresh   = "fresh"
	CodeStale   = "stale"
	CodeMissing = "missing"
)

// CodeEntries returns all @c entries from the snapshot and WAL, deduplicated by
// file path with WAL entries winning, sorted by path.
func CodeEntries(paths store.Paths) ([]memory.Entry, error) {
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return nil, err
	}

	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return nil, err
	}

	byPath := make(map[string]memory.Entry)
	for _, e := range snap.Entries {
		if e.Type == memory.TypeCode && e.Meta != nil {
			byPath[e.Meta.FilePath] = e
		}
	}
	for _, e := range walEntries {
		if e.Type == memory.TypeCode && e.Meta != nil {
			byPath[e.Meta.FilePath] = e
		}
	}

	result := make([]memory.Entry, 0, len(byPath))
	for _, e := range byPath {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Meta.FilePath < result[j].Meta.FilePath
	})
	return result, nil
}

// FindCodeEntry returns the code entry for an exact file path.
func FindCodeEntry(paths store.Paths, filePath string) (*memory.Entry, error) {
	entries, err := CodeEntries(paths)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Meta.FilePath == filePath {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// FileHashAndLOC computes SHA-256[:6] and the line count for a file.
func FileHashAndLOC(path string) (string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}

	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])[:constants.FileHashLength]

	loc := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		loc++
	}

	return hashStr, loc, nil
}

// CodeStatus compares a stored summary against the file on disk so callers can
// tell whether a cached summary is still safe to trust.
func CodeStatus(projectRoot string, meta *memory.CodeMeta) string {
	if meta == nil {
		return CodeMissing
	}
	currentHash, _, err := FileHashAndLOC(filepath.Join(projectRoot, meta.FilePath))
	switch {
	case err != nil:
		return CodeMissing
	case currentHash != meta.Hash:
		return CodeStale
	default:
		return CodeFresh
	}
}
