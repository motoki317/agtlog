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
| 2026-07-26 | [Codex timeline usage](./20260726-codex-timeline-usage.md) | Preserve polymorphic model context and treat reasoning as output detail |
