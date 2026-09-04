---
title: npm package for Memor
description: Install and use Memor through npm
---

**Repository knowledge graph and persistent memory for AI coding assistants.**

Every AI coding tool starts each conversation with no structural model of your
repository, so it reads whole files to find out where anything is. Memor indexes
the repository into one graph and hands the assistant a task-ranked map within a
token budget.

Local files. Ranked context. Zero cloud, zero daemon, zero git commits.

## Install

```bash
npm i -g @memor-dev/memor
```

Or run directly:

```bash
npx @memor-dev/memor init
```

## Quick Start

```bash
# Initialize and index your project
cd your-project
memor init --build
```

`memor init` creates `.memor/`, registers the MCP server in `.vscode/mcp.json`,
and adds a three-line pointer to `AGENTS.md`.

Use `memor init --tools claude,cursor` to register additional MCP hosts.

## Additional Commands

```bash
# Print the task-ranked map
memor context --query "where is auth handled"

# Locate a definition instead of grepping
memor symbol find ValidateToken

# Read only the lines a symbol occupies
memor symbol read ValidateToken

# Record a decision against the file it explains
memor remember "OAuth2 with PKCE; sessions rejected as stateful" --file src/auth.ts

# Check size, staleness, and footprint
memor status
```

## How It Works

```
Repository ──► EXTRACT (files, symbols, imports, spans)
                    │
Conversations ──► APPEND to graph.log (JSONL)
                    │
                    ▼
               COMPACTION (decay → archive → snapshot)
                    │
                    ▼
               graph.snap  ──►  graph.db (rendered projection)
                    │
                    ▼
         RETRIEVE (BM25 + proximity + PageRank + tags + recency)
                    │
                    ▼
         AI tools call repo_map at conversation start
```

- **Extraction**: deterministic, local, no model call and no CGO
- **Spans, not copies**: the graph stores coordinates into files git already tracks
- **Retrieval**: one pipeline, with a precision floor that drops rather than pads
- **Compaction**: deduplicates, decays, archives, enforces the budget

## MCP Tools

| Tool | Replaces |
|---|---|
| `repo_map` | Reading files to find out where anything is |
| `symbol_find` | grep and workspace search |
| `symbol_read` | Whole-file reads |
| `remember` | Losing the decision when the conversation ends |
| `graph_status` | Guessing whether the map is trustworthy |

## Supported Platforms

| OS | Architecture |
|---|---|
| Linux | x64, arm64 |
| macOS | x64, arm64 (Apple Silicon) |
| Windows | x64 |

## Documentation

Full documentation, design details, and CLI reference at [github.com/akashchekka/memor](https://github.com/akashchekka/memor).

## License

MIT


| OS | Architecture |
|---|---|
| Linux | x64, arm64 |
| macOS | x64, arm64 (Apple Silicon) |
| Windows | x64 |

## Documentation

Full documentation, design details, and CLI reference at [github.com/akashchekka/memor](https://github.com/akashchekka/memor).

## License

MIT
