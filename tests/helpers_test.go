// Package tests holds memor's test suite.
//
// The tests live outside the packages they exercise, so they reach the code
// only through its exported API. That costs white-box access to unexported
// helpers and means coverage must be attributed explicitly:
//
//	go test ./tests/ -coverpkg=./internal/...
package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/store"
)

// newProject creates an initialized but empty .memor/ directory.
func newProject(t *testing.T) (store.Paths, config.Config) {
	t.Helper()
	dir := t.TempDir()
	paths := store.ResolvePaths(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	return paths, config.Default()
}

// writeFile creates a file and any parent directories it needs.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sampleRepo writes a small two-package Go module: one file importing another
// plus a third-party module, and a package with a call between two functions.
func sampleRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	writeFile(t, root, "go.mod", "module example.com/demo\n\ngo 1.25\n")
	writeFile(t, root, "main.go", `package main

import (
	"fmt"

	"example.com/demo/internal/store"
	"github.com/spf13/cobra"
)

func main() {
	fmt.Println(store.Load())
	_ = cobra.Command{}
}
`)
	writeFile(t, root, "internal/store/store.go", `package store

// Loader reads records.
type Loader struct {
	Path string
}

// Load returns a greeting.
func Load() string {
	return normalize("hello")
}

func normalize(s string) string {
	return s
}

const MaxRecords = 64
`)
	return root
}
