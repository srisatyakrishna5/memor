// export.go — memor export
//
// Writes agent-authored nodes and the edges between them as JSONL. Structural
// nodes are excluded by default: they are reproducible from source with a
// single `memor build`, so shipping them would be shipping a derived artifact.
package cmd

import (
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/spf13/cobra"
)

var (
	exportOutput string
	exportAll    bool
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export memories as JSONL",
	Args:  cobra.NoArgs,
	RunE:  runExport,
}

func init() {
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "File to write; defaults to stdout")
	exportCmd.Flags().BoolVar(&exportAll, "all", false, "Include machine-extracted structure as well")
}

func runExport(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	g, err := graph.Load(sess.Paths)
	if err != nil {
		return err
	}

	keep := make(map[string]struct{}, g.NodeCount())
	var records []graph.Record
	for _, n := range g.Nodes() {
		if !exportAll && n.Origin() == graph.OriginExtract {
			continue
		}
		keep[n.ID] = struct{}{}
		records = append(records, graph.NodeRecord(n))
	}
	for _, e := range g.Edges() {
		if _, ok := keep[e.From]; !ok {
			continue
		}
		if _, ok := keep[e.To]; !ok {
			continue
		}
		records = append(records, graph.EdgeRecord(e))
	}

	lines, err := graph.Encode(records)
	if err != nil {
		return err
	}

	var data []byte
	for _, line := range lines {
		data = append(data, line...)
		data = append(data, '\n')
	}

	if exportOutput == "" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.WriteFile(exportOutput, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Exported %d records to %s\n", len(records), exportOutput)
	return nil
}
