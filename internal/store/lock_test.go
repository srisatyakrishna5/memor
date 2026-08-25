package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/memory"
)

func TestAcquireLockIsExclusive(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), LockFile)

	first, err := AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}

	if _, err := AcquireLock(lockPath, 0); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy while lock is held, got %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireLockWaitsForRelease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), LockFile)

	held, err := AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	releasing := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		close(releasing)
		held.Release()
	}()

	lock, err := AcquireLock(lockPath, 5*time.Second)
	if err != nil {
		t.Fatalf("AcquireLock should have waited for the holder: %v", err)
	}
	defer lock.Release()

	select {
	case <-releasing:
	default:
		t.Fatal("acquired the lock before the holder released it")
	}
}

func TestReleaseIsSafeToRepeat(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), LockFile)

	lock, err := AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	var unset *FileLock
	if err := unset.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}

// Compaction reads the WAL and then truncates it. Truncating the whole file
// discarded anything appended in between; truncating only the consumed prefix
// keeps it.
func TestTruncateWALPrefixKeepsLaterAppends(t *testing.T) {
	walPath := filepath.Join(t.TempDir(), MemoryWALFile)

	for _, content := range []string{"first", "second"} {
		if err := AppendToWAL(walPath, memory.Entry{Type: memory.TypeSemantic, Content: content}); err != nil {
			t.Fatalf("AppendToWAL: %v", err)
		}
	}

	entries, consumed, err := ReadWALConsumed(walPath)
	if err != nil {
		t.Fatalf("ReadWALConsumed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if err := AppendToWAL(walPath, memory.Entry{Type: memory.TypeSemantic, Content: "third"}); err != nil {
		t.Fatalf("AppendToWAL: %v", err)
	}

	if err := TruncateWALPrefix(walPath, consumed); err != nil {
		t.Fatalf("TruncateWALPrefix: %v", err)
	}

	remaining, err := ReadWAL(walPath)
	if err != nil {
		t.Fatalf("ReadWAL: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Content != "third" {
		t.Fatalf("expected only the late append to survive, got %+v", remaining)
	}
}

func TestReadWALConsumedCoversWholeFile(t *testing.T) {
	walPath := filepath.Join(t.TempDir(), MemoryWALFile)

	for i := 0; i < 3; i++ {
		if err := AppendToWAL(walPath, memory.Entry{
			Type:    memory.TypeSemantic,
			Content: string(rune('a'+i)) + " entry",
		}); err != nil {
			t.Fatalf("AppendToWAL: %v", err)
		}
	}

	if _, consumed, err := ReadWALConsumed(walPath); err != nil {
		t.Fatalf("ReadWALConsumed: %v", err)
	} else if err := TruncateWALPrefix(walPath, consumed); err != nil {
		t.Fatalf("TruncateWALPrefix: %v", err)
	}

	count, err := WALEntryCount(walPath)
	if err != nil {
		t.Fatalf("WALEntryCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected a fully consumed WAL to be emptied, got %d entries", count)
	}
}
