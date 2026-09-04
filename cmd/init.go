// init.go — memor init
//
// Creates .memor/, gitignores it, registers the MCP server, and drops a
// three-line pointer into AGENTS.md.
//
// Behavioural rules live in the MCP tool descriptions rather than a markdown
// template: they load with the tool, cannot be edited away, and are versioned
// with the binary. The only reason AGENTS.md exists at all is that Copilot
// cannot be told to read a file it does not already know about.
//
// Flags:
//
//	--tools    Additional hosts to register: claude, cursor
//	--no-mcp   Skip MCP registration
//	--build    Index the repository immediately after initializing
//
// Examples:
//
//	memor init
//	memor init --build
//	memor init --tools claude,cursor
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/memor-dev/memor/internal/session"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var (
	initTools string
	initNoMCP bool
	initBuild bool
)

const (
	agentsFile      = "AGENTS.md"
	agentsBeginMark = "<!-- BEGIN MEMOR -->"
	agentsEndMark   = "<!-- END MEMOR -->"
)

// agentsPointer is deliberately three lines. Everything an agent needs to know
// about how to behave is in the tool descriptions; duplicating it here would
// only create a second copy to drift.
const agentsPointer = `This repo uses memor for its repository state and project memory.
Call ` + "`repo_brief`" + ` first in every conversation, before reading or searching anything.
Then ` + "`repo_map`" + `, ` + "`symbol_find`" + `, ` + "`symbol_read`" + `. Full protocol: run ` + "`memor rules`" + `.`

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize memor in the current project",
	Args:  cobra.NoArgs,
	RunE:  runInit,
}

func init() {
	initCmd.Flags().StringVar(&initTools, "tools", "", "Additional hosts to register: claude, cursor")
	initCmd.Flags().BoolVar(&initNoMCP, "no-mcp", false, "Skip registering the memor MCP server")
	initCmd.Flags().BoolVar(&initBuild, "build", false, "Index the repository immediately")
}

func runInit(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	sess, err := session.Create(cwd)
	if err != nil {
		return err
	}
	fmt.Printf("Created %s\n", sess.Paths.Root)

	if err := ensureGitignore(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
	}
	if err := writeAgentsPointer(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update %s: %v\n", agentsFile, err)
	}
	if !initNoMCP {
		if err := registerMCPServers(cwd, initTools); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not register MCP server: %v\n", err)
		}
	}

	if initBuild {
		report, err := sess.Build()
		if err != nil {
			return err
		}
		fmt.Printf("Indexed %d files, %d symbols\n", report.Files, report.Symbols)
		return nil
	}

	fmt.Println("Run 'memor build' to index this repository.")
	return nil
}

func ensureGitignore(projectRoot string) error {
	path := filepath.Join(projectRoot, ".gitignore")

	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(content), store.DirName+"/") {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	entry := store.DirName + "/\n"
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		entry = "\n" + entry
	}
	if _, err := f.WriteString(entry); err != nil {
		return err
	}
	fmt.Printf("Added %s/ to .gitignore\n", store.DirName)
	return nil
}

// writeAgentsPointer inserts or refreshes the memor block, leaving everything
// else in AGENTS.md untouched. Owning a fenced region rather than the file is
// what keeps re-running init from clobbering a user's own instructions.
func writeAgentsPointer(projectRoot string) error {
	path := filepath.Join(projectRoot, agentsFile)

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	block := agentsBeginMark + "\n" + agentsPointer + "\n" + agentsEndMark + "\n"

	content := string(existing)
	if start := strings.Index(content, agentsBeginMark); start >= 0 {
		if end := strings.Index(content[start:], agentsEndMark); end >= 0 {
			end += start + len(agentsEndMark)
			if strings.HasPrefix(content[end:], "\n") {
				end++
			}
			updated := content[:start] + block + content[end:]
			if updated == content {
				return nil
			}
			return os.WriteFile(path, []byte(updated), 0o644)
		}
	}

	separator := ""
	if trimmed := strings.TrimRight(content, "\n"); trimmed != "" {
		separator = trimmed + "\n\n"
	}
	if err := os.WriteFile(path, []byte(separator+block), 0o644); err != nil {
		return err
	}
	fmt.Printf("Updated %s\n", agentsFile)
	return nil
}
