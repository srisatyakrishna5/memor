package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect wires a real client to the memor server over in-memory transports so
// tests exercise schema inference and argument validation, not just handlers.
func connect(t *testing.T, projectDir string) (*sdk.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	if _, err := NewServer(projectDir, "test").sdkServer().Connect(ctx, serverTransport, nil); err != nil {
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

// newProject creates an initialized project and returns a connected session.
func newProject(t *testing.T) (*sdk.ClientSession, context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	paths := store.ResolvePaths(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	session, ctx := connect(t, dir)
	return session, ctx, dir
}

func call(t *testing.T, s *sdk.ClientSession, ctx context.Context, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	return res
}

func mustCall(t *testing.T, s *sdk.ClientSession, ctx context.Context, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res := call(t, s, ctx, name, args)
	if res.IsError {
		t.Fatalf("%s returned tool error: %s", name, resultText(res))
	}
	return res
}

func resultText(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func decode[T any](t *testing.T, res *sdk.CallToolResult) T {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	return out
}

func TestToolsAreRegistered(t *testing.T) {
	session, ctx, _ := newProject(t)

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	got := make(map[string]*sdk.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}

	want := []string{"memory_context", "memory_add", "memory_search", "code_get", "code_save", "memory_stats"}
	for _, name := range want {
		tool, ok := got[name]
		if !ok {
			t.Errorf("tool %q not registered", name)
			continue
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("expected %d tools, got %d", len(want), len(got))
	}
}

func TestAddThenSearchRoundTrip(t *testing.T) {
	session, ctx, _ := newProject(t)

	const content = "Auth uses OAuth2 with PKCE via Auth0 because SAML was rejected for mobile"
	added := decode[AddOutput](t, mustCall(t, session, ctx, "memory_add", map[string]any{
		"content": content,
		"type":    "semantic",
		"tags":    []string{"Auth", "#api", " auth "},
	}))

	if want := memory.ContentID(content); added.ID != want {
		t.Errorf("id = %q, want content-addressed %q", added.ID, want)
	}
	if added.Type != "semantic" {
		t.Errorf("type = %q, want semantic", added.Type)
	}
	if len(added.Tags) != 2 || added.Tags[0] != "auth" || added.Tags[1] != "api" {
		t.Errorf("tags = %v, want normalized and deduplicated [auth api]", added.Tags)
	}

	found := decode[SearchOutput](t, mustCall(t, session, ctx, "memory_search", map[string]any{
		"query": "oauth auth0",
	}))
	if found.Count == 0 {
		t.Fatal("search returned no results for a memory that was just added")
	}
	if found.Results[0].ID != added.ID {
		t.Errorf("top result id = %q, want %q", found.Results[0].ID, added.ID)
	}
}

func TestAddRejectsInvalidInput(t *testing.T) {
	session, ctx, _ := newProject(t)

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty content", map[string]any{"content": "   "}, "content is required"},
		{"bad type", map[string]any{"content": "x", "type": "nonsense"}, "invalid type"},
		{"bad expiry", map[string]any{"content": "x", "expires": "next tuesday"}, "invalid expires"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, session, ctx, "memory_add", tc.args)
			if !res.IsError {
				t.Fatalf("expected a tool error, got success")
			}
			if !strings.Contains(resultText(res), tc.want) {
				t.Errorf("error = %q, want it to mention %q", resultText(res), tc.want)
			}
		})
	}
}

func TestContextIncludesStoredMemory(t *testing.T) {
	session, ctx, _ := newProject(t)

	mustCall(t, session, ctx, "memory_add", map[string]any{
		"content": "Deploys run pnpm turbo deploy from the release branch",
		"type":    "procedural",
		"tags":    []string{"deploy"},
	})

	got := resultText(mustCall(t, session, ctx, "memory_context", map[string]any{
		"query": "how do we deploy",
	}))
	if !strings.Contains(got, "pnpm turbo deploy") {
		t.Errorf("context did not include the stored memory:\n%s", got)
	}
}

func TestContextOnEmptyProject(t *testing.T) {
	session, ctx, _ := newProject(t)

	// Both paths matter: the engine emits a different block when a query is
	// supplied, and neither should reach the agent as a bare header.
	for _, args := range []map[string]any{nil, {"query": "how do we deploy"}} {
		got := resultText(mustCall(t, session, ctx, "memory_context", args))
		if !strings.Contains(got, "No project memory recorded yet") {
			t.Errorf("args=%v: expected an actionable empty-memory message, got:\n%s", args, got)
		}
	}
}

func TestCodeSummaryFreshnessLifecycle(t *testing.T) {
	session, ctx, dir := newProject(t)

	srcDir := filepath.Join(dir, "src", "lib")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "auth.ts")
	if err := os.WriteFile(src, []byte("export function login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	saved := decode[CodeSaveOutput](t, mustCall(t, session, ctx, "code_save", map[string]any{
		"path":    "src/lib/auth.ts",
		"summary": "Session login helpers",
		"exports": []string{"login()"},
	}))
	if saved.LOC != 1 {
		t.Errorf("loc = %d, want 1", saved.LOC)
	}
	if saved.Hash == "" || saved.Hash == "000000" {
		t.Errorf("hash = %q, want a real file hash", saved.Hash)
	}

	fresh := decode[CodeGetOutput](t, mustCall(t, session, ctx, "code_get", map[string]any{
		"path": "src/lib/auth.ts",
	}))
	if !fresh.Found || fresh.Status != "fresh" {
		t.Fatalf("status = %q (found=%v), want fresh", fresh.Status, fresh.Found)
	}
	if fresh.Summary != "Session login helpers" {
		t.Errorf("summary = %q", fresh.Summary)
	}
	if !strings.Contains(fresh.Recommendation, "Skip") {
		t.Errorf("recommendation = %q, want it to advise skipping the read", fresh.Recommendation)
	}

	// Editing the file must invalidate the cached summary.
	if err := os.WriteFile(src, []byte("export function login() {}\nexport function logout() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stale := decode[CodeGetOutput](t, mustCall(t, session, ctx, "code_get", map[string]any{
		"path": "src/lib/auth.ts",
	}))
	if stale.Status != "stale" {
		t.Fatalf("status = %q, want stale after the file changed", stale.Status)
	}
	if !strings.Contains(stale.Recommendation, "Re-read") {
		t.Errorf("recommendation = %q, want it to advise re-reading", stale.Recommendation)
	}
}

