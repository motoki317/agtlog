---
date: "2026-07-26"
author: "@motoki317"
status: "accepted"
---

# Context

Each event once retained a bounded raw copy of its source record.
[Full-fidelity item view](./_20260725-full-fidelity-item-view.md) kept complete event fields in
memory and replaced each raw copy with a reference to its source record. Its item view read the
record only after the reader pressed `R`, and showed it source-exact on one line. The `R` key
controlled one trailing section, so inspecting a record took an extra mode change, and compact
JSONL was hard to scan.

That record measured the largest available Claude and Codex sessions. Retained raw records cost more
memory than complete event fields:

| log size | event fields | share of log | retained raw records | share of log |
|---|---:|---:|---:|---:|
| 85.7 MB / 31,071 lines | 14.0 MB | 16% | at most 71.2 MB | 83% |
| 42.6 MB / 12,680 lines | 11.4 MB | 27% | at most 27.6 MB | 65% |
| 34.0 MB / 12,044 lines | 7.7 MB | 23% | at most 27.4 MB | 81% |

The JSONL reader accepts records up to its 16 MiB ceiling. That ceiling defines full fidelity. We do
not promise unbounded source records.

# Decision

We keep the field and reference decisions of Full-fidelity item view, and we replace its raw
display.

Parsers keep complete event fields, such as the text, tool input, diff, and output of
`model.Event`. The timeline applies its 4096-rune and 40-line preview bounds only when it renders,
and the item view renders the fields without those bounds. In event fields, parsers replace each
Fernet-style encrypted string with `<encrypted N chars>`. Such a string starts with `gAAAA` and has
at least 64 characters. Raw shows it unchanged. Event extraction runs only in the normal session
parse, and a raw read extracts no event fields.

Each event keeps a process-local reference to its source record: the physical file path, the byte
offset, the byte length, and a SHA-256 digest. The path names the file that holds the record, which
can differ from the session's `Path`. A Codex child session can hold records from its parent's log.
A child that the parser builds from a spawn announcement can have a `Path` that ends in
`#<agent path>` and names no file. A raw read reopens the referenced file. It rejects symlinks,
non-regular files, and a record whose digest no longer matches. Agent logs stay read-only.

An item with a valid record reference always renders Raw as its final section. The item view
requests the record asynchronously when the item opens, so opening an item does not wait for file
I/O. While the read is pending or after it fails, Raw shows a loading or unavailability message. A
failed or stale read never falls back to event fields or bounded previews.

We indent valid JSON with `json.Indent` and two spaces. Each indented line is sanitized separately
for the terminal, so the indentation stays visible and JSON string escapes stay within their value.
Invalid JSON stays one sanitized line. The item view caches the prepared lines after the read, so
later rebuilds do not repeat the formatting.

# Consequences

Every item open with a record reference starts a read and lays out the complete Raw section
eagerly. On 2026-07-25, `BenchmarkItemRenderer16MiB` processed a 16 MiB record in 273.577 ms with
the one-line renderer of Full-fidelity item view. The benchmark now lays out the cached indented
lines. If records near the 16 MiB ceiling make the item view too slow, we will virtualize item rows
instead of adding a second parser.

A pathological session can retain more event text than the measured samples. If that becomes a
practical limit, we will add a per-session budget for event fields that falls back to bounded
fields.

# Alternatives

We rejected keeping the `R` toggle, because it spends a key and a mode change on one section.

We rejected showing the source JSON without indentation, because nested records stay hard to
inspect.

We rejected decoding and re-encoding JSON, because the round trip can change number or string
representations. `json.Indent` changes only insignificant whitespace.

We rejected re-parsing the whole file without bounds when an item opens. It repeats all parsing
work, makes item latency scale with session size, and couples one event view to unrelated records.

We rejected rehydrating each event from stored per-line coordinates. That needs a second extraction
path for isolated records. The second parser can drift from the full-session parser and derive
different fields for the same event.
