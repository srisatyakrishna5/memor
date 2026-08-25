package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func serversIn(t *testing.T, path, rootKey string) map[string]interface{} {
	t.Helper()
	m, err := readJSONFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	servers, _ := m[rootKey].(map[string]interface{})
	return servers
}

func TestAddMCPServerWritesStdioEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".vscode", "mcp.json")

	added, err := addMCPServer(path, "servers")
	if err != nil {
		t.Fatalf("addMCPServer: %v", err)
	}
	if !added {
		t.Fatal("expected the entry to be added")
	}

	entry, ok := serversIn(t, path, "servers")[memorMCPServerName].(map[string]interface{})
	if !ok {
		t.Fatalf("no %q entry under \"servers\"", memorMCPServerName)
	}
	if entry["type"] != "stdio" {
		t.Errorf("type = %v, want stdio", entry["type"])
	}
	if entry["command"] != "memor" {
		t.Errorf("command = %v, want memor", entry["command"])
	}
	args, _ := entry["args"].([]interface{})
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", args)
	}
}

// VS Code keys servers under "servers"; every other host uses "mcpServers".
func TestAddMCPServerHonoursHostRootKey(t *testing.T) {
	for _, h := range mcpHosts {
		t.Run(h.key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), h.path)
			if _, err := addMCPServer(path, h.rootKey); err != nil {
				t.Fatalf("addMCPServer: %v", err)
			}

			m, err := readJSONFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := m[h.rootKey]; !ok {
				t.Fatalf("%s: expected root key %q, got keys %v", h.path, h.rootKey, keysOf(m))
			}
			if len(m) != 1 {
				t.Errorf("expected only the root key, got %v", keysOf(m))
			}
		})
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAddMCPServerPreservesExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".vscode", "mcp.json")
	writeJSON(t, path, map[string]interface{}{
		"inputs": []interface{}{map[string]interface{}{"id": "token"}},
		"servers": map[string]interface{}{
			"playwright": map[string]interface{}{"command": "npx"},
		},
	})

	if _, err := addMCPServer(path, "servers"); err != nil {
		t.Fatalf("addMCPServer: %v", err)
	}

	m, err := readJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["inputs"]; !ok {
		t.Error("unrelated top-level key \"inputs\" was dropped")
	}
	servers, _ := m["servers"].(map[string]interface{})
	if _, ok := servers["playwright"]; !ok {
		t.Error("existing playwright server was dropped")
	}
	if _, ok := servers[memorMCPServerName]; !ok {
		t.Error("memor server was not added")
	}
}

// Re-running init must not clobber a hand-edited command or args.
func TestAddMCPServerLeavesExistingMemorEntryAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	custom := map[string]interface{}{"command": "/opt/bin/memor", "args": []interface{}{"mcp", "--project", "/repo"}}
	writeJSON(t, path, map[string]interface{}{
		"mcpServers": map[string]interface{}{memorMCPServerName: custom},
	})

	added, err := addMCPServer(path, "mcpServers")
	if err != nil {
		t.Fatalf("addMCPServer: %v", err)
	}
	if added {
		t.Error("reported a change when the entry already existed")
	}

	entry, _ := serversIn(t, path, "mcpServers")[memorMCPServerName].(map[string]interface{})
	if entry["command"] != "/opt/bin/memor" {
		t.Errorf("command = %v, want the user's /opt/bin/memor", entry["command"])
	}
}

func TestAddMCPServerIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".vscode", "mcp.json")

	first, err := addMCPServer(path, "servers")
	if err != nil {
		t.Fatal(err)
	}
	second, err := addMCPServer(path, "servers")
	if err != nil {
		t.Fatal(err)
	}
	if !first || second {
		t.Errorf("changed flags = (%v, %v), want (true, false)", first, second)
	}
}

