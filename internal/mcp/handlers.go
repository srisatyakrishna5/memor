package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/retrieve"
	"github.com/memor-dev/memor/internal/session"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Five tools, matching v1's count. Bloated tool sets and ambiguous tool
// selection are a leading agent failure mode, so capability is folded into
// existing tools rather than added alongside them.
//
// Each description carries a behavioural contract, not an API summary. A
// description that tells the model what to do with the result is what changes
// its behaviour; one that only names the arguments does not.
func (s *Server) register(srv *sdk.Server) {
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "repo_map",
		Description: "Load a task-ranked map of this repository: the files that matter, their function and type signatures with exact line ranges, what imports what, what depends on them, and the decisions recorded about them in past conversations. Call this once at the start of a conversation, before reading or searching any file. Pass the user's request verbatim as the query so results are ranked for the task at hand.",
		Annotations: readOnly("Load repository map"),
	}, s.handleRepoMap)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "symbol_find",
		Description: "Locate a function, method, type, or constant by name and get its file, exact line range, signature, callers, and callees. Use this instead of grep or a workspace search: it returns the definition rather than every line that mentions the word. Follow up with symbol_read to see the body.",
		Annotations: readOnly("Find a symbol"),
	}, s.handleSymbolFind)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "symbol_read",
		Description: "Read the exact source lines a symbol occupies, or an explicit line range of a file. Use this instead of reading a whole file: it returns the forty lines you need rather than the four hundred around them. If it reports the file changed since indexing, run 'memor build' and try again.",
		Annotations: readOnly("Read a symbol body"),
	}, s.handleSymbolRead)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "remember",
		Description: "Record something worth knowing in future conversations: a decision and the alternative it rejected, a bug and its root cause, a command or workflow, a stated preference, or a summary of what a file does. Call at the end of a turn. Attach it to the files and symbols it explains so the next conversation finds it in place rather than in a list. Write self-contained sentences that will make sense months from now without this conversation.",
		Annotations: writes("Remember a fact"),
	}, s.handleRemember)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "graph_status",
		Description: "Report the state of this repository's graph: node and edge counts, how many indexed files have drifted from disk, pending writes, on-disk size, and the token budget. Use it to check whether the map is populated and trustworthy before relying on it.",
		Annotations: readOnly("Graph status"),
	}, s.handleStatus)
}

// RepoMapInput selects what slice of the graph to return.
type RepoMapInput struct {
	Query     string   `json:"query,omitempty" jsonschema:"what the user is trying to do, in their own words; used to rank the map for the task"`
	Tags      []string `json:"tags,omitempty" jsonschema:"optional topic tags to boost, such as auth or deploy"`
	OpenFiles []string `json:"openFiles,omitempty" jsonschema:"paths already open in the editor; they anchor the ranking around what the user is looking at"`
	Budget    int      `json:"budget,omitempty" jsonschema:"maximum tokens to return; defaults to the project's configured budget"`
	Limit     int      `json:"limit,omitempty" jsonschema:"maximum nodes to return; omit to let the token budget decide"`
}

func (s *Server) handleRepoMap(_ context.Context, _ *sdk.CallToolRequest, in RepoMapInput) (*sdk.CallToolResult, any, error) {
	sess, err := s.session()
	if err != nil {
		return nil, nil, err
	}
	g, ix, err := sess.Graph()
	if err != nil {
		return nil, nil, fmt.Errorf("load graph: %w", err)
	}
	if g.NodeCount() == 0 {
		return textResult("The graph is empty. Run 'memor build' in a terminal to index this repository, then call repo_map again."), nil, nil
	}

	openFiles := make([]string, 0, len(in.OpenFiles))
	for _, f := range in.OpenFiles {
		openFiles = append(openFiles, sess.RelPath(f))
	}

	result := retrieve.Retrieve(g, ix, sess.Cfg, retrieve.Query{
		Text:      in.Query,
		Tags:      in.Tags,
		OpenFiles: openFiles,
		Budget:    in.Budget,
		Limit:     in.Limit,
	})

	// Underfilling is correct. A short block beats a padded one, because a
	// plausible-but-wrong node degrades the answer more than a missing one.
	if len(result.Nodes) == 0 {
		return textResult("Nothing in the graph is relevant to that query. Read the files you need directly, then call remember to record what you learn."), nil, nil
	}

	if report, err := sess.Status(); err == nil && report.NeedsRebuild() {
		total := report.Fresh + report.Stale + report.Missing
		return textResult(result.Text + fmt.Sprintf(
			"\n! %d of %d indexed files changed since the last build. Run 'memor build' to refresh.\n",
			report.Stale+report.Missing, total)), nil, nil
	}
	return textResult(result.Text), nil, nil
}

