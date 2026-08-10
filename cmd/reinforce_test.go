package cmd

import (
	"reflect"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/engine"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
)

func TestReinforceMemorySurvivesCompaction(t *testing.T) {
	paths := store.ResolvePaths(t.TempDir())
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	meta := &memory.CodeMeta{
		FilePath: "internal/store/snapshot.go",
		LOC:      350,
		Hash:     "abcdef",
		Exports:  []string{"ReadSnapshot", "WriteSnapshot"},
		Summary:  "Persists active memories.",
	}
	entry := memory.Entry{
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		Type:      memory.TypeCode,
		ID:        memory.ContentID(meta.FilePath),
		Content:   meta.FilePath,
		Meta:      meta,
	}
	if _, err := store.WriteSnapshot(paths.MemoryDB, []memory.Entry{entry}, 10000); err != nil {
		t.Fatal(err)
	}
	reinforcedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()

	if err := reinforceMemory(paths, entry.ID, reinforcedAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Compact(paths, config.Default()); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected one reinforced entry, got %d", len(snapshot.Entries))
	}
	got := snapshot.Entries[0]
	if got.ID != entry.ID || got.Timestamp != reinforcedAt || !reflect.DeepEqual(got.Meta, meta) {
		t.Fatalf("reinforcement changed entry data: %#v", got)
	}
}
