---
title: npm package for Memor
description: Install and use Memor through npm
---

**Local memory persistence for AI coding assistants.**

Every AI coding tool starts each conversation cold. Memor stores project context locally and surfaces relevant memories within a token budget.

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
# Initialize in your project
cd your-project
memor init
```

`memor init` creates `.memor/` and adds a managed instruction block to `AGENTS.md` so AI tools can read and write memories.

Use `memor init --tools claude,cursor,windsurf` to configure additional AI tools.

## Additional Commands

```bash
# Add a memory manually
memor add -s "#arch #db: PostgreSQL 16 with Drizzle ORM"

# Get context for AI tools
memor context --budget 10000

# Search memories
memor search "deploy"

# Compact WAL into snapshot
memor compact
```

## How It Works

```
Conversations ──► APPEND to memory.wal (JSONL)
                       │
                       ▼
                  COMPACTION (score → dedupe → budget)
                       │
                       ▼
                  memory.db (compact DSL, token-budgeted)
                       │
                       ▼
             AI tools READ memory.db at conversation start
```

- **Write path**: Append memories as JSONL — fast, no coordination
- **Read path**: BM25 ranking with tags, type weights, and age decay
- **Compaction**: Deduplicates, scores, enforces token budget

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
