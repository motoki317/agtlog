---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

With no sort active, Sessions lists the newest update first. Subagents listed the oldest start first
to keep running siblings in place. Readers find recent work the same way in both tables only if both
use recency order.

# Decision

With no sort active, the Subagents table orders siblings within each parent by `UpdatedAt`, newest
first. Zero timestamps sort last, and session identity (agent, transcript path, and session ID)
breaks ties. The header shows no arrow. The selection follows session identity across rebuilds and
live refreshes. We accept that running siblings move as their timestamps advance. `internal/source`
raises each parent's `UpdatedAt` to the newest `UpdatedAt` among its descendants, so a sibling also
moves when one of its descendants updates.

This decision replaces the unsorted `StartedAt` order of
[Table sorting](./20260722-table-sorting.md). The column focus, the sort cycle, and the tree
traversal of that ADR still apply.

# Alternatives

We rejected keeping `StartedAt`, oldest first. It keeps siblings in place, but the two tables keep
different default orders.
