---
date: "2026-09-05"
author: "@motoki317"
status: "accepted"
---

# Context

Codex cumulative `token_count` counters can restart within one rollout after a
resume or a follow-up turn to an idle subagent. Whole-file reconciliation then
replaces earlier usage with the last counter total and removes request pricing.

A local scan found no counter decreases in 1,196 files from CLI 0.144.1–0.147.0.
It found decreases in 14 of 414 files from 0.149.0, 40 of 586 from 0.151.0,
and one of 36 from 0.153.0–0.153.4. This establishes an observed boundary at
0.149.0, not a guarantee about every release or rollout.

Among 33 recent files with decreases, 31 reconciled within every counter segment.
The other two each contained one segment whose request usage did not reconcile.

# Decision

A valid cumulative `total_tokens` strictly below the running maximum closes the
current segment and resets its running maximum. Valid raw counters are nonnegative,
and cached input cannot exceed input. The existing forked-sidecar replay prefix is
exempt and applies only to the first segment. Re-logged counters that raise no
component maximum remain duplicates.

The first segment retains its replay baseline or zero. A later segment starts
with the first record's raw `total_token_usage − last_token_usage` when
`last_token_usage` is valid and every field can be subtracted. Otherwise its
baseline is zero. A restart with `total == last` thus starts at zero.
A rewind with `total > last` excludes usage before its first
request instead of counting that usage twice.

Each segment uses the existing partition rule. Its own total is its final total
minus its baseline. It is clean only when that subtraction is valid, at least one
request was accepted, and accepted request usage sums to its own total. Clean
segments retain request offsets. An unclean segment retains aggregate ledger
entries with offset −1. All of its own total uses the model active at the last
cumulative record, including usage from earlier models in that segment. If the own total is invalid,
aggregate entries use the accepted per-model sums.

A request is accepted when its `last_token_usage` is valid and its addition does
not overflow either the model sum or the segment sum. With a valid cumulative
counter, at least one component must advance its running maximum.

The session sums usage by model across segments and concatenates their ledgers in
file order. Segment closure occurs during ingestion. Finalization reads the open
segment without mutation, so checkpoints retain both closed and open work.

Aggregate ledger entries no longer create timeline events. The Info tab shows
`unattributed` token flow and priced cost per model under Cost. It explains why
those spans have no turn costs. Physical requests without eligible content rows
and ledger offsets absent from the snapshot retain their explicit usage rows.

# Consequences

A counter reset no longer discards earlier totals or pricing from clean segments.
An unclean segment leaves its own turn rows unpriced without affecting clean
segments. Aggregate amounts remain visible before timeline events load.

Closed storage contains only ledger records. Session totals derive from those
records with the existing saturating usage arithmetic. Segment closure adds no
file pass, and total ingestion work remains linear in records and accepted usage.

# Impact

The summary-cache fingerprint advances from `codex-parser-v24` to
`codex-parser-v25`. Old cached summaries must be parsed again.

`UsageAggregate` and the optional CLI `usage_aggregate` field are removed.
CLI schema version 1 remains: the field occurred only on rows that no longer
exist. Agent logs remain read-only.

# Alternatives

Whole-file lumping was rejected because a reset replaces earlier billed usage
with the last segment's total and hides otherwise valid request pricing.

Residual redistribution was rejected because it invents request attribution and
can change nonlinear tiered pricing. Unclean segments remain lumped.

Adopting `token_usage_record` now was rejected because it is absent in
0.149–0.151 rollouts. Its exact response ledger needs a separate policy for files
written by mixed CLI versions. Usage omitted from `token_count` remains outside
this change.

# Notes

This decision revises the aggregate timeline display in
[the usage-ledger decision](./20260726-codex-usage-ledger.md). The ledger, snapshot
bound, and attribution rules remain binding.
[Summary checkpoints](./20260822-codex-summary-checkpoints.md) retain the segment
state and continue to require append-only history.
