# Interface design

agtlog is a resource browser for coding-agent sessions. The home screen answers “which sessions
cost what?” and the detail screen answers “what happened, and where did the cost go?” Information
appears rolled up first and expands in place on demand.

## Screen structure

The list fills the terminal with three stacked regions:

1. A rounded context panel titled `agtlog`. Its one-line summary contains visible session and
   project counts, visible rolled-up cost, existing watch-root count, and active filter, sort,
   agent, or refresh state. During filter editing, a second inner line shows the live `/query▊`
   input and scrolls horizontally to keep the editing cursor visible.
2. A rounded `Sessions` panel. It contains the column header and the scrollable row window and
   consumes all height left between the context panel and key bar. The title becomes
   `Sessions · filtered` when a text or agent filter is active. The bottom border shows the
   selected position when rows overflow.
3. An unbordered one-line key bar:

   ```text
   / filter   s sort   a agent   ↵ open   r refresh   t theme   ? help   q quit
   ```

The detail screen uses the same structure. A rounded `Session` panel contains three metadata
lines, a rounded `Timeline` panel consumes the remaining height, and an unbordered detail key bar
sits at the bottom. The help view is a full-width rounded panel. Every view recomputes its panel,
viewport, and column geometry after a terminal resize.

When the terminal is too short to hold complete stacked regions, list and detail switch to one
rounded compact panel (plus a key line when it fits). This preserves both borders and the
highest-value summary instead of slicing a pre-rendered panel.

## Themes and emphasis

Color is semantic. Theme roles cover borders, panel titles, table headers, normal and selected
rows, each agent, warnings, muted metadata, estimates, accents, and key hints.

| Theme | Claude | Codex | Accent | Warning | Base |
| --- | --- | --- | --- | --- | --- |
| `default` | `#D19A66` | `#61AFEF` | `#61AFEF` | `#E06C75` | `#1E222A` |
| `nord` | `#88C0D0` | `#81A1C1` | `#88C0D0` | `#BF616A` | `#2E3440` |
| `dracula` | `#BD93F9` | `#8BE9FD` | `#50FA7B` | `#FF5555` | `#282A36` |

`--theme` takes precedence over `AGTLOG_THEME`; otherwise the default palette is used. `t` cycles
`default` → `nord` → `dracula` at runtime. `NO_COLOR` forces the implicit `mono` theme regardless
of those selections. Mono identifies itself in the context summary, hides the inactive `t`
binding, and makes `t` a no-op. Mono retains bold, faint, reverse-video, layout, text markers,
and glyph meaning. A terminal that exposes only an ASCII color profile also uses semantic
bold/faint/reverse styles so selection does not depend on unavailable colors.

The selected list row is one full-width highlight bar with a single foreground color. Unselected
rows use the agent color in `AGENT`, muted `AGE` and `MSGS`, and the estimated style for `~$`.

## Session columns

The list interleaves all agents and shows one row per top-level session. Subagents do not become
separate rows; their tokens and cost are included recursively in the parent.

| Column | Width and alignment | Content |
| --- | --- | --- |
| `AGENT` | 6, left | `claude` or `codex`, with `!` replacing the last cell when the session has an error |
| `PROJECT` | 8–18, left | Basename of the working directory |
| `TITLE` | 20 minimum, left | Agent title or first useful user prompt; absorbs remaining width |
| `MODEL` | 13, left | Costliest model, plus `+N` and missing-pricing `!` markers |
| `AGE` | 4, right | Relative time such as `5m`, `2h`, `4d`, or `1.2y` |
| `MSGS` | 5, right | Own message count |
| `TOKENS` | 9, right | Recursive total plus folded-subagent count, including `1.0B ⑃17` |
| `$` | 7, right | Recursive cost; Codex estimates use `~$` and partial totals use `!` |

Columns have one space between them. Header and data rows use identical widths and alignment.
Slack grows `TITLE` first, then `PROJECT` up to 18 columns, then returns to `TITLE`. When the
minimum set does not fit, columns disappear in this order: `MODEL`, `MSGS`, `PROJECT`, then `AGE`.
At ordinary narrow widths the retained core is `AGENT TITLE TOKENS $`; at smaller physical widths
the title shrinks and the lowest-value numeric fields eventually disappear. Rows never wrap or
cross the panel border.

## Width and glyph safety

Width-sensitive rendering has one invariant: build plain text, truncate it with
`ansi.Truncate`, pad it from `ansi.StringWidth`, and only then apply a lipgloss style. Styled text
is never passed back through cell width, truncation, or padding logic. A selected row is assembled
and padded to the session panel's inner width before its selection style is applied once.

All interface glyphs are tested as one display column, including the rounded border characters.
If the active terminal width rules do not report every rounded-border glyph as one cell, panels
fall back to `+`, `-`, and `|` rather than risking drift.
The warning marker is ASCII `!`; `⚠` was rejected because emoji-capable terminals may render it as
two columns. `⑃` remains the folded-subagent marker because it measures and renders as one column
in the supported terminal path.

| Glyph | Meaning |
| --- | --- |
| `⑃N` | N recursive subagents are folded into a row |
| `~$` | API-equivalent estimate; a subscription user normally pays less |
| `!` | Session error or missing pricing, depending on the cell |
| `▸` / `▾` | Collapsed / expanded item |
| `▸ you:` | User prompt |
| `●` | Assistant turn summary |
| `⚙` | Tool call and linked result |
| `◇` | Thinking, compaction, or secondary system event |

## Session detail

The header panel uses three lines:

1. agent, project, and full working directory;
2. all models, Git branch, and start-to-update time;
3. recursive tokens and cost, followed by own and subagent splits.

The timeline is chronological and collapsed by default:

```text
▸ you: Investigate the failing watcher
▸ ● claude: The race is fixed · 2 thinking · 4 tools · 1 subagents
  ⚙ Edit(watch.go) → updated · 0.4s
  ⑃ Task(inspect watcher) ▸ opus-4.8 · 420k · $1.02
```

Expanding an assistant turn reveals thinking summaries, tool calls and linked results, compaction
events, and subagent spawns. Expanding a subagent inserts its timeline at the next indentation
level. The same rule applies recursively. The viewport receives width-bounded plain lines; color
is applied only after the visible line window has been selected.

## Key budget

Bindings stay single-key and follow terminal and Vim conventions:

| Screen | Keys | Action |
| --- | --- | --- |
| List | `j`/`k`, `↑`/`↓` | Move and keep the selection in the row window |
| List | `pgup`/`pgdn`, `home`/`end` | Move by a page or jump to an edge |
| List | `/` | Fuzzy-filter agent, project, and title |
| List | `s` | Cycle age, tokens, and cost sort |
| List | `a` | Cycle all, Claude, and Codex |
| List | `enter` | Open detail |
| List | `r` | Rediscover sessions |
| Detail | `j`/`k`, `↑`/`↓` | Move and scroll |
| Detail | `space`/`enter` | Expand or collapse |
| Detail | `J`/`K` | Next or previous subagent |
| Detail | `esc`/`h` | Return to the list |
| Both | `t` | Cycle color themes; no-op in mono |
| Both | `?` | Toggle full help |
| Both | `q`/`ctrl-c` | Quit |

## Terseness rules

1. Each screen answers one question without opening another view.
2. Turns and subagents start collapsed and expand with one key.
3. Lists use human-scale numbers and relative time, not raw counters or timestamps.
4. Parent rows show recursive totals; detail shows the breakdown.
5. Hard noise such as permission preambles and synthetic reminders does not enter the timeline.
6. Headers, rows, and key bars truncate instead of wrapping.
