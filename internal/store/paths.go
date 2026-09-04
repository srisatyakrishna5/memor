package store

import (
	"os"
	"path/filepath"
)

// Files inside .memor/.
//
// Only LogFile is ever appended to, and only SnapFile is lossless. DBFile is a
// write-only projection for humans and agents; nothing parses it back, which is
// why memor has no bespoke DSL parser.
const (
	DirName     = ".memor"
	LogFile     = "graph.log"
	SnapFile    = "graph.snap"
	DBFile      = "graph.db"
	ArchiveFile = "graph.archive"
	StateFile   = "state.json"
	MarksFile   = "marks.jsonl"
	BlobsDir    = "blobs"
	ConfigFile  = "config.toml"
	LockFile    = ".lock"
)

// Paths holds resolved paths to all memor files for a project.
type Paths struct {
	Root    string // .memor/ directory
	Log     string
	Snap    string
	DB      string
	Archive string
	State   string
	Marks   string
	Blobs   string
	Config  string
	Lock    string
}

// ResolvePaths computes all paths relative to a project root.
func ResolvePaths(projectRoot string) Paths {
	root := filepath.Join(projectRoot, DirName)
	return Paths{
		Root:    root,
		Log:     filepath.Join(root, LogFile),
		Snap:    filepath.Join(root, SnapFile),
		DB:      filepath.Join(root, DBFile),
		Archive: filepath.Join(root, ArchiveFile),
		State:   filepath.Join(root, StateFile),
		Marks:   filepath.Join(root, MarksFile),
		Blobs:   filepath.Join(root, BlobsDir),
		Config:  filepath.Join(root, ConfigFile),
		Lock:    filepath.Join(root, LockFile),
	}
}

// ResolveUserPaths computes paths for the user-global memory in ~/.memor/.
func ResolveUserPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return ResolvePaths(home), nil
}

// FindProjectRoot walks up from start looking for a directory that contains
// .memor/. Long-running callers such as the MCP server are not guaranteed to be
// launched from the project root. If no ancestor is initialized, start is
// returned unchanged so callers report a consistent "run memor init" error.
func FindProjectRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, DirName)); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// EnsureDirs creates the .memor/ directory if it doesn't exist.
func (p *Paths) EnsureDirs() error {
	return os.MkdirAll(p.Root, 0o755)
}

// Exists returns true if the .memor/ directory exists.
func (p *Paths) Exists() bool {
	info, err := os.Stat(p.Root)
	return err == nil && info.IsDir()
}

// HasGraph reports whether a store exists.
func (p *Paths) HasGraph() bool {
	for _, path := range []string{p.Snap, p.Log} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// FootprintBytes totals the on-disk size of every memor artifact.
func (p *Paths) FootprintBytes() int64 {
	var total int64
	_ = filepath.Walk(p.Root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
