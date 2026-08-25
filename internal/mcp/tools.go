package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/engine"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Status values reported by code_get.
const statusUnrecorded = "unrecorded"

func (s *Server) register(srv *sdk.Server) {
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "memory_context",
		Description: "Load this project's accumulated memory: past decisions, architecture, conventions, workflows, and known pitfalls. Call this once at the start of a conversation, before reading any files. Pass the user's request as the query so results are ranked for the task at hand.",
		Annotations: readOnly("Load project memory"),
	}, s.handleContext)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "memory_add",
		Description: "Record something worth remembering in future conversations: a decision and its rationale, a bug and its root cause, a command or workflow, or a stated preference. Call at the end of a turn. Write self-contained sentences that will make sense months later without this conversation. Do not record trivia or restate what is already in memory.",
		Annotations: writes("Remember a fact"),
	}, s.handleAdd)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "memory_search",
		Description: "Search project memory for a specific topic and get matching entries with their IDs. Use when you need targeted recall mid-conversation, or to find the ID of an entry that a new memory should supersede.",
		Annotations: readOnly("Search memory"),
	}, s.handleSearch)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "code_get",
		Description: "Look up the stored summary of a source file before reading it. If status is \"fresh\", the summary matches the file on disk and you can skip the read. If status is \"stale\" or \"unrecorded\", read the file and then call code_save.",
		Annotations: readOnly("Get cached file summary"),
	}, s.handleCodeGet)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "code_save",
		Description: "Store a structured summary of a source file after reading or editing it, so future conversations can understand the file without re-reading it. The file is hashed at save time so staleness can be detected later.",
		Annotations: writes("Save file summary"),
	}, s.handleCodeSave)

	sdk.AddTool(srv, &sdk.Tool{
		Name:        "memory_stats",
		Description: "Report how much memory this project holds: entry counts by type, pending writes, indexed knowledge documents, and the token budget. Use to check whether memory is populated or approaching its budget.",
		Annotations: readOnly("Memory statistics"),
	}, s.handleStats)
}

// ContextInput selects what slice of memory to return.
type ContextInput struct {
	Query  string   `json:"query,omitempty" jsonschema:"what the user is trying to do, in their own words; used to rank memories by relevance"`
	Tags   []string `json:"tags,omitempty" jsonschema:"optional topic tags to boost, such as auth or deploy"`
	Budget int      `json:"budget,omitempty" jsonschema:"maximum tokens to return; defaults to the project's configured budget"`
}

func (s *Server) handleContext(_ context.Context, _ *sdk.CallToolRequest, in ContextInput) (*sdk.CallToolResult, any, error) {
	paths, _, cfg, err := s.session()
	if err != nil {
		return nil, nil, err
	}

	out, err := engine.Context(paths, cfg, engine.ContextOptions{
		Budget: in.Budget,
		Query:  strings.TrimSpace(in.Query),
		Tags:   normalizeTags(in.Tags),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build context: %w", err)
	}

	if !hasContent(out) {
		return textResult("No project memory recorded yet. Record decisions, fixes, and conventions with memory_add as you work."), nil, nil
	}
	return textResult(out), nil, nil
}

// hasContent reports whether a rendered context block carries anything useful.
// Memory lines start with a type prefix such as @s or @c, and knowledge
// sections start with a markdown heading, so an absence of both means the
// header is all the engine produced.
func hasContent(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "@") || strings.HasPrefix(line, "## ") {
			return true
		}
	}
	return false
}

// AddInput describes a new memory.
type AddInput struct {
	Content    string   `json:"content" jsonschema:"the fact to remember, written as a self-contained sentence that will make sense without this conversation"`
	Type       string   `json:"type,omitempty" jsonschema:"semantic for decisions and architecture, episodic for bugs fixed and events, procedural for commands and workflows, preference for style conventions; defaults to semantic"`
	Tags       []string `json:"tags,omitempty" jsonschema:"short lowercase topic tags such as auth, deploy, or db"`
	Expires    string   `json:"expires,omitempty" jsonschema:"optional expiry as YYYY-MM-DD or a day count such as 30d; use for temporary workarounds"`
	Supersedes string   `json:"supersedes,omitempty" jsonschema:"id of an existing memory this replaces, obtained from memory_search"`
}

