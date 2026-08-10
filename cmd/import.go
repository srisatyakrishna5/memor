// import.go — memor import
//
// Imports memories from a JSONL file into the WAL. Supports dedup checking
// and dry-run mode.
//
// Flags:
//
//	--tag              Add an extra tag to all imported entries
//	--skip-duplicates  Skip entries whose content hash already exists
//	--dry-run          Show what would be imported without writing
//
// Examples:
//
//	memor import backup.jsonl
//	memor import decisions.jsonl --skip-duplicates
//	memor import shared.jsonl --tag "imported"
//	memor import backup.jsonl --dry-run
package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var (
	importTag      string
	importSkipDups bool
	importDryRun   bool
)

var importCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import memories from JSONL",
	Long:  "Import memory entries from a JSONL file into the WAL.",
	Args:  cobra.ExactArgs(1),
	RunE:  runImport,
}

func init() {
	importCmd.Flags().StringVar(&importTag, "tag", "", "Add an extra tag to all imported entries")
	importCmd.Flags().BoolVar(&importSkipDups, "skip-duplicates", false, "Skip entries whose content hash already exists")
	importCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Show what would be imported without writing")
}

func runImport(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	inputPath := args[0]

	// Read input JSONL
	entries, err := readJSONLFile(inputPath)
	if err != nil {
		return fmt.Errorf("read input file: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No entries found in input file.")
		return nil
	}

	// Build existing ID set for dedup
	var existingIDs map[string]struct{}
	if importSkipDups {
		existingIDs, err = collectExistingIDs(paths)
		if err != nil {
			return fmt.Errorf("collect existing IDs: %w", err)
		}
	}

	toImport, skipped := prepareImportEntries(entries, existingIDs, importTag)

	if len(toImport) == 0 {
		fmt.Printf("No new entries to import (%d skipped as duplicates).\n", skipped)
		return nil
	}

	// Dry run — just show what would be imported
	if importDryRun {
		fmt.Printf("Dry run — would import %d entries (%d skipped):\n\n", len(toImport), skipped)
		for _, e := range toImport {
			fmt.Printf("  %s [%s] %s: %s\n", e.Type.Prefix(), e.ID[:8], renderTagList(e.Tags), e.Content)
		}
		return nil
	}

	// Write to WAL
	for _, e := range toImport {
		if err := store.AppendToWAL(paths.MemoryWAL, e); err != nil {
			return fmt.Errorf("write WAL: %w", err)
		}
	}

	fmt.Printf("Imported %d entries (%d skipped)\n", len(toImport), skipped)

	maybeAutoCompact(paths)

	return nil
}

func prepareImportEntries(entries []memory.Entry, existingIDs map[string]struct{}, importTag string) ([]memory.Entry, int) {
	tag := strings.TrimSpace(strings.ToLower(importTag))
	toImport := make([]memory.Entry, 0, len(entries))
	skipped := 0
	for _, entry := range entries {
		if entry.ID == "" {
			entry.ID = memory.ContentID(entry.Content)
		}
		if _, exists := existingIDs[entry.ID]; exists {
			skipped++
			continue
		}
		if tag != "" {
			entry.Tags = append(entry.Tags, tag)
		}
		toImport = append(toImport, entry)
	}
	return toImport, skipped
}

// readJSONLFile reads memory entries from a JSONL file.
func readJSONLFile(path string) ([]memory.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []memory.Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry memory.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping malformed line %d: %v\n", lineNum, err)
			continue
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// collectExistingIDs returns a set of all content IDs from snapshot + WAL.
func collectExistingIDs(paths store.Paths) (map[string]struct{}, error) {
	ids := make(map[string]struct{})

	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return nil, err
	}
	for _, e := range snap.Entries {
		ids[e.ID] = struct{}{}
	}

	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return nil, err
	}
	for _, e := range walEntries {
		if e.ID == "" {
			e.ID = memory.ContentID(e.Content)
		}
		ids[e.ID] = struct{}{}
	}

	return ids, nil
}
