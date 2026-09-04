package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Mark is one agent's watermark: the last repository state it was shown.
//
// This is what turns "what changed?" into a bounded answer. Without it every
// session starts from zero knowledge and re-reads the repository, which is the
// exact cost memor exists to remove.
//
// Commit alone is not enough. Uncommitted work is the normal state of a working
// tree, and an agent that is told about the same forty dirty files every visit
// has learned nothing from the second visit onwards. Tree records the content
// hash of each path the agent was shown, so the next visit can separate what is
// genuinely new from what it has already seen.
type Mark struct {
	Agent  string            `json:"agent"`
	Commit string            `json:"commit,omitempty"`
	At     int64             `json:"at"`
	Tree   map[string]string `json:"tree,omitempty"` // changed path -> content hash when shown
}

// MaxTrackedPaths bounds a watermark's size. Past it, paths fall out and are
// reported as new again next visit \u2014 over-reporting, which is the safe failure.
const MaxTrackedPaths = 500

// Seen reports whether the agent was already shown this path at this content.
// A path the mark has never held is unseen even when both hashes are empty, so
// a newly deleted file is still news.
func (m Mark) Seen(path, hash string) bool {
	if m.Tree == nil {
		return false
	}
	prev, ok := m.Tree[path]
	return ok && prev == hash
}

// DefaultAgent labels callers that do not identify themselves.
const DefaultAgent = "default"

// NormalizeAgent constrains an agent label to a short, filesystem-safe token.
// The value arrives from an MCP client, so it is sanitized rather than trusted.
func NormalizeAgent(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return DefaultAgent
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return DefaultAgent
	}
	return out
}

// ReadMarks loads every watermark, keyed by agent. A malformed line is skipped:
// a corrupt watermark must degrade to "show everything", never to an error.
func ReadMarks(path string) map[string]Mark {
	marks := make(map[string]Mark)
	data, err := os.ReadFile(path)
	if err != nil {
		return marks
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m Mark
		if json.Unmarshal([]byte(line), &m) != nil || m.Agent == "" {
			continue
		}
		marks[m.Agent] = m
	}
	return marks
}

// ReadMark returns one agent's watermark.
func ReadMark(path, agent string) (Mark, bool) {
	m, ok := ReadMarks(path)[NormalizeAgent(agent)]
	return m, ok
}

// WriteMark records an agent's watermark, replacing any previous one. The file
// is rewritten rather than appended so it stays one line per agent and never
// needs its own compaction pass.
func WriteMark(path, lockPath string, m Mark) error {
	lock, err := AcquireLock(lockPath, WriteLockTimeout)
	if err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer lock.Release()

	marks := ReadMarks(path)
	m.Agent = NormalizeAgent(m.Agent)
	marks[m.Agent] = m

	agents := make([]string, 0, len(marks))
	for agent := range marks {
		agents = append(agents, agent)
	}
	sort.Strings(agents)

	var buf strings.Builder
	for _, agent := range agents {
		line, err := json.Marshal(marks[agent])
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return WriteFileAtomic(path, []byte(buf.String()))
}
