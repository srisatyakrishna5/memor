package extract

import (
	"regexp"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
)

// Symbol extraction for languages memor has no parser for.
//
// Go gets a real AST because the parser ships with the toolchain. Everything
// else is recovered by matching declaration lines and then bounding the body by
// brace depth or indentation. That is less accurate than a parser and it is
// meant to be: a declaration line is unambiguous enough to match on, while
// anything requiring real scope analysis is left out rather than guessed. A
// wrong span is worse than a missing one, because the agent cannot detect it.
//
// tree-sitter would do this properly and is not an option: its Go bindings are
// mostly C, and CGO would break the cross-compiled binaries the npm installer
// ships.

// bodyStyle decides how a declaration's extent is found.
type bodyStyle int

const (
	styleBrace  bodyStyle = iota // body runs to the matching closing brace
	styleIndent                  // body runs until the indent returns to the header's level
)

// langRules describes how to recognize declarations in one language.
type langRules struct {
	style bodyStyle
	// decl captures the declaration keyword in group 1 and the name in group 2.
	decl *regexp.Regexp
	// lineComment ends a line; blockComment delimits a span.
	lineComment string
	// kinds maps a captured keyword onto memor's symbol kind.
	kinds map[string]string
}

var (
	// TypeScript and JavaScript. Arrow functions and class expressions are
	// caught through the const/let/var branch, which is why the assignment is
	// required: a plain `const MAX = 4` is a value declaration and belongs here
	// too, but `const {a, b} = obj` is destructuring and must not match.
	jsDecl = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?(?:async\s+)?(function\*?|class|interface|type|enum|const|let|var)\s+([A-Za-z_$][\w$]*)`)

	pyDecl = regexp.MustCompile(`^(\s*)(?:async\s+)?(def|class)\s+([A-Za-z_]\w*)`)

	rustDecl = regexp.MustCompile(`^\s*(?:pub(?:\s*\([^)]*\))?\s+)?(?:default\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+"[^"]*"\s+)?(fn|struct|enum|trait|impl|type|const|static|macro_rules!)\s+([A-Za-z_]\w*)`)

	// Java, Kotlin, C# and Swift share a modifier-then-keyword shape for type
	// declarations. Methods are deliberately excluded: their syntax is
	// `Type name(args)` with no keyword to anchor on, and every heuristic for it
	// also matches calls, casts and field initializers.
	jvmDecl = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|abstract|sealed|partial|open|data|inner|record)\s+)*(class|interface|enum|record|struct|object|protocol|extension)\s+([A-Za-z_]\w*)`)

	phpDecl = regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract)\s+)*(function|class|interface|trait|enum)\s+([A-Za-z_]\w*)`)

	rubyDecl = regexp.MustCompile(`^(\s*)(def|class|module)\s+([A-Za-z_][\w.:]*[?!]?)`)
)

var rules = map[string]langRules{
	"ts": {style: styleBrace, decl: jsDecl, lineComment: "//", kinds: map[string]string{
		"function": "func", "function*": "func", "class": "type", "interface": "type",
		"type": "type", "enum": "type", "const": "const", "let": "var", "var": "var",
	}},
	"python": {style: styleIndent, decl: pyDecl, lineComment: "#", kinds: map[string]string{
		"def": "func", "class": "type",
	}},
	"rust": {style: styleBrace, decl: rustDecl, lineComment: "//", kinds: map[string]string{
		"fn": "func", "struct": "type", "enum": "type", "trait": "type", "impl": "type",
		"type": "type", "const": "const", "static": "var", "macro_rules!": "macro",
	}},
	"java":   {style: styleBrace, decl: jvmDecl, lineComment: "//", kinds: jvmKinds()},
	"kotlin": {style: styleBrace, decl: jvmDecl, lineComment: "//", kinds: jvmKinds()},
	"csharp": {style: styleBrace, decl: jvmDecl, lineComment: "//", kinds: jvmKinds()},
	"swift":  {style: styleBrace, decl: jvmDecl, lineComment: "//", kinds: jvmKinds()},
	"php": {style: styleBrace, decl: phpDecl, lineComment: "//", kinds: map[string]string{
		"function": "func", "class": "type", "interface": "type", "trait": "type", "enum": "type",
	}},
	"ruby": {style: styleIndent, decl: rubyDecl, lineComment: "#", kinds: map[string]string{
		"def": "func", "class": "type", "module": "type",
	}},
}

func jvmKinds() map[string]string {
	return map[string]string{
		"class": "type", "interface": "type", "enum": "type", "record": "type",
		"struct": "type", "object": "type", "protocol": "type", "extension": "type",
	}
}

func init() {
	// JavaScript shares TypeScript's rules; declaring it once avoids the two
	// drifting apart.
	rules["js"] = rules["ts"]
}

// SupportsSymbols reports whether a language has symbol extraction at all.
func SupportsSymbols(lang string) bool {
	if lang == "go" {
		return true
	}
	_, ok := rules[lang]
	return ok
}

// Symbols extracts declarations from a source file. Go is parsed properly;
// every other supported language is matched line by line.
//
// Calls are not resolved outside Go: matching call sites by name without scope
// analysis produces relations that look authoritative and are frequently wrong.
func Symbols(lang, rel, hash string, data []byte) ([]Symbol, error) {
	if lang == "go" {
		return GoSymbols(rel, hash, data)
	}
	rule, ok := rules[lang]
	if !ok {
		return nil, nil
	}
	if rule.style == styleIndent {
		return indentSymbols(rule, lang, rel, hash, data), nil
	}
	return braceSymbols(rule, rel, hash, data), nil
}

