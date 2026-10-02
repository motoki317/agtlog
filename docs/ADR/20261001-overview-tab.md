---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

The Subagents and Info tabs split delegated activity from its cost explanation. To compare own work
with delegated work, readers switched tabs. The Info cost tree also repeated the session hierarchy
that the Subagents table already showed.

# Decision

Detail has two tabs: Timeline and Overview. Overview places Activity first, then the
`Own model costs` blocks, then the Subagents table.

Activity compares turns, tokens, and cost across the `own`, `subagents`, and `total` rows. **Own**
activity excludes subagents. Tokens and cost are **owned**: they exclude the requests that a resumed
or forked session replays from an earlier session. A session without subagents shows only its
`own` row. Each model block always shows its full display name and rate lines. It shows a subtotal
when the model has a price or a replayed amount. The blocks can push the Subagents table below the
viewport, so reaching it can take a scroll.

We originally limited selection to subagent rows and selected the first subagent on entry.
A session without subagents had no selection, and `j`/`k` scrolled its viewport.
[Detail line cursor](./20261002-detail-line-cursor.md) replaces the subagent-only selection rule.
It retains first-subagent entry and viewport scrolling without subagents. Column focus and sorting from
[Table sorting](./20260722-table-sorting.md) act only on the Subagents table.

The Subagents table replaces the cost tree. To inspect a nested subagent's own and delegated cost,
readers open its Overview. Replay attribution and unattributed Codex usage stay below Activity when
they apply.

# Alternatives

We rejected a separate Turns section in Info, because it adds another summary without bringing own
and delegated activity together. We rejected keeping the cost tree beside the table, because it
repeats the same hierarchy and takes vertical space.

We rejected collapsible model rows, because a per-row expand mode hides the rate arithmetic behind a
key and adds hidden UI state.

# Notes

Earlier records describe displays in the Info and Subagents tabs, and Overview keeps them. Replay
attribution shows the owned, gross, and replayed totals that
[Cross-session cost deduplication](./20260724-cross-session-cost-dedup.md) defines. Unattributed
Codex usage shows the per-model `unattributed` usage that
[Codex counter segments](./20260905-codex-counter-segments.md) defines. The Subagents table keeps
the one-row Workflow runs of [Workflow subagent groups](./20260805-workflow-subagent-groups.md).
