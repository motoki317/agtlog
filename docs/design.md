# Interface design

agtlog is a resource browser for coding-agent sessions. The home screen answers “which sessions
cost what?” and the detail screen answers “what happened, and where did the cost go?” Information
appears rolled up first and expands in place on demand.

## Palette and emphasis

Color is semantic and sparse:

| Element | Style | Meaning |
| --- | --- | --- |
| Claude | warm `#D19A66` | Claude Code session or event |
| Codex | cool `#61AFEF` | Codex session or event |
| Warning | red `#E06C75` | Parse or API error |
| Age, messages, estimates | faint | Secondary or approximate information |
| Selected row | reverse video | Current keyboard target |
| Footer total | bold | Aggregate for the visible rows |

`NO_COLOR` removes the three colors. Reverse video, weight, layout, text markers, and glyphs retain
the same meaning in monochrome terminals.

## Session list

The list interleaves all agents and shows one row per top-level session. Subagents do not become
separate rows; their tokens and cost are included recursively in the parent.

| Column | Content |
| --- | --- |
| `AGENT` | `claude` or `codex`, plus `⚠` when the session contains an error |
| `PROJECT` | Basename of the working directory |
| `TITLE` | Agent title or first useful user prompt |
| `MODEL` | Costliest model, shortened, plus `+N` for additional models and `!` for missing pricing |
| `AGE` | Time since the most recent activity, such as `5m`, `2h`, `4d`, or `1.2y` |
| `MSGS` | Own message count, compacted when large |
| `TOKENS` | Recursive total, such as `88k`, `1.2M`, or `1.0B`, plus `⑃N` for folded subagents |
| `$` | Recursive cost; Codex estimates use `~$` and partial totals use `!` |

The fixed order is `AGENT PROJECT TITLE MODEL AGE MSGS TOKENS $`. Rightmost columns disappear first
when the terminal narrows. Cell formatters keep numeric values inside their assigned widths; the
table never widens beyond the screen.

The footer begins with visible session count, project count, and total cost. It adds active filter,
sort, agent, or refresh state when present, then includes as many high-value hints as fit:

```text
— 12 sessions · 4 projects · ~$18 [/] filter [s] sort [a] agent [↵] open [?] help [q] quit
```

## Session detail

The header uses three lines:

1. agent, project, and full working directory;
2. models, Git branch, and start-to-update time;
3. recursive tokens and cost, followed by own and subagent breakdowns.

The timeline is chronological and collapsed by default:

```text
▸ you: Investigate the failing watcher
▸ ● claude: The race is fixed · 2 thinking · 4 tools · 1 subagents
  ⚙ Edit(watch.go) → updated · 0.4s
  ⑃ Task(inspect watcher) ▸ opus-4.8 · 420k · $1.02
```

Expanding an assistant turn reveals thinking summaries, tool calls and linked results, compaction
events, and subagent spawns. Expanding a subagent inserts its own timeline at the next indentation
level. The same rule applies recursively.

## Key budget

Bindings stay single-key and follow terminal and Vim conventions:

| Screen | Keys | Action |
| --- | --- | --- |
| List | `j`/`k`, `↑`/`↓` | Move |
| List | `/` | Fuzzy-filter agent, project, and title |
| List | `s` | Cycle age, tokens, and cost sort |
| List | `a` | Cycle all, Claude, and Codex |
| List | `enter` | Open detail |
| List | `r` | Rediscover sessions |
| Detail | `j`/`k`, `↑`/`↓` | Move and scroll |
| Detail | `space`/`enter` | Expand or collapse |
| Detail | `J`/`K` | Next or previous subagent |
| Detail | `esc`/`h` | Return to the list |
| Both | `?` | Toggle full help |
| Both | `q`/`ctrl-c` | Quit |

The footer teaches the primary keys. `?` is a reminder for the complete set, not a prerequisite for
using the list.

## Glyph vocabulary

| Glyph | Meaning |
| --- | --- |
| `⑃N` | N recursive subagents are folded into a row |
| `~$` | API-equivalent estimate; a subscription user normally pays less |
| `!` | At least one model lacks pricing, so the displayed cost is partial |
| `⚠` | Session contains an agent or API error |
| `▸` / `▾` | Collapsed / expanded item |
| `▸ you:` | User prompt |
| `●` | Assistant turn summary |
| `⚙` | Tool call and linked result |
| `◇` | Thinking, compaction, or secondary system event |

## Terseness rules

1. Each screen answers one question without opening another view.
2. Turns and subagents start collapsed and expand with one key.
3. Lists use human-scale numbers and relative time, not raw counters or timestamps.
4. Parent rows show recursive totals; detail shows the breakdown.
5. Hard noise such as permission preambles and synthetic reminders does not enter the timeline.
6. Headers, rows, and footers truncate rather than wrap at 80 columns.
