---
date: "2026-07-25"
author: "@motoki317"
status: "superseded"
---

# Context

Parsers truncated event fields to 4096 runes, so an item view could not recover the omitted middle
of a field. Each event also retained a bounded copy of its source JSON, although the reader saw raw
content only on request. Measurements on the largest available sessions showed that the retained
raw records cost more memory than complete extracted fields.

# Decision

We kept complete extracted fields in memory and applied the preview bounds only in the timeline. We
replaced each retained raw string with a reference to its source record. The item view read the
record only after the reader pressed `R`, so opening an item stayed synchronous. It showed the
record source-exact on one line, with terminal control characters escaped.

[Always-present raw item records](./20260726-always-present-raw-item-records.md) supersedes this
record. It keeps the complete fields, the record references, the symlink and digest checks on each
read, the memory measurements, and the fallback plans if memory or latency grows. It shows Raw on
every item open as indented JSON, because the `R` toggle spent a mode change on one section and
compact JSONL was hard to scan.

# Alternatives

We rejected re-parsing the whole file when an item opens, and rehydrating each event from per-line
coordinates. The successor records both reasons, which remain in force.