// SymbolFindInput names the symbol to locate.
type SymbolFindInput struct {
	Name  string `json:"name" jsonschema:"exact or partial symbol name, such as Compact or Graph.AddNode"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum matches to return; defaults to 10"`
}

// SymbolFindOutput carries located definitions.
type SymbolFindOutput struct {
	Count   int            `json:"count" jsonschema:"number of matches returned"`
	Results []SymbolResult `json:"results" jsonschema:"matching definitions"`
}

// SymbolResult is one located definition.
type SymbolResult struct {
	Name      string   `json:"name" jsonschema:"symbol name"`
	Kind      string   `json:"kind,omitempty" jsonschema:"func, method, type, const, or var"`
	Path      string   `json:"path" jsonschema:"project-relative file path"`
	StartLine int      `json:"startLine" jsonschema:"first line of the definition"`
	EndLine   int      `json:"endLine" jsonschema:"last line of the definition"`
	Signature string   `json:"signature,omitempty" jsonschema:"declaration without its body"`
	Status    string   `json:"status" jsonschema:"fresh when the file matches the index, stale when it changed, missing when it no longer exists"`
	Callers   []string `json:"callers,omitempty" jsonschema:"symbols that call this one"`
	Callees   []string `json:"callees,omitempty" jsonschema:"symbols this one calls"`
	Notes     []string `json:"notes,omitempty" jsonschema:"recorded decisions that explain this symbol"`
}

