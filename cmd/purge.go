// purge.go — memor purge
//
// Deletes the entire .memor/ directory. With --all, also removes skill files
// from AI tool directories and cleans up empty parent directories.
//
// Flags:
//
//	--all   Also remove skill files from AI tool directories
//
// Examples:
//
//	memor purge
//	memor purge --all
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var purgeAll bool

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Remove all memor files from the current project",
	Long:  "Deletes .memor/ directory and optionally removes injected instructions from AI tool config files.",
	RunE:  runPurge,
}

func init() {
	purgeCmd.Flags().BoolVar(&purgeAll, "all", false, "Also remove injected instructions, auto-approve settings, and MCP registration from AI tool config files")
}

func runPurge(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		fmt.Println("Nothing to purge — .memor/ does not exist.")
		return nil
	}

	// Remove .memor/ directory
	if err := os.RemoveAll(paths.Root); err != nil {
		return fmt.Errorf("remove .memor/: %w", err)
	}
	fmt.Println("Removed .memor/")

	if purgeAll {
		// Clear injected instruction blocks without deleting user-owned instruction files.
		for _, inf := range getToolInstructionFiles() {
			fullPath := filepath.Join(cwd, inf.path)
			cleared, err := clearMemorInstructionsFile(fullPath, inf.content)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not clear %s: %v\n", inf.path, err)
				continue
			}
			if cleared {
				fmt.Printf("Cleared memor instructions from %s\n", inf.path)
			}
		}

		// Remove memor auto-approve entries from tool settings files.
		removeAutoApproveSettings(cwd)

		// Remove the memor MCP server registration from tool configs.
		deregisterMCPServers(cwd)
	}

	fmt.Println("Purge complete.")
	return nil
}
