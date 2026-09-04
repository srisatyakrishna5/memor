---
title: Memor Concepts
description: How repository state is indexed, tracked, ranked, and served
---

# Memor Concepts

How Memor works, end to end. Read [README.md](README.md) first for what it does;
this document explains why it is built the way it is. The reasoning behind the
current design, including the alternatives that were rejected and why the earlier
knowledge-graph model was abandoned, is in
[ADR-0002](docs/adr/adr-0002-static-repo-state.md).

---

## What Memor Solves

An AI coding assistant begins every session knowing nothing about your
repository — above all, not what has changed since it last worked in it. So it
rebuilds that picture from scratch, in a search-read-search loop that pulls whole
files into the context window. Three costs follow:

1. **Repetition cost.** The dominant one. The same exploration repeats every
   session because nothing is persisted between them, even when nothing changed.
2. **Direct token cost.** A 500-line source file is roughly 4,000 tokens. Ten
   such reads consume 40,000 tokens before any reasoning begins.
3. **Quality cost.** Token spend is not neutral. Accuracy degrades as inputs
   grow, and it degrades most when the relevant fact sits in the middle of a
   long context. A *plausible but wrong* result is worse than no result: a
   single topically-related distractor measurably reduces accuracy, and several
   compound the effect.

Memor keeps a static picture of the repository, tracks what moved since each
agent last looked, persists what past conversations learned, and serves all of it
cheapest-first inside a token budget.

---

## The Big Picture

Three layers of state, served through one escalation ladder.

```
  MANIFEST            what is here
  │  files, packages, symbols, docs — with purposes and spans
  │
  LEDGER              what moved
  │  indexed commit vs HEAD vs working tree, per-agent watermarks
  │
  JOURNAL             what you did
     decisions, fixes, workflows, preferences — attached to code

                         │
                         ▼
  repo_brief  →  repo_changes  →  repo_map  →  symbol_find  →  symbol_read
   ~600 tok        ~800 tok        2500 tok       ~700 tok        span only
```

An agent starts at the left and stops at the first step that answers the
question. Reading whole files sits off the right-hand end as the fallback.

Everything is stored as one **node** type. There is no traversal: relations are
metadata on the node they describe, so a query returns what it matched and
nothing adjacent to it.

### Node kinds

| Kind | Holds |
|---|---|
| `file` | A source file: path, line count, content hash, purpose, imports, dependents |
| `sym` | A function, method, type, or constant with its exact byte span, callers and callees |
| `pkg` | A directory or module |
| `doc` | A section of a markdown document |
| `mem` | A recorded decision, fix, workflow, or preference |

### Relation metadata

| Key | On | Holds | Produced by |
|---|---|---|---|
| `imports` | file | repo paths, package dirs, external specifiers | L0 extraction |
| `dependents` | file, pkg | repo paths that import it | L0 extraction |
| `symbols` | file | names defined in it | L1 extraction |
| `calls` / `callers` | sym | `path#name` of each end | L1 extraction |
| `tags` | any | topic tags | Memories and docs |
| `explains` | mem | node IDs the memory concerns | Recording a fact against code |
| `supersedes` | mem | node IDs it replaces | An explicit replacement |

`dependents` answers *"what breaks if I change this?"* — the question agents
actually ask — and is free to precompute at build time.

`explains` is the capability no comparable repository-map tool has. Others supply
structure. Memor can additionally bind *"a persistent index was rejected on
purpose"* to the exact symbol where a future agent would otherwise add one, and
it resurfaces there without anyone searching for it. Because node IDs are derived
from content rather than assigned, that binding survives a rebuild that deletes
and recreates the file node.

---

## Knowing What Changed

This is the layer that makes the rest worth having.

`memor build` records the commit it indexed in `state.json`. Each agent gets a
watermark in `marks.jsonl` — the last repository state it was shown. `repo_brief`
compares the two and advances the watermark, so the next call reports only what
happened after this one.

