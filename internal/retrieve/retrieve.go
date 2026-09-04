// Package retrieve implements memor's one ranking and packing pipeline.
//
// It scores only what the query actually matched. There is no graph walk, so a
// result set can never grow past the nodes a query names — which is the whole
// point: an agent asking about one thing must not be handed its neighbourhood.
package retrieve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/token"
)

// Query describes what to retrieve.
type Query struct {
	Text      string
	Tags      []string
	OpenFiles []string
	Paths     []string // restrict results to these path prefixes
	Changed   []string // paths changed since the caller last looked
	Budget    int
	MinScore  float64
	Limit     int
	Kinds     []graph.Kind
}

// Scored pairs a node with its blended relevance score.
type Scored struct {
	Node  *graph.Node
	Score float64
}

// Result is what the pipeline produced and what it cost.
type Result struct {
	Nodes    []Scored
	Text     string
	Tokens   int
	Budget   int
	Rejected int
}

const maxSeeds = 24

// Retrieve ranks the store for a query and packs the survivors into a budget.
func Retrieve(g *graph.Graph, ix *graph.Index, cfg config.Config, q Query) Result {
	q = q.withDefaults(cfg)

	candidates := candidateNodes(g, ix, q)
	scored, masked := score(g, ix, cfg, q, candidates)
	kept, gated := gate(scored, q)
	packed, text, tokens := pack(g, cfg, kept, q.Budget)

	return Result{
		Nodes:    packed,
		Text:     text,
		Tokens:   tokens,
		Budget:   q.Budget,
		Rejected: masked + gated,
	}
}

func (q Query) withDefaults(cfg config.Config) Query {
	q.Text = strings.TrimSpace(q.Text)
	if q.Budget <= 0 {
		q.Budget = cfg.Memory.TokenBudget
	}
	if q.MinScore <= 0 {
		q.MinScore = cfg.Retrieval.MinScore
	}
	return q
}

