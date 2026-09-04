---
title: Memor Concepts
description: How the repository knowledge graph is built, stored, ranked, and served
---

# Memor Concepts

How Memor works, end to end. Read [README.md](README.md) first for what it does;
this document explains why it is built the way it is. The reasoning behind the
v2 design, including the alternatives that were rejected, is in
[ADR-0001](docs/adr/adr-0001-memor-v2-code-knowledge-graph.md).

---

## What Memor Solves

An AI coding assistant begins every session with no structural model of your
repository. Answering *"where is authentication handled and what calls it?"*
requires a search-read-search loop that pulls whole files into the context
window. Three costs follow:

1. **Direct token cost.** A 500-line source file is roughly 4,000 tokens. Ten
   such reads consume 40,000 tokens before any reasoning begins.
2. **Quality cost.** Token spend is not neutral. Accuracy degrades as inputs
   grow, and it degrades most when the relevant fact sits in the middle of a
   long context. A *plausible but wrong* result is worse than no result: a
   single topically-related distractor measurably reduces accuracy, and several
   compound the effect.
3. **Repetition cost.** The same exploration repeats every session, because
   nothing is persisted.

Memor indexes the repository into one graph, persists what past conversations
learned, and serves a task-ranked map inside a token budget.

---

## The Big Picture

Everything is a **node**. Nodes are connected by typed **edges**.

```
                    ┌────────────┐
                    │   topic    │◄──── tagged ──── any node
                    └────────────┘

  ┌────────┐             ┌────────┐            ┌────────┐
  │  pkg   │─ contains ─►│  file  │─ contains ─►│  sym   │
  └────────┘             └────────┘             └────────┘
                          │      ▲                │    ▲
                    imports      └── imports ──┐  │    │
                          ▼                    │  └ calls
                    ┌────────┐            ┌────────┐
                    │  ext   │            │  file  │
                    └────────┘            └────────┘
                                               ▲
  ┌────────┐                                   │
  │  mem   │──────────── explains ─────────────┘
  └────────┘
```

v1 had three parallel subsystems — memories, knowledge, and code — each with its
own storage, reader, and ranker. `Context()` therefore built **two independent
BM25 indexes** and ran **two packing loops** against a single budget, which is
what produced silent duplicate emission and silent drops. One node type collapses
that seam, so a single index and a single packer make the defect inexpressible.

### Node kinds

| Kind | Holds |
|---|---|
| `file` | A source file: path, line count, content hash, summary |
| `sym` | A function, method, type, or constant with its exact byte span |
| `pkg` | A directory or module |
| `ext` | A third-party dependency with no body in this repository |
| `doc` | A section of a markdown document |
| `mem` | A recorded decision, fix, workflow, or preference |
| `topic` | A tag, promoted to a first-class node |

### Edge kinds

| Edge | From → To | Produced by |
|---|---|---|
| `imports` | file → file, pkg, or ext | L0 extraction |
| `contains` | pkg → file, file → sym | L0 / L1 extraction |
| `calls` | sym → sym | L1 extraction |
| `refs` | sym → sym, non-call use | L1 extraction |
| `tagged` | any → topic | Tags on memories and docs |
| `supersedes` | node → node | An explicit replacement |
| `explains` | mem → file or sym | Recording a fact against code |

`explains` is the capability no comparable repository-map tool has. Others supply
structure. Memor can additionally bind *"a persistent index was rejected on
purpose"* to the exact symbol where a future agent would otherwise add one, and
it resurfaces there without anyone searching for it. It costs one edge.

---

## Spans, Not Copies

A symbol node stores `path + byte range + line range + content hash + signature`.
The body stays on disk in the repository and is fetched on demand.

| Requirement | Why a span satisfies it |
|---|---|
| Index repository content | Every byte is addressable and retrievable in one seek |
| Work offline | Reading a byte range from a local file is as offline as it gets |
| Stay native to the repository | The graph is coordinates into the repo, with no second copy to drift |
| Minimal footprint | Storing bodies would duplicate `.git/objects`, already a compressed content-addressed store of the same bytes |
| Minimal tokens | The agent gets a ~12-token pointer and pulls a ~200-token body only when it decides it needs one |

Spans also keep staleness detectable **per file**: a hash mismatch identifies
exactly one drifted file. Storing bodies would create two sources of truth and
force a disk hash on every read — the I/O the design set out to avoid.

Bodies are cached lazily under `blobs/`, never mirrored eagerly. A body enters
the cache only when `symbol_read` serves it, keyed by its content hash, so
serving a stale entry is structurally impossible.