| Situation | Answer comes from |
|---|---|
| git repo, agent has been here | `git diff` from the agent's watermark, plus the dirty tree |
| git repo, first visit | `git diff` from the indexed commit, plus the dirty tree |
| no git | Stored file hashes compared against disk |

The hash fallback is less precise — it detects that a file differs from the
*index*, not from what the agent last *saw* — but it is never unavailable.

### Seen versus unseen

A commit is not enough on its own. Uncommitted work is the normal state of a
working tree, so the same forty dirty files appear in the change list every
single visit, and an agent told about all of them again has learned nothing from
the second visit onwards.

So a watermark also records the **content hash of every path it showed**. On the
next visit each change is classified:

- **unseen** — new to this agent, listed with its status and purpose.
- **seen** — already shown at this exact content, listed as a bare path.

Unseen changes sort first, so a truncated list drops what the agent already knows
rather than what is new to it. When nothing is unseen, the brief says so and
tells the agent to reuse what it has. Editing a file makes it unseen again,
because its hash no longer matches what was recorded.

Seen paths are still listed rather than hidden: a new conversation is a new
context window, and a bare path costs a few tokens where re-reading the file
costs thousands. A watermark is capped at `MaxTrackedPaths`; beyond it paths
fall out and are reported as new again, which over-reports rather than under-
reports.

git is invoked with an argument vector, never a shell string, and every revision
is validated as a hex object name before use, because revisions reach Memor from
MCP clients.

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
├── graph.log        # Append-only JSONL: nodes and tombstones
├── graph.snap       # Compacted canonical JSONL (lossless)
├── graph.db         # Rendered projection (write-only output)
├── state.json       # Commit and time of the last build
├── marks.jsonl      # Per-agent watermarks, one line each
├── graph.archive    # Evicted nodes
├── blobs/           # LRU body cache, hash-keyed, size-capped
└── .lock
```

Three rules keep this honest:

1. **`graph.log` is the only append target.** Every write in the system is a
   node record or a tombstone.
2. **`graph.snap` is the only lossless artifact.** Everything else regenerates
   from it — including the term index, which is rebuilt in memory on load and
   never written to disk, so it cannot drift out of sync with the store.
3. **`graph.db` is write-only.** Nothing parses it back, which is why Memor has
   no bespoke format parser to keep in sync with its writer.

`state.json` and `marks.jsonl` are rewritten atomically rather than appended:
both hold one current value per subject, so an append log would only create work
for a compaction pass.

---

## Append-Only Writes

Every write appends one JSONL record to `graph.log`:

```jsonl
{"o":"n","n":{"i":"7271f9a926dd","k":"mem","n":"s","x":"Archive before replacing the snapshot","t":1772800000,"m":{"t":"s","org":"agent","exp":"3b1c8e2a4f90"}}}
```

`o` is the operation: `n` (node) or `-n` (tombstone). Append is O(1), needs no
coordination beyond a short-lived lock, and survives a crash because a partial
line is simply skipped on read.

Multi-valued metadata such as `exp` (what a memory explains) or `imp` (imports)
is newline-joined inside the JSON string. A newline cannot appear in a path,
identifier or tag, and JSON escapes it, so every record stays on one line.

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
| `doc` | `source#section` |
| `mem` | The memory text itself |

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
| **L0** | Files, packages, imports, purpose lines | Line-oriented scan across Go, JS/TS, Python, Java, C#, Rust, Ruby, PHP |
| **L1** | Symbols, spans, signatures | `go/ast` for Go; declaration matching plus brace or indent bounding elsewhere |
| **L1+** | Calls and callers | Go only |
| **L2** | Summaries, patterns, decisions | Agent-authored, merged onto extracted nodes |

Go gets a real parser because it ships with the toolchain. Every other language
is recovered by matching declaration lines and then bounding the body by brace
depth or indentation:

