// search.go — memor search
//
// Ranks the graph against a query and lists the matching nodes with their IDs,
// so a memory can be found and superseded, or a file located by topic.
package cmd

import (
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/retrieve"
	"github.com/spf13/cobra"
)

var (
	searchLimit int
	searchKind  string
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Find nodes matching a query",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "n", 10, "Maximum results")
	searchCmd.Flags().StringVarP(&searchKind, "kind", "k", "", "Restrict to one kind: file, sym, pkg, ext, doc, mem")
}

func runSearch(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	g, ix, err := sess.Graph()
	if err != nil {
		return err
	}

	query := retrieve.Query{
		Text:   args[0],
		Limit:  searchLimit,
		Budget: 1 << 30, // The limit does the bounding here, not the budget.
	}
	if searchKind != "" {
		kind, ok := graph.ParseKind(strings.TrimSpace(searchKind))
		if !ok {
			return fmt.Errorf("unknown kind %q — use file, sym, pkg, ext, doc, or mem", searchKind)
		}
		query.Kinds = []graph.Kind{kind}
	}

	result := retrieve.Retrieve(g, ix, sess.Cfg, query)
	if len(result.Nodes) == 0 {
		fmt.Println("No matches above the precision floor.")
		return nil
	}

	for _, s := range result.Nodes {
		location := s.Node.Name
		if s.Node.Span != nil && s.Node.Span.Path != "" && s.Node.Kind == graph.KindSym {
			location = fmt.Sprintf("%s:%d %s", s.Node.Span.Path, s.Node.Span.L0, s.Node.Name)
		}
		fmt.Printf("%.3f  %-5s %s  [%s]\n", s.Score, s.Node.Kind, location, s.Node.ID)
		if text := strings.TrimSpace(s.Node.Text); text != "" {
			fmt.Printf("       %s\n", collapseLine(text, 120))
		}
	}
	return nil
}

func collapseLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
