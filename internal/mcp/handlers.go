package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/retrieve"
	"github.com/memor-dev/memor/internal/session"
	"github.com/memor-dev/memor/internal/vcs"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Seven tools arranged as an escalation ladder, cheapest first. Bloated tool
// sets and ambiguous tool selection are a leading agent failure mode, so each
// tool answers a question the one above it could not.
//
// Each description carries a behavioural contract, not an API summary. A
// description that tells the model what to do with the result is what changes
// its behaviour; one that only names the arguments does not.
func (s *Server) register(srv *sdk.Server) {
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "repo_brief",
		Description: "START HERE. Call this first in every conversation, before reading, searching, or listing anything. Costs a few hundred tokens and tells you what this repository is, its top-level layout, which files changed since you last worked here, and what you left unfinished. Changes you have already been shown are marked seen; if nothing is unseen, trust what you already know instead of re-reading the code.",
		Annotations: readOnly("Repository brief"),
	}, s.handleBrief)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "repo_changes",
		Description: "List the files that changed since a given commit, or since you last called repo_brief, each with what it does, whether the index still matches it, and whether you have seen it before. Use this after repo_brief when you need to know exactly which files moved. It is far cheaper than diffing or re-reading the tree.",
		Annotations: readOnly("Repository changes"),
	}, s.handleChanges)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "repo_map",
		Description: "Load a task-ranked view of the code: the files that matter for this request, their function and type signatures with exact line ranges, what they import, what depends on them, and the decisions recorded about them. Use it when you need code you have not seen yet. Pass the user's request verbatim as the query, and narrow with paths when you already know the area.",
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
		Description: "Record something worth knowing in future conversations: a decision and the alternative it rejected, a bug and its root cause, a command or workflow, a stated preference, or a summary of what a file does. Call at the end of a turn. Set task and status to leave a note about unfinished work, which repo_brief hands back next session. Attach it to the files and symbols it explains, and write self-contained sentences that will make sense months from now.",
		Annotations: writes("Remember a fact"),
	}, s.handleRemember)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "memor_status",
		Description: "Report whether memor's index can be trusted: node counts, how many indexed files have drifted from disk, which commit was indexed and how far behind HEAD it is, pending writes, and on-disk size. Call it if another tool returns something that looks wrong.",
		Annotations: readOnly("Memor status"),
	}, s.handleStatus)
}

// BriefInput identifies the calling agent so watermarks stay per-agent.
type BriefInput struct {
	Agent   string `json:"agent,omitempty" jsonschema:"stable identifier for you as a client, so 'what changed since last time' is tracked per agent; omit to use the shared default"`
	Journal int    `json:"journal,omitempty" jsonschema:"maximum unfinished journal entries to return; defaults to 5"`
}

func (s *Server) handleBrief(_ context.Context, _ *sdk.CallToolRequest, in BriefInput) (*sdk.CallToolResult, session.Brief, error) {
	sess, err := s.session()
	if err != nil {
		return nil, session.Brief{}, err
	}
	// Reading the brief is what makes it the agent's new baseline, so the
	// watermark advances here rather than requiring a second call to confirm.
	brief, err := sess.Brief(session.BriefOptions{
		Agent:   in.Agent,
		Journal: in.Journal,
		Advance: true,
	})
	if err != nil {
		return nil, session.Brief{}, fmt.Errorf("read brief: %w", err)
	}
	s.invalidate()
	return nil, brief, nil
}

// ChangesInput selects the comparison point.
type ChangesInput struct {
	Agent string `json:"agent,omitempty" jsonschema:"the same client identifier passed to repo_brief, so changes you have already been shown are marked seen"`
	Since string `json:"since,omitempty" jsonschema:"commit SHA to compare against; omit to compare against the commit memor last indexed"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum changed paths to return; defaults to 50"`
}

