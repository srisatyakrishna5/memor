---
title: Memor
description: Persistent repository state and memory for AI coding assistants
---

<h1 align="center">Memor</h1>

<p align="center">
  <strong>Persistent repository state and memory for AI coding assistants.</strong>
</p>

<p align="center">
  <a href="https://github.com/akashchekka/memor/releases"><img src="https://img.shields.io/github/v/release/akashchekka/memor?style=flat-square&color=blue" alt="GitHub Release"></a>
  <a href="https://github.com/akashchekka/memor/actions/workflows/release.yml"><img src="https://img.shields.io/github/actions/workflow/status/akashchekka/memor/release.yml?style=flat-square&label=build" alt="Build Status"></a>
  <a href="https://www.npmjs.com/package/@memor-dev/memor"><img src="https://img.shields.io/npm/v/@memor-dev/memor?style=flat-square&color=cb3837" alt="npm"></a>
  <a href="https://github.com/akashchekka/memor/blob/main/LICENSE"><img src="https://img.shields.io/github/license/akashchekka/memor?style=flat-square" alt="License"></a>
  <a href="https://goreportcard.com/report/github.com/akashchekka/memor"><img src="https://goreportcard.com/badge/github.com/akashchekka/memor?style=flat-square" alt="Go Report Card"></a>
</p>

<p align="center">
  Every AI coding tool starts each conversation knowing nothing about your repository — above all, not what changed since it last worked here. So it rebuilds that picture from scratch every session, in search-read-search loops that pull whole files into the context window. Memor remembers the repository between sessions and hands the assistant a few hundred tokens instead.
</p>

<p align="center">
  <em>Local files. Ranked context. Zero cloud, zero daemon, zero git commits.</em>
</p>

---

## Why

| Problem | Without Memor |
|---|---|
| No idea what changed since last session | Every conversation re-derives the whole repository |
| No structural model of the repository | Search-read-search loops before any real work starts |
| Whole files read to find forty relevant lines | Tokens spent on content the model does not need |
| Longer context is not free | Accuracy degrades as irrelevant content accumulates |
| Decisions live in chat history | The rejected alternative gets re-implemented |

Token spend is not the only cost. Model accuracy falls as inputs grow, and a
plausible-but-wrong result hurts more than a missing one, so Memor optimizes for
precision and returns a short block rather than a padded one.

---

## How It Works

Memor keeps three layers of state about your repository and serves them cheapest-first.

- **Manifest** — what is here. `memor build` walks the repository and records
  files, packages, imports, a one-line purpose per file, and symbols with exact
  byte spans across Go, TypeScript, JavaScript, Python, Rust, Ruby, PHP, Java,
  Kotlin, C# and Swift. Deterministic: no network call, no model call, no CGO.
- **Change ledger** — what moved. Every build records the commit it indexed, and
  every agent gets a watermark of the last state it was shown, so "what changed
  since last time?" is a `git diff` between two commits Memor already knows.
  The watermark also holds a content hash per path, so a file that is still
  dirty but unchanged since your last visit is marked seen instead of being
  described to you again. Outside a git work tree it compares file hashes.
- **Journal** — what you did. Decisions, fixes, workflows and preferences,
  attached to the files and symbols they concern, with an optional task and
  status so unfinished work is handed back next session.

Underneath:

- **Spans, not copies** — a symbol stores `path + byte range + content hash +
  signature`. Bodies stay on disk, so there is one source of truth and staleness
  stays detectable per file.
- **Write path** — everything appends to `graph.log` as JSONL. Nodes are
  content-addressed, so recording the same fact twice collapses to one node.
- **Read path** — one pipeline: match lexically, blend BM25 with recent-change,
  tag and recency signals, drop anything below a precision floor, then pack the
  survivors with the strongest results at both ends of the block. Nothing is
  traversed, so a query can never return a file's neighbours instead of an answer.
- **Compaction** — folds the log into `graph.snap`, archives decayed memories,
  and regenerates the rendered projection.

Relations — imports, dependents, callers, callees — are recorded as metadata on
the node they describe. They answer "what breaks if I change this?" without
letting retrieval wander.

