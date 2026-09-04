package tests

import (
	"os"
	"testing"

	"github.com/memor-dev/memor/internal/graph"
)

const legacySnapshot = `{"t":1713800000,"y":"s","id":"abc123abc123","tags":["arch"],"c":"PostgreSQL 16 with Drizzle"}
{"t":1713800100,"y":"e","id":"def456def456","tags":["bug"],"c":"Fixed the N+1 query on the dashboard","sup":"abc123abc123"}
{"t":1713800200,"y":"f","id":"ghi789ghi789","tags":["style"],"c":"No any types in TypeScript"}
{"t":1713800300,"y":"c","id":"jkl012jkl012","tags":["engine"],"c":"internal/engine/context.go","meta":{"file":"internal/engine/context.go","loc":251,"hash":"4a91cc","exports":["Context","rankEntries"],"deps":["internal/store/wal.go"],"summary":"Builds the ranked context block","patterns":"Call once per conversation"}}
`

const legacyWAL = `{"t":1713800400,"y":"p","id":"mno345mno345","tags":["deploy"],"c":"Deploy with pnpm turbo deploy"}
this line is not json
`

const legacyKnowledge = `@knowledge v1 | 1 docs | 2 sections | indexed:2026-01-01T00:00:00Z

@doc CONTRIBUTING.md #go [2 sections]
  source: CONTRIBUTING.md
  hash: aabbcc
  :: setup: Run go mod download then go build.
  :: testing: Run go test ./... before opening a pull request.
`

func TestMigrateConvertsV1Store(t *testing.T) {
	paths, cfg := newProject(t)

	if err := os.WriteFile(paths.LegacySnapshot, []byte(legacySnapshot), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LegacyWAL, []byte(legacyWAL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LegacyKnowledge, []byte(legacyKnowledge), 0o644); err != nil {
		t.Fatal(err)
	}

	if !graph.NeedsMigration(paths) {
		t.Fatal("expected a v1 store with no graph to require migration")
	}

	report, err := graph.Migrate(paths, cfg)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if report.Memories != 4 {
		t.Errorf("expected 4 memories migrated, got %d", report.Memories)
	}
	if report.Files != 1 {
		t.Errorf("expected 1 file node migrated, got %d", report.Files)
	}
	if report.Symbols != 2 {
		t.Errorf("expected 2 symbols from the exports list, got %d", report.Symbols)
	}
	if report.Docs != 2 {
		t.Errorf("expected 2 knowledge sections migrated, got %d", report.Docs)
	}
	if report.Skipped != 1 {
		t.Errorf("expected the malformed line to be counted as skipped, got %d", report.Skipped)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Every v1 concept must survive with no loss.
	if len(g.NodesOfKind(graph.KindMem)) != 4 {
		t.Errorf("expected 4 memory nodes, got %d", len(g.NodesOfKind(graph.KindMem)))
	}
	if _, ok := g.FindFile("internal/engine/context.go"); !ok {
		t.Error("expected the v1 code entry to become a file node")
	}
	if len(g.FindSymbols("rankEntries")) == 0 {
		t.Error("expected a v1 export to become a symbol node")
	}
	if len(g.NodesOfKind(graph.KindDoc)) != 2 {
		t.Errorf("expected 2 doc nodes, got %d", len(g.NodesOfKind(graph.KindDoc)))
	}
	if len(g.NodesOfKind(graph.KindTopic)) == 0 {
		t.Error("expected v1 tags to become topic nodes")
	}
}

// Deps were persisted by v1 but never traversed. Migration is where they become
// load-bearing, which is why an existing user gets a partial graph before any
// extraction runs.
func TestMigrateMakesLegacyDepsTraversable(t *testing.T) {
	paths, cfg := newProject(t)
	if err := os.WriteFile(paths.LegacySnapshot, []byte(legacySnapshot), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Migrate(paths, cfg); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	file, ok := g.FindFile("internal/engine/context.go")
	if !ok {
		t.Fatal("expected the file node")
	}

	found := false
	for _, e := range g.Out(file.ID) {
		if e.Kind == graph.EdgeImports {
			found = true
		}
	}
	if !found {
		t.Error("expected the v1 Deps list to become imports edges")
	}
}

func TestMigrateResolvesSupersedesChains(t *testing.T) {
	paths, cfg := newProject(t)
	if err := os.WriteFile(paths.LegacySnapshot, []byte(legacySnapshot), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Migrate(paths, cfg); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	g, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	found := false
	for _, e := range g.Edges() {
		if e.Kind == graph.EdgeSupersedes {
			found = true
		}
	}
	if !found {
		t.Error("expected the v1 supersedes reference to be remapped onto v2 IDs")
	}
}

// A migration that destroys the only copy of the data is not a migration.
func TestMigrateArchivesRatherThanDeletes(t *testing.T) {
	paths, cfg := newProject(t)
	if err := os.WriteFile(paths.LegacySnapshot, []byte(legacySnapshot), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Migrate(paths, cfg); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := os.Stat(paths.LegacySnapshot); !os.IsNotExist(err) {
		t.Error("expected the v1 snapshot to be renamed aside")
	}
	if _, err := os.Stat(paths.LegacySnapshot + ".v1.bak"); err != nil {
		t.Errorf("expected a recoverable backup: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	paths, cfg := newProject(t)
	if err := os.WriteFile(paths.LegacySnapshot, []byte(legacySnapshot), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Migrate(paths, cfg); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	before, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if graph.NeedsMigration(paths) {
		t.Error("expected migration to be complete after the first run")
	}
	if _, err := graph.Migrate(paths, cfg); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	after, err := graph.Load(paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if before.NodeCount() != after.NodeCount() {
		t.Errorf("re-running migration changed the graph: %d then %d",
			before.NodeCount(), after.NodeCount())
	}
}