func (s *Server) handleChanges(_ context.Context, _ *sdk.CallToolRequest, in ChangesInput) (*sdk.CallToolResult, session.ChangeSet, error) {
	sess, err := s.session()
	if err != nil {
		return nil, session.ChangeSet{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	set, err := sess.Changes(in.Agent, strings.TrimSpace(in.Since), limit)
	if err != nil {
		return nil, session.ChangeSet{}, fmt.Errorf("read changes: %w", err)
	}
	return nil, set, nil
}

// RepoMapInput selects what slice of the repository to return.
type RepoMapInput struct {
	Query     string   `json:"query,omitempty" jsonschema:"what the user is trying to do, in their own words; used to rank the map for the task"`
	Paths     []string `json:"paths,omitempty" jsonschema:"restrict results to these directory or file path prefixes"`
	Tags      []string `json:"tags,omitempty" jsonschema:"optional topic tags to boost, such as auth or deploy"`
	OpenFiles []string `json:"openFiles,omitempty" jsonschema:"paths already open in the editor; they anchor the ranking around what the user is looking at"`
	Budget    int      `json:"budget,omitempty" jsonschema:"maximum tokens to return; defaults to 2500"`
	Limit     int      `json:"limit,omitempty" jsonschema:"maximum nodes to return; omit to let the token budget decide"`
}

func (s *Server) handleRepoMap(_ context.Context, _ *sdk.CallToolRequest, in RepoMapInput) (*sdk.CallToolResult, any, error) {
	sess, g, ix, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	if g.NodeCount() == 0 {
		return textResult("Nothing is indexed. Run 'memor build' in a terminal to index this repository, then call repo_map again."), nil, nil
	}

	openFiles := make([]string, 0, len(in.OpenFiles))
	for _, f := range in.OpenFiles {
		openFiles = append(openFiles, sess.RelPath(f))
	}
	paths := make([]string, 0, len(in.Paths))
	for _, p := range in.Paths {
		paths = append(paths, sess.RelPath(p))
	}

	budget := in.Budget
	if budget <= 0 {
		budget = constants.DefaultMapBudget
	}

	result := retrieve.Retrieve(g, ix, sess.Cfg, retrieve.Query{
		Text:      in.Query,
		Tags:      in.Tags,
		OpenFiles: openFiles,
		Paths:     paths,
		Budget:    budget,
		Limit:     in.Limit,
	})

	// Underfilling is correct. A short block beats a padded one, because a
	// plausible-but-wrong node degrades the answer more than a missing one.
	if len(result.Nodes) == 0 {
		return textResult("Nothing indexed is relevant to that query. Read the specific file you need, then call remember to record what you learn."), nil, nil
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
	Limit int    `json:"limit,omitempty" jsonschema:"maximum matches to return; defaults to 5"`
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
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, SymbolFindOutput{}, fmt.Errorf("name is required")
	}

	sess, g, _, err := s.load()
	if err != nil {
		return nil, SymbolFindOutput{}, err
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 5
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
			Callers:   capList(n.MetaList(graph.MetaCallers)),
			Callees:   capList(n.MetaList(graph.MetaCalls)),
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

// maxRelated caps caller and callee lists. A widely used helper has dozens of
// callers and listing them all costs more than it tells the agent.
const maxRelated = 8

func capList(values []string) []string {
	if len(values) > maxRelated {
		return values[:maxRelated]
	}
	return values
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
	sess, g, _, err := s.load()
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
			"no indexed symbol named %q — call symbol_find first, or run 'memor build' if the index is out of date", name)
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
	Task       string   `json:"task,omitempty" jsonschema:"short label for the piece of work this note belongs to, such as 'migrate auth to oauth'"`
	Status     string   `json:"status,omitempty" jsonschema:"open, done, or blocked; open and blocked entries are returned by repo_brief next session"`
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
		s.invalidate()
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
		Task:       in.Task,
		Status:     in.Status,
	})
	if err != nil {
		return nil, RememberOutput{}, err
	}
	s.invalidate()

	attached := append(append([]string{}, in.Files...), in.Symbols...)
	return nil, RememberOutput{
		ID:       node.ID,
		Kind:     "memory",
		Type:     graph.MemTypeName(node.MemType()),
		Attached: attached,
	}, nil
}

// StatusOutput summarizes whether the index can be trusted.
type StatusOutput struct {
	Nodes         int            `json:"nodes" jsonschema:"total indexed nodes"`
	ByKind        map[string]int `json:"byKind,omitempty" jsonschema:"node counts keyed by kind"`
	Pending       int            `json:"pending" jsonschema:"records written but not yet compacted"`
	Fresh         int            `json:"fresh" jsonschema:"indexed files that still match disk"`
	Stale         int            `json:"stale" jsonschema:"indexed files that changed since the last build"`
	Missing       int            `json:"missing" jsonschema:"indexed files that no longer exist"`
	IndexedCommit string         `json:"indexedCommit,omitempty" jsonschema:"commit the last build indexed"`
	CommitsBehind int            `json:"commitsBehind,omitempty" jsonschema:"commits made since the index was built"`
	Bytes         int64          `json:"bytes" jsonschema:"on-disk size of the .memor directory"`
	TokenBudget   int            `json:"tokenBudget" jsonschema:"configured retrieval token budget"`
	Advice        string         `json:"advice" jsonschema:"what to do next given the current state"`
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
		Nodes:         report.Nodes,
		ByKind:        report.ByKind,
		Pending:       report.Pending,
		Fresh:         report.Fresh,
		Stale:         report.Stale,
		Missing:       report.Missing,
		IndexedCommit: report.IndexedCommit,
		Bytes:         report.Bytes,
		TokenBudget:   report.TokenBudget,
	}
	if report.IndexedCommit != "" {
		if behind, err := vcs.CommitsBetween(sess.Root, report.IndexedCommit); err == nil {
			out.CommitsBehind = behind
		}
	}
	switch {
	case report.Nodes == 0:
		out.Advice = "Nothing is indexed. Run 'memor build' to index this repository."
	case report.NeedsRebuild():
		out.Advice = "Enough indexed files have drifted that the map may mislead you. Run 'memor build'."
	case out.CommitsBehind > 0:
		out.Advice = fmt.Sprintf("The index is %d commits behind HEAD. Run 'memor build' to refresh it.", out.CommitsBehind)
	default:
		out.Advice = "The index is current. Call repo_brief to orient, then repo_map for a task-scoped view."
	}
	return nil, out, nil
}

// explainNotes returns the recorded decisions attached to a node.
func explainNotes(g *graph.Graph, id string) []string {
	memories := g.Explaining(id)
	out := make([]string, 0, len(memories))
	for _, m := range memories {
		out = append(out, m.Text)
	}
	return capList(out)
}
