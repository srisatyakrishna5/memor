---
title: Memor Concepts
description: An accessible guide to the data flow, storage model, and algorithms used by Memor
ms.date: 2026-08-10
ms.topic: concept
---

## What Memor Solves

AI coding assistants do not naturally remember earlier conversations. A new chat
may see the repository, but it does not know why a team chose PostgreSQL, how
production is deployed, or which workaround already failed.

Memor adds a small local memory layer for that missing history. It has four
goals:

* Save new memories without rewriting the entire store
* Keep complete memory data after compaction
* Return useful context within a predictable token budget
* Work without a cloud service, daemon, or database server
* Share the same memory across multiple AI coding tools

The implementation stays intentionally small. Memor uses local files,
deterministic scoring, and one in-memory BM25 ranker. It does not need a general
search engine for a memory set that is already bounded by a token budget.

## The Big Picture

A memory moves through three stages:

```text
AI assistant
    |
    | memor add
    v
memory.wal                  new writes, one JSON object per line
    |
    | memor compact
    v
deduplicate -> score -> fit to budget
    |                         |
    | kept                    | rejected
    v                         v
memory.snapshot.jsonl     memory.archive
    |
    | render a compact view
    v
memory.db
    |
    | memor context
    v
ranked context for the next conversation
```

New entries are appended quickly to the write-ahead log. Compaction later turns
the accumulated data into a bounded active snapshot. Retrieval ranks that active
data for the question being asked.

This resembles one part of a log-structured merge tree, but Memor is not a
database. It borrows the idea of append first and compact later without adding
tables, transactions, background workers, or persistent query indexes.

### Runtime Layers

The implementation has four small layers:

1. Cobra commands validate input and coordinate operations.
2. The store package reads and writes files under `.memor/`.
3. The engine compacts, ranks, and packs memories and knowledge.
4. The index and token packages provide BM25 scoring and token estimation.

No process stays running after a command finishes. Each command opens the files
it needs, performs one bounded operation, and exits.

## A Memory's Journey

Consider this command:

```bash
memor add -s "#deploy #api: Production deploys use pnpm turbo deploy"
```

Memor handles it in five steps:

1. Parse the tags and content into a memory entry.
2. Generate a timestamp and content-derived ID.
3. Append the complete entry to `memory.wal` as JSONL.
4. Trigger compaction when the WAL reaches its configured threshold.
5. Include the entry in future context when its text, tags, type, and age make
   it relevant.

Each stage has one job. The WAL keeps writes cheap, compaction controls growth,
and retrieval adapts the same stored memories to different questions.

## Local Files And Their Roles

Memor stores project data under `.memor/`:

