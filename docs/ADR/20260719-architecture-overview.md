---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code and Codex record similar work in different shapes. Claude Code writes a top-level
session and separate files for its subagents. Codex records `sub_agent_activity` in a rollout and
can place a subagent's transcript in a sibling rollout. If the UI read those formats directly,
discovery, cost rollup, and transcript rendering would each need agent-specific branches.

Session directories can contain hundreds of files, and one transcript can contain thousands of
JSONL records. The list therefore cannot build every timeline at startup. The source logs belong to
the agents that wrote them, so agtlog must leave them unchanged.

# Decision

We route both agents through one pipeline:

```text
internal/source discovery and follow
  -> internal/source/claude and internal/source/codex adapters
  -> internal/model.Session
  -> internal/cost pricing
  -> internal/tui list and detail views
```

The pipeline assigns ownership, not separate passes. Adapters call the cost calculator while they
build sessions, but `internal/cost` owns pricing policy and `internal/model` owns the normalized
result.

`internal/source.Registry` discovers paths and coordinates parsing. Each adapter absorbs its log
format and produces the same `internal/model.Session` type. The Claude adapter links separate
subagent files to their parent, and the Codex adapter reconciles inline activity with sibling
rollouts. Both adapters normalize messages, model names, timestamps, project metadata, usage,
errors, and detail events before the TUI sees them.

A session holds its own usage and a recursive `Subagents` tree. `TotalUsage` and `TotalCost`
traverse that tree, so every consumer gets the same recursive rollup and none reimplements it.

Discovery parses only the summary that the list needs. Opening a row calls
`Registry.LoadNodeDetail`, which asks the owning adapter to parse the timeline events of that node.
This on-open boundary keeps transcript construction out of startup.

agtlog treats agent configuration and log roots as read-only. Its only persistent writes are the
summary and pricing caches beneath the XDG cache directory.

# Consequences

An adapter can skip a malformed or partially written record without exposing format details to the
UI.

# Impact

A change to `Session`, usage semantics, or recursive rollup affects every adapter, both TUI views,
and the subcommands that the [machine-readable CLI](./20260805-machine-readable-cli.md) later added.
Adapter tests must therefore cover their native log shape, and model tests must cover recursive
behavior. If a change alters what an adapter stores in a `Session`, the adapter must bump its cache
fingerprint, as [summary cache repricing](./20260815-summary-cache-repricing.md) records.

The read-only invariant limits future features. Editing transcripts, agent settings, or source-log
metadata needs a separate product decision and cannot enter through the cache layer.

# Alternatives

We rejected agent-specific UI models, because every list and detail feature would branch on the
source format. That duplication would carry the most risk in recursive cost accounting.

We rejected a lowest-common-denominator flat record stream, because it discards the subagent tree
that attribution and navigation need.

We rejected eager detail parsing, because the list usually shows many sessions that the user never
opens. Eager parsing turns transcript size into startup latency and memory use.
