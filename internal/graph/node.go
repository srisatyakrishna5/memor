// Package graph implements memor's unified knowledge graph.
//
// Memories, files, symbols, packages, documents, and topics are all nodes
// connected by typed edges. One node type replaces the three parallel
// subsystems v1 grew (memories, knowledge, code), which is what allows a single
// retrieval pipeline and a single token-budget packer.
package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/constants"
)

// Kind classifies a node. Values are stable; the wire format uses the string
// code from kindCodes so graph.snap stays greppable and diffable.
type Kind uint8

const (
	KindFile  Kind = iota // source file
	KindSym               // function, method, type, const
	KindPkg               // directory or module
	KindExt               // third-party import, no body in this repo
	KindDoc               // knowledge section
	KindMem               // memory: decision, bug, workflow, preference
	KindTopic             // tag, promoted to a first-class node
)

var kindCodes = [...]string{"file", "sym", "pkg", "ext", "doc", "mem", "topic"}

func (k Kind) String() string {
	if int(k) >= len(kindCodes) {
		return "unknown"
	}
	return kindCodes[k]
}

// ParseKind resolves a wire code back to a Kind.
func ParseKind(s string) (Kind, bool) {
	for i, code := range kindCodes {
		if code == s {
			return Kind(i), true
		}
	}
	return 0, false
}

func (k Kind) MarshalJSON() ([]byte, error) { return json.Marshal(k.String()) }

func (k *Kind) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, ok := ParseKind(s)
	if !ok {
		return fmt.Errorf("unknown node kind %q", s)
	}
	*k = parsed
	return nil
}

// EdgeKind classifies a relation.
type EdgeKind uint8

const (
	EdgeImports    EdgeKind = iota // file -> file | ext
	EdgeContains                   // pkg -> file, file -> sym
	EdgeCalls                      // sym -> sym
	EdgeRefs                       // sym -> sym, non-call use
	EdgeTagged                     // any -> topic
	EdgeSupersedes                 // node -> node
	EdgeExplains                   // mem -> file | sym
)

var edgeCodes = [...]string{"imports", "contains", "calls", "refs", "tagged", "supersedes", "explains"}

func (e EdgeKind) String() string {
	if int(e) >= len(edgeCodes) {
		return "unknown"
	}
	return edgeCodes[e]
}

// ParseEdgeKind resolves a wire code back to an EdgeKind.
func ParseEdgeKind(s string) (EdgeKind, bool) {
	for i, code := range edgeCodes {
		if code == s {
			return EdgeKind(i), true
		}
	}
	return 0, false
}

func (e EdgeKind) MarshalJSON() ([]byte, error) { return json.Marshal(e.String()) }

func (e *EdgeKind) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, ok := ParseEdgeKind(s)
	if !ok {
		return fmt.Errorf("unknown edge kind %q", s)
	}
	*e = parsed
	return nil
}

// Memory subtypes, carried in Node.Meta under MetaMemType. They replace v1's
// Entry.Type for KindMem nodes and keep the same single-letter codes so
// migrated stores and existing agent prompts read identically.
const (
	MemSemantic   = "s"
	MemEpisodic   = "e"
	MemProcedural = "p"
	MemPreference = "f"
)

// Meta keys. Kept short because Meta is serialized on every node.
const (
	MetaMemType  = "t"   // memory subtype: s, e, p, f
	MetaLOC      = "loc" // line count for KindFile
	MetaLang     = "lang"
	MetaPkg      = "pkg"      // package or directory a file belongs to
	MetaSymKind  = "sym"      // func, method, type, const, var
	MetaPatterns = "patterns" // agent-authored L2 detail
	MetaLogic    = "logic"
	MetaSource   = "src" // source file for KindDoc
	MetaOrigin   = "org" // "extract" for machine-derived nodes, "agent" otherwise
)

// Origin values for MetaOrigin. Extraction wipes and rewrites machine-derived
// nodes on every build; agent-authored nodes survive untouched.
const (
	OriginExtract = "extract"
	OriginAgent   = "agent"
)

// ParseMemType maps a human or single-letter memory type onto its code.
func ParseMemType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "s", "semantic":
		return MemSemantic
	case "e", "episodic":
		return MemEpisodic
	case "p", "procedural":
		return MemProcedural
	case "f", "preference":
		return MemPreference
	default:
		return ""
	}
}

// MemTypeName expands a memory subtype code for human-facing output.
func MemTypeName(code string) string {
	switch code {
	case MemSemantic:
		return "semantic"
	case MemEpisodic:
		return "episodic"
	case MemProcedural:
		return "procedural"
	case MemPreference:
		return "preference"
	default:
		return "semantic"
	}
}

// Span locates a node's body in the repository. Bodies are never copied into
// .memor/: a span is a set of coordinates into files git already stores, so
// there is exactly one source of truth and staleness stays detectable per file.
type Span struct {
	Path  string `json:"p"`
	Start int    `json:"a"` // byte offset, inclusive
	End   int    `json:"b"` // byte offset, exclusive
	L0    int    `json:"l0"`
	L1    int    `json:"l1"`
	Hash  string `json:"h"` // sha256(file)[:6]
}

// Node is the single unit of storage.
type Node struct {
	ID   string            `json:"i"`
	Kind Kind              `json:"k"`
	Name string            `json:"n"` // path, symbol name, tag, doc section
	Text string            `json:"x"` // signature | summary | memory content
	Span *Span             `json:"s,omitempty"`
	T    int64             `json:"t"`           // created/updated, unix seconds
	Exp  int64             `json:"e,omitempty"` // expiry; -1 means permanent
	Meta map[string]string `json:"m,omitempty"`
}

