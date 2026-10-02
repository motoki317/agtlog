---
date: "2026-08-22"
author: "@motoki317"
status: "accepted"
---

# Context

A Codex rollout is the append-only JSONL file for one session. Follow mode parsed each changed
rollout again from byte zero, so the cost of a refresh grew with the full file.

The summary parser also accepts a final line without a newline. A watcher can read that line before
Codex finishes the write. If the parser advances past this fragment, the next parse starts inside
one JSON record.

# Decision

The Codex adapter owns an opaque checkpoint. It holds the summary accumulator and the data for the
validity checks below. A full parse first runs a **boundary scan** over the start of the file, which
finds the replay boundary of a forked sidecar. The checkpoint also holds that boundary. It
identifies the last complete line by its byte length and SHA-256 hash and does not store the line.
The accumulator keeps summary fields, such as the title, working directory, and git branch, but no
raw log lines.

The follower stores checkpoints by path beside its [session
index](./20260822-follow-session-index.md). Other adapters use full parsing.

A checkpoint resumes only after these checks pass:

- The current file size is at least the previous observed size.
- The previous head hash matches. The hash covers 4 KiB, or the observed file size when that size is
  smaller.
- The line that ends at the resume offset still has the stored length and hash.
- The resume offset is zero or follows a newline.

Any failed check starts a full parse. After a resumed scan, the parser runs these checks again. It
also checks that the path still names the opened file (`os.SameFile`). It then compares the hash of
the bytes after the resume offset with the hash taken before the scan. If any check fails, it starts
a full parse.

A parse stores a checkpoint only if the path still names the opened file. A full parse also needs a
boundary scan that completed on a newline-terminated line. The scan completes after the first
`session_meta` record and, for a forked sidecar, after the first two `token_count` records with a
valid `last_token_usage`. A parse failure or path removal deletes the checkpoint.

The accumulator consumes only newline-terminated lines. For the current summary, the parser applies
a valid trailing fragment to a temporary copy of the accumulator. The stored resume offset stays at
the end of the last complete line.

Checkpoints live only in follower memory. The summary cache keeps its whole-file fingerprint and
stores only the finished session.

# Consequences

A resumed parse skips the boundary scan and reuses the stored replay boundary and all prior summary
state. Each active changed path keeps accumulator state in memory. Parser finalization rebuilds
usage and request slices from that state, then reprices.

# Impact

The validity checks detect truncation, head replacement, and a changed resume boundary. They do not
detect a rewrite between the head and the boundary that does not shrink the file. Such a rewrite can
reuse stale accumulator values and produce incorrect totals. Codex rollouts stay subject to the
append-only assumption.

# Alternatives

We rejected persisting checkpoints in the summary cache because accumulator state is private to the
parser and valid only for one live file history.

We rejected advancing to the physical end of every read because a trailing fragment can end in the
middle of a JSON record.