---

## Quick Start

### Install

```bash
# npm (recommended)
npm i -g @memor-dev/memor

# or use directly without installing
npx @memor-dev/memor init
```

### Initialize and index

```bash
cd your-project
memor init --build
```

This creates `.memor/`, gitignores it, registers the MCP server in
`.vscode/mcp.json`, adds a three-line pointer to `AGENTS.md`, and indexes the
repository.

Use `memor init --tools claude,cursor` to register additional MCP hosts.

### Start a session

```bash
memor brief
```

Repository identity, layout, what changed since you last looked, and any
unfinished work — in a few hundred tokens. This is the first thing an agent
should read.

### See what moved

```bash
memor changes
memor changes --since a1b2c3d
```

### Get the map for a task

```bash
memor context --query "where is compaction handled and what breaks if I change it"
```

### Find and read a symbol

```bash
memor symbol find Compact
memor symbol read Compact
```

`symbol read` returns the lines the symbol occupies, not the file around them.

### Record a decision where it belongs

```bash
memor remember "Archive before replacing the snapshot so a failure mid-write is retryable" \
  --tag compaction --file internal/graph/snap.go
```

### Check the index

```bash
memor status
memor search "import extraction"
```

---

## On-Disk Layout

```
<project>/
├── .memor/              # Per-project state (gitignored)
│   ├── graph.log        # Append-only JSONL: nodes and tombstones
│   ├── graph.snap       # Lossless canonical snapshot
│   ├── graph.db         # Rendered projection (write-only; nothing parses it back)
│   ├── state.json       # Commit and time of the last build
│   ├── marks.jsonl      # Per-agent watermarks: the last state each agent saw
│   ├── graph.archive    # Evicted nodes
│   ├── blobs/           # Size-capped LRU body cache
│   └── config.toml
├── .vscode/mcp.json     # MCP server registration
└── AGENTS.md            # Three-line pointer
```

Three rules keep this honest:

1. `graph.log` is the only append target.
2. `graph.snap` is the only lossless artifact; everything else regenerates from it.
3. `graph.db` is write-only, which is why Memor has no bespoke format parser to
   keep in sync with its writer.

---

## Node Kinds

| Kind | Holds |
|---|---|
| `file` | A source file: path, line count, content hash, purpose, imports, dependents |
| `sym` | A function, method, type, or constant with its exact span, callers and callees |
| `pkg` | A directory or module |
| `doc` | A section of a markdown document |
| `mem` | A recorded decision, fix, workflow, or preference |

Relations are metadata lists on the node itself — `imports`, `dependents`,
`calls`, `callers`, `tags`, and the files or symbols a memory explains. They are
reported, never traversed.

---

## Language Support

| Language | Files, imports, purpose | Symbols and spans | Calls and callers |
|---|---|---|---|
| Go | yes | yes (`go/ast`) | yes |
| TypeScript, JavaScript | yes | yes | no |
| Python | yes | yes | no |
| Rust | yes | yes | no |
| Ruby, PHP | yes | yes | no |
| Java, Kotlin, C#, Swift | yes | types only | no |
| C, C++ | yes | no | no |

Only Go has a real parser, because it ships with the toolchain. Everything else
matches declaration lines and bounds the body by brace depth or indentation —
less accurate on purpose. Java and C# methods are `Type name(args)` with no
keyword to anchor on, so they are skipped rather than guessed at, and calls are
resolved for Go alone because name matching without scope analysis produces
relations that look authoritative and are often wrong.

---

## Memory subtypes

| Prefix | Type | Use For |
|---|---|---|
| `@s` | Semantic | Facts, decisions, architecture choices |
| `@e` | Episodic | Events, bugs fixed, migrations completed |
| `@p` | Procedural | Commands, workflows, how-tos |
| `@f` | Preference | Developer style preferences (permanent) |

---

## Rendered projection (`graph.db`)

