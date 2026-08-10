// code.go — memor code save|load|list
//
// Manages code file summaries within the memory system. Agents save structured
// summaries after reading or writing source files, enabling future agents to
// understand code without re-reading files.
//
// Subcommands:
//
//	save   Save a code file summary (exports, deps, summary, patterns)
//	load   Load summaries by file path or query
//	list   List all mapped files
//
// Examples:
//
//	memor code save src/lib/auth.ts --exports "refreshToken(), validate()" --summary "Auth middleware"
//	memor code load src/lib/auth.ts
//	memor code load --query "auth"
//	memor code list
package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var (
	codeExports  string
	codeDeps     string
	codeSummary  string
	codePatterns string
	codeLogic    string
	codeQuery    string
)

var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Manage code file summaries",
	Long:  "Save, load, and list structured code file summaries for AI agents.",
}

var codeSaveCmd = &cobra.Command{
	Use:   "save <file-path>",
	Short: "Save a code file summary",
	Long:  "Save a structured summary of a source file (exports, deps, summary, patterns).",
	Args:  cobra.ExactArgs(1),
	RunE:  runCodeSave,
}

var codeLoadCmd = &cobra.Command{
	Use:   "load [file-path]",
	Short: "Load code file summaries",
	Long:  "Load a specific file summary or search by query.",
	RunE:  runCodeLoad,
}

var codeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all mapped files",
	Long:  "Show all files with saved code summaries.",
	RunE:  runCodeList,
}

func init() {
	codeSaveCmd.Flags().StringVar(&codeExports, "exports", "", "Comma-separated exported symbols (functions, types, classes)")
	codeSaveCmd.Flags().StringVar(&codeDeps, "deps", "", "Comma-separated dependency file paths")
	codeSaveCmd.Flags().StringVar(&codeSummary, "summary", "", "One-line summary of what the file does")
	codeSaveCmd.Flags().StringVar(&codePatterns, "patterns", "", "Usage patterns and conventions")
	codeSaveCmd.Flags().StringVar(&codeLogic, "logic", "", "Step-by-step logic flow for complex files")

	codeLoadCmd.Flags().StringVar(&codeQuery, "query", "", "Search code summaries by keyword")

	codeCmd.AddCommand(codeSaveCmd)
	codeCmd.AddCommand(codeLoadCmd)
	codeCmd.AddCommand(codeListCmd)
}

func runCodeSave(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	filePath := args[0]

	// Compute file hash and LOC
	hash, loc, err := fileHashAndLOC(filepath.Join(cwd, filePath))
	if err != nil {
		// File might not exist locally (agent describing a remote or planned file)
		hash = "000000"
		loc = 0
	}

	// Parse exports and deps
	var exports []string
	if codeExports != "" {
		for _, e := range strings.Split(codeExports, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				exports = append(exports, e)
			}
		}
	}

	var deps []string
	if codeDeps != "" {
		for _, d := range strings.Split(codeDeps, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				deps = append(deps, d)
			}
		}
	}

	if codeSummary == "" {
		return fmt.Errorf("--summary is required")
	}

	meta := &memory.CodeMeta{
		FilePath: filePath,
		LOC:      loc,
		Hash:     hash,
		Exports:  exports,
		Deps:     deps,
		Summary:  codeSummary,
		Patterns: codePatterns,
		Logic:    codeLogic,
	}

	// Build the entry — use file path as content for dedup
	entry := memory.Entry{
		Timestamp: time.Now().Unix(),
		Type:      memory.TypeCode,
		Content:   filePath,
		ID:        memory.ContentID(filePath),
		Meta:      meta,
	}

	// Extract tags from file path (directory names)
	pathParts := strings.Split(filepath.ToSlash(filePath), "/")
	if len(pathParts) > 1 {
		// Use parent directory as a tag
		entry.Tags = []string{pathParts[len(pathParts)-2]}
	}

	if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
		return fmt.Errorf("write WAL: %w", err)
	}

	fmt.Printf("Saved code summary [%s]: %s [%d LOC | %s]\n", entry.ID[:8], filePath, loc, hash)

	maybeAutoCompact(paths)

	return nil
}

