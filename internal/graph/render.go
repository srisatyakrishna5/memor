package graph

import (
	"fmt"
	"path"
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
// why memor has no bespoke DSL parser to keep in sync with the writer.
func Render(g *Graph, cfg config.Config) string {
	var sb strings.Builder

	counts := g.CountByKind()
	built := g.BuiltAt
	if built == 0 {
		built = time.Now().Unix()
	}
	fmt.Fprintf(&sb, "@g v3 | %d files | %d symbols | %d docs | %d memories | built:%s\n",
		counts[KindFile.String()], counts[KindSym.String()], counts[KindDoc.String()],
		counts[KindMem.String()],
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
	if pkg := f.MetaValue(MetaPkg); pkg != "" {
		header += " pkg:" + pkg
	}
	sb.WriteString(header)
	sb.WriteByte('\n')

	if purpose := fileSummary(f); purpose != "" {
		fmt.Fprintf(&sb, "  : %s\n", purpose)
	}

	symbols := g.SymbolsIn(f.Name)
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

	if deps := f.MetaList(MetaImports); len(deps) > 0 {
		fmt.Fprintf(&sb, "  -> %s\n", strings.Join(deps, ", "))
	}
	// The reverse direction answers "what breaks if I change this?", which is
	// the question agents actually ask, and it is free to precompute.
	if dependents := f.MetaList(MetaDependents); len(dependents) > 0 {
		fmt.Fprintf(&sb, "  <- %s\n", strings.Join(dependents, ", "))
	}
	if tags := f.Tags(); len(tags) > 0 {
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
	for _, t := range m.Tags() {
		sb.WriteString(" #" + t)
	}
	if task := m.MetaValue(MetaTask); task != "" {
		fmt.Fprintf(&sb, " [%s/%s]", task, orDash(m.MetaValue(MetaStatus)))
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

// fileSummary prefers an agent-authored summary and falls back to the purpose
// line extraction derived from the file's own leading comment.
func fileSummary(f *Node) string {
	if f.Text != "" {
		return f.Text
	}
	return f.MetaValue(MetaPurpose)
}

func explainingMemories(g *Graph, targetID string) []*Node {
	out := append([]*Node(nil), g.explainIndex()[targetID]...)
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	return out
}

// looseMemories returns memories attached to nothing, so they still surface in
// the projection rather than vanishing between file blocks.
func looseMemories(g *Graph) []*Node {
	var out []*Node
	for _, m := range g.NodesOfKind(KindMem) {
		if len(m.MetaList(MetaExplains)) == 0 {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	return out
}

// RenderManifest produces the cheapest useful view of a repository: one line
// per file, grouped by directory, with symbol names but no signatures.
//
// This is what an agent reads first. Every field it carries has to earn its
// tokens, so bodies, spans, imports and hashes are all deliberately absent.
func RenderManifest(g *Graph, maxSymbols int) string {
	byDir := make(map[string][]*Node)
	for _, f := range g.NodesOfKind(KindFile) {
		dir := f.MetaValue(MetaPkg)
		if dir == "" {
			dir = "(root)"
		}
		byDir[dir] = append(byDir[dir], f)
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var sb strings.Builder
	for _, dir := range dirs {
		fmt.Fprintf(&sb, "%s/\n", dir)
		for _, f := range byDir[dir] {
			fmt.Fprintf(&sb, "  %s", path.Base(f.Name))
			if purpose := fileSummary(f); purpose != "" {
				fmt.Fprintf(&sb, " — %s", collapse(purpose))
			}
			sb.WriteByte('\n')
			if syms := f.MetaList(MetaSymbols); len(syms) > 0 && maxSymbols != 0 {
				shown := syms
				if maxSymbols > 0 && len(shown) > maxSymbols {
					shown = shown[:maxSymbols]
				}
				fmt.Fprintf(&sb, "    %s", strings.Join(shown, " "))
				if len(syms) > len(shown) {
					fmt.Fprintf(&sb, " +%d", len(syms)-len(shown))
				}
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// RenderToFile writes the projection to graph.db.
func RenderToFile(path string, g *Graph, cfg config.Config) error {
	return store.WriteFileAtomic(path, []byte(Render(g, cfg)))
}
