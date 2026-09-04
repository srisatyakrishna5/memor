package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/retrieve"
)

// retrievalFixture builds two files in different packages, one importing the
// other, plus a memory bound to the first.
func retrievalFixture(t *testing.T) (*graph.Graph, *graph.Index) {
	t.Helper()
	g := graph.New()

	auth := graph.FileNode("internal/auth/session.go", "Session issuance and validation", 120, "aaaaaa", "go")
	billing := graph.FileNode("internal/billing/invoice.go", "Invoice generation", 200, "bbbbbb", "go")
	auth.SetMetaList(graph.MetaSymbols, []string{"ValidateToken"})
	auth.SetMetaList(graph.MetaDependents, []string{"internal/billing/invoice.go"})
	billing.SetMetaList(graph.MetaSymbols, []string{"Total"})
	billing.SetMetaList(graph.MetaImports, []string{"internal/auth"})
	g.AddNode(auth)
	g.AddNode(billing)

	validate := graph.SymNode("internal/auth/session.go", "ValidateToken", "func ValidateToken(t string) error", "func",
		&graph.Span{Path: "internal/auth/session.go", Start: 0, End: 40, L0: 10, L1: 24, Hash: "aaaaaa"})
	total := graph.SymNode("internal/billing/invoice.go", "Total", "func Total(items []Item) int", "func",
		&graph.Span{Path: "internal/billing/invoice.go", Start: 0, End: 30, L0: 8, L1: 15, Hash: "bbbbbb"})
	g.AddNode(validate)
	g.AddNode(total)

	memory := graph.MemNode("Sessions are validated against a rotating key set, not a static secret", graph.MemSemantic, time.Now().Unix())
	memory.SetMetaList(graph.MetaTags, []string{"auth"})
	memory.SetMetaList(graph.MetaExplains, []string{auth.ID})
	g.AddNode(memory)

	return g, graph.BuildIndex(g)
}

func TestRetrieveRanksTheRelevantFileFirst(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{Text: "session validation", Budget: 2000})
	if len(result.Nodes) == 0 {
		t.Fatal("expected results")
	}
	if result.Nodes[0].Node.Name != "internal/auth/session.go" {
		t.Errorf("expected the auth file first, got %q:\n%s", result.Nodes[0].Node.Name, result.Text)
	}

	// A file that only depends on the answer must never outrank it.
	authIndex, billingIndex := -1, -1
	for i, s := range result.Nodes {
		switch s.Node.Name {
		case "internal/auth/session.go":
			authIndex = i
		case "internal/billing/invoice.go":
			billingIndex = i
		}
	}
	if billingIndex >= 0 && billingIndex < authIndex {
		t.Errorf("the dependent outranked the file that answers the query:\n%s", result.Text)
	}
}

// The whole point of dropping graph expansion: a query must not drag in a
// file's neighbours just because they are adjacent to a match.
func TestNeighboursAreNotPulledIn(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Text:   "rotating key set",
		Budget: 4000,
	})
	for _, s := range result.Nodes {
		if s.Node.Name == "internal/billing/invoice.go" {
			t.Errorf("an unrelated neighbour was pulled into the result:\n%s", result.Text)
		}
	}
}

// A path filter must be able to confine a broad query to one area.
func TestPathFilterConfinesResults(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Text:   "session invoice",
		Paths:  []string{"internal/billing"},
		Budget: 4000,
	})
	for _, s := range result.Nodes {
		if !strings.HasPrefix(s.Node.Name, "internal/billing") &&
			!strings.HasPrefix(graph.SpanPath(s.Node), "internal/billing") {
			t.Errorf("result escaped the path filter: %q", s.Node.Name)
		}
	}
}

// This is the v1 defect the rewrite exists to eliminate: a memory rendered
// inline on the file it explains must never also be emitted on its own.
func TestMemoryIsNotEmittedTwice(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Text:   "session validation rotating key",
		Budget: 2000,
	})
	if count := strings.Count(result.Text, "rotating key set"); count != 1 {
		t.Errorf("expected the memory exactly once, got %d:\n%s", count, result.Text)
	}
}

