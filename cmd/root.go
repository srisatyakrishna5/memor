package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "memor",
	Short: "Local memory persistence for AI coding assistants",
	Long: `Memor — a compact local memory store and CLI.
It sits in .memor/ inside your project (gitignored), learns from every conversation,
indexes your skills and instructions, and gives every AI tool exactly the right
context — within a token budget — at the start of every conversation.`,
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
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(contextCmd)
	rootCmd.AddCommand(compactCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(queryCmd)
	rootCmd.AddCommand(statsCmd)
	rootCmd.AddCommand(rebuildCmd)
	rootCmd.AddCommand(reinforceCmd)
	rootCmd.AddCommand(knowledgeCmd)
	rootCmd.AddCommand(purgeCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(codeCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(importCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(versionCmd)
}
