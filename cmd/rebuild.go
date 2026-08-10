package cmd

import (
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/engine"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var rebuildCmd = &cobra.Command{
	Use:        "rebuild",
	Short:      "Compact memory after storage changes",
	Deprecated: "persistent indexes were removed; use 'memor compact'",
	RunE:       runRebuild,
}

func runRebuild(cmd *cobra.Command, args []string) error {
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
	written, archived, err := engine.Compact(paths, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Rebuild complete: %d entries in snapshot, %d archived\n", written, archived)
	return nil
}
