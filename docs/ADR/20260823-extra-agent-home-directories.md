---
date: "2026-08-23"
author: "@motoki317"
status: "accepted"
---

# Context

agtlog found logs through each agent's own environment variable or its built-in default. Those
variables also tell Claude Code and Codex where to write. A user could not add an archive or a
second account without changing an agent's write location.

agtlog also accepted a comma-separated list in `CLAUDE_CONFIG_DIR`, although Claude Code treats the
variable as one directory. This difference made a directory name with a comma ambiguous and gave
the variable a second meaning.

Codex can store a subagent in a separate rollout file. When two homes contain that sidecar, the
child copies share one `(agent, ParentID, ID)` link key. Graph linking treats that key as ambiguous
and detaches the child unless discovery reconciles the copies first.

# Decision

We added the repeatable `--claude-dir` and `--codex-dir` flags and the `AGTLOG_CLAUDE_DIRS` and
`AGTLOG_CODEX_DIRS` environment variables, which use the platform path-list separator. Each value
names an agent home. agtlog reads `projects` under a Claude home and `sessions` under a Codex home.

Configured homes extend the existing roots. A flag replaces its matching `AGTLOG_*_DIRS` value, but
not the agent's environment variable or built-in default. `CLAUDE_CONFIG_DIR` names one directory,
as in Claude Code, and `CODEX_HOME` stays one directory.

Before discovery, agtlog validates each configured home of the agents that `--agent` selects. If a
named path is missing or is not a directory, agtlog stops with a usage error instead of an empty
result. Validation checks the named home, not its derived `projects` or `sessions` directory, so a
new home passes before it contains logs. Agent environment variables and built-in defaults stay
best-effort, because they are implicit state that can legitimately have no logs.

Discovery reconciles byte-identical session copies while it builds the source snapshot, before
graph linking. The Codex sidecar case requires that order, and the rule applies to both agents.
Candidates share an agent kind and session ID. agtlog also requires equivalent parsed state and
equal SHA-256 digests for every source file in the parsed tree. It keeps the copy with the
lexicographically smallest cleaned path.

An ID, a file size, or a parsed summary cannot prove equality. A digest mismatch or a read error
keeps both copies. The comparison clears physical paths and recalculated ownership fields, then
uses `reflect.DeepEqual`. A difference in a field that a later change adds to `Session` therefore
also keeps both copies.

Unproven duplicates stay separate sessions. The machine CLI excludes every session whose canonical
ref collides with another, reports each as an `unaddressable_session` warning, and fails a selector
that names one. An archive copy that has diverged from its live session therefore removes both
copies from `list`. Discovery returns one logical copy to every caller, so the machine CLI does no
mirror reconciliation of its own.

When startup discovery finds mirrored copies, it keeps the unlinked parser output of every
discovered session by physical path. It also records the mirrored agent and session IDs and the
source paths of every copy. A follow change or removal that touches those IDs or paths updates the
kept entries. The follower then runs the same snapshot builder before it emits a `SessionUpdate`.
If the lexical winner is deleted, the update therefore lists its path in `RemovedPaths` and the next
surviving copy in `Sessions`. If copies later fail the parsed-state or digest check, the follower
emits them separately, because suppressing one would discard data.

Each rebuilt snapshot is authoritative for its top-level `Session.Path` values. The follower keeps
the paths returned by the initial discovery and by each rebuild. On the next rebuild, it adds to
`RemovedPaths` each earlier path that the rebuild omits, even if its file still exists. The rebuilt
`Sessions` set upserts the paths that reappear after the copies diverge. Deletion, promotion,
divergence, and re-convergence are therefore reversible for front ends that keep rows by physical
path.

The watcher starts before startup discovery to avoid an observation gap. The follower handles no
change until that discovery finishes, so no update reaches the TUI before discovery registers the
mirrored copies. The follower reads the mirror state that the latest `Registry.Discover` published,
so a manual refresh with `r` can replace it. If startup found no mirrors and no Codex change is
indexed yet, the follower stops reading mirror state after its first change.

Partial mirrors still go through `AttributeOwnership`: their files differ, but their billed ledger
entries can overlap. Codex `RequestUsage` entries have no `MessageID`, so a synthetic key combines
the session ID, the record offset, and the usage fields. The owner is the copy with the earlier
start time, then the smaller session ID, then the smaller cleaned path. Copies of one session share
an ID and usually a start time, so the cleaned path usually decides. Attribution deducts shared
entries from the other copy's owned totals.

# Impact

Each additional root adds one discovery walk and, when watching is on, one recursive watch tree. If
the cache directory contains an agent home or lies inside one, agtlog disables its summary and
pricing caches and rejects an explicit price refresh.

Source hashing runs only after a collision on agent and session ID passes the file-size and
parsed-state checks. Snapshot assembly memoizes digests by source path for one snapshot. Until its
first Codex change, a setup without proven mirrors keeps no [follow
index](./20260822-follow-session-index.md) and refreshes only the affected files, with no
rediscovery and no unrelated parsing. The first Codex change builds the index, because separate
rollout files can change graph links.

# Alternatives

We rejected replacing the default roots, because the feature must combine active and archived
homes.

We rejected a configuration file, because flags and environment variables cover the request without
a format choice or a new dependency.

We rejected comma-separated `AGTLOG_*_DIRS` values, because commas are valid in paths. The platform
path-list separator matches `PATH` and avoids that ambiguity.
