package session

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/store"
	"github.com/memor-dev/memor/internal/vcs"
)

// Change is one repository path that moved, annotated with what memor knows
// about it. Purpose is what makes the ledger useful on its own: an agent can
// decide whether a change is relevant without opening the file.
type Change struct {
	Path    string     `json:"path"`
	Status  vcs.Status `json:"status"`
	Purpose string     `json:"purpose,omitempty"`
	Indexed bool       `json:"indexed"`
	// Seen marks a path the caller was already shown at this exact content.
	// Uncommitted work stays in the change list indefinitely, so without this an
	// agent cannot tell a new edit from one it read about last session.
	Seen bool `json:"seen,omitempty"`

	hash string // content hash now, recorded into the watermark
}

// ChangeSet answers "what moved since I last looked?".
type ChangeSet struct {
	Since     string   `json:"since,omitempty"` // commit the comparison started from
	Head      string   `json:"head,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Behind    int      `json:"commits,omitempty"` // commits between Since and HEAD
	Changes   []Change `json:"changes"`
	Unseen    int      `json:"unseen"` // changes the caller has not been shown before
	Truncated int      `json:"truncated,omitempty"`
	Source    string   `json:"source"` // "git" or "hash"
}

// Changes reports what differs from a reference point.
//
// The reference is the caller's revision if given, otherwise the commit the
// last build indexed. Outside a git work tree it falls back to comparing stored
// file hashes against disk, so the answer degrades in precision but never in
// availability.
func (s *Session) Changes(agent, since string, limit int) (ChangeSet, error) {
	g, err := graph.Load(s.Paths)
	if err != nil {
		return ChangeSet{}, err
	}
	mark, _ := store.ReadMark(s.Paths.Marks, agent)

	set := s.changesFrom(g, since)
	s.classify(&set, mark)
	truncate(&set, limit)
	return set, nil
}

func (s *Session) changesFrom(g *graph.Graph, since string) ChangeSet {
	set := ChangeSet{Source: "git"}

	if since == "" {
		since = store.ReadState(s.Paths.State).IndexedCommit
	}
	if head, err := vcs.ReadHead(s.Root); err == nil {
		set.Head = head.SHA
		set.Branch = head.Branch
	}

	var raw []vcs.Change
	if set.Head != "" && vcs.ValidSHA(since) {
		if changes, err := vcs.ChangedSince(s.Root, since); err == nil {
			raw = changes
			set.Since = since
			if n, err := vcs.CommitsBetween(s.Root, since); err == nil {
				set.Behind = n
			}
		}
	} else if set.Head != "" {
		if changes, err := vcs.Dirty(s.Root); err == nil {
			raw = changes
		}
	}

	if set.Head == "" {
		raw = s.staleFiles(g)
		set.Source = "hash"
	}

	set.Changes = s.annotate(g, raw)
	sort.Slice(set.Changes, func(i, j int) bool { return set.Changes[i].Path < set.Changes[j].Path })
	return set
}

// classify marks each change against the caller's watermark and orders the
// unseen ones first, so a truncated list drops what the agent already knows
// rather than what is new to it.
func (s *Session) classify(set *ChangeSet, mark store.Mark) {
	for i := range set.Changes {
		c := &set.Changes[i]
		c.Seen = mark.Seen(c.Path, c.hash)
		if !c.Seen {
			set.Unseen++
		}
	}
	sort.SliceStable(set.Changes, func(i, j int) bool {
		if set.Changes[i].Seen != set.Changes[j].Seen {
			return !set.Changes[i].Seen
		}
		return set.Changes[i].Path < set.Changes[j].Path
	})
}

func truncate(set *ChangeSet, limit int) {
	if limit > 0 && len(set.Changes) > limit {
		set.Truncated = len(set.Changes) - limit
		set.Changes = set.Changes[:limit]
	}
}

// staleFiles is the non-git fallback: every indexed file whose content no
// longer matches the hash recorded for it.
func (s *Session) staleFiles(g *graph.Graph) []vcs.Change {
	var out []vcs.Change
	for _, f := range g.NodesOfKind(graph.KindFile) {
		switch graph.FileStatus(s.Root, f) {
		case graph.StatusStale:
			out = append(out, vcs.Change{Path: f.Name, Status: vcs.StatusModified})
		case graph.StatusMissing:
			out = append(out, vcs.Change{Path: f.Name, Status: vcs.StatusDeleted})
		}
	}
	return out
}

// annotate attaches what the store knows about each changed path and drops the
// ones it deliberately does not index, so a lockfile churn never fills the
// ledger an agent reads first.
func (s *Session) annotate(g *graph.Graph, raw []vcs.Change) []Change {
	out := make([]Change, 0, len(raw))
	for _, c := range raw {
		if c.Path == "" || s.ignored(c.Path) {
			continue
		}
		change := Change{Path: c.Path, Status: c.Status, hash: s.contentHash(c.Path)}
		if node, ok := g.FindFile(c.Path); ok {
			change.Indexed = true
			change.Purpose = fileSummary(node)
		}
		out = append(out, change)
	}
	return out
}

// contentHash returns the current hash of a path, or "" if it is gone.
func (s *Session) contentHash(rel string) string {
	hash, _, err := graph.FileHashAndLOC(filepath.Join(s.Root, rel))
	if err != nil {
		return ""
	}
	return hash
}

// ignored reports whether a path is outside what memor indexes at all.
func (s *Session) ignored(rel string) bool {
	if strings.HasPrefix(rel, store.DirName+"/") {
		return true
	}
	for _, dir := range s.Cfg.Graph.Exclude {
		if rel == dir || strings.HasPrefix(rel, dir+"/") || strings.Contains(rel, "/"+dir+"/") {
			return true
		}
	}
	ext := strings.ToLower(filepath.Ext(rel))
	for _, allowed := range s.Cfg.Graph.Extensions {
		if ext == allowed {
			return false
		}
	}
	// Prose is not in Graph.Extensions but is indexed as documentation.
	return ext != ".md"
}

func fileSummary(n *graph.Node) string {
	if n.Text != "" {
		return n.Text
	}
	return n.MetaValue(graph.MetaPurpose)
}

// Brief is the cheapest complete answer to "where am I?": what this repository
// is, what moved since this agent last looked, and what it was doing.
type Brief struct {
	Name       string     `json:"name"`
	Root       string     `json:"root"`
	Languages  []string   `json:"languages,omitempty"`
	Files      int        `json:"files"`
	Symbols    int        `json:"symbols"`
	Memories   int        `json:"memories"`
	Head       string     `json:"head,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Indexed    string     `json:"indexed_commit,omitempty"`
	IndexedAt  int64      `json:"indexed_at,omitempty"`
	Stale      bool       `json:"stale"`
	FirstVisit bool       `json:"first_visit"`
	Layout     []DirEntry `json:"layout,omitempty"`
	Changes    ChangeSet  `json:"changes"`
	Journal    []Entry    `json:"journal,omitempty"`
	Advice     string     `json:"advice,omitempty"`
}

