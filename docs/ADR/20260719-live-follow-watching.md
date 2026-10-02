---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code and Codex append to JSONL files while they work. If a session browser shows a stale
snapshot, users must leave the application or refresh it again and again. File notification APIs
can also miss events during directory creation, rename sequences, or watcher overflow. Native
events alone are therefore not a complete source of truth.

The UI must accept updates without resetting the current sort, filter, selected row, list scroll
position, or expanded detail state.

# Decision

The TUI starts with an empty loading model and paints without waiting for discovery. In watch mode,
`Registry.Follow` installs the watches before one asynchronous `Registry.Discover` builds the
initial snapshot. Watcher changes and the discovery snapshot both reach the TUI as `SessionUpdate`
values.

The watcher uses fsnotify. It registers every existing directory below each agent root and watches
each new directory recursively. JSONL create, write, rename, and remove events enter a
300-millisecond debounce window, so one burst produces one refresh. A pending change flushes at
most two seconds after its first event, so a file that is written without pause still refreshes.

A stat-based recursive rescan runs every two seconds. It compares path, modification time, and size
fingerprints to find changes that native notifications missed. It sends those changes through the
same debounce and parse path.

We parse each changed session from the beginning, and a Claude subagent path maps back to its parent
session. Two later records changed the Codex path. [Codex summary
checkpoints](./20260822-codex-summary-checkpoints.md) resumes a Codex summary from a checkpoint, and
the [follow session index](./20260822-follow-session-index.md) rebuilds the Codex session graph from
cached parser output instead of a full discovery.

The list upserts sessions by path, because the parsed session ID can change while an agent appends
to the file. It then reapplies the active sort and filters and restores the selection by agent,
path, and session ID. A matching open detail keeps its expansion and viewport state. If the open
session disappears, the TUI returns to the list.

A watcher update can reach the TUI before the discovery snapshot does. While discovery runs, the
list records the removed paths and the parsed session paths from watcher updates. It then skips
snapshot entries for those paths, so the snapshot cannot replace or duplicate newer watcher results.

`--no-watch` skips the follower but still runs the initial discovery asynchronously. Non-TTY output
waits for the snapshot before it prints. The `r` key calls `Registry.Discover`, so a manual refresh
uses the same authoritative path as startup.

# Consequences

Active rows update without polling in the UI layer. Full Claude parsing keeps one parser behavior
for startup, refresh, and recovery from a half-written line. Startup shows discovery progress
without streaming or sorting partial session results.

# Impact

`Follower.Close` cancels the follower context before it closes fsnotify. Watch-tree walks and cache
I/O run in bounded batches. Path mapping, fingerprints, JSONL parsing, parser finalization, graph
linking, and ownership loops check that context. A single filesystem operation or JSON marshal
finishes before the next context check.

`Follower.Close` waits for the goroutines that the follower owns. The only goroutine that sends
updates closes `Updates()` before `Close` returns, so no sender outlives the channel.

The startup discovery runs in its own goroutine. On cancellation, `discoverAndFollow` returns
without waiting for that goroutine or its parse workers. They send results only to private buffered
channels, so they never block, and process exit reclaims any parse that has not returned.

Watcher tests need real temporary directories and timing margins. UI tests can send `SessionUpdate`
values directly to check stable selection, filtering, sorting, detail replacement, and removal
without filesystem timing.

# Alternatives

We rejected periodic discovery alone, because a short interval repeats directory walks and a long
interval feels stale. The rescan stays only as a recovery path.

We rejected fsnotify without a rescan, because nothing would repair a lost notification or a
directory race.

We deferred offset-incremental JSONL parsing, because it needs persistent parser state, truncation
detection, and graph reconciliation for two formats. We add per-file offsets if refresh latency on
large active transcripts becomes visible.
