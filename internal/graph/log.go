package graph

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/memor-dev/memor/internal/store"
)

// Log operations. graph.log is the only append target in .memor/; every write
// in the system is one of these two records.
const (
	OpNode     = "n"
	OpDropNode = "-n"
)

// Record is one line of graph.log or graph.snap.
type Record struct {
	Op   string `json:"o"`
	Node *Node  `json:"n,omitempty"`
	ID   string `json:"i,omitempty"` // node tombstone target
}

// NodeRecord wraps a node for the log.
func NodeRecord(n *Node) Record { return Record{Op: OpNode, Node: n} }

// DropNodeRecord tombstones a node.
func DropNodeRecord(id string) Record { return Record{Op: OpDropNode, ID: id} }

// Encode marshals records to newline-free JSON lines.
func Encode(records []Record) ([][]byte, error) {
	out := make([][]byte, 0, len(records))
	for _, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			return nil, fmt.Errorf("marshal record: %w", err)
		}
		out = append(out, data)
	}
	return out, nil
}

// Decode parses log lines. A malformed line warns and is skipped: one bad line
// must not destroy the store.
func Decode(lines [][]byte, source string) []Record {
	records := make([]Record, 0, len(lines))
	for i, line := range lines {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			fmt.Fprintf(os.Stderr, "memor: skipping malformed %s line %d: %v\n", source, i+1, err)
			continue
		}
		records = append(records, rec)
	}
	return records
}

// Append writes records to graph.log while holding the directory lock.
func Append(logPath string, records []Record) error {
	lines, err := Encode(records)
	if err != nil {
		return err
	}
	return store.AppendRecords(logPath, lines)
}

// AppendLocked writes records without taking the lock. Callers must hold it.
func AppendLocked(logPath string, records []Record) error {
	lines, err := Encode(records)
	if err != nil {
		return err
	}
	return store.AppendRecordsLocked(logPath, lines)
}

// ReadLog reads and decodes graph.log, reporting the consumed byte offset so a
// later truncation removes exactly what was read.
func ReadLog(logPath string) ([]Record, int64, error) {
	lines, consumed, err := store.ReadRecordsConsumed(logPath)
	if err != nil {
		return nil, 0, err
	}
	return Decode(lines, "graph.log"), consumed, nil
}

// ReadSnap reads and decodes graph.snap.
func ReadSnap(snapPath string) ([]Record, error) {
	lines, err := store.ReadRecords(snapPath)
	if err != nil {
		return nil, err
	}
	return Decode(lines, "graph.snap"), nil
}

// Apply folds records onto a graph in order. Later records win, which is what
// makes the log an authoritative replay of the snapshot.
func Apply(g *Graph, records []Record) {
	for _, rec := range records {
		switch rec.Op {
		case OpNode:
			g.AddNode(rec.Node)
		case OpDropNode:
			g.RemoveNode(rec.ID)
		}
	}
}

// Snapshot serializes a graph as canonical node records.
func Snapshot(g *Graph) []Record {
	nodes := g.Nodes()
	records := make([]Record, 0, len(nodes))
	for _, n := range nodes {
		records = append(records, NodeRecord(n))
	}
	return records
}
