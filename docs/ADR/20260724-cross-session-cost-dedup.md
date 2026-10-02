---
date: "2026-07-24"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code copies prior assistant records into resumed and forked session files. The copies keep
the same `messageId`, `requestId`, usage, UUID, and timestamp, and only `sessionId` changes. Before
this decision, agtlog deduplicated per file, so it counted one billed request once in every file
that replayed it.

The global-dedup approach comes from
[ccusage](https://github.com/ccusage/ccusage/blob/main/rust/crates/ccusage/src/adapter/claude/mod.rs),
which keeps one copy of each Claude request by message ID and request ID. agtlog uses that global
identity, but it needs session-level attribution because its list and detail views show individual
session files.

Claude logs have no readable pointer from a resumed or forked file to the source session.
`parentUuid` resolves records within each file, and `logicalParentUuid` marks in-file rewind or
compaction structure, not a cross-session relationship.

# Decision

We deduplicate billable Claude requests globally across the top-level session set by
`(agent, messageId, requestId)`. A Claude request with an empty `messageId` is not safe to match, so
every file in which it appears owns it. We first excluded Codex because we observed no such copies
in Codex logs. [Extra agent home directories](./20260823-extra-agent-home-directories.md) later
applied the same owner rule to Codex partial mirrors. These are copies of one session in two agent
homes whose files differ but share billed requests. A Codex request has no message ID, so its key
combines the session ID, the record offset, and the usage fields.

The origin owns a shared request. The origin is the session with the earliest `StartedAt`. Equal
start times fall back to the lexicographically smallest session `ID`, and equal IDs fall back to the
smallest cleaned path. A full transcript replay can copy the source session's first record and
produce equal `StartedAt` values. The tiebreak is then arbitrary but deterministic. The grand total
stays correct even if the chosen owner is not the session that a person sees as the origin.

The Claude parser keeps a request ledger of identity and usage, `Session.Requests`, in the summary
cache beside the token data in `Session.Usage`. `AttributeOwnership` in `internal/source` selects
owners in two passes over the full set. It recomputes each session's replayed cost, usage, and
request count. For each session, it also records the origin sessions of its replayed requests, with
the cost and request count per origin. Discovery and every live session update run it again.

Every top-level session total that agtlog shows or reports is owned. In the TUI, these are list
rows, the list grand total, the detail headline, and Overview Activity. In the CLI, they are the
token and cost totals of `list` and `show`. `Session.Usage`, `Session.Cost`, model costs, and
timeline events stay gross. If duplicates exist, the [Overview tab](./20261001-overview-tab.md)
shows owned and gross totals, the replayed amount and request count, and the origin sessions. It
replaced the original Info tab.

# Consequences

The detail view reconciles the owned headline with the gross timeline without marking or hiding
replayed events.

Attribution fields exist only at runtime: the summary cache excludes them, and agtlog recomputes
them after load. Attribution is linear in the number of top-level ledger entries, which keeps live
re-attribution practical at interactive scale.

# Impact

The decision affects Claude top-level session summaries and the owned totals of both agents. It does
not change Codex parsing, agent-log files, pricing, per-event timeline costs, or the recursive
subagent rollup. A top-level session's ledger contains only that file's requests, so cross-session
deduplication inside subagent files is out of scope.

# Alternatives

We rejected letting the first scanned copy own the request, as a streaming global dedup does.
Parallel file processing and scan order do not identify the origin.

We rejected letting the least recently updated session own the request because `UpdatedAt` is a
last-activity signal. An origin that continues for days can have a later `UpdatedAt` than a short
replay.

We rejected file birth time because the metadata is not portable and makes ownership depend on the
filesystem.

We deferred replacing the gross aggregates with the request ledger. The replacement would have
broadened a cost-correction change and risked the gross timeline and subagent behavior.
[Summary cache repricing](./20260815-summary-cache-repricing.md) later priced each session from
`Requests`. `Session.Usage` still duplicates the ledger's token data, and a later cleanup can derive
it from `Requests`.
