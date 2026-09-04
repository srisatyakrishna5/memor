// compact.go — memor compact
//
// Folds graph.log into graph.snap, archives decayed memories, and regenerates
// the rendered projection.
package cmd

import (
	"fmt"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/spf13/cobra"
)

var compactIfNeeded bool

var compactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Fold pending writes into the snapshot",
	Args:  cobra.NoArgs,
	RunE:  runCompact,
}

func init() {
	compactCmd.Flags().BoolVar(&compactIfNeeded, "if-needed", false, "Compact only if the log has grown past the configured threshold")
}

func runCompact(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}

	if compactIfNeeded {
		written, archived, ran, err := graph.AutoCompact(sess.Paths, sess.Cfg)
		if err != nil {
			return err
		}
		if !ran {
			fmt.Println("Nothing to compact.")
			return nil
		}
		fmt.Printf("Compacted %d nodes, archived %d\n", written, archived)
		return nil
	}

	written, archived, err := graph.Compact(sess.Paths, sess.Cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Compacted %d nodes, archived %d\n", written, archived)
	return nil
}
