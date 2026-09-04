// Package session wires paths, config, and the graph into the one entry point
// the CLI and the MCP server both use.
//
// Keeping this layer thin matters: every command must remain a single bounded
// operation that exits, with no daemon and no background worker.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/graph/extract"
	"github.com/memor-dev/memor/internal/store"
)

// Session is one project's resolved state.
type Session struct {
	Root  string
	Paths store.Paths
	Cfg   config.Config

	// Migrated is set when opening the session converted a v1 store, so a
	// caller can report what happened instead of finding nothing left to do.
	Migrated *graph.MigrationReport
}

// Open resolves the project root by walking up from start and loads its config.
// A v1 store found without a v2 one is migrated in place, so a user upgrading
// the binary never has to run a command to keep working.
func Open(start string) (*Session, error) {
	root := store.FindProjectRoot(start)
	paths := store.ResolvePaths(root)
	if !paths.Exists() {
		return nil, fmt.Errorf(
			"memor is not initialized for %s — run 'memor init' in the project root first", start)
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	s := &Session{Root: root, Paths: paths, Cfg: cfg}
	if graph.NeedsMigration(paths) {
		report, err := graph.Migrate(paths, cfg)
		if err != nil {
			return nil, fmt.Errorf("migrate v1 store: %w", err)
		}
		s.Migrated = &report
	}
	return s, nil
}

// Create initializes a project at dir without requiring it to exist already.
func Create(dir string) (*Session, error) {
	paths := store.ResolvePaths(dir)
	if err := paths.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}

	if _, err := os.Stat(paths.Config); os.IsNotExist(err) {
		if err := config.Save(paths.Config, config.Default()); err != nil {
			return nil, fmt.Errorf("write config: %w", err)
		}
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return &Session{Root: dir, Paths: paths, Cfg: cfg}, nil
}

// Graph loads the graph and its derived index.
func (s *Session) Graph() (*graph.Graph, *graph.Index, error) {
	g, err := graph.Load(s.Paths)
	if err != nil {
		return nil, nil, err
	}
	return g, graph.LoadIndex(s.Paths, g), nil
}

// RememberInput describes a fact to record.
type RememberInput struct {
	Content    string
	Type       string
	Tags       []string
	Files      []string // targets of explains edges
	Symbols    []string
	Expires    string
	Supersedes string
}

// Remember records a memory node plus its topic and explains edges.
//
// An explains edge is what no comparable tool has: it binds a decision to the
// exact file or symbol where a future agent would otherwise repeat the mistake.
func (s *Session) Remember(in RememberInput) (*graph.Node, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}

	memType := graph.MemSemantic
	if raw := strings.TrimSpace(in.Type); raw != "" {
		parsed := graph.ParseMemType(raw)
		if parsed == "" {
			return nil, fmt.Errorf(
				"invalid type %q — use semantic, episodic, procedural, or preference", in.Type)
		}
		memType = parsed
	}

	node := graph.MemNode(content, memType, time.Now().Unix())
	if expires := strings.TrimSpace(in.Expires); expires != "" {
		exp, err := graph.ParseExpiry(expires)
		if err != nil {
			return nil, err
		}
		node.Exp = exp
	}

	records := []graph.Record{graph.NodeRecord(node)}

	for _, tag := range normalizeTags(in.Tags) {
		topic := graph.TopicNode(tag)
		records = append(records,
			graph.NodeRecord(topic),
			graph.EdgeRecord(graph.Edge{From: node.ID, To: topic.ID, Kind: graph.EdgeTagged, W: 1}))
	}

	g, _, err := s.Graph()
	if err != nil {
		return nil, err
	}

	for _, path := range in.Files {
		rel := s.RelPath(path)
		if rel == "" {
			continue
		}
		target, ok := g.FindFile(rel)
		if !ok {
			// A file outside the graph is still worth binding to: extraction
			// will create the node later and the edge resolves then.
			hash, loc, err := graph.FileHashAndLOC(filepath.Join(s.Root, rel))
			if err != nil {
				continue
			}
			target = graph.FileNode(rel, "", loc, hash, "")
			records = append(records, graph.NodeRecord(target))
		}
		records = append(records, graph.EdgeRecord(
			graph.Edge{From: node.ID, To: target.ID, Kind: graph.EdgeExplains, W: 1}))
	}

	for _, name := range in.Symbols {
		for _, sym := range g.FindSymbols(name) {
			if !strings.EqualFold(sym.Name, strings.TrimSpace(name)) {
				continue
			}
			records = append(records, graph.EdgeRecord(
				graph.Edge{From: node.ID, To: sym.ID, Kind: graph.EdgeExplains, W: 1}))
		}
	}

	if sup := strings.TrimSpace(in.Supersedes); sup != "" {
		records = append(records, graph.EdgeRecord(
			graph.Edge{From: node.ID, To: sup, Kind: graph.EdgeSupersedes, W: 1}))
	}

	if err := graph.Append(s.Paths.Log, records); err != nil {
		return nil, fmt.Errorf("write memory: %w", err)
	}
	s.AutoCompact()
	return node, nil
}

