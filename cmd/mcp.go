// mcp.go — memor mcp
//
// Serves the repository graph over the Model Context Protocol on stdio, so MCP
// hosts such as GitHub Copilot in VS Code can call it as native tools instead
// of shelling out to the CLI.
//
// Nothing may be written to stdout while this runs: stdout carries JSON-RPC.
//
// Flags:
//
//	--project   Project directory to serve (default: current directory)
//
// Examples:
//
//	memor mcp
//	memor mcp --project /path/to/repo
package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/memor-dev/memor/internal/mcp"
	"github.com/spf13/cobra"
)

var mcpProject string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the repository graph over the Model Context Protocol",
	Long: `Serve the repository graph to MCP hosts over stdio.

Exposes repo_map, symbol_find, symbol_read, remember, and graph_status as
tools. The project root is discovered by walking up from the working directory,
so the server can be launched from a subdirectory.

Register with VS Code by adding to .vscode/mcp.json:

  {"servers": {"memor": {"command": "memor", "args": ["mcp"]}}}`,
	Args: cobra.NoArgs,
	RunE: runMCP,
}

func init() {
	mcpCmd.Flags().StringVar(&mcpProject, "project", "", "Project directory to serve (default: current directory)")
}

func runMCP(cmd *cobra.Command, args []string) error {
	dir := mcpProject
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = cwd
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return mcp.NewServer(dir, Version).Run(ctx)
}
