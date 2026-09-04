package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/graph"
)

func TestNodeIDIsContentAddressed(t *testing.T) {
	a := graph.MemNode("PostgreSQL 16 with Drizzle", graph.MemSemantic, 0)
	b := graph.MemNode("  postgresql 16 with drizzle  ", graph.MemSemantic, 0)

	if a.ID != b.ID {
		t.Errorf("expected normalization to collapse the same fact: %s vs %s", a.ID, b.ID)
	}
	if same := graph.MemNode("something else", graph.MemSemantic, 0); same.ID == a.ID {
		t.Error("expected distinct content to produce distinct IDs")
	}
	// A file named the same as a memory's text must not collide with it.
	if graph.FileNode("PostgreSQL 16 with Drizzle", "", 0, "", "").ID == a.ID {
		t.Error("expected the kind prefix to keep IDs distinct across kinds")
	}
}

func TestKindRoundTrip(t *testing.T) {
	kinds := []graph.Kind{
		graph.KindFile, graph.KindSym, graph.KindPkg,
		graph.KindDoc, graph.KindMem,
	}
	for _, kind := range kinds {
		parsed, ok := graph.ParseKind(kind.String())
		if !ok || parsed != kind {
			t.Errorf("kind %v did not round-trip through %q", kind, kind.String())
		}
	}
}

