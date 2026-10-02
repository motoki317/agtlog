# Interface design

agtlog is a resource browser for coding-agent sessions. The home screen answers “which sessions
cost what?” and the detail screen answers “what happened, and where did the cost go?”

This document records the rules that a new screen, column, or key must follow. The code owns the
exact values:

- Theme colors are in `internal/tui/styles.go`.
- Shared bindings are in `internal/tui/keys.go`. Screen-specific keys are matched in the `Update`
  code of `app.go`, `detail.go`, and `item.go`, and each screen's key bar lists its hints in drop
  order. `internal/tui/help.go` lists every screen's keys.
- The golden files in `internal/tui/testdata/*.golden` hold mono frames at 80 to 120 columns.
  `go test ./internal/tui -update` regenerates them.

## Terseness rules

1. Each screen answers one question without opening another view.
2. When opened from the list, Timeline rows with a body start collapsed and unfold in place.
   `enter` opens the focused row as its own screen.
3. Lists use human-scale numbers and relative time, not raw counters or timestamps.
4. Parent rows show recursive totals. Detail shows the breakdown.
5. `internal/model` strips `system-reminder`, `permission-preamble`, and `local-command-caveat`
   blocks from timeline text. Other turns that the harness injects stay visible as `harness:` rows.
6. Panel titles, list rows, table rows, and key bars truncate. Timeline event rows, except
   compaction and system rows, stay one row and truncate, so their request metrics keep their
   column. Timeline bodies, compaction and system rows, Overview cost blocks and notes, and item
   view rows wrap by default and truncate while wrapping is off.

## Screen structure

Each screen fills the terminal with rounded panels. Every screen except help ends with one
unbordered key bar.

- The **session list** stacks a context panel titled `agtlog`, a `Sessions` panel, and the key
  bar. The context summary counts the visible sessions and projects, totals their cost, and gives
  the number of watched roots. It also names the active filter, sort, and agent filter, the last
  refresh or discovery result, and the mono theme. The `Sessions` title gains
  `· filtered` while a text or agent filter is active. During startup, the summary reports
  discovery instead of totals, and the row window shows the progress. After a discovery failure,
  the row window shows the error and the retry key.
- The **session detail** stacks a `Session` panel with three metadata lines, a tab panel with
  Timeline and Overview, and the key bar.
- The **item view** shows one event in a single panel above the key bar.
- The **help view** replaces the current screen with a full-terminal panel of that screen's keys.

A panel's bottom border shows the position, such as `3/40`, when its content overflows.

Each key bar drops hints in a fixed order as the width shrinks. On the list, `↵ open` drops last. On
detail and item views, `esc back` never drops. `?` opens the help view on every screen, and the
help view lists that screen's bindings.

If the terminal is too short for the stacked regions, list and detail switch to one compact panel,
plus a key line when it fits. The compact panel keeps both borders and the highest-value lines
instead of slicing a rendered panel. The list keeps the filter input, the summary, and one row. The
detail keeps the title line, the branch-and-cost line, and the agent line, in that order, and then
as many rows from the active tab as fit.

Every view recomputes its panel, viewport, and column geometry after a resize. This includes the
screens stored beneath a drilled child, so a new drill-down screen implements the `detailScreen`
interface in `internal/tui/app.go` and lives on `detailStack`.

## Color and emphasis

Color is semantic and never carries meaning alone. Glyphs, labels, brackets, and layout carry the
same meaning in mono. Ordinary user, assistant, and thinking prose never takes an agent color. In
the timeline and the tables, only the short agent or role label carries identity color. The agent
line of the `Session` panel takes the agent color as a whole.

Each timeline role has its own label color, so a reader tells who acted at a glance:

| Row | Label color |
| --- | --- |
| Agent reply (`claude:`, `codex:`) | The agent's color |
| Typed prompt (`you:`) | The key-hint color, bold |
| Tool call (`⚙`) | The header color, bold |
| Subagent, Workflow, or advisor (`⑃`) | The accent color |
| Harness turn (`harness:`) and thinking | Muted |

The `you:` and tool labels reuse the key-hint and header colors, so the theme needs no extra roles.

Secondary text uses the muted color, not the faint attribute, because terminals render faint
inconsistently. Typed prompts use the user-prompt background. Harness, compaction, system, and
usage rows use the system-prompt background. A selected row uses the selection colors instead of
either tint.

Diff rows keep their `+`, `-`, or space prefix in every theme. Color themes color additions and
removals with solid diff colors and mute context rows. Mono relies on the prefixes alone.