---

## Local Files And Their Roles

```
.memor/
├── config.toml      # Configuration
├── graph.log        # Append-only JSONL: nodes, edges, tombstones
├── graph.snap       # Compacted canonical JSONL (lossless)
├── graph.db         # Rendered projection (write-only output)
├── graph.idx        # DERIVED: postings and PageRank. Deletable
├── graph.archive    # Evicted nodes
├── blobs/           # LRU body cache, hash-keyed, size-capped
└── .lock
```

Four rules keep this honest:

1. **`graph.log` is the only append target.** Every write in the system is a
   node, an edge, or a tombstone record.
2. **`graph.snap` is the only lossless artifact.** Everything else regenerates
   from it.
3. **`graph.idx` is disposable by definition.** Delete it and the next command
   rebuilds it. The compact binary-ish format lives here and nowhere else, which
   buys load speed without creating an opaque source of truth.
4. **`graph.db` is write-only.** Nothing parses it back. This is why v2 has no
   bespoke format parser to keep in sync with its writer — v1 needed one only
   because `knowledge.db` was both a projection and an input.

---

## Append-Only Writes

Every write appends one JSONL record to `graph.log`:

```jsonl
{"o":"n","n":{"i":"7271f9a926dd","k":"mem","n":"s","x":"Archive before replacing the snapshot","t":1772800000,"m":{"t":"s","org":"agent"}}}
{"o":"e","g":{"f":"7271f9a926dd","t":"3b1c8e2a4f90","k":"explains","w":1}}
```

`o` is the operation: `n` (node), `e` (edge), `-n` and `-e` (tombstones). Append
is O(1), needs no coordination beyond a short-lived lock, and survives a crash
because a partial line is simply skipped on read.

A malformed line warns and is skipped. One bad record must never destroy the
store.

---

## Content-Addressed IDs

A node's ID is `sha256(kind + "\0" + normalize(identity))[:12]`, where
`normalize` lowercases and trims.

| Kind | Identity |
|---|---|
| `file` | Project-relative path |
| `sym` | `path#name` — so two same-named symbols in different files stay distinct |
| `pkg` | Directory |
| `ext` | Import specifier |
| `doc` | `source#section` |
| `mem` | The memory text itself |
| `topic` | The tag |

Three properties follow:

- **Free deduplication.** Recording the same fact twice yields the same ID, so
  it collapses to one node rather than accumulating.
- **Idempotent tools.** An agent that retries `remember` cannot create a
  duplicate.
- **Stable references.** `supersedes` and `explains` can name a node without a
  lookup table.

Including the kind in the hash means a file named the same as a memory's text
cannot collide with it.

---

## Extraction

Extraction is **tiered** and **deterministic**. No tier makes a network call,
invokes a model, or requires CGO — the last of which matters because the npm
installer ships cross-compiled binaries per platform, and tree-sitter's Go
bindings are mostly C.

| Tier | Scope | Mechanism |
|---|---|---|
| **L0** | Files, packages, imports | Line-oriented scan across Go, JS/TS, Python, Java, C#, Rust, Ruby, PHP |
| **L1** | Symbols, spans, signatures, calls | `go/ast` from the standard library |
| **L2** | Summaries, patterns, decisions | Agent-authored, merged onto extracted nodes |

`memor build` prunes every machine-derived node before re-extracting, so deleted
files and renamed symbols cannot linger as ghosts. Agent-authored memories and
summaries survive untouched: extraction knows structure but not intent, so it
must never blank a summary a human or agent wrote.

Two exclusions are deliberate:

- **The Go standard library is not modelled.** Every Go file imports `fmt` or
  `os`, so including them would make `fmt` the highest-PageRank node in the
  repository while carrying no signal about it.
- **External dependency edges are damped.** A third-party package is worth
  showing but must not accumulate rank: it is imported by many files and depends
  on none of them.

---

## Retrieval

One pipeline, four stages:

```go
seeds    := Seeds(query, tags, openFiles)   // BM25 plus exact name match
frontier := Expand(seeds, maxHops)          // weighted graph walk
scored   := Score(frontier, query)
kept     := gate(scored, minScore)          // precision floor
return pack(kept, budget)                   // position-aware
```

### Scoring

```
score = 0.30·BM25 + 0.25·proximity + 0.20·PageRank + 0.15·tags + 0.10·recency
```

The result is multiplied by a kind weight (files outrank symbols, symbols
outrank bare package nodes) and by a test-file penalty, because a test mentions
every term the code under test mentions and would otherwise crowd out the
implementation that answers the question.

