---
date: "2026-08-22"
author: "@motoki317"
status: "accepted"
---

# Context

A Codex rollout is the append-only JSONL file for one session. Follow mode
reparsed each changed rollout from byte zero, so the cost grew with the full file.

The summary parser also accepts a final line without a newline. A watcher can read
that line before Codex finishes the write. Advancing past this fragment causes the
next parse to start inside one JSON record.

# Decision

The Codex adapter owns an opaque checkpoint. It stores the summary accumulator,
the fork replay result from the prefix scan, and the observed file size. It also
stores the resume offset, the head hash and length, and the last complete line.
The follower stores checkpoints by path beside its
[session index](./20260822-follow-session-index.md).
The checkpoint represents the last line with its byte length and SHA-256 hash,
not with retained log content. Other adapters continue to use full parsing.

A checkpoint resumes only after these checks pass:

- The current file size is at least the previous observed size.
- The previous head hash matches. The hash covers 4 KiB, or the observed file size
  when that size is smaller.
- The bytes before the resume offset match the hash of the last complete line.
- The resume offset is zero or follows a newline.

Any failed check starts a full parse. A parse failure or path removal deletes the
checkpoint.

The accumulator consumes only newline-terminated lines. For the current summary,
the parser applies a valid trailing fragment to a temporary accumulator copy.
The persistent resume offset remains at the last complete line.

Checkpoints remain in follower memory. The summary cache retains its whole-file
fingerprint and stores only the finished session.

# Consequences

An append-only refresh scans the bytes from the last complete line. The parser
reuses the prefix result and all prior summary state.

Each active changed path retains accumulator state in memory. Parser finalization
rebuilds usage and request slices from that state, then recalculates pricing.

# Impact

Agent logs remain read-only. Cache validation and cache write-through behavior do
not change. A process restart discards every checkpoint and performs a full parse
before it can resume.

The validity checks detect truncation, head replacement, and a changed resume
boundary. They do not detect a same-size rewrite between the head and boundary.
Such a rewrite can reuse stale accumulator values and produce incorrect totals.
Codex rollouts remain subject to the append-only assumption.

# Alternatives

Persisting checkpoints in the summary cache was rejected because accumulator
state is parser-private and valid only for one live file history.

Advancing to the physical end of every read was rejected because a trailing
fragment can end in the middle of a JSON record.

Reparsing from byte zero remained the fallback because it preserves existing
behavior after any failed validity check.

# Notes

Parity tests compare incremental and full parsing at line boundaries, mid-line
positions, and fixed-sequence chunk boundaries. They cover all Codex fixtures and
generated accounting cases.