The mono theme applies under a non-empty `NO_COLOR` and on terminals with only an ASCII color
profile. Mono uses bold and reverse video and no color. It shows `theme:mono` in the summary, hides
the `t` hint, and ignores `t`.

The selected list row is one full-width bar with one foreground color. In unselected rows, `AGENT`
takes the agent color, `SUBS` the accent color, and `AGE` and `TURNS` the muted color. `COST` takes
the estimated style for `~$`, and `MODEL` takes the warning style when pricing is missing.

## Session list

The list interleaves all agents and shows one row per top-level session. Subagents do not become
separate rows. Their turns and cost are included recursively in the parent row.

| Column | Content | First `⇧O` press |
| --- | --- | --- |
| `AGENT` | `claude` or `codex` | A→Z |
| `PROJECT` | Basename of the working directory | A→Z |
| `TITLE` | Agent title or first useful user prompt | A→Z |
| `MODEL` | Costliest own model, `+N` other models, and the `!` marker | A→Z |
| `AGE` | Relative update time, such as `5m`, `2h`, `4d`, or `1.2y` | Oldest first |
| `TURNS` | User messages, agent messages, and tool calls, including all descendants | Largest first |
| `SUBS` | Descendant subagents, without [Workflow](ADR/20260805-workflow-subagent-groups.md) group nodes, or blank when none | Largest first |
| `COST` | Recursive owned cost, with the `~$` and `!` markers | Largest first |

Each further `⇧O` press reverses the order, and the third press clears the sort. A new count or
cost column starts largest first, and every other column starts in ascending order.

**Own** usage and cost exclude subagents. **Owned** cost excludes the requests that a resumed or
forked session replays from an earlier session, because
[cross-session cost dedup](ADR/20260724-cross-session-cost-dedup.md) gives each replayed request to
the earliest session.

`T` switches `AGE` to a wider `TIME` column with the absolute update time. The `AGE` sort follows
the raw update timestamp in both modes.

Columns have one space between them. A two-cell selection marker precedes each row. Header and data
rows share widths and alignment. Slack grows `TITLE` to its preferred width, then `PROJECT` to its
cap, then `TITLE` again. If the columns do not fit, they disappear in this order: `MODEL`, `TURNS`,
`SUBS`, `PROJECT`, `AGE`. The retained core is `AGENT TITLE COST`. On a narrower panel, `TITLE`
shrinks, and then `COST` and `AGENT` disappear. Rows never wrap or cross the panel border.

Header focus starts on `AGENT`, and the focused header cell uses the selected style. `←` and `→`
move focus only among visible columns. A sort arrow `↑` or `↓` fits inside the column's width:
beside a left-aligned title, or in the final cell of a right-aligned one. If a resize removes the
focused column, focus moves to the nearest visible column. The sort stays active, and its arrow
returns when the column reappears.

`!` marks missing pricing only. The list does not mark API errors.

## Session detail

The `Session` panel title carries the project and the ancestor session labels as a `›`-separated
breadcrumb. The three metadata lines are:

1. The session title.
2. The agent, the project with its working directory, and the session's models.
3. The Git branch, the start-to-update time, and the total cost followed by the own and subagent
   splits.

If line 2 is too wide, it drops the working directory first. If line 3 is too wide, the cost splits
collapse to the total, and then the leading fields drop.

The tab panel title lists `Timeline` and `Overview` in fixed order. Brackets mark the active tab,
and the inactive name is muted. If the title does not fit, only the bracketed active name remains.
The brackets keep the active tab visible in mono.

### Timeline

The timeline is one flat chronological list. A session is one continuous log, so every event —
prompt, reply, thinking, tool call, compaction, system note, unattributed usage, subagent spawn —
is a sibling row. Indentation is reserved for what a row contains.

Each row starts with the selection marker, a muted time gutter, and the fold-marker column. The
gutter shows relative age. `T` switches it to clock time, with the date when the session spans more
than one date. Request metrics sit flush right on the event row:

- **`↑R/W/I ↓O`** is the request's cache-read, cache-write, uncached-input, and output tokens.
- The cost appears only when the request is priced and reaches one displayed cent.
- **`ctx`** is a context size in tokens: the request's prompt tokens (uncached input, cache reads,
  and cache writes) plus its output tokens. A request row therefore reports the context after its
  own output, not the prompt that it sent.

A user prompt bills nothing itself. Its row reports the prompt tokens of the first billed request
before the next prompt, which is the context that request started from. Each figure belongs to one
request, so a later row's `ctx` can be lower than an earlier row's without a compaction between
them. A compaction row has no metrics. If the log records the post-compaction context, the row
appends it to its text as `· ctx N`. Codex compaction records do not include it. A subagent row reports the child's recursive tokens and cost. When a metric row is
narrow, its left side truncates first, and the metrics keep their column.

