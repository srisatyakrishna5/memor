package extract

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
)

// maxSectionSummary bounds how much of a section body is carried into the
// graph. The span points at the file, so the index only needs enough text to
// make the section findable.
const maxSectionSummary = 320

// topicPattern recognizes technology and domain words worth promoting to a
// first-class topic node. A closed list beats free-form keyword extraction:
// arbitrary words produce topics that match everything and rank nothing.
var topicPattern = regexp.MustCompile(`(?i)\b(python|golang|go|rust|typescript|javascript|node|react|vue|angular|svelte|docker|kubernetes|k8s|postgres|postgresql|mysql|sqlite|redis|auth|authentication|authorization|api|graphql|grpc|deploy|deployment|test|testing|ci|cd|aws|azure|gcp|git|security|performance|cache|caching|logging|migration|schema|mcp|llm|agent)\b`)

func (b *builder) scanDocs() {
	patterns := make([]string, 0, len(b.cfg.Knowledge.ScanPaths))
	for _, p := range b.cfg.Knowledge.ScanPaths {
		patterns = append(patterns, filepath.ToSlash(p))
	}

	excluded := make(map[string]struct{}, len(b.cfg.Graph.Exclude))
	for _, e := range b.cfg.Graph.Exclude {
		excluded[e] = struct{}{}
	}

	_ = filepath.WalkDir(b.root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if p == b.root {
				return nil
			}
			if _, skip := excluded[entry.Name()]; skip {
				return filepath.SkipDir
			}
			if strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".github" {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(b.root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !matchesAny(patterns, rel) {
			return nil
		}
		b.indexDoc(rel, p)
		return nil
	})
}

func (b *builder) indexDoc(rel, abs string) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	content := string(data)
	hash := graph.HashBytes(data)

	for _, sec := range chunkByHeading(content) {
		node := graph.DocNode(rel, sec.name, sec.summary)
		node.Span = &graph.Span{Path: rel, L0: sec.line, L1: sec.endLine, Hash: hash}
		b.addNode(node)
		b.attachTopics(node.ID, topicsIn(sec.name+" "+sec.summary))
	}
}

// attachTopics promotes tags to nodes and links them. Topics being first-class
// nodes is what lets a tag participate in the same graph walk as a file.
func (b *builder) attachTopics(nodeID string, tags []string) {
	for _, tag := range tags {
		topic := b.addNode(graph.TopicNode(tag))
		b.addEdge(nodeID, topic.ID, graph.EdgeTagged, 1)
	}
}

type docSection struct {
	name    string
	summary string
	line    int
	endLine int
}

// chunkByHeading splits markdown into sections at ## and ### headings. The
// document title (#) becomes the intro section so a file with no subheadings
// still produces one node.
func chunkByHeading(content string) []docSection {
	lines := strings.Split(content, "\n")

	var sections []docSection
	var name string
	var start int
	var body strings.Builder

	flush := func(end int) {
		if name == "" {
			return
		}
		summary := strings.Join(strings.Fields(body.String()), " ")
		if len(summary) > maxSectionSummary {
			summary = summary[:maxSectionSummary] + "..."
		}
		if summary != "" {
			sections = append(sections, docSection{
				name:    slugify(name),
				summary: summary,
				line:    start,
				endLine: end,
			})
		}
		body.Reset()
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "), strings.HasPrefix(trimmed, "### "):
			flush(i)
			name = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
			start = i + 1
		case strings.HasPrefix(trimmed, "# ") && name == "":
			name = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
			start = i + 1
		case name != "":
			body.WriteString(line)
			body.WriteByte(' ')
		}
	}
	flush(len(lines))
	return sections
}

func topicsIn(text string) []string {
	matches := topicPattern.FindAllString(strings.ToLower(text), -1)
	return dedupe(matches)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && sb.Len() > 0 {
				sb.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(sb.String(), "-")
}

func matchesAny(patterns []string, rel string) bool {
	for _, pattern := range patterns {
		if matchPath(pattern, rel) {
			return true
		}
	}
	return false
}

// matchPath supports ** as a multi-segment wildcard, which path.Match does not.
func matchPath(pattern, filePath string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(filePath, "/"), "/")

	var match func(int, int) bool
	match = func(pi, fi int) bool {
		if pi == len(patternParts) {
			return fi == len(pathParts)
		}
		if patternParts[pi] == "**" {
			return match(pi+1, fi) || (fi < len(pathParts) && match(pi, fi+1))
		}
		if fi == len(pathParts) {
			return false
		}
		ok, err := path.Match(patternParts[pi], pathParts[fi])
		return err == nil && ok && match(pi+1, fi+1)
	}
	return match(0, 0)
}
