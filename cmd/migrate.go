// migrate.go — memor migrate
//
// Converts a v1 store (memory.snapshot.jsonl, memory.wal, knowledge.db) into
// the v2 graph and renames the v1 files to *.v1.bak. It runs automatically on
// the first v2 command, so this exists mainly to make the conversion explicit
// and inspectable.
package cmd

import (
	"fmt"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Convert a v1 store into the v2 graph",
	Args:  cobra.NoArgs,
	RunE:  runMigrate,
}

func runMigrate(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}

	// Opening the session migrates automatically, so by the time this runs the
	// work may already be done. Reporting the session's own result is honest;
	// saying "nothing to migrate" would not be.
	report := sess.Migrated
	if report == nil {
		if !sess.Paths.HasLegacyStore() {
			fmt.Println("No v1 store found. Nothing to migrate.")
			return nil
		}
		done, err := graph.Migrate(sess.Paths, sess.Cfg)
		if err != nil {
			return err
		}
		report = &done
	}

	fmt.Printf("Migrated %d memories, %d files, %d symbols, %d docs, %d topics, %d edges\n",
		report.Memories, report.Files, report.Symbols, report.Docs, report.Topics, report.Edges)
	if report.Skipped > 0 {
		fmt.Printf("Skipped %d malformed v1 records\n", report.Skipped)
	}
	fmt.Println("v1 files renamed to *.v1.bak. Run 'memor build' to add structure extraction.")
	return nil
}
