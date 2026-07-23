---
date: "2026-07-23"
author: "@motoki317"
status: "accepted"
---

# Context

`E` and `C` used to write an entry for every expandable row the timeline held at that moment. A
followed session keeps appending rows, and an appended row has no entry, so it fell back to the
opening default and rendered expanded. A reader who collapsed the timeline to scan it watched the
newest row arrive open, which is the one row a followed session guarantees will appear.

# Decision

`E` and `C` set `defaultExpanded` and drop the per-row overrides instead of enumerating the rows
that exist. `isExpanded` already resolves an unknown key against that default, so the bulk keys
now govern the rows that arrive later as well as the rows on screen. A row the reader folds itself
keeps its override until the next bulk key.

# Consequences

- A row appended to a followed session takes the last bulk choice.
- The timeline shifts less under a collapsed reader, because an arriving row occupies one line.
- A drilled subagent opens under the parent's state, which `newDetailState` callers already copied.
- The enumeration and its `expandableTimelineKeys` helper are deleted.

# Impact

The decision affects timeline folding alone. Event parsing, metrics, the context column, and the
list screen are untouched. The state stays per detail screen, so opening a different session from
the list starts expanded again.

# Alternatives

**Collapsing an arriving row once most rows are collapsed** was rejected because the ratio moves on
its own as a followed session grows. The same reader action would produce different results
depending on when it happened, and folding a few noisy tool rows would be read as a request for a
quiet timeline.

**Keeping the enumeration and also setting the default** was rejected as redundant: the enumeration
and the default agree on every row, so the entries only restate what the default already answers.

# Notes

The initial default stays expanded, so a session opened without pressing either key reads as before.
