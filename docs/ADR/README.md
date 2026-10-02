# Architecture Decision Records (ADR)

Each ADR records one design decision in agtlog, the context that required it, and the
alternatives that we rejected.

## Creating an ADR

1. Copy `_template.md` to `YYYYMMDD-<title>.md`. The date matches the front-matter date.
2. Write `# Context`, `# Decision`, and `# Alternatives`. Every ADR has these sections.
3. Add `# Consequences`, `# Impact`, or `# Notes` only if the section states something that no
   other section states.
4. Name the actor of each decision in the first person plural: "We rejected X because Y", not
   "X was rejected".
5. Add a row to the index below.

## Status

- **proposed**: under discussion.
- **accepted**: binding.
- **superseded**: replaced by a newer ADR. Prefix the old filename with `_`, and link the two
  records to each other. Besides superseded records, only `_template.md` carries the prefix.

A record that a later record replaces only in part stays `accepted`. At the replaced passage, it
links to the later record, and the later record names what it replaces.

## Index

| Date | ADR | Summary |
| --- | --- | --- |
| 2026-07-19 | [Coding-agent tool landscape](./20260719-tool-landscape.md) | Build a read-only Go TUI because no surveyed tool combines a terminal UI, transcript browsing, recursive subagent cost, and both agents |
| 2026-07-19 | [Architecture overview](./20260719-architecture-overview.md) | Normalize each agent's logs into one recursive session model and one pipeline |
| 2026-07-19 | [Cost model](./20260719-cost-model.md) | Price usage with ccusage-compatible API rates and mark estimated or unpriced totals |
| 2026-07-19 | [Live-follow watching](./20260719-live-follow-watching.md) | Combine recursive fsnotify watches with a stat-based recovery scan |
| 2026-07-19 | [TUI stack](./20260719-tui-stack.md) | Build a testable keyboard-first UI on Bubble Tea and Bubbles |
| 2026-07-22 | [Table sorting](./20260722-table-sorting.md) | Keep the column focus separate from the sort state |
| 2026-07-23 | [Bulk fold default](./20260723-bulk-fold-default.md) | Apply expand-all and collapse-all to current and later timeline rows |
| 2026-07-23 | [Harness turn labels](./20260723-harness-turn-labels.md) | Label harness-injected user turns without a new event kind |
| 2026-07-24 | [Advisor tool cost](./20260724-advisor-tool-cost.md) | Price and show each advisor iteration separately |
| 2026-07-24 | [Cross-session cost deduplication](./20260724-cross-session-cost-dedup.md) | Assign each replayed Claude request to its earliest session |
| 2026-07-25 | [Full-fidelity item view](./_20260725-full-fidelity-item-view.md) | Keep complete event fields and read raw records by reference. Superseded by always-present raw item records |
| 2026-07-26 | [Codex timeline usage](./20260726-codex-timeline-usage.md) | Decode `summary` only in `reasoning` records, keep the model file-wide, and count reasoning tokens within output |
| 2026-07-26 | [Codex usage ledger](./20260726-codex-usage-ledger.md) | Match timeline usage to a billed-request ledger recorded at parse time |
| 2026-07-26 | [Always-present raw item records](./20260726-always-present-raw-item-records.md) | Load the raw record when an item opens and show valid JSON as indented, terminal-safe lines |
| 2026-08-05 | [Machine-readable CLI](./20260805-machine-readable-cli.md) | Add versioned `list`, `show`, and `search` commands with stable refs |
| 2026-08-05 | [Workflow subagent groups](./20260805-workflow-subagent-groups.md) | Group each Workflow run under one node and title siblings from their first unique prompt line |
| 2026-08-15 | [Summary cache repricing](./20260815-summary-cache-repricing.md) | Cache usage without prices and reprice each cache hit |
| 2026-08-15 | [Codex sidecar linking](./20260815-codex-sidecar-linking.md) | Link Codex sidecars by their parent IDs and decode wrapped message items |
| 2026-08-22 | [Follow session index](./20260822-follow-session-index.md) | Build each live snapshot from a path-indexed copy of parser output |
| 2026-08-22 | [Codex summary checkpoints](./20260822-codex-summary-checkpoints.md) | Resume Codex summaries after the last validated complete line |
| 2026-08-23 | [Extra agent home directories](./20260823-extra-agent-home-directories.md) | Read extra agent homes without changing where Claude Code or Codex write |
| 2026-09-05 | [Codex counter segments](./20260905-codex-counter-segments.md) | Reconcile each cumulative-counter segment separately and show unattributed usage |
| 2026-10-01 | [Recursive turn counts](./20261001-recursive-turn-counts.md) | Count messages and tool calls across the session tree and keep `messages` per node |
| 2026-10-01 | [Subagent age order](./20261001-subagent-age-order.md) | Order unsorted Subagents rows by newest update within each parent, like Sessions |
| 2026-10-01 | [Overview tab](./20261001-overview-tab.md) | Combine own and delegated activity, model costs, and the Subagents table in one tab |
| 2026-10-02 | [Collapsed Timeline start](./20261002-collapsed-timeline-start.md) | Start Timeline rows collapsed so readers can scan events before opening bodies |
| 2026-10-02 | [Detail line cursor](./20261002-detail-line-cursor.md) | Move through text lines on both detail tabs, with event actions on Timeline and row-only subagent activation on Overview |
