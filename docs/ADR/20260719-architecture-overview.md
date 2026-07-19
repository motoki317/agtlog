---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code and Codex record similar work in different shapes. Claude Code writes a top-level
session and separate files for its subagents. Codex records `sub_agent_activity` in a rollout and
may place a subagent's transcript in a sibling rollout. If the UI handled those formats directly,
discovery, cost rollup, and transcript rendering would each need agent-specific branches.

Session directories may contain hundreds of files, and individual transcripts can contain
thousands of JSONL records. The list therefore cannot pay the cost of building every timeline at
startup. agtlog must also preserve the source logs exactly: they belong to the agents that wrote
them, not to agtlog.

# Decision

agtlog uses one pipeline:

```text
internal/source discovery and follow
  -> internal/source/claude and internal/source/codex adapters
  -> internal/model.Session
  -> internal/cost pricing
  -> internal/tui list and detail views
```

`internal/source.Registry` discovers paths and coordinates parsing. Each adapter absorbs its log
format and produces the same `internal/model.Session` type. A session contains its own usage and a
recursive `Subagents` tree. `TotalUsage` and `TotalCost` traverse that tree, so every consumer gets
the same recursive rollup instead of reimplementing it.

The Claude adapter links separate subagent files to their parent. The Codex adapter reconciles
inline activity with sibling rollouts. Both normalize messages, model names, timestamps, project
metadata, usage, errors, and detail events before the TUI sees them. `internal/cost` prices the
normalized usage from an embedded LiteLLM snapshot overlaid by a valid runtime cache.

Discovery parses only the summary needed by the list. Opening a row calls `Registry.LoadDetail`,
which asks the owning adapter to parse timeline events. This on-open boundary keeps transcript
construction out of startup.

agtlog treats agent configuration and log roots as read-only. Its only persistent writes are
summary and pricing caches beneath the operating system's XDG cache directory. It never modifies
files beneath Claude Code or Codex roots.

# Consequences

- List, detail, sorting, and totals operate on one model for both agents.
- Recursive accounting has one implementation in the domain model.
- Supporting another agent requires an adapter into `Session`, not another UI path.
- Detail navigation can load more slowly than summary display because event parsing is deferred
  until the user opens a session.
- A malformed or partially written record can be skipped inside an adapter without exposing format
  details to the UI.

# Impact

Changes to `Session`, usage semantics, or recursive rollup affect every adapter and both views.
Adapter tests must therefore cover their native log shape and model tests must cover recursive
behavior. Cache fingerprints include parser and pricing inputs so a changed interpretation does not
reuse an incompatible summary.

The read-only invariant limits future features. Editing transcripts, agent settings, or source-log
metadata would require a separate product decision and cannot be added through the cache layer.

# Alternatives

**Agent-specific UI models** were rejected because every list and detail feature would branch on
the source format. The duplication would be most dangerous in recursive cost accounting.

**A lowest-common-denominator flat record stream** was rejected because it would discard the
subagent tree, which is required for attribution and navigation.

**Eager detail parsing** was rejected because the list usually shows many sessions that the user
will not open. It would turn transcript size into startup latency and memory use.

# Notes

The pipeline is an ownership boundary, not a requirement that each package run in a separate pass.
Adapters call the cost calculator while constructing sessions, but pricing policy remains owned by
`internal/cost` and normalized results remain owned by `internal/model`.
