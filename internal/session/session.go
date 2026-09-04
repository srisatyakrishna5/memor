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
	"github.com/memor-dev/memor/internal/vcs"
)

// Session is one project's resolved state.
type Session struct {
	Root  string
	Paths store.Paths
	Cfg   config.Config
}

// Open resolves the project root by walking up from start and loads its config.
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
	return &Session{Root: root, Paths: paths, Cfg: cfg}, nil
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

// Graph loads the store and builds its term index.
func (s *Session) Graph() (*graph.Graph, *graph.Index, error) {
	g, err := graph.Load(s.Paths)
	if err != nil {
		return nil, nil, err
	}
	return g, graph.BuildIndex(g), nil
}

// RememberInput describes a fact to record.
type RememberInput struct {
	Content    string
	Type       string
	Tags       []string
	Files      []string // nodes this memory explains
	Symbols    []string
	Expires    string
	Supersedes string
	Task       string
	Status     string
}

// Remember records a memory node with its tags and attachments.
//
// Binding a memory to the exact file or symbol it concerns is what makes it
// surface at the moment a future agent would otherwise repeat the mistake.
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
	node.SetMetaList(graph.MetaTags, normalizeTags(in.Tags))
	node.SetMeta(graph.MetaTask, strings.TrimSpace(in.Task))
	if status, err := normalizeStatus(in.Status); err != nil {
		return nil, err
	} else {
		node.SetMeta(graph.MetaStatus, status)
	}
	if head, err := vcs.ReadHead(s.Root); err == nil {
		node.SetMeta(graph.MetaCommit, head.SHA)
	}

	g, _, err := s.Graph()
	if err != nil {
		return nil, err
	}

	records := []graph.Record{}
	var attached []string

	for _, path := range in.Files {
		rel := s.RelPath(path)
		if rel == "" {
			continue
		}
		target, ok := g.FindFile(rel)
		if !ok {
			// A file outside the store is still worth binding to: extraction
			// creates the node later and the attachment resolves then, because
			// the ID is derived from the path rather than assigned.
			hash, loc, err := graph.FileHashAndLOC(filepath.Join(s.Root, rel))
			if err != nil {
				continue
			}
			target = graph.FileNode(rel, "", loc, hash, "")
			records = append(records, graph.NodeRecord(target))
		}
		attached = append(attached, target.ID)
	}

	for _, name := range in.Symbols {
		for _, sym := range g.FindSymbols(name) {
			if strings.EqualFold(sym.Name, strings.TrimSpace(name)) {
				attached = append(attached, sym.ID)
			}
		}
	}

	node.SetMetaList(graph.MetaExplains, attached)
	if sup := strings.TrimSpace(in.Supersedes); sup != "" {
		node.SetMetaList(graph.MetaSupersedes, []string{sup})
	}

	records = append(records, graph.NodeRecord(node))
	if err := graph.Append(s.Paths.Log, records); err != nil {
		return nil, fmt.Errorf("write memory: %w", err)
	}
	s.AutoCompact()
	return node, nil
}

func normalizeStatus(status string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return "", nil
	case graph.StatusOpen, graph.StatusDone, graph.StatusBlocked:
		return strings.ToLower(strings.TrimSpace(status)), nil
	default:
		return "", fmt.Errorf("invalid status %q — use open, done, or blocked", status)
	}
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
	node.SetMetaList(graph.MetaTags, normalizeTags(tags))

	if err := graph.Append(s.Paths.Log, []graph.Record{graph.NodeRecord(node)}); err != nil {
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
	Nodes    int
	Skipped  int
	Commit   string
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

	g, err := graph.Rebuild(s.Paths, s.Cfg, result.Nodes)
	if err != nil {
		return BuildReport{}, err
	}

	counts := g.CountByKind()
	report := BuildReport{
		Files:    counts[graph.KindFile.String()],
		Symbols:  counts[graph.KindSym.String()],
		Docs:     counts[graph.KindDoc.String()],
		Nodes:    g.NodeCount(),
		Skipped:  result.FilesSkipped,
		Duration: time.Since(start),
	}

	// The commit is written last and only on success, so a failed build can
	// never leave a watermark claiming the repository was indexed at it.
	state := store.State{
		IndexedAt:   time.Now().Unix(),
		FileCount:   report.Files,
		SymbolCount: report.Symbols,
	}
	if head, err := vcs.ReadHead(s.Root); err == nil {
		state.IndexedCommit = head.SHA
		report.Commit = head.SHA
	}
	if err := store.WriteState(s.Paths.State, state); err != nil {
		return report, fmt.Errorf("write state: %w", err)
	}
	return report, nil
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
