---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Before writing agtlog's code, one question had to be answered with evidence: does a tool
already cover agtlog's target — a **keyboard-first terminal UI** that **browses coding-agent
session transcripts** (Claude Code + Codex, extensible), **estimates API cost per session**,
and **rolls each session's cost up recursively through its subagents**? If one did, agtlog
should not exist.

A 2026-07 landscape survey (GitHub metadata + README-level fact checks against ~25 tools, web
discovery) found a crowded field around Claude Code usage, but **no tool at the intersection**
agtlog targets. The tools cluster into five niches, each owning a different slice:

- **Cost CLIs** — `ccusage` (Rust, 17.3k*) reports cost across 15+ agents via LiteLLM pricing,
  but is a report generator, not an interactive browser (its `blocks --live` is a monitor, not
  a navigable list).
- **Cost dashboards / monitors** — `codeburn` (TS, 8.7k*, TUI+web+desktop+menubar, 36 agents,
  LiteLLM) and `Claude-Code-Usage-Monitor` (Python, 8.5k*, Rich live TUI, burn-rate forecast).
  These **aggregate** spend by task/model/project; they never render the conversation.
- **Transcript viewers** — all single-agent, Claude-only: `claude-devtools` (Electron/web),
  `claude-code-trace` (Rust; desktop/web/TUI), `claude-trace-replay`, `sniffly`, `opcode`,
  `borball/claude-session-manager-tui` (Go TUI), `claudatui` (Rust TUI), `claude-code-log`
  (HTML export). Codex has its own separate viewers (`codex-history-viewer`, `codex-trace`).
- **Session launchers** — `Claude Squad` (Go, 8.1k*), `ccmanager` (TS, 1.2k*): multi-agent, but
  they *run* live agents across worktrees; they do not browse past logs or estimate cost.
- **Menubar / statusline / web** — `ccseva`, `toki-monitor` (Claude+Codex), `ccstatusline`,
  `viberank`, `ccflare`.

The **reference target shape** the survey measured against is the on-disk log format agtlog must
read: Claude Code writes per-session JSONL under `~/.claude/projects/`, including **subagent
(Task) sidechains** that belong to a parent session; Codex writes
`~/.codex/sessions/YYYY/MM/DD/rollout-<id>.jsonl` with the full event stream and token counters.
Both are local, append-only, and readable without a proxy or API key — the field norm is
**local-first, read-only over files already on disk**, and agtlog follows it.

# Decision

**Build agtlog as a Go TUI over the on-disk logs; reuse the industry's pricing data, not any
existing end-to-end tool.** No surveyed tool combines all four of agtlog's axes —
{terminal TUI} × {browses transcripts} × {cost incl. recursive subagent rollup} ×
{Claude + Codex unified}. Each neighbour owns three of four at most:

| Tool | What it is | Disqualifier (verified against README/source) |
|---|---|---|
| **ccusage** | Multi-agent cost CLI (Rust) | Cost across 15+ agents via LiteLLM, but a report generator — no interactive transcript browsing, no subagent rollup. The pricing approach to *reuse*, not the UI. |
| **codeburn** | Multi-agent cost dashboard (TUI+web+desktop+menubar) | Owns multi-agent + cost + TUI, but **aggregates** spend by task/model/project — never renders the conversation, and no per-session recursive subagent rollup. Different mission (optimize/guard/yield cost-analytics). |
| **claude-devtools** | Claude transcript viewer (Electron/Docker-web) | **Already does recursive subagent cost trees** ("nested agents render recursively … tokens, duration, cost") — so the mechanism is not novel — but it is **GUI/web and Claude-only**, and presents a per-session drill-in tree, not a browsable multi-agent session list. |
| **claude-code-trace** | Claude log viewer (Rust; desktop/web/TUI) | Closest by *form*: terminal, browses JSONL, expands subagents, live-tails. But **no cost engine** (token counts only), **Codex is a separate binary** (`codex-trace`), and its own README calls the TUI "functional but … rough." |
| **borball/claude-session-manager-tui**, **claudatui** | Claude log browsers (Go / Rust TUI) | Right form (two-pane terminal browse) but **no cost, no subagents, Claude-only**; borball is 0★, last push 2026-04 (effectively a prototype). |
| **Claude-Code-Usage-Monitor** | Live cost monitor (Python Rich TUI) | Burn-rate forecast + plan limits, but a **live monitor**, not a historical browser; Claude-only; no subagents. |
| **Claude Squad**, **ccmanager** | Multi-agent session launchers (Go / Ink TUI) | Multi-agent, but they **run** live agents across git worktrees — a different niche (orchestration), not log browsing or cost. |
| **opcode** | Claude GUI toolkit (Tauri) | Rich usage-analytics dashboard, but a **desktop GUI**, Claude-only, and session-management-first; last push 2025-10 (stalling). |

