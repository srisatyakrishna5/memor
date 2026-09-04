// context.go — memor context
//
// Prints the task-ranked repository map an agent should load before reading any
// file. This is the CLI equivalent of the repo_map MCP tool.
//
// Flags:
//
//	--query    What the user is trying to do; ranks the map for that task
//	--tag      Topic tag to boost; repeatable
//	--file     File already open in the editor; anchors ranking; repeatable
//	--budget   Maximum tokens to emit
//	--limit    Maximum nodes to emit
//
// Examples:
//
//	memor context
//	memor context --query "where is compaction handled"
//	memor context --tag auth --budget 4000
package cmd

import (
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/retrieve"
	"github.com/spf13/cobra"
)

var (
	contextQuery  string
	contextTags   []string
	contextFiles  []string
	contextBudget int
	contextLimit  int
	contextStats  bool
)

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Print the task-ranked repository map",
	Args:  cobra.NoArgs,
	RunE:  runContext,
}

func init() {
	contextCmd.Flags().StringVarP(&contextQuery, "query", "q", "", "What you are trying to do; ranks the map for that task")
	contextCmd.Flags().StringSliceVarP(&contextTags, "tag", "t", nil, "Topic tag to boost (repeatable)")
	contextCmd.Flags().StringSliceVarP(&contextFiles, "file", "f", nil, "File already open in the editor (repeatable)")
	contextCmd.Flags().IntVarP(&contextBudget, "budget", "b", 0, "Maximum tokens to emit")
	contextCmd.Flags().IntVarP(&contextLimit, "limit", "n", 0, "Maximum nodes to emit")
	contextCmd.Flags().BoolVar(&contextStats, "stats", false, "Print token accounting to stderr")
}

func runContext(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	g, ix, err := sess.Graph()
	if err != nil {
		return err
	}
	if g.NodeCount() == 0 {
		return fmt.Errorf("the graph is empty — run 'memor build' to index this repository")
	}

	files := make([]string, 0, len(contextFiles))
	for _, f := range contextFiles {
		files = append(files, sess.RelPath(f))
	}

	result := retrieve.Retrieve(g, ix, sess.Cfg, retrieve.Query{
		Text:      contextQuery,
		Tags:      contextTags,
		OpenFiles: files,
		Budget:    contextBudget,
		Limit:     contextLimit,
	})

	fmt.Print(result.Text)
	if contextStats {
		fmt.Fprintf(os.Stderr, "\n%d nodes, %d/%d tokens, %d below the precision floor\n",
			len(result.Nodes), result.Tokens, result.Budget, result.Rejected)
	}
	return nil
}