// AddOutput reports the stored entry. ID is derived from content, so re-adding
// the same fact returns the same ID instead of creating a duplicate.
type AddOutput struct {
	ID   string   `json:"id" jsonschema:"content-addressed id of the stored memory"`
	Type string   `json:"type" jsonschema:"resolved memory type"`
	Tags []string `json:"tags,omitempty" jsonschema:"normalized tags"`
}

func (s *Server) handleAdd(_ context.Context, _ *sdk.CallToolRequest, in AddInput) (*sdk.CallToolResult, AddOutput, error) {
	paths, _, cfg, err := s.session()
	if err != nil {
		return nil, AddOutput{}, err
	}

	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, AddOutput{}, fmt.Errorf("content is required")
	}

	entryType := memory.TypeSemantic
	if t := strings.TrimSpace(in.Type); t != "" {
		parsed := memory.ParseType(t)
		if parsed == "" {
			return nil, AddOutput{}, fmt.Errorf(
				"invalid type %q — use semantic, episodic, procedural, or preference", in.Type)
		}
		entryType = parsed
	}

	entry := memory.Entry{
		Timestamp:  time.Now().Unix(),
		Type:       entryType,
		ID:         memory.ContentID(content),
		Tags:       normalizeTags(in.Tags),
		Content:    content,
		Supersedes: strings.TrimSpace(in.Supersedes),
	}

	if expires := strings.TrimSpace(in.Expires); expires != "" {
		expiry, err := memory.ParseExpiry(expires)
		if err != nil {
			return nil, AddOutput{}, fmt.Errorf(
				"invalid expires %q — use YYYY-MM-DD or a day count such as 30d", in.Expires)
		}
		entry.Expires = expiry
	}

	if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
		return nil, AddOutput{}, fmt.Errorf("write memory: %w", err)
	}
	compact(paths, cfg)

	return nil, AddOutput{ID: entry.ID, Type: entry.Type.FullName(), Tags: entry.Tags}, nil
}

// SearchInput is a relevance query over stored memories.
type SearchInput struct {
	Query string `json:"query" jsonschema:"keywords describing what to recall"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum entries to return; defaults to 10"`
}

// SearchOutput carries ranked matches.
type SearchOutput struct {
	Count   int            `json:"count" jsonschema:"number of entries returned"`
	Results []SearchResult `json:"results" jsonschema:"matching memories, most relevant first"`
}

// SearchResult is a single ranked memory.
type SearchResult struct {
	ID      string   `json:"id" jsonschema:"content-addressed id, usable as memory_add supersedes"`
	Type    string   `json:"type" jsonschema:"memory type"`
	Tags    []string `json:"tags,omitempty" jsonschema:"topic tags"`
	Content string   `json:"content" jsonschema:"the remembered fact"`
	Score   float64  `json:"score" jsonschema:"relevance score, higher is more relevant"`
}

func (s *Server) handleSearch(_ context.Context, _ *sdk.CallToolRequest, in SearchInput) (*sdk.CallToolResult, SearchOutput, error) {
	paths, _, cfg, err := s.session()
	if err != nil {
		return nil, SearchOutput{}, err
	}

	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, SearchOutput{}, fmt.Errorf("query is required")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	ranked, err := engine.Search(paths, cfg, query, limit)
	if err != nil {
		return nil, SearchOutput{}, fmt.Errorf("search memory: %w", err)
	}

	results := make([]SearchResult, 0, len(ranked))
	for _, se := range ranked {
		content := se.Content
		if se.Type == memory.TypeCode && se.Meta != nil && se.Meta.Summary != "" {
			content = fmt.Sprintf("%s — %s", se.Meta.FilePath, se.Meta.Summary)
		}
		results = append(results, SearchResult{
			ID:      se.ID,
			Type:    se.Type.FullName(),
			Tags:    se.Tags,
			Content: content,
			Score:   se.Score,
		})
	}

	return nil, SearchOutput{Count: len(results), Results: results}, nil
}

