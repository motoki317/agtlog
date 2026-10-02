---
date: "2026-08-15"
author: "@motoki317"
status: "accepted"
---

# Context

A Codex sidecar can carry `parent_thread_id` even when the parent log has no parseable spawn
announcement. Three observed orphan classes exposed this gap:

- Codex CLI 0.147.0 wraps the spawn in an `item_completed` envelope.
- `multi_agent_v1` exec spawns announce nothing in the parent log.
- A parent session can reuse one `agent_path` across multiple spawns.

Before this decision, the graph linker trusted only parent announcements. The parser keeps one
placeholder per agent path, and a repeated spawn overwrites the placeholder's `ID` with its own
thread ID. An earlier sidecar then lost its node.

# Decision

We link a parsed sidecar to the same-agent session whose `ID` matches the sidecar's `ParentID`, if
exactly one parsed session has that `ID`. If a parent announcement identifies exactly one parsed
child, the linker replaces the parser's placeholder node with that sidecar. The linker appends
child-driven links in `StartedAt` order, then `ID` order. It rejects cycles and a child that another
parent already owns. It also rejects a child whose `(agent, parent, ID)` identity another parsed
session shares, because it cannot select one owner safely. A rejected link leaves the child under
its existing owner, or in the root list if it has none.

From the wrapped `item_completed` envelope, we decode only `UserMessage`, `AgentMessage`, and
`SubAgentActivity` items. We do not parse the `world_state` subagent roster, and we do not use
`agent_nickname` as a display label.

# Consequences

A repeated agent path keeps one nested node per parsed sidecar. The detail timeline shows the three
wrapped item types. It skips other wrapped item types so that it does not duplicate items that
`response_item` records also log.

The machine-readable CLI addresses each node by a canonical ref. Among children of one parent that
share an agent path, the first in `Subagents` order keeps that path as its ref. Each later one ends
its ref with its thread ID. This order is not spawn order. The parser's placeholder carries the
thread ID of the last announced spawn, so that sidecar takes the placeholder's slot. The linker
appends earlier spawns after it.

If two child refs still collide, both children stay in the graph, but the CLI reports each one as
`unaddressable_session`. If a root ref collides, the CLI reports the root as unaddressable and omits
the root and its subagents.

# Alternatives

We rejected parent-only linking because two supported spawn paths produce no usable parent
announcement.

We rejected parsing `world_state` because the child's `ParentID` already provides the ownership
edge. We rejected `agent_nickname` as a label. A sidecar with an `agent_path` takes its title from
the last path component, and a sidecar without one takes its title from its first user message.
