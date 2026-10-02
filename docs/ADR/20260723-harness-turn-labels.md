---
date: "2026-07-23"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code and Codex both log some harness-injected model input as user-role records. If every
such record counts as human-authored, skill bodies, task notifications, compaction summaries, and
slash-command echoes appear under the `you:` label.

The two formats expose different classification signals. Claude records marker fields, an origin,
and wrapper prefixes. Codex separates the human-facing `event_msg/user_message` from the rendered
model input in a user-role `response_item`. A shared classifier would hide these format contracts
and share no useful logic.

# Decision

`model.Event` carries a `Harness` boolean. Each parser sets it from its own log format, and a format
change must update the parser that owns the affected record contract. Claude harness markers take
precedence over `origin.kind: "human"`. If `origin` is absent and no marker matches, the Claude
parser treats the turn as human. The Codex parser flags every user-role `response_item` message and
leaves `event_msg/user_message` unflagged. A typed prompt appears in both forms. The parser merges
the two copies into one event when their text matches within `codexMirrorWindow` events. The merged
event stays human, whichever copy the log writes first.

Flagged records remain `EventUser`, so context lookahead, folding, and metrics keep the user-turn
path. In the TUI, only user-role rendering reads the flag. Typed prompts keep `you:` and the
user-prompt tint. Injected turns stay visible and render as `harness:` with the system-prompt tint.

Only detail loading sets the flag, and summary parsing, event counts, and pricing never read it. A
change to the classification therefore needs no `CacheFingerprint()` bump.

# Alternatives

We rejected **a new `EventKind`**. `nextRequestContext` and the related folding and metric paths
identify prompts as `EventUser`, so a new kind breaks the context column unless those paths
duplicate the user-turn behavior.

We rejected **an enum instead of a boolean**, because no consumer distinguishes skill bodies, task
notifications, compaction summaries, or slash-command echoes after classification.

We rejected **defaulting an absent Claude `origin` to harness**, because every unmarked human prompt
in a log that predates the field would be mislabeled.

# Notes

No record decides to hide `harness:` rows. CLI `show` passes the flag through as the event's
`harness` field.
