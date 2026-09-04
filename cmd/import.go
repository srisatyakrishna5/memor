// import.go — memor import
//
// Reads JSONL produced by `memor export` and appends it to the log. Nodes are
// content-addressed, so importing the same file twice is a no-op rather than a
// duplication.
package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import [file]",
	Short: "Import memories from JSONL",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runImport,
}

func runImport(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}

	var lines [][]byte
	if len(args) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		lines = splitLines(data)
	} else {
		lines, err = store.ReadRecords(args[0])
		if err != nil {
			return err
		}
	}

	records := graph.Decode(lines, "import")
	if len(records) == 0 {
		fmt.Println("Nothing to import.")
		return nil
	}
	if err := graph.Append(sess.Paths.Log, records); err != nil {
		return err
	}
	if _, _, err := graph.Compact(sess.Paths, sess.Cfg); err != nil {
		return err
	}

	fmt.Printf("Imported %d records\n", len(records))
	return nil
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if line := trimCR(data[start:i]); len(line) > 0 {
			out = append(out, line)
		}
		start = i + 1
	}
	if line := trimCR(data[start:]); len(line) > 0 {
		out = append(out, line)
	}
	return out
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}