func (s *Server) handleSymbolFind(_ context.Context, _ *sdk.CallToolRequest, in SymbolFindInput) (*sdk.CallToolResult, SymbolFindOutput, error) {
	sess, err := s.session()
	if err != nil {
		return nil, SymbolFindOutput{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, SymbolFindOutput{}, fmt.Errorf("name is required")
	}

	g, _, err := sess.Graph()
	if err != nil {
		return nil, SymbolFindOutput{}, fmt.Errorf("load graph: %w", err)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	matches := g.FindSymbols(name)
	if len(matches) > limit {
		matches = matches[:limit]
	}

	results := make([]SymbolResult, 0, len(matches))
	for _, n := range matches {
		result := SymbolResult{
			Name:      n.Name,
			Kind:      n.MetaValue(graph.MetaSymKind),
			Signature: n.Text,
			Status:    graph.FileStatus(sess.Root, n),
			Callers:   relatedNames(g, n.ID, graph.EdgeCalls, false),
			Callees:   relatedNames(g, n.ID, graph.EdgeCalls, true),
			Notes:     explainNotes(g, n.ID),
		}
		if n.Span != nil {
			result.Path = n.Span.Path
			result.StartLine = n.Span.L0
			result.EndLine = n.Span.L1
		}
		results = append(results, result)
	}
	return nil, SymbolFindOutput{Count: len(results), Results: results}, nil
}

// SymbolReadInput identifies the body to fetch.
type SymbolReadInput struct {
	Name      string `json:"name,omitempty" jsonschema:"symbol name to read; prefer this over a line range"`
	Path      string `json:"path,omitempty" jsonschema:"project-relative file path; required when reading a line range or disambiguating a symbol"`
	StartLine int    `json:"startLine,omitempty" jsonschema:"first line to read, 1-based; only used when name is omitted"`
	EndLine   int    `json:"endLine,omitempty" jsonschema:"last line to read, inclusive; only used when name is omitted"`
}

// SymbolReadOutput carries the requested source.
type SymbolReadOutput struct {
	Path      string `json:"path" jsonschema:"project-relative file path"`
	StartLine int    `json:"startLine" jsonschema:"first line returned"`
	EndLine   int    `json:"endLine" jsonschema:"last line returned"`
	Source    string `json:"source" jsonschema:"the exact source text"`
	Status    string `json:"status" jsonschema:"fresh when the file matches the index"`
}

func (s *Server) handleSymbolRead(_ context.Context, _ *sdk.CallToolRequest, in SymbolReadInput) (*sdk.CallToolResult, SymbolReadOutput, error) {
	sess, err := s.session()
	if err != nil {
		return nil, SymbolReadOutput{}, err
	}

	name := strings.TrimSpace(in.Name)
	path := sess.RelPath(in.Path)
	if name == "" && path == "" {
		return nil, SymbolReadOutput{}, fmt.Errorf("provide either a symbol name or a file path")
	}

	if name == "" {
		source, err := graph.ReadLines(sess.Root, path, in.StartLine, in.EndLine)
		if err != nil {
			return nil, SymbolReadOutput{}, err
		}
		return nil, SymbolReadOutput{
			Path:      path,
			StartLine: in.StartLine,
			EndLine:   in.EndLine,
			Source:    source,
			Status:    graph.StatusFresh,
		}, nil
	}

	g, _, err := sess.Graph()
	if err != nil {
		return nil, SymbolReadOutput{}, fmt.Errorf("load graph: %w", err)
	}

	var target *graph.Node
	for _, candidate := range g.FindSymbols(name) {
		if path != "" && (candidate.Span == nil || candidate.Span.Path != path) {
			continue
		}
		target = candidate
		break
	}
	if target == nil || target.Span == nil {
		return nil, SymbolReadOutput{}, fmt.Errorf(
			"no indexed symbol named %q — call symbol_find first, or run 'memor build' if the graph is out of date", name)
	}

	source, err := graph.ReadSpan(sess.Root, target.Span)
	if err != nil {
		return nil, SymbolReadOutput{}, err
	}
	_ = graph.CacheBody(sess.Paths, sess.Cfg, source)

	return nil, SymbolReadOutput{
		Path:      target.Span.Path,
		StartLine: target.Span.L0,
		EndLine:   target.Span.L1,
		Source:    source,
		Status:    graph.FileStatus(sess.Root, target),
	}, nil
}

// RememberInput describes a new memory or file summary.
type RememberInput struct {
	Content    string   `json:"content,omitempty" jsonschema:"the fact to remember, written as a self-contained sentence that will make sense without this conversation"`
	Type       string   `json:"type,omitempty" jsonschema:"semantic for decisions and architecture, episodic for bugs fixed and events, procedural for commands and workflows, preference for style conventions; defaults to semantic"`
	Tags       []string `json:"tags,omitempty" jsonschema:"short lowercase topic tags such as auth, deploy, or db"`
	Files      []string `json:"files,omitempty" jsonschema:"project-relative paths this fact explains; the fact will surface on those files in future repo_map calls"`
	Symbols    []string `json:"symbols,omitempty" jsonschema:"symbol names this fact explains"`
	Expires    string   `json:"expires,omitempty" jsonschema:"optional expiry as YYYY-MM-DD or a day count such as 30d; use for temporary workarounds"`
	Supersedes string   `json:"supersedes,omitempty" jsonschema:"id of an existing memory this replaces"`
	Summary    string   `json:"summary,omitempty" jsonschema:"when describing one file, a one-line statement of what it does and why it exists; requires exactly one entry in files"`
	Patterns   string   `json:"patterns,omitempty" jsonschema:"conventions callers must follow when using that file"`
	Logic      string   `json:"logic,omitempty" jsonschema:"step-by-step flow, for a file whose control flow is not obvious"`
}

// RememberOutput reports what was stored. The ID is derived from the content,
// so re-recording the same fact returns the same ID instead of duplicating it.
type RememberOutput struct {
	ID       string   `json:"id" jsonschema:"content-addressed id of the stored node"`
	Kind     string   `json:"kind" jsonschema:"memory or file"`
	Type     string   `json:"type,omitempty" jsonschema:"resolved memory type"`
	Attached []string `json:"attached,omitempty" jsonschema:"files and symbols this fact was bound to"`
}

func (s *Server) handleRemember(_ context.Context, _ *sdk.CallToolRequest, in RememberInput) (*sdk.CallToolResult, RememberOutput, error) {
	sess, err := s.session()
	if err != nil {
		return nil, RememberOutput{}, err
	}

	// Describing a file and recording a fact are the same intent at different
	// granularities, so they are one tool rather than two.
	if summary := strings.TrimSpace(in.Summary); summary != "" {
		if len(in.Files) != 1 {
			return nil, RememberOutput{}, fmt.Errorf("summary requires exactly one entry in files")
		}
		node, err := sess.Describe(in.Files[0], summary, in.Patterns, in.Logic, in.Tags)
		if err != nil {
			return nil, RememberOutput{}, err
		}
		return nil, RememberOutput{ID: node.ID, Kind: "file", Attached: []string{node.Name}}, nil
	}

	node, err := sess.Remember(session.RememberInput{
		Content:    in.Content,
		Type:       in.Type,
		Tags:       in.Tags,
		Files:      in.Files,
		Symbols:    in.Symbols,
		Expires:    in.Expires,
		Supersedes: in.Supersedes,
	})
	if err != nil {
		return nil, RememberOutput{}, err
	}

	attached := append(append([]string{}, in.Files...), in.Symbols...)
	return nil, RememberOutput{
		ID:       node.ID,
		Kind:     "memory",
		Type:     graph.MemTypeName(node.MemType()),
		Attached: attached,
	}, nil
}

// StatusOutput summarizes the graph.
type StatusOutput struct {
	Nodes       int            `json:"nodes" jsonschema:"total nodes in the graph"`
	Edges       int            `json:"edges" jsonschema:"total edges"`
	ByKind      map[string]int `json:"byKind,omitempty" jsonschema:"node counts keyed by kind"`
	Pending     int            `json:"pending" jsonschema:"records written but not yet compacted"`
	Fresh       int            `json:"fresh" jsonschema:"indexed files that still match disk"`
	Stale       int            `json:"stale" jsonschema:"indexed files that changed since the last build"`
	Missing     int            `json:"missing" jsonschema:"indexed files that no longer exist"`
	Bytes       int64          `json:"bytes" jsonschema:"on-disk size of the .memor directory"`
	TokenBudget int            `json:"tokenBudget" jsonschema:"configured retrieval token budget"`
	Advice      string         `json:"advice" jsonschema:"what to do next given the current state"`
}

func (s *Server) handleStatus(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, StatusOutput, error) {
	sess, err := s.session()
	if err != nil {
		return nil, StatusOutput{}, err
	}
	report, err := sess.Status()
	if err != nil {
		return nil, StatusOutput{}, fmt.Errorf("read status: %w", err)
	}

	out := StatusOutput{
		Nodes:       report.Nodes,
		Edges:       report.Edges,
		ByKind:      report.ByKind,
		Pending:     report.Pending,
		Fresh:       report.Fresh,
		Stale:       report.Stale,
		Missing:     report.Missing,
		Bytes:       report.Bytes,
		TokenBudget: report.TokenBudget,
	}
	switch {
	case report.Nodes == 0:
		out.Advice = "The graph is empty. Run 'memor build' to index this repository."
	case report.NeedsRebuild():
		out.Advice = "Enough indexed files have drifted that the map may mislead you. Run 'memor build'."
	default:
		out.Advice = "The graph is current. Call repo_map to load it."
	}
	return nil, out, nil
}

func relatedNames(g *graph.Graph, id string, kind graph.EdgeKind, outgoing bool) []string {
	edges := g.In(id)
	if outgoing {
		edges = g.Out(id)
	}

	var out []string
	for _, e := range edges {
		if e.Kind != kind {
			continue
		}
		other := e.From
		if outgoing {
			other = e.To
		}
		if n, ok := g.Node(other); ok {
			out = append(out, n.Name)
		}
	}
	return out
}

func explainNotes(g *graph.Graph, id string) []string {
	var out []string
	for _, e := range g.In(id) {
		if e.Kind != graph.EdgeExplains {
			continue
		}
		if n, ok := g.Node(e.From); ok {
			out = append(out, n.Text)
		}
	}
	return out
}
