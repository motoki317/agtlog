---
date: "2026-08-05"
author: "@motoki317"
status: "accepted"
---

# Context

agtlog normalized, priced, and linked local Claude Code and Codex logs, but only the terminal UI
used that pipeline. No interface let a coding agent find a past failure and request the surrounding
events with bounded output.

The new interface needs stable addresses and a stable schema. Internal session IDs are not
sufficient. A later record can give a Codex inline subagent a thread ID after the parser first
creates the subagent from its agent path.

Search adds a correctness trap. Timeline cleanup removes harness-only `system-reminder`,
`permission-preamble`, and `local-command-caveat` blocks from parsed text. If a block occurs inside
a string, the surrounding text becomes adjacent after cleanup. A valid match in cleaned text can
therefore be absent from the raw JSONL bytes.

# Decision

We added three non-interactive verbs: `list`, `show`, and `search`. The terminal UI stays the
default when agtlog runs without arguments. JSON is the default command format, and
`--format text` is the terminal-safe alternative.

We defined versioned wire data-transfer objects (DTOs) in `internal/cli`. In version 1, a change to
a field's meaning, scope, unit, required status, nullability, or closed enum requires a version
bump. Consumers ignore unknown fields, so an optional addition keeps the version.

Every response uses canonical refs. A top-level ref is `<agent>:<root-id>`, and a descendant ref is
`<agent>:<root-id>#<subagent-path>`. `docs/cli.md` defines how agtlog builds `<subagent-path>`. Bare
IDs, prefixes, thread IDs, and file paths stay input-only selectors.

Token categories are disjoint: `uncached_input`, `output`, `cache_write`, and `cache_read` sum to
`total`. agtlog removes Codex cache reads from raw input before it emits `uncached_input`.

`search` matches substrings or regular expressions against the normalized fields of parsed
timelines. Summary filters reject unrelated candidates before their timelines open. Workers parse
candidates in parallel, and a coordinator commits hits in order of root `updated_at` descending,
then canonical ref, event, and field. Each worker releases decoded event timelines after it
extracts the bounded hit data.

The generated 1.3 GB benchmark shows that an exact corpus scan meets the five-second budget, so
version 1 needs no cleaned-text index.

# Consequences

Coding agents can use a bounded find-then-read loop: the canonical ref and event index of a search
hit address `show` directly.

List totals stay additive across top-level rows, because the earliest-started session owns a
replayed request. The smaller root ID and then the smaller cleaned path break a tie. Subcommands use
cached and embedded prices unless the caller passes `--refresh-prices`.

# Impact

The command entry point dispatches to the machine CLI only when the first argument is `list`,
`show`, or `search`.

Discovery returns structured per-path diagnostics. The terminal UI ignores them, and broad commands
convert them to warning objects. An unreadable session named by a `show` selector or by
`search --session` returns an error. `search --session` also returns an error when a known
descendant of that session is unreadable.

JSON response fields are required unless the CLI reference marks them optional. Text output
sanitizes untrusted control characters.

# Alternatives

We rejected `--json` on the terminal UI, because a screen snapshot has no verbs, no selectors, no
stable event indices, and no bounded paging.

We rejected JSON output of `internal/model`, because parser refactors can change the public
contract, and raw input tokens are not comparable across agents.

We rejected a raw-byte prefilter for search candidates, because cleanup creates text adjacency that
the source bytes do not contain. JSON escaping and Unicode folding add more false negatives.

We rejected an MCP server as the first interface, because the CLI is the smaller local composition
surface. An MCP server can use the CLI later if agent clients need one.

We left these features out of schema version 1, each with the trigger that brings it back:

- A cost-aggregation verb (`agtlog cost --group-by ...`). The
  [tool-landscape ADR](./20260719-tool-landscape.md) leaves the report-generator niche to other
  tools. We add the verb if agents repeatedly reimplement grouping over `list`.
- A `--follow` mode. We add it when an agent needs to watch a running session.
- `--fields` projection. `jq` covers it. We add it if payload size becomes the complaint.

# Notes

The CLI contract is in `docs/cli.md`. The benchmark in `internal/cli/corpus_benchmark_test.go`
generates 1,600 top-level sessions across both agents: 1,300,000,000 bytes of JSONL with varied
event counts and one Claude subagent file, so it does not measure the search cost of descendants or
Workflow groups. Its `absent-sentinel` search is a zero-hit worst case. A run writes the corpus to a
temporary directory:

```bash
go test -run '^$' -bench '^BenchmarkMachineReadableCLILatency$' \
  -benchtime=1x -count=1 ./internal/cli
```

On an Apple M4 Max, the maximum observed times were 0.083 seconds for `list` and 0.073 seconds for
`show`. A project-filtered search took at most 0.165 seconds, and a corpus-wide search at most 0.890
seconds. The acceptance budgets are 1.0 seconds for `list`, 1.5 for `show`, 2.0 for a
project-filtered search, and 5.0 for a corpus-wide search.
