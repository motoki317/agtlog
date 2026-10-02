# Machine-readable CLI

agtlog exposes local Claude Code and Codex sessions through `list`, `show`, and `search`. Without
one of these verbs, `agtlog` starts the terminal UI.

`agtlog <verb> --help` lists the flags of each verb with their defaults.

A successful command writes one response to stdout. Help is the exception: `-h` and `--help` write
plain help text to stdout and exit 0. The default format, JSON, is the complete machine contract.
Errors and usage diagnostics never go to stdout. A successful response carries non-fatal warnings
as fields.

## Compatibility

Every JSON document contains `schema_version`. This document defines version 1. Consumers must
ignore unknown fields and reject a schema version that they do not support.

A change to the meaning, scope, unit, required status, or nullability of a field, or to a closed
enum, requires a new schema version. A new optional field does not. Every enum whose complete value
list this document gives is closed.

Every field that this document does not mark `optional` is present, also when its value is zero,
`false`, an empty string, or an empty array. Timestamps use RFC 3339 with an explicit offset and
keep the offset from the source log. A missing source timestamp is `0001-01-01T00:00:00Z`.

## Common syntax

```text
agtlog list [flags]
agtlog show <selector> [flags]
agtlog search <pattern> [flags]
```

The verb must be the first command-line token. Otherwise agtlog parses the arguments as terminal
UI flags and writes no JSON. For example, `agtlog --offline list` writes the terminal UI usage
text and `agtlog: unexpected argument "list"` to stderr and exits 1.

Flags can appear before or after the `show` selector or the `search` pattern. `--` ends flag
parsing, and every later token is an operand, for example `agtlog search --limit 5 -- -pattern`.

agtlog reads Claude logs from `projects` below the home that `CLAUDE_CONFIG_DIR` names. If that
variable is unset, it reads `~/.config/claude/projects` and `~/.claude/projects`. It reads Codex
logs from `sessions` below `CODEX_HOME`, or from `~/.codex/sessions` if that variable is unset. A
missing directory among these yields no sessions and no error.

Each `--claude-dir` or `--codex-dir` value adds an agent home, and agtlog appends `projects` or
`sessions` to it. An added home never replaces the homes above. To read only chosen homes, set
`CLAUDE_CONFIG_DIR` and `CODEX_HOME` to them.

If a verb has no `--claude-dir`, the `AGTLOG_CLAUDE_DIRS` list supplies the added Claude homes, with
the separator that `PATH` uses. `AGTLOG_CODEX_DIRS` works the same way for `--codex-dir`. If an
added home is missing or is not a directory, the command fails with `usage`. `--agent` skips this
check and discovery for the other agent.

Subcommands never start a background price refresh, so `--offline` only states the default.
Combining `--offline` with `--refresh-prices` is a usage error.

## Paging

In every command, `--all` removes the count limit and sets `page.limit` to `0`. `--limit 0` is a
usage error.

Each command discovers the current logs. Event indices stay the same while a source timeline does
not change. If a log changes between page requests, the caller must restart paging to get a
consistent result. An append to the log of a live session is such a change, and no response field
reports it.

## Session refs and selectors

A canonical ref identifies one node in a session graph:

```text
<agent>:<root-id>
<agent>:<root-id>#<subagent-path>
```

`<root-id>` is the session ID of the top-level session. `<subagent-path>` has one or more segments
separated by `/`:

- A Codex descendant uses its logged agent path without the leading `/root/`.
- A Claude subagent uses its logged `agentId`, after the path of its parent subagent, if any.
- A Claude Code Workflow run becomes a **group node** at `<run-id>`, the logged `runId`. The agents
  of the run are its children at `<run-id>/<agent-id>`.

A group node has no log file and no events of its own. No JSON field marks it: its `path` is the
log path of its parent plus `#<run-id>`, and `show` returns no events for it.

Every response uses canonical refs. A Codex descendant ref stays the same when a later record
supplies the thread ID. If two Codex siblings log the same agent path, only one keeps that path,
and the other uses its thread ID after the path of its parent. Which sibling keeps the path can
change when the parent log announces another spawn on that path. This contract promises no
stability for a Claude descendant ref.

The root ID and each path segment are opaque strings from the source log. agtlog percent-encodes
each one as a URL path segment. A `#`, `/`, or control character from a log therefore cannot change
the structure of a ref. The session objects carry these IDs only inside `ref`.

A selector can be one of these:

- a canonical ref
- the absolute path of a log file
- an ID: a top-level session ID, a Claude `agentId`, a Workflow `runId`, or a Codex thread ID
- a unique prefix of such an ID with at least six characters

