// clean.go — memor clean
//
// Removes memor's footprint. By default it drops only the derived artifacts,
// which are always safe to delete because they regenerate. --all removes the
// whole .memor/ directory and deregisters the MCP server.
package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var (
	cleanAll   bool
	cleanForce bool
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove derived artifacts, or the entire memor footprint",
	Args:  cobra.NoArgs,
	RunE:  runClean,
}

func init() {
	cleanCmd.Flags().BoolVar(&cleanAll, "all", false, "Remove .memor/ entirely and deregister the MCP server")
	cleanCmd.Flags().BoolVarP(&cleanForce, "force", "y", false, "Skip the confirmation prompt")
}

func runClean(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := store.FindProjectRoot(cwd)
	paths := store.ResolvePaths(root)
	if !paths.Exists() {
		return fmt.Errorf("memor is not initialized for %s", cwd)
	}

	if !cleanAll {
		// graph.db is a derived projection and the blob directory is a cache.
		// Dropping them costs one rebuild and nothing else.
		if err := os.Remove(paths.DB); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.RemoveAll(paths.Blobs); err != nil {
			return err
		}
		fmt.Println("Removed derived artifacts. They regenerate on the next command.")
		return nil
	}

	if !cleanForce && !confirm(fmt.Sprintf("Delete %s and every memory it holds?", paths.Root)) {
		fmt.Println("Cancelled.")
		return nil
	}

	if err := os.RemoveAll(paths.Root); err != nil {
		return err
	}
	fmt.Printf("Removed %s\n", paths.Root)

	deregisterMCPServer(root)
	removeGitignoreEntry(root)
	return nil
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

func removeGitignoreEntry(root string) {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var kept []string
	removed := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == store.DirName+"/" {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err == nil {
		fmt.Println("Removed .memor/ from .gitignore")
	}
}
