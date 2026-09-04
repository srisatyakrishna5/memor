package graph

import (
	"sort"
	"strings"
)

// Graph is the in-memory projection of graph.snap plus graph.log.
//
// It is a flat node set with no adjacency and no persistence of its own:
// callers load it, use it, and drop it. Relations live in node metadata, so
// there is nothing here to traverse and no way for a query to expand beyond
// what it matched.
type Graph struct {
	nodes   map[string]*Node
	BuiltAt int64

	// explains is the only reverse lookup kept, because rendering a file needs
	// the memories attached to it and scanning every memory per file is
	// quadratic. It is rebuilt on demand and dropped on any mutation.
	explains map[string][]*Node
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{nodes: make(map[string]*Node)}
}

// explainIndex maps a node ID onto the memories that reference it.
func (g *Graph) explainIndex() map[string][]*Node {
	if g.explains != nil {
		return g.explains
	}
	index := make(map[string][]*Node)
	for _, n := range g.nodes {
		if n.Kind != KindMem {
			continue
		}
		for _, target := range n.MetaList(MetaExplains) {
			index[target] = append(index[target], n)
		}
	}
	g.explains = index
	return index
}

// AddNode inserts or replaces a node. Replacement merges metadata so an
// agent-authored summary survives a later extraction pass over the same file.
func (g *Graph) AddNode(n *Node) {
	if n == nil || n.ID == "" {
		return
	}
	g.explains = nil
	existing, ok := g.nodes[n.ID]
	if !ok {
		clone := *n
		clone.Meta = copyMeta(n.Meta)
		g.nodes[n.ID] = &clone
		return
	}

	merged := *n
	merged.Meta = copyMeta(existing.Meta)
	for k, v := range n.Meta {
		merged.Meta[k] = v
	}
	// Extraction knows structure but not intent, so it must not blank a summary
	// an agent wrote. Agent writes always win.
	if merged.Text == "" {
		merged.Text = existing.Text
	}
	if n.Origin() == OriginExtract && existing.Origin() == OriginAgent {
		if existing.Text != "" {
			merged.Text = existing.Text
		}
		merged.Meta[MetaOrigin] = OriginAgent
	}
	if merged.Span == nil {
		merged.Span = existing.Span
	}
	if merged.Exp == 0 {
		merged.Exp = existing.Exp
	}
	g.nodes[n.ID] = &merged
}

func copyMeta(m map[string]string) map[string]string {
	if m == nil {
		return make(map[string]string, 4)
	}
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// RemoveNode deletes a node.
func (g *Graph) RemoveNode(id string) {
	delete(g.nodes, id)
	g.explains = nil
}

// PruneExtracted drops every machine-derived node. A rebuild runs this first so
// deleted files and renamed symbols cannot linger as ghosts.
func (g *Graph) PruneExtracted() {
	g.explains = nil
	for id, n := range g.nodes {
		if n.Origin() == OriginExtract {
			delete(g.nodes, id)
		}
	}
}

// Node looks up a node by ID.
func (g *Graph) Node(id string) (*Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// NodeCount returns the number of nodes.
func (g *Graph) NodeCount() int { return len(g.nodes) }

// Nodes returns every node in a stable order.
func (g *Graph) Nodes() []*Node {
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	sortNodes(out)
	return out
}

// NodesOfKind returns every node of one kind in a stable order.
func (g *Graph) NodesOfKind(k Kind) []*Node {
	var out []*Node
	for _, n := range g.nodes {
		if n.Kind == k {
			out = append(out, n)
		}
	}
	sortNodes(out)
	return out
}

func sortNodes(out []*Node) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
}

// CountByKind reports how many nodes exist per kind.
func (g *Graph) CountByKind() map[string]int {
	counts := make(map[string]int, len(kindCodes))
	for _, n := range g.nodes {
		counts[n.Kind.String()]++
	}
	return counts
}

// FindFile returns the file node for a project-relative path.
func (g *Graph) FindFile(path string) (*Node, bool) {
	return g.Node(NodeID(KindFile, path))
}

// FindSymbols returns symbol nodes whose name matches, case-insensitively.
// An exact match is returned alone; otherwise substring matches are returned so
// a partial name still resolves.
func (g *Graph) FindSymbols(name string) []*Node {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return nil
	}
	var exact, partial []*Node
	for _, n := range g.nodes {
		if n.Kind != KindSym {
			continue
		}
		lower := strings.ToLower(n.Name)
		switch {
		case lower == needle:
			exact = append(exact, n)
		case strings.Contains(lower, needle):
			partial = append(partial, n)
		}
	}
	result := exact
	if len(result) == 0 {
		result = partial
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return SpanPath(result[i]) < SpanPath(result[j])
	})
	return result
}

// SymbolsIn returns the symbol nodes defined in a file, ordered by position.
func (g *Graph) SymbolsIn(path string) []*Node {
	var out []*Node
	for _, n := range g.nodes {
		if n.Kind == KindSym && SpanPath(n) == path {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := lineOf(out[i]), lineOf(out[j])
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func lineOf(n *Node) int {
	if n.Span == nil {
		return 0
	}
	return n.Span.L0
}

// SpanPath returns the file a node's span points at, or "".
func SpanPath(n *Node) string {
	if n == nil || n.Span == nil {
		return ""
	}
	return n.Span.Path
}

// Explaining returns the memories attached to a node.
func (g *Graph) Explaining(id string) []*Node {
	out := append([]*Node(nil), g.explainIndex()[id]...)
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	return out
}

// AllTags returns every tag name in use, sorted.
func (g *Graph) AllTags() []string {
	var all []string
	for _, n := range g.nodes {
		all = append(all, n.Tags()...)
	}
	return DedupeSorted(all)
}

// NodesTagged returns the IDs of nodes carrying any of the given tags.
func (g *Graph) NodesTagged(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		if norm := NormalizeTag(t); norm != "" {
			want[norm] = struct{}{}
		}
	}
	var out []string
	for id, n := range g.nodes {
		for _, t := range n.Tags() {
			if _, ok := want[t]; ok {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