// A symbol listed inside a selected file block must not also appear standalone.
func TestSymbolIsNotEmittedTwice(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Text:   "validate token session",
		Budget: 2000,
	})
	if count := strings.Count(result.Text, "func ValidateToken"); count > 1 {
		t.Errorf("expected ValidateToken at most once, got %d:\n%s", count, result.Text)
	}
}

// The precision floor must drop rather than fill. Returning a plausible but
// wrong node costs more accuracy than returning nothing.
func TestPrecisionFloorDropsIrrelevantNodes(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Text:   "kubernetes ingress controller",
		Budget: 8000,
	})
	if len(result.Nodes) != 0 {
		t.Errorf("expected nothing above the floor, got %d nodes:\n%s", len(result.Nodes), result.Text)
	}
}

func TestBudgetIsRespected(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{Text: "session", Budget: 60})
	if result.Tokens > 60 {
		t.Errorf("packed %d tokens into a 60-token budget", result.Tokens)
	}
}

// Accuracy is highest at both ends of a context window, so the strongest
// results must take the head and the tail rather than the first two slots.
func TestStrongestResultsTakeBothEnds(t *testing.T) {
	g := graph.New()
	// Term frequency decreases down the list, so the scores are distinct and
	// the resulting order is observable from outside the package.
	for i, repeat := range []int{5, 4, 3, 2, 1} {
		summary := strings.TrimSpace(strings.Repeat("session ", repeat))
		g.AddNode(graph.FileNode(fmt.Sprintf("internal/auth/file%d.go", i), summary, 10, "aaaaaa", "go"))
	}

	result := retrieve.Retrieve(g, graph.BuildIndex(g), config.Default(), retrieve.Query{
		Text:   "session",
		Budget: 4000,
	})
	if len(result.Nodes) < 5 {
		t.Fatalf("expected all 5 files above the floor, got %d", len(result.Nodes))
	}

	first := result.Nodes[0].Score
	last := result.Nodes[len(result.Nodes)-1].Score
	middle := result.Nodes[len(result.Nodes)/2].Score

	for i, s := range result.Nodes {
		if s.Score > first {
			t.Errorf("result %d outscores the head: %v > %v", i, s.Score, first)
		}
		if i != 0 && s.Score > last {
			t.Errorf("result %d outscores the tail: %v > %v", i, s.Score, last)
		}
	}
	if middle > first || middle > last {
		t.Errorf("expected the weakest survivor in the middle, got %v against ends %v and %v",
			middle, first, last)
	}
}

func TestTagsBoostMatchingNodes(t *testing.T) {
	g, ix := retrievalFixture(t)

	tagged := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Tags:   []string{"auth"},
		Budget: 2000,
	})
	if len(tagged.Nodes) == 0 {
		t.Fatal("expected a tag-only query to return the tagged memory")
	}
	if !strings.Contains(tagged.Text, "rotating key set") {
		t.Errorf("expected the tagged memory:\n%s", tagged.Text)
	}
}

// An empty query cannot be off-topic, so structural ranking alone decides and
// the floor must not truncate a valid overview to nothing.
func TestEmptyQueryReturnsAnOverview(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{Budget: 4000})
	if len(result.Nodes) == 0 {
		t.Error("expected an overview when no query is given")
	}
}

func TestKindFilterRestrictsResults(t *testing.T) {
	g, ix := retrievalFixture(t)

	result := retrieve.Retrieve(g, ix, config.Default(), retrieve.Query{
		Text:   "session",
		Budget: 4000,
		Kinds:  []graph.Kind{graph.KindFile},
	})
	for _, s := range result.Nodes {
		if s.Node.Kind != graph.KindFile {
			t.Errorf("expected only files, got %s", s.Node.Kind)
		}
	}
}