// Edge is a typed relation between two nodes.
type Edge struct {
	From string   `json:"f"`
	To   string   `json:"t"`
	Kind EdgeKind `json:"k"`
	W    float32  `json:"w,omitempty"`
}

// NodeID derives a content-addressed identifier from a node's kind and its
// identity string. Re-deriving an ID for the same logical thing is therefore
// free deduplication and makes re-adding a fact idempotent.
func NodeID(k Kind, identity string) string {
	sum := sha256.Sum256([]byte(k.String() + "\x00" + normalizeIdentity(identity)))
	return hex.EncodeToString(sum[:])[:constants.NodeIDLength]
}

func normalizeIdentity(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// FileNode builds a source-file node keyed by its project-relative path.
func FileNode(path, summary string, loc int, hash, lang string) *Node {
	return &Node{
		ID:   NodeID(KindFile, path),
		Kind: KindFile,
		Name: path,
		Text: summary,
		T:    time.Now().Unix(),
		Span: &Span{Path: path, Hash: hash},
		Meta: map[string]string{
			MetaLOC:    fmt.Sprintf("%d", loc),
			MetaLang:   lang,
			MetaOrigin: OriginExtract,
		},
	}
}

// SymNode builds a symbol node. Identity is path#name so two same-named symbols
// in different files stay distinct.
func SymNode(path, name, signature, symKind string, span *Span) *Node {
	return &Node{
		ID:   NodeID(KindSym, path+"#"+name),
		Kind: KindSym,
		Name: name,
		Text: signature,
		Span: span,
		T:    time.Now().Unix(),
		Meta: map[string]string{MetaSymKind: symKind, MetaOrigin: OriginExtract},
	}
}

// PkgNode builds a package or directory node.
func PkgNode(dir string) *Node {
	return &Node{
		ID:   NodeID(KindPkg, dir),
		Kind: KindPkg,
		Name: dir,
		T:    time.Now().Unix(),
		Meta: map[string]string{MetaOrigin: OriginExtract},
	}
}

// ExtNode builds a node for a dependency whose body is not in this repository.
func ExtNode(importPath string) *Node {
	return &Node{
		ID:   NodeID(KindExt, importPath),
		Kind: KindExt,
		Name: importPath,
		T:    time.Now().Unix(),
		Meta: map[string]string{MetaOrigin: OriginExtract},
	}
}

// DocNode builds a knowledge-section node.
func DocNode(source, section, summary string) *Node {
	return &Node{
		ID:   NodeID(KindDoc, source+"#"+section),
		Kind: KindDoc,
		Name: section,
		Text: summary,
		T:    time.Now().Unix(),
		Meta: map[string]string{MetaSource: source, MetaOrigin: OriginExtract},
	}
}

// TopicNode builds a tag node.
func TopicNode(tag string) *Node {
	tag = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, "#")))
	return &Node{
		ID:   NodeID(KindTopic, tag),
		Kind: KindTopic,
		Name: tag,
		T:    time.Now().Unix(),
	}
}

// MemNode builds an agent-authored memory node. Its ID is derived from the
// normalized content, so recording the same fact twice collapses to one node.
func MemNode(content, memType string, at int64) *Node {
	if memType == "" {
		memType = MemSemantic
	}
	if at == 0 {
		at = time.Now().Unix()
	}
	return &Node{
		ID:   NodeID(KindMem, content),
		Kind: KindMem,
		Name: memType,
		Text: content,
		T:    at,
		Meta: map[string]string{MetaMemType: memType, MetaOrigin: OriginAgent},
	}
}

// MemType returns the memory subtype of a node, defaulting to semantic.
func (n *Node) MemType() string {
	if n.Meta == nil {
		return MemSemantic
	}
	if t, ok := n.Meta[MetaMemType]; ok && t != "" {
		return t
	}
	return MemSemantic
}

// Origin reports whether a node was machine-extracted or agent-authored.
func (n *Node) Origin() string {
	if n.Meta == nil {
		return OriginAgent
	}
	if o, ok := n.Meta[MetaOrigin]; ok && o != "" {
		return o
	}
	return OriginAgent
}

// SetMeta stores a metadata value, allocating the map on first use.
func (n *Node) SetMeta(key, value string) {
	if value == "" {
		return
	}
	if n.Meta == nil {
		n.Meta = make(map[string]string, 4)
	}
	n.Meta[key] = value
}

// MetaValue reads a metadata value, tolerating a nil map.
func (n *Node) MetaValue(key string) string {
	if n.Meta == nil {
		return ""
	}
	return n.Meta[key]
}

// IsExpired reports whether a node's expiry has passed. -1 means permanent.
func (n *Node) IsExpired() bool {
	if n.Exp <= 0 {
		return false
	}
	return time.Now().Unix() > n.Exp
}

// AgeDays returns how many days old the node is.
func (n *Node) AgeDays() float64 {
	if n.T == 0 {
		return 0
	}
	return time.Since(time.Unix(n.T, 0)).Hours() / 24
}

// Structural reports whether a node is derived from the repository rather than
// authored by an agent. Structural nodes are rebuilt by extraction, so they are
// never archived by compaction.
func (n *Node) Structural() bool {
	switch n.Kind {
	case KindFile, KindSym, KindPkg, KindExt, KindTopic, KindDoc:
		return true
	default:
		return false
	}
}

// ParseExpiry converts "YYYY-MM-DD" or a day duration such as "30d" into a Unix
// timestamp.
func ParseExpiry(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if digits, ok := strings.CutSuffix(s, "d"); ok {
		var days int
		if _, err := fmt.Sscanf(digits, "%d", &days); err == nil {
			return time.Now().AddDate(0, 0, days).Unix(), nil
		}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, fmt.Errorf("invalid expiry %q: use YYYY-MM-DD or a day count such as 30d", s)
	}
	return t.Unix(), nil
}
