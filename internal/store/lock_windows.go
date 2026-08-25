//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockRegionLen is the byte range locked. Windows byte-range locks are
// mandatory, so we lock a single byte we never read or write.
const lockRegionLen = 1

func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockRegionLen, 0,
		new(windows.Overlapped),
	)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, lockRegionLen, 0,
		new(windows.Overlapped),
	)
}
