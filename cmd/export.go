// export.go — memor export
//
// Exports memories as portable JSONL. Auto-compacts first so only memory.db
// is read — no WAL parsing, no duplicates.
//
// Flags:
//
//	--type    Filter by memory types (comma-separated: semantic,episodic,procedural,preference,code)
//	--tags    Filter by tags (comma-separated)
//	--since   Export only entries after this date (YYYY-MM-DD)
//	-o        Output file path (default: stdout)
//
// Examples:
//
//	memor export > backup.jsonl
//	memor export --type semantic,procedural -o decisions.jsonl
//	memor export --tags "auth,api" > auth.jsonl
//	memor export --since 2026-04-01 > recent.jsonl
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/engine"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var (
	exportType  string
	exportTags  string
	exportSince string
	exportOut   string
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export memories as JSONL",
	Long:  "Compact and export memories as portable JSONL. Filters by type, tags, or date.",
	RunE:  runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportType, "type", "", "Filter by types (comma-separated: semantic,episodic,procedural,preference,code)")
	exportCmd.Flags().StringVar(&exportTags, "tags", "", "Filter by tags (comma-separated)")
	exportCmd.Flags().StringVar(&exportSince, "since", "", "Export entries after this date (YYYY-MM-DD)")
	exportCmd.Flags().StringVarP(&exportOut, "output", "o", "", "Output file path (default: stdout)")
}

func runExport(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Auto-compact first — merge WAL into memory.db
	walCount, _ := store.WALEntryCount(paths.MemoryWAL)
	if walCount > 0 {
		_, _, err := engine.Compact(paths, cfg)
		if err != nil {
			return fmt.Errorf("compact before export: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Compacted before export")
	}

	// Read snapshot only (WAL is now empty)
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	if len(snap.Entries) == 0 {
		fmt.Fprintln(os.Stderr, "No entries to export.")
		return nil
	}

	// Build type filter
	typeFilter := make(map[memory.Type]struct{})
	if exportType != "" {
		for _, t := range strings.Split(exportType, ",") {
			parsed := memory.ParseType(strings.TrimSpace(t))
			if parsed == "" {
				return fmt.Errorf("invalid type %q — use semantic, episodic, procedural, preference, or code", t)
			}
			typeFilter[parsed] = struct{}{}
		}
	}

	// Build tag filter
	tagFilter := make(map[string]struct{})
	if exportTags != "" {
		for _, t := range strings.Split(exportTags, ",") {
			t = strings.TrimSpace(strings.ToLower(t))
			if t != "" {
				tagFilter[t] = struct{}{}
			}
		}
	}

	// Parse since date
	var sinceTime time.Time
	if exportSince != "" {
		sinceTime, err = time.Parse("2006-01-02", exportSince)
		if err != nil {
			return fmt.Errorf("invalid --since date %q — use YYYY-MM-DD", exportSince)
		}
	}

	filtered := filterExportEntries(snap.Entries, typeFilter, tagFilter, sinceTime)

	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "No entries match the filters.")
		return nil
	}

	// Write JSONL
	var out *os.File
	if exportOut != "" {
		out, err = os.Create(exportOut)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer out.Close()
	} else {
		out = os.Stdout
	}

	encoder := json.NewEncoder(out)
	for _, e := range filtered {
		if err := encoder.Encode(e); err != nil {
			return fmt.Errorf("encode entry: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "Exported %d entries\n", len(filtered))
	return nil
}

func filterExportEntries(entries []memory.Entry, types map[memory.Type]struct{}, tags map[string]struct{}, since time.Time) []memory.Entry {
	filtered := make([]memory.Entry, 0, len(entries))
	for _, entry := range entries {
		if len(types) > 0 {
			if _, ok := types[entry.Type]; !ok {
				continue
			}
		}
		if len(tags) > 0 && !hasAnyTag(entry.Tags, tags) {
			continue
		}
		if !since.IsZero() && entry.Timestamp < since.Unix() {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// hasAnyTag returns true if the entry has at least one tag in the filter set.
func hasAnyTag(entryTags []string, filter map[string]struct{}) bool {
	for _, t := range entryTags {
		if _, ok := filter[strings.ToLower(t)]; ok {
			return true
		}
	}
	return false
}
