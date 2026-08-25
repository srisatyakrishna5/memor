package engine

import (
	"fmt"
	"sync"
	"testing"

	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
)

// Compaction reads the WAL and then truncates it. Before the state lock, an
// append landing between those two steps was silently destroyed — a real risk
// now that a long-lived MCP server writes alongside CLI invocations.
func TestCompactDoesNotDropConcurrentAppends(t *testing.T) {
	paths, cfg := setupTestProject(t)

	const (
		writers    = 24
		compactors = 4
	)

	content := func(i int) string { return fmt.Sprintf("concurrent entry %02d", i) }

	start := make(chan struct{})
	failures := make(chan error, writers+compactors)
	var wg sync.WaitGroup

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := store.AppendToWAL(paths.MemoryWAL, memory.Entry{
				Type:    memory.TypeSemantic,
				Tags:    []string{"race"},
				Content: content(i),
			})
			if err != nil {
				failures <- fmt.Errorf("append %d: %w", i, err)
			}
		}(i)
	}

	for i := 0; i < compactors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, err := Compact(paths, cfg); err != nil {
				failures <- fmt.Errorf("compact: %w", err)
			}
		}()
	}

	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent operation failed: %v", err)
	}

	// Fold whatever the last compaction left behind into the snapshot.
	if _, _, err := Compact(paths, cfg); err != nil {
		t.Fatalf("final Compact: %v", err)
	}

	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	archived, err := store.ReadWAL(paths.Archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	survived := make(map[string]struct{}, writers)
	for _, e := range snap.Entries {
		survived[e.Content] = struct{}{}
	}
	for _, e := range archived {
		survived[e.Content] = struct{}{}
	}

	for i := 0; i < writers; i++ {
		if _, ok := survived[content(i)]; !ok {
			t.Errorf("lost entry %q", content(i))
		}
	}
}

// AutoCompact is opportunistic: if another process holds the lock it must skip
// rather than block the caller or fail the surrounding command.
func TestAutoCompactSkipsWhenLockIsHeld(t *testing.T) {
	paths, cfg := setupTestProject(t)

	for i := 0; i < cfg.Memory.WALMaxEntries+1; i++ {
		if err := store.AppendToWAL(paths.MemoryWAL, memory.Entry{
			Type:    memory.TypeSemantic,
			Content: fmt.Sprintf("pending %d", i),
		}); err != nil {
			t.Fatalf("AppendToWAL: %v", err)
		}
	}

	lock, err := store.AcquireLock(paths.Lock, 0)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lock.Release()

	written, archived, ran, err := AutoCompact(paths, cfg)
	if err != nil {
		t.Fatalf("AutoCompact should skip silently, got error: %v", err)
	}
	if ran || written != 0 || archived != 0 {
		t.Fatalf("expected a skipped compaction, got ran=%v written=%d archived=%d", ran, written, archived)
	}

	count, err := store.WALEntryCount(paths.MemoryWAL)
	if err != nil {
		t.Fatalf("WALEntryCount: %v", err)
	}
	if count != cfg.Memory.WALMaxEntries+1 {
		t.Fatalf("expected the WAL untouched, got %d entries", count)
	}
}