// CodeGetInput identifies the file whose summary is wanted.
type CodeGetInput struct {
	Path string `json:"path" jsonschema:"path to the source file, relative to the project root"`
}

// CodeGetOutput reports the stored summary and whether it still matches disk.
type CodeGetOutput struct {
	Path           string   `json:"path" jsonschema:"normalized project-relative path"`
	Found          bool     `json:"found" jsonschema:"whether a summary is stored for this file"`
	Status         string   `json:"status" jsonschema:"fresh when the summary matches the file on disk, stale when the file changed since it was written, missing when the file no longer exists, unrecorded when nothing is stored"`
	Recommendation string   `json:"recommendation" jsonschema:"what to do next given the status"`
	LOC            int      `json:"loc,omitempty" jsonschema:"line count when the summary was written"`
	Exports        []string `json:"exports,omitempty" jsonschema:"exported symbols"`
	Deps           []string `json:"deps,omitempty" jsonschema:"files this one depends on"`
	Summary        string   `json:"summary,omitempty" jsonschema:"one-line description of the file"`
	Patterns       string   `json:"patterns,omitempty" jsonschema:"usage patterns and conventions"`
	Logic          string   `json:"logic,omitempty" jsonschema:"step-by-step logic flow"`
}

func (s *Server) handleCodeGet(_ context.Context, _ *sdk.CallToolRequest, in CodeGetInput) (*sdk.CallToolResult, CodeGetOutput, error) {
	paths, root, _, err := s.session()
	if err != nil {
		return nil, CodeGetOutput{}, err
	}

	path := relPath(root, in.Path)
	if path == "" {
		return nil, CodeGetOutput{}, fmt.Errorf("path is required")
	}

	entry, err := engine.FindCodeEntry(paths, path)
	if err != nil {
		return nil, CodeGetOutput{}, fmt.Errorf("read code summaries: %w", err)
	}
	if entry == nil {
		return nil, CodeGetOutput{
			Path:           path,
			Found:          false,
			Status:         statusUnrecorded,
			Recommendation: "No summary stored. Read the file, then call code_save.",
		}, nil
	}

	meta := entry.Meta
	status := engine.CodeStatus(root, meta)
	out := CodeGetOutput{
		Path:     path,
		Found:    true,
		Status:   status,
		LOC:      meta.LOC,
		Exports:  meta.Exports,
		Deps:     meta.Deps,
		Summary:  meta.Summary,
		Patterns: meta.Patterns,
		Logic:    meta.Logic,
	}

	switch status {
	case engine.CodeFresh:
		out.Recommendation = "Summary matches the file on disk. Skip reading the file."
	case engine.CodeStale:
		out.Recommendation = "The file changed since this summary was written. Re-read it, then call code_save."
	default:
		out.Recommendation = "The file no longer exists at this path. Treat the summary as historical."
	}
	return nil, out, nil
}

// CodeSaveInput is a structured summary of one source file.
type CodeSaveInput struct {
	Path     string   `json:"path" jsonschema:"path to the source file, relative to the project root"`
	Summary  string   `json:"summary" jsonschema:"one line describing what the file does and why it exists"`
	Exports  []string `json:"exports,omitempty" jsonschema:"exported functions, types, or classes"`
	Deps     []string `json:"deps,omitempty" jsonschema:"project files this one depends on"`
	Patterns string   `json:"patterns,omitempty" jsonschema:"conventions callers must follow when using this file"`
	Logic    string   `json:"logic,omitempty" jsonschema:"step-by-step flow, for files whose control flow is not obvious"`
}