If a billed request has no content row to carry its usage, an `unattributed usage` row carries its
model, tokens, cost, and context. If a
[Codex counter segment](ADR/20260905-codex-counter-segments.md) cannot be split safely into request
deltas, the rows of that segment carry no request metrics. Overview then reports the segment's
authoritative usage per model as `unattributed`, with a line that explains the missing turn costs.
Clean segments keep their request metrics.

A row shows `▸` or `▾` only when it has a body:

- a prompt or reply with more than one non-empty line, or one line wider than `timelinePreviewCap`,
  which does not depend on the terminal width
- a tool call with a diff, an output, or a multiline input

The body appears two cells in. A tool body shows diff rows first, then a muted `input:` section,
then a muted `output:` section. File tools (`Read`, `Edit`, `MultiEdit`, `Write`, and
`apply_patch`) keep their path in the event row and show no input section. An expanded row never
repeats text: the event row drops its input preview while the body shows the input, and drops the
result summary while the body shows the output. Past `detailPreviewLineCap` lines or
`detailPreviewRuneCap` characters, a section keeps its head and tail around one
`… N lines hidden …` row. Thinking, compaction, system, and usage
rows have no body. The item view shows each event's complete text.

`space` toggles the selected line's event, and `←` and `→` fold and unfold it. `E` and `C` replace every
individual fold choice and set the state for the existing rows and for the rows that arrive later.
If a fold removes the selected line, the cursor moves to the event's header. Rebuilds preserve the
cursor's line offset within its event, clamped to the event's remaining lines.
Tab switches retain only the event selection. Returning to Timeline selects that event's header,
or the final line when tail following resumes.

`w` switches body, compaction, and system rows between hard wrapping and truncation. Each wrapped
row keeps the role of its logical row, and the selection highlights every wrapped row of the
selected text line. A left click selects the clicked text line, including a wrapped continuation,
without folding its event.

A subagent never expands inline. `enter` or `l` on its row stores the current detail screen and
opens the child on its Timeline tab. `esc` or `h` restores the nearest stored screen, and the same
key at the root returns to the list. The child inherits the parent's wrap setting, bulk fold state,
and Subagents sort and column focus. Later changes affect only the active screen. `enter` or `l` on
any other row opens an item view.

### Overview

Overview places an Activity table first, then the own-model cost blocks, then the Subagents table.

Activity shows `TURNS`, `TOKENS`, and `COST` for `own`, `subagents`, and `total`. A session without
subagents shows only `own`. Cost uses the same owned split as the header, and tokens subtract the
same replayed Claude requests. Under width pressure, Activity drops `TOKENS`. Below Activity, replay
attribution appears when the session replays requests that an earlier session owns, and
unattributed Codex usage appears when a counter segment could not be split.

Each own-model block shows the model's full display name and pricing markers, then its rate lines,
logged-cost overrides, replayed amounts, and subtotal. A model without a usable price shows
`price unavailable` in place of rate terms. A note that explains `~` appears only when an own model
uses an estimated rate. A session without model usage shows `No own model usage.` In
Overview, `w` word-wraps the cost blocks and notes, and table rows always stay fitted.

The Subagents table lists every descendant in pre-order. Tree guides indent nested rows, and a deep
row replaces its ancestor guides with `…` to keep a minimum title width. Each row shows `AGENT`,
`TITLE`, `MODEL`, `TURNS`, `COST`, and `AGE`. Turns and cost include all descendants. As in the
session list, `T` switches `AGE` to `TIME`.

With sorting cleared, siblings sort within each parent by update time, newest first, as in the
session list. Zero timestamps sort last, and ties use session identity. Column sorts also reorder
siblings within each parent, so descendants stay beneath their parent. Running siblings can move
during live refreshes, and a selected subagent follows session identity.

Under width pressure, `AGE` disappears first, then `TURNS`. Next, `TITLE` and `MODEL` shrink to
their minimum widths, and then `MODEL` disappears. Further pressure shrinks `TITLE`, `AGENT`, and
`COST` to one cell each. Columns then disappear from the right until the row fits.

With subagents present, the cursor moves through every Overview line, including Activity, cost
blocks, headings, and blank separators. A left click selects its text line, including a wrapped
continuation, without opening a child. `enter` or `l` opens a child only on its subagent row. Back
restores the parent's selection. Column focus and sort keys act on the Subagents table wherever
the cursor is. Page keys and the mouse wheel scroll without moving the cursor.

