package tests

import (
	"strings"
	"testing"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/graph/extract"
)

// symbolsOf indexes an extraction result by symbol name.
func symbolsOf(t *testing.T, lang, path, src string) map[string]*graph.Node {
	t.Helper()
	syms, err := extract.Symbols(lang, path, "aaaaaa", []byte(src))
	if err != nil {
		t.Fatalf("Symbols(%s): %v", lang, err)
	}
	out := make(map[string]*graph.Node, len(syms))
	for _, s := range syms {
		out[s.Node.Name] = s.Node
	}
	return out
}

// A span must land on the declaration it names. A wrong span is worse than a
// missing one, because the agent reads the returned lines as authoritative.
func assertSpan(t *testing.T, src string, n *graph.Node, wantPrefix string) {
	t.Helper()
	if n == nil {
		t.Fatal("symbol not extracted")
	}
	if n.Span == nil || n.Span.Start >= n.Span.End || n.Span.End > len(src) {
		t.Fatalf("%s: invalid span %+v", n.Name, n.Span)
	}
	body := src[n.Span.Start:n.Span.End]
	if !strings.HasPrefix(strings.TrimSpace(body), wantPrefix) {
		t.Errorf("%s: span starts at %q, want prefix %q", n.Name, firstLine(body), wantPrefix)
	}
	lines := strings.Count(src[:n.Span.Start], "\n") + 1
	if n.Span.L0 != lines {
		t.Errorf("%s: L0 is %d, byte offset says line %d", n.Name, n.Span.L0, lines)
	}
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func TestTypeScriptSymbols(t *testing.T) {
	src := `import { thing } from './thing';

export interface Session {
  id: string;
}

export type Token = string;

export async function validateToken(token: string): Promise<boolean> {
  if (!token) {
    return false;
  }
  return check(token);
}

export const MAX_RETRIES = 3;

export const useSession = (id: string) => {
  return { id };
};

export class SessionStore {
  private items: Session[] = [];

  add(s: Session) {
    this.items.push(s);
  }
}
`
	got := symbolsOf(t, "ts", "src/session.ts", src)

	for _, name := range []string{"Session", "Token", "validateToken", "MAX_RETRIES", "useSession", "SessionStore"} {
		if got[name] == nil {
			t.Errorf("expected %q to be extracted, got %v", name, names(got))
		}
	}

	assertSpan(t, src, got["validateToken"], "export async function validateToken")
	assertSpan(t, src, got["SessionStore"], "export class SessionStore")

	// The class body must be enclosed, not cut at its first nested brace.
	if store := got["SessionStore"]; store != nil && !strings.Contains(src[store.Span.Start:store.Span.End], "this.items.push") {
		t.Error("class span stopped before the end of its body")
	}
	if kind := got["validateToken"].MetaValue(graph.MetaSymKind); kind != "func" {
		t.Errorf("expected func, got %q", kind)
	}
	if sig := got["validateToken"].Text; strings.HasSuffix(sig, "{") {
		t.Errorf("signature kept its opening brace: %q", sig)
	}
}

// Destructuring is not a declaration anyone can reference by name.
func TestJavaScriptSkipsDestructuring(t *testing.T) {
	src := "const { a, b } = require('mod');\nconst ready = true;\n"
	got := symbolsOf(t, "js", "index.js", src)

	if got["ready"] == nil {
		t.Error("expected a named const to be extracted")
	}
	if len(got) != 1 {
		t.Errorf("expected only the named const, got %v", names(got))
	}
}

func TestPythonSymbols(t *testing.T) {
	src := `"""Session handling."""

import os


def validate_token(token):
    if not token:
        return False
    return True


class SessionStore:
    def __init__(self):
        self.items = []

    def add(self, item):
        def inner():
            pass
        self.items.append(item)


CONSTANT = 3
`
	got := symbolsOf(t, "python", "session.py", src)

	for _, name := range []string{"validate_token", "SessionStore", "__init__", "add"} {
		if got[name] == nil {
			t.Errorf("expected %q to be extracted, got %v", name, names(got))
		}
	}
	// A closure cannot be referenced from outside the function that defines it.
	if got["inner"] != nil {
		t.Error("expected a nested closure to be skipped")
	}

	assertSpan(t, src, got["validate_token"], "def validate_token")
	assertSpan(t, src, got["SessionStore"], "class SessionStore")

	// The class must end before the module-level constant that follows it.
	if store := got["SessionStore"]; store != nil {
		body := src[store.Span.Start:store.Span.End]
		if strings.Contains(body, "CONSTANT") {
			t.Error("class span ran past its dedent")
		}
		if !strings.Contains(body, "self.items.append") {
			t.Error("class span stopped before the end of its body")
		}
	}
}

func TestRustSymbols(t *testing.T) {
	src := `use std::fmt;

pub struct Session {
    id: String,
}

pub async fn validate_token(token: &str) -> bool {
    if token.is_empty() {
        return false;
    }
    true
}

pub trait Store {
    fn add(&self, s: Session);
}
`
	got := symbolsOf(t, "rust", "src/session.rs", src)

	for _, name := range []string{"Session", "validate_token", "Store"} {
		if got[name] == nil {
			t.Errorf("expected %q to be extracted, got %v", name, names(got))
		}
	}
	assertSpan(t, src, got["validate_token"], "pub async fn validate_token")
}

func TestJavaAndCSharpTypes(t *testing.T) {
	java := `package auth;

public final class SessionStore {
    private final List<Session> items = new ArrayList<>();

    public void add(Session s) {
        items.add(s);
    }
}
`
	got := symbolsOf(t, "java", "SessionStore.java", java)
	if got["SessionStore"] == nil {
		t.Fatalf("expected the class, got %v", names(got))
	}
	assertSpan(t, java, got["SessionStore"], "public final class SessionStore")

	cs := "namespace Auth\n{\n    public interface ISessionStore\n    {\n        void Add(Session s);\n    }\n}\n"
	csGot := symbolsOf(t, "csharp", "ISessionStore.cs", cs)
	if csGot["ISessionStore"] == nil {
		t.Errorf("expected the interface, got %v", names(csGot))
	}
}

// A brace inside a comment must not close a declaration early.
func TestCommentBracesDoNotCloseABody(t *testing.T) {
	src := "function run() {\n  // closes with } here\n  return 1;\n}\nfunction after() {\n  return 2;\n}\n"
	got := symbolsOf(t, "js", "a.js", src)

	run := got["run"]
	if run == nil {
		t.Fatal("expected run to be extracted")
	}
	if body := src[run.Span.Start:run.Span.End]; !strings.Contains(body, "return 1") {
		t.Errorf("span closed inside the comment: %q", body)
	}
	if got["after"] == nil {
		t.Error("expected the following declaration to still be found")
	}
}

// A language with no rules must return nothing rather than guessing.
func TestUnsupportedLanguageYieldsNoSymbols(t *testing.T) {
	if extract.SupportsSymbols("c") {
		t.Error("C is not supported and must not claim to be")
	}
	syms, err := extract.Symbols("c", "main.c", "aaaaaa", []byte("int main(void) { return 0; }\n"))
	if err != nil || len(syms) != 0 {
		t.Errorf("expected no symbols, got %d (%v)", len(syms), err)
	}
}

// Every span memor hands out must be readable back through the same guard
// symbol_read uses, or the extractor is producing coordinates nothing can serve.
func TestExtractedSpansAreReadable(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/poly\n")
	writeFile(t, root, "web/app.ts", "export function boot(): void {\n  start();\n}\n")
	writeFile(t, root, "svc/handler.py", "def handle(req):\n    return req\n")
	writeFile(t, root, "core/lib.rs", "pub fn parse(s: &str) -> bool {\n    true\n}\n")

	result, err := extract.Repo(root, extractConfig())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	g := buildGraph(t, result)

	for _, name := range []string{"boot", "handle", "parse"} {
		matches := g.FindSymbols(name)
		if len(matches) == 0 {
			t.Errorf("expected %q to be indexed", name)
			continue
		}
		if _, err := graph.ReadSpan(root, matches[0].Span); err != nil {
			t.Errorf("%s: span is not readable: %v", name, err)
		}
	}
}

func names(m map[string]*graph.Node) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	return out
}
