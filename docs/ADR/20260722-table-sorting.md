---
date: "2026-07-22"
author: "@motoki317"
status: "accepted"
---

# Context

The Sessions and Subagents tables expose more sortable columns than a single key can represent. One
shifted letter per column is ambiguous: `AGENT` and `AGE` share a first letter, as do `TITLE` and
`TURNS`, and `T` already toggles the time format.

The Subagents table indents rows by depth to show parent-child structure. A global sort of the
flattened rows would separate children from their parents.

# Decision

Each table keeps a column focus, which its header highlights, separate from its sort state. `←` and
`→` move focus among the visible columns. `⇧O` cycles the focused column through its first
direction, the reverse direction, and no sort. Count and cost columns start largest first, and every
other column starts ascending, so `AGE` starts oldest first. `⇧A` and `⇧N` reach `AGE` and `TITLE`
directly. A sorted header puts `↑` or `↓` inside the column's existing width, so sorting does not
change the responsive column layout.

Sort choices stay in memory, and we add no configuration or persistence for them. The Sessions sort
lasts until agtlog exits. A session opened from the list starts with an unsorted Subagents table. A
drilled child starts with a copy of its parent's Subagents sort and column focus.

Focus and sort are independent, so a sorted column can leave the screen on a narrow resize and stay
the active sort. Every current and future visible column is reachable without another letter.

The Subagents table sorts copied sibling slices within each parent and then traverses the tree in
pre-order. It never sorts the flattened result and never mutates `Session.Subagents`, so children
stay beneath their parents under every sort. The `AGE` column sorts by `UpdatedAt`, the timestamp
behind both its relative age and the clock time that `T` shows. The Sessions list sorts by
`UpdatedAt`, newest first, when no sort is active.

We first ordered unsorted Subagents rows by `StartedAt`, oldest first.
[Subagent age order](./20261001-subagent-age-order.md) replaced that choice with the Sessions order.

# Impact

Tests must cover the three-state cycle, comparison by each column's displayed value, zero
timestamps, and non-finite and negative costs. They must cover focus across a resize, selection by
session identity, and an unmutated session graph. They must also cover a sort that survives a live
update and a drill-down, help text, and representative golden frames.

# Alternatives

We rejected **parser order** for unsorted Subagents rows, because Claude subagent files are
discovered by UUID-shaped paths, which carry no useful chronology.

# Notes

The focused-column interaction follows the k9s table pattern. The Subagents table is part of the
[Overview tab](./20261001-overview-tab.md), and only that table supports column sorting in detail.