// Metadata lists are the replacement for edges, so they must survive the JSON
// round-trip through graph.snap unchanged and in a stable order.
func TestMetaListRoundTrip(t *testing.T) {
	node := graph.FileNode("a.go", "", 1, "aaaaaa", "go")
	node.SetMetaList(graph.MetaImports, []string{"internal/store", "internal/graph", "internal/store", ""})

	got := node.MetaList(graph.MetaImports)
	if len(got) != 2 || got[0] != "internal/graph" || got[1] != "internal/store" {
		t.Fatalf("expected deduplicated sorted values, got %v", got)
	}

	lines, err := graph.Encode([]graph.Record{graph.NodeRecord(node)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if bytes.ContainsRune(lines[0], '\n') {
		t.Error("a record must stay on one line")
	}

	decoded := graph.Decode(lines, "test")
	if len(decoded) != 1 || decoded[0].Node == nil {
		t.Fatal("expected one node record")
	}
	if rt := decoded[0].Node.MetaList(graph.MetaImports); len(rt) != 2 || rt[0] != "internal/graph" {
		t.Errorf("metadata list did not survive the round-trip: %v", rt)
	}
}

// Extraction knows structure but not intent, so a rebuild must never blank a
// summary an agent wrote.
func TestAgentSummarySurvivesExtraction(t *testing.T) {
	g := graph.New()

	authored := graph.FileNode("main.go", "Entry point; delegates to cmd.Execute", 5, "aaaaaa", "go")
	authored.SetMeta(graph.MetaOrigin, graph.OriginAgent)
	g.AddNode(authored)

	g.AddNode(graph.FileNode("main.go", "", 5, "bbbbbb", "go"))

	node, ok := g.FindFile("main.go")
	if !ok {
		t.Fatal("expected the file node to exist")
	}
	if node.Text != "Entry point; delegates to cmd.Execute" {
		t.Errorf("extraction overwrote an agent summary: %q", node.Text)
	}
	if node.Span.Hash != "bbbbbb" {
		t.Errorf("expected the fresh hash to win, got %q", node.Span.Hash)
	}
}

func TestPruneExtractedKeepsAgentNodes(t *testing.T) {
	g := graph.New()

	memory := graph.MemNode("Compaction archives before truncating", graph.MemSemantic, 0)
	file := graph.FileNode("a.go", "", 1, "aaaaaa", "go")
	memory.SetMetaList(graph.MetaExplains, []string{file.ID})
	g.AddNode(memory)
	g.AddNode(file)

	g.PruneExtracted()

	if _, ok := g.Node(memory.ID); !ok {
		t.Error("expected the agent-authored memory to survive a rebuild")
	}
	if _, ok := g.Node(file.ID); ok {
		t.Error("expected the extracted file node to be pruned")
	}
	// The attachment is content-addressed by path, so re-extracting the same
	// file must restore the link rather than orphan the memory.
	g.AddNode(graph.FileNode("a.go", "", 1, "bbbbbb", "go"))
	if notes := g.Explaining(file.ID); len(notes) != 1 {
		t.Errorf("expected the attachment to resolve again after a rebuild, got %d", len(notes))
	}
}

func TestLogRoundTripThroughSnapshot(t *testing.T) {
	paths, cfg := newProject(t)

	memory := graph.MemNode("BM25 is rebuilt per call by design", graph.MemSemantic, time.Now().Unix())
	memory.SetMetaList(graph.MetaTags, []string{"retrieval"})
	if err := graph.Append(paths.Log, []graph.Record{graph.NodeRecord(memory)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, _, err := graph.Compact(paths, cfg); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	node, ok := g.Node(memory.ID)
	if !ok {
		t.Fatal("expected the memory to survive compaction")
	}
	if node.Text != memory.Text {
		t.Errorf("content changed through the snapshot: %q", node.Text)
	}
	if tags := node.Tags(); len(tags) != 1 || tags[0] != "retrieval" {
		t.Errorf("expected the tag to survive, got %v", tags)
	}
}

// A superseded memory must disappear on compaction, not accumulate alongside
// the fact that replaced it.
func TestCompactionDropsSupersededMemories(t *testing.T) {
	paths, cfg := newProject(t)

	old := graph.MemNode("Use MD5 for content hashing", graph.MemSemantic, time.Now().Unix())
	replacement := graph.MemNode("Use SHA-256 for content hashing", graph.MemSemantic, time.Now().Unix())
	replacement.SetMetaList(graph.MetaSupersedes, []string{old.ID})

	if err := graph.Append(paths.Log, []graph.Record{
		graph.NodeRecord(old),
		graph.NodeRecord(replacement),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, archived, err := graph.Compact(paths, cfg); err != nil {
		t.Fatalf("Compact: %v", err)
	} else if archived != 1 {
		t.Errorf("expected exactly one archived node, got %d", archived)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := g.Node(old.ID); ok {
		t.Error("expected the superseded memory to be gone")
	}
	if _, ok := g.Node(replacement.ID); !ok {
		t.Error("expected the replacement to survive")
	}
}

func TestExpiredMemoriesAreArchived(t *testing.T) {
	paths, cfg := newProject(t)

	expired := graph.MemNode("Temporary workaround for the CI cache", graph.MemEpisodic, time.Now().Unix())
	expired.Exp = time.Now().Add(-time.Hour).Unix()

	if err := graph.Append(paths.Log, []graph.Record{graph.NodeRecord(expired)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, archived, err := graph.Compact(paths, cfg); err != nil {
		t.Fatalf("Compact: %v", err)
	} else if archived != 1 {
		t.Errorf("expected the expired memory to be archived, got %d", archived)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := g.Node(expired.ID); ok {
		t.Error("expected the expired memory to be gone from the active graph")
	}
}

// The term index is derived by definition: it must be rebuildable from the
// store alone, with no cached file to drift out of sync.
func TestIndexIsDerivedFromTheStore(t *testing.T) {
	paths, cfg := newProject(t)

	memory := graph.MemNode("Spans point into the repo rather than copying it", graph.MemSemantic, time.Now().Unix())
	if err := graph.Append(paths.Log, []graph.Record{graph.NodeRecord(memory)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, _, err := graph.Compact(paths, cfg); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	first := graph.BuildIndex(g).Seeds("spans repo", 5)
	second := graph.BuildIndex(g).Seeds("spans repo", 5)
	if len(first) == 0 {
		t.Fatal("expected the memory to be findable by its own words")
	}
	if len(first) != len(second) || first[0] != second[0] {
		t.Errorf("expected two builds to agree: %v vs %v", first, second)
	}
}

// A malformed line must warn and be skipped. One bad record cannot be allowed
// to destroy the store.
func TestMalformedLogLineIsSkipped(t *testing.T) {
	paths, _ := newProject(t)

	memory := graph.MemNode("A valid record", graph.MemSemantic, time.Now().Unix())
	encoded, err := graph.Encode([]graph.Record{graph.NodeRecord(memory)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	content := append([]byte("this is not json\n"), append(encoded[0], '\n')...)
	if err := os.WriteFile(paths.Log, content, 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load should tolerate a malformed line: %v", err)
	}
	if _, ok := g.Node(memory.ID); !ok {
		t.Error("expected the valid record to survive alongside the malformed one")
	}
}

func TestFileStatusDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hash, loc, err := graph.FileHashAndLOC(path)
	if err != nil {
		t.Fatalf("FileHashAndLOC: %v", err)
	}
	node := graph.FileNode("sample.go", "", loc, hash, "go")

	if got := graph.FileStatus(dir, node); got != graph.StatusFresh {
		t.Errorf("expected fresh, got %s", got)
	}

	if err := os.WriteFile(path, []byte("package sample\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := graph.FileStatus(dir, node); got != graph.StatusStale {
		t.Errorf("expected stale after an edit, got %s", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := graph.FileStatus(dir, node); got != graph.StatusMissing {
		t.Errorf("expected missing after deletion, got %s", got)
	}
}

// Serving a span from a drifted file would hand the agent the wrong lines under
// a correct-looking line number, which is worse than refusing.
func TestReadSpanRefusesDriftedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	span := &graph.Span{Path: "sample.go", Start: 0, End: 7, L0: 1, L1: 1, Hash: "000000"}
	if _, err := graph.ReadSpan(dir, span); err == nil {
		t.Error("expected a hash mismatch to be an error, not a silent stale read")
	}
}
