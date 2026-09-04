package cmd

import (
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/session"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "memor",
	Short: "Repository knowledge graph and persistent memory for AI coding assistants",
	Long: `Memor — a repository-native knowledge graph and memory store.

It lives in .memor/ inside your project (gitignored), indexes your files,
symbols, dependencies, and docs into one graph, records what past conversations
learned, and hands an AI assistant a task-ranked map within a token budget
instead of making it read files to find out where anything is.`,
	SilenceUsage: true,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(contextCmd)
	rootCmd.AddCommand(rememberCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(symbolCmd)
	rootCmd.AddCommand(compactCmd)
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(importCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(rulesCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(versionCmd)
}

// openSession resolves the project for a command, reporting the same
// actionable error everywhere when the project is not initialized.
func openSession() (*session.Session, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	return session.Open(cwd)
}
