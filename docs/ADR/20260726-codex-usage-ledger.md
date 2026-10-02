---
date: "2026-07-26"
author: "@motoki317"
status: "accepted"
---

# Context

Codex summary parsing and timeline loading decided independently which `event_msg/token_count`
records represented billed requests. The summary pass excluded replayed records and duplicate
cumulative totals, which are re-logged `token_count` records whose cumulative counter does not
advance. The request deltas form a clean partition when they sum exactly to the cumulative total.
When they did not, the summary pass fell back to aggregate pricing. The timeline pass applied
neither rule. It could attach usage to structural rows, or discard usage when a request produced no
eligible event.

We corrected the timeline's model and output-token semantics in
[Codex timeline usage](./20260726-codex-timeline-usage.md). After that fix, the timeline row costs
of 705 of 724 measured sessions reconciled with their summary cost. The remaining differences
included duplicate cumulative records, empty attribution windows, and disagreement about the replay
boundary.

A sidecar is the rollout file of a Codex subagent. Its structural bridge is its
`inter_agent_communication_metadata` record. Across 459 measured sidecars, 38,322 `token_count`
records occurred before the bridge and 6,985 occurred after it. The billing boundary therefore
decides whether most of a sidecar's `token_count` records bill to the child or count as inherited
parent history. Telling the two apart needs a parent-child record alignment, which matches sidecar
records to the parent's records and checks their cumulative totals. A boundary change needs that
evidence, not an incidental change to timeline attribution.

# Decision

Codex `Parse` finalizes the billed-request ledger after it reads the file. A clean partition stores
one ledger entry per accepted request, located by the byte offset of its `token_count` record. An
unclean partition stores authoritative aggregate entries with a negative offset, and we neither
price nor redistribute its individual request deltas.
[Codex counter segments](./20260905-codex-counter-segments.md) applies the partition rule to each
cumulative-counter segment.

`Parse` also records the number of source bytes that it consumed in `SourceSize`. Timeline loading
reads only that many bytes and matches physical `token_count` records to ledger entries by offset.
`SourceSize` is zero for a subagent placeholder that the parser builds from a spawn announcement.
The placeholder has no ledger entries, so its timeline shows no usage. If its `Path` points to a
sibling rollout, loading reads that whole file. If its `Path` ends in `#<agent path>`, loading
reads no file.

Each request's usage attaches to the first eligible row appended since the previous `token_count`
record. Assistant text, reasoning, and tool-call rows are eligible, and user, tool-result,
compaction, system, and subagent rows are not. `codexUsageTarget` also accepts advisor rows, but the
Codex loader creates none. A request without an eligible row becomes an explicit `usage` row at its
physical record. A ledger offset that the timeline scan does not find also becomes an explicit usage
row instead of being dropped. Aggregate entries also became explicit usage rows until
[counter segments](./20260905-codex-counter-segments.md) moved them out of the timeline.

The loader processes sidecar content speculatively until the bridge. If a bridge appears, the loader
rolls back the speculative rows and keeps the ledger-accepted pre-bridge requests as explicit usage
rows. If no bridge appears, the whole file stays the child's timeline because nothing shows a
replayed region to skip. If a bridge-less sidecar later proves to contain inherited parent history,
the fix needs the parent-child record alignment.

The replay selection policy stays the timestamp heuristic in `Parse`. It applies to a forked
sidecar, whose first `session_meta` record has `thread_source` set to `subagent` or a nonempty
`forked_from_id`. The heuristic reads the first two `token_count` records that carry a valid
`last_token_usage`. If both share one timestamp second, it treats the records in that second as
replayed. We deferred the choice between that heuristic and the structural bridge as the billing
boundary, and `Parse` still uses the heuristic. It does not read the parent rollout.

# Consequences

The summary and the timeline use one finalized selection, so a duplicate or replayed physical record
cannot add row cost. Every per-request ledger entry has an eligible row or an explicit usage row.

An unclean partition shows request content without per-request metrics. We did not measure how
often a partition is unclean.

# Alternatives

We rejected keeping a selection predicate in both passes. Partition cleanliness is known only at the
end of the file, and duplicated selection already caused a pricing defect.

We rejected distributing aggregate residuals across request rows because marginal price tiers make
the result depend on an invented distribution.

We rejected a second file scan to find a missing bridge because detail loading already makes one
full pass. Speculative processing keeps the one-pass bound.

We rejected changing the replay boundary in this work because it moves session totals without the
parent-child record alignment that establishes the correct policy.
