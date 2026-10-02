---
date: "2026-10-02"
author: "@motoki317"
status: "accepted"
---

# Context

An expanded Timeline spends screen rows on event bodies, with up to `detailPreviewLineCap` (40)
lines per section. A screen shows few events, so a reader must page through bodies to find an event.

# Decision

We start each session opened from the list with its Timeline rows collapsed so that readers can
scan events before they inspect bodies. `space` and `→` open the focused body, and `E` opens all
bodies.

This replaces “Each session opened from the list starts expanded” in
[Bulk fold default](20260723-bulk-fold-default.md). We retain that record's bulk-key semantics and
drill inheritance: `E` and `C` govern current and later rows, and a drilled subagent copies its
parent's default. Later bulk choices affect only the active screen.

# Alternatives

We rejected **an expanded start** because the bodies leave too few event headers on screen for a
reader to scan the session.

We rejected **a persisted user preference for the start state**. The TUI has no configuration
surface for view state, and we keep runtime writes under the XDG cache. We do not add a persistent
setting when `E` opens all bodies with one key.
