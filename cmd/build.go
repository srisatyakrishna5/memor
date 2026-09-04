// build.go — memor build
//
// Indexes the repository: files, packages, imports, per-file purpose lines, Go
// symbols with exact spans, and knowledge sections. It also records the commit
// it indexed, which is what later answers "what changed since then?".
// Agent-authored memories and file summaries are preserved; only
// machine-derived nodes are replaced.
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

	fmt.Printf("Indexed %d files, %d symbols, %d docs in %s\n",
		report.Files, report.Symbols, report.Docs,
		report.Duration.Round(1e6))
	if report.Commit != "" {
		fmt.Printf("At commit %s\n", shortSHA(report.Commit))
	}
	if report.Skipped > 0 {
		fmt.Printf("Skipped %d files above the size limit\n", report.Skipped)
	}
	fmt.Printf("Wrote %s\n", sess.Paths.DB)
	return nil
}
