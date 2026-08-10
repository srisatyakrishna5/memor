package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
)

func setupTestProject(t *testing.T) (store.Paths, config.Config) {
	t.Helper()
	dir := t.TempDir()
	paths := store.ResolvePaths(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	cfg := config.Default()

	// Write empty snapshot
	if err := os.WriteFile(paths.MemoryDB, []byte("@mem v1 | 0 entries | budget:10000 | compacted:none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write empty WAL
	if err := os.WriteFile(paths.MemoryWAL, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	return paths, cfg
}

func TestCompactBasic(t *testing.T) {
	paths, cfg := setupTestProject(t)

	// Add entries to WAL
	entries := []memory.Entry{
		{Type: memory.TypeSemantic, Tags: []string{"arch"}, Content: "PostgreSQL 16"},
		{Type: memory.TypeProcedural, Tags: []string{"deploy"}, Content: "pnpm turbo deploy"},
		{Type: memory.TypePreference, Tags: []string{"style"}, Content: "no any types"},
	}
	for _, e := range entries {
		if err := store.AppendToWAL(paths.MemoryWAL, e); err != nil {
			t.Fatal(err)
		}
	}

	written, archived, err := Compact(paths, cfg)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if written != 3 {
		t.Errorf("expected 3 written, got %d", written)
	}
	if archived != 0 {
		t.Errorf("expected 0 archived, got %d", archived)
	}

	// WAL should be truncated
	count, _ := store.WALEntryCount(paths.MemoryWAL)
	if count != 0 {
		t.Errorf("expected WAL truncated, got %d entries", count)
	}

	// Snapshot should have entries
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 3 {
		t.Errorf("expected 3 entries in snapshot, got %d", len(snap.Entries))
	}
}

func TestCompactDeduplicates(t *testing.T) {
	paths, cfg := setupTestProject(t)

	first := memory.Entry{Type: memory.TypeSemantic, Tags: []string{"old"}, Content: "PostgreSQL 16", Author: "first"}
	latest := memory.Entry{Type: memory.TypeSemantic, Tags: []string{"current"}, Content: "PostgreSQL 16", Author: "latest"}
	if err := store.AppendToWAL(paths.MemoryWAL, first); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendToWAL(paths.MemoryWAL, latest); err != nil {
		t.Fatal(err)
	}

	written, _, err := Compact(paths, cfg)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if written != 1 {
		t.Errorf("expected 1 deduplicated entry, got %d", written)
	}
	snapshot, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Author != "latest" || snapshot.Entries[0].Tags[0] != "current" {
		t.Fatalf("expected latest duplicate to win, got %#v", snapshot.Entries)
	}
}

func TestCompactSupersedes(t *testing.T) {
	paths, cfg := setupTestProject(t)

	oldEntry := memory.Entry{
		Type:    memory.TypeSemantic,
		Tags:    []string{"db"},
		Content: "PostgreSQL 15",
	}
	oldEntry.ID = memory.ContentID(oldEntry.Content)

	newEntry := memory.Entry{
		Type:       memory.TypeSemantic,
		Tags:       []string{"db"},
		Content:    "PostgreSQL 16",
		Supersedes: oldEntry.ID,
	}

	if err := store.AppendToWAL(paths.MemoryWAL, oldEntry); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendToWAL(paths.MemoryWAL, newEntry); err != nil {
		t.Fatal(err)
	}

	written, _, err := Compact(paths, cfg)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if written != 1 {
		t.Errorf("expected 1 entry after supersede, got %d", written)
	}

	snap, _ := store.ReadSnapshot(paths.MemoryDB)
	if len(snap.Entries) > 0 && snap.Entries[0].Content != "PostgreSQL 16" {
		t.Errorf("expected superseding entry to survive, got: %s", snap.Entries[0].Content)
	}
}

func TestCompactMergesWALAndSnapshot(t *testing.T) {
	paths, cfg := setupTestProject(t)

	// Write initial snapshot with one entry
	initialEntries := []memory.Entry{
		{Type: memory.TypeSemantic, Tags: []string{"arch"}, Content: "existing entry", Timestamp: time.Now().Unix()},
	}
	if _, err := store.WriteSnapshot(paths.MemoryDB, initialEntries, 10000); err != nil {
		t.Fatal(err)
	}

	// Add new entry to WAL
	if err := store.AppendToWAL(paths.MemoryWAL, memory.Entry{
		Type: memory.TypeProcedural, Tags: []string{"deploy"}, Content: "new WAL entry",
	}); err != nil {
		t.Fatal(err)
	}

	written, _, err := Compact(paths, cfg)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if written != 2 {
		t.Errorf("expected 2 entries (merged), got %d", written)
	}
}

func TestCompactMigratesLegacySnapshotToCanonicalStore(t *testing.T) {
	paths, cfg := setupTestProject(t)
	legacySnapshot := "@mem v1 | 1 entries | budget:10000 | compacted:2026-08-10T00:00:00Z\n\n" +
		"@s #migration: legacy entry [2026-08-10]\n"
	if err := os.WriteFile(paths.MemoryDB, []byte(legacySnapshot), 0o644); err != nil {
		t.Fatal(err)
	}

	written, _, err := Compact(paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if written != 1 {
		t.Fatalf("expected one migrated entry, got %d", written)
	}
	if _, err := os.Stat(paths.Snapshot); err != nil {
		t.Fatalf("canonical snapshot was not created: %v", err)
	}
	snapshot, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Content != "legacy entry" {
		t.Fatalf("legacy entry was not migrated: %#v", snapshot.Entries)
	}
}

func TestCompactArchivesEntriesBelowScoreThreshold(t *testing.T) {
	paths, cfg := setupTestProject(t)
	cfg.Compaction.Decay.MinScore = 2
	if err := store.AppendToWAL(paths.MemoryWAL, memory.Entry{
		Type: memory.TypeEpisodic, Tags: []string{"old"}, Content: "archive this entry",
	}); err != nil {
		t.Fatal(err)
	}

	written, archived, err := Compact(paths, cfg)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if written != 0 || archived != 1 {
		t.Fatalf("expected 0 written and 1 archived, got %d written and %d archived", written, archived)
	}
	archiveEntries, err := store.ReadWAL(paths.Archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(archiveEntries) != 1 || archiveEntries[0].Content != "archive this entry" {
		t.Fatalf("expected rejected entry in archive, got %#v", archiveEntries)
	}
}

func TestCompactArchivesEntriesRejectedByTokenBudget(t *testing.T) {
	paths, cfg := setupTestProject(t)
	cfg.Memory.TokenBudget = 1
	cfg.Compaction.Decay.MinScore = 0
	entries := []memory.Entry{
		{Type: memory.TypeSemantic, Tags: []string{"one"}, Content: "first budget rejection"},
		{Type: memory.TypeProcedural, Tags: []string{"two"}, Content: "second budget rejection"},
	}
	for _, entry := range entries {
		if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
			t.Fatal(err)
		}
	}

	written, archived, err := Compact(paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 || archived != 2 {
		t.Fatalf("expected 0 written and 2 archived, got %d written and %d archived", written, archived)
	}
	archiveEntries, err := store.ReadWAL(paths.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(archiveEntries) != 2 {
		t.Fatalf("expected both budget rejections in archive, got %#v", archiveEntries)
	}
}

func TestCompactRemovesExpiredEntriesWithoutArchiving(t *testing.T) {
	paths, cfg := setupTestProject(t)
	now := time.Now()
	entries := []memory.Entry{
		{Type: memory.TypeEpisodic, Content: "expired entry", Expires: now.Add(-time.Hour).Unix()},
		{Type: memory.TypeSemantic, Content: "active entry", Expires: now.Add(time.Hour).Unix()},
	}
	for _, entry := range entries {
		if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
			t.Fatal(err)
		}
	}

	written, archived, err := Compact(paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if written != 1 || archived != 0 {
		t.Fatalf("expected one active entry and no archive records, got %d written and %d archived", written, archived)
	}
	snapshot, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Content != "active entry" {
		t.Fatalf("expired entry survived compaction: %#v", snapshot.Entries)
	}
	archiveEntries, err := store.ReadWAL(paths.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(archiveEntries) != 0 {
		t.Fatalf("expired entries should be deleted, not archived: %#v", archiveEntries)
	}
}

func TestCompactPreservesPermanentExpirySentinel(t *testing.T) {
	paths, cfg := setupTestProject(t)
	cfg.Compaction.Decay.MinScore = 0
	entry := memory.Entry{
		Timestamp: time.Now().AddDate(-1, 0, 0).Unix(),
		Type:      memory.TypePreference,
		Content:   "permanent preference",
		Expires:   -1,
	}
	if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
		t.Fatal(err)
	}

	written, archived, err := Compact(paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if written != 1 || archived != 0 {
		t.Fatalf("expected permanent entry to survive, got %d written and %d archived", written, archived)
	}
}

func TestCompactArchiveFailurePreservesActiveSnapshot(t *testing.T) {
	paths, cfg := setupTestProject(t)
	entry := memory.Entry{
		Timestamp: time.Now().Unix(),
		Type:      memory.TypeSemantic,
		ID:        memory.ContentID("must remain recoverable"),
		Tags:      []string{"storage"},
		Content:   "must remain recoverable",
	}
	if _, err := store.WriteSnapshot(paths.MemoryDB, []memory.Entry{entry}, 10000); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.Archive, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Memory.TokenBudget = 1

	if _, _, err := Compact(paths, cfg); err == nil {
		t.Fatal("expected compaction to fail when archive path is a directory")
	}

	snapshot, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].ID != entry.ID {
		t.Fatalf("archive failure changed active snapshot: %#v", snapshot.Entries)
	}
}

func TestContextBasic(t *testing.T) {
	paths, cfg := setupTestProject(t)

	// Add entries
	entries := []memory.Entry{
		{Type: memory.TypeSemantic, Tags: []string{"arch"}, Content: "PostgreSQL 16 with Drizzle ORM"},
		{Type: memory.TypeProcedural, Tags: []string{"deploy"}, Content: "pnpm turbo deploy"},
	}
	for _, e := range entries {
		if err := store.AppendToWAL(paths.MemoryWAL, e); err != nil {
			t.Fatal(err)
		}
	}

	// Compact first so entries are in snapshot
	if _, _, err := Compact(paths, cfg); err != nil {
		t.Fatal(err)
	}

	result, err := Context(paths, cfg, ContextOptions{Budget: 10000})
	if err != nil {
		t.Fatalf("Context failed: %v", err)
	}

	if result == "" {
		t.Error("expected non-empty context output")
	}
	if len(result) < 10 {
		t.Errorf("context output too short: %s", result)
	}
}

func TestContextEmpty(t *testing.T) {
	paths, cfg := setupTestProject(t)

	result, err := Context(paths, cfg, ContextOptions{Budget: 10000})
	if err != nil {
		t.Fatalf("Context failed: %v", err)
	}

	if result != "# No memories found\n" {
		t.Errorf("expected no memories message, got: %s", result)
	}
}

func TestContextRespectsQuery(t *testing.T) {
	paths, cfg := setupTestProject(t)

	entries := []memory.Entry{
		{Type: memory.TypeSemantic, Tags: []string{"db"}, Content: "PostgreSQL 16 with Drizzle ORM"},
		{Type: memory.TypeSemantic, Tags: []string{"auth"}, Content: "OAuth2 PKCE via Auth0"},
		{Type: memory.TypeProcedural, Tags: []string{"deploy"}, Content: "deploy to kubernetes cluster"},
	}
	for _, e := range entries {
		if err := store.AppendToWAL(paths.MemoryWAL, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Compact(paths, cfg); err != nil {
		t.Fatal(err)
	}

	result, err := Context(paths, cfg, ContextOptions{Budget: 10000, Query: "deploy kubernetes"})
	if err != nil {
		t.Fatalf("Context failed: %v", err)
	}

	// Result should contain all entries but deploy-related should rank higher
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestKnowledgeMetadataSurvivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "runbook.md")
	knowledgePath := filepath.Join(dir, "knowledge.db")
	if err := os.WriteFile(sourcePath, []byte("## Deploy\nUse Go and Docker."), 0o644); err != nil {
		t.Fatal(err)
	}

	kb := &KnowledgeDB{Version: "1"}
	if err := IndexDocument(kb, sourcePath); err != nil {
		t.Fatal(err)
	}
	if err := WriteKnowledgeDB(knowledgePath, kb); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadKnowledgeDB(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Docs) != 1 || loaded.Docs[0].Source != sourcePath || loaded.Docs[0].Hash == "" {
		t.Fatalf("knowledge metadata was not preserved: %#v", loaded.Docs)
	}
}

func TestKnowledgeDocumentWithoutTagsSurvivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "handbook.md")
	knowledgePath := filepath.Join(dir, "knowledge.db")
	if err := os.WriteFile(sourcePath, []byte("## Etiquette\nBe concise and kind."), 0o644); err != nil {
		t.Fatal(err)
	}

	kb := &KnowledgeDB{Version: "1"}
	if err := IndexDocument(kb, sourcePath); err != nil {
		t.Fatal(err)
	}
	if len(kb.Docs[0].Tags) != 0 {
		t.Fatalf("test document unexpectedly produced tags: %#v", kb.Docs[0].Tags)
	}
	if err := WriteKnowledgeDB(knowledgePath, kb); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadKnowledgeDB(knowledgePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Docs) != 1 || loaded.Docs[0].Source != sourcePath {
		t.Fatalf("untagged knowledge document was not preserved: %#v", loaded.Docs)
	}
}

func TestScanKnowledgePathsSupportsRecursiveGlobstar(t *testing.T) {
	root := t.TempDir()
	nestedDir := filepath.Join(root, "tools", "reviewer")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "SKILL.md"), []byte("## Review\nTest Go code."), 0o644); err != nil {
		t.Fatal(err)
	}

	kb := &KnowledgeDB{Version: "1"}
	indexed, err := ScanKnowledgePaths(kb, root, []string{"**/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 || len(kb.Docs) != 1 {
		t.Fatalf("expected one recursively discovered document, indexed=%d docs=%d", indexed, len(kb.Docs))
	}
}
