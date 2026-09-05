---
date: "2026-07-26"
author: "@motoki317"
status: "accepted"
---

# Context

Codex summary parsing and timeline loading independently decided which
`event_msg/token_count` records represented billed requests. The summary pass excluded replayed
records and duplicate cumulative totals, then fell back to aggregate pricing when request deltas
did not partition the cumulative total. The timeline pass applied neither rule and could attach
usage to structural rows or discard it when a request produced no eligible event.

After correcting the timeline's model and output-token semantics, 705 of 724 measured sessions
reconciled and the aggregate timeline-to-summary cost ratio was 0.9939. The remaining differences
included duplicate cumulative records, empty attribution windows, and replay-boundary disagreement.

The structural bridge is not a small header boundary. Across 459 measured sidecars, 38,322
`token_count` records occurred before the bridge and 6,985 occurred after it. Changing which
boundary defines a child's billed work therefore needs evidence from parent-child record alignment,
not an incidental change to timeline attribution.

# Decision

Codex `Parse` finalizes the billed-request ledger after it has read the file. A clean partition
stores one entry per accepted request with its byte offset, resolved model, normalized usage, and
priced USD. An unclean partition stores authoritative per-model aggregate entries with a negative
offset; individual request deltas are not priced or redistributed.

`Parse` also records the number of source bytes it consumed. Timeline loading limits its existing
file scan to that byte count and matches physical `token_count` records to ledger entries by offset.
A zero byte count means the session was not parsed independently, so loading remains unbounded and
does not claim the snapshot reconciliation guarantee.

Attribution records the first eligible row appended during the current request. User, compaction,
system, and subagent rows are never eligible. A request without a candidate becomes an explicit
`usage` event at its physical record. Aggregate entries and ledger offsets absent from the snapshot
also become explicit usage events instead of being dropped.

Sidecar content is processed speculatively before a bridge. If a bridge appears, the speculative
content is rolled back while ledger-accepted pre-bridge requests remain as explicit usage rows. If
no bridge appears, the entire file remains the child's timeline because there is no evidence of a
replayed region to skip. If a bridge-less sidecar is later shown to contain inherited parent
history, the correction requires a parent-child record-alignment oracle rather than a substitute
activation marker.

The replay selection policy remains the existing timestamp heuristic in `Parse`. Determining
whether that heuristic or the structural bridge is the correct billing boundary is deferred until
records can be aligned with parent history and their cumulative totals checked. This preserves
session totals while removing the timeline's competing billed-request rule.

# Consequences

The summary and timeline use one finalized selection, so duplicate or replayed physical records
cannot create extra row cost. Every selected amount has either an eligible event or an explicit
usage row, and both passes operate on one file snapshot when `SourceSize` is available.

Unclean partitions show request content without per-request metrics and show authoritative
per-model session-usage rows. This avoids assigning a nonlinear tiered-price residual to arbitrary
requests.

The summary-cache fingerprint advances to v21 because older entries contain neither the ledger nor
the snapshot bound. Ledger storage remains proportional to accepted requests or fallback models,
and agent logs remain read-only.

# Alternatives

Keeping a predicate in both passes was rejected because final partition cleanliness is known only
at end of file and duplicated selection already caused a pricing defect.

Distributing aggregate residuals across request rows was rejected because marginal price tiers make
the result dependent on an invented distribution.

Scanning the file again to find a missing bridge was rejected because detail loading already has
one full pass. Speculative processing preserves the one-pass bound.

Changing the replay boundary in this work was rejected because it would move Info-tab totals
without the parent-child oracle needed to establish the correct policy.

# Notes

[Counter-segment partitioning](./20260905-codex-counter-segments.md) revises
whole-file reconciliation and aggregate timeline rows. Reconciliation now applies
per cumulative-counter segment, and the Info tab displays aggregate amounts as
`unattributed` usage. This record remains accepted: its ledger, snapshot bound,
and request attribution rules still apply.
