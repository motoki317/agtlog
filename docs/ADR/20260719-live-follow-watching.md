---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code and Codex append to JSONL files while they work. A session browser that shows a stale
snapshot forces users to leave the application or refresh continuously. File notification APIs can
also miss events during directory creation, rename sequences, or watcher overflow, so native events
alone are not a complete source of truth.

The UI must accept updates without resetting the current sort, filter, selected row, or expanded
detail state.

# Decision

`Registry.Discover` builds the initial snapshot. `Registry.Follow` then creates a `Follower` whose
`Updates()` channel emits:

```text
SessionUpdate{Paths, RemovedPaths, Sessions}
```

The watcher uses fsnotify and registers every existing directory below each agent root. A newly
created directory is watched recursively. JSONL create, write, rename, and remove events enter a
300-millisecond debounce window so one burst produces one refresh.

A stat-based recursive rescan runs every two seconds. It compares path, modification time, and size
fingerprints to recover changes that native notifications missed. This rescan is a safety net, not
the primary update path.

Each changed session is parsed from the beginning. Claude subagent paths are mapped back to their
parent session; Codex changes can trigger a full registry snapshot because sibling rollouts may
alter the session graph. Offset-incremental parsing is deferred. The current full-parse design has a
clear ceiling: add per-file offsets if refresh latency on large active transcripts becomes visible.

The list upserts sessions by agent, path, and session identity. It then reapplies the active sort and
filters and restores selection by identity. A matching open detail preserves its expansion and
viewport state; removal returns safely to the list.

`--no-watch` disables the follower and produces a static initial snapshot. The `r` key still calls
`Registry.Discover`, so manual refresh uses the same authoritative path as startup.

# Consequences

- Active rows update without polling at the UI layer.
- Debouncing prevents an append burst from causing a parse per filesystem event.
- The two-second scan bounds how long a missed notification can leave the list stale.
- Full parsing keeps one parser behavior for startup, refresh, and half-written-line recovery.
- Very large, rapidly changing transcripts may eventually exceed the acceptable refresh budget.

# Impact

The watcher reads only JSONL metadata and content. It never modifies agent roots. Closing the TUI
cancels the follower, closes fsnotify, and waits for its goroutines, which prevents terminal exit
from leaving background work behind.

Watcher tests need real temporary directories and timing margins. UI tests can send
`SessionUpdate` values directly to verify stable selection, filtering, sorting, detail replacement,
and removal without relying on filesystem timing.

# Alternatives

**Periodic discovery only** was rejected because a short interval wastes repeated directory walks
and a long interval feels stale. The rescan remains only as a recovery path.

**fsnotify without a rescan** was rejected because notification loss and directory races would have
no repair mechanism.

**Offset-incremental JSONL parsing in v1** was deferred because it requires persistent parser state,
truncation detection, and graph reconciliation for two different formats. Full parsing is simpler
and has not crossed its measured ceiling.

# Notes

The application performs a second discovery after the follower starts. This closes the startup gap
in which a file could change after the first snapshot but before watches are active.
