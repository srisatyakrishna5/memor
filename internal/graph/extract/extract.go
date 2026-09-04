// Package extract builds graph nodes and edges from a repository on disk.
//
// Extraction is deterministic and tiered. L0 recovers files, packages, and
// imports with a line-oriented scan that needs no dependencies. L1 recovers
// symbols, spans, and calls using the standard library's own parser. Neither
// tier makes a network call, invokes a model, or requires CGO — which is what
// keeps `memor build` a bounded local command and keeps the npm
// cross-compilation matrix intact.
package extract

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/graph"
)

// Result is the full set of machine-derived nodes and edges for a repository.
type Result struct {
	Nodes []*graph.Node
	Edges []graph.Edge

	FilesScanned int
	FilesSkipped int
}

// fileRecord carries per-file state between the scan pass and the resolve pass.
type fileRecord struct {
	rel     string
	dir     string
	lang    string
	nodeID  string
	hash    string
	imports []string
	symbols []Symbol
}

// Repo walks a project root and extracts its structure.
func Repo(root string, cfg config.Config) (Result, error) {
	if !cfg.Graph.Enabled {
		return Result{}, nil
	}

	b := &builder{
		root:       root,
		cfg:        cfg,
		byID:       make(map[string]*graph.Node),
		goModule:   goModulePath(root),
		filesByRel: make(map[string]*fileRecord),
		dirs:       make(map[string]struct{}),
	}

	if err := b.scan(); err != nil {
		return Result{}, err
	}
	if cfg.Knowledge.Enabled {
		b.scanDocs()
	}
	b.resolveImports()
	b.resolveCalls()

	return b.result(), nil
}

type builder struct {
	root     string
	cfg      config.Config
	goModule string

	byID       map[string]*graph.Node
	order      []*graph.Node
	edges      []graph.Edge
	files      []*fileRecord
	filesByRel map[string]*fileRecord
	dirs       map[string]struct{}

	scanned int
	skipped int
}

func (b *builder) addNode(n *graph.Node) *graph.Node {
	if existing, ok := b.byID[n.ID]; ok {
		return existing
	}
	b.byID[n.ID] = n
	b.order = append(b.order, n)
	return n
}

func (b *builder) addEdge(from, to string, kind graph.EdgeKind, weight float32) {
	if from == "" || to == "" || from == to {
		return
	}
	b.edges = append(b.edges, graph.Edge{From: from, To: to, Kind: kind, W: weight})
}

func (b *builder) result() Result {
	sort.Slice(b.edges, func(i, j int) bool {
		if b.edges[i].From != b.edges[j].From {
			return b.edges[i].From < b.edges[j].From
		}
		if b.edges[i].Kind != b.edges[j].Kind {
			return b.edges[i].Kind < b.edges[j].Kind
		}
		return b.edges[i].To < b.edges[j].To
	})
	return Result{
		Nodes:        b.order,
		Edges:        b.edges,
		FilesScanned: b.scanned,
		FilesSkipped: b.skipped,
	}
}

func (b *builder) scan() error {
	excluded := make(map[string]struct{}, len(b.cfg.Graph.Exclude))
	for _, e := range b.cfg.Graph.Exclude {
		excluded[e] = struct{}{}
	}
	extensions := make(map[string]struct{}, len(b.cfg.Graph.Extensions))
	for _, e := range b.cfg.Graph.Extensions {
		extensions[strings.ToLower(e)] = struct{}{}
	}
	maxBytes := int64(b.cfg.Graph.MaxFileKB) * 1024

	err := filepath.WalkDir(b.root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if p == b.root {
				return nil
			}
			if _, skip := excluded[entry.Name()]; skip || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if _, ok := extensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		// A generated bundle costs far more to index than it is ever worth.
		if maxBytes > 0 && info.Size() > maxBytes {
			b.skipped++
			return nil
		}

		rel, err := filepath.Rel(b.root, p)
		if err != nil {
			return nil
		}
		b.indexFile(filepath.ToSlash(rel), p)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk repository: %w", err)
	}
	return nil
}

func (b *builder) indexFile(rel, abs string) {
	data, err := os.ReadFile(abs)
	if err != nil {
		b.skipped++
		return
	}
	b.scanned++

	lang := languageOf(rel)
	hash := graph.HashBytes(data)
	loc := strings.Count(string(data), "\n") + 1

	fileNode := b.addNode(graph.FileNode(rel, "", loc, hash, lang))
	dir := path.Dir(rel)
	if dir == "." {
		dir = "(root)"
	}
	fileNode.SetMeta(graph.MetaPkg, dir)

	pkgNode := b.addNode(graph.PkgNode(dir))
	b.addEdge(pkgNode.ID, fileNode.ID, graph.EdgeContains, 1)
	b.dirs[dir] = struct{}{}

	rec := &fileRecord{
		rel:     rel,
		dir:     dir,
		lang:    lang,
		nodeID:  fileNode.ID,
		hash:    hash,
		imports: Imports(lang, data),
	}

	if b.cfg.Graph.Symbols && lang == "go" {
		symbols, err := GoSymbols(rel, hash, data)
		if err == nil {
			for _, s := range symbols {
				b.addNode(s.Node)
				b.addEdge(fileNode.ID, s.Node.ID, graph.EdgeContains, 1)
			}
			rec.symbols = symbols
		}
	}

	b.files = append(b.files, rec)
	b.filesByRel[rel] = rec
}

