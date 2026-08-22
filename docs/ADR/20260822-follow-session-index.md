---
date: "2026-08-22"
author: "@motoki317"
status: "accepted"
---

# Context

Each debounced flush for a Codex file triggered a full discovery after the changed
file was parsed. A discovery with summary-cache hits decoded and repriced every
cached session. This work consumed most of the CPU time in follow mode.

Graph linking, top-level session sorting, and ownership attribution operate in
memory. Graph linking and attribution together stayed below 40 ms across five
discoveries. The follower can repeat these operations from retained parser output.

# Decision

On the first flush that requires Codex graph reconciliation, the follower indexes
the pre-link parser output from every adapter by source path. Each later flush
replaces refreshed paths, removes deleted paths, and adds new paths. The follower
builds every emitted snapshot from the current index.

A failed refresh retains the previous indexed session and queues that path for the
next flush. A path that fails during index creation starts absent and enters the
index after a retry succeeds. Removing a path also removes its queued retry.

Snapshot assembly makes a shallow copy of each `Session` and recursively copies
its `Subagents` tree. It then links the copies, sorts the roots, and attributes
ownership. All other slices and maps remain shared. Snapshot assembly assigns
ownership fields on the copies and does not modify the shared collections.

Cached summaries enter the index after repricing. Fresh summaries receive pricing
during parsing. The pricing table does not change during a process, so unchanged
indexed sessions retain current prices.

# Consequences

Follow mode performs one full discovery for its first Codex reconciliation.
Later snapshots avoid cache decoding and repricing for unchanged sessions.

Each snapshot allocates one `Session` structure per indexed node and new
`Subagents` slices. It does not copy the larger shared collections.

# Impact

Discovery and follow snapshots link copies, so parser output remains unchanged.
Cancellation checks cover discovery, copying, linking, sorting, and attribution.
Cache reads retain their version, agent, fingerprint, size, permission, and JSON
validity checks. Removal events still identify deleted source paths. Agent logs
remain read-only.

Cache loading and fresh parsing reject a top-level `Session.Path` that differs from
the adapter's discovered path. Conflicting cache or parser data cannot enter the
index under another source path.

A changed Codex rollout JSONL file still receives a full parse. Large-file offset
resumption is a separate decision after follow-mode CPU is measured again.

# Alternatives

Full discovery after each Codex event was rejected because unchanged cache entries
dominated follow-mode CPU and allocation.

Relinking indexed objects was rejected because `linkSessionGraphsContext` mutates
its input. It substitutes placeholder children, removes competing children,
rewrites placeholder paths, rolls child timestamps into parents, and appends
child-driven links. Repeated linking can therefore duplicate children and retain
a rolled-up `UpdatedAt` after a child disappears.

Deep-copying every slice and map was rejected because snapshot assembly does not
write through the shared collections.

# Notes

The motivating profile covered 386 top-level sessions. Three configured
filesystem roots held 3,031 session files.
