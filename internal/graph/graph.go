package graph

import (
	"sort"
	"strings"
)

// edgeKey identifies an edge uniquely so repeated extraction collapses instead
// of accumulating parallel duplicates.
type edgeKey struct {
	from string
	to   string
	kind EdgeKind
}

// Graph is the in-memory projection of graph.snap plus graph.log.
//
// It is a plain adjacency structure with no query engine and no persistence of
// its own: callers load it, use it, and drop it. That is what keeps memor a
// bounded one-shot command rather than a database.
type Graph struct {
	nodes   map[string]*Node
	edges   map[edgeKey]*Edge
	out     map[string][]*Edge
	in      map[string][]*Edge
	BuiltAt int64
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		nodes: make(map[string]*Node),
		edges: make(map[edgeKey]*Edge),
		out:   make(map[string][]*Edge),
		in:    make(map[string][]*Edge),
	}
}

// AddNode inserts or replaces a node. Replacement merges metadata so an
// agent-authored summary survives a later extraction pass over the same file.
func (g *Graph) AddNode(n *Node) {
	if n == nil || n.ID == "" {
		return
	}
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

// AddEdge inserts an edge, keeping the highest weight seen for a given triple.
// Edges pointing at nodes that do not exist are dropped: a dangling edge is a
// distractor waiting to happen.
func (g *Graph) AddEdge(e Edge) {
	if e.From == "" || e.To == "" || e.From == e.To {
		return
	}
	key := edgeKey{e.From, e.To, e.Kind}
	if existing, ok := g.edges[key]; ok {
		if e.W > existing.W {
			existing.W = e.W
		}
		return
	}
	stored := e
	if stored.W == 0 {
		stored.W = 1
	}
	g.edges[key] = &stored
	g.out[e.From] = append(g.out[e.From], &stored)
	g.in[e.To] = append(g.in[e.To], &stored)
}

// RemoveNode deletes a node and every edge incident to it.
func (g *Graph) RemoveNode(id string) {
	if _, ok := g.nodes[id]; !ok {
		return
	}
	delete(g.nodes, id)
	for key := range g.edges {
		if key.from == id || key.to == id {
			delete(g.edges, key)
		}
	}
	g.reindexAdjacency()
}

// RemoveEdge deletes a single relation.
func (g *Graph) RemoveEdge(from, to string, kind EdgeKind) {
	key := edgeKey{from, to, kind}
	if _, ok := g.edges[key]; !ok {
		return
	}
	delete(g.edges, key)
	g.reindexAdjacency()
}

// PruneExtracted drops every machine-derived node and its edges. A rebuild runs
// this first so deleted files and renamed symbols cannot linger as ghosts.
func (g *Graph) PruneExtracted() {
	for id, n := range g.nodes {
		if n.Origin() == OriginExtract {
			delete(g.nodes, id)
		}
	}
	for key, e := range g.edges {
		switch e.Kind {
		case EdgeImports, EdgeContains, EdgeCalls, EdgeRefs:
			delete(g.edges, key)
		default:
			if _, ok := g.nodes[key.from]; !ok {
				delete(g.edges, key)
				continue
			}
			if _, ok := g.nodes[key.to]; !ok {
				delete(g.edges, key)
			}
		}
	}
	g.reindexAdjacency()
}

// Resolve drops edges whose endpoints no longer exist. Callers run it after a
// bulk load so retrieval never walks into a missing node.
func (g *Graph) Resolve() {
	for key := range g.edges {
		if _, ok := g.nodes[key.from]; !ok {
			delete(g.edges, key)
			continue
		}
		if _, ok := g.nodes[key.to]; !ok {
			delete(g.edges, key)
		}
	}
	g.reindexAdjacency()
}

func (g *Graph) reindexAdjacency() {
	g.out = make(map[string][]*Edge, len(g.out))
	g.in = make(map[string][]*Edge, len(g.in))
	for _, e := range g.edges {
		g.out[e.From] = append(g.out[e.From], e)
		g.in[e.To] = append(g.in[e.To], e)
	}
}

// Node looks up a node by ID.
func (g *Graph) Node(id string) (*Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// NodeCount returns the number of nodes.
func (g *Graph) NodeCount() int { return len(g.nodes) }

// EdgeCount returns the number of edges.
func (g *Graph) EdgeCount() int { return len(g.edges) }

// Nodes returns every node in a stable order.
func (g *Graph) Nodes() []*Node {
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// CountByKind reports how many nodes exist per kind.
func (g *Graph) CountByKind() map[string]int {
	counts := make(map[string]int, len(kindCodes))
	for _, n := range g.nodes {
		counts[n.Kind.String()]++
	}
	return counts
}

// Edges returns every edge in a stable order.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, 0, len(g.edges))
	for _, e := range g.edges {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].To < out[j].To
	})
	return out
}

// Out returns edges leaving a node.
func (g *Graph) Out(id string) []*Edge { return g.out[id] }

// In returns edges entering a node. The reverse direction answers "what breaks
// if I change this?", which is what agents most often need, so it is kept
// precomputed rather than derived per query.
func (g *Graph) In(id string) []*Edge { return g.in[id] }

// Neighbors returns the IDs reachable from id in either direction.
func (g *Graph) Neighbors(id string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, e := range g.out[id] {
		if _, ok := seen[e.To]; !ok {
			seen[e.To] = struct{}{}
			out = append(out, e.To)
		}
	}
	for _, e := range g.in[id] {
		if _, ok := seen[e.From]; !ok {
			seen[e.From] = struct{}{}
			out = append(out, e.From)
		}
	}
	return out
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
		return spanPath(result[i]) < spanPath(result[j])
	})
	return result
}

func spanPath(n *Node) string {
	if n.Span == nil {
		return ""
	}
	return n.Span.Path
}

// TopicIDs maps tag names onto topic node IDs, creating none.
func (g *Graph) TopicIDs(tags []string) []string {
	var out []string
	for _, t := range tags {
		id := NodeID(KindTopic, strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "#"))))
		if _, ok := g.nodes[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// Tags returns the topic names attached to a node.
func (g *Graph) Tags(id string) []string {
	var out []string
	for _, e := range g.out[id] {
		if e.Kind != EdgeTagged {
			continue
		}
		if topic, ok := g.nodes[e.To]; ok {
			out = append(out, topic.Name)
		}
	}
	sort.Strings(out)
	return out
}
