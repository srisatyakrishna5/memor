package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
)

// Compact merges the WAL + existing snapshot into a fresh memory.db.
// Returns the number of entries written and the number archived.
func Compact(paths store.Paths, cfg config.Config) (written int, archived int, err error) {
	lock, err := store.AcquireLock(paths.Lock, store.CompactLockTimeout)
	if err != nil {
		return 0, 0, fmt.Errorf("lock state: %w", err)
	}
	defer lock.Release()

	return compactLocked(paths, cfg)
}

// compactLocked performs the compaction. Callers must already hold the state
// lock; nothing it calls acquires the lock itself, so it cannot self-deadlock.
func compactLocked(paths store.Paths, cfg config.Config) (written int, archived int, err error) {
	// 1. PARSE — read WAL and existing snapshot
	walEntries, walConsumed, err := store.ReadWALConsumed(paths.MemoryWAL)
	if err != nil {
		return 0, 0, fmt.Errorf("read WAL: %w", err)
	}

	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return 0, 0, fmt.Errorf("read snapshot: %w", err)
	}

	// Merge: WAL entries take priority over existing snapshot entries
	combined := mergeEntries(snap.Entries, walEntries)

	// 2. DEDUPLICATE — content-addressed by ID
	combined = deduplicate(combined)

	// 3. SCORE — compute relevance for each entry
	scored := scoreEntries(combined, cfg)

	// 4. DECAY — archive entries that fall below the configured threshold
	var candidates []memory.Entry
	var evicted []memory.Entry
	for _, se := range scored {
		if se.Score < cfg.Compaction.Decay.MinScore {
			evicted = append(evicted, se.Entry)
			continue
		}
		candidates = append(candidates, se.Entry)
	}

	// 5. PARTITION — storage owns serialization and the final token-budget partition
	result := store.PrepareSnapshot(candidates, cfg.Memory.TokenBudget)
	evicted = append(evicted, result.Evicted...)

	// 6. COMMIT — archive before replacing the active snapshot so failures are retryable
	if len(evicted) > 0 {
		if err := store.AppendToArchive(paths.Archive, evicted); err != nil {
			return 0, 0, fmt.Errorf("write archive: %w", err)
		}
	}
	if err := store.CommitSnapshot(paths.MemoryDB, result); err != nil {
		return 0, 0, fmt.Errorf("write snapshot: %w", err)
	}

	if err := store.TruncateWALPrefix(paths.MemoryWAL, walConsumed); err != nil {
		return 0, 0, fmt.Errorf("truncate WAL: %w", err)
	}

	return len(result.Written), len(evicted), nil
}

// AutoCompact compacts only once the WAL reaches the configured threshold.
// It writes nothing to stdout so callers on a stdio transport stay protocol-safe.
func AutoCompact(paths store.Paths, cfg config.Config) (written, archived int, ran bool, err error) {
	count, err := store.WALEntryCount(paths.MemoryWAL)
	if err != nil {
		return 0, 0, false, fmt.Errorf("count WAL entries: %w", err)
	}
	if count < cfg.Memory.WALMaxEntries {
		return 0, 0, false, nil
	}

	// Opportunistic: if someone else is compacting or writing, skip this round
	// rather than stalling the caller. The threshold will still be met next time.
	lock, err := store.AcquireLock(paths.Lock, 0)
	if errors.Is(err, store.ErrLockBusy) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("lock state: %w", err)
	}
	defer lock.Release()

	written, archived, err = compactLocked(paths, cfg)
	if err != nil {
		return 0, 0, false, err
	}
	return written, archived, true, nil
}

// mergeEntries combines snapshot + WAL entries. WAL entries (newer) come last.
func mergeEntries(existing, wal []memory.Entry) []memory.Entry {
	combined := make([]memory.Entry, 0, len(existing)+len(wal))
	combined = append(combined, existing...)
	combined = append(combined, wal...)
	return combined
}

// deduplicate removes duplicate entries by content-addressed ID.
// Handles supersedes chains: if entry B supersedes A, A is removed.
func deduplicate(entries []memory.Entry) []memory.Entry {
	byID := make(map[string]memory.Entry, len(entries))
	superseded := make(map[string]struct{})

	for _, e := range entries {
		if e.ID == "" {
			e.ID = memory.ContentID(e.Content)
		}
		// Mark superseded entries
		if e.Supersedes != "" {
			superseded[e.Supersedes] = struct{}{}
		}
		// Later entries with the same ID overwrite earlier ones
		byID[e.ID] = e
	}

	result := make([]memory.Entry, 0, len(byID))
	for id, e := range byID {
		if _, ok := superseded[id]; ok {
			continue // skip superseded
		}
		if e.IsExpired() {
			continue // skip expired
		}
		result = append(result, e)
	}
	return result
}

// scoreEntries computes relevance scores and sorts descending.
func scoreEntries(entries []memory.Entry, cfg config.Config) []memory.ScoredEntry {
	// Build tag overlap counts for reference boost
	tagCounts := make(map[string]int)
	for _, e := range entries {
		for _, tag := range e.Tags {
			tagCounts[tag]++
		}
	}

	scored := make([]memory.ScoredEntry, 0, len(entries))
	for _, e := range entries {
		typeWeight := cfg.TypeWeight(string(e.Type))
		recencyDecay := 1.0 / (1.0 + e.AgeDays()*cfg.Compaction.Decay.Rate)

		// Reference boost: how many other entries share tags
		refBoost := 0.0
		for _, tag := range e.Tags {
			refBoost += float64(tagCounts[tag]-1) * 0.1
		}

		score := typeWeight * recencyDecay * (1.0 + refBoost)

		scored = append(scored, memory.ScoredEntry{Entry: e, Score: score})
	}

	// Sort by score descending
	sortByScore(scored)
	return scored
}

func sortByScore(entries []memory.ScoredEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Score > entries[j-1].Score; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

func renderCompactLine(e memory.Entry) string {
	if e.Type == memory.TypeCode && e.Meta != nil {
		return renderCodeEntry(e)
	}
	prefix := e.Type.Prefix()
	var tags []string
	for _, t := range e.Tags {
		tags = append(tags, "#"+t)
	}
	tagStr := strings.Join(tags, " ")
	return fmt.Sprintf("%s %s: %s", prefix, tagStr, e.Content)
}

// renderCodeEntry formats a @c entry as a multi-line block.
func renderCodeEntry(e memory.Entry) string {
	m := e.Meta
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("@c %s [%d LOC | %s]", m.FilePath, m.LOC, m.Hash))
	if len(m.Exports) > 0 {
		sb.WriteString(fmt.Sprintf("\n  exports: %s", strings.Join(m.Exports, ", ")))
	}
	if len(m.Deps) > 0 {
		sb.WriteString(fmt.Sprintf("\n  deps: %s", strings.Join(m.Deps, ", ")))
	}
	if m.Summary != "" {
		sb.WriteString(fmt.Sprintf("\n  summary: %s", m.Summary))
	}
	if m.Patterns != "" {
		sb.WriteString(fmt.Sprintf("\n  patterns: %s", m.Patterns))
	}
	if m.Logic != "" {
		sb.WriteString(fmt.Sprintf("\n  logic: %s", m.Logic))
	}
	return sb.String()
}
