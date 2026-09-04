package graph

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/store"
)

// legacyEntry mirrors the v1 memory.Entry wire format. It is duplicated here
// rather than imported so the v1 packages could be deleted outright; migration
// is a one-shot read of a format that will never gain a field again.
type legacyEntry struct {
	Timestamp  int64          `json:"t"`
	Type       string         `json:"y"`
	ID         string         `json:"id"`
	Tags       []string       `json:"tags"`
	Content    string         `json:"c"`
	Expires    int64          `json:"x,omitempty"`
	Supersedes string         `json:"sup,omitempty"`
	Meta       *legacyCodeMet `json:"meta,omitempty"`
}

type legacyCodeMet struct {
	FilePath string   `json:"file"`
	LOC      int      `json:"loc"`
	Hash     string   `json:"hash"`
	Exports  []string `json:"exports,omitempty"`
	Deps     []string `json:"deps,omitempty"`
	Summary  string   `json:"summary"`
	Patterns string   `json:"patterns,omitempty"`
	Logic    string   `json:"logic,omitempty"`
}

// MigrationReport describes what a v1 to v2 migration moved.
type MigrationReport struct {
	Memories int
	Files    int
	Symbols  int
	Docs     int
	Topics   int
	Edges    int
	Skipped  int
}

// NeedsMigration reports whether a v1 store is present without a v2 one.
func NeedsMigration(paths store.Paths) bool {
	return paths.HasLegacyStore() && !paths.HasGraph()
}

// Migrate converts a v1 store into a v2 graph and renames the v1 files to
// *.v1.bak. It is idempotent: running it twice is a no-op because the second
// run finds no v1 files.
//
// Existing users get a partial graph immediately, before any extraction runs,
// because v1 @c entries already carry Deps that become real imports edges.
func Migrate(paths store.Paths, cfg config.Config) (MigrationReport, error) {
	lock, err := store.AcquireLock(paths.Lock, store.CompactLockTimeout)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("lock state: %w", err)
	}
	defer lock.Release()

	entries, skipped := readLegacyEntries(paths)
	g := New()
	if existing, err := ReadSnap(paths.Snap); err == nil {
		Apply(g, existing)
	}

	report := MigrationReport{Skipped: skipped}
	idMap := make(map[string]string, len(entries))

	for _, e := range entries {
		if e.Meta != nil && e.Meta.FilePath != "" {
			migrateCodeEntry(g, e, &report)
			idMap[e.ID] = NodeID(KindFile, e.Meta.FilePath)
			continue
		}
		if strings.TrimSpace(e.Content) == "" {
			report.Skipped++
			continue
		}
		node := migrateMemoryEntry(g, e, &report)
		idMap[e.ID] = node.ID
	}

	// Supersedes chains reference v1 content IDs, so they can only be resolved
	// once every node has been assigned its v2 identity.
	for _, e := range entries {
		if e.Supersedes == "" {
			continue
		}
		from, ok := idMap[e.ID]
		if !ok {
			continue
		}
		to, ok := idMap[e.Supersedes]
		if !ok {
			continue
		}
		g.AddEdge(Edge{From: from, To: to, Kind: EdgeSupersedes, W: 1})
	}

	migrateKnowledge(g, paths.LegacyKnowledge, &report)

	g.Resolve()
	g.BuiltAt = time.Now().Unix()
	report.Edges = g.EdgeCount()
	report.Topics = len(g.NodesOfKind(KindTopic))

	if err := Write(paths, g); err != nil {
		return report, fmt.Errorf("write snapshot: %w", err)
	}
	if err := RenderToFile(paths.DB, g, cfg); err != nil {
		return report, fmt.Errorf("render projection: %w", err)
	}
	if err := archiveLegacyFiles(paths); err != nil {
		return report, err
	}
	return report, nil
}