An exact match wins over a prefix match. A selector shorter than six characters that matches
nothing exactly fails with `usage`. A Codex child whose thread ID is not logged yet has only its
agent name as an ID, and that name does not select it. Only its canonical ref does.

An ambiguous selector fails with `ambiguous_ref`. Its `error.candidates` array lists the matches in
canonical-ref order, each with `ref`, `agent`, `project`, `title`, and `updated_at`.

## Shared session fields

`list.sessions[]` and `show.session` use this object:

| Field | Type | Meaning |
| --- | --- | --- |
| `ref` | string | Canonical ref. |
| `agent` | `claude` or `codex` | Source agent. |
| `project` | string | Basename of the working directory. |
| `cwd` | string | Working directory from the log. |
| `title` | string | Session title. |
| `git_branch` | string | Logged Git branch. |
| `models` | string array | Model names of this node in lexical order. |
| `started_at` | timestamp | Start time of this node. |
| `updated_at` | timestamp | Latest time of this node or any descendant. |
| `messages` | integer | Messages of this node, without descendants. |
| `turns` | integer, optional | User messages, agent messages, and tool calls of this node and all descendants, including Workflow children. |
| `subagents` | integer | Recursive count of agent transcripts. Workflow group nodes do not count. |
| `has_error` | boolean | `true` when this node logged an API error. Only Claude logs set it. |
| `tokens` | token totals | Recursive owned token totals. |
| `cost` | cost totals | Recursive owned API-equivalent cost. |
| `path` | string | Log file of this node. A Workflow group, or a Codex child stored in its parent's log, has `<parent log>#<suffix>` instead. That value is not a file, and no selector matches it. |

Owned totals leave out each request that another session owns. When several sessions log the same
request, the session with the earliest `started_at` owns it. Equal start times go to the lexically
smallest session ID. [Cross-session cost deduplication](ADR/20260724-cross-session-cost-dedup.md)
defines when two log entries are the same request.

agtlog assigns owners across all discovered sessions before the `list` filters run. A filtered list
therefore leaves out each request whose owner is outside the filter. The rows of an unfiltered
`list --all` without warnings sum to a corpus total that counts no request twice. With a warning,
the sum can miss requests, because a session that `unaddressable_session` leaves out can still own
them. Ownership never crosses agents, so the sum also holds for one agent under `--agent` alone.

Token totals have these required integer fields:

| Field | Meaning |
| --- | --- |
| `uncached_input` | Input tokens that were neither read from nor written to a prompt cache. |
| `output` | Output tokens. |
| `cache_write` | Prompt-cache creation tokens. |
| `cache_read` | Prompt-cache read tokens. |
| `total` | Sum of the four disjoint fields above. |

Cost totals have these required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `usd` | number | API-equivalent cost in US dollars. |
| `complete` | boolean | `false` when a logged model has no published rate. |
| `estimated` | boolean | `true` when a Codex model has no published rate of its own and agtlog applied the rate of a [stand-in model](ADR/20260719-cost-model.md). |
| `missing_pricing` | string array | Unpriced model names in lexical order. |

## `list`

`list` returns top-level sessions only.

```json
{
  "schema_version": 1,
  "command": "list",
  "sessions": [],
  "page": {
    "offset": 0,
    "limit": 50,
    "returned": 0,
    "total": 0,
    "has_more": false,
    "next_offset": 0
  },
  "warnings": []
}
```

`--query` keeps a session when the characters of the query appear in order, not necessarily
adjacent, in its agent, project, and title. The match ignores case. It reads these fields as a
terminal shows them: escape sequences are removed, and control and format characters become
spaces. The terminal UI filter uses the same match. `--query` does not change the row order.

`--since` and `--until` filter on `updated_at`. A time value is an RFC 3339 timestamp, a local date
such as `2026-08-01`, or a duration such as `7d`, `24h`, or `90m`. A duration counts back from the
wall clock at command start. A local date means midnight in the local time zone of the machine.
Because both bounds are inclusive, `--until 2026-08-05` includes midnight at the start of 5 August
and excludes the rest of that day.

`--sort` orders rows by one field: `updated` by `updated_at`, `started` by `started_at`, `tokens`
by `tokens.total`, `cost` by `cost.usd`, `turns` by `turns`, and `messages` by `messages`. The
default is `--sort updated --order desc`. Rows with equal values fall back to agent and then
session ID, both ascending under either `--order`.

## `show`

