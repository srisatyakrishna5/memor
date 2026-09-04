package extract

import (
	"strings"
	"unicode"
)

// maxPurpose caps a one-line file purpose. The manifest lists every file in the
// repository, so a few wasted words per line becomes thousands of wasted tokens.
const maxPurpose = 120

// Purpose derives a one-line description of a file from its leading comment or
// docstring. It is deliberately shallow: a wrong summary is worse than none, so
// anything that is not obviously a description of the file returns "".
//
// An agent can always overwrite this with `memor remember --summary`, and that
// value wins on the next build.
func Purpose(lang string, data []byte) string {
	src := string(data)
	var raw string
	switch lang {
	case "go":
		raw = leadingLineComment(src, "//")
	case "js", "ts", "java", "csharp", "rust", "kotlin", "swift", "c", "php":
		if block := leadingBlockComment(src); block != "" {
			raw = block
		} else {
			raw = leadingLineComment(src, "//")
		}
	case "python", "ruby":
		if doc := leadingDocstring(src); doc != "" {
			raw = doc
		} else {
			raw = leadingLineComment(src, "#")
		}
	default:
		return ""
	}
	return firstSentence(raw)
}

// leadingLineComment collects the first comment paragraph, skipping blank
// lines, shebangs and build directives above it. A file whose comment opens
// with a filename banner falls through to the paragraph below it, because the
// banner repeats the path the manifest already prints.
func leadingLineComment(src, marker string) string {
	var paragraphs []string
	var current []string
	started := false

	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, strings.Join(current, " "))
			current = nil
		}
	}

scan:
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		hasContent := len(paragraphs) > 0 || len(current) > 0
		switch {
		case strings.HasPrefix(trimmed, marker):
			started = true
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
			if body == "" {
				flush()
				continue
			}
			if isDirective(trimmed) {
				continue
			}
			current = append(current, body)
		case trimmed == "", strings.HasPrefix(trimmed, "#!"):
			if started && hasContent {
				break scan
			}
		default:
			break scan
		}
		if len(paragraphs) >= 2 {
			break
		}
	}
	flush()

	return bestParagraph(paragraphs)
}

// bestParagraph picks the first paragraph that reads like a description.
// A short leading heading such as "build.go — memor build" is a title, not a
// summary, so the paragraph below it wins when there is one.
func bestParagraph(paragraphs []string) string {
	var fallback string
	for _, p := range paragraphs {
		text := stripBanner(p)
		if text == "" {
			continue
		}
		if len(strings.Fields(text)) >= 4 {
			return text
		}
		if fallback == "" {
			fallback = text
		}
	}
	return fallback
}

// stripBanner removes a leading "file.go — command" style heading. It returns
// "" when the whole line was the heading, so the caller tries the next
// paragraph instead of describing a file by its own name.
func stripBanner(s string) string {
	for _, sep := range []string{" — ", " - ", " – "} {
		head, rest, ok := strings.Cut(s, sep)
		if !ok || len(strings.Fields(head)) > 3 {
			continue
		}
		if !strings.Contains(head, ".") {
			continue
		}
		return strings.TrimSpace(rest)
	}
	if fields := strings.Fields(s); len(fields) <= 3 && strings.Contains(s, ".") &&
		!strings.HasSuffix(strings.TrimSpace(s), ".") {
		// A bare "build.go" heading with nothing after it.
		return ""
	}
	return strings.TrimSpace(s)
}

// isDirective matches compiler pragmas such as //go:build and // eslint-disable,
// which are instructions rather than descriptions.
func isDirective(line string) bool {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "//"), "#"))
	if body == "" {
		return false
	}
	if strings.HasPrefix(body, "go:") || strings.HasPrefix(body, "+build") {
		return true
	}
	for _, prefix := range []string{"eslint", "prettier", "ts-", "@ts-", "nolint", "coding:", "-*-"} {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}
	return false
}

func leadingBlockComment(src string) string {
	trimmed := strings.TrimLeftFunc(src, unicode.IsSpace)
	if !strings.HasPrefix(trimmed, "/*") {
		return ""
	}
	end := strings.Index(trimmed, "*/")
	if end < 0 {
		return ""
	}
	var parts []string
	for _, line := range strings.Split(trimmed[2:end], "\n") {
		parts = append(parts, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*")))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func leadingDocstring(src string) string {
	trimmed := strings.TrimLeftFunc(src, unicode.IsSpace)
	for _, quote := range []string{`"""`, "'''"} {
		if !strings.HasPrefix(trimmed, quote) {
			continue
		}
		body := trimmed[len(quote):]
		end := strings.Index(body, quote)
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(strings.ReplaceAll(body[:end], "\n", " "))
	}
	return ""
}

// firstSentence keeps the opening statement and drops the rest. A package
// comment can run for paragraphs; the manifest only has room for its claim.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	// Strip a leading "Package foo " or "Module foo " so the line carries
	// information the file path does not already give.
	for _, prefix := range []string{"Package ", "package ", "Module ", "Copyright ", "SPDX"} {
		if strings.HasPrefix(s, prefix) {
			if prefix == "Copyright " || prefix == "SPDX" {
				return ""
			}
			if _, rest, ok := strings.Cut(strings.TrimPrefix(s, prefix), " "); ok {
				s = rest
			}
			break
		}
	}
	if idx := strings.Index(s, ". "); idx > 0 {
		s = s[:idx]
	}
	s = strings.TrimRight(s, ".")
	if len(s) > maxPurpose {
		s = strings.TrimSpace(s[:maxPurpose])
		if cut := strings.LastIndex(s, " "); cut > maxPurpose/2 {
			s = s[:cut]
		}
	}
	return s
}
