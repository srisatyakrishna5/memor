package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/graph/extract"
	"github.com/memor-dev/memor/internal/mcp"
	"github.com/memor-dev/memor/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectMCP wires a real client to the memor server over in-memory transports
// so tests exercise schema inference and argument validation, not just handlers.
func connectMCP(t *testing.T, projectDir string) (*sdk.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	if _, err := mcp.NewServer(projectDir, "test").Connect(ctx, serverTransport); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "memor-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session, ctx
}

// newMCPProject creates an initialized project with a small indexed repository.
func newMCPProject(t *testing.T) (*sdk.ClientSession, context.Context, string) {
	t.Helper()
	dir := t.TempDir()

	paths := store.ResolvePaths(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	writeFile(t, dir, "go.mod", "module example.com/demo\n\ngo 1.25\n")
	writeFile(t, dir, "internal/auth/session.go", `package auth

// ValidateToken checks a session token against the rotating key set.
func ValidateToken(token string) error {
	return verify(token)
}

func verify(token string) error {
	return nil
}
`)

	cfg := config.Default()
	cfg.Knowledge.Enabled = false
	result, err := extract.Repo(dir, cfg)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := graph.Rebuild(paths, cfg, result.Nodes, result.Edges); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	session, ctx := connectMCP(t, dir)
	return session, ctx, dir
}

func callTool(t *testing.T, s *sdk.ClientSession, ctx context.Context, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, textOf(res))
	}
	return res
}

func textOf(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String()
}

func structuredOf[T any](t *testing.T, res *sdk.CallToolResult) T {
	t.Helper()
	var out T
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	return out
}