func TestCodeGetAcceptsAbsoluteAndNativePaths(t *testing.T) {
	session, ctx, dir := newProject(t)

	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "run.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mustCall(t, session, ctx, "code_save", map[string]any{
		"path":    filepath.Join(dir, "pkg", "run.go"),
		"summary": "Entry point",
	})

	got := decode[CodeGetOutput](t, mustCall(t, session, ctx, "code_get", map[string]any{
		"path": filepath.Join("pkg", "run.go"),
	}))
	if got.Path != "pkg/run.go" {
		t.Errorf("path = %q, want normalized pkg/run.go", got.Path)
	}
	if got.Status != "fresh" {
		t.Errorf("status = %q, want fresh", got.Status)
	}
}

func TestCodeGetUnrecordedFile(t *testing.T) {
	session, ctx, _ := newProject(t)

	got := decode[CodeGetOutput](t, mustCall(t, session, ctx, "code_get", map[string]any{
		"path": "src/never/seen.ts",
	}))
	if got.Found {
		t.Error("found = true for a file that was never summarized")
	}
	if got.Status != statusUnrecorded {
		t.Errorf("status = %q, want %q", got.Status, statusUnrecorded)
	}
	if !strings.Contains(got.Recommendation, "Read the file") {
		t.Errorf("recommendation = %q, want it to advise reading the file", got.Recommendation)
	}
}

func TestStatsReflectsStoredMemories(t *testing.T) {
	session, ctx, _ := newProject(t)

	mustCall(t, session, ctx, "memory_add", map[string]any{"content": "Postgres 16 with Drizzle"})
	mustCall(t, session, ctx, "memory_add", map[string]any{"content": "Retries use exponential backoff", "type": "procedural"})

	got := decode[StatsOutput](t, mustCall(t, session, ctx, "memory_stats", map[string]any{}))
	if total := got.Entries + got.Pending; total != 2 {
		t.Errorf("entries+pending = %d, want 2", total)
	}
	if got.TokenBudget <= 0 {
		t.Errorf("tokenBudget = %d, want the configured budget", got.TokenBudget)
	}
}

func TestUninitializedProjectGivesActionableError(t *testing.T) {
	// t.TempDir has no .memor/ and, being outside the repo, no initialized ancestor.
	session, ctx := connect(t, t.TempDir())

	for _, name := range []string{"memory_context", "memory_search", "memory_stats"} {
		args := map[string]any{}
		if name == "memory_search" {
			args["query"] = "anything"
		}
		res := call(t, session, ctx, name, args)
		if !res.IsError {
			t.Errorf("%s succeeded on an uninitialized project", name)
			continue
		}
		if !strings.Contains(resultText(res), "memor init") {
			t.Errorf("%s error = %q, want it to point at 'memor init'", name, resultText(res))
		}
	}
}

func TestServerFindsProjectRootFromSubdirectory(t *testing.T) {
	dir := t.TempDir()
	paths := store.ResolvePaths(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	session, ctx := connect(t, nested)
	mustCall(t, session, ctx, "memory_add", map[string]any{"content": "Discovered from a nested directory"})

	// The write must land in the root .memor/, not a new one under the subdirectory.
	if _, err := os.Stat(filepath.Join(nested, ".memor")); !os.IsNotExist(err) {
		t.Error("server created .memor/ in the subdirectory instead of using the project root")
	}
	got := decode[SearchOutput](t, mustCall(t, session, ctx, "memory_search", map[string]any{"query": "nested directory"}))
	if got.Count == 0 {
		t.Error("memory written from a subdirectory was not readable from the project root")
	}
}
