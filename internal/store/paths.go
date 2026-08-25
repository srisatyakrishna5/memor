package store

import (
	"os"
	"path/filepath"
)

const (
	DirName         = ".memor"
	MemoryDBFile    = "memory.db"
	SnapshotFile    = "memory.snapshot.jsonl"
	MemoryWALFile   = "memory.wal"
	ArchiveFile     = "memory.archive"
	KnowledgeDBFile = "knowledge.db"
	ConfigFile      = "config.toml"
	LockFile        = "lock"
)

// Paths holds resolved paths to all memor files for a project.
type Paths struct {
	Root      string // .memor/ directory
	MemoryDB  string
	Snapshot  string
	MemoryWAL string
	Archive   string
	Knowledge string
	Config    string
	Lock      string
}

// ResolvePaths computes all paths relative to a project root.
func ResolvePaths(projectRoot string) Paths {
	root := filepath.Join(projectRoot, DirName)
	return Paths{
		Root:      root,
		MemoryDB:  filepath.Join(root, MemoryDBFile),
		Snapshot:  filepath.Join(root, SnapshotFile),
		MemoryWAL: filepath.Join(root, MemoryWALFile),
		Archive:   filepath.Join(root, ArchiveFile),
		Knowledge: filepath.Join(root, KnowledgeDBFile),
		Config:    filepath.Join(root, ConfigFile),
		Lock:      filepath.Join(root, LockFile),
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

// EnsureDirs creates the .memor/ directory if it doesn't exist.
func (p *Paths) EnsureDirs() error {
	return os.MkdirAll(p.Root, 0o755)
}

// Exists returns true if the .memor/ directory exists.
func (p *Paths) Exists() bool {
	info, err := os.Stat(p.Root)
	return err == nil && info.IsDir()
}
