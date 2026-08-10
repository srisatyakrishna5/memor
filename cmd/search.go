// search.go — memor search, memor query
//
// memor search: Keyword search across all memories using BM25 ranking.
// Returns scored results sorted by relevance.
//
//	Flags: --top (number of results, default: 5)
//
//	Examples:
//	  memor search "redis cache"
//	  memor search "deploy" --top 10
//	  memor search "pnpm build monorepo"
//
// memor query: Filter memories by tags. Returns all entries matching the tags.
//
//	Flags: --tags (required, comma-separated)
//
//	Examples:
//	  memor query --tags "auth"
//	  memor query --tags "db,perf"
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/engine"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var searchTop int

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search memories by keyword",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().IntVar(&searchTop, "top", 5, "Number of results to return")
}

func runSearch(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	query := strings.Join(args, " ")
	results, err := engine.Search(paths, cfg, query, searchTop)
	if err != nil {
		return err
	}

	if len(results) == 0 {
		fmt.Println("No matches found.")
		return nil
	}

	for i, r := range results {
		fmt.Printf("%d. [%s] %s %s: %s (score: %.3f)\n",
			i+1, r.ID, r.Type.Prefix(), renderTagList(r.Tags), r.Content, r.Score)
	}
	return nil
}

var queryTags string

var queryCmd = &cobra.Command{
	Use:   "query",
	Short: "Query memories by tags",
	RunE:  runQuery,
}

func init() {
	queryCmd.Flags().StringVar(&queryTags, "tags", "", "Comma-separated tags to filter by")
}

func runQuery(cmd *cobra.Command, args []string) error {
	if queryTags == "" {
		return fmt.Errorf("--tags is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	tags := strings.Split(queryTags, ",")
	for i, t := range tags {
		tags[i] = strings.TrimSpace(t)
	}

	results, err := engine.QueryByTags(paths, tags)
	if err != nil {
		return err
	}

	if len(results) == 0 {
		fmt.Println("No entries found for those tags.")
		return nil
	}

	for i, e := range results {
		fmt.Printf("%d. [%s] %s %s: %s\n",
			i+1, e.ID, e.Type.Prefix(), renderTagList(e.Tags), e.Content)
	}
	return nil
}

func renderTagList(tags []string) string {
	var parts []string
	for _, t := range tags {
		parts = append(parts, "#"+t)
	}
	return strings.Join(parts, " ")
}
