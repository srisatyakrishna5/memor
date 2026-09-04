---
title: Memor
description: Repository knowledge graph and persistent memory for AI coding assistants
---

<h1 align="center">Memor</h1>

<p align="center">
  <strong>Repository knowledge graph and persistent memory for AI coding assistants.</strong>
</p>

<p align="center">
  <a href="https://github.com/akashchekka/memor/releases"><img src="https://img.shields.io/github/v/release/akashchekka/memor?style=flat-square&color=blue" alt="GitHub Release"></a>
  <a href="https://github.com/akashchekka/memor/actions/workflows/release.yml"><img src="https://img.shields.io/github/actions/workflow/status/akashchekka/memor/release.yml?style=flat-square&label=build" alt="Build Status"></a>
  <a href="https://www.npmjs.com/package/@memor-dev/memor"><img src="https://img.shields.io/npm/v/@memor-dev/memor?style=flat-square&color=cb3837" alt="npm"></a>
  <a href="https://github.com/akashchekka/memor/blob/main/LICENSE"><img src="https://img.shields.io/github/license/akashchekka/memor?style=flat-square" alt="License"></a>
  <a href="https://goreportcard.com/report/github.com/akashchekka/memor"><img src="https://goreportcard.com/badge/github.com/akashchekka/memor?style=flat-square" alt="Go Report Card"></a>
</p>

<p align="center">
  Every AI coding tool starts each conversation with no structural model of your repository. Answering "where is auth handled and what calls it?" costs a search-read-search loop that pulls whole files into the context window. Memor indexes the repository into one graph and hands the assistant a task-ranked map instead.
</p>

<p align="center">
  <em>Local files. Ranked context. Zero cloud, zero daemon, zero git commits.</em>
</p>

---

## Why

| Problem | Without Memor |
|---|---|
| No structural model of the repository | Search-read-search loops before any real work starts |
| Whole files read to find forty relevant lines | Tokens spent on content the model does not need |
| Longer context is not free | Accuracy degrades as irrelevant content accumulates |
| Every session re-derives the same map | The same exploration repeats indefinitely |
| Decisions live in chat history | The rejected alternative gets re-implemented |

Token spend is not the only cost. Model accuracy falls as inputs grow, and a
plausible-but-wrong result hurts more than a missing one, so Memor optimizes for
precision and returns a short block rather than a padded one.

---

## How It Works

Memories, files, symbols, packages, documents, and topics are all **nodes** in one
graph, connected by typed **edges**.

- **Extraction** — `memor build` walks the repository and records files, packages,
  imports, and Go symbols with exact byte spans. It is deterministic: no network
  call, no model call, no CGO.
- **Spans, not copies** — a symbol node stores `path + byte range + content hash +
  signature`. Bodies stay on disk, so there is one source of truth and staleness
  stays detectable per file.
- **Write path** — everything appends to `graph.log` as JSONL. Nodes are
  content-addressed, so recording the same fact twice collapses to one node.
- **Read path** — one pipeline: seed on lexical matches, walk the graph outward,
  blend BM25 with proximity, PageRank, tags, and recency, drop anything below a
  precision floor, then pack the survivors with the strongest results at both
  ends of the block.
- **Compaction** — folds the log into `graph.snap`, archives decayed memories,
  and regenerates the rendered projection.

The capability no comparable tool has is the `explains` edge: a decision binds
to the exact file or symbol where a future agent would otherwise repeat the
mistake, and resurfaces there without anyone searching for it.

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

### Check the graph

```bash
memor status
memor search "import extraction"
```

---

## On-Disk Layout

```
<project>/
├── .memor/              # Per-project graph (gitignored)
│   ├── graph.log        # Append-only JSONL: nodes, edges, tombstones
│   ├── graph.snap       # Lossless canonical snapshot
│   ├── graph.db         # Rendered projection (write-only; nothing parses it back)
│   ├── graph.idx        # Derived index; safe to delete at any time
│   ├── graph.archive    # Evicted nodes
│   ├── blobs/           # Size-capped LRU body cache
│   └── config.toml
├── .vscode/mcp.json     # MCP server registration
└── AGENTS.md            # Three-line pointer
```

Four rules keep this honest:

1. `graph.log` is the only append target.
2. `graph.snap` is the only lossless artifact; everything else regenerates from it.
3. `graph.idx` is disposable by definition — delete it and the next command rebuilds it.
4. `graph.db` is write-only, which is why v2 has no bespoke format parser to keep in sync.

---

## Node and Edge Kinds

| Kind | Holds |
|---|---|
| `file` | A source file: path, line count, content hash, summary |
| `sym` | A function, method, type, or constant with its exact span |
| `pkg` | A directory or module |
| `ext` | A third-party dependency with no body in this repository |
| `doc` | A section of a markdown document |
| `mem` | A recorded decision, fix, workflow, or preference |
| `topic` | A tag, promoted to a first-class node |

| Edge | Meaning |
|---|---|
| `imports` | file → file, package, or external dependency |
| `contains` | package → file, file → symbol |
| `calls` | symbol → symbol |
| `refs` | symbol → symbol, non-call use |
| `tagged` | any node → topic |
| `supersedes` | node → the node it replaces |
| `explains` | memory → the file or symbol it explains |

### Memory subtypes

