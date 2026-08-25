// init.go — memor init
//
// Initializes Memor in the current project. Creates the .memor/ directory with
// config.toml, empty memory.db and memory.wal, installs a git pre-commit hook,
// adds .memor/ to .gitignore, and copies SKILL.md into AI tool skills directories.
// Imports .memor-bootstrap.jsonl if present.
//
// Flags:
//
//	--tools      Comma-separated tools to configure: copilot,claude,cursor,windsurf
//	--no-mcp     Skip registering the memor MCP server in AI tool configs
//
// Examples:
//
//	memor init
//	memor init --tools copilot,claude,cursor
//	memor init --no-mcp
package cmd

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/constants"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var initTools string
var initNoMCP bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize memory in the current project",
	Long:  "Creates .memor/ directory, config.toml, empty WAL and snapshot, installs git hooks, registers the memor MCP server, and injects instructions into AI tool configs.",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().StringVar(&initTools, "tools", "", "Comma-separated tools to configure: copilot,claude,cursor,windsurf")
	initCmd.Flags().BoolVar(&initNoMCP, "no-mcp", false, "Skip registering the memor MCP server in AI tool configs")
}

func runInit(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	paths := store.ResolvePaths(cwd)

	// Create directories
	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}

	// Create config.toml with defaults
	if _, err := os.Stat(paths.Config); os.IsNotExist(err) {
		cfg := config.Default()
		if err := config.Save(paths.Config, cfg); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Println("Created", paths.Config)
	}

	// Create empty memory.db
	if _, err := os.Stat(paths.MemoryDB); os.IsNotExist(err) {
		if err := os.WriteFile(paths.MemoryDB, []byte(fmt.Sprintf("@mem v1 | 0 entries | budget:%d | compacted:none\n", constants.DefaultTokenBudget)), 0o644); err != nil {
			return fmt.Errorf("write memory.db: %w", err)
		}
		fmt.Println("Created", paths.MemoryDB)
	}

	// Create empty memory.wal
	if _, err := os.Stat(paths.MemoryWAL); os.IsNotExist(err) {
		if err := os.WriteFile(paths.MemoryWAL, nil, 0o644); err != nil {
			return fmt.Errorf("write memory.wal: %w", err)
		}
		fmt.Println("Created", paths.MemoryWAL)
	}

	// Add .memor/ to .gitignore
	if err := ensureGitignore(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
	}

	// Install pre-commit hook
	if err := installPreCommitHook(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install pre-commit hook: %v\n", err)
	}

	// Inject into AI tool configs
	if err := injectToolConfigs(cwd, initTools); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not inject tool configs: %v\n", err)
	}

	// Inject auto-approve settings for terminal commands
	if err := injectAutoApproveSettings(cwd, initTools); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not inject auto-approve settings: %v\n", err)
	}

	// Register the MCP server so agents get memory as tools, not shell commands
	if !initNoMCP {
		if err := registerMCPServers(cwd, initTools); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not register MCP server: %v\n", err)
		}
	}

	// Import bootstrap file if exists
	bootstrapPath := filepath.Join(cwd, ".memor-bootstrap.jsonl")
	if info, err := os.Stat(bootstrapPath); err == nil && !info.IsDir() {
		entries, err := store.ReadWAL(bootstrapPath)
		if err == nil && len(entries) > 0 {
			for _, e := range entries {
				if err := store.AppendToWAL(paths.MemoryWAL, e); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not import bootstrap entry: %v\n", err)
				}
			}
			fmt.Printf("Found .memor-bootstrap.jsonl — imported %d entries\n", len(entries))
		}
	}

	fmt.Println("Memor initialized successfully.")
	return nil
}

func ensureGitignore(projectRoot string) error {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")

	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if strings.Contains(string(content), ".memor/") {
		return nil // already present
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	entry := ".memor/\n"
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		entry = "\n" + entry
	}
	_, err = f.WriteString(entry)
	if err == nil {
		fmt.Println("Added .memor/ to .gitignore")
	}
	return err
}

func installPreCommitHook(projectRoot string) error {
	hooksDir := filepath.Join(projectRoot, ".git", "hooks")
	if _, err := os.Stat(filepath.Join(projectRoot, ".git")); os.IsNotExist(err) {
		return nil // not a git repo
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}

	hookPath := filepath.Join(hooksDir, "pre-commit")

	// Check if hook already exists and has memor content
	if content, err := os.ReadFile(hookPath); err == nil {
		if strings.Contains(string(content), "memor") {
			return nil
		}
	}

	hookScript := `#!/bin/sh
# Memor pre-commit hook — auto-extract memories from staged changes
# This hook never blocks commits (always exits 0)

if command -v memor >/dev/null 2>&1; then
  memor compact --if-needed 2>/dev/null || true
fi

exit 0
`

	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		return err
	}
	fmt.Println("Installed .git/hooks/pre-commit (memory auto-extract)")
	return nil
}

