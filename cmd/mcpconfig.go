// mcpconfig.go — MCP server registration for AI tool hosts
//
// Writes the memor stdio server into each host's MCP config so agents get the
// memory tools natively instead of shelling out to the CLI. Hosts disagree on
// the top-level key: VS Code uses "servers", everyone else uses "mcpServers".
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// memorMCPServerName is the key memor registers itself under in host configs.
const memorMCPServerName = "memor"

// mcpHost describes where one AI tool expects MCP server registrations.
type mcpHost struct {
	name    string
	key     string // matches --tools values
	path    string // config file, relative to project root
	rootKey string // "servers" for VS Code, "mcpServers" elsewhere
	// detectDir gates default registration on that directory already existing.
	// Empty means always register when no --tools flag is given.
	detectDir string
}

// VS Code also reads a workspace .mcp.json natively, so the Claude Code entry
// doubles as portable configuration for the Copilot Agent Host.
var mcpHosts = []mcpHost{
	{"GitHub Copilot", "copilot", filepath.Join(".vscode", "mcp.json"), "servers", ""},
	{"Claude Code", "claude", ".mcp.json", "mcpServers", ".claude"},
	{"Cursor", "cursor", filepath.Join(".cursor", "mcp.json"), "mcpServers", ".cursor"},
}

// memorServerEntry is the stdio launch definition understood by every host.
func memorServerEntry() map[string]interface{} {
	return map[string]interface{}{
		"type":    "stdio",
		"command": "memor",
		"args":    []interface{}{"mcp"},
	}
}

// parseToolsFlag splits a comma-separated --tools value into a lookup set.
func parseToolsFlag(toolsFlag string) map[string]struct{} {
	requested := make(map[string]struct{})
	for _, t := range strings.Split(toolsFlag, ",") {
		if t = strings.TrimSpace(strings.ToLower(t)); t != "" {
			requested[t] = struct{}{}
		}
	}
	return requested
}

func (h mcpHost) selected(projectRoot string, requested map[string]struct{}, toolsFlag string) bool {
	if toolsFlag != "" {
		_, ok := requested[h.key]
		return ok
	}
	if h.detectDir == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(projectRoot, h.detectDir))
	return err == nil
}

func registerMCPServers(projectRoot, toolsFlag string) error {
	requested := parseToolsFlag(toolsFlag)
	registered := false

	for _, h := range mcpHosts {
		if !h.selected(projectRoot, requested, toolsFlag) {
			continue
		}
		added, err := addMCPServer(filepath.Join(projectRoot, h.path), h.rootKey)
		if err != nil {
			return fmt.Errorf("register MCP server for %s: %w", h.name, err)
		}
		if added {
			fmt.Printf("Registered memor MCP server in %s\n", h.path)
			registered = true
		}
	}

	if _, ok := requested["windsurf"]; ok {
		fmt.Println("")
		fmt.Println("Note: Windsurf stores MCP servers globally, not per project.")
		fmt.Println("  Add this to ~/.codeium/windsurf/mcp_config.json:")
		fmt.Println(`  "memor": { "command": "memor", "args": ["mcp"] }`)
	}

	if registered {
		fmt.Println("Restart your editor to load the memor MCP tools.")
	}
	return nil
}

// addMCPServer registers memor in an MCP host config. An existing "memor" entry
// is left untouched so a hand-edited command or args survives re-running init.
func addMCPServer(path, rootKey string) (bool, error) {
	m, err := readJSONFile(path)
	if err != nil {
		return false, err
	}

	servers, _ := m[rootKey].(map[string]interface{})
	if servers == nil {
		servers = make(map[string]interface{})
	}
	if _, exists := servers[memorMCPServerName]; exists {
		return false, nil
	}

	servers[memorMCPServerName] = memorServerEntry()
	m[rootKey] = servers
	return true, writeJSONFile(path, m)
}

// removeMCPServer strips the memor entry, preserving any other servers the user
// configured. A config left holding nothing is deleted rather than reduced to {}.
func removeMCPServer(path, rootKey string) (bool, error) {
	m, err := readJSONFile(path)
	if err != nil {
		return false, err
	}

	servers, _ := m[rootKey].(map[string]interface{})
	if servers == nil {
		return false, nil
	}
	if _, exists := servers[memorMCPServerName]; !exists {
		return false, nil
	}

	delete(servers, memorMCPServerName)
	if len(servers) == 0 {
		delete(m, rootKey)
	} else {
		m[rootKey] = servers
	}

	if len(m) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	return true, writeJSONFile(path, m)
}

// deregisterMCPServers removes memor from every host config that has it.
func deregisterMCPServers(projectRoot string) {
	for _, h := range mcpHosts {
		fullPath := filepath.Join(projectRoot, h.path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}
		removed, err := removeMCPServer(fullPath, h.rootKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove memor MCP server from %s: %v\n", h.path, err)
			continue
		}
		if removed {
			fmt.Printf("Removed memor MCP server from %s\n", h.path)
		}
	}
}