func migrateCodeEntry(g *Graph, e legacyEntry, report *MigrationReport) {
	meta := e.Meta
	node := FileNode(meta.FilePath, meta.Summary, meta.LOC, meta.Hash, "")
	node.T = e.Timestamp
	node.SetMeta(MetaOrigin, OriginAgent)
	node.SetMeta(MetaPatterns, meta.Patterns)
	node.SetMeta(MetaLogic, meta.Logic)
	g.AddNode(node)
	report.Files++

	for _, export := range meta.Exports {
		name := strings.TrimSpace(export)
		if name == "" {
			continue
		}
		sym := SymNode(meta.FilePath, name, name, "func", &Span{Path: meta.FilePath, Hash: meta.Hash})
		sym.T = e.Timestamp
		sym.SetMeta(MetaOrigin, OriginAgent)
		g.AddNode(sym)
		g.AddEdge(Edge{From: node.ID, To: sym.ID, Kind: EdgeContains, W: 1})
		report.Symbols++
	}

	// Deps were persisted by v1 but never traversed. They become load-bearing
	// here, which is the whole point of the migration running before extraction.
	for _, dep := range meta.Deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		target := NodeID(KindFile, dep)
		if _, ok := g.Node(target); !ok {
			ext := ExtNode(dep)
			ext.SetMeta(MetaOrigin, OriginAgent)
			g.AddNode(ext)
			target = ext.ID
		}
		g.AddEdge(Edge{From: node.ID, To: target, Kind: EdgeImports, W: 1})
	}
	attachTags(g, node.ID, e.Tags)
}

func migrateMemoryEntry(g *Graph, e legacyEntry, report *MigrationReport) *Node {
	memType := ParseMemType(e.Type)
	if memType == "" {
		memType = MemSemantic
	}
	node := MemNode(e.Content, memType, e.Timestamp)
	node.Exp = e.Expires
	g.AddNode(node)
	attachTags(g, node.ID, e.Tags)
	report.Memories++
	return node
}

func attachTags(g *Graph, nodeID string, tags []string) {
	for _, tag := range tags {
		clean := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, "#")))
		if clean == "" {
			continue
		}
		topic := TopicNode(clean)
		g.AddNode(topic)
		g.AddEdge(Edge{From: nodeID, To: topic.ID, Kind: EdgeTagged, W: 1})
	}
}

func readLegacyEntries(paths store.Paths) ([]legacyEntry, int) {
	var entries []legacyEntry
	skipped := 0

	// The canonical JSONL snapshot is lossless; memory.db is a lossy projection
	// of the same data, so it is deliberately not read.
	for _, path := range []string{paths.LegacySnapshot, paths.LegacyWAL} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var entry legacyEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				skipped++
				continue
			}
			entries = append(entries, entry)
		}
		f.Close()
	}
	return entries, skipped
}

var (
	legacyDocRegex     = regexp.MustCompile(`^@doc\s+(\S+)\s*((?:#\S+\s*)*)\[(\d+)\s+sections?\]$`)
	legacySectionRegex = regexp.MustCompile(`^\s+::\s+(\S+):\s+(.+)$`)
)

// migrateKnowledge folds the v1 knowledge.db DSL into KindDoc nodes. This is
// the last code in memor that parses a rendered projection back; graph.db is
// write-only, so nothing replaces it.
func migrateKnowledge(g *Graph, path string, report *MigrationReport) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var source, docName string
	var tags []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		if m := legacyDocRegex.FindStringSubmatch(line); m != nil {
			docName = m[1]
			source = docName
			tags = strings.Fields(m[2])
			continue
		}
		if rest, ok := strings.CutPrefix(line, "  source: "); ok {
			source = strings.TrimSpace(rest)
			continue
		}
		if m := legacySectionRegex.FindStringSubmatch(line); m != nil && docName != "" {
			node := DocNode(source, m[1], m[2])
			node.SetMeta(MetaOrigin, OriginAgent)
			g.AddNode(node)
			attachTags(g, node.ID, tags)
			report.Docs++
		}
	}
}

// archiveLegacyFiles renames the v1 store aside rather than deleting it. A
// migration that loses data with no recovery path is not a migration.
func archiveLegacyFiles(paths store.Paths) error {
	for _, path := range []string{
		paths.LegacySnapshot, paths.LegacyMemoryDB, paths.LegacyWAL,
		paths.LegacyArchive, paths.LegacyKnowledge,
	} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Rename(path, path+".v1.bak"); err != nil {
			return fmt.Errorf("archive %s: %w", path, err)
		}
	}
	return nil
}
