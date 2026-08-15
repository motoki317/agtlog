# Architecture Decision Records (ADR)

ADRs capture significant architectural decisions behind agtlog, including the
trade-offs and alternatives that led to each choice.

## Creating an ADR

1. Copy `_template.md`.
2. Name it `YYYYMMDD-<title>.md`, matching the front-matter date.
3. Fill every section: Context, Decision, Consequences, Impact, Alternatives,
   and Notes.

## Status

- **proposed**: under discussion.
- **accepted**: binding.
- **superseded**: replaced by a newer ADR; prefix the old filename with `_`
  and cross-link both records.

## Index

| Date | ADR | Summary |
| --- | --- | --- |
| 2026-07-19 | [Coding-agent tool landscape](./20260719-tool-landscape.md) | Build a read-only Go TUI that unifies transcript browsing, cost, and recursive subagent rollup |
| 2026-07-19 | [Architecture overview](./20260719-architecture-overview.md) | Normalize different log shapes into one recursive session pipeline |
| 2026-07-19 | [Cost model](./20260719-cost-model.md) | Estimate usage with ccusage-compatible API rates and explicit uncertainty |
| 2026-07-19 | [Live-follow watching](./20260719-live-follow-watching.md) | Combine recursive fsnotify watches with a stat-based recovery scan |
| 2026-07-19 | [TUI stack](./20260719-tui-stack.md) | Use Bubble Tea and Bubbles for a testable keyboard-first terminal UI |
| 2026-07-22 | [Table sorting](./20260722-table-sorting.md) | Keep visible column focus separate from stable sort state |
| 2026-07-23 | [Bulk fold default](./20260723-bulk-fold-default.md) | Apply bulk expansion state to current and later timeline rows |
| 2026-07-23 | [Harness turn labels](./20260723-harness-turn-labels.md) | Distinguish harness-injected user turns without changing event kinds |
| 2026-07-24 | [Advisor tool cost](./20260724-advisor-tool-cost.md) | Price and render each advisor iteration independently |
| 2026-07-24 | [Cross-session cost deduplication](./20260724-cross-session-cost-dedup.md) | Assign replayed Claude requests to one deterministic owner |
| 2026-07-25 | [Full-fidelity item view](./_2026-07-25-full-fidelity-item-view.md) | Superseded by always-present raw item records |
| 2026-07-26 | [Codex timeline usage](./20260726-codex-timeline-usage.md) | Preserve polymorphic model context and treat reasoning as output detail |
| 2026-07-26 | [Codex usage ledger](./20260726-codex-usage-ledger.md) | Reconcile request usage against a parser snapshot and explicit aggregates |
| 2026-07-26 | [Always-present raw item records](./20260726-always-present-raw-item-records.md) | Load raw records on item open and present valid JSON as terminal-safe indented lines |
| 2026-08-05 | [Machine-readable CLI](./20260805-machine-readable-cli.md) | Add versioned list, show, and deterministic search commands with stable refs |
| 2026-08-15 | [Summary cache repricing](./20260815-summary-cache-repricing.md) | Cache billed usage without prices and apply current pricing on every cache hit |
| 2026-08-15 | [Codex sidecar linking](./20260815-codex-sidecar-linking.md) | Link Codex sidecars from child parent IDs and decode wrapped message items |