```text
.memor/
|-- config.toml
|-- memory.wal
|-- memory.snapshot.jsonl
|-- memory.db
|-- memory.archive
`-- knowledge.db
```

| File | Purpose |
|------|---------|
| `config.toml` | Active token, compaction, and knowledge settings |
| `memory.wal` | New and updated entries waiting for compaction |
| `memory.snapshot.jsonl` | Complete canonical form of active memories |
| `memory.db` | Compact text projection for AI context |
| `memory.archive` | Entries removed by decay or the active token budget |
| `knowledge.db` | Indexed summaries of project documentation sections |

Two snapshot files may look redundant, but they serve different audiences.
`memory.snapshot.jsonl` preserves every field needed by the application.
`memory.db` uses fewer tokens and is easier for an AI assistant to scan. Keeping
these roles separate avoids forcing one format to be both lossless and minimal.

## Append-Only Writes With JSONL

`memory.wal` is a JSON Lines file. Every non-empty line is one complete memory:

```jsonl
{"t":1786356000,"y":"s","id":"4f92ea20bb5d","tags":["deploy","api"],"c":"Production deploys use pnpm turbo deploy"}
```

Appending a line is cheaper and simpler than reading and rewriting a large JSON
array. JSONL also limits malformed data to an individual line. The reader warns
about a malformed line and continues with later valid entries.

The WAL is truncated only after compaction has archived rejected entries and
committed the new active snapshot. If compaction fails earlier, the WAL remains
available for another attempt.

Implementation: [internal/store/wal.go](internal/store/wal.go)

## Content-Addressed IDs

Memor derives an entry ID from normalized content instead of generating a random
UUID. It trims surrounding whitespace, converts the text to lowercase, hashes it
with SHA-256, and keeps the first 12 hexadecimal characters:

$$
id(c) = hex(SHA256(lower(trim(c))))[0:12]
$$

This gives equivalent content the same ID regardless of capitalization.
Content-derived IDs provide two useful behaviors:

* Duplicate facts written by different tools collapse into one active entry.
* A newer copy of the same fact replaces the older copy during compaction.

An entry can also set `Supersedes` to the ID of a different entry. This supports
changes such as replacing "use Node 20" with "use Node 22" even though the two
sentences have different IDs.

The shortened hash is a deduplication key. It is not encryption, a signature, or
proof that content is trustworthy.

Implementation: [internal/memory/types.go](internal/memory/types.go)

## Memory Types

Memories are classified by how they help future work:

| Prefix | Type | Use it for | Default weight |
|--------|------|------------|---------------:|
| `@f` | Preference | Style and developer conventions | 1.0 |
| `@s` | Semantic | Facts, decisions, and architecture | 0.9 |
| `@p` | Procedural | Commands and repeatable workflows | 0.8 |
| `@c` | Code | Structured summaries of source files | 0.7 |
| `@e` | Episodic | Events, fixes, and completed migrations | 0.5 |

The type is a ranking signal, not a separate storage system. Preference memories
receive the strongest default weight because conventions usually remain useful.
Episodic memories receive a lower default weight because an old event often
matters less than an active decision or workflow.

Code memories can carry structured metadata such as a file path, line count,
hash, exports, dependencies, summary, patterns, and control flow. The canonical
snapshot preserves all of it even though the compact view shows only what helps
the next prompt.

## Compaction

Compaction turns the existing snapshot and pending WAL entries into a new active
set:

1. Read the canonical snapshot and WAL.
2. Put WAL entries after snapshot entries so newer copies take precedence.
3. Deduplicate by ID.
4. Remove entries that were superseded or expired.
5. Score each remaining entry for long-term retention.
6. Archive entries below the configured minimum score.
7. Fit the remaining entries into the active token budget.
8. Archive entries that do not fit.
9. Commit the new snapshot and truncate the WAL.

### Retention Score

The retention score answers a storage question: which memories deserve space in
the active snapshot even when no search query is present?

$$
S_{retention} = W_{type} \times D_{age} \times (1 + B_{sharedTags})
$$

The terms are deliberately straightforward:

* $W_{type}$ comes from the memory type configuration.
* $D_{age}$ gradually decreases as a memory gets older.
* $B_{sharedTags}$ gives a small boost to memories connected to active topics.

The shared-tag boost is calculated directly from active entries. It does not
need a persistent tag index.

### One Budget Decision

Snapshot serialization calculates the token cost of the exact text that will be
written. It returns two explicit lists: written and evicted. This makes every
candidate accountable and prevents a second hidden trimming step from dropping
data.

Rejected entries are archived before the active snapshot is replaced. Archive
writes are idempotent by content ID, so retrying a partially completed
compaction does not create duplicate archive records.

Implementation: [internal/engine/compact.go](internal/engine/compact.go) and
[internal/store/snapshot.go](internal/store/snapshot.go)

## Canonical Snapshot And Compact View

The canonical snapshot uses JSONL because the application needs exact
timestamps, provenance, expiry, supersession, and code metadata. The compact
view leaves out fields that do not help the next AI response.

`memory.db` may look like this:

```text
@mem v1 | 3 entries | budget:15000 | compacted:2026-08-10T12:00:00Z

