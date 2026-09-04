---
title: "ADR-0002: Replace the Knowledge Graph with Static Repository State"
status: Accepted
date: 2026-09-04
supersedes: ADR-0001
tags: ["architecture", "decision", "token-efficiency", "git", "mcp"]
---

# ADR-0002: Replace the Knowledge Graph with Static Repository State

## Status

Accepted. Supersedes [ADR-0001](adr-0001-memor-v2-code-knowledge-graph.md).

## Context

ADR-0001 built a knowledge graph: typed edges between files, symbols, packages,
documents and topics, with BFS expansion and offline PageRank driving retrieval.
It shipped and it worked, in the sense that it produced a ranked map.

It did not solve the problem memor exists to solve. Agents kept loading far more
context than they needed.

Three observations drove the reconsideration.

**The graph answered a question agents were not asking.** Edge traversal is
valuable when the caller does not know what it is looking for and needs the
neighbourhood of a match. In practice an agent either knows the symbol it wants
(and should get exactly that) or is orienting itself (and wants a summary, not a
subgraph). Expansion mostly delivered topically-adjacent files — precisely the
distractors ADR-0001's own evidence section identified as worse than filler.

**The missing signal was temporal, not structural.** An agent starting a session
had no way to know what had changed since it last worked in the repository, so
it re-derived everything from scratch every time. That is the actual source of
the token cost. No amount of ranking quality fixes it, because the agent is not
running a query — it is rebuilding a mental model it already had.

**LLMs do not need a graph to reason about code.** They need to know what exists,
what moved, and what was decided. The graph was modelling knowledge the model
already has, at the cost of a traversal engine, a PageRank pass, a derived index
file, and an edge record type in the storage format.

## Decision

Replace the graph model with **static repository state** in three layers, and
restructure the MCP surface as a cost-ordered escalation ladder.

### D1 — Relations become metadata, not edges

Imports, dependents, calls, callers, tags and memory attachments are stored as
lists on the node they describe. They are reported, never traversed.

This keeps everything the edges were actually used for — "what does this import",
"what breaks if I change this", "what decision explains this file" — while making
it structurally impossible for a query to return something it did not match.

Consequences:

- `Edge`, `EdgeKind`, adjacency maps, `Resolve`, `Neighbors` are deleted.
- `OpEdge` and `OpDropEdge` leave the log format. A record is a node or a
  tombstone.
- PageRank goes, and with it `graph.idx` — the file existed only to cache it.
  The BM25 index is rebuilt in memory on load, which it already was.
- `KindExt` and `KindTopic` go. An external dependency is a string in an import
  list; a tag is a string in a tag list. Neither needed to be a node.

### D2 — Git is the change signal

`memor build` records the commit it indexed in `state.json`. Every agent gets a
watermark in `marks.jsonl` holding the last commit it was shown.

"What changed since last time?" is then `git diff` between two commits it already
knows, plus the dirty working tree. Outside a git work tree memor falls back to
comparing stored file hashes against disk: less precise, never unavailable.

A commit alone turned out to be insufficient. Uncommitted work is the normal
state of a working tree, so the same dirty files reappear in the change list on
every visit and the second visit teaches the agent nothing. The watermark
therefore also stores the **content hash of each path it showed**, and changes
are classified as *unseen* (listed with status and purpose) or *seen* (listed as
a bare path). Unseen sorts first, so truncation drops what the agent already
knows. Seen paths are still listed rather than hidden, because a new
conversation is a new context window and a bare path costs a few tokens where
re-reading the file costs thousands.

Git is invoked with an argument vector and never a shell string, and every
revision is validated against `^[0-9a-f]{7,40}$` before use, because revisions
reach the store from MCP clients.

### D3 — Retrieval scores matches only

The blend becomes `0.45·bm25 + 0.20·changed + 0.20·tag + 0.15·recency`, scaled by
kind weight and a test-file penalty. The `changed` term is what puts a file an
agent just touched at the top of its own query.

A node that matches neither the query text nor a requested tag is not scored at
all. The precision floor, position-aware packing and redundancy suppression from
ADR-0001 are kept unchanged — they were never graph-dependent.

