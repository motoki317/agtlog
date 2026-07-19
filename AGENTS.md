# Agent guide

agtlog reads local Claude Code and Codex JSONL logs, normalizes sessions, and
estimates API-equivalent cost. It never writes to agent configuration or log
directories. The keyboard-first terminal UI lists top-level sessions and opens
lazy detail timelines with nested subagents.

## Repository map

- `cmd/agtlog` contains the executable entry point.
- `internal/model` contains the unified session and usage model.
- `internal/cost` embeds LiteLLM pricing, overlays the XDG pricing cache, refreshes
  stale data for the next launch, and calculates per-record cost.
- `internal/source/claude` and `internal/source/codex` parse agent-specific logs.
- `internal/source` discovers sessions and follows filesystem changes.
- `internal/tui` contains the list, detail timeline, key map, help, and styles.
- `internal/leakcheck` guards committable files against local identifiers.
- `docs/ADR` records durable design decisions.
- `docs/design.md` records the terminal interface vocabulary.
- `docs/plans` is gitignored planning scratch.

## Build and test

```bash
just build
just test
just check
just leakcheck
just pre-commit
just test-race
```

The module targets Go 1.26.2 and all release builds set `CGO_ENABLED=0`.

## Conventions

- Use Conventional Commits in English and keep each commit green.
- Keep agent logs read-only. Tests create fictional fixtures in temporary or
  `testdata` directories.
- Runtime writes stay beneath the XDG cache directory. `--offline` disables the
  pricing fetch but still uses the embedded snapshot and a valid cached overlay.
- Never commit machine-local paths, hostnames, account IDs, project names, or
  other environment identifiers. Use fictional values in tests and examples.
- Put private local terms in the gitignored `.leakcheck` denylist and run
  `just leakcheck` before every commit.
- Comments explain constraints or rejected alternatives, not nearby code.
- Use TDD for pure logic and fixture-driven tests for adapters.
- Record design rationale in dated ADRs instead of code comments.
