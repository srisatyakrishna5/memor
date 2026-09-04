// Package extract builds graph nodes from a repository on disk.
//
// Extraction is deterministic and tiered. L0 recovers files, packages, imports
// and a one-line purpose with a line-oriented scan that needs no dependencies.
// L1 recovers symbols, spans, and calls using the standard library's own
// parser. Neither tier makes a network call, invokes a model, or requires CGO
// — which is what keeps `memor build` a bounded local command and keeps the npm
// cross-compilation matrix intact.
//
// Relations are written onto the nodes they belong to rather than emitted as
// traversable edges, so retrieval can report them but never walk them.
package extract

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/graph"
)

// Result is the full set of machine-derived nodes for a repository.
type Result struct {
	Nodes []*graph.Node

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

func (b *builder) result() Result {
	return Result{
		Nodes:        b.order,
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
	fileNode.SetMeta(graph.MetaPurpose, Purpose(lang, data))

	b.addNode(graph.PkgNode(dir))
	b.dirs[dir] = struct{}{}

	rec := &fileRecord{
		rel:     rel,
		dir:     dir,
		lang:    lang,
		nodeID:  fileNode.ID,
		hash:    hash,
		imports: Imports(lang, data),
	}

	if b.cfg.Graph.Symbols && SupportsSymbols(lang) {
		symbols, err := Symbols(lang, rel, hash, data)
		if err == nil {
			names := make([]string, 0, len(symbols))
			for _, s := range symbols {
				b.addNode(s.Node)
				names = append(names, s.Node.Name)
			}
			fileNode.SetMetaList(graph.MetaSymbols, names)
			rec.symbols = symbols
		}
	}

	b.files = append(b.files, rec)
	b.filesByRel[rel] = rec
}

// resolveImports records what each file imports and, in reverse, what imports
// it. The reverse direction is the one an agent actually asks for — "what
// breaks if I change this?" — so it is precomputed rather than derived.
func (b *builder) resolveImports() {
	dependents := make(map[string][]string)

	for _, f := range b.files {
		targets := make([]string, 0, len(f.imports))
		for _, imp := range f.imports {
			target := b.resolveImport(f, imp)
			if target == "" {
				continue
			}
			targets = append(targets, target)
			dependents[target] = append(dependents[target], f.rel)
		}
		if node, ok := b.byID[f.nodeID]; ok {
			node.SetMetaList(graph.MetaImports, targets)
		}
	}

	for target, importers := range dependents {
		for _, id := range []string{
			graph.NodeID(graph.KindFile, target),
			graph.NodeID(graph.KindPkg, target),
		} {
			if node, ok := b.byID[id]; ok {
				node.AppendMetaList(graph.MetaDependents, importers)
				break
			}
		}
	}
}

// resolveImport maps a raw import specifier onto what it refers to: a repo path,
// a repo package directory, or the external specifier itself. An import that
// cannot be placed returns "" rather than a guess.
func (b *builder) resolveImport(f *fileRecord, imp string) string {
	switch f.lang {
	case "go":
		if b.goModule != "" && strings.HasPrefix(imp, b.goModule) {
			dir := strings.TrimPrefix(strings.TrimPrefix(imp, b.goModule), "/")
			if dir == "" {
				dir = "(root)"
			}
			if _, ok := b.dirs[dir]; ok {
				return dir
			}
		}
		// The standard library is deliberately not recorded. Every Go file
		// imports fmt or os, so listing them would bury the handful of imports
		// that actually say something about the file.
		if isGoStdlib(imp) {
			return ""
		}
	case "js", "ts", "python", "rust":
		if rel := b.resolveRelative(f, imp); rel != "" {
			return rel
		}
	}

	if isLocalSpecifier(imp) {
		// A relative path that failed to resolve is a broken or generated
		// import. Recording it as an external dependency would be a lie.
		return ""
	}
	return imp
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
			return rec.rel
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
// rather than guessed: a wrong relation is a distractor, and a distractor is
// worse than a missing one. Targets are qualified as path#name so two
// same-named symbols in different files stay distinguishable.
func (b *builder) resolveCalls() {
	byFile := make(map[string]map[string]*graph.Node)
	byDir := make(map[string]map[string]*graph.Node)
	for _, f := range b.files {
		for _, s := range f.symbols {
			if byFile[f.rel] == nil {
				byFile[f.rel] = make(map[string]*graph.Node)
			}
			if byDir[f.dir] == nil {
				byDir[f.dir] = make(map[string]*graph.Node)
			}
			byFile[f.rel][s.Node.Name] = s.Node
			if _, exists := byDir[f.dir][s.Node.Name]; !exists {
				byDir[f.dir][s.Node.Name] = s.Node
			}
		}
	}

	callers := make(map[string][]string)
	for _, f := range b.files {
		for _, s := range f.symbols {
			var calls []string
			for _, name := range s.Calls {
				target, ok := byFile[f.rel][name]
				if !ok {
					target, ok = byDir[f.dir][name]
				}
				if !ok || target.ID == s.Node.ID {
					continue
				}
				calls = append(calls, QualifySymbol(target))
				callers[target.ID] = append(callers[target.ID], QualifySymbol(s.Node))
			}
			s.Node.SetMetaList(graph.MetaCalls, calls)
		}
	}
	for id, names := range callers {
		if node, ok := b.byID[id]; ok {
			node.SetMetaList(graph.MetaCallers, names)
		}
	}
}

// QualifySymbol renders a symbol node as path#name.
func QualifySymbol(n *graph.Node) string {
	if p := graph.SpanPath(n); p != "" {
		return p + "#" + n.Name
	}
	return n.Name
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
