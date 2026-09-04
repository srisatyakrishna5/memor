package tests

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/graph/extract"
	"github.com/memor-dev/memor/internal/session"
	"github.com/memor-dev/memor/internal/token"
)

// newSession initializes memor on a sample repository and indexes it.
func newSession(t *testing.T) *session.Session {
	t.Helper()
	root := sampleRepo(t)

	sess, err := session.Create(root)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess.Cfg.Knowledge.Enabled = false
	if _, err := sess.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return sess
}

func TestPurposeIsExtractedFromLeadingComments(t *testing.T) {
	cases := []struct {
		name string
		lang string
		src  string
		want string
	}{
		{
			name: "go package comment drops the package name",
			lang: "go",
			src:  "// Package store persists records to disk. It is append-only.\npackage store\n",
			want: "persists records to disk",
		},
		{
			name: "build directives are not descriptions",
			lang: "go",
			src:  "//go:build linux\n\n// Locks a file using flock.\npackage store\n",
			want: "Locks a file using flock",
		},
		{
			name: "typescript block comment",
			lang: "ts",
			src:  "/**\n * Renders the invoice table.\n */\nexport function Table() {}\n",
			want: "Renders the invoice table",
		},
		{
			name: "python docstring",
			lang: "python",
			src:  "\"\"\"Parses configuration files.\"\"\"\n\nimport os\n",
			want: "Parses configuration files",
		},
		{
			name: "a licence header says nothing about the file",
			lang: "go",
			src:  "// Copyright 2026 The Memor Authors.\npackage store\n",
			want: "",
		},
		{
			name: "code with no comment yields nothing",
			lang: "go",
			src:  "package store\n\nfunc Load() {}\n",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extract.Purpose(tc.lang, []byte(tc.src)); got != tc.want {
				t.Errorf("Purpose() = %q, want %q", got, tc.want)
			}
		})
	}
}

// An agent's own summary is intent, which extraction cannot recover, so it has
// to survive a rebuild.
func TestAgentSummaryOutranksExtractedPurpose(t *testing.T) {
	sess := newSession(t)

	if _, err := sess.Describe("main.go", "Wires the CLI and exits", "", "", nil); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if _, err := sess.Build(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	g, _, err := sess.Graph()
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	node, ok := g.FindFile("main.go")
	if !ok {
		t.Fatal("expected main.go")
	}
	if node.Text != "Wires the CLI and exits" {
		t.Errorf("a rebuild overwrote the agent summary: %q", node.Text)
	}
}

// The manifest is the cheapest full view of a repository, so it must stay far
// below the cost of the detailed projection it replaces.
func TestManifestIsCheaperThanTheFullProjection(t *testing.T) {
	sess := newSession(t)
	g, _, err := sess.Graph()
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}

	manifest := graph.RenderManifest(g, 8)
	if !strings.Contains(manifest, "main.go") {
		t.Errorf("expected every file to be listed:\n%s", manifest)
	}
	// Signatures belong in repo_map, not in the manifest.
	if strings.Contains(manifest, "func Load() string") {
		t.Errorf("manifest carried a full signature:\n%s", manifest)
	}
	if token.Count(manifest) >= token.Count(graph.Render(g, sess.Cfg)) {
		t.Error("expected the manifest to cost less than the full projection")
	}
}

func TestBuildRecordsIndexedState(t *testing.T) {
	sess := newSession(t)

	report, err := sess.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.IndexedAt == 0 {
		t.Error("expected the build to record when it ran")
	}
}

// A returning agent must be told what moved, and that answer must stay cheap.
func TestBriefReportsChangesSinceTheLastVisit(t *testing.T) {
	sess := newSession(t)
	gitRepo(t, sess.Root)
	if _, err := sess.Build(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	first, err := sess.Brief(session.BriefOptions{Agent: "copilot", Advance: true})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if !first.FirstVisit {
		t.Error("expected the first call to report a first visit")
	}
	if len(first.Changes.Changes) != 0 {
		t.Errorf("expected a clean tree to report no changes, got %v", first.Changes.Changes)
	}

	writeFile(t, sess.Root, "internal/store/store.go", "package store\n\nfunc Load() string { return \"x\" }\n")

	second, err := sess.Brief(session.BriefOptions{Agent: "copilot", Advance: true})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if second.FirstVisit {
		t.Error("expected the watermark to persist")
	}
	found := false
	for _, c := range second.Changes.Changes {
		if c.Path == "internal/store/store.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the edited file in the brief, got %v", second.Changes.Changes)
	}

	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if cost := token.Count(string(encoded)); cost > constants.DefaultBriefBudget {
		t.Errorf("brief cost %d tokens, over the %d budget", cost, constants.DefaultBriefBudget)
	}
}