// line is one source line with the offsets needed to build a span.
type line struct {
	text  string
	start int // byte offset of the first character
	end   int // byte offset just past the newline
}

func splitLines(data []byte) []line {
	src := string(data)
	out := make([]line, 0, strings.Count(src, "\n")+1)
	offset := 0
	for offset <= len(src) {
		idx := strings.IndexByte(src[offset:], '\n')
		if idx < 0 {
			out = append(out, line{text: src[offset:], start: offset, end: len(src)})
			break
		}
		out = append(out, line{text: src[offset : offset+idx], start: offset, end: offset + idx + 1})
		offset += idx + 1
	}
	return out
}

// braceSymbols matches declarations and closes each one at its matching brace.
func braceSymbols(rule langRules, rel, hash string, data []byte) []Symbol {
	lines := splitLines(data)
	var out []Symbol

	for i, ln := range lines {
		match := rule.decl.FindStringSubmatch(ln.text)
		if match == nil {
			continue
		}
		kind, ok := rule.kinds[match[1]]
		if !ok {
			continue
		}
		// A value declaration only earns a symbol when it binds a name to
		// something; `const x` inside a for-header or a destructuring pattern
		// is not a declaration worth an entry.
		if kind == "const" || kind == "var" {
			if !strings.Contains(ln.text, "=") {
				continue
			}
		}

		endLine, endOffset := braceExtent(lines, i, rule.lineComment)
		span := &graph.Span{
			Path:  rel,
			Start: ln.start,
			End:   endOffset,
			L0:    i + 1,
			L1:    endLine + 1,
			Hash:  hash,
		}
		out = append(out, Symbol{Node: graph.SymNode(rel, match[2], signatureOf(ln.text), kind, span)})
	}
	return out
}

// braceExtent finds where a brace-delimited declaration ends. A declaration
// with no brace before its terminating semicolon is a one-liner such as a type
// alias, and ends on its own line.
func braceExtent(lines []line, start int, lineComment string) (int, int) {
	depth := 0
	opened := false

	for i := start; i < len(lines); i++ {
		code := stripTrailing(lines[i].text, lineComment)
		for _, r := range code {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
				if opened && depth <= 0 {
					return i, lines[i].end
				}
			}
		}
		if !opened && strings.HasSuffix(strings.TrimSpace(code), ";") {
			return i, lines[i].end
		}
		// A declaration that never opens a brace within a few lines is an
		// abstract member or a wrapped signature, not a block.
		if !opened && i-start > 3 {
			return start, lines[start].end
		}
	}
	return len(lines) - 1, lines[len(lines)-1].end
}

// indentSymbols handles languages whose blocks are bounded by indentation.
func indentSymbols(rule langRules, lang, rel, hash string, data []byte) []Symbol {
	lines := splitLines(data)
	var out []Symbol

	for i, ln := range lines {
		match := rule.decl.FindStringSubmatch(ln.text)
		if match == nil {
			continue
		}
		indent, keyword, name := match[1], match[2], match[3]
		kind, ok := rule.kinds[keyword]
		if !ok {
			continue
		}
		// A method is worth an entry; anything nested deeper is a closure or a
		// conditional definition, which no caller can reference by name.
		if len(strings.ReplaceAll(indent, "\t", "    ")) > 4 {
			continue
		}

		endLine, endOffset := indentExtent(lines, i, len(indent), lang)
		span := &graph.Span{
			Path:  rel,
			Start: ln.start,
			End:   endOffset,
			L0:    i + 1,
			L1:    endLine + 1,
			Hash:  hash,
		}
		out = append(out, Symbol{Node: graph.SymNode(rel, name, signatureOf(ln.text), kind, span)})
	}
	return out
}

// indentExtent runs to the last line that is still inside the block. Ruby
// closes blocks with `end` rather than by dedenting, so it is bounded by a
// matching `end` at the header's own indentation.
func indentExtent(lines []line, start, indent int, lang string) (int, int) {
	last := start
	for i := start + 1; i < len(lines); i++ {
		text := lines[i].text
		if strings.TrimSpace(text) == "" {
			continue
		}
		current := len(strings.ReplaceAll(leadingSpace(text), "\t", "    "))
		if lang == "ruby" {
			if current == indent && strings.TrimSpace(text) == "end" {
				return i, lines[i].end
			}
			last = i
			continue
		}
		if current <= indent {
			return last, lines[last].end
		}
		last = i
	}
	return last, lines[last].end
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// stripTrailing removes a line comment so a brace inside it is not counted.
// String literals are not tracked: a brace inside a string is rare in a
// declaration body and the cost of getting it wrong is a span that is too long,
// which the content hash still protects against serving as something else.
func stripTrailing(text, marker string) string {
	if marker == "" {
		return text
	}
	if idx := strings.Index(text, marker); idx >= 0 {
		return text[:idx]
	}
	return text
}

// signatureOf renders a declaration line as a signature: the header without its
// opening brace, collapsed and bounded.
func signatureOf(text string) string {
	sig := strings.Join(strings.Fields(text), " ")
	sig = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(sig, "{")), ":")
	sig = strings.TrimSpace(sig)
	if len(sig) > maxSignature {
		sig = sig[:maxSignature] + "…"
	}
	return sig
}
