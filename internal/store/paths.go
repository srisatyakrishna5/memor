package store

import (
	"os"
	"path/filepath"
)

// Files inside .memor/.
//
// Only LogFile is ever appended to, and only SnapFile is lossless. DBFile is a
// write-only projection for humans and agents; nothing parses it back, which is
// why v2 has no bespoke DSL parser. IdxFile is derived and safe to delete at
// any time.
const (
	DirName     = ".memor"
	LogFile     = "graph.log"
	SnapFile    = "graph.snap"
	DBFile      = "graph.db"
	IdxFile     = "graph.idx"
	ArchiveFile = "graph.archive"
	BlobsDir    = "blobs"
	ConfigFile  = "config.toml"
	LockFile    = ".lock"
)

// Legacy v1 filenames, read once by `memor migrate` and never written.
const (
	LegacyMemoryDBFile  = "memory.db"
	LegacySnapshotFile  = "memory.snapshot.jsonl"
	LegacyWALFile       = "memory.wal"
	LegacyArchiveFile   = "memory.archive"
	LegacyKnowledgeFile = "knowledge.db"
	LegacyLockFile      = "lock"
)

// Paths holds resolved paths to all memor files for a project.
type Paths struct {
	Root    string // .memor/ directory
	Log     string
	Snap    string
	DB      string
	Idx     string
	Archive string
	Blobs   string
	Config  string
	Lock    string

	LegacyMemoryDB  string
	LegacySnapshot  string
	LegacyWAL       string
	LegacyArchive   string
	LegacyKnowledge string
}

// ResolvePaths computes all paths relative to a project root.
func ResolvePaths(projectRoot string) Paths {
	root := filepath.Join(projectRoot, DirName)
	return Paths{
		Root:            root,
		Log:             filepath.Join(root, LogFile),
		Snap:            filepath.Join(root, SnapFile),
		DB:              filepath.Join(root, DBFile),
		Idx:             filepath.Join(root, IdxFile),
		Archive:         filepath.Join(root, ArchiveFile),
		Blobs:           filepath.Join(root, BlobsDir),
		Config:          filepath.Join(root, ConfigFile),
		Lock:            filepath.Join(root, LockFile),
		LegacyMemoryDB:  filepath.Join(root, LegacyMemoryDBFile),
		LegacySnapshot:  filepath.Join(root, LegacySnapshotFile),
		LegacyWAL:       filepath.Join(root, LegacyWALFile),
		LegacyArchive:   filepath.Join(root, LegacyArchiveFile),
		LegacyKnowledge: filepath.Join(root, LegacyKnowledgeFile),
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

// HasLegacyStore reports whether a v1 store is present. A v2 command finding
// one and no graph.log triggers migration.
func (p *Paths) HasLegacyStore() bool {
	for _, path := range []string{p.LegacySnapshot, p.LegacyMemoryDB, p.LegacyWAL, p.LegacyKnowledge} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// HasGraph reports whether a v2 store exists.
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
