---
date: "2026-11-15"
author: "@motoki317"
status: "accepted"
---

# Context

Under [Claude transcript parts](./20261115-claude-transcript-parts.md), each part held the
`parseFile` result of one transcript, so a stale summary reparsed only the transcripts that changed.
A transcript that grew still went through `parseFile` from byte 0. On the session measured in that
record, one appended main line still made `show` take more than twice as long as it took on the
unchanged session. Most of the difference was the parse of the 102 MB main transcript, and every
append to an active session's main transcript repeated it.

[Codex summary checkpoints](./20260822-codex-summary-checkpoints.md) already resume a grown Codex
rollout after its last validated complete line, from state that the follower keeps in memory. This
record reuses its checks and rejects its in-memory storage.

# Decision

We store resumable parse state in each Claude part, and the parser resumes a grown transcript from
that part. A part holds an accumulator and a checkpoint. This replaces the payload and the load rule
of [Claude transcript parts](./20261115-claude-transcript-parts.md). That payload held the stat and
the `parseFile` result, and that load rule ignored a part whose stat differed.

`parseFile` keeps its state in an accumulator. The accumulator ingests one line at a time and keeps
every usage record from before deduplication. Claude Code can log one request in several records,
and a record appended later can replace the one that counts. The `finish` method therefore
deduplicates all records again on every call and rebuilds `Usage`, `Requests`, and `Models` from the
result.

The checkpoint (`jsonl.Checkpoint`) holds:

- the file size, and the resume offset, which is the end of the last newline-terminated line
- the length and hash of the head, which is the first 4 KiB, or the whole file if it is smaller
- the length and hash of the line that ends at the resume offset

On load, the parser resumes only if all of these checks pass:

- The file is at least as large as the stored size.
- The head hash matches.
- The resume offset is 0, or the byte before it is a newline.
- The hash of the line that ends at the resume offset matches.

The parser then ingests each newline-terminated line after the resume offset into the stored
accumulator and moves the offset past it. A final line without a newline goes into a copy of the
accumulator, for this parse only. The stored offset therefore never moves past the last newline.

A resumed parse must give the result of a parse from byte 0, and that parse counts a final line
without a newline. The parser therefore reads the bytes after the stored offset on every resume. It
does so even for a transcript that did not change since its part was stored. Such a resume happens
when another file of the session made the summary stale.

After the scan, a resumed parse runs the post-scan checks of
[Codex summary checkpoints](./20260822-codex-summary-checkpoints.md). If any of them fails, the
parser parses the file from byte 0. Both adapters take the checkpoint type, the load checks, and the
hash helpers from `internal/source/jsonl`.

# Consequences

Three rounds timed a build with parts only and a build with this record back to back on the same
session. The table gives the best time of each:

| Step | Parts only | With checkpoints |
| --- | --- | --- |
| One appended main line | 0.81 s | 0.43 s |
| One appended subagent line | 0.33 s | 0.42 s |

The appended subagent line got slower by 0.09 s, and 0.03 s of that is CPU time. For each
unchanged transcript, this build opens the file, hashes its head three times and its boundary line
twice, decodes a larger part, and deduplicates its usage records again. The parts-only build
compared only the stat.

In the same runs, the Claude cache namespace for that session grew from 7.5 MB to 8.5 MB, because a
part keeps the usage records from before deduplication. The part of the 102 MB main transcript is
1.5 MB, against the 64 MiB `maxSummaryCacheBytes`.

# Impact

The checks detect truncation, a changed head, and a changed boundary line. They do not detect a
rewrite between the head and the boundary that keeps the file size or grows it. After such a
rewrite, the part keeps stale usage records, and the summary reports wrong totals. We assume that
Claude transcripts are append-only, as Codex rollouts are. Claude Code writes compaction records as
new lines in the transcript that it compacts, which supports the assumption.

The part payload holds the JSON encoding of the accumulator, including its `usageRecord` and
`model.Session` values. A change to those encoded fields needs a `CacheFingerprint` bump. Without
one, an old part passes the checks and decodes each new field as its zero value. A part in the
format of [Claude transcript parts](./20261115-claude-transcript-parts.md) has no accumulator, so
the parser rejects it, and this record kept the fingerprint.

A part larger than `maxSummaryCacheBytes` is not stored, and its transcript is parsed from byte 0 on
every stale summary.

# Alternatives

We rejected keeping Claude resume state in memory, as the Codex follower does. `show` and `list`
start a new process on every run, so they get nothing from it.

The Codex record rejected persistence because accumulator state is private to the parser and valid
only for one live file history. The registry stores a part without decoding it, so only the Claude
parser reads the accumulator. The load checks tie a stored part to the bytes that it was parsed
from, as they tie an in-memory checkpoint. The same two points hold for Codex rollouts. This
record does not change Codex, and it measured no Codex rollout.