**BM25 with shared stemming.** Indexing and querying pass through the same
tokenizer and the same conservative suffix stripper. Without it a
natural-language query and a machine-shaped identifier never meet: "compaction"
and `Compact` share no token, so the file implementing compaction would score
zero against a question about it. A file node also inherits the names of the
symbols it contains, so a query naming a function reaches the file that defines
it rather than only the bare symbol.

**Proximity** is a decayed walk outward from the seeds, weighted per edge kind.
A file contains dozens of symbols and a topic tags dozens of nodes, so
propagating those at full strength would flood the frontier with everything
structurally adjacent to one good hit.

**PageRank** is computed offline at build time over the edge graph. Structure
earns a node relevance independently of the query, which is what stops a
rarely-named but heavily depended-on file from being invisible.

### The precision floor

`gate()` **drops rather than fills**. If nothing clears the threshold, the block
returned is short.

Underfilling is correct. One distractor measurably degrades output and four
compound it, so a short answer beats a padded one. Structural weight alone
cannot clear the floor either: a node must be lexically matched or directly
adjacent to something that was. Without that rule a heavily depended-on symbol
has high PageRank in *every* query and gets returned for questions it has
nothing to do with.

### Position-aware packing

`pack()` places the strongest results **first and last**, with the weakest
survivors in the middle. Accuracy is highest at both ends of a context window
and lowest in the middle, so emitting in plain descending order wastes the
recency-favoured tail.

Packing also suppresses redundancy. A memory rendered inline on the file it
explains is never also emitted standalone, and a symbol listed inside a selected
file block is never emitted on its own. This is the guard that makes the v1
double-emission defect inexpressible.

---

## Compaction

Compaction folds `graph.log` into `graph.snap`:

1. **Parse** — read the snapshot, then the log, so later records win.
2. **Decay** — score memory nodes; archive those below the threshold, plus
   expired and superseded ones. Structural nodes are never archived: extraction
   regenerates them, so archiving would only churn the archive.
3. **Commit** — archive **before** replacing the active snapshot, so a failure
   mid-write is retryable rather than lossy.
4. **Truncate** — remove only the prefix of the log that was actually consumed.
5. **Project** — regenerate `graph.db` and drop the derived index.

A memory's retention score is its subtype weight, decayed by age, boosted by how
many nodes it connects to. A decision explaining three files outranks an isolated
note of the same age. Preferences do not decay: they are stated once and expected
to hold.

`AutoCompact` runs opportunistically after a write once the log passes a
threshold. If another process holds the lock it **skips rather than blocks**, and
it writes nothing to stdout so it cannot corrupt MCP's JSON-RPC framing.

---

## Concurrency And Recovery

A long-lived MCP server writes alongside CLI invocations, so these invariants are
load-bearing and each has a test behind it:

| # | Invariant | Why it matters |
|---|---|---|
| I1 | One lock per `.memor/` directory, not per file | The only reason compaction can span archive, snapshot, and truncate atomically |
| I2 | Commit order: archive, then snapshot, then truncate | A failure at any point leaves a retryable state |
| I3 | Truncate only the consumed prefix | An append racing a compaction survives; `Truncate(0)` would silently drop it |
| I4 | Locked and unlocked function variants are split | Compaction cannot self-deadlock |
| I5 | Lock contention means skip, not fail, for auto-compaction | Housekeeping never stalls a tool call |
| I6 | The OS releases the lock on process exit | No stale-lock recovery code, no lease, no PID file |
| I7 | Auto-compaction writes nothing to stdout | A stray print corrupts MCP stdio framing |
| I8 | Content-addressed IDs | Free deduplication and idempotent re-adds |
| I9 | A malformed line warns and continues | One bad record cannot destroy the store |

Snapshot writes are atomic: content is written to a temporary file and renamed
into place, with a move-aside fallback because Windows refuses to rename over an
open file.

---

## Staleness

Every file node carries the content hash recorded when it was indexed.

- **fresh** — the file on disk still matches.
- **stale** — the file changed since indexing.
- **missing** — the file no longer exists.

`symbol_read` refuses to serve a span from a drifted file. Handing back the wrong
lines under a correct-looking line number is worse than refusing, because the
agent has no way to detect it.

When enough of the graph has drifted, `repo_map` and `graph_status` say so and
name the fix. A stale graph is worse than no graph.

---

## The Agent Surface

Five MCP tools. Bloated tool sets and ambiguous tool selection are a leading
agent failure mode, so capability is folded into existing tools rather than added
alongside them.

