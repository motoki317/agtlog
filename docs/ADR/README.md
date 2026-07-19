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
