// status.go — memor status
//
// Reports node and edge counts, how much of the index has drifted from disk,
// pending writes, and on-disk footprint.
package cmd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show graph size, staleness, and footprint",
	Args:  cobra.NoArgs,
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	report, err := sess.Status()
	if err != nil {
		return err
	}

	fmt.Printf("Project:   %s\n", sess.Root)
	fmt.Printf("Nodes:     %d\n", report.Nodes)
	fmt.Printf("Edges:     %d\n", report.Edges)

	kinds := make([]string, 0, len(report.ByKind))
	for kind := range report.ByKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		fmt.Printf("  %-8s %d\n", kind, report.ByKind[kind])
	}

	fmt.Printf("Pending:   %d records awaiting compaction\n", report.Pending)
	fmt.Printf("Files:     %d fresh, %d stale, %d missing\n", report.Fresh, report.Stale, report.Missing)
	fmt.Printf("Footprint: %s\n", humanBytes(report.Bytes))
	fmt.Printf("Budget:    %d tokens\n", report.TokenBudget)
	if report.BuiltAt > 0 {
		fmt.Printf("Built:     %s\n", time.Unix(report.BuiltAt, 0).Format(time.RFC3339))
	}
	if len(report.Topics) > 0 {
		fmt.Printf("Topics:    %s\n", strings.Join(report.Topics, ", "))
	}

	switch {
	case report.Nodes == 0:
		fmt.Println("\nThe graph is empty. Run 'memor build' to index this repository.")
	case report.NeedsRebuild():
		fmt.Println("\nEnough indexed files have drifted that the map may mislead an agent. Run 'memor build'.")
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
