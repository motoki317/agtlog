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

The TUI starts with an empty loading model and paints without waiting for discovery. In watch mode,
`Registry.Follow` installs the watches before one asynchronous `Registry.Discover` builds the
initial snapshot. Both operations reach the TUI as `SessionUpdate` values:

```text
SessionUpdate{Paths, RemovedPaths, Sessions, DiscoveryComplete, DiscoveryErr}
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

A watcher update can arrive before the initial snapshot. During discovery, the list records removed
paths and successfully parsed session paths from watcher updates. It skips snapshot entries for
those paths, so the snapshot cannot replace or duplicate newer watcher results.

`--no-watch` skips the follower but still runs initial discovery asynchronously. Non-TTY output
waits for the snapshot before it prints. The `r` key still calls `Registry.Discover`, so manual
refresh uses the same authoritative path as startup.

# Consequences

- Active rows update without polling at the UI layer.
- Debouncing prevents an append burst from causing a parse per filesystem event.
- A missed notification is detected by the next two-second scan, then follows the normal debounce
  and parsing path.
- Full parsing keeps one parser behavior for startup, refresh, and half-written-line recovery.
- Very large, rapidly changing transcripts may eventually exceed the acceptable refresh budget.
- Startup shows discovery progress without streaming or sorting partial session results.

# Impact

The follower reads directory entries and JSONL metadata and content. It never modifies agent roots.
`Follower.Close` cancels the follower context before closing fsnotify. Watch-tree and cache I/O use
bounded batches; path mapping, fingerprints, JSONL parsing, parser finalization, graph linking, and
ownership loops observe that context. An individual filesystem operation or JSON marshal finishes
before the next context check.

`Follower.Close` waits for follower-owned goroutines. The sole goroutine that sends updates closes
`Updates()` before `Close` returns, so no sender can outlive the channel.

The startup discovery runs in a separate goroutine. `discoverAndFollow` returns on cancellation
without waiting for that goroutine or its parse workers. They publish only to private buffered
result channels. Process exit reclaims any parse that has not returned.

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

Starting the follower before the single discovery closes the startup gap. Path-based merging keeps
an update that arrives after watches are active but before the snapshot reaches the TUI.
