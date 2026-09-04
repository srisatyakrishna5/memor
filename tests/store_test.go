package tests

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/store"
)

func TestAcquireLockIsExclusive(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), store.LockFile)

	first, err := store.AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}

	if _, err := store.AcquireLock(lockPath, 0); !errors.Is(err, store.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy while lock is held, got %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := store.AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireLockWaitsForRelease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), store.LockFile)

	held, err := store.AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	releasing := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		close(releasing)
		held.Release()
	}()

	lock, err := store.AcquireLock(lockPath, 5*time.Second)
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
	lockPath := filepath.Join(t.TempDir(), store.LockFile)

	lock, err := store.AcquireLock(lockPath, 0)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	var unset *store.FileLock
	if err := unset.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}

// Compaction reads the log and then truncates it. Truncating the whole file
// discarded anything appended in between; truncating only the consumed prefix
// keeps it.
func TestTruncatePrefixKeepsLaterAppends(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), store.LogFile)

	if err := store.AppendRecords(logPath, [][]byte{
		[]byte(`{"o":"n","i":"first"}`),
		[]byte(`{"o":"n","i":"second"}`),
	}); err != nil {
		t.Fatalf("AppendRecords: %v", err)
	}

	records, consumed, err := store.ReadRecordsConsumed(logPath)
	if err != nil {
		t.Fatalf("ReadRecordsConsumed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	if err := store.AppendRecords(logPath, [][]byte{[]byte(`{"o":"n","i":"third"}`)}); err != nil {
		t.Fatalf("AppendRecords: %v", err)
	}

	if err := store.TruncatePrefix(logPath, consumed); err != nil {
		t.Fatalf("TruncatePrefix: %v", err)
	}

	remaining, err := store.ReadRecords(logPath)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(remaining) != 1 || string(remaining[0]) != `{"o":"n","i":"third"}` {
		t.Fatalf("expected only the late append to survive, got %q", remaining)
	}
}

func TestReadRecordsConsumedCoversWholeFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), store.LogFile)

	for i := 0; i < 3; i++ {
		if err := store.AppendRecords(logPath, [][]byte{
			[]byte(`{"o":"n","i":"` + string(rune('a'+i)) + `"}`),
		}); err != nil {
			t.Fatalf("AppendRecords: %v", err)
		}
	}

	if _, consumed, err := store.ReadRecordsConsumed(logPath); err != nil {
		t.Fatalf("ReadRecordsConsumed: %v", err)
	} else if err := store.TruncatePrefix(logPath, consumed); err != nil {
		t.Fatalf("TruncatePrefix: %v", err)
	}

	count, err := store.RecordCount(logPath)
	if err != nil {
		t.Fatalf("RecordCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected a fully consumed log to be emptied, got %d records", count)
	}
}
