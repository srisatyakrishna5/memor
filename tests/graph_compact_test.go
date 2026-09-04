package tests

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/store"
)

// Compaction reads the log and then truncates it. Before the state lock, an
// append landing between those two steps was silently destroyed — a real risk
// now that a long-lived MCP server writes alongside CLI invocations.
func TestCompactDoesNotDropConcurrentAppends(t *testing.T) {
	paths, cfg := newProject(t)

	const (
		writers    = 24
		compactors = 4
	)

	content := func(i int) string { return fmt.Sprintf("concurrent memory %02d", i) }

	start := make(chan struct{})
	failures := make(chan error, writers+compactors)
	var wg sync.WaitGroup

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			node := graph.MemNode(content(i), graph.MemSemantic, time.Now().Unix())
			if err := graph.Append(paths.Log, []graph.Record{graph.NodeRecord(node)}); err != nil {
				failures <- fmt.Errorf("append %d: %w", i, err)
			}
		}(i)
	}

	for i := 0; i < compactors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, err := graph.Compact(paths, cfg); err != nil {
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
	if _, _, err := graph.Compact(paths, cfg); err != nil {
		t.Fatalf("final Compact: %v", err)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	archived, err := store.ReadRecords(paths.Archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	survived := make(map[string]struct{}, writers)
	for _, n := range g.NodesOfKind(graph.KindMem) {
		survived[n.Text] = struct{}{}
	}
	for _, rec := range graph.Decode(archived, "archive") {
		if rec.Node != nil {
			survived[rec.Node.Text] = struct{}{}
		}
	}

	for i := 0; i < writers; i++ {
		if _, ok := survived[content(i)]; !ok {
			t.Errorf("lost memory %q", content(i))
		}
	}
}

// AutoCompact is opportunistic: if another process holds the lock it must skip
// rather than block the caller or fail the surrounding command.
func TestAutoCompactSkipsWhenLockIsHeld(t *testing.T) {
	paths, cfg := newProject(t)
	cfg.Memory.LogMaxRecords = 2

	for i := 0; i < cfg.Memory.LogMaxRecords+1; i++ {
		node := graph.MemNode(fmt.Sprintf("pending %d", i), graph.MemSemantic, time.Now().Unix())
		if err := graph.Append(paths.Log, []graph.Record{graph.NodeRecord(node)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	lock, err := store.AcquireLock(paths.Lock, 0)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lock.Release()

	written, archived, ran, err := graph.AutoCompact(paths, cfg)
	if err != nil {
		t.Fatalf("AutoCompact should skip silently, got error: %v", err)
	}
	if ran || written != 0 || archived != 0 {
		t.Fatalf("expected a skipped compaction, got ran=%v written=%d archived=%d", ran, written, archived)
	}

	count, err := store.RecordCount(paths.Log)
	if err != nil {
		t.Fatalf("RecordCount: %v", err)
	}
	if count != cfg.Memory.LogMaxRecords+1 {
		t.Fatalf("expected the log untouched, got %d records", count)
	}
}

// AutoCompact must do nothing until the threshold is reached, so a single
// remember call does not pay for a full snapshot rewrite.
func TestAutoCompactWaitsForThreshold(t *testing.T) {
	paths, cfg := newProject(t)
	cfg.Memory.LogMaxRecords = 5

	node := graph.MemNode("just one record", graph.MemSemantic, time.Now().Unix())
	if err := graph.Append(paths.Log, []graph.Record{graph.NodeRecord(node)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	_, _, ran, err := graph.AutoCompact(paths, cfg)
	if err != nil {
		t.Fatalf("AutoCompact: %v", err)
	}
	if ran {
		t.Error("expected auto-compaction to wait for the threshold")
	}
}
