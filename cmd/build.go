// build.go — memor build
//
// Extracts the repository into the graph: files, packages, imports, Go symbols
// with exact spans, and knowledge sections. Agent-authored memories and file
// summaries are preserved; only machine-derived nodes are replaced.
//
// Examples:
//
//	memor build
//	memor build --quiet
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var buildQuiet bool

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Index the repository into the knowledge graph",
	Long: `Walk the project, extract structure, and rewrite the graph.

Every machine-derived node is replaced so deleted files and renamed symbols
cannot linger as ghosts. Memories you recorded and summaries an agent wrote are
left untouched.`,
	Args: cobra.NoArgs,
	RunE: runBuild,
}

func init() {
	buildCmd.Flags().BoolVarP(&buildQuiet, "quiet", "q", false, "Print nothing on success")
}

func runBuild(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}

	report, err := sess.Build()
	if err != nil {
		return err
	}
	if buildQuiet {
		return nil
	}

	fmt.Printf("Indexed %d files, %d symbols, %d docs, %d edges in %s\n",
		report.Files, report.Symbols, report.Docs, report.Edges,
		report.Duration.Round(1e6))
	if report.Skipped > 0 {
		fmt.Printf("Skipped %d files above the size limit\n", report.Skipped)
	}
	fmt.Printf("Wrote %s\n", sess.Paths.DB)
	return nil
}
