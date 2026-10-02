---
date: "2026-10-01"
author: "@motoki317"
status: "accepted"
---

# Context

The Sessions list showed only a session's own messages beside its recursive cost. Tool activity and
delegated work increased the cost without increasing that count. CLI schema version 1 already
defines `messages` as the node's own message count.

# Decision

A turn is one user message, one agent message, or one tool call. A session's own turns are its
messages plus its tool calls. Its recursive turns cover every descendant in the same tree as cost,
including the children of Workflow group nodes. Tool outputs are not turns.

Claude counts nonempty user-text records and nonempty assistant text blocks as messages. It counts
every assistant `tool_use` block as a tool call, including Agent, Task, and Workflow spawns. A
tool-only assistant record adds tool calls but no messages. Codex counts `user_message` and
`agent_message` events as messages, including `item_completed` events that wrap a user or agent
message item. It counts `function_call` and `custom_tool_call` response items as tool calls. Codex
counts neither messages nor tool calls inside the
[fork replay prefix](./20260726-codex-usage-ledger.md).

The TUI Sessions and Subagents tables show recursive `TURNS` counts. The Subagents table replaced
its former `TOKENS` column with a `TURNS` column 5 cells wide, as in Sessions. The activity columns
of both tables therefore match, and the width that `TOKENS` used goes to the title. Tokens stay in
Overview Activity and Timeline. Overview Activity separates own, subagent, and total turns beside
owned tokens and owned cost. A leaf shows only its own activity.

CLI list text shows the same recursive count. CLI JSON adds `turns` to the shared session object
and `totals.turns` to `show`, and keeps `messages` and schema version 1. Older v1 producers omit
both fields, so consumers must tolerate their absence. [docs/cli.md](../cli.md) defines both fields
and the `turns` and `messages` sort keys.

# Impact

Both summary parsers store each node's own tool calls in `Session.ToolCalls`.

# Alternatives

We rejected redefining `messages`, because it breaks the CLI contract. We rejected counting only
the root's turns, because a row that includes delegated cost would then omit delegated activity. We
rejected loading timelines to count tools, because it defeats lazy detail loading.

# Notes

Resumed or forked Claude sessions that replay history count the replayed records as their own
turns, as the message count does. Cost deduplicates requests by identity, but turns have no
equivalent identity. Replay deduplication for Claude turns is outside this decision.