// candidateNodes collects everything eligible to be scored: nodes the query
// names directly, the files the caller already has open, and anything carrying
// a requested tag. An empty query falls back to the whole store, because with
// nothing to match against there is nothing to be off-topic about.
func candidateNodes(g *graph.Graph, ix *graph.Index, q Query) []string {
	set := make(map[string]struct{})

	for _, id := range ix.Seeds(q.Text, maxSeeds) {
		set[id] = struct{}{}
	}
	for _, path := range q.OpenFiles {
		if n, ok := g.FindFile(strings.TrimSpace(path)); ok {
			set[n.ID] = struct{}{}
		}
	}
	for _, id := range g.NodesTagged(q.Tags) {
		set[id] = struct{}{}
	}
	// An exact symbol name in the query is a much stronger signal than any BM25
	// score it happens to produce.
	for _, word := range strings.Fields(q.Text) {
		for _, n := range g.FindSymbols(strings.Trim(word, "()[]{}.,;:\"'`")) {
			if strings.EqualFold(n.Name, word) {
				set[n.ID] = struct{}{}
			}
		}
	}

	if len(set) == 0 && q.Text == "" && len(q.Tags) == 0 {
		for _, n := range g.Nodes() {
			set[n.ID] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// score blends the ranking signals and reports how many candidates it masked
// out for having no query relevance at all.
func score(g *graph.Graph, ix *graph.Index, cfg config.Config, q Query, candidates []string) ([]Scored, int) {
	maxBM25 := ix.MaxBM25(q.Text)

	tagSet := make(map[string]struct{}, len(q.Tags))
	for _, t := range q.Tags {
		tagSet[graph.NormalizeTag(t)] = struct{}{}
	}
	kindFilter := make(map[graph.Kind]struct{}, len(q.Kinds))
	for _, k := range q.Kinds {
		kindFilter[k] = struct{}{}
	}
	changedSet := make(map[string]struct{}, len(q.Changed))
	for _, p := range q.Changed {
		changedSet[strings.TrimSpace(p)] = struct{}{}
	}

	out := make([]Scored, 0, len(candidates))
	masked := 0
	for _, id := range candidates {
		n, ok := g.Node(id)
		if !ok || n.IsExpired() {
			continue
		}
		if len(kindFilter) > 0 {
			if _, want := kindFilter[n.Kind]; !want {
				continue
			}
		}
		if !withinPaths(n, q.Paths) {
			continue
		}

		bm25 := 0.0
		if q.Text != "" && maxBM25 > 0 {
			bm25 = ix.BM25(id, q.Text) / maxBM25
		}

		tagBoost := 0.0
		if len(tagSet) > 0 {
			for _, tag := range n.Tags() {
				if _, ok := tagSet[tag]; ok {
					tagBoost = 1
					break
				}
			}
		}

		// A node that matched nothing about the query is a distractor, and a
		// distractor costs more accuracy than an outright omission.
		if q.Text != "" && bm25 == 0 && tagBoost == 0 {
			masked++
			continue
		}

		changed := 0.0
		if _, ok := changedSet[nodePath(n)]; ok {
			changed = 1
		}

		blended := cfg.Retrieval.BM25*bm25 +
			cfg.Retrieval.Changed*changed +
			cfg.Retrieval.Tag*tagBoost +
			cfg.Retrieval.Recency*recency(n, cfg)

		out = append(out, Scored{Node: n, Score: blended * kindWeight(n, cfg) * testPenalty(n)})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Node.ID < out[j].Node.ID
	})
	return out, masked
}

func nodePath(n *graph.Node) string {
	if p := graph.SpanPath(n); p != "" {
		return p
	}
	return n.Name
}

func withinPaths(n *graph.Node, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	path := nodePath(n)
	for _, prefix := range prefixes {
		if prefix = strings.TrimSpace(prefix); prefix != "" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func recency(n *graph.Node, cfg config.Config) float64 {
	if n.Kind != graph.KindMem {
		return 0.5
	}
	return 1.0 / (1.0 + n.AgeDays()*cfg.Memory.Decay.Rate)
}

// kindWeight biases the blend toward the granularity most likely to answer a
// question: files and decisions over individual symbols, symbols over bare
// package and dependency nodes.
func kindWeight(n *graph.Node, cfg config.Config) float64 {
	switch n.Kind {
	case graph.KindFile:
		return constants.WeightFile
	case graph.KindSym:
		return constants.WeightSym
	case graph.KindDoc:
		return constants.WeightDoc
	case graph.KindPkg:
		return constants.WeightPkg
	case graph.KindMem:
		return cfg.TypeWeight(n.MemType())
	default:
		return constants.WeightPkg
	}
}

// testPenalty discounts test files. They mention every term the code under test
// mentions, so without this they crowd out the implementation that actually
// answers a "where is X handled" question.
func testPenalty(n *graph.Node) float64 {
	path := n.Name
	if n.Span != nil && n.Span.Path != "" {
		path = n.Span.Path
	}
	if isTestPath(path) {
		return 0.5
	}
	return 1
}

func isTestPath(path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, "_test.go"),
		strings.HasSuffix(lower, ".test.ts"),
		strings.HasSuffix(lower, ".test.js"),
		strings.HasSuffix(lower, ".spec.ts"),
		strings.HasSuffix(lower, ".spec.js"),
		strings.HasPrefix(lower, "test_"),
		strings.Contains(lower, "/test_"),
		strings.Contains(lower, "/tests/"),
		strings.Contains(lower, "/__tests__/"):
		return true
	default:
		return false
	}
}

// gate applies the precision floor. It drops rather than fills: a short block
// beats a padded one, because a single plausible-but-wrong node measurably
// degrades the model's answer and four compound it.
func gate(scored []Scored, q Query) ([]Scored, int) {
	if q.Text == "" {
		// With no query nothing can be off-topic, so structural ranking alone
		// decides and the floor would only truncate a valid overview.
		if q.Limit > 0 && len(scored) > q.Limit {
			return scored[:q.Limit], len(scored) - q.Limit
		}
		return scored, 0
	}

	kept := make([]Scored, 0, len(scored))
	for _, s := range scored {
		if s.Score < q.MinScore {
			continue
		}
		kept = append(kept, s)
		if q.Limit > 0 && len(kept) >= q.Limit {
			break
		}
	}
	return kept, len(scored) - len(kept)
}

// maxSectionsPerDoc caps how many sections one document may contribute. A long
// design document mentions every term in the repository, so without a cap a
// single file can crowd out the code that actually answers the question.
const maxSectionsPerDoc = 2

// pack fills the budget and then reorders. Accuracy is highest at the start and
// end of a context window, so the strongest results take both ends and the
// weakest survivors sit in the middle where they cost least.
func pack(g *graph.Graph, cfg config.Config, scored []Scored, budget int) ([]Scored, string, int) {
	header := fmt.Sprintf("@g v3 | %d files | %d symbols | %d memories | budget:%d\n",
		g.CountByKind()[graph.KindFile.String()],
		g.CountByKind()[graph.KindSym.String()],
		g.CountByKind()[graph.KindMem.String()],
		budget)

	// Every candidate file is known up front, which is what lets a symbol and a
	// memory be suppressed when the block that already renders them is coming.
	candidateFiles := make(map[string]struct{}, len(scored))
	for _, s := range scored {
		if s.Node.Kind == graph.KindFile {
			candidateFiles[s.Node.ID] = struct{}{}
		}
	}

	used := token.Count(header)
	rendered := make([]string, 0, len(scored))
	selected := make([]Scored, 0, len(scored))
	docSections := make(map[string]int)

	for _, s := range scored {
		if redundant(g, s.Node, candidateFiles) {
			continue
		}
		if s.Node.Kind == graph.KindDoc {
			source := s.Node.MetaValue(graph.MetaSource)
			if docSections[source] >= maxSectionsPerDoc {
				continue
			}
			docSections[source]++
		}

		block := renderNode(g, cfg, s.Node)
		if block == "" {
			continue
		}
		cost := token.Count(block)
		if used+cost > budget {
			continue
		}
		used += cost
		rendered = append(rendered, block)
		selected = append(selected, s)
	}

	ordered, orderedBlocks := interleave(selected, rendered)

	var sb strings.Builder
	sb.WriteString(header)
	for _, block := range orderedBlocks {
		sb.WriteByte('\n')
		sb.WriteString(block)
	}
	return ordered, sb.String(), used
}

// redundant reports whether a node's content will already appear inside a file
// block that is also a candidate.
//
// This is the guard that makes the v1 double-emission defect inexpressible: a
// memory rendered inline on the file it explains, or a symbol listed in that
// file's signature list, is never emitted a second time on its own.
func redundant(g *graph.Graph, n *graph.Node, candidateFiles map[string]struct{}) bool {
	switch n.Kind {
	case graph.KindSym:
		if n.Span == nil {
			return false
		}
		_, ok := candidateFiles[graph.NodeID(graph.KindFile, n.Span.Path)]
		return ok
	case graph.KindMem:
		for _, target := range n.MetaList(graph.MetaExplains) {
			if _, ok := candidateFiles[target]; ok {
				return true
			}
			// A memory bound to a symbol renders inline on that symbol's file.
			if node, found := g.Node(target); found && node.Kind == graph.KindSym {
				if _, ok := candidateFiles[graph.NodeID(graph.KindFile, graph.SpanPath(node))]; ok {
					return true
				}
			}
		}
		return false
	default:
		return false
	}
}

// interleave places the strongest results at both ends of the block and the
// weakest in the middle.
func interleave(scored []Scored, blocks []string) ([]Scored, []string) {
	front := make([]Scored, 0, len(scored))
	frontBlocks := make([]string, 0, len(blocks))
	var back []Scored
	var backBlocks []string

	for i := range scored {
		if i%2 == 0 {
			front = append(front, scored[i])
			frontBlocks = append(frontBlocks, blocks[i])
			continue
		}
		back = append(back, scored[i])
		backBlocks = append(backBlocks, blocks[i])
	}
	for i := len(back) - 1; i >= 0; i-- {
		front = append(front, back[i])
		frontBlocks = append(frontBlocks, backBlocks[i])
	}
	return front, frontBlocks
}

func renderNode(g *graph.Graph, cfg config.Config, n *graph.Node) string {
	switch n.Kind {
	case graph.KindFile:
		return graph.RenderFile(g, n, cfg.Retrieval.MaxSymbols)
	case graph.KindMem:
		return graph.RenderMemory(g, n)
	case graph.KindDoc:
		return graph.RenderDoc(g, n)
	case graph.KindSym:
		return renderSymbol(n)
	case graph.KindPkg:
		return fmt.Sprintf("@p %s\n", n.Name)
	default:
		return ""
	}
}

func renderSymbol(n *graph.Node) string {
	signature := n.Text
	if signature == "" {
		signature = n.Name
	}
	if n.Span == nil {
		return fmt.Sprintf("@y %s\n", signature)
	}
	return fmt.Sprintf("@y %s :: %s  @%d-%d\n", n.Span.Path, signature, n.Span.L0, n.Span.L1)
}
