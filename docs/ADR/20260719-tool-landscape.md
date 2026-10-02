---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Before we wrote agtlog's code, we needed evidence for one question: does an existing tool already
cover agtlog's target? The target is a keyboard-first terminal UI that browses coding-agent session
transcripts for Claude Code and Codex, extensible to other agents. It estimates API cost per session
and rolls each session's cost up recursively through its subagents.

On 2026-07-19 we checked about 25 tools against their GitHub metadata and their README or source.
The table in Decision lists 20 of them. The tools fell into five niches: cost CLIs, cost dashboards
and monitors, transcript viewers, session launchers, and menubar, statusline, and web tools. Each
niche covered a different part of agtlog's target.

The survey measured each tool against the on-disk logs that agtlog must read. Claude Code writes
per-session JSONL under `~/.claude/projects/`, including subagent (Task) sidechains that belong to a
parent session. Codex writes `~/.codex/sessions/YYYY/MM/DD/rollout-<id>.jsonl` with the full event
stream and token counters. Both are local and append-only, and a tool can read them without a proxy
or an API key.

# Decision

We build agtlog as a Go TUI over the on-disk logs. We reuse the field's pricing data, not an
existing end-to-end tool. No surveyed tool combines the four properties: a terminal TUI, transcript
browsing, cost with recursive subagent rollup, and one view of Claude Code and Codex. Each neighbor
has three of the four at most:

| Tool | Niche and form | Why it does not fit |
| --- | --- | --- |
| `ccusage` | Cost CLI (Rust) | Reports cost for 15+ agents from LiteLLM pricing, but it generates reports. It has no interactive transcript browser and no subagent rollup, and `blocks --live` is a monitor, not a navigable list. |
| `codeburn` | Cost dashboard (TypeScript: TUI, web, desktop, menubar), 36 agents, LiteLLM pricing | Has multiple agents, cost, and a TUI, but it aggregates spend by task, model, and project. It never renders the conversation and has no per-session recursive subagent rollup. |
| `Claude-Code-Usage-Monitor` | Live cost monitor (Python, Rich TUI) | Forecasts burn rate against plan limits, but it is a live monitor, not a historical browser. Claude only, no subagents. |
| `claude-devtools` | Claude transcript viewer (Electron, Docker web) | Already renders recursive subagent cost trees ("nested agents render recursively … tokens, duration, cost"). It is a GUI or web app, supports only Claude, and drills into one session instead of listing sessions across agents. |
| `claude-code-trace` | Claude log viewer (Rust: desktop, web, TUI) | Closest in form: a terminal JSONL browser that expands subagents and tails live. It has no cost engine (token counts only), ships Codex support as the separate `codex-trace` binary, and its README calls the TUI "functional but … rough." |
| `borball/claude-session-manager-tui`, `claudatui` | Claude log browsers (Go TUI, Rust TUI) | Right two-pane terminal form, but no cost, no subagents, and Claude only. |
| `opcode` | Claude GUI toolkit (Tauri) | Has a usage-analytics dashboard, but it is a desktop GUI, supports only Claude, and puts session management first. |
| `claude-trace-replay`, `sniffly`, `claude-code-log` (HTML export) | Claude transcript viewers | Claude only. |
| `codex-history-viewer`, `codex-trace` | Codex transcript viewers | Codex only. |
| `Claude Squad`, `ccmanager` | Session launchers (Go TUI, Ink TUI) | Multi-agent, but they run live agents across git worktrees. That is orchestration, not log browsing or cost. |
| `ccseva`, `toki-monitor`, `ccstatusline`, `viberank`, `ccflare` | Menubar, statusline, and web tools | Not terminal transcript browsers. `toki-monitor` covers Claude and Codex. `ccflare` is a proxy. |

Cost estimation is a solved, reusable component. `ccusage` and `codeburn` both price the same
on-disk token counts from LiteLLM's model price table, which LiteLLM refreshes daily. agtlog uses
that table instead of per-model rates that we maintain by hand. Our new code covers three parts
that no tool serves: a unified multi-agent session model, a k9s-style TUI, and a recursive subagent
cost rollup per session row.

# Consequences

agtlog is a Go project. We do not reopen the language choice per feature.

Like `ccusage` and `codeburn`, agtlog uses a small parser adapter per agent. Each adapter
normalizes on-disk JSONL into a common session, turn, and subagent model, so a new agent is a new
adapter, not a core change.

Pricing is data, not code. The LiteLLM price table refreshes outside agtlog releases. Prices lag the
refresh cadence of the table. We accept that lag for an estimate.

agtlog fixes only ratios to a model's input rate. Cache writes cost 1.25 times and cache reads 0.1
times the input rate when the table omits those rates. A 1-hour cache write always costs 2 times the
input rate, tiered rates included. [Cost model](./20260719-cost-model.md) records the formula.

agtlog only reads files that are already on disk. It uses no proxy (unlike `ccflare`), no API key,
and no upload. This is a security property, not a simplification to revisit.

The recursive rollup is required scope. agtlog attributes Claude Code subagents (Task sidechains)
and Codex child sessions to their parent session. It sums them recursively into the parent's cost
column. [Codex sidecar linking](./20260815-codex-sidecar-linking.md) records how a Codex child finds
its parent.

# Impact

agtlog is a browse and cost TUI. These niches are out of scope:

- Session launchers and orchestrators. Claude Squad and ccmanager run agents across worktrees.
- A live burn-rate monitor as the primary mode. Claude-Code-Usage-Monitor and claudectl own the
  "when do I hit my limit" forecast. agtlog can have a live tail, but the product is a browser.
- A proxy (ccflare) or a team observability stack (claude-code-otel with Grafana).
- A GUI, Electron, or web app (claude-devtools, opcode, sniffly). agtlog is a keyboard-first
  terminal tool by decision.

We reopen build versus buy only on new evidence: a specific competitor that ships the exact
combination, not a new preference.

# Alternatives

Each row of the table is a rejected alternative. For two tools, we weighed a contribution against
a new build.

We considered a contribution of a transcript-browser mode to `codeburn` and rejected it. codeburn
is a cost-analytics product (optimize, guard, yield), and a k9s-style transcript browser does not
fit that mission. We re-check codeburn if it ships transcript browsing.

We considered a contribution of cost and Codex unification to `claude-code-trace` and rejected it.
That work is most of agtlog's purpose, grafted onto a codebase that splits Claude and Codex into two
binaries and calls its own TUI rough. We re-check claude-code-trace if it adds a cost engine and
merges Codex.

# Notes

k9s and lazygit define the target interaction: a resource list, a `/` filter, drill-in,
single-key navigation, and a persistent help footer. agtlog applies that interaction model to
agent session logs.
