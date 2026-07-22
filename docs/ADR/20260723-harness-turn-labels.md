---
date: "2026-07-23"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code and Codex both log some harness-injected model input as user-role records. Treating
every such record as human-authored makes skill bodies, task notifications, compaction summaries,
and dispatch preambles appear under the `you:` label.

The formats expose different classification signals. Claude records marker fields, an origin,
and wrapper prefixes. Codex distinguishes the human-facing `event_msg/user_message` from the
rendered model input in a user-role `response_item`. A shared classifier would hide these format
contracts without sharing any useful logic.

# Decision

`model.Event` carries a `Harness` boolean that only user-role render paths interpret. Each parser
sets the flag from its own log format. Claude harness markers take precedence over
`origin.kind: "human"`; when `origin` is absent and no marker matches, the parser treats the turn
as human. Codex flags user events from `response_item` and preserves an existing human
classification when a preferred mirrored copy replaces it.

Flagged records remain `EventUser`. The timeline changes only their label and prompt role, so
context lookahead, folding, and metrics continue to use the established user-turn path.

# Consequences

- Typed prompts retain `you:` and the user-prompt tint.
- Injected turns render as `harness:` with the existing system-prompt tint.
- Logs from Claude versions without `origin` keep plain unmarked prompts human-classified.
- Future format changes must update the parser that owns the affected record contract.

# Impact

The decision affects lazy event loading and user-prompt rendering. It does not change summary
parsing, event counts, pricing, cache fingerprints, or the read-only treatment of agent logs.

# Alternatives

**A new `EventKind`** was rejected because `nextRequestContext` and related folding and metric
paths identify prompts as `EventUser`; a new kind would break the context column unless those
paths duplicated user-turn behavior.

**An enum instead of a boolean** was rejected because no consumer distinguishes skill bodies,
task notifications, compaction summaries, wrappers, or dispatch preambles after classification.

**Defaulting an absent Claude `origin` to harness** was rejected because every unmarked human
prompt in a log predating the field would be mislabeled.

# Notes

Harness wrapper rows remain visible because hiding them is a separate timeline policy decision.