**The decisive enabler**: cost estimation is a solved, reusable component. `ccusage` and
`codeburn` both price the same on-disk token counts from **LiteLLM's model price table**,
refreshed daily. agtlog consumes that table rather than hand-rolling per-model rates. The novel
code is bounded to what nobody serves: a **unified multi-agent session model**, the **k9s-style
TUI**, and the **recursive subagent cost rollup per session row**.

# Consequences

- **agtlog is a Go project** — matching the Go TUI precedent in this space (borball,
  Claude Squad) and the Bubble Tea/Lip Gloss ecosystem for k9s/lazygit-style UIs. Settled, not
  re-opened per feature.
- **Per-agent adapters, like ccusage and codeburn** — a small parser interface per agent
  (Claude Code, Codex) that normalises on-disk JSONL into a common session/turn/subagent model.
  New agents are adapters, not core changes.
- **Pricing is data, not code** — the LiteLLM price table is a dependency refreshed out-of-band;
  rates are never hardcoded. A cut corner with a ceiling: pricing lags the table's refresh
  cadence; acceptable for an estimate, upgradeable by pinning a table version if audits need it.
- **Read-only, local-first** — agtlog only reads files already on disk; no proxy (unlike
  ccflare), no API key, no upload. This is the field norm and a security property, not a
  simplification to revisit.
- **Recursive rollup is the load-bearing feature** — Claude Code subagents (Task sidechains)
  must be attributed to their parent session and summed recursively into the parent's cost
  column. This is the one capability no terminal/multi-agent tool provides and the reason
  agtlog exists; it is not optional scope.

# Impact

agtlog is scoped as a **browse + cost TUI**, explicitly NOT:

- a **session launcher / orchestrator** (Claude Squad, ccmanager own running agents across
  worktrees);
- a **live burn-rate monitor** as its primary mode (Claude-Code-Usage-Monitor, claudectl own
  the "when do I hit my limit" forecast) — a live tail may exist, but the product is a browser;
- a **proxy** (ccflare) or a **team observability stack** (claude-code-otel/Grafana);
- a **GUI/Electron/web app** (claude-devtools, opcode, sniffly) — agtlog is keyboard-first
  terminal by decision.

Requests pulling toward those niches are out of scope. The survey is point-in-time:
**re-litigate build-vs-buy only with new evidence** — a specific competitor shipping the exact
combination, not a fresh preference.

# Alternatives

Every row of the disqualifier table is a rejected alternative. The two closest calls, where
"contribute instead of build" was genuinely weighed:

- **codeburn** — very active (pushed daily, 8.7k★, sponsored, 36-adapter model). It already owns
  multi-agent + cost. Contributing a transcript-browser mode was considered and rejected: codeburn
  is a **cost-analytics** product (optimize/guard/yield), and a k9s transcript browser is a
  foreign body in that mission. Worth re-checking if it ships transcript browsing.
- **claude-code-trace** — closest by form (Rust terminal log viewer, subagent expand, live tail).
  Contributing cost + Codex unification was considered; rejected because that is essentially
  agtlog's entire thesis grafted onto a codebase that split Claude and Codex into two binaries
  and self-describes its TUI as rough. Worth re-checking if it adds a cost engine and merges Codex.

# Notes

- **Prior art on the mechanism**: recursive subagent cost trees already exist in
  `claude-devtools` (Electron, Claude-only). agtlog's novelty is not the recursion but its
  delivery — recursion in a **terminal**, as a **session-list column**, **across agents**.
- **Naming / collision check**: no functional collision found among surveyed tools; "agtlog"
  did not match an existing agent-log tool in the 2026-07 survey.
- **UX north star, not competitors**: k9s and lazygit define the target interaction —
  resource-list + `/` filter + drill-in, single-key navigation, persistent help footer. agtlog
  applies that grammar to agent session logs; they are inspiration, not alternatives.
- This ADR records a point-in-time survey (2026-07-19). Star counts and activity dates are
  signals captured that day, not standing facts.
