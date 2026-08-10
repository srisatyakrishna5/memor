// context.go — memor context
//
// The main entry point for AI agents. Retrieves the most relevant memories and
// knowledge sections within a token budget. Outputs a ready-to-inject context block.
//
// Flags:
//
//	--budget   Override token budget from config.toml
//	--query    Filter by relevance to a specific task
//
// Examples:
//
//	memor context
//	memor context --query "fix flaky auth test"
//	memor context --budget 5000 --query "deploy api"
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

var (
	contextBudget int
	contextQuery  string
)

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Get relevant context for a conversation",
	Long:  "Retrieves the most relevant memories and knowledge sections within a token budget. The main command agents call at conversation start.",
	RunE:  runContext,
}

func init() {
	contextCmd.Flags().IntVar(&contextBudget, "budget", 0, "Token budget (default from config.toml)")
	contextCmd.Flags().StringVar(&contextQuery, "query", "", "Query to find relevant context for")
}

func runContext(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Parse query from remaining args if not set via flag
	query := contextQuery
	if query == "" && len(args) > 0 {
		query = strings.Join(args, " ")
	}

	opts := engine.ContextOptions{
		Budget: contextBudget,
		Query:  query,
	}

	result, err := engine.Context(paths, cfg, opts)
	if err != nil {
		return err
	}

	fmt.Print(result)
	return nil
}
