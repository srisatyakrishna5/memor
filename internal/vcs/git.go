// Package vcs reads repository state from git.
//
// git already knows precisely what changed and when; memor's job is only to
// remember which of those commits it has already shown an agent. Nothing here
// mutates the repository.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrNoGit is returned when the directory is not a git work tree, or git is not
// installed. Callers fall back to content hashing.
var ErrNoGit = errors.New("not a git repository")

// commandTimeout bounds every git invocation so a hung or prompting git can
// never wedge an MCP tool call.
const commandTimeout = 5 * time.Second

// shaPattern guards every value interpolated into a git argument. Revisions
// come from state.json and marks.jsonl, which an agent can influence, so they
// are validated as hex object names before use rather than trusted.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ValidSHA reports whether s is a plain hex object name.
func ValidSHA(s string) bool { return shaPattern.MatchString(s) }

// Status classifies how a path changed.
type Status string

const (
	StatusAdded     Status = "added"
	StatusModified  Status = "modified"
	StatusDeleted   Status = "deleted"
	StatusRenamed   Status = "renamed"
	StatusUntracked Status = "untracked"
)

// Change is one changed path.
type Change struct {
	Path   string `json:"path"`
	Status Status `json:"status"`
}

// Head describes the current commit.
type Head struct {
	SHA    string
	Branch string
}

// Available reports whether root is inside a git work tree.
func Available(root string) bool {
	out, err := run(root, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// ReadHead returns the current commit and branch. A repository with no commits
// yet reports ErrNoGit rather than a fabricated empty SHA.
func ReadHead(root string) (Head, error) {
	sha, err := run(root, "rev-parse", "HEAD")
	if err != nil {
		return Head{}, err
	}
	sha = strings.TrimSpace(sha)
	if !ValidSHA(sha) {
		return Head{}, ErrNoGit
	}
	branch, err := run(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Head{SHA: sha}, nil
	}
	return Head{SHA: sha, Branch: strings.TrimSpace(branch)}, nil
}

// Dirty returns uncommitted changes in the work tree, including untracked
// files. These are what an agent is most likely to be asked about.
func Dirty(root string) ([]Change, error) {
	out, err := run(root, "status", "--porcelain=v1", "--untracked-files=all", "-z")
	if err != nil {
		return nil, err
	}
	return parsePorcelain(out), nil
}

// ChangedSince returns paths that differ between sha and the working tree.
// An unknown or unreachable sha yields an error so the caller can fall back to
// a full report instead of silently claiming nothing changed.
func ChangedSince(root, sha string) ([]Change, error) {
	if !ValidSHA(sha) {
		return nil, errors.New("invalid revision")
	}
	out, err := run(root, "diff", "--name-status", "-z", sha)
	if err != nil {
		return nil, err
	}
	committed := parseNameStatus(out)

	untracked, err := Dirty(root)
	if err != nil {
		return committed, nil
	}
	return merge(committed, untracked), nil
}

// CommitsBetween counts commits from sha to HEAD.
func CommitsBetween(root, sha string) (int, error) {
	if !ValidSHA(sha) {
		return 0, errors.New("invalid revision")
	}
	out, err := run(root, "rev-list", "--count", sha+"..HEAD")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, r := range strings.TrimSpace(out) {
		if r < '0' || r > '9' {
			return 0, errors.New("unexpected rev-list output")
		}
		count = count*10 + int(r-'0')
	}
	return count, nil
}

// parsePorcelain reads `git status --porcelain=v1 -z`. Records are NUL
// separated; a rename record is followed by an extra NUL-terminated old path.
func parsePorcelain(out string) []Change {
	fields := strings.Split(out, "\x00")
	var changes []Change
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		code, path := entry[:2], entry[3:]
		if strings.ContainsAny(code, "R") {
			i++ // consume the original path
		}
		changes = append(changes, Change{Path: normalize(path), Status: statusFromPorcelain(code)})
	}
	return changes
}

func statusFromPorcelain(code string) Status {
	switch {
	case code == "??":
		return StatusUntracked
	case strings.ContainsAny(code, "R"):
		return StatusRenamed
	case strings.ContainsAny(code, "A"):
		return StatusAdded
	case strings.ContainsAny(code, "D"):
		return StatusDeleted
	default:
		return StatusModified
	}
}

// parseNameStatus reads `git diff --name-status -z`, where a status field and
// its path are separate NUL-terminated records and a rename carries two paths.
func parseNameStatus(out string) []Change {
	fields := strings.Split(out, "\x00")
	var changes []Change
	for i := 0; i < len(fields); i++ {
		code := fields[i]
		if code == "" {
			continue
		}
		if code[0] == 'R' || code[0] == 'C' {
			if i+2 >= len(fields) {
				break
			}
			changes = append(changes, Change{Path: normalize(fields[i+2]), Status: StatusRenamed})
			i += 2
			continue
		}
		if i+1 >= len(fields) {
			break
		}
		changes = append(changes, Change{Path: normalize(fields[i+1]), Status: statusFromCode(code[0])})
		i++
	}
	return changes
}

func statusFromCode(c byte) Status {
	switch c {
	case 'A':
		return StatusAdded
	case 'D':
		return StatusDeleted
	default:
		return StatusModified
	}
}

// merge combines two change lists, letting the later list win on conflict.
func merge(base, extra []Change) []Change {
	index := make(map[string]int, len(base))
	out := make([]Change, 0, len(base)+len(extra))
	for _, c := range base {
		index[c.Path] = len(out)
		out = append(out, c)
	}
	for _, c := range extra {
		if i, ok := index[c.Path]; ok {
			out[i] = c
			continue
		}
		index[c.Path] = len(out)
		out = append(out, c)
	}
	return out
}

func normalize(path string) string {
	return filepath.ToSlash(strings.Trim(strings.TrimSpace(path), `"`))
}

// run executes git with an argument vector. There is no shell involved, so a
// path or revision can never be interpreted as a command.
func run(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	// Suppress credential and editor prompts; a blocked git is worse than a
	// missing answer.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", ErrNoGit
	}
	return stdout.String(), nil
}
