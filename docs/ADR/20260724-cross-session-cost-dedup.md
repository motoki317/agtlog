---
date: "2026-07-24"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code copies prior assistant records into resumed and forked session files. The copies retain
the same `messageId`, `requestId`, usage, UUID, and timestamp, while `sessionId` changes. agtlog's
per-file deduplication therefore counted one billed request once in every file that replayed it.

The global-dedup approach comes from
[ccusage](https://github.com/ccusage/ccusage/blob/main/rust/crates/ccusage/src/adapter/claude/mod.rs),
originally `ryoppippi/ccusage`. Its Claude adapter loads entries in `load_entries_inner`, retains one
copy through `push_deduped_entry`, and keys usage with `usage_dedupe_hash(message_id, request_id)`.
agtlog uses that global identity but needs session-level attribution because its list and detail
views continue to show individual session files.

Claude logs expose no readable pointer from a resumed or forked file to the source session.
`parentUuid` resolves records within each file, and `logicalParentUuid` marks in-file rewind or
compaction structure rather than a cross-session relationship. The affected logs also contain no
`isSidechain` or Advisor records, disproving the earlier `/btw` sidechain explanation.

# Decision

Deduplicate billable Claude requests globally across the top-level session set by
`(agent, messageId, requestId)`. A request with an empty `messageId` is not safe to match and remains
owned by every file in which it appears, preserving the existing parser behavior.

The origin owns a shared request. The origin is the session with the earliest `StartedAt`; equal
start times use the lexicographically smallest session `ID`. Ownership never depends on
`UpdatedAt`, because a long-running origin can finish after its replay, or on file birth time,
because birth time is not portable.

The Claude parser retains a cached request ledger containing identity and usage. This duplicates
token data already held in `Session.Usage`, but avoids changing gross readers in the same release.
A pure two-pass attribution function clears runtime duplicate fields, selects owners, and
recomputes duplicate cost, usage, request count, model totals, and owner summaries over the full
set. Discovery and every live session update rerun that function.

List rows, the list grand total, and the detail headline display owned totals. `Session.Usage`,
`Session.Cost`, model costs, and timeline events remain gross. When duplicates exist, the info tab
shows owned and gross totals plus the replayed amount, request count, and origin sessions.

# Consequences

- Every nonempty shared request contributes to one displayed top-level session and once to the
  displayed grand total.
- Ownership is independent of concurrent parser completion and last-activity sorting.
- The info tab reconciles the owned headline with the gross timeline without marking or hiding
  replayed events.
- Runtime attribution and request prices are excluded from JSON caches and recomputed after load.
  The cached request ledger changes the Claude parser fingerprint from `v13` to `v14`.
- Attribution is linear in the number of top-level request-ledger entries, which keeps live
  re-attribution practical at interactive scale.

# Impact

The decision affects Claude top-level session summaries and TUI accounting. It does not change
Codex parsing, agent-log files, pricing, per-event timeline costs, or the existing recursive
subagent rollup. A top-level session's ledger contains only that file's requests, so cross-session
deduplication inside subagent files remains out of scope.

# Alternatives

**Let the first scanned copy own the request**, as a streaming global dedup naturally does, was
rejected because parallel file processing and scan order do not identify the origin.

**Let the least recently updated session own the request** was rejected because `UpdatedAt` is a
last-activity signal. An origin that continues for days can have a later `UpdatedAt` than a short
replay.

**Use file birth time** was rejected because the metadata is not portable and would make ownership
filesystem-dependent.

**Replace gross aggregates with the request ledger** was deferred. Deriving every existing reader
from one source would remove redundancy, but it broadens a cost-correction change and risks the
gross timeline and subagent behavior.

# Notes

A full transcript replay can copy the source session's first record and produce equal `StartedAt`
values. The smallest-ID tiebreak is arbitrary in that case, but deterministic; the displayed grand
total remains correct even if the chosen owner is not the human-perceived origin.
