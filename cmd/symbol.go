// symbol.go — memor symbol find|read
//
// The CLI equivalents of the symbol_find and symbol_read MCP tools. `find`
// replaces grep for locating a definition; `read` replaces reading a whole file
// to see forty lines of it.
package cmd

import (
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/spf13/cobra"
)

var (
	symbolLimit int
	symbolPath  string
	symbolLines string
)

var symbolCmd = &cobra.Command{
	Use:   "symbol",
	Short: "Find and read symbols by name",
}

var symbolFindCmd = &cobra.Command{
	Use:   "find <name>",
	Short: "Locate a symbol's definition, callers, and callees",
	Args:  cobra.ExactArgs(1),
	RunE:  runSymbolFind,
}

var symbolReadCmd = &cobra.Command{
	Use:   "read <name>",
	Short: "Print the exact source lines a symbol occupies",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runSymbolRead,
}

func init() {
	symbolFindCmd.Flags().IntVarP(&symbolLimit, "limit", "n", 10, "Maximum matches")
	symbolReadCmd.Flags().StringVarP(&symbolPath, "path", "p", "", "File to read from; disambiguates a symbol or selects a line range")
	symbolReadCmd.Flags().StringVarP(&symbolLines, "lines", "l", "", "Line range to read, such as 20-80; requires --path")
	symbolCmd.AddCommand(symbolFindCmd, symbolReadCmd)
}

func runSymbolFind(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	g, _, err := sess.Graph()
	if err != nil {
		return err
	}

	matches := g.FindSymbols(args[0])
	if len(matches) == 0 {
		return fmt.Errorf("no indexed symbol matching %q — run 'memor build' if the graph is out of date", args[0])
	}
	if len(matches) > symbolLimit {
		matches = matches[:symbolLimit]
	}

	for _, n := range matches {
		location := "(no span)"
		if n.Span != nil {
			location = fmt.Sprintf("%s:%d-%d", n.Span.Path, n.Span.L0, n.Span.L1)
		}
		fmt.Printf("%s  %s  [%s]\n", location, n.Text, graph.FileStatus(sess.Root, n))

		if callers := n.MetaList(graph.MetaCallers); len(callers) > 0 {
			fmt.Printf("  <- %s\n", strings.Join(callers, ", "))
		}
		if callees := n.MetaList(graph.MetaCalls); len(callees) > 0 {
			fmt.Printf("  -> %s\n", strings.Join(callees, ", "))
		}
		for _, note := range g.Explaining(n.ID) {
			fmt.Printf("  ~ %s\n", collapseLine(note.Text, 140))
		}
	}
	return nil
}

func runSymbolRead(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}

	if symbolLines != "" {
		if symbolPath == "" {
			return fmt.Errorf("--lines requires --path")
		}
		first, last, err := parseLineRange(symbolLines)
		if err != nil {
			return err
		}
		source, err := graph.ReadLines(sess.Root, sess.RelPath(symbolPath), first, last)
		if err != nil {
			return err
		}
		fmt.Println(source)
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("provide a symbol name, or use --path with --lines")
	}

	g, _, err := sess.Graph()
	if err != nil {
		return err
	}

	want := sess.RelPath(symbolPath)
	for _, n := range g.FindSymbols(args[0]) {
		if n.Span == nil {
			continue
		}
		if want != "" && n.Span.Path != want {
			continue
		}
		source, err := graph.ReadSpan(sess.Root, n.Span)
		if err != nil {
			return err
		}
		fmt.Printf("// %s:%d-%d\n%s\n", n.Span.Path, n.Span.L0, n.Span.L1, source)
		return nil
	}
	return fmt.Errorf("no indexed symbol named %q — run 'memor symbol find %s' to see what exists", args[0], args[0])
}

func parseLineRange(s string) (int, int, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "-", 2)
	var first, last int
	if _, err := fmt.Sscanf(parts[0], "%d", &first); err != nil {
		return 0, 0, fmt.Errorf("invalid line range %q — use start-end, such as 20-80", s)
	}
	if len(parts) == 2 {
		if _, err := fmt.Sscanf(parts[1], "%d", &last); err != nil {
			return 0, 0, fmt.Errorf("invalid line range %q — use start-end, such as 20-80", s)
		}
	}
	return first, last, nil
}