func runCodeLoad(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	// Collect all code entries from snapshot + WAL
	entries, err := allCodeEntries(paths)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Println("No code summaries found.")
		return nil
	}

	// Filter by file path or query
	var filtered []memory.Entry
	if len(args) > 0 {
		// Exact file path match
		target := args[0]
		for _, e := range entries {
			if e.Meta != nil && e.Meta.FilePath == target {
				filtered = append(filtered, e)
			}
		}
	} else if codeQuery != "" {
		// Search by keyword
		q := strings.ToLower(codeQuery)
		for _, e := range entries {
			if e.Meta == nil {
				continue
			}
			searchable := strings.ToLower(e.Meta.FilePath + " " + e.Meta.Summary + " " + strings.Join(e.Meta.Exports, " "))
			if strings.Contains(searchable, q) {
				filtered = append(filtered, e)
			}
		}
	} else {
		filtered = entries
	}

	if len(filtered) == 0 {
		fmt.Println("No matching code summaries found.")
		return nil
	}

	// Check freshness and render
	for _, e := range filtered {
		if e.Meta == nil {
			continue
		}

		// Check if file has changed
		status := "fresh"
		currentHash, _, err := fileHashAndLOC(filepath.Join(cwd, e.Meta.FilePath))
		if err != nil {
			status = "missing"
		} else if currentHash != e.Meta.Hash {
			status = "stale"
		}

		fmt.Printf("@c %s [%d LOC | %s] (%s)\n", e.Meta.FilePath, e.Meta.LOC, e.Meta.Hash, status)
		if len(e.Meta.Exports) > 0 {
			fmt.Printf("  exports: %s\n", strings.Join(e.Meta.Exports, ", "))
		}
		if len(e.Meta.Deps) > 0 {
			fmt.Printf("  deps: %s\n", strings.Join(e.Meta.Deps, ", "))
		}
		if e.Meta.Summary != "" {
			fmt.Printf("  summary: %s\n", e.Meta.Summary)
		}
		if e.Meta.Patterns != "" {
			fmt.Printf("  patterns: %s\n", e.Meta.Patterns)
		}
		if e.Meta.Logic != "" {
			fmt.Printf("  logic: %s\n", e.Meta.Logic)
		}
		fmt.Println()
	}

	return nil
}

func runCodeList(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	entries, err := allCodeEntries(paths)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Println("No code summaries found.")
		return nil
	}

	fmt.Printf("%d file(s) mapped:\n\n", len(entries))
	for _, e := range entries {
		if e.Meta == nil {
			continue
		}
		fmt.Printf("  %-45s [%4d LOC | %s] %s\n", e.Meta.FilePath, e.Meta.LOC, e.Meta.Hash, e.Meta.Summary)
	}

	return nil
}

// allCodeEntries returns all @c entries from snapshot + WAL, deduped by file path.
func allCodeEntries(paths store.Paths) ([]memory.Entry, error) {
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return nil, err
	}

	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return nil, err
	}

	// Dedup by file path — WAL entries (newer) win
	byPath := make(map[string]memory.Entry)
	for _, e := range snap.Entries {
		if e.Type == memory.TypeCode && e.Meta != nil {
			byPath[e.Meta.FilePath] = e
		}
	}
	for _, e := range walEntries {
		if e.Type == memory.TypeCode && e.Meta != nil {
			byPath[e.Meta.FilePath] = e
		}
	}

	var result []memory.Entry
	for _, e := range byPath {
		result = append(result, e)
	}
	return result, nil
}

// fileHashAndLOC computes SHA-256[:6] and line count for a file.
func fileHashAndLOC(path string) (string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}

	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])[:constants.FileHashLength]

	loc := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		loc++
	}

	return hashStr, loc, nil
}