A `show` response holds `schema_version`, `command`, `session`, `subagent_refs`, `totals`,
`events`, `page`, and `warnings`. `session` describes the selected node with the shared session
fields. `subagent_refs` holds the canonical refs of its direct children in lexical order. `events`
holds one page of its timeline. `page` has the integers `offset`, `limit`, `returned`, `total`, and
`next_offset`, and the booleans `has_more` and `complete`.

Under `totals.tokens`, `totals.cost`, and the optional `totals.turns`, `self` covers the selected
node, `descendants` covers all nodes below it, and `total` is their sum. `totals.turns` holds
integers.

Every event contains integer `index`, timestamp `timestamp`, string `kind`, string `text`, string
`model`, and string array `truncated`. The event kinds are `user`, `assistant-text`, `thinking`,
`tool-call`, `tool-result`, `subagent`, `advisor`, `system`, `compact`, and `usage`. In `user` and
`assistant-text` events, `text` omits the harness-only `system-reminder`, `permission-preamble`,
and `local-command-caveat` blocks. `truncated` names each bounded field: `text`, `tool.summary`,
`tool.input`, `tool.diff`, or `tool.output`.

An event can also contain these fields:

| Field | Type and presence | Meaning |
| --- | --- | --- |
| `tool` | optional object | Tool metadata. |
| `usage` | optional object | Normalized request tokens. |
| `cost` | optional object | Cost of the request that `usage` describes. |
| `record` | optional object | Physical source record. |
| `harness` | optional boolean | `true` on a user turn that the harness injected. Absent otherwise. |
| `subagent_ref` | optional string | Canonical ref of the child that the event links to. |
| `compact` | optional object | Compaction metadata. |

A nested event object that is present has all of its fields:

| Object | Fields |
| --- | --- |
| `tool` | Identity: `name` string and `call_id` string. Text: `summary`, `input`, `diff`, and `output` strings. Duration: `duration_ms` integer. |
| `usage` | Token integers: `uncached_input`, `output`, `cache_write`, `cache_read`, `flow`, and `context`. |
| `cost` | `usd` number, `complete` boolean, `estimated` boolean. |
| `record` | `path` string, `offset` integer byte offset, `length` integer byte length. |
| `compact` | `trigger` string, `post_tokens` integer. |

`usage.context` is the prompt size of the request. `usage.flow` is the uncached input, cache
writes, and output that the request added. Event usage is diagnostic. For totals, use the session
`tokens`, not a sum of events.

`--kind K[,K...]` keeps the selected event kinds. `--offset` is an index into the full timeline
before kind filtering, so `event.index` stays the same across filters. `page.next_offset` is one
past the last returned index. If no event is returned, it equals `page.offset`.

`page.total` counts the events of the selected kinds in the full timeline, including events before
`page.offset`. `page.complete` is `true` when no event of the selected kinds follows this page.
`page.has_more` is its inverse.

`--max-text N` bounds each text field of an event to N runes, 2000 by default, and `--full`
removes the bound. A bounded field keeps its first and last runes around a `…` that replaces the
middle, and the `…` counts toward N.

The complete JSON response of an event page is limited to 256 KiB, also with `--full`. A page
stops before the event that crosses the limit. If the first event alone crosses the limit,
agtlog bounds its text fields and returns it. Resume at `page.next_offset`. If the required
non-text metadata alone exceeds the limit, the command fails with `internal` instead of writing an
oversized document.

`--no-events` returns `session`, `subagent_refs`, and `totals` without reading the timeline.
`events` is empty. Its `page` says nothing about which events exist, and its `limit` of `0` does
not mean an unbounded request. Combining `--no-events` with `--raw` is a usage error.

`--raw INDEX` selects a separate response that holds the exact source line of one event:

```json
{
  "schema_version": 1,
  "command": "show",
  "raw_record": {
    "index": 12,
    "path": "/workspace/logs/session.jsonl",
    "offset": 91234,
    "length": 5120,
    "raw_json": "{\"type\":\"event\"}"
  },
  "warnings": []
}
```

`raw_json` is a string. agtlog does not decode it, so its key order and spacing are those of the
source. `--format text` with `--raw` is a usage error. The 256 KiB limit does not apply.

## `search`

`search` matches the pattern against five fields of each event: `text`, `tool.input`, `tool.diff`,
`tool.output`, and `tool.summary`. Each one holds the text of the `show` field of the same name
before any `--max-text` bound. By default, the pattern matches as a case-insensitive substring
with Unicode simple case folding. `--regex` reads the pattern as RE2, also case-insensitive.
`--case-sensitive` keeps case in both modes.

