---
date: "2026-07-23"
author: "@motoki317"
status: "accepted"
---

# Context

`E` and `C` wrote a fold entry for every expandable row that the timeline held at that moment. A
followed session keeps appending rows. An appended row had no entry, so it took the default state,
which was expanded. A reader who collapsed the timeline to scan it watched the newest row arrive
open, and the newest row is the one row that a followed session guarantees to add.

# Decision

`E` and `C` set the timeline's default fold state and clear the per-row choices. A row without its
own choice follows the default, so the bulk keys govern the rows that arrive later as well as the
rows on screen. A row that the reader folds keeps its own choice until the next bulk key.

Each session opened from the list starts expanded. A drilled subagent copies its parent's default
when it opens, and a later bulk key changes only the screen where the reader presses it.

# Alternatives

We rejected **collapsing an arriving row once most rows are collapsed**. The ratio changes as a
followed session grows, so the same reader action gives different results depending on when it
happened. Folding a few noisy tool rows would also read as a request for a quiet timeline.

We rejected **keeping the enumeration and also setting the default**. The enumeration and the
default agree on every row, so the entries only restate what the default answers.