@s #architecture: The API uses PostgreSQL [2026-08-10]
@p #deploy: Production deploys use pnpm turbo deploy [2026-08-09]
@f #typescript: Prefer unknown with type guards [perm]
```

When `memory.snapshot.jsonl` exists, it is the authority for programmatic reads.
`memory.db` remains the compact, human-readable projection. Older projects that
only have `memory.db` are still readable; the next compaction creates the
canonical file.

Snapshot files are replaced through temporary files. On platforms that cannot
rename over an existing file, Memor keeps a temporary backup and restores it if
replacement fails.

## Retrieval With BM25

Compaction decides what stays active. Retrieval answers a different question:
which active memories are useful for this request?

Memor builds a BM25 scorer in memory over every active entry's content and tags.
BM25 is a standard keyword-ranking method with two useful properties:

* Repeating a word many times gives diminishing returns.
* A focused short memory is not automatically outranked by a long memory.

In plain language, a memory scores well when it contains meaningful words from
the query and those words are not common across every memory. Memor uses the
conventional BM25 defaults $k_1=1.2$ and $b=0.75$.

Implementation: [internal/index/bm25.go](internal/index/bm25.go)

## Final Context Ranking

BM25 is one part of the final score:

$$
S_{context} = 0.4S_{BM25} + 0.2S_{tags} + 0.2W_{type} + 0.2D_{age}
$$

This combines four understandable signals:

* Query text matching the memory content and tags
* Tags explicitly requested by the caller
* The configured importance of the memory type
* The age of the memory

When no query is supplied, BM25 contributes zero. Type and age still produce a
useful order. Tag-only queries scan tags directly because the active set is
already bounded.

`memor reinforce <id>` refreshes an entry's timestamp by appending an updated
copy to the WAL. Normal deduplication applies the update during compaction, so no
separate recency database is needed.

Implementation: [internal/engine/context.go](internal/engine/context.go)

## Token Budgeting

AI models count text in tokens rather than characters. Exact tokenization varies
between model families, so Memor uses a dependency-free estimate for local
budgeting.

The estimator blends word count with character count. The word estimate uses
about 1.3 tokens per word, while the character estimate uses about one token per
3.8 characters. Their average is rounded to the nearest whole token.

This is not a billing calculator. It is a deterministic way to keep memory
output bounded. The same estimator is used when fitting the active snapshot and
when packing memories and knowledge into `memor context` output.

Implementation: [internal/token/token.go](internal/token/token.go)

## Project Knowledge

Memories capture facts learned during work. Knowledge indexing handles existing
project documents such as instructions, skills, and runbooks.

Memor processes a document by:

1. Splitting it at level-two Markdown headings.
2. Turning each heading into a stable section name.
3. Keeping a short section summary.
4. Extracting a small set of topic tags.
5. Saving the source path and SHA-256 hash for refresh checks.

Project scanning walks the repository once and supports recursive `**` path
patterns. It skips `.git`, `.memor`, `.venv`, and `node_modules` so dependency
and generated content do not flood the knowledge store.

Knowledge receives a configurable share of the context budget. Matching
sections are ranked with BM25. If no section matches, that space is returned to
project memories instead of being wasted.

Implementation: [internal/engine/knowledge.go](internal/engine/knowledge.go)

## Configuration

`config.toml` exposes settings that currently change runtime behavior:

```toml
[memory]
token_budget = 15000
wal_max_entries = 2

[compaction.type_weights]
preference = 1.0
semantic = 0.9
procedural = 0.8
episodic = 0.5
code = 0.7

[compaction.decay]
rate = 0.03
min_score = 0.1

[knowledge]
enabled = true
scan_paths = [".github/**/*.md", "**/SKILL.md", "CONTRIBUTING.md"]
budget_share = 0.4
```

Older project configurations may still contain settings that were removed from
the implementation. The TOML decoder ignores unknown keys, so those files can
continue loading while users migrate to the smaller active configuration.

## End-To-End Context Flow

When an assistant runs:

```bash
memor context --budget 10000 --query "deploy the API"
```

the engine follows this flow:

```text
1. Load canonical project memories
2. Add pending WAL entries
3. Add user-level memories when available
4. Deduplicate and apply supersession
5. Build BM25 over active content and tags
6. Combine BM25, tag, type, and age scores
7. Reserve part of the budget for knowledge
8. Pack memories in score order
9. Pack matching knowledge sections
10. Return unused knowledge space to memories when needed
```

The result contains only the text selected for the current request. Complete
entries remain local on disk.

## Failure And Recovery

The order of file operations protects recoverability:

* A failed append is reported before success.
* Automatic compaction runs only after a successful append.
* A failed automatic compaction is reported as a warning and leaves the WAL
    available for retry.
* Rejected entries are archived before active data is replaced.
* Archive retries skip IDs already present.
* The canonical snapshot is written before the compact view, so a failed view
    update does not discard complete entry metadata.
* Snapshot replacement uses temporary files and rollback where needed.
* The WAL is truncated last.

These choices do not turn the files into a fully transactional database. They do
protect the important invariant: a failed maintenance step should leave a
recoverable copy of each memory.

## Security And Privacy

Memor does not send memories to a cloud service and does not collect telemetry.
`memor init` adds `.memor/` to `.gitignore` so project memories remain outside
normal source control operations.

The pre-commit hook performs best-effort compaction and never blocks a commit.
The `.gitignore` entry, not the hook, is what prevents `.memor/` from being
committed.

Local storage is not a secret vault. Memories should not contain passwords, API
keys, access tokens, private keys, or personal data that does not belong in the
project workspace.

## Deliberate Omissions

The active set is constrained by the token budget, so direct BM25 scoring and
inline metadata checks are sufficient for the current workload. Memor does not
currently use:

* Trigram posting lists
* Bloom filters
* Persistent tag maps
* Access-recency rings
* Embeddings or vector databases
* Background daemons or file watchers
* Plugin systems for command registration
* Multiple compaction strategies

Each omitted component would add files, synchronization rules, failure modes,
and concepts for contributors to learn. It should return only if measured
workloads reveal a problem that the simpler design cannot solve.

## Where To Go Next

This is the canonical architecture and concepts guide. For installation,
commands, and contributor setup, see [README.md](README.md).

The shortest useful mental model is:

```text
append complete memories -> compact safely -> rank active context -> stay local
```