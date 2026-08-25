// clean.go — memor clean
//
// Resets all memory data — clears memory.db, memory.wal, memory.archive,
// and knowledge.db. Preserves the .memor/ directory structure
// and config.toml. Use when you want a fresh start without re-running init.
//
// Examples:
//
//	memor clean
package cmd

import (
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Reset all memory data while keeping .memor/ and config",
	Long:  "Clears memory.db, memory.wal, memory.archive, and knowledge.db. Preserves the .memor/ directory and config.toml.",
	RunE:  runClean,
}

func runClean(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — nothing to clean")
	}

	// clean rewrites the same files compaction owns, so take the state lock to
	// keep a concurrent add or MCP session from half-surviving the reset.
	lock, err := store.AcquireLock(paths.Lock, store.CompactLockTimeout)
	if err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer lock.Release()

	// Reset memory.db to empty snapshot
	if err := os.WriteFile(paths.MemoryDB, []byte(fmt.Sprintf("@mem v1 | 0 entries | budget:%d | compacted:none\n", constants.DefaultTokenBudget)), 0o644); err != nil {
		return fmt.Errorf("reset memory.db: %w", err)
	}
	fmt.Println("Reset memory.db")
	if err := os.Remove(paths.Snapshot); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset canonical snapshot: %w", err)
	}

	// Truncate memory.wal
	if err := os.WriteFile(paths.MemoryWAL, nil, 0o644); err != nil {
		return fmt.Errorf("reset memory.wal: %w", err)
	}
	fmt.Println("Reset memory.wal")

	// Truncate archive
	if err := os.WriteFile(paths.Archive, nil, 0o644); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reset memory.archive: %w", err)
		}
	} else {
		fmt.Println("Reset memory.archive")
	}

	// Truncate knowledge.db
	if err := os.WriteFile(paths.Knowledge, nil, 0o644); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reset knowledge.db: %w", err)
		}
	} else {
		fmt.Println("Reset knowledge.db")
	}

	fmt.Println("Clean complete — .memor/ directory and config.toml preserved.")
	return nil
}