| Language | Declarations recovered |
|---|---|
| TypeScript, JavaScript | `function`, `class`, `interface`, `type`, `enum`, and named `const`/`let`/`var` |
| Python | `def` and `class`, top level and one level in |
| Rust | `fn`, `struct`, `enum`, `trait`, `impl`, `type`, `const`, `static`, `macro_rules!` |
| Ruby | `def`, `class`, `module` |
| PHP | `function`, `class`, `interface`, `trait`, `enum` |
| Java, Kotlin, C#, Swift | Type declarations only |

The omissions are the point. Java and C# methods are `Type name(args)` with no
keyword to anchor on, and every heuristic that matches them also matches calls,
casts and field initializers. Destructuring bindings and nested closures are
skipped because no caller can reference them by name. **Calls and callers are
resolved for Go alone**: matching call sites by name without scope analysis
produces relations that look authoritative and are frequently wrong.

tree-sitter would do all of this properly and is not an option — its Go bindings
are mostly C, and CGO would break the cross-compiled binaries the npm installer
ships.

A **purpose line** is one sentence describing what a file is for, lifted from its
leading comment or docstring. It is deliberately shallow: a filename banner, a
licence header or a build directive yields nothing, because a wrong summary is
worse than none. An agent overrides it with `memor remember --summary`, and that
value wins on every later build.

`memor build` prunes every machine-derived node before re-extracting, so deleted
files and renamed symbols cannot linger as ghosts. Agent-authored memories and
summaries survive untouched: extraction knows structure but not intent, so it
must never blank a summary a human or agent wrote.

One exclusion is deliberate: **the Go standard library is not recorded.** Every
Go file imports `fmt` or `os`, so listing them would bury the handful of imports
that actually say something about the file.

---

## Retrieval

One pipeline, four stages:

```go
candidates := Candidates(query, tags, openFiles)  // BM25 plus exact name match
scored     := Score(candidates, query, changed)
kept       := gate(scored, minScore)              // precision floor
return pack(kept, budget)                         // position-aware
```

There is no expansion stage. A node that the query did not match is never a
candidate, so the result set cannot grow past what was asked for.

### Scoring

```
score = 0.45·BM25 + 0.20·recently-changed + 0.20·tags + 0.15·recency
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
symbols it defines, so a query naming a function reaches the file that defines
it rather than only the bare symbol.

**Recently-changed** boosts files that moved since the caller last looked. A
question asked during a piece of work is almost always about that work, and this
is the one signal a static index cannot supply on its own.

### The precision floor

`gate()` **drops rather than fills**. If nothing clears the threshold, the block
returned is short.

Underfilling is correct. One distractor measurably degrades output and four
compound it, so a short answer beats a padded one. A node matching neither the
query text nor a requested tag is masked before scoring even begins.

### Position-aware packing

`pack()` places the strongest results **first and last**, with the weakest
survivors in the middle. Accuracy is highest at both ends of a context window
and lowest in the middle, so emitting in plain descending order wastes the
recency-favoured tail.

Packing also suppresses redundancy. A memory rendered inline on the file it
explains is never also emitted standalone, and a symbol listed inside a selected
file block is never emitted on its own.

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

When enough of the index has drifted, `repo_map` and `memor_status` say so and
name the fix. A stale index is worse than no index.

---

## The Agent Surface

Seven MCP tools arranged as an **escalation ladder**, cheapest first. Bloated
tool sets and ambiguous tool selection are a leading agent failure mode, so each
tool answers a question the one above it could not.

| Tool | Answers | Budget |
|---|---|---|
| `repo_brief` | Where am I, what moved, what was I doing? | ≤ 600 tok |
| `repo_changes` | Which files exactly, and do they matter? | ≤ 800 tok |
| `repo_map` | Show me code I have not seen | 2500 tok default |
| `symbol_find` | Where is this one thing? | ≤ 700 tok |
| `symbol_read` | Show me its body | span size |
| `remember` | Record this for next time | ≤ 60 tok |
| `memor_status` | Can I trust the index? | ≤ 250 tok |

The server instructions state the ladder explicitly and name reading whole files
as the **fallback**, not the default. `repo_brief` advances the caller's
watermark as a side effect, because reading it is what makes it the agent's new
baseline — requiring a second call to confirm would cost tokens to say nothing.

Each description carries a **behavioural contract**, not an API summary. A
description that tells the model what to do with the result changes its
behaviour; one that only names the arguments does not.

This is also why behavioural rules live in tool descriptions rather than a
markdown template: they load with the tool, cannot be edited away by a user, and
are versioned with the binary. `memor init` therefore owns two files outside
`.memor/` — `.vscode/mcp.json`, because an MCP server cannot discover itself, and
a fenced three-line block in `AGENTS.md`, because Copilot cannot be told to read
a file it does not already know about.

The parsed store is cached across tool calls and invalidated when `state.json`
or the log changes, so a session of tool calls parses the snapshot once rather
than once per call.

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
min_score = 0.12           # Precision floor
max_symbols_per_file = 8
```