On first entry, Overview selects the first subagent. A tab return remembers a subagent by key only
if the cursor left from its row. Otherwise, the return selects the first subagent. Rebuilds and
live updates preserve a subagent selection by key. Above the table, they preserve the text-line
index, clamped to stay above the first subagent row. The position counter shows `N/total` on a
subagent row and `0/total` above the table. A session without subagents has no cursor, its line keys
scroll the viewport, and its table shows `No subagents`.

### Item view

The item view lists its sections in this order: Event, Request when the row carries usage, the
kind-specific content, then Raw when the row has a source record. Event aligns metadata labels and
values in two columns. Request shows the token flow, `ctx`, and the rate term of each token category
as Overview shows it, and closes with a precise total. A substituted rate names the published
stand-in and the logged model before the arithmetic.

A tool item shows Input, Diff, Output, and Result summary sections. Other items show one content
section: Prompt, Harness, Message, Thinking, Advisor, System, Compact, or Usage. Content sections
show the complete extracted fields without the timeline preview caps. Raw shows the source record.
[Always-present raw item records](ADR/20260726-always-present-raw-item-records.md) records its
format and failure rules.

The item title joins the project, the ancestor and current session labels, and a short event label
with `›`. If the title is too wide, it drops text from the left, so the event label stays visible.
The viewport scrolls by line or to an edge, and `w` toggles wrapping. `esc` and `h` restore the
parent detail screen.

## Keys

Each binding is one keystroke, never a sequence, and follows terminal and Vim conventions. On
session detail screens, `h` mirrors `esc` and `l` mirrors `enter`. The item view takes `h` for back,
and the session list binds neither. `←` and `→` act on the focused structure: column focus in a
table, and folding on the Timeline.

On both detail tabs, `j`/`down` and `k`/`up` move the cursor one text line, and a wrapped line is
one step. Movement reveals the whole selected line when it fits, or its first screen row when it
exceeds the viewport. `g` selects the first line, and `G` selects the last. At either end, another
outward press scrolls the viewport one screen row without moving the cursor, clamped at the content
edge. Overview without subagents has no cursor. Its `j`/`k` keys scroll one screen row, and `g`/`G`
scroll to the viewport edges.

On Timeline, fold and open keys act on the event that contains the selected line, and the position
counter counts events. `G` also resumes tail following. The Timeline follows live updates while
its viewport is anchored at the bottom and the cursor belongs to the last event. A followed update
selects the final line.

A new action on a column or row reuses the focus and an existing key where it can, as `⇧O` sorts
the focused column. `⇧A` and `⇧N` are the only direct sort shortcuts, for `AGE`
and `TITLE`, and a new column gets none. [Table sorting](ADR/20260722-table-sorting.md) records why.
A new binding must appear in the help view of every screen where it acts.

## Width and glyph safety

Width-sensitive rendering has one invariant. Build plain text, truncate it with `ansi.Truncate`, pad
it from `ansi.StringWidth`, and only then apply a Lip Gloss style. `fitPlain` truncates and pads. A
`panelLine` carries a row's plain and styled forms, and `renderPanel` uses the styled form only when
the plain form fills the inner width exactly. Otherwise the row loses its styling. Styled text never
goes back through width, truncation, or padding logic. A selected row is assembled and padded to the
panel's inner width before its selection style is applied once. Untrusted log text passes through
`model.TerminalText` or `model.TerminalLine` before any width calculation.

`TestUIGlyphsHaveStableDisplayWidths` checks each interface glyph, including the rounded border
characters, as one display column, and a new glyph must join its list. If `ansi.StringWidth` does
not report every rounded-border glyph as one cell, panels fall back to `+`, `-`, and `|` rather than
risk drift. The warning marker is ASCII `!`. We rejected
`⚠` because emoji-capable terminals can render it two columns wide.

| Glyph | Meaning |
| --- | --- |
| `⑃` | A subagent or Workflow run that opens as its own detail screen, or an advisor call |
| `⚙` | A tool call and its linked result |
| `◇` | Thinking, compaction, a system event, or unattributed usage |
| `▸` / `▾` | A collapsed or expanded row, shown only on rows that can expand |
| `›` | The selected row, and the breadcrumb separator |
| `~$` | A cost at a stand-in rate, because the logged model has no exact published price |
| `!` | Missing pricing, so the total is partial |
| `…` | Truncated text or hidden lines |
| `▊` | The cursor in the filter input |
| `∞` | A cost too large to display |
| `—` | No value, such as an unknown model or update time |
| `↑` / `↓` | Sort direction in a header, or input-side and output tokens in a metric |
| `+` / `-` / space | An added, removed, or context diff row |
