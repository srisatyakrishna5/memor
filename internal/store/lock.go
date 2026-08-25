package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLockBusy reports that another process holds the state lock. Opportunistic
// callers such as auto-compaction should treat this as "skip", not "fail".
var ErrLockBusy = errors.New("memor: state lock is held by another process")

// lockRetryInterval is how long AcquireLock waits between attempts.
const lockRetryInterval = 20 * time.Millisecond

const (
	// WriteLockTimeout bounds how long a single WAL append waits for the lock.
	WriteLockTimeout = 5 * time.Second
	// CompactLockTimeout bounds how long an explicit compaction waits. Failing to
	// acquire within this window means something is wedged, not merely busy.
	CompactLockTimeout = 10 * time.Second
)

// FileLock is an advisory, cross-process exclusive lock over a project's .memor/
// state. The OS drops the lock when the holding process exits, so a crash can
// never leave a stale lock behind.
type FileLock struct {
	f *os.File
}

// AcquireLock takes the exclusive lock at path, retrying until timeout elapses.
// A timeout of 0 attempts exactly once. Returns ErrLockBusy if the lock is still
// held when the timeout expires.
func AcquireLock(path string, timeout time.Duration) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		locked, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if locked {
			return &FileLock{f: f}, nil
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, ErrLockBusy
		}
		time.Sleep(lockRetryInterval)
	}
}

// Release unlocks and closes the lock file. It is safe to call on a nil lock and
// safe to call more than once, so callers can always defer it.
func (l *FileLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil

	unlockErr := unlock(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// lockPathFor returns the lock guarding the .memor/ directory that dataPath
// lives in. One lock covers every state file in the directory, which is what
// lets compaction hold it across the archive, snapshot, and WAL writes.
func lockPathFor(dataPath string) string {
	return filepath.Join(filepath.Dir(dataPath), LockFile)
}