| Tool | Replaces |
|---|---|
| `repo_map` | Reading files to find out where anything is |
| `symbol_find` | grep and workspace search |
| `symbol_read` | Whole-file reads |
| `remember` | Losing the decision when the conversation ends |
| `graph_status` | Guessing whether the map is trustworthy |

Each description carries a **behavioural contract**, not an API summary. A
description that tells the model what to do with the result changes its
behaviour; one that only names the arguments does not.

This is also why behavioural rules live in tool descriptions rather than a
markdown template: they load with the tool, cannot be edited away by a user, and
are versioned with the binary. `memor init` therefore owns two files outside
`.memor/` — `.vscode/mcp.json`, because an MCP server cannot discover itself, and
a fenced three-line block in `AGENTS.md`, because Copilot cannot be told to read
a file it does not already know about.

---

## Configuration

`.memor/config.toml`. Every setting has a working default, and a partial file
keeps the defaults for everything it omits — otherwise an author who sets only
`token_budget` would silently get a zero `min_score` and lose the precision gate.

```toml
[memory]
token_budget = 15000       # Retrieval budget
log_max_records = 64       # Auto-compaction threshold
max_memory_nodes = 2000    # Retention cap

[memory.decay]
rate = 0.03                # Age decay per day
min_score = 0.1            # Archive threshold

[graph]
enabled = true             # Kill switch: false disables all extraction
symbols = true             # L1 symbol extraction
max_file_kb = 512          # Skip generated bundles

[graph.cache]
enabled = true
max_bytes = 262144         # 256 KB hard cap on the body cache

[retrieval]
max_hops = 2
min_score = 0.12           # Precision floor
max_symbols_per_file = 8
```

`graph.enabled = false` reverts Memor to storing and retrieving agent-authored
memories only, with no repository extraction.

---

## Migrating From v1

A v1 store is migrated automatically on the first v2 command; `memor migrate`
makes it explicit. It is idempotent, because a second run finds no v1 files.

| v1 | v2 |
|---|---|
| `Entry{Type: s/e/p/f}` | `Node{Kind: mem}` with the subtype in metadata |
| `Entry.Tags` | `tagged` edges to `topic` nodes |
| `Entry.Supersedes` | A `supersedes` edge, with IDs remapped |
| `CodeMeta.FilePath`, `LOC`, `Hash` | `Node{Kind: file}` plus a span |
| `CodeMeta.Exports` | `contains` edges to `sym` nodes |
| `CodeMeta.Deps` | `imports` edges |
| `CodeMeta.Summary` | `Node.Text` |
| `KnowledgeSection` | `Node{Kind: doc}` |

v1 `Deps` were persisted but never traversed. Migration is where they become
load-bearing, so an existing user gets a partial graph immediately — before any
extraction runs.

The v1 files are renamed to `*.v1.bak` rather than deleted. A migration that
destroys the only copy of the data is not a migration.

---

## Security And Privacy

- All data stays local. No cloud, no telemetry, no network calls.
- `.memor/` is gitignored by `memor init`.
- Bodies are never copied into `.memor/`. The graph stores coordinates into
  files git already tracks.
- The blob cache is keyed by content hash and capped in size.
- Never store secrets, API keys, passwords, or PII in memories.

---

## Deliberate Omissions

| Not included | Why |
|---|---|
| Embeddings and vector search | Requires a model over the network or bundled; similarity returns code that *resembles* the query, which is exactly the distractor class that hurts most. "Calls", "imports", and "defined-in" are the relations an agent needs, and similarity does not encode them |
| A graph database or query language | Requires a persistent server and a large prompt surface for the agent to learn. Progressive disclosure is achieved with one narrow expansion tool instead |
| LLM-based extraction | Costs a model call per text unit, scaling with repository size, and makes indexing non-deterministic |
| A daemon or background worker | Each command performs one bounded operation and exits |
| Mirroring source into `.memor/` | Duplicates `.git/objects`, and two sources of truth make staleness undetectable without hashing on every read |
| Committing the graph | Deferred, not rejected. It needs a merge driver and `.gitattributes` work that is not on the critical path |

---

## Where To Go Next

- [README.md](README.md) — installation, commands, and quick start
- [ADR-0001](docs/adr/adr-0001-memor-v2-code-knowledge-graph.md) — the full design rationale, evidence, and rejected alternatives
- `memor rules` — the protocol Memor expects an agent to follow
- `internal/graph/` — nodes, edges, log, snapshot, index, projection
- `internal/retrieve/` — the single ranking and packing pipeline
