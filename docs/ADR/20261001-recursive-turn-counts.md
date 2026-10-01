---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

The Sessions list showed only a session's own messages beside its recursive cost.
Tool activity and delegated work increased cost without increasing that count.
CLI schema version 1 already defines `messages` as the node's own message count.

# Decision

A turn is one user message, one agent message, or one tool call.
`Session.Turns()` adds the node's `Messages` and `ToolCalls`.
`TotalTurns()` includes every descendant in the same tree as cost, including
children of Workflow group nodes.

Claude counts every assistant `tool_use` block, including Agent, Task, and
Workflow spawns. Codex counts `function_call` and `custom_tool_call` response
items outside the [fork replay prefix](./20260726-codex-usage-ledger.md). Tool outputs do not count as turns.
Claude counts nonempty user-text records and assistant text blocks as messages.
Codex counts user and agent event messages, including completed-item wrappers.
Claude tool-only assistant records add tool calls but no messages.

The TUI Sessions and Subagents tables show recursive `TURNS` counts.
The Subagents table drops `TOKENS` so its activity columns match Sessions.
CLI list text shows the same turn count.
CLI JSON adds `turns` to the shared session object without changing `messages`
or schema version 1. Current producers always emit `turns`, including zero.
Consumers must tolerate its absence from older v1 producers.
CLI sorting accepts both recursive `turns` and own `messages`.

Overview Activity separates own, subagent, and total turns beside owned tokens
and owned cost. A leaf shows only its own activity.
CLI `show` adds `totals.turns` with integer `self`, `descendants`, and `total`
counts. Older v1 producers omit this object. Text output shows the same split
after `COST`. The shared `session.turns` field remains recursive.

# Consequences

The Subagents table replaces its token column with a five-cell turn column.
The remaining width goes to the title. Tokens remain in Overview Activity and Timeline.

# Impact

Both summary parsers retain an own tool-call count. Their fingerprints advance
to `claude-parser-v19` and `codex-parser-v26` to invalidate summaries without it.
The cache envelope is unchanged.

# Alternatives

Redefining `messages` would break the CLI contract. Counting only the root's
turns would omit delegated activity from a row that includes delegated cost.
Loading timelines to count tools would defeat lazy detail loading.

# Notes

Resumed or forked Claude sessions that replay history count those records as
their own turns, as the message count already does. Cost deduplicates requests
by identity, but turns have no equivalent identity. Replay deduplication for
Claude turns remains outside this decision.
