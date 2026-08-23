---
date: "2026-08-23"
author: "@motoki317"
status: "accepted"
---

# Context

agtlog previously found logs from each agent's own environment variable or its
built-in default. Those variables also tell Claude Code and Codex where to write.
A user had no way to add an archive or second account without changing an
agent's write location.

`CLAUDE_CONFIG_DIR` also accepted a comma-separated list in agtlog, although
Claude Code treats it as one directory. This difference made a comma-bearing
directory ambiguous and gave the variable a second meaning.

Codex can store a subagent in a separate rollout file. When two homes contain
that sidecar, the child copies share one `(agent, ParentID, ID)` link key. Graph
linking treats that key as ambiguous and detaches the child unless discovery
reconciles the copies first.

# Decision

Add repeatable `--claude-dir` and `--codex-dir` flags. Add
`AGTLOG_CLAUDE_DIRS` and `AGTLOG_CODEX_DIRS` environment variables that use the
platform path-list separator. Each value names an agent home, and agtlog appends
the agent's log suffix.

Configured homes extend the existing roots. A flag replaces its matching
`AGTLOG_*_DIRS` value, but it does not replace the agent environment variable or
built-in defaults.

Validate every selected, configured home before discovery. A missing or
non-directory named path makes explicit input fail. Agent environment
variables and built-in defaults remain best-effort because they represent
implicit state that can legitimately have no logs.

Reconcile byte-identical session copies during source snapshot construction,
before graph linking. Candidates share an agent kind and session ID. agtlog also
requires equivalent parsed state and SHA-256 digests for every source file in
the parsed tree. It retains the copy with the lexicographically smallest cleaned
path. This rule applies to both agent types, and the Codex sidecar case requires
the pre-link order.

An ID, file size, or parsed summary cannot prove equality. A digest mismatch or
read error retains both copies. Graph linking preserves ambiguous child
identities, and the machine CLI rejects duplicate canonical refs. A field added
to `Session` also participates in `reflect.DeepEqual`, so a difference retains
both copies. Discovery returns one logical copy to every caller, so the machine
CLI performs no additional mirror reconciliation.

`AttributeOwnership` remains for partial mirrors. Their files differ, but their
billed ledger entries can overlap. Codex `RequestUsage` entries lack a
`MessageID`, so a synthetic key uses the session ID, record offset, and usage
fields. The existing earliest-session rule selects the owner. Attribution
deducts shared entries from the later copy's owned totals after exact mirrors
are gone.

Treat `CLAUDE_CONFIG_DIR` as one directory, matching Claude Code.
`CODEX_HOME` remains one directory.

# Consequences

Existing root order stays stable, and roots removed by path normalization do not
add discovery or watch work. A typo in explicit configuration produces a usage
error instead of an empty result.

# Impact

Each additional root adds one discovery walk and, when watching is enabled, one
live filesystem watch. Configured roots remain read-only. If the cache contains
or falls inside an agent home, agtlog disables session caching and rejects an
explicit price refresh.

Source hashing runs only after an agent-and-session-ID collision passes the file
size and parsed-state checks. Digests are memoized by source path for one
snapshot. A setup without identity collisions pays no hashing cost. The machine
CLI and TUI consume the same reconciled snapshot.

# Alternatives

Replacing the default roots was rejected because the feature must combine active
and archived homes. Users who need one non-default home can set the agent's own
environment variable for one agtlog command.

A configuration file was rejected because flags and environment variables cover
the request without a format choice or a new dependency.

Comma-separated `AGTLOG_*_DIRS` values were rejected because commas are valid in
paths. The platform path-list separator matches `PATH` and avoids that ambiguity.

# Notes

agtlog validates the named home, not its derived `projects` or `sessions`
directory. A new home can therefore pass validation before it contains logs.
