package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces a file's contents in one step so a crash mid-write
// can never leave a half-written canonical snapshot behind.
//
// The rename-into-place fast path is used everywhere it works. Windows refuses
// to rename over an open file, so the fallback moves the existing file aside
// first and restores it if the replacement fails.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if err := tempFile.Chmod(0o644); err != nil {
		tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err == nil {
		return nil
	}

	backupFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.bak")
	if err != nil {
		return err
	}
	backupPath := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	defer os.Remove(backupPath)

	if err := os.Rename(path, backupPath); err != nil {
		if os.IsNotExist(err) {
			return os.Rename(tempPath, path)
		}
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if restoreErr := os.Rename(backupPath, path); restoreErr != nil {
			return fmt.Errorf("replace %s: %v; restore original: %w", path, err, restoreErr)
		}
		return err
	}
	return os.Remove(backupPath)
}