// Describe attaches or updates an agent-authored summary on a file node.
func (s *Session) Describe(path, summary, patterns, logic string, tags []string) (*graph.Node, error) {
	rel := s.RelPath(path)
	if rel == "" {
		return nil, fmt.Errorf("path is required")
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, fmt.Errorf("summary is required")
	}

	// A missing file is not fatal: agents legitimately describe planned files.
	// The placeholder hash makes the node read back as stale.
	hash, loc, err := graph.FileHashAndLOC(filepath.Join(s.Root, rel))
	if err != nil {
		hash, loc = "000000", 0
	}

	node := graph.FileNode(rel, summary, loc, hash, "")
	node.SetMeta(graph.MetaOrigin, graph.OriginAgent)
	node.SetMeta(graph.MetaPatterns, strings.TrimSpace(patterns))
	node.SetMeta(graph.MetaLogic, strings.TrimSpace(logic))

	records := []graph.Record{graph.NodeRecord(node)}
	for _, tag := range normalizeTags(tags) {
		topic := graph.TopicNode(tag)
		records = append(records,
			graph.NodeRecord(topic),
			graph.EdgeRecord(graph.Edge{From: node.ID, To: topic.ID, Kind: graph.EdgeTagged, W: 1}))
	}

	if err := graph.Append(s.Paths.Log, records); err != nil {
		return nil, fmt.Errorf("write summary: %w", err)
	}
	s.AutoCompact()
	return node, nil
}

// BuildReport summarizes an extraction pass.
type BuildReport struct {
	Files    int
	Symbols  int
	Docs     int
	Edges    int
	Nodes    int
	Skipped  int
	Duration time.Duration
}

// Build re-extracts the repository and replaces every machine-derived node.
func (s *Session) Build() (BuildReport, error) {
	if !s.Cfg.Graph.Enabled {
		return BuildReport{}, fmt.Errorf("graph extraction is disabled — set graph.enabled = true in %s", s.Paths.Config)
	}

	start := time.Now()
	result, err := extract.Repo(s.Root, s.Cfg)
	if err != nil {
		return BuildReport{}, err
	}

	g, err := graph.Rebuild(s.Paths, s.Cfg, result.Nodes, result.Edges)
	if err != nil {
		return BuildReport{}, err
	}

	counts := g.CountByKind()
	return BuildReport{
		Files:    counts[graph.KindFile.String()],
		Symbols:  counts[graph.KindSym.String()],
		Docs:     counts[graph.KindDoc.String()],
		Edges:    g.EdgeCount(),
		Nodes:    g.NodeCount(),
		Skipped:  result.FilesSkipped,
		Duration: time.Since(start),
	}, nil
}

// Status reports node counts, staleness, and footprint.
func (s *Session) Status() (graph.Report, error) {
	g, err := graph.Load(s.Paths)
	if err != nil {
		return graph.Report{}, err
	}
	return graph.Status(s.Paths, s.Root, g, s.Cfg)
}

// AutoCompact folds the log into the snapshot once it grows past the threshold.
// It swallows its error and writes nothing to stdout: an opportunistic
// housekeeping step must never fail a tool call or corrupt MCP stdio framing.
func (s *Session) AutoCompact() {
	if _, _, _, err := graph.AutoCompact(s.Paths, s.Cfg); err != nil {
		fmt.Fprintf(os.Stderr, "memor: auto-compaction skipped: %v\n", err)
	}
}

// RelPath normalizes a caller-supplied path to a slash-separated path relative
// to the project root, so every code path agrees on node identity.
func (s *Session) RelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(s.Root, p); err == nil {
			p = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// normalizeTags trims, lowercases, and drops empty or duplicate tags.
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