// CodeSaveOutput confirms what was stored.
type CodeSaveOutput struct {
	ID   string `json:"id" jsonschema:"content-addressed id of the stored summary"`
	Path string `json:"path" jsonschema:"normalized project-relative path"`
	LOC  int    `json:"loc" jsonschema:"line count measured at save time"`
	Hash string `json:"hash" jsonschema:"file hash used to detect staleness later"`
}

func (s *Server) handleCodeSave(_ context.Context, _ *sdk.CallToolRequest, in CodeSaveInput) (*sdk.CallToolResult, CodeSaveOutput, error) {
	paths, root, cfg, err := s.session()
	if err != nil {
		return nil, CodeSaveOutput{}, err
	}

	path := relPath(root, in.Path)
	if path == "" {
		return nil, CodeSaveOutput{}, fmt.Errorf("path is required")
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return nil, CodeSaveOutput{}, fmt.Errorf("summary is required")
	}

	// A missing file is not fatal: agents legitimately summarize planned or
	// generated files. The placeholder hash makes the entry read back as stale.
	hash, loc, err := engine.FileHashAndLOC(filepath.Join(root, path))
	if err != nil {
		hash, loc = "000000", 0
	}

	entry := memory.Entry{
		Timestamp: time.Now().Unix(),
		Type:      memory.TypeCode,
		ID:        memory.ContentID(path),
		Content:   path,
		Meta: &memory.CodeMeta{
			FilePath: path,
			LOC:      loc,
			Hash:     hash,
			Exports:  in.Exports,
			Deps:     in.Deps,
			Summary:  summary,
			Patterns: strings.TrimSpace(in.Patterns),
			Logic:    strings.TrimSpace(in.Logic),
		},
	}
	if parts := strings.Split(path, "/"); len(parts) > 1 {
		entry.Tags = normalizeTags([]string{parts[len(parts)-2]})
	}

	if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
		return nil, CodeSaveOutput{}, fmt.Errorf("write code summary: %w", err)
	}
	compact(paths, cfg)

	return nil, CodeSaveOutput{ID: entry.ID, Path: path, LOC: loc, Hash: hash}, nil
}

// StatsOutput summarizes the current memory footprint.
type StatsOutput struct {
	Entries       int            `json:"entries" jsonschema:"entries in the active snapshot"`
	Pending       int            `json:"pending" jsonschema:"entries written but not yet compacted"`
	TokenBudget   int            `json:"tokenBudget" jsonschema:"configured token budget for retrieved context"`
	KnowledgeDocs int            `json:"knowledgeDocs" jsonschema:"indexed instruction and skill documents"`
	ByType        map[string]int `json:"byType,omitempty" jsonschema:"snapshot entry counts keyed by memory type"`
}

func (s *Server) handleStats(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, StatsOutput, error) {
	paths, _, cfg, err := s.session()
	if err != nil {
		return nil, StatsOutput{}, err
	}

	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return nil, StatsOutput{}, fmt.Errorf("read snapshot: %w", err)
	}
	pending, err := store.WALEntryCount(paths.MemoryWAL)
	if err != nil {
		return nil, StatsOutput{}, fmt.Errorf("read WAL: %w", err)
	}

	byType := make(map[string]int, len(snap.Entries))
	for _, e := range snap.Entries {
		byType[e.Type.FullName()]++
	}

	out := StatsOutput{
		Entries:     len(snap.Entries),
		Pending:     pending,
		TokenBudget: cfg.Memory.TokenBudget,
		ByType:      byType,
	}
	if kb, err := engine.LoadKnowledgeDB(paths.Knowledge); err == nil {
		out.KnowledgeDocs = len(kb.Docs)
	}
	return nil, out, nil
}

// compact runs threshold-based compaction. Failures are reported on stderr and
// never fail the tool call, because the entry is already durable in the WAL.
func compact(paths store.Paths, cfg config.Config) {
	if _, _, _, err := engine.AutoCompact(paths, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "memor: auto-compact failed: %v\n", err)
	}
}
