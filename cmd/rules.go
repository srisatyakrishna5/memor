// rules.go — memor rules
//
// Prints the same protocol the MCP server injects into a host's system prompt.
// AGENTS.md points here rather than repeating it, so there is one copy to keep
// correct instead of two.
package cmd

import (
	"fmt"

	"github.com/memor-dev/memor/internal/mcp"
	"github.com/spf13/cobra"
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Print the agent protocol memor expects",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(mcp.Instructions)
	},
}