```json
{
  "schema_version": 1,
  "command": "search",
  "hits": [
    {
      "session": {
        "ref": "claude:session-01",
        "agent": "claude",
        "project": "forge",
        "title": "Inspect relay",
        "updated_at": "2026-08-05T12:00:00Z"
      },
      "event": {
        "index": 12,
        "timestamp": "2026-08-05T11:59:00Z",
        "kind": "assistant-text",
        "tool": ""
      },
      "field": "text",
      "range": [8, 15],
      "snippet": "Inspect watcher race",
      "matches": 1
    }
  ],
  "page": {
    "offset": 0,
    "limit": 30,
    "returned": 1,
    "has_more": false,
    "next_offset": 1,
    "complete": true,
    "total": 1,
    "sessions_scanned": 1,
    "sessions_matched": 1
  },
  "warnings": []
}
```

In a hit, `event.tool` is the tool name. `field` names the matching field, one of the five above.
`range` is `[start, end]`, the half-open rune offsets of the first match in that field. `matches`
counts the matches in that field.

`snippet` is the field text from S runes before the first match to S runes after it. S is the
`--snippet` value, 200 by default. A `…` replaces the text cut from either side. In `snippet`, the
match therefore starts at rune offset `start`, or at S + 1 when `start` is greater than S.

`search` accepts the session filters of `list` except `--query`: `--agent`, `--project`, `--cwd`,
`--since`, and `--until`. `--session SELECTOR` limits the search to one node. The filters test each
top-level session, or the `--session` node, before agtlog reads any timeline. agtlog then searches
each selected node and all nodes below it. `--kind K[,K...]` keeps the hits in events of those
kinds.

Hits are sorted by these keys in turn:

1. the `updated_at` of the top-level session, descending
2. the top-level ref, which is the part of `session.ref` before `#`
3. `session.ref`
4. `event.index`
5. the field, in the order `text`, `tool.input`, `tool.diff`, `tool.output`, `tool.summary`

A hit in a descendant carries the `updated_at` of that descendant, which can be older than the
top-level `updated_at` that orders it.

The complete JSON response is limited to 256 KiB, also with `--all`. When the limit stops a page,
`next_offset` is the first hit that was left out, `has_more` is `true`, `complete` is `false`, and
`total` is absent. Resume at that `next_offset`. If the required metadata of the response or of one
hit exceeds the limit, the command fails with `internal`.

Search stops reading timelines once it holds `offset + limit + 1` ordered hits. Under `--all` or a
large `--limit`, `limit` here is an upper bound on the hits that fit in one response.
`page.complete` is `true` only when search read every timeline that it selected and no warning
occurred. `page.total` is an optional integer that is present only then. It counts all ordered
hits, including hits before `page.offset`. A page with more hits after it therefore has no `total`.

`sessions_scanned` counts the nodes that search read before it stopped, and `sessions_matched`
counts those among them with a hit. `page.has_more` is `true` when the scan found another ordered
hit after the page, or when the 256 KiB limit stopped the page. If `complete` is `false`,
`has_more: false` does not prove that an unreadable session holds no more hits.

To read the whole event of a hit, pass its `session.ref` and `event.index` to `show`:

```bash
agtlog show <session.ref> --offset <event.index> --limit 1 --full
```

## Warnings and errors

A warning object has `code`, `message`, and either `ref` or `path`. Its code is one of these:

- `unreadable_session`: agtlog could not load or parse a log.
- `unaddressable_session`: a parsed session has an empty ID, or its canonical ref collides with
  another, as with two diverged copies of one session.

`list`, and `search` without `--session`, leave such a session out, warn, and continue.

If a log of the selected graph cannot be read, `search --session` fails with `unreadable_session`
instead. A `search --session` response has no warnings. `show` always returns an empty `warnings`
array.

Errors go to stderr as JSON, also with `--format text`:

```json
{
  "schema_version": 1,
  "error": {
    "code": "not_found",
    "message": "no session matches the selector"
  }
}
```

| Error code | Exit | Meaning |
| --- | --- | --- |
| `usage` | 2 | Invalid syntax, flags, values, or flag combinations, a missing added home, or a selector shorter than six characters that matches nothing exactly. |
| `not_found` | 3 | No session matches the selector, or no event exists at the `--raw` index. |
| `ambiguous_ref` | 3 | More than one session matches the selector. |
| `unreadable_session` | 1 | agtlog could not read a selected log or a required session graph. |
| `unaddressable_session` | 1 | The selected session has an empty ID or a canonical ref that collides with another. |
| `record_changed` | 1 | A raw source record changed after discovery, or agtlog can no longer read it. Run the command again. |
| `record_unavailable` | 1 | An event has no physical source record. |
| `internal` | 1 | An internal invariant or runtime operation failed. |

Exit 0 means success, including an empty result.
