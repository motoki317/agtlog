---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

Subagents and Info split delegated activity from its cost explanation. Comparing
own work with delegated work required readers to switch tabs, and the cost tree
repeated the session hierarchy already available in the Subagents table.

# Decision

Detail has two tabs: Timeline and Overview. Overview places Activity first,
then own-model cost blocks, then the existing Subagents table.

Activity compares turns, owned tokens, and owned cost across own,
subagent, and total rows. A session without subagents shows only its own row.
Each model block always shows its full display name, rate lines, and subtotal.
Only subagent rows are focusable, with the first subagent selected on entry.
A session without subagents has no selection; `j`/`k` scroll its viewport.
Column focus and sorting remain specific to the Subagents table.

The table replaces the cost tree. To inspect a nested own-cost split, readers
open that subagent's Overview. Replay attribution and unattributed Codex usage
remain below Activity when applicable.

# Consequences

Full model blocks consume more vertical space, but readers inspect rate arithmetic
without an expansion key. The viewport scrolls to reach the Subagents table.

# Impact

The session header, Timeline, parser data, and cost attribution retain their
existing meanings.

# Alternatives

A separate Turns section in Info would add another summary without bringing
own and delegated activity together. Keeping the cost tree alongside the table
would repeat the same hierarchy and consume vertical space.

Collapsible model rows were rejected because a per-row expand mode hides rate
arithmetic behind a key and adds hidden UI state.

# Notes

The Info-tab displays described in [Codex counter segments](./20260905-codex-counter-segments.md),
[Codex usage ledger](./20260726-codex-usage-ledger.md), and
[Codex timeline usage](./20260726-codex-timeline-usage.md) now live in Overview.
The column focus and sorting behavior from [Table sorting](./20260722-table-sorting.md)
applies to the Subagents table within Overview.
