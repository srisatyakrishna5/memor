package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/store"
)

// Render produces the graph.db projection: a compact, human- and agent-readable
// map of the repository.
//
// Nothing parses this format back. It is write-only output, which is precisely
// why v2 has no bespoke DSL parser to keep in sync with the writer.
func Render(g *Graph, cfg config.Config) string {
	var sb strings.Builder

	counts := g.CountByKind()
	built := g.BuiltAt
	if built == 0 {
		built = time.Now().Unix()
	}
	fmt.Fprintf(&sb, "@g v2 | %d files | %d symbols | %d docs | %d memories | %d edges | built:%s\n",
		counts[KindFile.String()], counts[KindSym.String()], counts[KindDoc.String()],
		counts[KindMem.String()], g.EdgeCount(),
		time.Unix(built, 0).UTC().Format(time.RFC3339))

	files := g.NodesOfKind(KindFile)
	for _, f := range files {
		sb.WriteByte('\n')
		sb.WriteString(RenderFile(g, f, cfg.Retrieval.MaxSymbols))
	}

	if docs := g.NodesOfKind(KindDoc); len(docs) > 0 {
		sb.WriteString("\n")
		for _, d := range docs {
			sb.WriteString(RenderDoc(g, d))
		}
	}

	if loose := looseMemories(g); len(loose) > 0 {
		sb.WriteString("\n")
		for _, m := range loose {
			sb.WriteString(RenderMemory(g, m))
		}
	}

	return sb.String()
}

// RenderFile formats one file node with its symbols, dependencies, dependents,
// and any memories that explain it.
func RenderFile(g *Graph, f *Node, maxSymbols int) string {
	var sb strings.Builder

	hash := ""
	if f.Span != nil {
		hash = f.Span.Hash
	}
	header := fmt.Sprintf("@f %s [%s LOC | %s]", f.Name, orDash(f.MetaValue(MetaLOC)), orDash(hash))
	if pkg := packageOf(g, f); pkg != "" {
		header += " pkg:" + pkg
	}
	sb.WriteString(header)
	sb.WriteByte('\n')

	if f.Text != "" {
		fmt.Fprintf(&sb, "  : %s\n", f.Text)
	}

	symbols := containedSymbols(g, f)
	shown := symbols
	if maxSymbols > 0 && len(shown) > maxSymbols {
		shown = shown[:maxSymbols]
	}
	for _, s := range shown {
		line := s.Text
		if line == "" {
			line = s.Name
		}
		if s.Span != nil && s.Span.L0 > 0 {
			fmt.Fprintf(&sb, "  %s  @%d-%d\n", line, s.Span.L0, s.Span.L1)
		} else {
			fmt.Fprintf(&sb, "  %s\n", line)
		}
	}
	if len(symbols) > len(shown) {
		fmt.Fprintf(&sb, "  ... %d more symbols\n", len(symbols)-len(shown))
	}

	if deps := edgeTargets(g, f.ID, EdgeImports, true); len(deps) > 0 {
		fmt.Fprintf(&sb, "  -> %s\n", strings.Join(deps, ", "))
	}
	// The reverse direction answers "what breaks if I change this?", which is
	// the question agents actually ask, and it is free to precompute.
	if dependents := edgeTargets(g, f.ID, EdgeImports, false); len(dependents) > 0 {
		fmt.Fprintf(&sb, "  <- %s\n", strings.Join(dependents, ", "))
	}
	if tags := g.Tags(f.ID); len(tags) > 0 {
		fmt.Fprintf(&sb, "  # %s\n", strings.Join(tags, " "))
	}

	for _, m := range explainingMemories(g, f.ID) {
		sb.WriteString(renderExplains(m))
	}
	for _, s := range shown {
		for _, m := range explainingMemories(g, s.ID) {
			fmt.Fprintf(&sb, "  %s%s", s.Name+" ", strings.TrimPrefix(renderExplains(m), "  "))
		}
	}

	return sb.String()
}

// RenderDoc formats a knowledge section.
func RenderDoc(g *Graph, d *Node) string {
	summary := collapse(d.Text)
	if len(summary) > 220 {
		summary = summary[:220] + "..."
	}
	line := fmt.Sprintf("@d %s :: %s", orDash(d.MetaValue(MetaSource)), d.Name)
	if summary != "" {
		line += " — " + summary
	}
	return line + "\n"
}

// RenderMemory formats one memory node.
func RenderMemory(g *Graph, m *Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "@%s", m.MemType())
	if tags := g.Tags(m.ID); len(tags) > 0 {
		for _, t := range tags {
			sb.WriteString(" #" + t)
		}
	}
	fmt.Fprintf(&sb, ": %s [%s]\n", collapse(m.Text), datestamp(m))
	return sb.String()
}

func renderExplains(m *Node) string {
	return fmt.Sprintf("  ~ %q [%s, %s]\n", collapse(m.Text), MemTypeName(m.MemType()), datestamp(m))
}

func datestamp(n *Node) string {
	if n.Exp == -1 || n.MemType() == MemPreference {
		return "perm"
	}
	if n.T == 0 {
		return time.Now().Format("2006-01-02")
	}
	return time.Unix(n.T, 0).Format("2006-01-02")
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func packageOf(g *Graph, f *Node) string {
	if pkg := f.MetaValue(MetaPkg); pkg != "" {
		return pkg
	}
	for _, e := range g.In(f.ID) {
		if e.Kind != EdgeContains {
			continue
		}
		if n, ok := g.Node(e.From); ok && n.Kind == KindPkg {
			return n.Name
		}
	}
	return ""
}

func containedSymbols(g *Graph, f *Node) []*Node {
	var out []*Node
	for _, e := range g.Out(f.ID) {
		if e.Kind != EdgeContains {
			continue
		}
		if n, ok := g.Node(e.To); ok && n.Kind == KindSym {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := 0, 0
		if out[i].Span != nil {
			li = out[i].Span.L0
		}
		if out[j].Span != nil {
			lj = out[j].Span.L0
		}
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func edgeTargets(g *Graph, id string, kind EdgeKind, outgoing bool) []string {
	var edges []*Edge
	if outgoing {
		edges = g.Out(id)
	} else {
		edges = g.In(id)
	}

	var names []string
	seen := make(map[string]struct{})
	for _, e := range edges {
		if e.Kind != kind {
			continue
		}
		other := e.To
		if !outgoing {
			other = e.From
		}
		n, ok := g.Node(other)
		if !ok {
			continue
		}
		if _, dup := seen[n.Name]; dup {
			continue
		}
		seen[n.Name] = struct{}{}
		names = append(names, n.Name)
	}
	sort.Strings(names)
	return names
}

func explainingMemories(g *Graph, targetID string) []*Node {
	var out []*Node
	for _, e := range g.In(targetID) {
		if e.Kind != EdgeExplains {
			continue
		}
		if n, ok := g.Node(e.From); ok && n.Kind == KindMem {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	return out
}

// looseMemories returns memories that explain nothing, so they still surface
// in the projection rather than vanishing between file blocks.
func looseMemories(g *Graph) []*Node {
	var out []*Node
	for _, m := range g.NodesOfKind(KindMem) {
		attached := false
		for _, e := range g.Out(m.ID) {
			if e.Kind == EdgeExplains {
				attached = true
				break
			}
		}
		if !attached {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	return out
}

// RenderToFile writes the projection to graph.db.
func RenderToFile(path string, g *Graph, cfg config.Config) error {
	return store.WriteFileAtomic(path, []byte(Render(g, cfg)))
}