```
@g v3 | 62 files | 591 symbols | 169 docs | 2 memories | built:2026-09-04T07:02:00Z

@f internal/graph/snap.go [275 LOC | 7511f4] pkg:internal/graph
  : Loads, compacts and rebuilds the canonical snapshot
  func Load(paths store.Paths) (*Graph, error)                          @15-33
  func Compact(paths store.Paths, cfg config.Config) (int, int, error)   @61-69
  func AutoCompact(paths store.Paths, cfg config.Config) (...)           @125-150
  -> internal/config, internal/store
  <- cmd/compact.go, internal/session/session.go
  ~ "Archive before replacing the snapshot so a failure mid-write is retryable" [semantic, 2026-09-03]
```

`->` is what the file imports. `<-` is what imports it, which answers "what
breaks if I change this?" and is free to precompute. `~` is a memory attached to
the file, rendered in place.

That block is roughly 90 tokens and substitutes for a 3,400-token file read.

## Log format (`graph.log`)

```jsonl
{"o":"n","n":{"i":"7271f9a926dd","k":"mem","n":"s","x":"Archive before replacing the snapshot","t":1772800000,"m":{"t":"s","org":"agent","exp":"3b1c8e2a4f90"}}}
```

---

## CLI Reference

| Command | Description |
|---|---|
| `memor init` | Create `.memor/`, register the MCP server, add the `AGENTS.md` pointer |
| `memor build` | Index the repository and record the commit it indexed |
| `memor brief` | Where you are, what changed, what is unfinished (start here) |
| `memor changes` | Files changed since the last build or a given commit, new ones first |
| `memor status` | Node counts, staleness, indexed commit, footprint |
| `memor context` | Print the task-ranked map |
| `memor remember` | Record a decision, fix, workflow, preference, or file summary |
| `memor search <query>` | List matching nodes with scores and IDs |
| `memor symbol find <name>` | Locate a definition with its span, callers, and callees |
| `memor symbol read <name>` | Print the exact lines a symbol occupies |
| `memor compact` | Fold pending writes into the snapshot |
| `memor export` / `import` | Move memories between machines as JSONL |
| `memor clean` | Remove derived artifacts, or `--all` for the whole footprint |
| `memor rules` | Print the agent protocol |
| `memor mcp` | Serve repository state over the Model Context Protocol |

---

## Retrieval

One pipeline ranks the store with no embeddings, vector database, or network call:

```
score = 0.45·BM25 + 0.20·recently-changed + 0.20·tags + 0.15·recency
```

- **BM25 with shared stemming** — indexing and querying stem identically, so a
  question about "compaction" reaches a function named `Compact`.
- **Recent-change boost** — a file you just touched outranks one you did not,
  because the question is almost always about the work in progress.
- **Matches only** — a node that matches neither the query nor a requested tag is
  never scored. There is no graph walk, so a result set cannot grow past what the
  query named.
- **A precision floor** — results below the threshold are dropped, not replaced.
- **Position-aware packing** — strongest results take the head and the tail,
  weakest survivors sit in the middle where they cost least.

---

## AI Tool Integration

Memor speaks the Model Context Protocol, so hosts surface it as native tools with
typed arguments rather than prose an agent may ignore. The tools form a ladder,
cheapest first — an agent starts at the top and escalates only when it has to:

| Tool | Answers | Replaces |
|---|---|---|
| `repo_brief` | Where am I, what moved, what was I doing? | Re-deriving the repository every session |
| `repo_changes` | Which files exactly, and do they matter? | Diffing and re-reading the tree |
| `repo_map` | Show me code I have not seen | Reading files to find where anything is |
| `symbol_find` | Where is this one thing? | grep and workspace search |
| `symbol_read` | Show me its body | Whole-file reads |
| `remember` | Record this for next time | Losing the decision when the conversation ends |
| `memor_status` | Can I trust the index? | Guessing whether the map is trustworthy |

Reading whole files is the documented fallback, not the default. Pass a stable
`agent` identifier to `repo_brief` and `repo_changes` so each client gets its own
watermark and is told only what is new to it.

Behavioural rules live in the tool descriptions rather than a markdown template:
they load with the tool, cannot be edited away, and are versioned with the
binary. `memor init` therefore owns two files outside `.memor/`:
`.vscode/mcp.json` and a fenced three-line block in `AGENTS.md`.