// Journal entries are what let an agent resume work rather than rediscover it.
func TestJournalSurfacesUnfinishedWork(t *testing.T) {
	sess := newSession(t)

	if _, err := sess.Remember(session.RememberInput{
		Content: "Half-migrated the store to the new lock API; lock_windows.go is still on the old one",
		Type:    "episodic",
		Task:    "lock-api-migration",
		Status:  "open",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := sess.Remember(session.RememberInput{
		Content: "Renamed graph.idx out of existence",
		Type:    "episodic",
		Task:    "drop-pagerank",
		Status:  "done",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	brief, err := sess.Brief(session.BriefOptions{})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if len(brief.Journal) != 1 {
		t.Fatalf("expected only unfinished work, got %d entries", len(brief.Journal))
	}
	if brief.Journal[0].Task != "lock-api-migration" {
		t.Errorf("unexpected entry %+v", brief.Journal[0])
	}
}

func TestRememberRejectsAnUnknownStatus(t *testing.T) {
	sess := newSession(t)

	if _, err := sess.Remember(session.RememberInput{Content: "x", Status: "nearly"}); err == nil {
		t.Error("expected an unknown status to be rejected")
	}
}

// Uncommitted work is the normal state of a working tree, so the same dirty
// files appear in the change list forever. The watermark has to distinguish an
// edit the agent has already been told about from one it has not.
func TestWatermarkSeparatesNewChangesFromSeenOnes(t *testing.T) {
	sess := newSession(t)
	gitRepo(t, sess.Root)
	if _, err := sess.Build(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	writeFile(t, sess.Root, "main.go", "package main\n\nfunc main() { println(1) }\n")
	// Re-index so the advice under test is about the watermark rather than
	// index drift, which legitimately outranks it.
	if _, err := sess.Build(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	first, err := sess.Brief(session.BriefOptions{Agent: "copilot", Advance: true})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if first.Changes.Unseen != 1 {
		t.Fatalf("expected the edit to be new, got %d unseen in %+v", first.Changes.Unseen, first.Changes.Changes)
	}

	// Same tree, second visit: the file is still dirty but no longer news.
	second, err := sess.Brief(session.BriefOptions{Agent: "copilot", Advance: true})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if second.Changes.Unseen != 0 {
		t.Errorf("expected nothing new on an unchanged tree, got %d", second.Changes.Unseen)
	}
	if len(second.Changes.Changes) != 1 || !second.Changes.Changes[0].Seen {
		t.Errorf("expected the dirty file to still be listed but marked seen, got %+v", second.Changes.Changes)
	}
	if second.Changes.Changes[0].Purpose != "" {
		t.Error("a seen change should cost its path and nothing more")
	}
	if !strings.Contains(second.Advice, "Nothing has changed") {
		t.Errorf("expected advice to say the agent can reuse what it knows, got %q", second.Advice)
	}

	// Editing it again makes it news once more.
	writeFile(t, sess.Root, "main.go", "package main\n\nfunc main() { println(2) }\n")
	third, err := sess.Brief(session.BriefOptions{Agent: "copilot", Advance: true})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if third.Changes.Unseen != 1 {
		t.Errorf("expected the re-edit to be new again, got %d", third.Changes.Unseen)
	}
}

// Watermarks are per agent, so one client reading the brief must not silence
// the changes for another.
func TestWatermarksAreIndependentPerAgent(t *testing.T) {
	sess := newSession(t)
	gitRepo(t, sess.Root)
	if _, err := sess.Build(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	writeFile(t, sess.Root, "main.go", "package main\n\nfunc main() { println(1) }\n")

	if _, err := sess.Brief(session.BriefOptions{Agent: "one", Advance: true}); err != nil {
		t.Fatalf("Brief: %v", err)
	}
	other, err := sess.Brief(session.BriefOptions{Agent: "two", Advance: true})
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if other.Changes.Unseen != 1 {
		t.Errorf("expected the second agent to see the change, got %d unseen", other.Changes.Unseen)
	}
}

// --keep-mark exists so a caller can look without consuming the delta.
func TestBriefCanReadWithoutAdvancing(t *testing.T) {
	sess := newSession(t)
	gitRepo(t, sess.Root)
	if _, err := sess.Build(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	writeFile(t, sess.Root, "main.go", "package main\n\nfunc main() { println(1) }\n")

	for i := 0; i < 2; i++ {
		b, err := sess.Brief(session.BriefOptions{Agent: "copilot"})
		if err != nil {
			t.Fatalf("Brief: %v", err)
		}
		if b.Changes.Unseen != 1 {
			t.Fatalf("call %d: expected the change to stay unseen, got %d", i, b.Changes.Unseen)
		}
	}
}

// Without git, change detection has to fall back to comparing stored hashes
// rather than reporting nothing.
func TestChangesFallBackToHashesWithoutGit(t *testing.T) {
	sess := newSession(t)

	writeFile(t, sess.Root, "main.go", "package main\n\nfunc main() { println(1) }\n")

	set, err := sess.Changes("", "", 50)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if set.Source != "hash" {
		t.Skip("temp dir is inside a git work tree")
	}
	found := false
	for _, c := range set.Changes {
		if c.Path == "main.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the drifted file to be reported, got %v", set.Changes)
	}
}
