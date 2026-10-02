# Agent guide

agtlog reads local Claude Code and Codex JSONL logs, normalizes sessions, and estimates
API-equivalent cost. It never writes to agent configuration or log directories. The
keyboard-first terminal UI lists top-level sessions and loads a detail timeline with nested
subagents when the user opens a session. The `list`, `show`, and `search` subcommands give
scripts the same sessions as JSON.

## Repository map

- `internal/model` contains the unified session and usage model.
- `internal/cost` embeds LiteLLM pricing, overlays the XDG pricing cache, refreshes stale data
  for the next launch, and calculates per-record cost.
- `internal/source` discovers sessions, caches their summaries, and follows filesystem changes.
- `internal/source/claude` and `internal/source/codex` are the adapters that parse each agent's
  logs. If a change alters what an adapter stores in a session summary, bump the adapter's
  `CacheFingerprint` so that agtlog parses cached sessions again.
- `internal/source/jsonl` reads size-bounded JSONL lines with their byte offsets and decodes
  records.
- `internal/cli` implements the subcommands. `docs/cli.md` is their versioned JSON contract.
- `internal/tui` contains the session list, detail screens, key map, help, and styles.
  `docs/design.md` records the rules that a new screen, column, or key must follow.
- `internal/leakcheck` guards committable files against local identifiers.
- `docs/plans` is gitignored planning scratch.

## Build and test

Run `just` to list the recipes. Every commit must pass `just pre-commit`: the build, `gofmt`,
`go vet`, the leak check, and `go test ./...`. The Nix dev shell installs a Git hook that runs
it. If a commit changes `go.mod`, `go.sum`, `flake.nix`, or `flake.lock`, the hook also runs
`just nix-build`. If that build reports a hash mismatch after a dependency change, update
`vendorHash` in `flake.nix`. CI runs `just pre-commit` and `just test-race`.

Builds set `CGO_ENABLED=0`, so a dependency must not need cgo. Only `just test-race` enables cgo,
because the race detector needs it.

## Conventions

- Use Conventional Commits in English.
- Runtime writes stay beneath the XDG cache directory. Tests create fixtures in temporary or
  `testdata` directories.
- Never commit machine-local paths, hostnames, account IDs, local project names, or other
  environment identifiers. Use fictional values in tests and examples.
- The leak check finds local paths, hostnames, and project names only when the gitignored
  `.leakcheck` file lists them. Copy `.leakcheck.example` to `.leakcheck` and add yours.
- Comments explain a constraint or a rejected alternative at that spot in the code, not what the
  code does. Record a decision that shapes more than one place in a dated ADR.
  `docs/ADR/README.md` describes the format.
- Use TDD for pure logic and fixture-driven tests for adapters.
