---
date: "2026-07-22"
author: "@motoki317"
status: "accepted"
---

# Context

The Sessions and Subagents tables expose more sortable columns than a single key can represent.
Assigning one shifted letter to every column is ambiguous: AGENT collides with AGE, MODEL collides
with MSGS, and `shift+T` already toggles the time format.

Subagents add two ordering constraints. Their indentation describes parent-child structure, so a
global sort of flattened rows would separate children from their parents. Their update timestamps
also advance while work runs and propagate from active descendants, which would make a default
order based on `UpdatedAt` move sibling rows during live refreshes.

# Decision

Each table keeps a visible column focus separate from its independent sort state. `←` and `→` move
focus among visible columns, and `shift+O` cycles the focused column through its preferred
direction, the reverse direction, and cleared sorting. `shift+A` and `shift+N` provide direct AGE
and TITLE access. A sorted header uses its existing final cell for `↑` or `↓`, so sorting does not
change responsive column layout.

The Subagents table sorts copied sibling slices within each parent and then traverses the tree in
pre-order. It never sorts the flattened result or mutates `Session.Subagents`.

Cleared Subagents order uses `StartedAt` ascending because it is fixed when the session begins.
The AGE column uses `UpdatedAt` because that is the value rendered in the cell. The Sessions list
retains its cleared `UpdatedAt` descending order.

# Consequences

- One key scheme reaches every current and future visible column without consuming another letter
  per column.
- Column focus can diverge from the active sort, and a hidden sorted column remains active across
  narrow resizes.
- Subagent indentation remains positional and children stay beneath their parents under every
  sort.
- Running subagents remain stable in cleared order, while an explicit AGE sort can still move as
  displayed update times change.
- Sort choices last only for the current view; no configuration or persistence surface is added.

# Impact

The decision affects input handling, responsive header rendering, list rebuilding, and Subagents
tree traversal in `internal/tui`. It does not change the normalized session model, parsers, cost
calculation, or source ordering.

Tests must cover the three-state cycle, displayed-value comparators, unknown timestamps, invalid
costs, responsive focus, selection identity, graph immutability, live replacement, drill-down,
help text, and representative golden frames.

# Alternatives

**One shifted letter per column** was rejected because current names already collide and the time
toggle consumes another obvious mnemonic.

**Sorting the flattened Subagents rows** was rejected because indentation would no longer express
the actual tree.

**Using `UpdatedAt` for cleared Subagents order** was rejected because live descendants propagate
updates upward and would repeatedly move sibling branches.

**Keeping parser order** was rejected because Claude subagent files are discovered by UUID-shaped
paths, which provides no useful chronology.

# Notes

The focused-column interaction follows the k9s table pattern. Timeline and Info remain unsorted
because neither tab is a table.
