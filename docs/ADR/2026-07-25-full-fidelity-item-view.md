---
date: "2026-07-25"
author: "@motoki317"
status: "accepted"
---

# Context

Event fields were truncated to 4096 runes during parsing, so an item view could not recover content omitted from the middle.
Each event also retained a bounded copy of its source JSON, even though raw content was visible only on request.

Measurements from the largest available Claude and Codex sessions showed that retained raw records cost more memory than complete extracted fields:

| log size | extracted fields | share | retained raw records | share |
|---|---:|---:|---:|---:|
| 85.7 MB / 31,071 lines | 14.0 MB | 16% | at most 71.2 MB | 83% |
| 42.6 MB / 12,680 lines | 11.4 MB | 27% | at most 27.6 MB | 65% |
| 34.0 MB / 12,044 lines | 7.7 MB | 23% | at most 27.4 MB | 81% |

The JSONL reader accepts records up to its 16 MiB ceiling.
That ceiling defines full fidelity; the design does not promise unbounded source records.

# Decision

Keep complete extracted event fields in memory and apply the existing 4096-rune and 40-line bounds only when rendering the session timeline.
The item view renders the extracted fields without those bounds.

Replace the retained raw string with a process-local reference containing the physical file path, byte offset, byte length, and SHA-256 digest.
The item view reads that range asynchronously when raw display is first requested, validates the digest, and escapes terminal control characters without reformatting or encrypted-token elision.
A failed or stale read displays an unavailability reason and never falls back to derived or bounded content.

This remains a single-parser design.
Event extraction occurs only during the normal session parse; raw loading does not derive event fields.

# Consequences

Replacing retained raw strings with references is a net memory reduction on the measured sessions.
In the largest sample, complete extracted fields add about 14.0 MB while removing retained raw records saves as much as 71.2 MB.

Opening an item remains synchronous because raw I/O starts only after the user requests it.
The timeline retains its existing output and bounds text before wrapping, while the item renderer eagerly holds and lays out a loaded record.

A pathological session can retain more extracted text than the measured samples.
If that becomes a practical limit, add a per-session extracted-field budget that falls back to bounded fields.
Do not add a second extraction path.

# Impact

Agent logs remain read-only.
Raw reads reopen the physical file, reject symlinks and non-regular files, and reject content whose digest no longer matches.
Codex events attached to synthetic child sessions still reference the physical file that contained their record.

The eager item renderer processed a 16 MiB record in 273.577 ms at 61.33 MB/s in the acceptance benchmark, with 223,097,936 bytes allocated across 112 allocations.
If records near the ceiling make interaction unacceptably slow on supported systems, virtualize item rows rather than introducing a second parser.

# Alternatives

**Re-parse the whole file without bounds when an item opens.**
This repeats all parsing work, makes item latency scale with session size, and couples one event view to unrelated records.

**Rehydrate each event from stored per-line coordinates.**
This would require a second extraction path for isolated records.
That parser could drift from the normal full-session parser and produce different derived fields for the same event.

# Notes

Derived fields continue to elide encrypted tokens.
Only the explicitly requested raw view is source-exact.
