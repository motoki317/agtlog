---
date: "2026-11-15"
author: "@motoki317"
status: "accepted"
---

# Context

Since CLI 0.153, Codex writes one `token_usage_record` line per API response. The record carries
`response_id`, `thread_id`, and the usage of that response. agtlog built its billed-request ledger
only from `event_msg/token_count` records, and that ledger missed billed usage in three cases:

- A `compacted` record names its compaction request in `compaction_response_id`. Only the request's
  `token_usage_record` logs its usage. The `token_count` after the compaction has a zero
  `last_token_usage` and an unchanged cumulative total.
- In a paginated subagent rollout, Codex logged the first request's `token_count` again after
  `turn_aborted`, within the same second. The same-second replay heuristic of the
  [usage ledger](./20260726-codex-usage-ledger.md) then treated that request as replayed parent
  history and dropped it.
- A crash cut a rollout. A request kept its `token_usage_record` but lost its `token_count`.

A local scan compared the two ledgers on 491 rollouts written from 2026-10-15 to 2026-11-15. In 451
of them, the `token_count` ledger billed exactly the sum of the rollout's own records. The three
cases explain the other 40 exactly: 37 by compaction, 1 by the heuristic, and 2 by crashes. From the
earliest local record, on 2026-09-04, to 2026-11-15, the missed compaction requests cost about $63
at base rates, or 1.9% of agtlog's Codex total for that period.

For the format facts in this record, the scan read all 1,181 local rollouts that have records, from
CLI 0.153 to 0.163. No record carried a `thread_id` other than the `id` of the rollout's first
`session_meta`.

Since CLI 0.149, every local subagent rollout has `history_mode: "paginated"`. In all 2,030 of them,
the first `token_count` has a cumulative total equal to its own `last_token_usage`, so none carries
the parent's counter. The replay heuristic also examines rollouts with a `forked_from_id`, but no
local rollout of that kind is paginated, so this evidence covers only subagents.

# Decision

We bill a rollout from its own records if at least one `token_usage_record` qualifies. A record
qualifies if its `thread_id` equals the rollout's session ID, and if its usage passes
`validTokenUsage`, is nonzero, and does not overflow the sum of the qualifying records before it.
The session ID is the `id` of the first `session_meta`, or its `session_id` if `id` is empty. Each
qualifying record becomes one ledger entry at the record's byte offset, with the model of the latest
`turn_context` before it. `token_count` records then add no entry. The usage ledger deferred the
choice of a billing boundary for replayed parent records. The `thread_id` match settles it for these
rollouts, because a copied parent record would carry the parent's `thread_id`. Billing these
rollouts therefore needs neither the replay heuristic nor
[counter segments](./20260905-codex-counter-segments.md).

A rollout keeps the `token_count` ledger if it has no qualifying record. It also keeps the
`token_count` ledger if that ledger already holds a billed request outside the replay prefix when
the first qualifying record arrives. An older CLI wrote the start of such a rollout, and a newer CLI
resumed it. The `token_count` ledger then misses the later compaction requests but keeps the earlier
requests that no record logs.

The replay heuristic still decides the counts under either ledger: it excludes the messages and tool
calls of the replayed second. The heuristic finds that second from the timestamps of the first two
`token_count` records. A paginated rollout carries no parent counter, so there the heuristic can
only fire falsely. The parser applies it only if the first `session_meta` does not have
`history_mode: "paginated"`.

The timeline attaches an entry at a record offset by the rule that the usage ledger defines for a
`token_count` offset, with one change to the window of eligible rows. The window starts after the
previous `token_count` or after the previous record that holds a ledger entry, whichever is later.
Each local compaction request had no eligible row and became an explicit `usage` row.

# Alternatives

We rejected a per-request rule that pairs each record with its `token_count`. Only a resumed rollout
of the kind above needs it, and no local rollout is of that kind: in the 1,181 rollouts with
records, each `token_count` that advanced the cumulative total directly followed its own record, and
none came before the rollout's first record. The fallback guards against that kind of rollout and
costs it only its later compaction usage.

We rejected deduplicating records by `response_id`, because no `response_id` repeated in the 1,181
rollouts.

We rejected attaching compaction usage to the `compact` row. `codexUsageTarget` excludes compaction
rows, and that rule needs its own decision.
