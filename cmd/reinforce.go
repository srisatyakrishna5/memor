// reinforce.go — memor reinforce
//
// Bumps a memory's relevance by refreshing its timestamp in the WAL.
// Useful when the AI or developer references a memory and wants to keep it from
// being archived during compaction.
//
// Args: memory ID (required)
//
// Examples:
//
//	memor reinforce 0a3f9c2b1e7d
//	memor reinforce b4e1a7c3d9f2
package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var reinforceCmd = &cobra.Command{
	Use:   "reinforce [id]",
	Short: "Bump the relevance of a useful memory",
	Args:  cobra.ExactArgs(1),
	RunE:  runReinforce,
}

func runReinforce(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	id := args[0]
	if err := reinforceMemory(paths, id, time.Now().Unix()); err != nil {
		return err
	}

	fmt.Printf("Reinforced memory %s\n", id)
	return nil
}

func reinforceMemory(paths store.Paths, id string, timestamp int64) error {
	snapshot, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return fmt.Errorf("read WAL: %w", err)
	}

	entries := append(snapshot.Entries, walEntries...)
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].ID != id {
			continue
		}
		entry := entries[i]
		entry.Timestamp = timestamp
		if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
			return fmt.Errorf("write reinforced memory: %w", err)
		}
		return nil
	}

	return fmt.Errorf("memory %s not found", id)
}
