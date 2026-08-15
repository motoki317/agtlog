---
date: "2026-08-15"
author: "@motoki317"
status: "accepted"
---

# Context

Codex sidecars can carry `parent_thread_id` even when the parent log has no parseable spawn announcement. Three observed orphan classes exposed this gap:

- Codex CLI 0.147.0 wraps the spawn in an `item_completed` envelope.
- `multi_agent_v1` exec spawns announce nothing in the parent log.
- A parent session can reuse one `agent_path` across multiple spawns.

The previous graph linker trusted parent announcements. The parser reused one placeholder for repeated agent paths. An earlier sidecar then lost its node.

# Decision

Link a parsed sidecar to a same-agent session whose `ID` matches the sidecar's `ParentID`. Keep parent announcements as substitution input when they identify one parsed child. Append child-driven links in `StartedAt` and `ID` order. Reject ambiguous identities, foreign ownership, and cycles.

Decode only `UserMessage`, `AgentMessage`, and `SubAgentActivity` items from the wrapped envelope. Do not parse the `world_state` subagent roster. Do not use `agent_nickname` as a display label.

# Consequences

The linker can recover sidecars from all three orphan classes when the child declares its parent. Repeated agent paths retain one nested node per parsed sidecar. The detail timeline shows the three wrapped item types. It does not duplicate other response items.

Canonical child refs keep the agent path for the first sibling. A repeated path uses the child's thread ID segment. If a child ref still collides, the child remains in the graph and its ref is omitted; a root ref collision invalidates the root.

# Impact

The change affects Codex summary parsing, Codex detail events, and registry graph linking. It does not write source logs or depend on parent-side roster text. Ambiguous `(agent, parent, ID)` identities remain unlinked. The linker cannot select one owner safely.

# Alternatives

Parent-only linking was rejected because two supported spawn paths produce no usable parent announcement.

Parsing `world_state` was rejected because child `ParentID` already provides the ownership edge. Using `agent_nickname` was rejected because some sidecars have no `agent_path`. Their first user message already supplies a title.

# Notes

The Codex parser fingerprint is `codex-parser-v24` so cached summaries reparse the wrapped envelope.
