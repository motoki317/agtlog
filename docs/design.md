# Interface design

agtlog is a resource browser for coding-agent sessions. The home screen answers “which sessions
cost what?” and the detail screen answers “what happened, and where did the cost go?” Information
appears rolled up first and expands in place on demand.

## Screen structure

The list fills the terminal with three stacked regions:

1. A rounded context panel titled `agtlog`. Its one-line summary contains visible session and
   project counts, visible rolled-up cost, existing watch-root count, and active filter, agent,
   or refresh state. An active sort appears as its column and direction, such as `sort:cost↓`;
   cleared sorting has no summary label. During filter editing, a second inner line shows the live
   `/query▊` input and scrolls horizontally to keep the editing cursor visible.
2. A rounded `Sessions` panel. It contains the column header and the scrollable row window and
   consumes all height left between the context panel and key bar. The title becomes
   `Sessions · filtered` when a text or agent filter is active. The bottom border shows the
   selected position when rows overflow.
3. An unbordered one-line key bar:

   ```text
   ↑/↓ move   / filter   ←/→ column   ⇧O sort   T time   ↵ open   ? help   q quit
   ```

The detail screen uses the same structure. A rounded `Session` panel contains three metadata
lines, a rounded tab panel consumes the remaining height, and an unbordered detail key bar sits at
the bottom. The tab panel leads with the active `Timeline` or `Subagents` name and shows the
inactive name faint when space permits. The help view is a full-width rounded panel. Every view
recomputes its panel, viewport, and column geometry after a terminal resize, including detail
screens stored beneath a drilled child.
The navigation stack stores session details and item views behind the same `detailScreen`
interface (`update`, `view`, `resize`, and `setWrap`). Push, pop, resize, and wrap inheritance
therefore use one path for both concrete screen types.

When the terminal is too short to hold complete stacked regions, list and detail switch to one
rounded compact panel (plus a key line when it fits). This preserves both borders and the
highest-value summary instead of slicing a pre-rendered panel.

## Themes and emphasis

Color is semantic. Theme roles cover borders, panel titles, table headers, normal and selected
rows, each agent, warnings, muted metadata, estimates, accents, key hints, prompts, and added and
removed diff lines. Ordinary user, assistant, and thinking prose never takes an agent color.
Only the short agent or role label carries identity color; thinking remains muted.

| Theme | Claude | Codex | Accent | Warning | Diff add | Diff remove | Base | User prompt | System prompt |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `default` | `#D19A66` | `#61AFEF` | `#61AFEF` | `#E06C75` | `#98C379` | `#E06C75` | `#1E222A` | `#262B33` | `#2B2A26` |
| `nord` | `#88C0D0` | `#81A1C1` | `#88C0D0` | `#BF616A` | `#A3BE8C` | `#BF616A` | `#2E3440` | `#353C4A` | `#3B4252` |
| `dracula` | `#BD93F9` | `#8BE9FD` | `#50FA7B` | `#FF5555` | `#50FA7B` | `#FF5555` | `#282A36` | `#2E3040` | `#343646` |

`--theme` takes precedence over `AGTLOG_THEME`; otherwise the default palette is used. `t` cycles
`default` → `nord` → `dracula` at runtime. `NO_COLOR` forces the implicit `mono` theme regardless
of those selections. Mono identifies itself in the context summary, hides the inactive `t`
binding, and makes `t` a no-op. Mono retains bold, faint, reverse-video, layout, text markers,
and glyph meaning. A terminal that exposes only an ASCII color profile also uses semantic
bold, faint, and reverse styles so selection does not depend on unavailable colors.
Diff rows retain their `+`, `-`, or context-space prefix in every theme. Color themes render
additions solid green, removals solid red, and context muted. Mono uses the prefixes, with bold
added lines, instead of color.

User-entered prompt rows use the user-prompt background; system and compaction rows use the
system-prompt background. Both tints cover the padded row and are absent in mono. A selected row
uses the selection foreground and background instead of either prompt tint. Reduced color
profiles use explicit palette fallbacks so the base, prompt, and selection backgrounds stay
distinct after color conversion.

The selected list row is one full-width highlight bar with a single foreground color. Unselected
rows use the agent color in `AGENT`, the accent color in `SUBS`, muted `AGE` and `MSGS`, and the
estimated style for `~$`.

## Session columns

The list interleaves all agents and shows one row per top-level session. Subagents do not become
separate rows; their tokens and cost are included recursively in the parent.

