---
date: "2026-08-22"
author: "@motoki317"
status: "accepted"
---

# Context

In follow mode, each debounced flush for a Codex file ran a full discovery after it parsed the
changed file. A discovery with summary-cache hits decoded and repriced every cached session. In a
profile of follow mode, this work used most of the CPU time.

Graph linking, top-level session sorting, and ownership attribution run in memory. Graph linking
and attribution together stayed below 40 ms across five discoveries. The follower can repeat these
steps from retained parser output.

# Decision

We made the follower keep an index of pre-link parser output by source path. On the first flush
that needs Codex graph reconciliation, the follower builds the index from the output of every
adapter. Each later flush replaces refreshed paths, removes deleted paths, and adds new paths. The
follower builds every emitted snapshot from the current index. After the first Codex flush, every
later flush rebuilds a snapshot, including a flush that changes only Claude files.
[Extra agent home directories](./20260823-extra-agent-home-directories.md) added mirror
reconciliation to snapshot assembly and let startup discovery seed the index when it finds mirrored
copies.

A failed refresh keeps the previous indexed session and queues its path for the next flush. A path
that fails while the follower builds the index starts absent and enters the index after a retry
succeeds. Removing a path also removes its queued retry.

Snapshot assembly copies each `Session` struct and its `Subagents` tree recursively. It then links
the copies, sorts the roots, and attributes ownership. All other slices and maps stay shared, and
assembly writes ownership fields only on the copies. Discovery uses the same assembly, so parser
output stays unchanged.

Cached summaries enter the index after repricing, and fresh summaries receive prices during
parsing. The pricing table does not change within a process, so unchanged indexed sessions keep
current prices.

# Consequences

Follow mode runs a second full discovery, after the startup one, on its first Codex change. If
startup discovery found mirrored copies, their parser output seeds the index instead. Later
snapshots skip cache decoding and repricing for unchanged sessions. Each snapshot allocates one
`Session` struct per indexed node and new `Subagents` slices, but it does not copy the larger shared
collections.

# Impact

Cache loading and fresh parsing reject a top-level `Session.Path` that differs from the adapter's
discovered path. Conflicting cache or parser data therefore cannot enter the index under another
source path. Cancellation checks cover discovery, copying, linking, and attribution.

# Alternatives

We rejected a full discovery after each Codex event, because unchanged cache entries dominated
follow-mode CPU and allocation.

We rejected relinking the indexed objects, because `linkSessionGraphsContext` mutates its input. It
substitutes placeholder children, removes competing children, rewrites placeholder paths, rolls
child timestamps into parents, and appends child-driven links. Repeated linking can therefore
duplicate children and keep a rolled-up `UpdatedAt` after a child disappears.

We rejected deep copies of every slice and map, because snapshot assembly does not write through
the shared collections.