### D4 — The MCP surface is a ladder

| Tool | Answers | Budget |
|---|---|---|
| `repo_brief` | Where am I, what moved, what was I doing? | ≤ 600 tok |
| `repo_changes` | Which files exactly, and do they matter? | ≤ 800 tok |
| `repo_map` | Show me code I have not seen | 2500 tok default |
| `symbol_find` | Where is this one thing? | ≤ 700 tok |
| `symbol_read` | Show me its body | span size |
| `remember` | Record this for next time | ≤ 60 tok |
| `memor_status` | Can I trust the index? | ≤ 250 tok |

`repo_map`'s default budget drops from 15,000 to 2,500. The 15,000 figure was
the configured ceiling, not a target, and offering it as a default invited
filling it.

The server instructions state the ladder explicitly and name reading whole files
as the fallback rather than the default. Tool descriptions carry behavioural
contracts, because the description is what changes model behaviour.

The parsed store is cached across tool calls and invalidated on `state.json`
mtime and log size. Previously every call re-read the snapshot and rebuilt all
BM25 postings.

## Alternatives considered

**Keep the graph, tune retrieval.** Rejected. Expansion was not misconfigured; it
was answering the wrong question. Tuning decay constants would have reduced the
symptom while keeping the traversal engine, the PageRank pass and the edge record
type.

**Keep edges as data but allow one-hop expansion behind a flag.** Rejected as
premature. A flag that defaults off is dead code; a flag that defaults on is the
old behaviour. The metadata lists mean a caller who genuinely wants a file's
dependents can read them from the result it already has.

**Track changes by file hash only, no git dependency.** Rejected as the primary
mechanism, kept as the fallback. Hashing detects that a file differs from the
index, not that it differs from what the agent last saw, and it cannot report
deletions of files that were never indexed or attribute changes to commits.

**Embeddings for retrieval.** Out of scope. It requires either a model
dependency or a network call, both of which break the local-first, CGO-free,
cross-compiled-binary constraints from ADR-0001 that still hold.

## Consequences

**Better.** A returning agent spends ~600 tokens to learn nothing changed,
instead of thousands rediscovering the repository. Retrieval cannot return
neighbours of a match. The storage format loses a record type, and the store
loses a file. Per-call latency drops because the index is cached.

**Worse.** Multi-hop questions ("what transitively depends on this") now require
the agent to make several calls or read the dependents list itself. Structural
importance no longer boosts a rarely-named but heavily-depended-on file, so such
a file surfaces only when a query actually names something in it.

**Unchanged.** Spans instead of copies, the append-log and snapshot lifecycle,
the compaction ordering (archive → snapshot → truncate), memory decay and
eviction, the concurrency invariants, and the precision-over-recall stance.

**Breaking.** The on-disk format, the CLI and the MCP tool names all change, and
the v1 migration path is deleted. memor is pre-1.0; an existing store is
rebuilt with `memor build` and agent-authored memories are carried across with
`memor export` and `memor import`.

## Addendum: symbol extraction beyond Go

ADR-0001 restricted L1 to Go because `go/ast` ships with the toolchain and
tree-sitter needs CGO, which would break the cross-compiled npm binaries. That
left non-Go repositories with no spans at all, so `symbol_find` returned nothing
and the agent fell back to whole-file reads — the exact behaviour this ADR exists
to remove.

The resolution is a third option neither ADR considered: match declaration lines
with regular expressions and bound the body by brace depth or indentation. It is
less accurate than a parser, and the accuracy is spent deliberately:

- **Java, Kotlin, C#, Swift get types only.** A method is `Type name(args)` with
  no keyword to anchor on, and every heuristic that matches one also matches
  calls, casts and field initializers.
- **Calls and callers stay Go-only.** Resolving call sites by name without scope
  analysis produces relations that look authoritative and are frequently wrong,
  and a wrong relation is the distractor class this ADR is about.
- **Destructuring bindings and nested closures are skipped.** No caller can
  reference them by name.

A span that is too long is recoverable — the agent reads a few extra lines. A
span pointing at the wrong declaration is not, because nothing in the response
reveals the error. Every rule above resolves in that direction.