func TestRemoveMCPServerKeepsOtherServersAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".cursor", "mcp.json")
	writeJSON(t, path, map[string]interface{}{
		"mcpServers": map[string]interface{}{
			memorMCPServerName: memorServerEntry(),
			"playwright":       map[string]interface{}{"command": "npx"},
		},
	})

	removed, err := removeMCPServer(path, "mcpServers")
	if err != nil {
		t.Fatalf("removeMCPServer: %v", err)
	}
	if !removed {
		t.Fatal("expected the entry to be removed")
	}

	servers := serversIn(t, path, "mcpServers")
	if _, ok := servers[memorMCPServerName]; ok {
		t.Error("memor entry survived removal")
	}
	if _, ok := servers["playwright"]; !ok {
		t.Error("unrelated playwright server was removed")
	}
}

// A config holding nothing but memor is deleted rather than left as "{}".
func TestRemoveMCPServerDeletesConfigItOwns(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".vscode", "mcp.json")
	if _, err := addMCPServer(path, "servers"); err != nil {
		t.Fatal(err)
	}

	if _, err := removeMCPServer(path, "servers"); err != nil {
		t.Fatalf("removeMCPServer: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted, stat err = %v", path, err)
	}
}

func TestRemoveMCPServerOnMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".vscode", "mcp.json")

	removed, err := removeMCPServer(path, "servers")
	if err != nil {
		t.Fatalf("removeMCPServer: %v", err)
	}
	if removed {
		t.Error("reported a change for a file that does not exist")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("removal created the file")
	}
}

func TestRegisterMCPServersDefaultsToCopilotOnly(t *testing.T) {
	dir := t.TempDir()

	if err := registerMCPServers(dir, ""); err != nil {
		t.Fatalf("registerMCPServers: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".vscode", "mcp.json")); err != nil {
		t.Errorf("expected .vscode/mcp.json: %v", err)
	}
	for _, p := range []string{".mcp.json", filepath.Join(".cursor", "mcp.json")} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("did not expect %s without --tools or a detected tool directory", p)
		}
	}
}

// Without --tools, other hosts are configured only when already in use.
func TestRegisterMCPServersDetectsExistingToolDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := registerMCPServers(dir, ""); err != nil {
		t.Fatalf("registerMCPServers: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".cursor", "mcp.json")); err != nil {
		t.Errorf("expected .cursor/mcp.json once .cursor/ exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Error("did not expect .mcp.json without .claude/")
	}
}

func TestRegisterMCPServersHonoursToolsFlag(t *testing.T) {
	dir := t.TempDir()

	if err := registerMCPServers(dir, "claude"); err != nil {
		t.Fatalf("registerMCPServers: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Errorf("expected .mcp.json for --tools claude: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".vscode", "mcp.json")); !os.IsNotExist(err) {
		t.Error("did not expect .vscode/mcp.json when only claude was requested")
	}
}

func TestDeregisterMCPServersClearsEveryHost(t *testing.T) {
	dir := t.TempDir()
	if err := registerMCPServers(dir, "copilot,claude,cursor"); err != nil {
		t.Fatalf("registerMCPServers: %v", err)
	}

	deregisterMCPServers(dir)

	for _, h := range mcpHosts {
		if _, err := os.Stat(filepath.Join(dir, h.path)); !os.IsNotExist(err) {
			t.Errorf("%s still present after deregistration", h.path)
		}
	}
}

// Regression: the filter used to derive its key from the first word of the
// display name, so "GitHub Copilot" became "github" and --tools copilot matched
// nothing.
func TestInjectToolConfigsMatchesCopilotToolKey(t *testing.T) {
	dir := t.TempDir()

	if err := injectToolConfigs(dir, "copilot"); err != nil {
		t.Fatalf("injectToolConfigs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("expected AGENTS.md for --tools copilot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cursorrules")); !os.IsNotExist(err) {
		t.Error("did not expect .cursorrules when only copilot was requested")
	}
}

func TestInstructionsPointAtMCPTools(t *testing.T) {
	instructions := makeInstructions()

	for _, tool := range []string{"memory_context", "memory_add", "code_get", "code_save"} {
		if !strings.Contains(instructions, tool) {
			t.Errorf("instructions do not mention the %s tool", tool)
		}
	}
}
