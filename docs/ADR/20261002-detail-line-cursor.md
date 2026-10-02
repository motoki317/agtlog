---
date: "2026-10-02"
author: "@motoki317"
status: "accepted"
---

# Context

Timeline line keys selected event headers and skipped expanded bodies. An edge-only scroll let
readers reach the final body, but each press moved one screen row there and a whole event elsewhere.

At a terminal height of 16 rows, entering Overview selected the first subagent and scrolled Activity
off-screen. Line keys moved only between subagents. Pressing `k` at the first subagent scrolled the
viewport while the selected row slid off-screen.

# Decision

We move the cursor one text line per `j`/`down` or `k`/`up` press on both detail tabs. It traverses
Timeline headers and expanded bodies, and every Overview line when subagents exist. A wrapped text
line remains one unit, and all its screen rows share the selection.
When a line exceeds the viewport height, movement reveals its first row. A press toward another
text line skips the remaining screen rows.

On Timeline, we keep the event that owns the selected line as the target for fold and open keys.
If a fold removes the selected body line, the cursor returns to that event's header. The position counter
still counts events.

We preserve the owning event and the cursor's line offset within its block across rebuilds and
live updates, clamped to the remaining lines. Tab switches retain only the event selection. A
return selects its header unless the Timeline resumes tail following. Opening at the tail, a
followed live update, and `G` select the final line. We retain tail following while the viewport is
anchored at the bottom and the cursor belongs to the last event.

We replace the subagent-only selection rule in [Overview tab](./20261001-overview-tab.md).
Overview's cursor can select Activity, cost blocks, headings, and separators. We open a child with
`enter` or `l` only on its subagent row. Column focus and sorting work wherever the cursor is.
Clicks select their text line without opening a child. The position counter shows `N/total` on
subagent rows and `0/total` above them.

We retain Overview's entry on the first subagent and remember a subagent by key when the cursor
leaves from its row. Returning after leaving from any other line selects the first subagent.
Across rebuilds and live updates, we preserve a subagent by key. Above the table, we preserve the
line index, clamped to stay above the first subagent row. Without subagents, Overview has no cursor
and line keys scroll the viewport one screen row.

We use `g` and `G` to select the first and last text lines. At either cursor edge, an outward
line-key press scrolls the viewport one screen row without moving the cursor, clamped at the
content edge.

# Alternatives

We rejected **an event cursor with edge scrolling** because each keypress takes a different-sized
step at an event boundary and at the Timeline's end.

We rejected **a screen-row cursor** because readers preferred text-line units and fewer presses
through prose. Text-line units also match the existing highlight across all wrapped rows of a
selected line.

We rejected **browser-style scrolling through bodies**: select the next event if it is visible,
otherwise scroll toward it. The size of a step then depends on what is on screen.