| Column | Width and alignment | Content | First sort |
| --- | --- | --- | --- |
| `AGENT` | 6, left | `claude` or `codex`, with `!` replacing the last cell when the session has an error | A→Z |
| `PROJECT` | 8–18, left | Basename of the working directory | A→Z |
| `TITLE` | 20 minimum, left | Agent title or first useful user prompt; absorbs remaining width | A→Z |
| `MODEL` | 13, left | Costliest model, plus `+N` and missing-pricing `!` markers | A→Z |
| `AGE` | 4, right | Relative time such as `5m`, `2h`, `4d`, or `1.2y` | Oldest first |
| `MSGS` | 5, right | Own message count | Largest first |
| `SUBS` | 4, right | Count of subagents folded recursively into the row; blank when none | Largest first |
| `$` | 7, right | Recursive cost; Codex estimates use `~$` and partial totals use `!` | Largest first |

Columns have one space between them. Header and data rows use identical widths and alignment.
Slack grows `TITLE` first, then `PROJECT` up to 18 columns, then returns to `TITLE`. When the
minimum set does not fit, columns disappear in this order: `MODEL`, `MSGS`, `SUBS`, `PROJECT`,
then `AGE`.
At ordinary narrow widths the retained core is `AGENT TITLE $`; at smaller physical widths the
title shrinks and the lowest-value numeric field eventually disappears. Rows never wrap or cross
the panel border.

The focused header cell uses the selected style and starts at `AGENT`. `←` and `→` move it only
among visible columns. An active sort replaces the final cell of its header with `↑` or `↓`; the
column never widens for the arrow. A resize that removes the focused column moves focus to the
nearest survivor but retains the active sort, so its arrow returns when the column reappears.
`AGE` direction follows the raw update timestamp in both relative and absolute time modes.

## Width and glyph safety

Width-sensitive rendering has one invariant: build plain text, truncate it with
`ansi.Truncate`, pad it from `ansi.StringWidth`, and only then apply a lipgloss style. Styled text
is never passed back through cell width, truncation, or padding logic. A selected row is assembled
and padded to the session panel's inner width before its selection style is applied once.

All interface glyphs are tested as one display column, including the rounded border characters.
If the active terminal width rules do not report every rounded-border glyph as one cell, panels
fall back to `+`, `-`, and `|` rather than risking drift.
The warning marker is ASCII `!`; `⚠` was rejected because emoji-capable terminals may render it as
two columns. `⑃` remains the subagent marker only in the detail timeline; it measures and renders
as one column in the supported terminal path.

| Glyph | Meaning |
| --- | --- |
| `⑃` | Marks a subagent that opens as its own detail screen |
| `~$` | API-equivalent estimate; a subscription user normally pays less |
| `!` | Session error or missing pricing, depending on the cell |
| `▸` / `▾` | Collapsed / expanded item |
| `▸ you:` | User prompt |
| `⚙` | Tool call and linked result |
| `◇` | Thinking, compaction, or secondary system event |
| `+` / `-` / leading space | Added, removed, or context diff row |

## Session detail

The header panel uses three lines:

1. agent, project, and full working directory;
2. all models, Git branch, and start-to-update time;
3. recursive tokens and cost, followed by own and subagent splits.

The timeline is one flat chronological list, expanded by default. A session is one continuous log,
so every event — prompt, thinking summary, tool call, compaction, system note, subagent spawn — is
a sibling row, and each row states its own request's tokens, cost, and the context window it was
sent with:

```text
▸ you: Investigate the failing watcher                                    ctx 62k
  ◇ thinking: The lock is held across the reload      ↑61k/0/1200 ↓340 · ctx 62k
▾ ⚙ Edit(watch.go) → updated · 0.4s                  ↑62k/0/900 ↓1100 · ctx 63k
    output: watcher tests passed
  claude: The race is fixed                          ↑63k/0/800 ↓2000 · ctx 64k
  ⑃ Task(inspect watcher) opus-4.8                              420k · $1.02
```

A user prompt bills nothing itself, so its row reports only the window the request it triggered was
sent with. Reading down, the context column therefore only grows until a compaction resets it.

Indentation is reserved for what a row contains. A prompt with more than one line, an assistant
reply beyond the preview cap, and a tool call with a diff, output, or multiline input each carry
their own `▸`/`▾` marker and reveal their body two spaces in: diff rows or a muted `input:` section
first, followed by a muted `output:` section. Non-file tools and multiline commands show their
input. Each section keeps its head and tail when it exceeds the preview line cap and inserts one
`… N lines hidden …` row.

