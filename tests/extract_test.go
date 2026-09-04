package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/graph/extract"
)

// buildGraph folds an extraction result into a graph.
func buildGraph(t *testing.T, result extract.Result) *graph.Graph {
	t.Helper()
	g := graph.New()
	for _, n := range result.Nodes {
		g.AddNode(n)
	}
	return g
}

// contains reports whether a metadata list holds a value.
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// extractConfig disables knowledge indexing so extraction tests see only code.
func extractConfig() config.Config {
	cfg := config.Default()
	cfg.Knowledge.Enabled = false
	return cfg
}

func TestRepoExtractsStructure(t *testing.T) {
	root := sampleRepo(t)

	result, err := extract.Repo(root, extractConfig())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	g := buildGraph(t, result)

	if _, ok := g.FindFile("main.go"); !ok {
		t.Error("expected main.go to be indexed")
	}
	if _, ok := g.FindFile("internal/store/store.go"); !ok {
		t.Error("expected internal/store/store.go to be indexed")
	}

	for _, name := range []string{"Load", "Loader", "normalize", "MaxRecords"} {
		if len(g.FindSymbols(name)) == 0 {
			t.Errorf("expected symbol %q to be extracted", name)
		}
	}
}

// The standard library is universally imported, so recording it would bury the
// handful of imports that actually say something about a file.
func TestStdlibIsNotRecorded(t *testing.T) {
	root := sampleRepo(t)

	result, err := extract.Repo(root, extractConfig())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	g := buildGraph(t, result)

	main, ok := g.FindFile("main.go")
	if !ok {
		t.Fatal("expected main.go")
	}
	if contains(main.MetaList(graph.MetaImports), "fmt") {
		t.Error("expected the standard library to be excluded")
	}
}

func TestInternalImportResolvesToPackage(t *testing.T) {
	root := sampleRepo(t)

	result, err := extract.Repo(root, extractConfig())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	g := buildGraph(t, result)

	main, ok := g.FindFile("main.go")
	if !ok {
		t.Fatal("expected main.go")
	}

	imports := main.MetaList(graph.MetaImports)
	if !contains(imports, "internal/store") {
		t.Errorf("expected the internal package to be recorded, got %v", imports)
	}
	if !contains(imports, "github.com/spf13/cobra") {
		t.Errorf("expected the external module to be recorded, got %v", imports)
	}

	// The reverse direction is what answers "what breaks if I change this?".
	pkg, ok := g.Node(graph.NodeID(graph.KindPkg, "internal/store"))
	if !ok {
		t.Fatal("expected the internal package node")
	}
	if !contains(pkg.MetaList(graph.MetaDependents), "main.go") {
		t.Errorf("expected main.go to be recorded as a dependent, got %v", pkg.MetaList(graph.MetaDependents))
	}
}

func TestGoSymbolSpansArePrecise(t *testing.T) {
	root := sampleRepo(t)
	path := filepath.Join(root, "internal", "store", "store.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := extract.GoSymbols("internal/store/store.go", graph.HashBytes(data), data)
	if err != nil {
		t.Fatalf("GoSymbols: %v", err)
	}

	var load *graph.Node
	for _, s := range symbols {
		if s.Node.Name == "Load" {
			load = s.Node
		}
	}
	if load == nil {
		t.Fatal("expected Load to be extracted")
	}
	if load.Span == nil || load.Span.Start >= load.Span.End {
		t.Fatalf("expected a valid span, got %+v", load.Span)
	}

	body := string(data[load.Span.Start:load.Span.End])
	if body[:9] != "func Load" {
		t.Errorf("span does not start at the declaration: %q", body[:20])
	}
	// The signature must carry the declaration without the body, so the map
	// costs a pointer rather than the function.
	if load.Text != "func Load() string" {
		t.Errorf("unexpected signature %q", load.Text)
	}
}

func TestCallsResolveWithinPackage(t *testing.T) {
	root := sampleRepo(t)

	result, err := extract.Repo(root, extractConfig())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	g := buildGraph(t, result)

	load := g.FindSymbols("Load")
	if len(load) == 0 {
		t.Fatal("expected Load")
	}

	calls := load[0].MetaList(graph.MetaCalls)
	if !contains(calls, "internal/store/store.go#normalize") {
		t.Errorf("expected Load to record a call to normalize, got %v", calls)
	}

	normalize := g.FindSymbols("normalize")
	if len(normalize) == 0 {
		t.Fatal("expected normalize")
	}
	if !contains(normalize[0].MetaList(graph.MetaCallers), "internal/store/store.go#Load") {
		t.Error("expected normalize to record Load as a caller")
	}
}

func TestImportsPerLanguage(t *testing.T) {
	cases := []struct {
		lang   string
		source string
		want   string
	}{
		{"go", "package a\n\nimport (\n\t\"fmt\"\n\talias \"example.com/x/y\"\n)\n", "example.com/x/y"},
		{"ts", "import { a } from './helper';\nimport 'side-effect';\n", "./helper"},
		{"js", "const x = require('lodash');\n", "lodash"},
		{"python", "from pkg.mod import thing\n", "pkg.mod"},
		{"java", "import java.util.List;\n", "java.util.List"},
		{"csharp", "using System.Text;\n", "System.Text"},
		{"rust", "use crate::store::Loader;\n", "crate::store::Loader"},
	}

	for _, tc := range cases {
		got := extract.Imports(tc.lang, []byte(tc.source))
		found := false
		for _, imp := range got {
			if imp == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected %q in %v", tc.lang, tc.want, got)
		}
	}
}

// A file above the size limit costs far more to index than a generated bundle
// is ever worth.
func TestOversizedFilesAreSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/demo\n")
	writeFile(t, root, "huge.go", "package main\n"+string(make([]byte, 4096)))

	cfg := extractConfig()
	cfg.Graph.MaxFileKB = 1

	result, err := extract.Repo(root, cfg)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if result.FilesSkipped != 1 {
		t.Errorf("expected 1 skipped file, got %d", result.FilesSkipped)
	}
}

func TestGraphDisabledExtractsNothing(t *testing.T) {
	root := sampleRepo(t)
	cfg := config.Default()
	cfg.Graph.Enabled = false

	result, err := extract.Repo(root, cfg)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if len(result.Nodes) != 0 {
		t.Error("expected the kill switch to disable extraction entirely")
	}
}