`graph.enabled = false` reverts Memor to storing and retrieving agent-authored
memories only, with no repository extraction.

---

## Upgrading

Memor is pre-1.0 and the on-disk format changes without a migration path. The
earlier knowledge-graph format is not read by the current binary.

```bash
memor export -o memories.jsonl   # with the old binary
memor clean --all
memor init --build               # with the new binary
memor import memories.jsonl
```

Only agent-authored nodes are worth carrying. Structural nodes are reproduced
from source by `memor build` in well under a second, so exporting them would be
exporting a derived artifact.

---

## Security And Privacy

- All data stays local. No cloud, no telemetry, no network calls.
- `.memor/` is gitignored by `memor init`.
- Bodies are never copied into `.memor/`. The store holds coordinates into
  files git already tracks.
- The blob cache is keyed by content hash and capped in size.
- git is invoked with an argument vector, never a shell string, and revisions
  are validated as hex object names before use.
- Never store secrets, API keys, passwords, or PII in memories.

---

## Deliberate Omissions

| Not included | Why |
|---|---|
| Graph traversal and PageRank | Tried and removed. Expansion returned the neighbourhood of a match, which is the distractor class that hurts most, and structural rank made heavily-imported files surface for questions they had nothing to do with. See [ADR-0002](docs/adr/adr-0002-static-repo-state.md) |
| Embeddings and vector search | Requires a model over the network or bundled; similarity returns code that *resembles* the query, which is again the distractor problem |
| A graph database or query language | Requires a persistent server and a large prompt surface for the agent to learn |
| LLM-based extraction | Costs a model call per text unit, scaling with repository size, and makes indexing non-deterministic |
| A daemon or background worker | Each command performs one bounded operation and exits |
| Mirroring source into `.memor/` | Duplicates `.git/objects`, and two sources of truth make staleness undetectable without hashing on every read |
| Java/C# method extraction | `Type name(args)` has no keyword to anchor on, and every heuristic for it also matches calls, casts and field initializers |
| Call graphs outside Go | Name matching without scope analysis produces relations that look authoritative and are often wrong |
| Committing the store | Deferred, not rejected. It needs a merge driver and `.gitattributes` work that is not on the critical path |

---

## Where To Go Next

- [README.md](README.md) — installation, commands, and quick start
- [ADR-0002](docs/adr/adr-0002-static-repo-state.md) — why the knowledge graph was replaced, and what was considered instead
- [ADR-0001](docs/adr/adr-0001-memor-v2-code-knowledge-graph.md) — superseded, kept for the evidence base and the storage rules that still hold
- `memor rules` — the protocol Memor expects an agent to follow
- `internal/graph/` — nodes, log, snapshot, index, projection
- `internal/vcs/` — the git reads behind the change ledger
- `internal/retrieve/` — the single ranking and packing pipeline
