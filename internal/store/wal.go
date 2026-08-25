package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/memor-dev/memor/internal/memory"
)

// AppendToWAL appends a single memory entry as a JSONL line to the WAL file.
func AppendToWAL(walPath string, entry memory.Entry) error {
	if entry.ID == "" {
		entry.ID = memory.ContentID(entry.Content)
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	data = append(data, '\n')

	// Compaction reads the WAL and then truncates it. Without this lock an append
	// landing between those two steps is silently destroyed.
	lock, err := AcquireLock(lockPathFor(walPath), WriteLockTimeout)
	if err != nil {
		return fmt.Errorf("lock WAL: %w", err)
	}
	defer lock.Release()

	f, err := os.OpenFile(walPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open WAL: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write WAL: %w", err)
	}

	return nil
}

// ReadWAL reads all entries from a JSONL WAL file.
// Malformed lines are skipped with a warning printed to stderr.
func ReadWAL(walPath string) ([]memory.Entry, error) {
	entries, _, err := ReadWALConsumed(walPath)
	return entries, err
}

// ReadWALConsumed reads the WAL and also reports how many bytes were consumed.
// The offset always lands on a line boundary, so passing it to TruncateWALPrefix
// removes exactly what was read and leaves any later append intact.
func ReadWALConsumed(walPath string) ([]memory.Entry, int64, error) {
	f, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("open WAL: %w", err)
	}
	defer f.Close()

	var entries []memory.Entry
	scanner := bufio.NewScanner(f)
	// Allow lines up to 1MB (for large content fields)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var consumed int64
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		consumed += int64(advance)
		return advance, token, err
	})

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry memory.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			fmt.Fprintf(os.Stderr, "memor: skipping malformed WAL line %d: %v\n", lineNum, err)
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, consumed, fmt.Errorf("scan WAL: %w", err)
	}

	return entries, consumed, nil
}

// WALEntryCount returns the number of lines in the WAL without fully parsing.
func WALEntryCount(walPath string) (int, error) {
	f, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			count++
		}
	}
	return count, scanner.Err()
}

// TruncateWALPrefix removes the first n bytes of the WAL and keeps everything
// after them. Compaction passes the offset it actually consumed so an entry
// appended while it was running survives instead of being wiped.
func TruncateWALPrefix(walPath string, n int64) error {
	if n <= 0 {
		return nil
	}

	data, err := os.ReadFile(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read WAL: %w", err)
	}

	if int64(len(data)) <= n {
		return os.WriteFile(walPath, nil, 0o644)
	}
	return os.WriteFile(walPath, data[n:], 0o644)
}

// AppendToArchive appends entries to the archive file.
func AppendToArchive(archivePath string, entries []memory.Entry) error {
	existingEntries, err := ReadWAL(archivePath)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	existingIDs := make(map[string]struct{}, len(existingEntries)+len(entries))
	for _, entry := range existingEntries {
		id := entry.ID
		if id == "" {
			id = memory.ContentID(entry.Content)
		}
		existingIDs[id] = struct{}{}
	}

	f, err := os.OpenFile(archivePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	for _, entry := range entries {
		if entry.ID == "" {
			entry.ID = memory.ContentID(entry.Content)
		}
		if _, exists := existingIDs[entry.ID]; exists {
			continue
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal archive entry: %w", err)
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write archive: %w", err)
		}
		existingIDs[entry.ID] = struct{}{}
	}

	return nil
}
