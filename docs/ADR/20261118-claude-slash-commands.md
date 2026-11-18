---
date: "2026-11-18"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code logs a typed slash command as a user record that holds a wrapper:
`<command-name>/goal</command-name>`, a `<command-message>` tag, and an optional
`<command-args>` tag with the typed arguments. The same wrapper can also arrive as the prompt of a
`queued_command` attachment. [Harness turn labels](./20260723-harness-turn-labels.md) counted
these wrappers as harness input. `show` therefore marked them `harness: true`, and the TUI showed
them as `harness:` rows, although the user typed them.

In the local logs, Claude Code 2.1.319 and older also log a typed `/compact` as a plain user
record whose text is `/compact`. We call that record the plain copy. Its timestamp is 1 to 74 ms
before the wrapper's, and the compaction boundary and summary lie between the two records.
Claude Code 2.1.322 and newer log the wrapper alone. The logs hold no session from 2.1.320 or
2.1.321, and no other command has a plain copy.

# Decision

This record replaces the harness classification of Claude slash-command wrappers in
[Harness turn labels](./20260723-harness-turn-labels.md).

When the Claude parser loads a timeline, it converts a wrapper to the text `/name args`, or
`/name` when the arguments are empty. The name must start with `/` and hold no whitespace, or the
wrapper keeps its text and stays harness input. The converted event follows the usual harness
rules for its record, such as `isMeta`. Summary parsing keeps the raw record text, so titles,
message counts, and the cache stay unchanged.

The parser drops a converted wrapper when its plain copy exists: a plain, non-harness user event
with the same converted text that appears earlier in the file and is stamped 0 to 1 s before the
wrapper. A converted wrapper is never a plain copy, so two `/compact` calls logged as wrappers
alone keep two events.

# Alternatives

We rejected matching on `promptId`, because it differed within 23 of the 219 local pairs of a
plain copy and its wrapper. We rejected matching adjacent records, because 3 to 21 records
separated the two. The 1 s window is far wider than the 74 ms maximum gap. A plain prompt and a
wrapper with the same text that are more than 1 s apart count as two separate commands.
