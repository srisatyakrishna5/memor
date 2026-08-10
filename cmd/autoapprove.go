package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// memorAutoApprovePattern is the regex used by VS Code to auto-approve memor terminal commands.
const memorAutoApprovePattern = `/^memor (context|add|code)\b/`

// memorAllowedBashRules are the Claude Code permission rules for memor commands.
var memorAllowedBashRules = []string{
	"Bash(memor context *)",
	"Bash(memor add *)",
	"Bash(memor code *)",
}

type autoApproveTarget struct {
	name   string
	key    string
	path   string
	apply  func(string) (bool, error)
	remove func(string) (bool, error)
}

var autoApproveTargets = []autoApproveTarget{
	{"GitHub Copilot", "copilot", filepath.Join(".vscode", "settings.json"), mergeVSCodeAutoApprove, removeVSCodeAutoApprove},
	{"Claude Code", "claude", filepath.Join(".claude", "settings.json"), mergeClaudePermissions, removeClaudePermissions},
}

func injectAutoApproveSettings(projectRoot string, toolsFlag string) error {
	requested := make(map[string]struct{})
	if toolsFlag != "" {
		for _, t := range strings.Split(toolsFlag, ",") {
			requested[strings.TrimSpace(strings.ToLower(t))] = struct{}{}
		}
	}

	for _, t := range autoApproveTargets {
		if toolsFlag != "" {
			if _, ok := requested[t.key]; !ok {
				continue
			}
		} else if t.key != "copilot" {
			// Without --tools flag, check if the tool directory already exists
			toolDir := filepath.Dir(filepath.Join(projectRoot, t.path))
			if _, err := os.Stat(toolDir); os.IsNotExist(err) {
				continue
			}
		}

		fullPath := filepath.Join(projectRoot, t.path)
		changed, err := t.apply(fullPath)
		if err != nil {
			return fmt.Errorf("auto-approve %s: %w", t.name, err)
		}
		if changed {
			fmt.Printf("Updated %s (auto-approve memor commands)\n", t.path)
		}
	}

	// Print manual setup hint for tools without file-based auto-approve
	showCursorHint := false
	if toolsFlag != "" {
		if _, ok := requested["cursor"]; ok {
			showCursorHint = true
		}
	} else {
		cursorDir := filepath.Join(projectRoot, ".cursor")
		if _, err := os.Stat(cursorDir); err == nil {
			showCursorHint = true
		}
	}
	if showCursorHint {
		fmt.Println("")
		fmt.Println("Note: Cursor requires manual setup for auto-approve.")
		fmt.Println("  Go to Settings > Cursor Settings > Agents > Auto-Run > Command Allowlist")
		fmt.Println("  and add: memor context, memor add, memor code")
	}

	return nil
}

// readJSONFile reads a JSON file into a map, returning an empty map if the file doesn't exist.
func readJSONFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]interface{}), nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return make(map[string]interface{}), nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// writeJSONFile writes a map as indented JSON.
func writeJSONFile(path string, m map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// mergeVSCodeAutoApprove adds the memor auto-approve regex to .vscode/settings.json.
func mergeVSCodeAutoApprove(path string) (bool, error) {
	m, err := readJSONFile(path)
	if err != nil {
		return false, err
	}

	autoApprove, _ := m["chat.tools.terminal.autoApprove"].(map[string]interface{})
	if autoApprove == nil {
		autoApprove = make(map[string]interface{})
	}

	if autoApprove[memorAutoApprovePattern] == true {
		return false, nil // already set
	}

	autoApprove[memorAutoApprovePattern] = true
	m["chat.tools.terminal.autoApprove"] = autoApprove
	return true, writeJSONFile(path, m)
}

// mergeClaudePermissions adds memor Bash allow rules to .claude/settings.json.
func mergeClaudePermissions(path string) (bool, error) {
	m, err := readJSONFile(path)
	if err != nil {
		return false, err
	}

	perms, _ := m["permissions"].(map[string]interface{})
	if perms == nil {
		perms = make(map[string]interface{})
	}

	var existingAllow []interface{}
	if raw, ok := perms["allow"]; ok {
		existingAllow, _ = raw.([]interface{})
	}

	existing := make(map[string]struct{})
	for _, v := range existingAllow {
		if s, ok := v.(string); ok {
			existing[s] = struct{}{}
		}
	}

	changed := false
	for _, rule := range memorAllowedBashRules {
		if _, ok := existing[rule]; !ok {
			existingAllow = append(existingAllow, rule)
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	perms["allow"] = existingAllow
	m["permissions"] = perms
	return true, writeJSONFile(path, m)
}

// removeAutoApproveSettings removes memor auto-approve entries from tool settings files.
func removeAutoApproveSettings(projectRoot string) {
	for _, t := range autoApproveTargets {
		fullPath := filepath.Join(projectRoot, t.path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}
		changed, err := t.remove(fullPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not clean auto-approve from %s: %v\n", t.path, err)
			continue
		}
		if changed {
			fmt.Printf("Removed memor auto-approve from %s\n", t.path)
		}
	}
}

// removeVSCodeAutoApprove removes the memor auto-approve regex from .vscode/settings.json.
func removeVSCodeAutoApprove(path string) (bool, error) {
	m, err := readJSONFile(path)
	if err != nil {
		return false, err
	}

	autoApprove, _ := m["chat.tools.terminal.autoApprove"].(map[string]interface{})
	if autoApprove == nil {
		return false, nil
	}

	if _, ok := autoApprove[memorAutoApprovePattern]; !ok {
		return false, nil
	}

	delete(autoApprove, memorAutoApprovePattern)
	if len(autoApprove) == 0 {
		delete(m, "chat.tools.terminal.autoApprove")
	} else {
		m["chat.tools.terminal.autoApprove"] = autoApprove
	}

	if len(m) == 0 {
		return true, os.Remove(path)
	}
	return true, writeJSONFile(path, m)
}

// removeClaudePermissions removes memor Bash allow rules from .claude/settings.json.
func removeClaudePermissions(path string) (bool, error) {
	m, err := readJSONFile(path)
	if err != nil {
		return false, err
	}

	perms, _ := m["permissions"].(map[string]interface{})
	if perms == nil {
		return false, nil
	}

	rawAllow, ok := perms["allow"]
	if !ok {
		return false, nil
	}
	existingAllow, _ := rawAllow.([]interface{})

	toRemove := make(map[string]struct{})
	for _, rule := range memorAllowedBashRules {
		toRemove[rule] = struct{}{}
	}

	var filtered []interface{}
	changed := false
	for _, v := range existingAllow {
		if s, ok := v.(string); ok {
			if _, remove := toRemove[s]; remove {
				changed = true
				continue
			}
		}
		filtered = append(filtered, v)
	}

	if !changed {
		return false, nil
	}

	if len(filtered) == 0 {
		delete(perms, "allow")
	} else {
		perms["allow"] = filtered
	}
	if len(perms) == 0 {
		delete(m, "permissions")
	} else {
		m["permissions"] = perms
	}

	if len(m) == 0 {
		return true, os.Remove(path)
	}
	return true, writeJSONFile(path, m)
}
