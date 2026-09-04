---
title: "ADR-0001: Rewrite .memor as a Repository-Native Code Knowledge Graph"
status: "Accepted"
date: "2026-09-03"
authors: "Memor maintainers"
tags: ["architecture", "decision", "knowledge-graph", "token-efficiency", "storage"]
supersedes: ""
superseded_by: ""
---

## Status

**Accepted**

Supersedes the implicit v1 storage design documented in [CONCEPTS.md](../../CONCEPTS.md). The v1
on-disk format remains readable through a one-shot migration path (IMP-003) and is otherwise retired.

---

## Context

### Problem statement

GitHub Copilot and comparable coding agents begin every session with no structural model of the
repository. Answering a question such as *"where is authentication handled and what calls it?"*
requires a search-read-search loop that pulls whole files into the context window. Three costs follow:

1. **Direct token cost.** A 500-LOC source file is roughly 15 KB, or about 4,000 tokens. Ten such
   reads consume 40,000 tokens before any reasoning begins.
2. **Quality cost.** Token spend is not neutral. It actively degrades output quality (see
   [Evidence](#evidence-longer-context-degrades-output)).
3. **Repetition cost.** The same exploration repeats every session because nothing is persisted.

Memor v1 solves the repetition cost for *facts*. It does not solve it for *structure*, and it does
not solve the direct or quality costs at all.

### Evidence: longer context degrades output

This is the load-bearing premise of the decision, so it is sourced rather than asserted.

| Finding | Source |
|---|---|
| Accuracy depends on the **position** of relevant information. Highest at the beginning or end of the context window; "significantly degrades when models must access relevant information in the middle of long contexts, even for explicitly long-context models." | REF-001 |
| Across **18 models** (GPT-4.1, Claude 4, Gemini 2.5, Qwen3), performance degrades as input length grows *with task difficulty held constant*. Degradation is non-uniform. | REF-002 |
| On LongMemEval, **~300-token focused prompts outperformed the equivalent ~113k-token full prompts** across every model family tested. | REF-002 |
| **A single distractor** (topically related but non-answering content) measurably reduces accuracy; four distractors compound the effect. Impact amplifies with input length. Distractors are strictly worse than unrelated filler. | REF-002 |
| Failure mode splits by model family: Claude abstains under ambiguity (recall loss); GPT hallucinates confidently (precision loss). | REF-002 |
| Attention is a finite budget. Transformer attention is n^2 pairwise, so "every new token introduced depletes this budget." Guiding principle: "find the smallest possible set of high-signal tokens." | REF-003 |

The distractor finding is the sharpest constraint on the design. **Returning three plausible-but-wrong
symbols is worse than returning nothing.** Retrieval must optimise precision over recall.

### Evidence: code graphs address this specific problem

| System | Approach | Result |
|---|---|---|
| Aider repo map (REF-004) | tree-sitter extracts definitions and references, builds a file-level dependency graph, ranks with PageRank, packs top-ranked signatures into a fixed budget (default 1k tokens) | Production system. The map alone "may give it enough context to solve many tasks" |
| RepoGraph (REF-005, ICLR 2025) | Plug-in repository-level code graph | Improved **all four** host methods on SWE-bench; state of the art among open-source frameworks; generalised to CrossCodeEval |
| CodexGraph (REF-006) | Agent issues queries against a graph database with a unified cross-language schema | Motivated explicitly by "similarity-based retrieval often has low recall in complex tasks" |
| GraphRAG (REF-007) | LLM-extracted entities and relations, Leiden clustering, LLM-generated community summaries | Strong on holistic questions, but **indexing is LLM-heavy** |

### Current state of the v1 implementation

Three parallel subsystems grew independently inside `.memor/`:

| Subsystem | Storage | Reader/writer | Ranker |
|---|---|---|---|
| Memories | `memory.wal`, `memory.snapshot.jsonl`, `memory.db`, `memory.archive` | JSONL plus a compact DSL | `rankEntries` |
| Knowledge | `knowledge.db` | Bespoke regex parser (`LoadKnowledgeDB`) | Separate BM25 inside `loadKnowledge` |
| Code (`@c`) | Occupies the memory subsystem | `CodeMeta` as a side-channel on `Entry` | None. `Deps` is never traversed |

`Context()` in [internal/engine/context.go](../../internal/engine/context.go) therefore constructs
**two independent BM25 indexes** and runs **two packing loops** against a single token budget.

That seam has already produced a live defect. The knowledge-fallback branch re-loops over `ranked`
and guards with:

```go
if memoryTokens+lineTokens <= memoryBudget-headerTokens {
    continue // "already written"
}
```

`memoryTokens` is frozen at its post-loop-1 total, so it no longer identifies *which* entries were
written. Entries that were written mostly fail the test and are **emitted twice**; small entries the
first loop skipped after an early `break` mostly pass it and are **dropped entirely**. Both failures
are silent, and both spend budget on the near-miss duplicate content that REF-002 identifies as most
damaging.

The defect cannot be fixed cleanly without collapsing the two paths into one. One path requires one
node type. One node type is a graph.

### Assets already present in v1

| Graph concept | v1 construct | Status |
|---|---|---|
| Node | `@c` entry keyed by `Meta.FilePath` | Present, file granularity only |
| Edge | `CodeMeta.Deps []string` | Persisted but never traversed or ranked on |
| Temporal edge | `Entry.Supersedes` | Used during compaction only |
| Node summary | `Meta.Summary`, `Patterns`, `Logic` | Present, but LLM-authored |
| Freshness | `Meta.Hash` plus `CodeStatus()` returning fresh/stale/missing | Working primitive |
| Ranking | `0.4*BM25 + 0.2*tags + 0.2*typeWeight + 0.2*recency` | No graph signal |
| Budget packing | Greedy line-by-line to budget | No position awareness, no precision floor |

The missing capability is **automatic extraction**. `memor code save` currently requires an agent to
supply `--exports` and `--deps` as strings, which inverts the goal: the agent spends tokens reading a
file in order to save tokens later, and break-even arrives only after several sessions.

### Measured repository footprint

Measured on this repository on 2026-09-03:

| Scope | Files | Size | Estimated tokens |
|---|---:|---:|---:|
| Memor Go source | 50 | 217 KB | ~58,500 |
| Full workspace including the `.github/` corpus | 1,755 | 12.8 MB | ~3,450,000 |

### Constraints

| # | Constraint | Origin |
|---|---|---|
| C1 | No database server, no daemon, no background worker | CONCEPTS.md |
| C2 | Each command performs one bounded operation and exits | CONCEPTS.md |
| C3 | Small active footprint | README.md ("< 200 KB per project") |
| C4 | Reproducible from source; not a hand-maintained artifact | `memor init` gitignores `.memor/` |
| C5 | No mandatory network or LLM calls during indexing | "Local files. Zero cloud, zero daemon." |
| C6 | One MCP surface serving Copilot, Claude, Cursor, and Windsurf | README.md |
| C7 | **No CGO.** npm ships cross-compiled prebuilt binaries per platform | [npm/scripts/install.js](../../npm/scripts/install.js), [go.mod](../../go.mod) |

C7 is decisive and easy to overlook. `tree-sitter/go-tree-sitter` is 68% C and requires CGO, which
breaks the cross-compilation matrix the npm installer depends on.

---

## Decision

Rewrite the `.memor` implementation as a **single unified knowledge graph** in which memories,
files, symbols, documents, and topics are all nodes connected by typed edges, retrieved through one
task-conditioned ranking pipeline, and exposed to agents through a minimal MCP surface.

Four decisions define the design.

### D1: One node type replaces three subsystems

```go
type Kind uint8

const (
    KindFile  Kind = iota // source file
    KindSym                // function, method, type, const
    KindPkg                // directory or module
    KindExt                // third-party import, no body
    KindDoc                // knowledge section
    KindMem                // memory: decision, bug, workflow, preference
    KindTopic              // tag, promoted to a first-class node
)

type Node struct {
    ID   string            `json:"i"` // sha256(kind + identity)[:12]
    Kind Kind              `json:"k"`
    Name string            `json:"n"` // path, symbol name, tag, doc section
    Text string            `json:"x"` // signature | summary | memory content
    Span *Span             `json:"s,omitempty"`
    T    int64             `json:"t"`
    Exp  int64             `json:"e,omitempty"`
    Meta map[string]string `json:"m,omitempty"`
}

type Span struct {
    Path  string `json:"p"`
    Start int    `json:"a"`  // byte offset
    End   int    `json:"b"`
    L0    int    `json:"l0"` // line range, for the agent
    L1    int    `json:"l1"`
    Hash  string `json:"h"`  // sha256(file)[:6], for staleness
}

type Edge struct {
    From string   `json:"f"`
    To   string   `json:"t"`
    Kind EdgeKind `json:"k"`
    W    float32  `json:"w,omitempty"`
}
```

Edge kinds:

| Kind | From to To | Extraction tier |
|---|---|---|
| `imports` | file to file or external | L0 |
| `contains` | pkg to file, file to symbol | L0 / L1 |
| `calls` | symbol to symbol | L1 |
| `refs` | symbol to symbol, non-call use | L1 |
| `tagged` | any node to topic | replaces `Entry.Tags` |
| `supersedes` | node to node | replaces `Entry.Supersedes` |
| `explains` | memory to symbol or file | the differentiator |

`explains` is the capability no comparable tool has. Aider and RepoGraph supply structure. Memor can
additionally bind *"a persistent index was rejected on purpose"* to the exact symbol where a future
agent would otherwise add one. It costs one edge.

The v1 concepts map without loss:

| v1 | v2 |
|---|---|
| `Entry{Type: s/e/p/f}` | `Node{Kind: KindMem, Meta["t"]: "s"}` |
| `Entry.Tags` | `Edge{tagged}` to `KindTopic` |
| `Entry.Supersedes` | `Edge{supersedes}` |
| `CodeMeta.FilePath`, `LOC`, `Hash` | `Node{KindFile}` plus `Span` |
| `CodeMeta.Exports` | `Edge{contains}` to `KindSym` nodes |
| `CodeMeta.Deps` | `Edge{imports}` |
| `CodeMeta.Summary` | `Node.Text` |
| `KnowledgeSection` | `Node{KindDoc}` |

### D2: Store spans, not source text

A symbol node records `path + byte range + content hash + signature`. The body remains on disk in the
repository and is fetched on demand.

| Requirement | Why spans satisfy it |
|---|---|
| Store repository content | Every byte is addressable and retrievable in one seek. Content is indexed, not copied |
| Work offline | Reading a byte range from a local file is the most offline operation available |
| Native to the repository | The graph is a set of coordinates into the repo; it has no meaning apart from it, and no second copy to drift |
| Minimal footprint | Storing bodies duplicates `.git/objects`, which is already a compressed content-addressed store of the same bytes |
| Minimal tokens | The agent receives a ~12-token pointer and pulls a ~200-token body only when it decides it needs one |

Spans also preserve per-file staleness detection. A hash mismatch identifies exactly one drifted
file. Storing bodies would create two sources of truth and require hashing disk on every read, which
is the I/O the design set out to avoid.

Bodies are cached lazily, never mirrored eagerly:

```toml
[graph.cache]
enabled   = true
max_bytes = 262144   # 256 KB hard cap
policy    = "lru"
```

A body enters the cache only when `symbol_read` serves it, keyed by content hash, so serving a stale
entry is structurally impossible.

### D3: One retrieval pipeline

```go
func Retrieve(g *Graph, q Query) (string, error) {
    seeds    := g.Seeds(q.Text, q.Tags, q.OpenFiles) // BM25 plus exact name match
    frontier := g.Expand(seeds, q.MaxHops)           // weighted graph walk
    scored   := g.Score(frontier, q)
    kept     := gate(scored, q.MinScore)             // precision floor
    return pack(kept, q.Budget), nil                 // position-aware
}
```

```text
score(n) = 0.30*bm25(n,q) + 0.25*proximity(n,seeds) + 0.20*rank(n)
         + 0.15*tagMatch(n,q) + 0.10*recency(n)
```

`rank(n)` is offline personalised PageRank over the edge graph, computed at build time (damping 0.85,
20 power iterations). `proximity` is a decayed 2-hop expansion from seed nodes, where seeds are files
named in the query, files open in the editor, and files touched by recent episodic memories.

Two behaviours differ deliberately from v1:

- **`gate()` drops rather than fills.** If nothing clears `min_score`, the returned block is short.
  Underfilling is correct: per REF-002, one distractor measurably degrades output and four compound
  it. The v1 fill-to-budget loop optimises the wrong variable.
- **`pack()` places the strongest results first *and* last**, weakest survivors in the middle. Per
  REF-001, accuracy is highest at both ends of the window. v1 emits in descending score order, which
  buries nothing at the recency-favoured tail.

Single BM25 index, single packer. The duplicate-emission defect becomes inexpressible.

### D4: Tiered, deterministic extraction

| Tier | Scope | Mechanism | Dependencies |
|---|---|---|---|
| **L0** | Files, packages, imports | Line-oriented scan for import statements across Go, JS/TS, Python, Java, C#, Rust | None |
| **L1** | Symbols, spans, signatures, calls | `go/ast` for Go first; tree-sitter via `wazero` evaluated behind a build tag | stdlib, then optional |
| **L2** | Summaries, patterns, decisions | Existing `code save` and `memory_add`, merged onto extracted nodes | Agent-authored |

No tier requires a network call or an LLM call, satisfying C5. `tree-sitter/go-tree-sitter` is
excluded under C7; `wazero` is pure Go with no CGO and depends only on `golang.org/x/sys`, which is
already required.

### On-disk layout

```text
.memor/
├── config.toml
├── graph.log        # append-only JSONL: nodes, edges, tombstones
├── graph.snap       # compacted canonical JSONL (lossless)
├── graph.db         # rendered DSL projection (write-only output)
├── graph.idx        # DERIVED: adjacency, postings, PageRank. Deletable
├── graph.archive    # evicted nodes
├── blobs/           # LRU body cache, hash-keyed, size-capped
└── .lock
```

Four rules keep this honest:

1. **`graph.log` is the only append target.** Every write is a node, edge, or tombstone record.
2. **`graph.snap` is the only lossless artifact.** Everything else regenerates from it.
3. **`graph.idx` is disposable by definition.** Delete it and the next command rebuilds it. The
   binary format lives here and nowhere else, giving load speed without an opaque source of truth.
4. **`graph.db` is write-only.** Nothing parses it back. This deletes the bespoke regex parser in
   `LoadKnowledgeDB`, which exists only because `knowledge.db` is both a projection and an input.

Rendered projection format:

```text
@g v2 | 214 files | 1,847 symbols | 5,102 edges | built:2026-09-03T10:00:00Z

@f internal/engine/context.go [251 LOC | 4a91cc] pkg:engine
  Context(paths, cfg, opts) (string, error)             @22-108
  rankEntries(entries, query, tags, cfg) []ScoredEntry  @111-155
  loadKnowledge(paths, query, budget) (string, error)   @158-232
  -> internal/index, internal/token, internal/store
  <- cmd/context.go, internal/mcp/tools.go
  ~ "BM25 is rebuilt per call by design; no persistent index" [semantic, 2026-08-10]
```

`->` is `imports`; `<-` is the reverse edge, which is the direction agents usually need ("what breaks
if I change this?") and is free to precompute. The `~` line is an `explains` edge rendered inline.

That block is approximately 90 tokens and substitutes for a 3,400-token file read.

### Package structure

```text
internal/
├── graph/
│   ├── node.go        # Node, Edge, Kind, Span, ID derivation
│   ├── log.go         # append-only writer, tombstones
│   ├── snap.go        # compaction: dedup, score, evict, commit
│   ├── idx.go         # adjacency, postings, PageRank; derived and disposable
│   ├── extract/
│   │   ├── imports.go # L0
│   │   └── golang.go  # L1, go/ast
│   └── render.go      # graph.db projection
├── retrieve/
│   └── retrieve.go    # single pipeline; replaces rankEntries + loadKnowledge
├── store/             # RETAINED: lock.go, lock_unix.go, lock_windows.go, paths.go
├── token/             # RETAINED unchanged
├── config/            # extended
└── mcp/               # 5 tools
```

Removed: `internal/memory/`, `internal/engine/{context,knowledge,code,compact}.go`,
`internal/index/bm25.go` (folded into `idx.go`).

### MCP surface

Five tools, matching the current count. REF-003 identifies bloated tool sets and ambiguous tool
selection as a leading agent failure mode, so capability is folded rather than added.

| v2 tool | Replaces | Contract |
|---|---|---|
| `repo_map` | `memory_context` | Task-ranked structure plus memories. Called once at conversation start |
| `symbol_find` | `memory_search` plus grep | Name or pattern to definition span, signature, callers, callees |
| `symbol_read` | `code_get` plus most `read_file` calls | Span to exact source bytes; returns 40 lines, not 400 |
| `remember` | `memory_add` plus `code_save` | Records a fact or a file summary |
| `graph_status` | `memory_stats` | Node and edge counts, staleness ratio, budget |

Tool descriptions retain the behavioural-contract style already established in
[internal/mcp/tools.go](../../internal/mcp/tools.go). The existing `code_get` phrasing
("If status is fresh, the summary matches the file on disk and you can skip the read") is what
actually changes agent behaviour, and is the template for all five.

### External file footprint

`memor init` currently writes to seven locations outside `.memor/`. v2 reduces this to two, both pure
pointers, because Copilot cannot be instructed to read `.memor/instructions.md`:

```jsonc
// .vscode/mcp.json — MCP discovery. Irreducible.
{ "servers": { "memor": { "command": "memor", "args": ["mcp"] } } }
```

```markdown
<!-- AGENTS.md — approximately 3 lines. -->
This repo uses memor. Call `repo_map` before reading or searching any file.
Full protocol: run `memor rules` or read the MCP tool descriptions.
```

The 60-line instruction template shrinks because tool descriptions are a better home for behavioural
rules than a markdown file: they load with the tool, cannot be edited away by a user, and are
versioned with the binary.

---

## Consequences

### Positive

- **POS-001**: Structural questions become answerable without file reads. Estimated exploration cost
  falls from approximately 28,000 tokens to approximately 1,950 tokens on a representative task, an
  order-of-magnitude reduction.
- **POS-002**: Output quality should improve rather than merely hold, given the focused-versus-full
  prompt result in REF-002. Avoided reads were actively degrading answers, not merely costing money.
- **POS-003**: Output tokens fall as well as input tokens. Exact line spans let the model emit a
  span-anchored patch (approximately 400 tokens) instead of regenerating a file (approximately 3,500
  tokens).
- **POS-004**: The duplicate-emission and silent-drop defect in `Context()` becomes structurally
  inexpressible, because a single packer replaces two.
- **POS-005**: `CodeMeta.Deps` becomes load-bearing rather than decorative.
- **POS-006**: Coverage becomes complete and automatic rather than incidental to which files an agent
  happened to open.
- **POS-007**: The bespoke `knowledge.db` regex parser is deleted, because `graph.db` is write-only.
- **POS-008**: Agent-authored L2 summaries become more valuable, because they now attach to a ranked
  structure instead of a flat list.
- **POS-009**: External file footprint drops from seven owned files to two pointers, removing a class
  of merge conflicts and "did Memor break my settings?" reports.
- **POS-010**: Every MCP client benefits equally, satisfying C6 without per-tool work.

### Negative

- **NEG-001**: On-disk footprint grows roughly an order of magnitude. Estimated at approximately
  180 KB for this repository including a warm blob cache. **C3 must be formally revised** from
  "< 200 KB total" to "< 200 KB for memory artifacts, < 2 MB for the graph."
- **NEG-002**: Cold-start rebuild cost is single-digit seconds on a large repository. It parallelises,
  but it is a new latency that v1 does not have.
- **NEG-003**: Extraction is per-language work with a long tail. L0 covers six languages cheaply;
  L1 initially covers Go only.
- **NEG-004**: A stale graph is worse than no graph. Hash-based staleness mitigates this, but a
  missed compaction hook can still mislead an agent.
- **NEG-005**: A rewrite risks silently losing v1's hard-won correctness invariants (see IMP-002).
- **NEG-006**: The on-disk format breaks. Existing users require a migration path.
- **NEG-007**: Dropping `.cursorrules` and default auto-approve is a small behaviour regression for
  existing users.
- **NEG-008**: More surface to test and maintain: one new package, one rewritten retrieval path, and
  a benchmark harness that must be kept current.

### Risks and mitigations

| Risk | Mitigation |
|---|---|
| Heuristic extraction produces wrong edges, creating distractors, the worst failure class per REF-002 | Precision gate (D3); confidence field on edges; low-confidence edges retained in `graph.snap` but excluded from `graph.db` |
| Stale graph misleads the agent | Reuse `CodeStatus`; render staleness inline; auto-rebuild above a stale-ratio threshold |
| Scope creep toward a graph database | C1 and C2 restated as hard constraints. No query language, no index server |
| WASM grammar assets bloat the binary | L1b stays behind a build tag; grammars ship as optional downloads, never baked in |
| Ranking weights are initial guesses | IMP-012 gates promotion of every subsequent phase on measured results |
| Rewrite loses crash safety | IMP-002 carries invariants and their tests across verbatim |

---

## Alternatives Considered

### Status quo: agent-authored `@c` summaries

- **ALT-001**: **Description**: Retain `memor code save` with agent-supplied `--exports` and `--deps`.
- **ALT-002**: **Rejection reason**: Inverts the goal. The agent spends tokens reading a file in order
  to save tokens later, so break-even is several sessions away. Coverage is incidental to which files
  an agent happened to open. `Deps` remains inert and no structural navigation is possible.

### GraphRAG-style LLM extraction

- **ALT-003**: **Description**: LLM-extracted entities and relations, Leiden clustering, LLM-generated
  community summaries (REF-007).
- **ALT-004**: **Rejection reason**: Requires an LLM call per text unit *and* per community summary,
  violating C5 outright, with cost scaling in repository size. The clustering machinery also exceeds
  C1 and C2. The community-summary compression idea is retained but produced by deterministic
  aggregation rather than generation.

### Embedding and vector index over code chunks

- **ALT-005**: **Description**: Chunk source files, embed, retrieve by cosine similarity.
- **ALT-006**: **Rejection reason**: Requires an embedding model, either over the network (violates
  C5) or bundled as ONNX (violates C3 and C7). More fundamentally, similarity retrieval returns code
  that *resembles* the query, which is precisely the distractor class REF-002 identifies as most
  harmful. "Calls", "imports", and "defined-in" are the relations an agent actually needs, and
  similarity does not encode them.

### Query-time graph database

- **ALT-007**: **Description**: Persist a graph database and let the agent write queries against it
  (REF-006).
- **ALT-008**: **Rejection reason**: Requires a persistent database and query engine, violating C1.
  Adds multiple tool round-trips per question, each with protocol overhead. Requires the agent to
  learn a query language, which is a large prompt surface. The progressive-disclosure property is
  retained through the narrow `symbol_read` expansion tool instead.

### Mirror repository content into `.memor/`

- **ALT-009**: **Description**: Store a compressed copy of every source file inside `.memor/` so the
  graph is fully self-contained.
- **ALT-010**: **Rejection reason**: Three independent objections. First, arithmetic: even on this
  50-file repository a mirror is 217 KB raw or approximately 65 KB gzipped, consuming a third of the
  entire v1 budget before a single memory is stored; the full workspace would require approximately
  3.5 MB. Second, staleness stops being detectable per-file: with two sources of truth the agent must
  hash disk on every read to trust the mirror, which is the I/O the mirror was meant to avoid. Third,
  it duplicates `.git/objects`, which is already a compressed, content-addressed, offline store of the
  same bytes.

### Git blob-pinned spans

- **ALT-011**: **Description**: Pin spans to git blob SHAs rather than path plus byte offset, making
  them immutable and dirty-tree-proof.
- **ALT-012**: **Rejection reason**: Makes Memor require git, which today it does not; it only touches
  `.gitignore`. Deferred, not permanently rejected. Revisit if dirty-tree correctness becomes a
  reported problem.

### Commit the graph to version control

- **ALT-013**: **Description**: Store `graph.snap` under `.memor-graph/` and commit it, so the map
  travels with a clone and the cloud Copilot coding agent can use it.
- **ALT-014**: **Rejection reason**: Deferred rather than rejected on merit. The benefits are real
  (zero cold start, team-shared decision memories, reviewable structural diffs), but committing a
  generated artifact requires a merge driver and `.gitattributes` work that is not on the critical
  path. Revisit after IMP-012 produces measured value.

### Evolve v1 in place

- **ALT-015**: **Description**: Extend `Entry` and `CodeMeta`, add a graph package alongside, and keep
  the three existing subsystems.
- **ALT-016**: **Rejection reason**: The three-subsystem seam is the source of the live packing
  defect, and it cannot be closed without unifying the node type. Roughly 70% of the machinery is
  reusable and is explicitly carried across (IMP-002), so the practical difference is confined to the
  storage schema and the retrieval path. The user selected the rewrite to obtain a graph-first model
  rather than a graph bolted onto a memory-first one.

---

## Implementation Notes

### Prioritised backlog

Ordered strictly by priority. Every item is independently shippable, and no earlier item depends on a
later one. `graph.enabled = false` reverts to v1 behaviour at any point.

| # | Item | Priority | Depends on | Done when |
|---:|---|---|---|---|
| 1 | IMP-001 Node, Edge, Kind, Span, content-addressed IDs | **P0** | — | `internal/graph/node.go` round-trips every v1 concept in the mapping table |
| 2 | IMP-002 Carry crash-safety invariants and their tests | **P0** | — | `lock_test.go` and `compact_concurrency_test.go` pass unchanged against v2 |
| 3 | IMP-003 `graph.log` writer plus `snap.go` compaction | **P0** | 1, 2 | Kill and restart mid-compaction leaves a recoverable store |
| 4 | IMP-004 `memor migrate` (v1 to v2) | **P0** | 1, 3 | This repository's memories round-trip with zero loss; v1 files renamed `.v1.bak` |
| 5 | IMP-005 Config v2 plus `graph.enabled` kill switch | **P0** | 3 | Setting `false` restores v1-equivalent behaviour |
| 6 | IMP-006 L0 import extraction | **P1** | 1, 3 | Graph builds over this repository in under one second |
| 7 | IMP-007 `idx.go`: adjacency, postings, PageRank | **P1** | 6 | Deleting `graph.idx` rebuilds it transparently |
| 8 | IMP-008 `render.go`: `graph.db` projection | **P1** | 7 | Rendered map for this repository fits in 4,000 tokens |
| 9 | IMP-009 `memor graph build\|status` CLI | **P1** | 8 | Footprint and staleness ratio are observable |
| 10 | IMP-010 Benchmark harness plus 20-question task suite | **P1** | 9 | Baseline v1 numbers recorded for tokens, reads, and correctness |
| 11 | IMP-011 Unified `retrieve` pipeline | **P2** | 7, 10 | `rankEntries` and `loadKnowledge` both deleted |
| 12 | IMP-012 Precision gate (`min_score`) | **P2** | 11 | Distractor rate under 10% on the suite |
| 13 | IMP-013 Position-aware packing | **P2** | 11 | Strongest results occupy both ends of the block |
| 14 | IMP-014 **Ship gate**: measure against IMP-010 baseline | **P2** | 11, 12, 13 | 50% or greater token reduction with zero correctness regression |
| 15 | IMP-015 `repo_map` MCP tool | **P3** | 14 | Agent calls it before any file read |
| 16 | IMP-016 `symbol_find` MCP tool | **P3** | 15 | Replaces grep in agent traces |
| 17 | IMP-017 `symbol_read` MCP tool plus span reads | **P3** | 15 | Serves a 40-line span in place of a 400-line read |
| 18 | IMP-018 Fold `remember` and `graph_status`; rewrite `AGENTS.md` | **P3** | 15, 16, 17 | Tool count stays at five |
| 19 | IMP-019 Consolidate external files from seven to two | **P3** | 18 | `memor init` writes only `.vscode/mcp.json` and `AGENTS.md` |
| 20 | IMP-020 L1 Go symbol extraction via `go/ast` | **P4** | 6, 11 | Symbol-level precision beats file-level on the suite |
| 21 | IMP-021 Spans and per-symbol hash staleness | **P4** | 20 | Stale spans are never served |
| 22 | IMP-022 `explains` edges (memory to file, then to symbol) | **P4** | 11, 20 | Decision memories render inline on the owning node |
| 23 | IMP-023 LRU blob cache under `blobs/` | **P4** | 17, 21 | Cache stays within `max_bytes`; stale hits impossible |
| 24 | IMP-024 tree-sitter via `wazero`, behind a build tag | **P5** | 20 | Multi-language L1 without CGO; grammars stay optional |
| 25 | IMP-025 Additional language L1 extractors | **P5** | 24 | Driven by user demand, not speculation |
| 26 | IMP-026 Revisit committing the graph (ALT-013) | **P5** | 14 | Merge driver plus `.gitattributes` proven on a real branch |

### Priority definitions

- **P0** — Foundation. Nothing else can be built or migrated without these. No user-visible value.
- **P1** — The graph exists and is inspectable. First point at which the work can be evaluated.
- **P2** — Retrieval. Where the token savings actually land. **IMP-014 is the go/no-go gate.**
- **P3** — Agent surface. Where GitHub Copilot consumes the result.
- **P4** — Depth. Symbol granularity and the `explains` differentiator.
- **P5** — Breadth. Optional, demand-driven, or deferred by an earlier alternative.

### Invariants that must survive (IMP-002 detail)

These are load-bearing, non-obvious, and each has a test behind it. They are the primary risk of a
rewrite.

| # | Invariant | Current location | Why it matters |
|---|---|---|---|
| I1 | One lock per `.memor/` directory, not per file | `lockPathFor()` | The only reason compaction can span archive, snapshot, and truncate |
| I2 | Commit order: archive, then snapshot, then truncate | `compactLocked` step 6 | "archive before replacing the active snapshot so failures are retryable" |
| I3 | `ReadWALConsumed` plus `TruncateWALPrefix` truncate only the consumed prefix | `store/wal.go` | Appends racing a compaction are not lost. Rewriting this as `Truncate(0)` silently drops writes |
| I4 | Locked and unlocked function split | `Compact` versus `compactLocked` | "nothing it calls acquires the lock itself, so it cannot self-deadlock" |
| I5 | `ErrLockBusy` means skip, not fail; `AutoCompact` uses `timeout=0` | `AutoCompact` | Auto-compaction must never stall an MCP call |
| I6 | The OS releases the lock on process exit | `FileLock` doc comment | No stale-lock recovery code is needed. Do not add a lease or PID file |
| I7 | `AutoCompact` writes nothing to stdout | `AutoCompact` doc comment | Any stray print corrupts MCP stdio JSON-RPC framing |
| I8 | Content-addressed IDs: `sha256(lower(trim(c)))[:12]` | `memory.ContentID` | Free deduplication and idempotent re-adds |
| I9 | A malformed line warns and continues | `store.ReadWAL` | One bad line must not destroy the store |

Carry `lock.go`, `lock_unix.go`, `lock_windows.go`, `lock_test.go`, and
`compact_concurrency_test.go` across unchanged.

### Migration (IMP-004 detail)

```text
memor migrate    # v1 to graph.log; v1 files renamed *.v1.bak; idempotent
```

Reads `memory.snapshot.jsonl`, `memory.wal`, and `knowledge.db`; emits v2 nodes and edges per the
D1 mapping table; then compacts. `@c` entries carrying `Meta.Deps` become real `imports` edges, so
existing users receive a partial graph immediately, before any extraction runs.

Auto-triggers on the first v2 command when v1 files are present and `graph.log` is absent. The
`@mem v1` header already emitted by v1 provides the version discriminator.

### Success criteria (IMP-014 gate)

A fixed suite of approximately 20 realistic repository questions ("where is X handled", "what breaks
if I change Y", "how do I add a Z"), run in three configurations: no graph, graph only, graph plus
memory.

| Metric | Target |
|---|---|
| Tokens to first correct answer | 50% or greater reduction. Primary metric |
| File reads before answering | Reduced. Proxy for wasted exploration |
| Answer correctness, human-scored | **Zero regression. Non-negotiable** |
| Distractor rate (returned nodes irrelevant to the question) | Under 10%, per REF-002 |
| Build time and footprint | Under one second and under 2 MB for this repository |

A token win accompanied by a correctness loss is a failed experiment, not a trade-off.

### Open items deliberately deferred

| Question | Current default | Revisit at |
|---|---|---|
| `graph.snap` format: JSONL or binary | JSONL. `graph.idx` already absorbs the speed argument, and JSONL stays greppable and diffable | IMP-007, if load time is measurable |
| Per-branch graph | No. Staleness is handled per node by hash | If long-lived branches cause reported drift |
| Commit the graph (ALT-013) | No | IMP-026, after IMP-014 |
| Git blob-pinned spans (ALT-011) | No | If dirty-tree correctness is reported |

---

## References

- **REF-001**: Liu, N. F., Lin, K., Hewitt, J., Paranjape, A., Bevilacqua, M., Petroni, F., and
  Liang, P. (2023). *Lost in the Middle: How Language Models Use Long Contexts*. TACL.
  [arXiv:2307.03172](https://arxiv.org/abs/2307.03172)
- **REF-002**: Hong, K., Troynikov, A., and Huber, J. (2025). *Context Rot: How Increasing Input
  Tokens Impacts LLM Performance*. Chroma Technical Report, July 2025.
  [trychroma.com/research/context-rot](https://www.trychroma.com/research/context-rot)
- **REF-003**: Anthropic Applied AI (2025). *Effective Context Engineering for AI Agents*.
  [anthropic.com/engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **REF-004**: Aider (2023). *Building a Better Repository Map with Tree-sitter*.
  [aider.chat/2023/10/22/repomap.html](https://aider.chat/2023/10/22/repomap.html)
- **REF-005**: Ouyang, S., Yu, W., Ma, K., Xiao, Z., Zhang, Z., Jia, M., Han, J., Zhang, H., and
  Yu, D. (2025). *RepoGraph: Enhancing AI Software Engineering with Repository-level Code Graph*.
  ICLR 2025. [arXiv:2410.14684](https://arxiv.org/abs/2410.14684)
- **REF-006**: Liu, X., Lan, B., Hu, Z., Liu, Y., Zhang, Z., Wang, F., Shieh, M., and Zhou, W.
  (2024). *CodexGraph: Bridging Large Language Models and Code Repositories via Code Graph
  Databases*. [arXiv:2408.03910](https://arxiv.org/abs/2408.03910)
- **REF-007**: Edge, D. et al. (2024). *From Local to Global: A Graph RAG Approach to
  Query-Focused Summarization*. [arXiv:2404.16130](https://arxiv.org/pdf/2404.16130) and
  [microsoft.github.io/graphrag](https://microsoft.github.io/graphrag/)
- **REF-008**: Shi, F., Chen, X., Misra, K., Scales, N., Dohan, D., Chi, E., Schärli, N., and
  Zhou, D. (2023). *Large Language Models Can Be Easily Distracted by Irrelevant Context*.
  [arXiv:2302.00093](https://arxiv.org/abs/2302.00093)
- **REF-009**: Wu, D., Wang, H., Yu, W., Zhang, Y., Chang, K.-W., and Yu, D. (2025). *LongMemEval:
  Benchmarking Chat Assistants on Long-Term Interactive Memory*.
  [arXiv:2410.10813](https://arxiv.org/abs/2410.10813)
- **REF-010**: Tetrate. *wazero: the zero-dependency WebAssembly runtime for Go developers*.
  [pkg.go.dev/github.com/tetratelabs/wazero](https://pkg.go.dev/github.com/tetratelabs/wazero)
- **REF-011**: Memor project. [CONCEPTS.md](../../CONCEPTS.md) — v1 storage model, compaction
  pipeline, and the "not a database" constraint superseded in part by this decision.
