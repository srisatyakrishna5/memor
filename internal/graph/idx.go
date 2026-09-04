package graph

import (
	"math"
	"sort"
	"strings"
)

// Index holds the BM25 term postings derived from the graph. It is rebuilt on
// every load and never persisted: without edges there is no offline signal
// worth caching, and a cache that can drift is a source of silent misranking.
type Index struct {
	ids       []string
	pos       map[string]int
	termFreq  []map[string]int
	docLen    []float64
	docFreq   map[string]int
	avgDocLen float64
	docCount  int
}

// BuildIndex computes term postings over a graph.
func BuildIndex(g *Graph) *Index {
	nodes := g.Nodes()
	ix := &Index{
		ids:      make([]string, len(nodes)),
		pos:      make(map[string]int, len(nodes)),
		termFreq: make([]map[string]int, len(nodes)),
		docLen:   make([]float64, len(nodes)),
		docFreq:  make(map[string]int),
		docCount: len(nodes),
	}

	total := 0.0
	for i, n := range nodes {
		ix.ids[i] = n.ID
		ix.pos[n.ID] = i

		terms := tokenize(indexText(n))
		ix.docLen[i] = float64(len(terms))
		total += ix.docLen[i]

		tf := make(map[string]int, len(terms))
		for _, t := range terms {
			tf[t]++
		}
		ix.termFreq[i] = tf
		for t := range tf {
			ix.docFreq[t]++
		}
	}
	if ix.docCount > 0 {
		ix.avgDocLen = total / float64(ix.docCount)
	}
	return ix
}

// indexText is the searchable surface of a node: its name, its text, and the
// tags attached to it. Path segments are split so "internal/engine/context.go"
// matches a query for "engine".
func indexText(n *Node) string {
	var sb strings.Builder
	sb.WriteString(splitIdentifier(n.Name))
	sb.WriteByte(' ')
	sb.WriteString(n.Text)
	sb.WriteByte(' ')
	sb.WriteString(n.MetaValue(MetaPurpose))
	for _, tag := range n.Tags() {
		sb.WriteByte(' ')
		sb.WriteString(tag)
	}
	if n.Span != nil && n.Span.Path != "" && n.Span.Path != n.Name {
		sb.WriteByte(' ')
		sb.WriteString(splitIdentifier(n.Span.Path))
	}

	// A file inherits the names of the symbols it contains. Without this a
	// query naming a function scores its bare symbol node highly and the file
	// that defines it not at all, so the answer arrives without its context.
	if n.Kind == KindFile {
		for _, sym := range n.MetaList(MetaSymbols) {
			sb.WriteByte(' ')
			sb.WriteString(splitIdentifier(sym))
		}
	}
	return sb.String()
}

// splitIdentifier expands paths and camelCase so a natural-language query lines
// up with machine-shaped names.
func splitIdentifier(s string) string {
	var sb strings.Builder
	sb.WriteString(s)
	sb.WriteByte(' ')

	replaced := strings.NewReplacer("/", " ", "\\", " ", ".", " ", "_", " ", "-", " ", "#", " ").Replace(s)
	sb.WriteString(replaced)
	sb.WriteByte(' ')

	for _, word := range strings.Fields(replaced) {
		var current strings.Builder
		for i, r := range word {
			if i > 0 && r >= 'A' && r <= 'Z' {
				sb.WriteString(current.String())
				sb.WriteByte(' ')
				current.Reset()
			}
			current.WriteRune(r)
		}
		sb.WriteString(current.String())
		sb.WriteByte(' ')
	}
	return sb.String()
}

// tokenize splits text into stemmed terms. Indexing and querying must share
// this function or a query term will never line up with an indexed one.
func tokenize(text string) []string {
	raw := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	out := make([]string, 0, len(raw))
	for _, term := range raw {
		out = append(out, stem(term))
	}
	return out
}

// inflections are stripped longest-first so "compaction" reduces to "compact"
// rather than losing only its trailing "n".
var inflections = []string{
	"ations", "ation", "ition", "ings", "ing", "ions", "ion",
	"ments", "ment", "ness", "edly", "ers", "er", "ed", "es", "s", "ly",
}

// stem applies conservative English suffix stripping.
//
// Without it a natural-language query and a machine-shaped identifier never
// meet: "compaction" and Compact share no token, so the file that implements
// compaction scores zero against a question about it. Full Porter stemming
// would be more accurate but is a dependency and a behaviour change on every
// term; this handles the cases that actually arise between prose and code.
func stem(word string) string {
	if len(word) <= 4 {
		return word
	}
	for _, suffix := range inflections {
		if !strings.HasSuffix(word, suffix) {
			continue
		}
		if len(word)-len(suffix) < 3 {
			continue
		}
		word = word[:len(word)-len(suffix)]
		break
	}
	// A silent trailing "e" is what separates "handle" from "handled" once the
	// inflection is gone, so it goes too.
	if len(word) > 4 && strings.HasSuffix(word, "e") {
		word = word[:len(word)-1]
	}
	return word
}

// BM25 scores one node against a query. Standard k1/b parameters.
func (ix *Index) BM25(nodeID, query string) float64 {
	if ix.avgDocLen == 0 {
		return 0
	}
	i, ok := ix.pos[nodeID]
	if !ok {
		return 0
	}

	const (
		k1 = 1.2
		b  = 0.75
	)

	tf := ix.termFreq[i]
	dl := ix.docLen[i]

	score := 0.0
	for _, qt := range tokenize(query) {
		f := float64(tf[qt])
		if f == 0 {
			continue
		}
		df := float64(ix.docFreq[qt])
		idf := math.Log((float64(ix.docCount)-df+0.5)/(df+0.5) + 1.0)
		score += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*(dl/ix.avgDocLen)))
	}
	return score
}

// Seeds returns node IDs whose indexed text matches the query at all, ordered
// by raw BM25. Nothing expands beyond them: a node that does not match the
// query never enters the result set.
func (ix *Index) Seeds(query string, limit int) []string {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	type hit struct {
		id    string
		score float64
	}
	var hits []hit
	for _, id := range ix.ids {
		if s := ix.BM25(id, query); s > 0 {
			hits = append(hits, hit{id, s})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].id < hits[j].id
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.id
	}
	return out
}

// MaxBM25 reports the highest BM25 score in the corpus for a query, used to
// normalize scores into 0..1 before the weighted blend.
func (ix *Index) MaxBM25(query string) float64 {
	best := 0.0
	for _, id := range ix.ids {
		if s := ix.BM25(id, query); s > best {
			best = s
		}
	}
	return best
}
