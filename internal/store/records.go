package store

import (
	"bufio"
	"fmt"
	"os"
)

// maxRecordBytes bounds a single JSONL line. Nodes carry signatures and
// summaries, not bodies, so a megabyte is generous.
const maxRecordBytes = 1024 * 1024

// AppendRecords appends newline-delimited records to an append-only log while
// holding the directory lock.
//
// Compaction reads the log and then truncates it. Without this lock an append
// landing between those two steps is silently destroyed.
func AppendRecords(path string, records [][]byte) error {
	if len(records) == 0 {
		return nil
	}

	lock, err := AcquireLock(lockPathFor(path), WriteLockTimeout)
	if err != nil {
		return fmt.Errorf("lock log: %w", err)
	}
	defer lock.Release()

	return AppendRecordsLocked(path, records)
}

// AppendRecordsLocked appends without taking the lock. Callers must already
// hold it; the split exists so compaction cannot self-deadlock.
func AppendRecordsLocked(path string, records [][]byte) error {
	if len(records) == 0 {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, rec := range records {
		if len(rec) == 0 {
			continue
		}
		if _, err := w.Write(rec); err != nil {
			return fmt.Errorf("write log: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("write log: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush log: %w", err)
	}
	return f.Sync()
}

// ReadRecords reads every non-empty line from an append-only log.
func ReadRecords(path string) ([][]byte, error) {
	records, _, err := ReadRecordsConsumed(path)
	return records, err
}

// ReadRecordsConsumed reads the log and also reports how many bytes were
// consumed. The offset always lands on a line boundary, so passing it to
// TruncatePrefix removes exactly what was read and leaves any later append
// intact.
func ReadRecordsConsumed(path string) ([][]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	var records [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)

	var consumed int64
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, tok, err := bufio.ScanLines(data, atEOF)
		consumed += int64(advance)
		return advance, tok, err
	})

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		records = append(records, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return records, consumed, fmt.Errorf("scan log: %w", err)
	}
	return records, consumed, nil
}

// RecordCount returns the number of non-empty lines without parsing them.
func RecordCount(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			count++
		}
	}
	return count, scanner.Err()
}

// TruncatePrefix removes the first n bytes of a log and keeps everything after
// them. Compaction passes the offset it actually consumed so a record appended
// while it was running survives instead of being wiped.
func TruncatePrefix(path string, n int64) error {
	if n <= 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read log: %w", err)
	}

	if int64(len(data)) <= n {
		return os.WriteFile(path, nil, 0o644)
	}
	return os.WriteFile(path, data[n:], 0o644)
}

// AppendArchive appends records to the archive without holding the lock.
// Compaction calls it while already holding it.
func AppendArchive(path string, records [][]byte) error {
	return AppendRecordsLocked(path, records)
}
