// stats.go — memor stats
//
// Shows entry counts, token usage, and file sizes. Useful for
// monitoring memory growth and checking if compaction is needed.
//
// Examples:
//
//	memor stats
package cmd

import (
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show entry counts, token usage, and file sizes",
	RunE:  runStats,
}

func runStats(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	// Snapshot stats
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	// WAL stats
	walCount, err := store.WALEntryCount(paths.MemoryWAL)
	if err != nil {
		return fmt.Errorf("read WAL: %w", err)
	}

	// File sizes
	dbSize := fileSize(paths.MemoryDB)
	walSize := fileSize(paths.MemoryWAL)
	archiveSize := fileSize(paths.Archive)
	knowledgeSize := fileSize(paths.Knowledge)

	fmt.Println("Memor Stats")
	fmt.Println("───────────────────────────────────")
	fmt.Printf("Snapshot entries:  %d\n", len(snap.Entries))
	fmt.Printf("WAL entries:       %d\n", walCount)
	fmt.Printf("Token budget:      %d\n", snap.TokenBudget)
	fmt.Println()
	fmt.Println("File Sizes")
	fmt.Println("───────────────────────────────────")
	fmt.Printf("memory.db:         %s\n", humanSize(dbSize))
	fmt.Printf("memory.wal:        %s\n", humanSize(walSize))
	fmt.Printf("memory.archive:    %s\n", humanSize(archiveSize))
	fmt.Printf("knowledge.db:      %s\n", humanSize(knowledgeSize))

	// Type breakdown
	typeCounts := map[string]int{}
	for _, e := range snap.Entries {
		typeCounts[e.Type.FullName()]++
	}
	if len(typeCounts) > 0 {
		fmt.Println()
		fmt.Println("Type Breakdown")
		fmt.Println("───────────────────────────────────")
		for t, c := range typeCounts {
			fmt.Printf("%-15s %d\n", t, c)
		}
	}

	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func humanSize(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB"}
	size := float64(bytes)
	i := 0
	for size >= 1024 && i < len(units)-1 {
		size /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%.1f %s", size, units[i])
}