| Prefix | Type | Use For |
|---|---|---|
| `@s` | Semantic | Facts, decisions, architecture choices |
| `@e` | Episodic | Events, bugs fixed, migrations completed |
| `@p` | Procedural | Commands, workflows, how-tos |
| `@f` | Preference | Developer style preferences (permanent) |

### Rendered projection (`graph.db`)

```
@g v2 | 48 files | 439 symbols | 59 docs | 2 memories | 917 edges | built:2026-09-03T12:15:26Z

@f internal/graph/snap.go [275 LOC | 7511f4] pkg:internal/graph
  func Load(paths store.Paths) (*Graph, error)                          @15-33
  func Compact(paths store.Paths, cfg config.Config) (int, int, error)   @61-69
  func AutoCompact(paths store.Paths, cfg config.Config) (...)           @125-150
  -> internal/config, internal/store
  <- cmd/compact.go, internal/session/session.go
  ~ "Archive before replacing the snapshot so a failure mid-write is retryable" [semantic, 2026-09-03]
```

`->` is what the file imports. `<-` is what imports it, which answers "what
breaks if I change this?" and is free to precompute. `~` is an `explains` edge
rendered in place.

That block is roughly 90 tokens and substitutes for a 3,400-token file read.

### Log format (`graph.log`)

```jsonl
{"o":"n","n":{"i":"7271f9a926dd","k":"mem","n":"s","x":"Archive before replacing the snapshot","t":1772800000,"m":{"t":"s","org":"agent"}}}
{"o":"e","g":{"f":"7271f9a926dd","t":"3b1c8e2a4f90","k":"explains","w":1}}
```

---

## CLI Reference

| Command | Description |
|---|---|
| `memor init` | Create `.memor/`, register the MCP server, add the `AGENTS.md` pointer |
| `memor build` | Index the repository into the graph |
| `memor status` | Node and edge counts, staleness, footprint |
| `memor context` | Print the task-ranked map (the main agent entry point) |
| `memor remember` | Record a decision, fix, workflow, preference, or file summary |
| `memor search <query>` | List matching nodes with scores and IDs |
| `memor symbol find <name>` | Locate a definition with its span, callers, and callees |
| `memor symbol read <name>` | Print the exact lines a symbol occupies |
| `memor compact` | Fold pending writes into the snapshot |
| `memor migrate` | Convert a v1 store into the v2 graph |
| `memor export` / `import` | Move memories between machines as JSONL |
| `memor clean` | Remove derived artifacts, or `--all` for the whole footprint |
| `memor rules` | Print the agent protocol |
| `memor mcp` | Serve the graph over the Model Context Protocol |

---

## Retrieval

One pipeline ranks the graph with no embeddings, vector database, or network call:

```
score = 0.30·BM25 + 0.25·proximity + 0.20·PageRank + 0.15·tags + 0.10·recency
```

- **BM25 with shared stemming** — indexing and querying stem identically, so a
  question about "compaction" reaches a function named `Compact`.
- **Graph proximity** — a decayed walk outward from seed nodes, weighted per edge
  kind so one good hit does not flood the frontier with everything adjacent to it.
- **Offline PageRank** — structure earns relevance independently of the query, so
  a rarely-named but heavily depended-on file is not invisible.
- **A precision floor** — results below the threshold are dropped, not replaced.
  Structural weight alone cannot clear it; a node must be lexically matched or
  directly adjacent to something that was.
- **Position-aware packing** — strongest results take the head and the tail,
  weakest survivors sit in the middle where they cost least.

---

## AI Tool Integration

Memor speaks the Model Context Protocol, so hosts surface it as native tools with
typed arguments rather than prose an agent may ignore. Five tools:

| Tool | Replaces |
|---|---|
| `repo_map` | Reading files to find out where anything is |
| `symbol_find` | grep and workspace search |
| `symbol_read` | Whole-file reads |
| `remember` | Losing the decision when the conversation ends |
| `graph_status` | Guessing whether the map is trustworthy |

Behavioural rules live in the tool descriptions rather than a markdown template:
they load with the tool, cannot be edited away, and are versioned with the
binary. `memor init` therefore owns two files outside `.memor/` instead of seven:
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
max_hops = 2
min_score = 0.12           # Precision floor
max_symbols_per_file = 8
```

---

## Design Principles

1. **Local-first, no infrastructure** — just files on disk
2. **One node type, one index, one packer** — the seam that caused duplicate emission in v1 no longer exists
3. **Spans, not copies** — the graph is coordinates into the repository, never a second copy of it
4. **Deterministic extraction** — no network call, no model call, no CGO
5. **Precision over recall** — a short answer beats a padded one
6. **Derived data is disposable** — anything regenerable is deletable
7. **Append-only writes, compacted reads** — LSM-tree inspired architecture
8. **Zero-config start** — `memor init --build` and done

The complete architecture, data flow, scoring, persistence, and recovery model
is documented in [CONCEPTS.md](CONCEPTS.md). The reasoning behind the v2 design,
including the alternatives that were rejected, is in
[ADR-0001](docs/adr/adr-0001-memor-v2-code-knowledge-graph.md).

---

## Upgrading from v1

A v1 store is migrated automatically on the first v2 command. `memor migrate`
makes it explicit. Memories, tags, code summaries, and knowledge sections all
carry across; v1 `deps` become real `imports` edges, so you get a partial graph
before extraction even runs. The v1 files are renamed to `*.v1.bak` rather than
deleted.

Run `memor build` afterwards to add structural extraction.

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

