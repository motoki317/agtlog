---
date: "2026-08-05"
author: "@motoki317"
status: "accepted"
---

# Context

agtlog already normalized, priced, and linked local Claude Code and Codex logs,
but only the terminal UI consumed that pipeline. No interface let a coding agent
find a past failure and request the surrounding events with bounded output.

The new interface needs stable addresses and a stable schema. Internal session
IDs are not sufficient because a later record can give a Codex inline subagent a
thread ID after the parser first creates it from its agent path.

Search adds a correctness trap. Timeline cleanup removes harness-only
`system-reminder`, `permission-preamble`, and `local-command-caveat` blocks from
parsed text. If a block occurs inside a string, the surrounding text becomes
adjacent after cleanup. A valid cleaned-text match can therefore be absent from
the raw JSONL bytes.

# Decision

Add three non-interactive verbs: `list`, `show`, and `search`. Keep the no-argument
terminal UI as the default. JSON is the default command format, and
`--format text` is the terminal-safe alternative.

Define versioned wire data-transfer objects (DTOs) in `internal/cli`. Version 1
requires a version bump if a field changes meaning, scope, unit, required status,
nullability, or closed enum. Consumers ignore unknown fields, so an optional
addition keeps the version.

Use canonical refs in every response. A top-level ref is `<agent>:<root-id>`. A
descendant ref is `<agent>:<root-id>#<subagent-path>`. Bare IDs, prefixes, thread
IDs, and file paths remain input-only selectors.

Normalize token categories into disjoint `uncached_input`, `output`,
`cache_write`, and `cache_read` values. Their sum is `total`. agtlog removes Codex
cache reads from raw input before emitting `uncached_input`.

Search the normalized fields from parsed timelines with substring or regular
expression matching. Summary filters reject unrelated candidates before their
timelines open. Workers parse candidates in parallel, while a coordinator commits
hits in root `updated_at` descending, canonical-ref, event, and field order. Each
worker releases decoded event timelines after it extracts the bounded hit data.

Do not use raw bytes to reject a search candidate. The generated 1.3 GB benchmark
shows that an exact corpus scan meets the five-second budget, so version 1 does
not need a cleaned-text index.

# Consequences

- Coding agents can use a bounded find-then-read loop. A search hit's canonical
  ref and event index address `show` directly.
- The public schema cannot drift with `internal/model` refactors.
- List totals remain additive across top-level rows because the earliest-started
  session owns a replayed request; equal starts use the smallest root ID.
- Subcommands use cached and embedded prices unless the caller requests a
  synchronous refresh.
- Search opens every surviving candidate graph. Filters remain the primary
  performance control.

# Impact

The command entry point dispatches only when the first argument is `list`, `show`,
or `search`. Existing terminal UI flags and its `StaticView` fallback when either
stdin or stdout is non-terminal retain their behavior.

Discovery returns structured per-path diagnostics. The terminal UI ignores
them. Broad command operations convert them to warning objects. An explicit
unreadable `show` selector or `search --session` selector returns an error, as does
a scoped search whose known descendant is unreadable.

JSON response fields are required unless the CLI reference marks them optional.
Text output sanitizes untrusted control characters.

# Alternatives

- **Add `--json` to the terminal UI.** Rejected because a screen snapshot lacks
  verbs and selectors. It also lacks stable event indices and bounded paging.
- **Expose `internal/model` through JSON.** Rejected because parser refactors can
  change the public contract and raw input tokens are not comparable across
  agents.
- **Reject search candidates with a raw-byte prefilter.** Rejected because cleanup
  creates text adjacency that does not exist in the source bytes. JSON escaping
  and Unicode folding add more false-negative cases.
- **Build an MCP server first.** Rejected because the CLI is the smaller local
  composition surface. An MCP server can use it later if agent clients need one.
- **Add a corpus-cost verb, follow mode, or field projection.** Rejected from schema
  version 1. Existing JSON tools cover projection. A new verb or mode needs
  separate evidence.

# Notes

The CLI contract lives in `docs/cli.md`. The benchmark in
`internal/cli/corpus_benchmark_test.go` generates 1,600 top-level sessions across
both agents, 1,300,000,000 bytes of JSONL, varied event counts, and one Claude
subagent file. Its `absent-sentinel` search is a zero-hit worst case. Reproducing
the benchmark writes about 1.3 GB to a temporary directory. Run it with:

```bash
go test -run '^$' -bench '^BenchmarkMachineReadableCLILatency$' \
  -benchtime=1x -count=1 ./internal/cli
```

On an Apple M4 Max, the maximum observed times were 0.083 seconds for `list` and
0.073 seconds for `show`. They were 0.165 seconds for a scoped search and 0.890
seconds for a corpus-wide search. The acceptance budgets are 1.0, 1.5, 2.0, and
5.0 seconds, respectively. The corpus contains fictional content.