Run `memor rules` to print the protocol.

---

## Configuration

`.memor/config.toml` — every setting has a working default, and a partial file
keeps the defaults for everything it omits:

```toml
[memory]
token_budget = 15000       # Retrieval budget
log_max_records = 64       # Auto-compaction threshold

[memory.decay]
rate = 0.03                # Age decay per day
min_score = 0.1            # Archive threshold

[graph]
enabled = true             # Kill switch: false disables all extraction
symbols = true             # L1 symbol extraction
max_file_kb = 512          # Skip generated bundles

[retrieval]
min_score = 0.12           # Precision floor
max_symbols_per_file = 8
```

---

## Design Principles

1. **Local-first, no infrastructure** — just files on disk
2. **Remember, don't re-derive** — the cheapest context is the context an agent
   already had last session
3. **One node type, one index, one packer** — no seam for duplicate emission to
   hide in
4. **Report relations, never traverse them** — a query cannot return a file's
   neighbours instead of an answer
5. **Spans, not copies** — coordinates into the repository, never a second copy of it
6. **Deterministic extraction** — no network call, no model call, no CGO
7. **Precision over recall** — a short answer beats a padded one
8. **Derived data is disposable** — anything regenerable is deletable
9. **Append-only writes, compacted reads** — LSM-tree inspired architecture
10. **Zero-config start** — `memor init --build` and done

The complete architecture, data flow, scoring, persistence, and recovery model
is documented in [CONCEPTS.md](CONCEPTS.md). The reasoning behind the current
design, including the alternatives that were rejected and why the earlier
knowledge-graph model was abandoned, is in
[ADR-0002](docs/adr/adr-0002-static-repo-state.md), which supersedes
[ADR-0001](docs/adr/adr-0001-memor-v2-code-knowledge-graph.md).

---

## Upgrading

Memor is pre-1.0 and the on-disk format changes without a migration path. To
move an existing store forward:

```bash
memor export -o memories.jsonl   # with the old binary
memor clean --all
memor init --build               # with the new binary
memor import memories.jsonl
```

Structural nodes are not worth carrying: `memor build` reproduces them from
source in well under a second.

---

## Contributing

Contributions are welcome! Memor is written in Go 1.25+.

### Setup

```bash
git clone https://github.com/akashchekka/memor.git
cd memor
go build -o memor
```

### Run tests

```bash
go test ./...

# Coverage must name the packages explicitly: the tests live outside them.
go test ./tests/ -coverpkg=./internal/... -cover
```

### Project structure

```
main.go              # Entry point
cmd/                 # CLI commands (cobra)
internal/
  config/            # config.toml parsing
  constants/         # Centralized constants
  graph/             # Nodes, edges, log, snapshot, index, projection, migration
    extract/         # L0 imports, L1 Go symbols, knowledge docs
  mcp/               # Model Context Protocol server (5 tools)
  retrieve/          # The single ranking and packing pipeline
  session/           # Shared entry point for the CLI and the MCP server
  store/             # Locking, append-only records, atomic writes, paths
  token/             # Token counting
tests/               # The whole suite, exercising only the exported API
```

### Guidelines

- Keep the CLI fast — every command should complete in well under a second
- No external services or network calls
- Run `go test ./...` before submitting a PR
- Tests live in `tests/`, so they reach the code only through exported API.
  If a test needs an unexported helper, that is a signal the seam should be
  exported deliberately rather than reached around
- Follow existing code style (no linter config needed, just match what's there)


---

## Tech Stack

- **Go 1.25** — single static binary, no runtime dependencies, no CGO
- **Cobra** — CLI framework
- **TOML** — `pelletier/go-toml`
- **MCP Go SDK** — `modelcontextprotocol/go-sdk`

---

## Security & Privacy

- All data stays local — no cloud, no telemetry, no network calls
- `.memor/` is gitignored by default — never committed
- Bodies are never copied into `.memor/`; the graph stores coordinates into files git already tracks
- Never store secrets, API keys, passwords, or PII in memories

---

## License

See [LICENSE](LICENSE) for details.

