// mcpconfig.go — MCP server registration
//
// Writes the memor stdio server into a host's MCP config so agents get the
// graph tools natively instead of shelling out to the CLI. Hosts disagree on
// the top-level key: VS Code uses "servers", everyone else uses "mcpServers".
//
// VS Code is registered by default and is the only host written without an
// explicit --tools flag. This is the irreducible file: an MCP server cannot
// discover itself.
package cmd

import (
	"encoding/json"
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
	// byDefault registers the host even when --tools is not given.
	byDefault bool
}

var mcpHosts = []mcpHost{
	{"GitHub Copilot", "copilot", filepath.Join(".vscode", "mcp.json"), "servers", true},
	{"Claude Code", "claude", ".mcp.json", "mcpServers", false},
	{"Cursor", "cursor", filepath.Join(".cursor", "mcp.json"), "mcpServers", false},
}

func memorServerEntry() map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": "memor",
		"args":    []any{"mcp"},
	}
}

func parseToolsFlag(toolsFlag string) map[string]struct{} {
	requested := make(map[string]struct{})
	for _, t := range strings.Split(toolsFlag, ",") {
		if t = strings.TrimSpace(strings.ToLower(t)); t != "" {
			requested[t] = struct{}{}
		}
	}
	return requested
}

func (h mcpHost) selected(requested map[string]struct{}, toolsFlag string) bool {
	if toolsFlag == "" {
		return h.byDefault
	}
	_, ok := requested[h.key]
	return ok || h.byDefault
}

func registerMCPServers(projectRoot, toolsFlag string) error {
	requested := parseToolsFlag(toolsFlag)
	registered := false

	for _, h := range mcpHosts {
		if !h.selected(requested, toolsFlag) {
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
		fmt.Println("\nWindsurf stores MCP servers globally, not per project.")
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

	servers, _ := m[rootKey].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
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

	servers, _ := m[rootKey].(map[string]any)
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

// deregisterMCPServer removes memor from every host config that has it.
func deregisterMCPServer(projectRoot string) {
	for _, h := range mcpHosts {
		fullPath := filepath.Join(projectRoot, h.path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}
		removed, err := removeMCPServer(fullPath, h.rootKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove memor from %s: %v\n", h.path, err)
			continue
		}
		if removed {
			fmt.Printf("Removed memor MCP server from %s\n", h.path)
		}
	}
}

// readJSONFile reads a JSON file into a map, returning an empty map when the
// file does not exist or is blank.
func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return make(map[string]any), nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func writeJSONFile(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
