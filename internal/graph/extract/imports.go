package extract

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

// L0 import patterns. These are line-oriented on purpose: a scan that never
// builds a syntax tree costs nothing to add a language to, and import
// statements are the one construct every language keeps on its own line.
var (
	goImportLine  = regexp.MustCompile(`^\s*(?:import\s+)?(?:[\w.]+\s+)?"([^"]+)"`)
	jsFrom        = regexp.MustCompile(`(?:^|\s)(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]`)
	jsBareImport  = regexp.MustCompile(`(?:^|\s)import\s*['"]([^'"]+)['"]`)
	jsRequire     = regexp.MustCompile(`(?:require|import)\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	pyFrom        = regexp.MustCompile(`^\s*from\s+([\w.]+)\s+import\s`)
	pyImport      = regexp.MustCompile(`^\s*import\s+([\w.]+)`)
	javaImport    = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.]+)\s*;`)
	csharpUsing   = regexp.MustCompile(`^\s*(?:global\s+)?using\s+(?:static\s+)?([\w.]+)\s*;`)
	rustUse       = regexp.MustCompile(`^\s*(?:pub\s+)?use\s+([\w:]+)`)
	rubyRequire   = regexp.MustCompile(`^\s*require(?:_relative)?\s+['"]([^'"]+)['"]`)
	phpUse        = regexp.MustCompile(`^\s*use\s+([\w\\]+)`)
	blockLineStop = regexp.MustCompile(`^\s*\)`)
)

// Imports returns the module specifiers a source file depends on.
func Imports(lang string, data []byte) []string {
	switch lang {
	case "go":
		return goImports(data)
	case "js", "ts":
		return matchAll(data, jsFrom, jsBareImport, jsRequire)
	case "python":
		return lineMatches(data, pyFrom, pyImport)
	case "java", "kotlin":
		return lineMatches(data, javaImport)
	case "csharp":
		return lineMatches(data, csharpUsing)
	case "rust":
		return lineMatches(data, rustUse)
	case "ruby":
		return lineMatches(data, rubyRequire)
	case "php":
		return lineMatches(data, phpUse)
	default:
		return nil
	}
}

// goImports handles both the single-line form and the parenthesized block,
// which is the only place Go puts an import path on a line without the keyword.
func goImports(data []byte) []string {
	var out []string
	inBlock := false

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "//"):
			continue
		case trimmed == "import (":
			inBlock = true
			continue
		case inBlock && blockLineStop.MatchString(line):
			inBlock = false
			continue
		}

		if inBlock || strings.HasPrefix(trimmed, "import ") {
			if m := goImportLine.FindStringSubmatch(line); m != nil {
				out = append(out, m[1])
			}
			continue
		}
		// Imports must precede any declaration, so the first one ends the scan.
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "type ") {
			break
		}
	}
	return dedupe(out)
}

func lineMatches(data []byte, patterns ...*regexp.Regexp) []string {
	var out []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		for _, re := range patterns {
			if m := re.FindStringSubmatch(line); m != nil {
				out = append(out, normalizeSpecifier(m[1]))
				break
			}
		}
	}
	return dedupe(out)
}

func matchAll(data []byte, patterns ...*regexp.Regexp) []string {
	var out []string
	text := string(data)
	for _, re := range patterns {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out = append(out, normalizeSpecifier(m[1]))
		}
	}
	return dedupe(out)
}

// normalizeSpecifier collapses a language-specific reference onto the shortest
// form that still identifies a dependency. Trailing wildcards and trait imports
// carry no extra information for the graph.
func normalizeSpecifier(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".*")
	s = strings.TrimSuffix(s, "::*")
	s = strings.TrimSuffix(s, "::")
	return s
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