// DirEntry is one top-level directory and how much of the repository is in it.
type DirEntry struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

// Entry is one journal record.
type Entry struct {
	ID      string   `json:"id"`
	Task    string   `json:"task,omitempty"`
	Status  string   `json:"status,omitempty"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
	At      int64    `json:"at"`
}

// briefChangeLimit caps how many changed paths the brief carries, and
// briefPurposeChars caps the description on each. Past these points the honest
// answer is "call repo_changes", not a longer brief — the brief only has to be
// good enough to decide where to look.
const (
	briefChangeLimit  = 20
	briefPurposeChars = 70
)

// BriefOptions tunes what the brief reports.
type BriefOptions struct {
	Agent   string
	Journal int  // maximum open journal entries to include
	Advance bool // record this reading as the agent's new watermark
}

// Brief assembles the session-opening summary and advances the caller's
// watermark, so the next call reports only what happened after this one.
func (s *Session) Brief(opts BriefOptions) (Brief, error) {
	g, err := graph.Load(s.Paths)
	if err != nil {
		return Brief{}, err
	}

	agent := store.NormalizeAgent(opts.Agent)
	mark, seen := store.ReadMark(s.Paths.Marks, agent)
	state := store.ReadState(s.Paths.State)

	counts := g.CountByKind()
	b := Brief{
		Name:       filepath.Base(s.Root),
		Root:       s.Root,
		Files:      counts[graph.KindFile.String()],
		Symbols:    counts[graph.KindSym.String()],
		Memories:   counts[graph.KindMem.String()],
		Indexed:    state.IndexedCommit,
		IndexedAt:  state.IndexedAt,
		FirstVisit: !seen,
		Languages:  languages(g),
		Layout:     layout(g),
		Journal:    openJournal(g, opts.Journal),
	}

	// An agent that has been here before wants the delta from its own last
	// visit, not from whenever the index was built.
	from := mark.Commit
	if from == "" {
		from = state.IndexedCommit
	}

	// Classification runs over the complete set before truncation, so the
	// watermark records everything the agent could act on rather than only the
	// first twenty paths.
	full := s.changesFrom(g, from)
	s.classify(&full, mark)

	if opts.Advance {
		s.advance(agent, full)
	}

	b.Changes = full
	truncate(&b.Changes, briefChangeLimit)
	for i := range b.Changes.Changes {
		c := &b.Changes.Changes[i]
		// A path the agent has already been shown needs only its name; repeating
		// its description every session is the cost this watermark exists to cut.
		if c.Seen {
			c.Purpose = ""
			continue
		}
		c.Purpose = trim(c.Purpose, briefPurposeChars)
	}
	b.Head = full.Head
	b.Branch = full.Branch

	report, err := graph.Status(s.Paths, s.Root, g, s.Cfg)
	if err == nil {
		b.Stale = report.NeedsRebuild()
	}
	b.Advice = advise(b)
	return b, nil
}

// advance records what this brief showed, so the next one can tell the agent
// what is new rather than repeating itself. It is written even without a commit:
// outside git the per-path hashes carry the whole signal.
func (s *Session) advance(agent string, set ChangeSet) {
	tree := make(map[string]string, len(set.Changes))
	for i, c := range set.Changes {
		if i >= store.MaxTrackedPaths {
			break
		}
		tree[c.Path] = c.hash
	}
	_ = store.WriteMark(s.Paths.Marks, s.Paths.Lock, store.Mark{
		Agent:  agent,
		Commit: set.Head,
		At:     time.Now().Unix(),
		Tree:   tree,
	})
}

func advise(b Brief) string {
	switch {
	case b.Files == 0:
		return "Store is empty. Run `memor build` to index this repository."
	case b.Stale:
		return "Index has drifted from the working tree. Run `memor build` before trusting file spans."
	case b.Changes.Unseen == 0 && !b.FirstVisit:
		return "Nothing has changed since your last visit. Reuse what you already know instead of re-reading the code."
	case b.Changes.Truncated > 0:
		return "Too many files changed to list here. Call repo_changes for the full set before reading anything."
	case b.Changes.Unseen > 0:
		return "Read only the changed files listed above; use repo_map or symbol_find for anything else."
	default:
		return "Use repo_map for a task-scoped view instead of reading files directly."
	}
}

func trim(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = strings.TrimSpace(s[:max])
	if cut := strings.LastIndex(s, " "); cut > max/2 {
		s = s[:cut]
	}
	return s + "…"
}

func languages(g *graph.Graph) []string {
	counts := make(map[string]int)
	for _, f := range g.NodesOfKind(graph.KindFile) {
		if lang := f.MetaValue(graph.MetaLang); lang != "" && lang != "text" {
			counts[lang]++
		}
	}
	langs := make([]string, 0, len(counts))
	for lang := range counts {
		langs = append(langs, lang)
	}
	sort.Slice(langs, func(i, j int) bool {
		if counts[langs[i]] != counts[langs[j]] {
			return counts[langs[i]] > counts[langs[j]]
		}
		return langs[i] < langs[j]
	})
	if len(langs) > 5 {
		langs = langs[:5]
	}
	return langs
}

// layout summarizes the repository by top-level directory. A full tree is the
// single largest thing an agent would otherwise pull in, and the top level is
// enough to decide where to look next.
func layout(g *graph.Graph) []DirEntry {
	counts := make(map[string]int)
	for _, f := range g.NodesOfKind(graph.KindFile) {
		top, _, ok := strings.Cut(f.Name, "/")
		if !ok {
			top = "."
		}
		counts[top]++
	}
	out := make([]DirEntry, 0, len(counts))
	for dir, n := range counts {
		out = append(out, DirEntry{Path: dir, Files: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// openJournal returns the most recent unfinished work, newest first.
func openJournal(g *graph.Graph, limit int) []Entry {
	if limit == 0 {
		limit = 5
	}
	var out []Entry
	for _, m := range g.NodesOfKind(graph.KindMem) {
		status := m.MetaValue(graph.MetaStatus)
		if status == "" || status == graph.StatusDone {
			continue
		}
		out = append(out, Entry{
			ID:      m.ID,
			Task:    m.MetaValue(graph.MetaTask),
			Status:  status,
			Content: m.Text,
			Tags:    m.Tags(),
			At:      m.T,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
