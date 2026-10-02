---
date: "2026-09-05"
author: "@motoki317"
status: "accepted"
---

# Context

Codex cumulative `token_count` counters can restart within one rollout after a resume or after a
follow-up turn to an idle subagent. Whole-file reconciliation then replaced earlier usage with the
last counter total and removed request pricing.

A local scan found no counter decreases in 1,196 files from CLI 0.144.1–0.147.0. It found decreases
in 14 of 414 files from 0.149.0, 40 of 586 from 0.151.0, and one of 36 from 0.153.0–0.153.4. This
establishes an observed boundary at 0.149.0, not a guarantee about every release or rollout.

In a separate check of 33 recent files with decreases, 31 reconciled within every counter segment.
The other two each contained one segment whose request usage did not reconcile.

# Decision

The parser tracks a running maximum of each cumulative counter component. A valid cumulative record
whose `total_tokens` is strictly below the running maximum of `total_tokens` closes the current
segment and opens the next one. The next segment starts with every running maximum at zero, and the
closing record is its first record. A valid raw counter is nonnegative, and its cached input does
not exceed its input. Records in the forked-sidecar replay prefix never close a segment, and the
prefix applies only to the first segment. Re-logged counters that raise no component maximum remain
duplicates.

The baseline of the first segment is the last cumulative total inside the replay prefix, or zero
without a prefix. The baseline of a later segment is its first record's raw
`total_token_usage − last_token_usage` if `last_token_usage` is valid and every field can be
subtracted. Otherwise its baseline is zero. If the counter starts over from zero, the first record
has `total == last`, and the segment starts at zero. If the counter falls to a nonzero value, the
first record has `total > last`. The baseline then excludes the earlier usage instead of counting
it twice.

Each segment uses the partition rule of the [usage ledger](./20260726-codex-usage-ledger.md). The
segment delta is its final cumulative total minus its baseline. The segment is clean only if the
subtraction is valid, it accepted at least one request, and the accepted usage sums to its delta. A
clean segment keeps its request offsets. An unclean segment keeps aggregate ledger entries with
offset −1. The cumulative counter carries no model. All of the delta therefore goes to the model
active at the last cumulative record, including usage from earlier models in that segment. If the
delta is absent or invalid, the aggregate entries use the accepted per-model sums.

A request is accepted if its `last_token_usage` is valid and its addition overflows neither the
model sum nor the segment sum. If the record has a valid cumulative counter, at least one component
must also advance its running maximum. An accepted request and an aggregate entry must bill at
least one token, so a segment that only replays inherited usage adds no ledger entry.

The session sums usage by model across segments and concatenates their ledgers in file order.
Segment closure happens during ingestion. Finalization reads the open segment without changing it,
so [summary checkpoints](./20260822-codex-summary-checkpoints.md) keep both closed and open work.

Aggregate ledger entries create no timeline events. This replaces the aggregate timeline rows of the
usage ledger, whose `SourceSize` bound and request attribution rules still apply. The Info tab shows
the `unattributed` token flow and priced cost per model, with a line that explains why unclean
segments have no turn costs. The [Overview tab](./20261001-overview-tab.md) replaced the Info tab
and keeps this display.

# Consequences

Aggregate amounts are visible before timeline events load.

Closed segments store only ledger records. Session totals derive from those records with the
saturating usage arithmetic. Segment closure adds no file pass, and total ingestion work stays
linear in records and accepted usage.

# Impact

We removed `UsageAggregate` and the optional CLI `usage_aggregate` field. The CLI schema stays at
version 1 because the field occurred only on the aggregate timeline rows that this decision removes.

# Alternatives

We rejected whole-file lumping because a reset replaces earlier billed usage with the last segment's
total and hides valid request pricing.

Some Codex rollouts also log `token_usage_record` records, which form an exact response ledger. We
rejected adopting them in this change because 0.149–0.151 rollouts lack them. Their ledger needs a
separate policy for files written by mixed CLI versions. agtlog reads usage only from `token_count`
records, so usage that those records omit stays uncounted.
