package engine

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// KnowledgeDB represents the indexed knowledge base.
type KnowledgeDB struct {
	Version   string
	IndexedAt time.Time
	Docs      []KnowledgeDoc
}

// KnowledgeDoc represents a single indexed document.
type KnowledgeDoc struct {
	Name     string
	Tags     []string
	Source   string // file path
	Hash     string // sha256 of source file
	Sections []KnowledgeSection
}

// KnowledgeSection is a section within a document.
type KnowledgeSection struct {
	Name    string
	Summary string
}

// LoadKnowledgeDB reads the knowledge.db file.
func LoadKnowledgeDB(path string) (*KnowledgeDB, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &KnowledgeDB{Version: "1"}, nil
		}
		return nil, err
	}
	defer f.Close()

	kb := &KnowledgeDB{Version: "1"}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	headerRegex := regexp.MustCompile(`^@knowledge v(\S+)(?:.*indexed:(\S+))?`)
	docRegex := regexp.MustCompile(`^@doc\s+(\S+)\s*((?:#\S+\s*)*)\[(\d+)\s+sections?\]$`)
	sectionRegex := regexp.MustCompile(`^\s+::\s+(\S+):\s+(.+)$`)

	var currentDoc *KnowledgeDoc

	for scanner.Scan() {
		line := scanner.Text()

		if m := headerRegex.FindStringSubmatch(line); m != nil {
			kb.Version = m[1]
			if len(m) > 2 && m[2] != "" {
				kb.IndexedAt, _ = time.Parse(time.RFC3339, m[2])
			}
			continue
		}

		if m := docRegex.FindStringSubmatch(line); m != nil {
			doc := KnowledgeDoc{
				Name: m[1],
				Tags: parseSectionTags(m[2]),
			}
			kb.Docs = append(kb.Docs, doc)
			currentDoc = &kb.Docs[len(kb.Docs)-1]
			continue
		}

		if m := sectionRegex.FindStringSubmatch(line); m != nil && currentDoc != nil {
			currentDoc.Sections = append(currentDoc.Sections, KnowledgeSection{
				Name:    m[1],
				Summary: m[2],
			})
			continue
		}

		if currentDoc != nil && strings.HasPrefix(line, "  source: ") {
			currentDoc.Source = strings.TrimPrefix(line, "  source: ")
			continue
		}
		if currentDoc != nil && strings.HasPrefix(line, "  hash: ") {
			currentDoc.Hash = strings.TrimPrefix(line, "  hash: ")
			continue
		}
	}

	return kb, scanner.Err()
}

// WriteKnowledgeDB writes the knowledge.db index file.
func WriteKnowledgeDB(path string, kb *KnowledgeDB) error {
	var sb strings.Builder
	now := time.Now().UTC().Format(time.RFC3339)

	totalSections := 0
	for _, doc := range kb.Docs {
		totalSections += len(doc.Sections)
	}

	sb.WriteString(fmt.Sprintf("@knowledge v1 | %d docs | %d sections | indexed:%s\n\n",
		len(kb.Docs), totalSections, now))

	for _, doc := range kb.Docs {
		tags := renderSectionTags(doc.Tags)
		sb.WriteString(fmt.Sprintf("@doc %s %s [%d sections]\n", doc.Name, tags, len(doc.Sections)))
		if doc.Source != "" {
			sb.WriteString(fmt.Sprintf("  source: %s\n", doc.Source))
		}
		if doc.Hash != "" {
			sb.WriteString(fmt.Sprintf("  hash: %s\n", doc.Hash))
		}
		for _, sec := range doc.Sections {
			sb.WriteString(fmt.Sprintf("  :: %s: %s\n", sec.Name, sec.Summary))
		}
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// IndexDocument chunks a markdown/text file by ## headings and adds it to the knowledge DB.
func IndexDocument(kb *KnowledgeDB, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read document: %w", err)
	}

	content := string(data)
	hash := fileHash(data)

	// Check if already indexed with same hash
	baseName := docName(filePath)
	cleanPath := filepath.Clean(filePath)
	for i, doc := range kb.Docs {
		if filepath.Clean(doc.Source) == cleanPath || doc.Source == "" && doc.Name == baseName {
			if doc.Hash == hash {
				return nil // unchanged
			}
			// Remove old version, re-index
			kb.Docs = append(kb.Docs[:i], kb.Docs[i+1:]...)
			break
		}
	}

	sections := chunkByHeading(content)
	tags := extractTags(content)

	doc := KnowledgeDoc{
		Name:     baseName,
		Tags:     tags,
		Source:   cleanPath,
		Hash:     hash,
		Sections: sections,
	}

	kb.Docs = append(kb.Docs, doc)
	return nil
}

// ScanKnowledgePaths discovers and indexes files matching known patterns.
func ScanKnowledgePaths(kb *KnowledgeDB, projectRoot string, patterns []string) (int, error) {
	indexed := 0
	err := filepath.WalkDir(projectRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".memor", ".venv", "node_modules":
				if filePath != projectRoot {
					return filepath.SkipDir
				}
			}
			return nil
		}

		relativePath, err := filepath.Rel(projectRoot, filePath)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)
		for _, pattern := range patterns {
			if !matchKnowledgePath(filepath.ToSlash(pattern), relativePath) {
				continue
			}
			if err := IndexDocument(kb, filePath); err != nil {
				fmt.Fprintf(os.Stderr, "memor: warning: could not index %s: %v\n", filePath, err)
				return nil
			}
			indexed++
			break
		}
		return nil
	})
	return indexed, err
}

