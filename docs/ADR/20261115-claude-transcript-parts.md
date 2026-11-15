---
date: "2026-11-15"
author: "@motoki317"
status: "accepted"
---

# Context

The Claude summary cache stores one entry per top-level session. The entry's fingerprint covers the
main transcript, every file in the directory named after it (its path without `.jsonl`), and the
legacy `agent-*.jsonl` files beside it. An append to any one of those files makes the whole entry
stale. Each stale entry sends the main transcript and every subagent transcript through `parseFile`
again. `show` and `list` discover the whole session set for
[cross-session deduplication](./20260724-cross-session-cost-dedup.md), so one run reparses every
session that changed since the previous run.

One measured session has a 102 MB main transcript and 142 subagent transcripts that total 315 MB.
While the session was unchanged, `show` took 0.4 s. After one appended line, in the main transcript
or in one subagent transcript, it took 2.3 s. Most of that time went to subagent transcripts that did
not change. A status-line script outside this repository runs `show` on every render and kills it
after 2 seconds.

`parseFile` reads one transcript and returns three values: its session node without subagents, its
workflow names, and its title prompt. The result depends only on that file's bytes. After every file
is parsed, tree assembly links the nodes and resolves title collisions, and pricing runs on the whole
tree.

# Decision

We store the `parseFile` result of each transcript in a part entry. When a summary is stale, the
parser reparses only the transcripts whose part does not match the file.

The registry in `internal/source` owns part storage. A part entry lives in the adapter's cache
namespace beside the summary entries. Its envelope holds a version, the agent, the source path, and
the adapter's payload as raw JSON. The registry checks the envelope and the `maxSummaryCacheBytes`
limit.

The adapter owns the payload. An adapter that implements `ParseWithPartsContext` receives a `load`
and a `store` callback for each parse. The Claude payload holds the file's stat (path, mtime, and
size) and the `parseFile` result. The parser stats the file before and after `parseFile`, and it
stores the part only if the two stats match. On load, it ignores a part whose stored stat differs
from the file's current stat.

The parser encodes the payload as soon as `parseFile` returns. Tree assembly renames colliding
titles, raises a parent's `UpdatedAt`, and sets `SpawnCallID`, so an encoding after it stores values
that come from other files. Each load decodes a fresh copy, because tree assembly mutates the nodes.

The summary entry and its fingerprint do not change, and they stay the fast path for an unchanged
session.

# Consequences

On the measured session, one appended main line takes 0.9 s, and one appended subagent line takes
0.4 s. The Claude cache namespace for that session grows from 3.6 MB to 7.4 MB, because each
transcript's `parseFile` result is stored in its part and again inside the summary entry.

# Impact

Parts of deleted transcripts stay on disk. Follow mode removes the summary entry of a removed
session, but not its parts. Codex does not implement `ParseWithPartsContext`. Parts live in the
namespace that `CacheFingerprint` selects. A bump of the fingerprint therefore abandons the old parts
on disk together with the old summaries.

# Alternatives

We rejected reusing subagent nodes from the stale summary entry. Those nodes come out of tree
assembly, so the parser cannot recover the `parseFile` result from them.

We rejected a stat check in the registry. We plan to let the Claude adapter resume a grown
transcript from its old part, and that needs the part of a file whose stat changed.

We rejected passing the `load` and `store` callbacks through a `context.Context` value or through an
interface type declared in `internal/source`. Either one makes the Claude adapter import
`internal/source`. Both adapters import only `internal/cost`, `internal/model`, and
`internal/source/jsonl`, and the registry reaches them only through interfaces.
