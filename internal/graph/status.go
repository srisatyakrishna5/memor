package graph

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/store"
)

// Freshness values reported by FileStatus.
const (
	StatusFresh   = "fresh"
	StatusStale   = "stale"
	StatusMissing = "missing"
	StatusUnknown = "unrecorded"
)

// FileHashAndLOC computes SHA-256[:6] and the line count for a file.
func FileHashAndLOC(path string) (string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	return HashBytes(data), countLines(data), nil
}

// HashBytes returns the short content hash used for staleness detection.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:constants.FileHashLength]
}

func countLines(data []byte) int {
	loc := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		loc++
	}
	return loc
}

// FileStatus compares a stored node against the file on disk. A stale graph is
// worse than no graph, so every served span and summary is checked against the
// hash recorded when it was written.
func FileStatus(projectRoot string, n *Node) string {
	if n == nil || n.Span == nil || n.Span.Path == "" {
		return StatusUnknown
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, n.Span.Path))
	switch {
	case err != nil:
		return StatusMissing
	case n.Span.Hash == "":
		return StatusUnknown
	case HashBytes(data) != n.Span.Hash:
		return StatusStale
	default:
		return StatusFresh
	}
}

// Report summarizes the state of a project's store.
type Report struct {
	Nodes         int
	ByKind        map[string]int
	Pending       int
	Fresh         int
	Stale         int
	Missing       int
	StaleRatio    float64
	Bytes         int64
	TokenBudget   int
	BuiltAt       int64
	Tags          []string
	IndexedCommit string
	IndexedAt     int64
}

// Status inspects a loaded graph against the working tree.
func Status(paths store.Paths, projectRoot string, g *Graph, cfg config.Config) (Report, error) {
	pending, err := store.RecordCount(paths.Log)
	if err != nil {
		return Report{}, fmt.Errorf("count log records: %w", err)
	}

	state := store.ReadState(paths.State)
	rep := Report{
		Nodes:         g.NodeCount(),
		ByKind:        g.CountByKind(),
		Pending:       pending,
		Bytes:         paths.FootprintBytes(),
		TokenBudget:   cfg.Memory.TokenBudget,
		BuiltAt:       g.BuiltAt,
		Tags:          g.AllTags(),
		IndexedCommit: state.IndexedCommit,
		IndexedAt:     state.IndexedAt,
	}

	for _, f := range g.NodesOfKind(KindFile) {
		switch FileStatus(projectRoot, f) {
		case StatusFresh:
			rep.Fresh++
		case StatusStale:
			rep.Stale++
		default:
			rep.Missing++
		}
	}
	if total := rep.Fresh + rep.Stale + rep.Missing; total > 0 {
		rep.StaleRatio = float64(rep.Stale+rep.Missing) / float64(total)
	}
	return rep, nil
}

// NeedsRebuild reports whether enough of the graph has drifted that serving it
// would mislead an agent.
func (r Report) NeedsRebuild() bool {
	return r.StaleRatio > constants.StaleRebuildRatio
}

// ReadSpan returns the exact source bytes a span points at, with the line range
// it covers. A hash mismatch is an error rather than a silent stale read.
func ReadSpan(projectRoot string, span *Span) (string, error) {
	if span == nil || span.Path == "" {
		return "", fmt.Errorf("span has no path")
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, span.Path))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", span.Path, err)
	}
	if span.Hash != "" && HashBytes(data) != span.Hash {
		return "", fmt.Errorf("%s changed since indexing — rebuild the graph", span.Path)
	}

	start, end := span.Start, span.End
	if start < 0 || start > len(data) {
		return "", fmt.Errorf("span start %d is outside %s", start, span.Path)
	}
	if end <= start || end > len(data) {
		end = len(data)
	}
	return string(data[start:end]), nil
}

// ReadLines returns a 1-based inclusive line range from a file. It is the
// fallback when a node has no byte offsets, such as a file node.
func ReadLines(projectRoot, path string, first, last int) (string, error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, path))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	if first < 1 {
		first = 1
	}
	if last <= 0 || last > len(lines) {
		last = len(lines)
	}
	if first > len(lines) {
		return "", fmt.Errorf("%s has only %d lines", path, len(lines))
	}
	return strings.Join(lines[first-1:last], "\n"), nil
}

// CacheBody stores a served body under .memor/blobs/, keyed by its content
// hash. Keying on content makes serving a stale entry structurally impossible.
func CacheBody(paths store.Paths, cfg config.Config, body string) error {
	if !cfg.Graph.Cache.Enabled || body == "" {
		return nil
	}
	if err := os.MkdirAll(paths.Blobs, 0o755); err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(body))
	key := hex.EncodeToString(sum[:])[:16]
	path := filepath.Join(paths.Blobs, key)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		return os.Chtimes(path, now, now)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	return evictBlobs(paths, cfg.Graph.Cache.MaxBytes)
}

// evictBlobs enforces the cache cap by dropping least-recently-used entries.
func evictBlobs(paths store.Paths, maxBytes int64) error {
	entries, err := os.ReadDir(paths.Blobs)
	if err != nil {
		return nil
	}

	type blob struct {
		path string
		size int64
		mod  int64
	}
	var blobs []blob
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		blobs = append(blobs, blob{filepath.Join(paths.Blobs, e.Name()), info.Size(), info.ModTime().Unix()})
		total += info.Size()
	}
	if total <= maxBytes {
		return nil
	}

	sort.Slice(blobs, func(i, j int) bool { return blobs[i].mod < blobs[j].mod })
	for _, b := range blobs {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(b.path); err != nil {
			continue
		}
		total -= b.size
	}
	return nil
}