const (
	memorInstructionsStart = "<!-- BEGIN MEMOR INSTRUCTIONS -->"
	memorInstructionsEnd   = "<!-- END MEMOR INSTRUCTIONS -->"
)

//go:embed templates/memor-instructions.md.tmpl
var memorInstructionsTemplate string

func makeInstructions() string {
	normalized := strings.ReplaceAll(memorInstructionsTemplate, "\r\n", "\n")
	return strings.TrimRight(normalized, "\n") + "\n"
}

func wrapMemorInstructions(instructions string) string {
	return memorInstructionsStart + "\n" + strings.TrimRight(instructions, "\n") + "\n" + memorInstructionsEnd + "\n"
}

func upsertMemorInstructions(content, instructions string) (string, bool) {
	wrapped := wrapMemorInstructions(instructions)
	start := strings.Index(content, memorInstructionsStart)
	if start >= 0 {
		end := strings.Index(content[start:], memorInstructionsEnd)
		if end >= 0 {
			end += start + len(memorInstructionsEnd)
			if strings.HasPrefix(content[end:], "\r\n") {
				end += len("\r\n")
			} else if strings.HasPrefix(content[end:], "\n") {
				end++
			}
			updated := content[:start] + wrapped + content[end:]
			return updated, updated != content
		}
	}

	if strings.TrimSpace(content) == strings.TrimSpace(instructions) {
		return wrapped, wrapped != content
	}

	if strings.TrimSpace(content) == "" {
		return wrapped, wrapped != content
	}

	separator := "\n\n"
	if strings.HasSuffix(content, "\n\n") || strings.HasSuffix(content, "\r\n\r\n") {
		separator = ""
	} else if strings.HasSuffix(content, "\n") || strings.HasSuffix(content, "\r\n") {
		separator = "\n"
	}

	return content + separator + wrapped, true
}

func removeMemorInstructions(content, instructions string) (string, bool) {
	start := strings.Index(content, memorInstructionsStart)
	if start >= 0 {
		end := strings.Index(content[start:], memorInstructionsEnd)
		if end >= 0 {
			end += start + len(memorInstructionsEnd)
			updated := removeInstructionRange(content, start, end)
			return updated, updated != content
		}
	}

	if strings.TrimSpace(content) == strings.TrimSpace(instructions) {
		return "", true
	}

	start = strings.Index(content, instructions)
	if start < 0 {
		return content, false
	}
	end := start + len(instructions)
	updated := removeInstructionRange(content, start, end)
	return updated, updated != content
}

func removeInstructionRange(content string, start, end int) string {
	before := content[:start]
	after := content[end:]

	if strings.HasPrefix(after, "\r\n") {
		after = after[len("\r\n"):]
	} else if strings.HasPrefix(after, "\n") {
		after = after[1:]
	}

	if strings.HasSuffix(before, "\r\n\r\n") {
		before = before[:len(before)-len("\r\n")]
	} else if strings.HasSuffix(before, "\n\n") {
		before = before[:len(before)-1]
	}

	updated := before + after
	if strings.TrimSpace(updated) == "" {
		updated = ""
	}
	return updated
}

func writeMemorInstructionsFile(path, instructions string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	updated, changed := upsertMemorInstructions(string(content), instructions)
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func clearMemorInstructionsFile(path, instructions string) (bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	updated, changed := removeMemorInstructions(string(content), instructions)
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// toolInstructionFile maps each tool to its auto-discovered instruction file.
type toolInstructionFile struct {
	toolName string
	key      string // matches --tools values
	path     string // relative to project root
	content  string
}

func getToolInstructionFiles() []toolInstructionFile {
	instructions := makeInstructions()
	return []toolInstructionFile{
		{"GitHub Copilot", "copilot", "AGENTS.md", instructions},
		{"Cursor", "cursor", ".cursorrules", instructions},
		{"Windsurf", "windsurf", ".windsurfrules", instructions},
	}
}

func injectToolConfigs(projectRoot string, toolsFlag string) error {
	files := getToolInstructionFiles()

	// If specific tools requested, filter
	if toolsFlag != "" {
		requested := parseToolsFlag(toolsFlag)

		var filtered []toolInstructionFile
		for _, inf := range files {
			if _, ok := requested[inf.key]; ok {
				filtered = append(filtered, inf)
			}
		}
		files = filtered
	}

	for _, inf := range files {
		// When no --tools flag, only create Copilot instruction file by default
		if toolsFlag == "" && inf.toolName != "GitHub Copilot" {
			continue
		}
		fullPath := filepath.Join(projectRoot, inf.path)
		_, statErr := os.Stat(fullPath)
		existed := statErr == nil
		if changed, err := writeMemorInstructionsFile(fullPath, inf.content); err != nil {
			return err
		} else if changed {
			if existed {
				fmt.Printf("Updated %s\n", inf.path)
			} else {
				fmt.Printf("Created %s\n", inf.path)
			}
		}
	}

	return nil
}
