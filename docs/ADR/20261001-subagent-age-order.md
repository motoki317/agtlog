---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

Sessions uses newest-update-first order when sorting is cleared. Subagents used
oldest-start-first order to keep running siblings stationary. Consistent recency
order lets readers find recent work the same way in both tables.

# Decision

Cleared Subagents order uses `UpdatedAt` descending within each parent, with
zero timestamps last and session identity as the tie-breaker. The header shows
no arrow. Selection follows session identity across rebuilds and live refreshes.

This decision supersedes the cleared-order parts of
[Table sorting](./20260722-table-sorting.md), as listed in that ADR's Notes.
Its column focus, sort cycle, and tree traversal decisions still apply.

# Consequences

Running siblings can move as their timestamps advance, including updates from descendants.

# Impact

The change affects cleared Subagents ordering in `internal/tui` and its ordering,
selection, and golden-frame tests. Explicit sorts, column focus, and the three-state
sort cycle retain their behavior. The source session graph remains unchanged.

# Alternatives

Keeping `StartedAt` ascending would preserve stationary siblings but retain
different default ordering across the two tables. Sorting all descendants
globally would separate children from their parents.

# Notes

Session identity consists of the source agent, transcript path, and session ID.