A subagent never expands inline. `enter` or `l` on its row pushes the current detail state and opens
the child on its Timeline tab. The `Session` title carries the project and ancestor session labels
as a `›`-separated breadcrumb. `esc` or `h` restores the nearest stored parent; the same key at the
root returns to the session list. Each child inherits its parent's wrap setting, while later wrap
changes affect only the active screen.

The Subagents tab lists every descendant in pre-order. Its cleared order sorts siblings within
each parent by `StartedAt`, oldest first, so descendants remain beneath their parent and active
siblings do not move as their `UpdatedAt` changes. Column sorts also reorder siblings rather than
the flattened list. The `AGE` column is the exception to the cleared key: it sorts the displayed
`UpdatedAt` value. Nested descendants are indented by depth; each row shows the agent, title,
costliest model, recursive tokens, recursive cost, and relative age. Age is omitted first under
width pressure so token and cost cells remain intact. Agent, token, and estimated-cost cells
receive their semantic styles only after the plain row is fitted. The tab has its own selection,
column focus, and sort state, supports step and edge movement, and drills into the selected session
on Timeline. A session without descendants shows `No subagents`.

Timeline rows wrap by default. `w` switches the current detail screen between hard wrapping and
truncation. Wrapping operates on plain text before color is applied; every visual row retains
the logical row's role, and selection highlights all wrapped rows belonging to the selected item.

`space` is the only in-place expansion key. `enter` or `l` opens the focused row: a subagent opens
its session detail, while an assistant reply, tool, thinking row, user message, compaction, or
system event opens a pushed item view. A tool item shows its full input, diff, and output, bounded only by the
model's per-field limit rather than the timeline preview cap. Other item views show the event's
full text. The item title extends the session breadcrumb with a short event label. Its viewport
supports `j`, `k`, `g`, `G`, and `w`; the normal back keys restore the parent detail.

## Key budget

Bindings stay single-key and follow terminal and Vim conventions:
The detail key bar states the primary split as `space toggle · enter open`.

| Screen | Keys | Action |
| --- | --- | --- |
| List | `j`/`k`, `↑`/`↓` | Move and keep the selection in the row window |
| List | `pgup`/`pgdn`, `home`/`end` | Move by a page or jump to an edge |
| List | `g`/`G` | Jump to top or bottom |
| List | `/` | Fuzzy-filter agent, project, and title |
| List | `←`/`→` | Move focus among visible columns |
| List | `shift+O` | Sort the focused column, reverse it, then clear the sort |
| List | `shift+A` / `shift+N` | Sort AGE or TITLE directly with the same three-state cycle |
| List | `a` | Cycle all, Claude, and Codex |
| List | `enter` | Open detail |
| List | `r` | Rediscover sessions |
| Detail | `j`/`k`, `↑`/`↓`, `g`/`G` | Move and scroll; `g`/`G` jumps to top or bottom |
| Detail | `space` | Expand or collapse the selected row's body; no-op on subagents |
| Timeline | `←`/`→` | Collapse or expand the focused row |
| Subagents | `←`/`→`, `shift+O`, `shift+A`, `shift+N` | Move column focus or cycle the focused, AGE, or TITLE sort |
| Detail | `enter`, `l` | Open the focused row; subagents open session detail and other rows open an item view |
| Detail | `tab`/`shift+tab` | Cycle Timeline, Subagents, and Info |
| Detail | `w` | Toggle timeline wrapping; wrapping is the default |
| Detail | `esc`/`h` | Pop the current detail screen; return to the list at the root |
| Item | `j`/`k`, `↑`/`↓`, `g`/`G` | Scroll by a row or jump to an edge |
| Item | `w` | Toggle wrapping; wrapping is the default |
| Item | `esc`/`h` | Restore the parent detail screen |
| Both | `t` | Cycle color themes; no-op in mono |
| Both | `?` | Toggle full help |
| Both | `q`/`ctrl-c` | Quit |

## Terseness rules

1. Each screen answers one question without opening another view.
2. Rows start expanded and collapse in place; `enter` opens every focused row as its own screen.
3. Lists use human-scale numbers and relative time, not raw counters or timestamps.
4. Parent rows show recursive totals; detail shows the breakdown.
5. Hard noise such as permission preambles and synthetic reminders does not enter the timeline.
6. Headers, list rows, and key bars truncate. Timeline and item rows wrap by default and truncate while wrapping is disabled.