// resolveImports turns raw import strings into edges. An import that resolves
// inside the repository becomes a structural edge; anything else becomes an
// external node, so a dependency is visible without pretending it has a body.
func (b *builder) resolveImports() {
	for _, f := range b.files {
		seen := make(map[string]struct{}, len(f.imports))
		for _, imp := range f.imports {
			target, kind := b.resolveImport(f, imp)
			if target == "" {
				continue
			}
			if _, dup := seen[target]; dup {
				continue
			}
			seen[target] = struct{}{}

			// A third-party package is worth showing but must not accumulate
			// rank: it is imported by many files and depends on none of them.
			weight := float32(1)
			if node, ok := b.byID[target]; ok && node.Kind == graph.KindExt {
				weight = 0.25
			}
			b.addEdge(f.nodeID, target, kind, weight)
		}
	}
}

func (b *builder) resolveImport(f *fileRecord, imp string) (string, graph.EdgeKind) {
	switch f.lang {
	case "go":
		if b.goModule != "" && strings.HasPrefix(imp, b.goModule) {
			dir := strings.TrimPrefix(strings.TrimPrefix(imp, b.goModule), "/")
			if dir == "" {
				dir = "(root)"
			}
			if _, ok := b.dirs[dir]; ok {
				return graph.NodeID(graph.KindPkg, dir), graph.EdgeImports
			}
		}
		// The standard library is deliberately not modelled. Every Go file
		// imports fmt or os, so including them would make those the highest
		// PageRank nodes in the repository while carrying no signal about it.
		if isGoStdlib(imp) {
			return "", graph.EdgeImports
		}
	case "js", "ts", "python", "rust":
		if id := b.resolveRelative(f, imp); id != "" {
			return id, graph.EdgeImports
		}
	}

	if isLocalSpecifier(imp) {
		// A relative path that failed to resolve is a broken or generated
		// import. Recording it as an external dependency would be a lie.
		return "", graph.EdgeImports
	}
	return b.addNode(graph.ExtNode(imp)).ID, graph.EdgeImports
}

// isGoStdlib reports whether an import path is in the standard library. The
// canonical rule is that a stdlib path's first segment contains no dot, because
// every module path outside it starts with a hostname.
func isGoStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

// resolveRelative maps a module specifier onto a file already in the graph.
func (b *builder) resolveRelative(f *fileRecord, imp string) string {
	var base string
	switch {
	case strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../"):
		base = path.Clean(path.Join(f.dirPath(), imp))
	case f.lang == "python" && strings.HasPrefix(imp, "."):
		base = path.Clean(path.Join(f.dirPath(), strings.ReplaceAll(strings.TrimLeft(imp, "."), ".", "/")))
	case f.lang == "python":
		base = strings.ReplaceAll(imp, ".", "/")
	case f.lang == "rust":
		trimmed := strings.TrimPrefix(imp, "crate::")
		if trimmed == imp {
			return ""
		}
		base = strings.ReplaceAll(trimmed, "::", "/")
	default:
		return ""
	}

	candidates := []string{base}
	for _, ext := range candidateExtensions(f.lang) {
		candidates = append(candidates, base+ext, path.Join(base, "index"+ext), path.Join(base, "mod"+ext), path.Join(base, "__init__"+ext))
	}
	for _, c := range candidates {
		if rec, ok := b.filesByRel[c]; ok && rec.rel != f.rel {
			return rec.nodeID
		}
	}
	return ""
}

func (f *fileRecord) dirPath() string {
	if f.dir == "(root)" {
		return "."
	}
	return f.dir
}

func candidateExtensions(lang string) []string {
	switch lang {
	case "js", "ts":
		return []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}
	case "python":
		return []string{".py"}
	case "rust":
		return []string{".rs"}
	default:
		return nil
	}
}

func isLocalSpecifier(imp string) bool {
	return strings.HasPrefix(imp, ".") || strings.HasPrefix(imp, "/") ||
		strings.HasPrefix(imp, "crate::") || strings.HasPrefix(imp, "self::") ||
		strings.HasPrefix(imp, "super::")
}

// resolveCalls links call sites to symbol definitions, preferring a definition
// in the same file and then in the same package. Unresolvable names are dropped
// rather than guessed: a wrong edge is a distractor, and a distractor is worse
// than a missing edge.
func (b *builder) resolveCalls() {
	byFile := make(map[string]map[string]string)
	byDir := make(map[string]map[string]string)
	for _, f := range b.files {
		for _, s := range f.symbols {
			if byFile[f.rel] == nil {
				byFile[f.rel] = make(map[string]string)
			}
			if byDir[f.dir] == nil {
				byDir[f.dir] = make(map[string]string)
			}
			byFile[f.rel][s.Node.Name] = s.Node.ID
			if _, exists := byDir[f.dir][s.Node.Name]; !exists {
				byDir[f.dir][s.Node.Name] = s.Node.ID
			}
		}
	}

	for _, f := range b.files {
		for _, s := range f.symbols {
			seen := make(map[string]struct{}, len(s.Calls))
			for _, name := range s.Calls {
				target, ok := byFile[f.rel][name]
				if !ok {
					target, ok = byDir[f.dir][name]
				}
				if !ok || target == s.Node.ID {
					continue
				}
				if _, dup := seen[target]; dup {
					continue
				}
				seen[target] = struct{}{}
				b.addEdge(s.Node.ID, target, graph.EdgeCalls, 1)
			}
		}
	}
}

func languageOf(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "ts"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "js"
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".cs":
		return "csharp"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".kt":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".c", ".h", ".cc", ".cpp", ".hpp":
		return "c"
	default:
		return "text"
	}
}

// goModulePath reads the module line from go.mod so internal imports can be
// told apart from third-party ones.
func goModulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
