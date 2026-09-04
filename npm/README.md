---
title: npm package for Memor
description: Install and use Memor through npm
---

**Persistent repository state and memory for AI coding assistants.**

Every AI coding tool starts each conversation knowing nothing about your
repository — above all, not what changed since it last worked here. So it reads
whole files to rebuild that picture every session. Memor remembers it between
sessions and hands the assistant a few hundred tokens instead.

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
# Start here: where you are, what changed, what is unfinished
memor brief

# What moved since the last build, or since a given commit
memor changes

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
Repository ──► INDEX (files, purposes, symbols, imports, spans)
                    │  records the commit it indexed
Conversations ──► APPEND to graph.log (JSONL)
                    │
                    ▼
               COMPACTION (decay → archive → snapshot)
                    │
                    ▼
               graph.snap  ──►  graph.db (rendered projection)
                    │
                    ▼
         RETRIEVE (BM25 + recent-change + tags + recency)
                    │
                    ▼
         AI tools call repo_brief at conversation start
```

- **Extraction**: deterministic, local, no model call and no CGO
- **Spans, not copies**: coordinates into files git already tracks
- **Change tracking**: git tells memor what moved; each agent gets a watermark
- **Retrieval**: one pipeline, with a precision floor that drops rather than pads
- **Compaction**: deduplicates, decays, archives, enforces the budget

## MCP Tools

A ladder, cheapest first — an agent starts at the top and escalates only when it
has to. Reading whole files is the fallback, not the default.

| Tool | Answers |
|---|---|
| `repo_brief` | Where am I, what moved, what was I doing? |
| `repo_changes` | Which files exactly, and do they matter? |
| `repo_map` | Show me code I have not seen |
| `symbol_find` | Where is this one thing? |
| `symbol_read` | Show me its body |
| `remember` | Record this for next time |
| `memor_status` | Can I trust the index? |

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