func matchKnowledgePath(pattern, filePath string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(filePath, "/"), "/")
	var match func(int, int) bool
	match = func(patternIndex, pathIndex int) bool {
		if patternIndex == len(patternParts) {
			return pathIndex == len(pathParts)
		}
		if patternParts[patternIndex] == "**" {
			return match(patternIndex+1, pathIndex) || pathIndex < len(pathParts) && match(patternIndex, pathIndex+1)
		}
		if pathIndex == len(pathParts) {
			return false
		}
		matched, err := path.Match(patternParts[patternIndex], pathParts[pathIndex])
		return err == nil && matched && match(patternIndex+1, pathIndex+1)
	}
	return match(0, 0)
}

// RefreshKnowledge re-indexes changed files by comparing SHA-256 hashes.
func RefreshKnowledge(kb *KnowledgeDB) (refreshed int, stale int, err error) {
	for i := 0; i < len(kb.Docs); i++ {
		doc := &kb.Docs[i]
		if doc.Source == "" {
			continue
		}

		data, err := os.ReadFile(doc.Source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "memor: warning: source missing for %s: %v\n", doc.Name, err)
			stale++
			continue
		}

		newHash := fileHash(data)
		if newHash != doc.Hash {
			content := string(data)
			doc.Sections = chunkByHeading(content)
			doc.Tags = extractTags(content)
			doc.Hash = newHash
			refreshed++
		}
	}
	return refreshed, stale, nil
}

// chunkByHeading splits markdown content by ## headings into sections.
func chunkByHeading(content string) []KnowledgeSection {
	var sections []KnowledgeSection
	lines := strings.Split(content, "\n")

	var currentName string
	var currentBody strings.Builder

	flushSection := func() {
		if currentName != "" {
			summary := strings.TrimSpace(currentBody.String())
			// Truncate summary to first 200 chars for the index
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}
			sections = append(sections, KnowledgeSection{
				Name:    slugify(currentName),
				Summary: summary,
			})
		}
		currentBody.Reset()
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flushSection()
			currentName = strings.TrimPrefix(line, "## ")
		} else if currentName != "" {
			currentBody.WriteString(line)
			currentBody.WriteString(" ")
		}
	}
	flushSection()

	return sections
}

// extractTags pulls potential tags from content (lowercase words appearing frequently).
func extractTags(content string) []string {
	// Simple heuristic: extract words that look like technology/topic names
	// from headings and first-level content
	tagPatterns := regexp.MustCompile(`(?i)\b(python|go|rust|typescript|javascript|node|react|vue|angular|docker|k8s|kubernetes|postgres|mysql|redis|auth|api|deploy|test|ci|cd|aws|azure|gcp|git)\b`)
	matches := tagPatterns.FindAllString(strings.ToLower(content), -1)

	seen := make(map[string]struct{})
	var tags []string
	for _, m := range matches {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			tags = append(tags, m)
		}
	}
	return tags
}

func fileHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func docName(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return strings.ToLower(name)
}

func slugify(s string) string {
	s = strings.ToLower(s)
	reg := regexp.MustCompile(`[^a-z0-9]+`)
	s = reg.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func parseSectionTags(s string) []string {
	var tags []string
	for _, part := range strings.Fields(s) {
		tag := strings.TrimPrefix(part, "#")
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func renderSectionTags(tags []string) string {
	var parts []string
	for _, t := range tags {
		parts = append(parts, "#"+t)
	}
	return strings.Join(parts, " ")
}
