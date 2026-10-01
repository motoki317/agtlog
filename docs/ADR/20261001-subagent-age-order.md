---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

Sessions uses newest-update-first order when sorting is cleared. Subagents used
oldest-start-first order to keep running siblings stationary. The user chose
consistent cleared order across both tables.

# Decision

Cleared Subagents order uses `UpdatedAt` descending within each parent, with
zero timestamps last and session identity as the tie-breaker. The header shows
no arrow. The traversal sorts copied sibling slices and preserves pre-order,
so children stay beneath their parents. Selection follows session identity
across rebuilds and live refreshes.

This decision supersedes the cleared-order paragraph and the rejected
“Using `UpdatedAt` for cleared Subagents order” alternative in
[Table sorting](./20260722-table-sorting.md), including its stationary-siblings consequence.
Its column focus, sort cycle, and tree traversal decisions still apply.

# Consequences

The most recently updated sibling appears first in both tables. Running
siblings can move as their timestamps advance, including updates from descendants.
The user accepts that movement in exchange for consistent recency order.

# Impact

The change affects the TUI Subagents table's cleared order. Explicit sorts,
column focus, and the three-state sort cycle retain their behavior.
The source session graph remains unchanged.

# Alternatives

Keeping `StartedAt` ascending would preserve stationary siblings but retain
different default ordering across the two tables. Sorting all descendants
globally would separate children from their parents.

# Notes

Session identity consists of the source agent, transcript path, and session ID.