// Bloated tool sets are a leading agent failure mode, so the count is asserted
// rather than left to drift.
func TestExactlyFiveToolsAreRegistered(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := map[string]bool{
		"repo_map": true, "symbol_find": true, "symbol_read": true,
		"remember": true, "graph_status": true,
	}
	if len(list.Tools) != len(want) {
		t.Errorf("expected %d tools, got %d", len(want), len(list.Tools))
	}
	for _, tool := range list.Tools {
		if !want[tool.Name] {
			t.Errorf("unexpected tool %q", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s: a tool description is what changes agent behaviour; it cannot be empty", tool.Name)
		}
	}
}

func TestRepoMapReturnsRankedStructure(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res := callTool(t, session, ctx, "repo_map", map[string]any{
		"query": "how are session tokens validated",
	})
	text := textOf(res)
	if !strings.Contains(text, "internal/auth/session.go") {
		t.Errorf("expected the auth file in the map:\n%s", text)
	}
	if !strings.Contains(text, "ValidateToken") {
		t.Errorf("expected the symbol signature in the map:\n%s", text)
	}
}

func TestSymbolFindReturnsSpanAndCallees(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res := callTool(t, session, ctx, "symbol_find", map[string]any{"name": "ValidateToken"})
	out := structuredOf[mcp.SymbolFindOutput](t, res)

	if out.Count == 0 {
		t.Fatal("expected a match")
	}
	found := out.Results[0]
	if found.Path != "internal/auth/session.go" {
		t.Errorf("unexpected path %q", found.Path)
	}
	if found.StartLine == 0 || found.EndLine < found.StartLine {
		t.Errorf("expected a usable line range, got %d-%d", found.StartLine, found.EndLine)
	}
	if found.Status != graph.StatusFresh {
		t.Errorf("expected a freshly built symbol to be fresh, got %q", found.Status)
	}
	if len(found.Callees) == 0 {
		t.Error("expected verify to be recorded as a callee")
	}
}

// symbol_read exists so an agent fetches the lines a symbol occupies rather
// than the whole file around them.
func TestSymbolReadReturnsOnlyTheSymbolBody(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res := callTool(t, session, ctx, "symbol_read", map[string]any{"name": "ValidateToken"})
	out := structuredOf[mcp.SymbolReadOutput](t, res)

	if !strings.HasPrefix(out.Source, "func ValidateToken") {
		t.Errorf("expected the body to start at the declaration:\n%s", out.Source)
	}
	if strings.Contains(out.Source, "func verify") {
		t.Errorf("expected only the requested symbol, got the rest of the file:\n%s", out.Source)
	}
}

func TestSymbolReadRejectsUnknownSymbol(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      "symbol_read",
		Arguments: map[string]any{"name": "NoSuchSymbol"},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError {
		t.Error("expected an actionable error rather than a silent empty result")
	}
}

func TestRememberBindsAFactToAFile(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res := callTool(t, session, ctx, "remember", map[string]any{
		"content": "Session tokens are validated against a rotating key set, not a static secret",
		"type":    "semantic",
		"tags":    []string{"auth"},
		"files":   []string{"internal/auth/session.go"},
	})
	out := structuredOf[mcp.RememberOutput](t, res)
	if out.ID == "" {
		t.Fatal("expected a content-addressed id")
	}
	if out.Type != "semantic" {
		t.Errorf("unexpected type %q", out.Type)
	}

	// The point of an explains edge is that the fact resurfaces on the file it
	// explains, without anyone searching for it.
	mapRes := callTool(t, session, ctx, "repo_map", map[string]any{"query": "session token validation"})
	if !strings.Contains(textOf(mapRes), "rotating key set") {
		t.Errorf("expected the memory to render inline on its file:\n%s", textOf(mapRes))
	}
}

// Re-recording the same fact must collapse onto the same node.
func TestRememberIsIdempotent(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	args := map[string]any{"content": "Compaction archives before it truncates"}
	first := structuredOf[mcp.RememberOutput](t, callTool(t, session, ctx, "remember", args))
	second := structuredOf[mcp.RememberOutput](t, callTool(t, session, ctx, "remember", args))

	if first.ID != second.ID {
		t.Errorf("expected the same id, got %s and %s", first.ID, second.ID)
	}
}

func TestRememberDescribesAFile(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res := callTool(t, session, ctx, "remember", map[string]any{
		"summary": "Session issuance and validation against the rotating key set",
		"files":   []string{"internal/auth/session.go"},
	})
	out := structuredOf[mcp.RememberOutput](t, res)
	if out.Kind != "file" {
		t.Errorf("expected a file description, got %q", out.Kind)
	}
}

func TestRememberRejectsSummaryWithoutExactlyOneFile(t *testing.T) {
	session, ctx, _ := newMCPProject(t)

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{"summary": "ambiguous", "files": []string{"a.go", "b.go"}},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError {
		t.Error("expected a summary spanning two files to be rejected")
	}
}

func TestGraphStatusReportsFreshness(t *testing.T) {
	session, ctx, dir := newMCPProject(t)

	out := structuredOf[mcp.StatusOutput](t, callTool(t, session, ctx, "graph_status", map[string]any{}))
	if out.Nodes == 0 {
		t.Fatal("expected an indexed graph")
	}
	if out.Stale != 0 {
		t.Errorf("expected nothing stale immediately after a build, got %d", out.Stale)
	}
	if !strings.Contains(out.Advice, "repo_map") {
		t.Errorf("expected advice pointing at the next action, got %q", out.Advice)
	}

	// A stale graph is worse than no graph, so drift must be reported.
	writeFile(t, dir, "internal/auth/session.go", "package auth\n\nfunc ValidateToken(token string) error { return nil }\n")
	drifted := structuredOf[mcp.StatusOutput](t, callTool(t, session, ctx, "graph_status", map[string]any{}))
	if drifted.Stale == 0 {
		t.Error("expected the edited file to be reported as stale")
	}
	if !strings.Contains(drifted.Advice, "memor build") {
		t.Errorf("expected advice to recommend a rebuild, got %q", drifted.Advice)
	}
}

func TestUninitializedProjectReportsAnActionableError(t *testing.T) {
	session, ctx := connectMCP(t, t.TempDir())

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      "graph_status",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error for an uninitialized project")
	}
	if !strings.Contains(textOf(res), "memor init") {
		t.Errorf("expected the error to name the fix, got %q", textOf(res))
	}
}
