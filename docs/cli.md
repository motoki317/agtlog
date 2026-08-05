# Machine-readable CLI

agtlog exposes local Claude Code and Codex sessions through `list`, `show`, and
`search`. Running `agtlog` without one of these verbs still starts the terminal UI.
When either stdin or stdout is non-terminal, that existing TUI path prints its
plain static snapshot instead.

Except for help, each command that exits successfully writes one response to
stdout. `-h` and `--help` write plain help text to stdout and exit 0. JSON is the
default format and uses two-space indentation. `--format text` emits a compact,
terminal-safe human view of a result. JSON is the complete machine contract.
Operational errors and usage diagnostics never share stdout with a response.
Non-fatal warnings are fields in a successful response.

## Compatibility

Every JSON document contains `schema_version`. Version 1 is the contract in this
document. Consumers must ignore unknown fields and reject schema versions they do
not support.

A change to a field's meaning, scope, unit, required status, nullability, or closed
enum requires a new schema version. A new optional field does not require a new
version.

All fields shown without an `optional` label are present. This rule includes zero,
`false`, empty strings, and empty arrays. Timestamps use RFC 3339 with an explicit
offset and retain the offset from the source log. A missing source timestamp is
`0001-01-01T00:00:00Z`.

## Common syntax

```text
agtlog list [flags]
agtlog show <selector> [flags]
agtlog search <pattern> [flags]
```

Flags can appear before or after the `show` selector or `search` pattern. `--` ends
flag parsing, and every later token is an operand. Flags must precede `--`; for
example, use `agtlog search --limit 5 -- -pattern`. The verb must be the first
command-line token.

Every subcommand accepts these flags:

| Flag | Meaning |
| --- | --- |
| `--agent claude\|codex` | Keep one agent. |
| `--format json\|text` | Select JSON or plain text. The default is `json`. |
| `--offline` | Explicitly retain the default cached and embedded pricing behavior. |
| `--refresh-prices` | Refresh the price cache before the command. |

Subcommands do not start a background price refresh. `--offline` and
`--refresh-prices` are mutually exclusive. `--theme` and `--no-watch` belong to
the terminal UI and are usage errors on subcommands.

## Session refs and selectors

A canonical ref identifies one node in a session graph:

```text
<agent>:<root-id>
<agent>:<root-id>#<subagent-path>
```

Every response uses canonical refs. Codex descendant refs use the logged agent
path, so a ref stays unchanged when a later record supplies a thread ID. A Claude
descendant ref can instead use its logged `agentId`; that Codex stability guarantee
does not apply to Claude.

The root ID and each subagent path segment are opaque strings derived from the
source log. `/` separates nested path segments after `#`. Reserved or non-ASCII
bytes inside a component use URL percent encoding, so log-derived `#`, `/`, and
control characters cannot change ref structure.

Input selectors accept a canonical ref, a root ID, or a descendant ID recorded by
either agent. They also accept a unique prefix of such an ID with at least six
characters. A Codex thread ID is a descendant ID. An absolute path selects the
session stored in that file.
A bare path segment for an inline subagent is not a global selector.

An exact ID wins over prefix matches. An ambiguous selector returns candidates in
canonical-ref order. Each candidate contains `ref`, `agent`, `project`, `title`,
and `updated_at`.

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
| `models` | string array | Model names for this node in lexical order. |
| `started_at` | timestamp | Start time for this node. |
| `updated_at` | timestamp | Latest time for this node or any descendant. |
| `messages` | integer | Message count for this node, excluding descendants. |
| `subagents` | integer | Recursive descendant count. |
| `has_error` | boolean | Whether this node reports an API error. |
| `tokens` | token totals | Recursive owned token totals. |
| `cost` | cost totals | Recursive owned API-equivalent cost. |
| `path` | string | Source path. |

Owned totals subtract replayed Claude requests that share the same nonempty
message ID and the same request ID. An empty request ID still participates; only
an empty message ID bypasses deduplication. The earliest `started_at` owns a
duplicate; equal starts use the lexically smallest root ID. Summing `tokens.total`
or `cost.usd` across `list` rows therefore produces a corpus total without double
counting.

Token totals use these required integer fields:

| Field | Meaning |
| --- | --- |
| `uncached_input` | Input tokens not read from or written to a prompt cache. |
| `output` | Output tokens. |
| `cache_write` | Prompt-cache creation tokens. |
| `cache_read` | Prompt-cache read tokens. |
| `total` | Sum of the four disjoint categories above. |

Codex input counts include cache reads in the raw log, so agtlog subtracts that
folded amount from `uncached_input`.

Cost totals use these required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `usd` | number | API-equivalent cost in US dollars. |
| `complete` | boolean | It is `false` when any logged model lacks a published rate. |
| `estimated` | boolean | It is `true` when agtlog used a published stand-in rate. |
| `missing_pricing` | string array | Unpriced model names in lexical order. |

## `list`

`list` returns top-level sessions only. The default order is `updated_at`
descending, then agent and ID.

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

Filters:

- `--project NAME` matches the project basename.
- `--cwd PATH` matches that directory and its descendants.
- `--query STRING` uses the terminal UI's fuzzy match over agent, project, and
  title.
- `--since VALUE` and `--until VALUE` filter `updated_at`.

A time value can be an RFC 3339 timestamp or a local date such as `2026-08-01`.
It can also be a duration such as `7d`, `24h`, or `90m`. A duration means that
amount of time before the wall clock at command start. Local dates use the
machine's local time zone at midnight. Both `--since` and `--until` boundaries
are inclusive. Consequently, `--until 2026-08-05` includes midnight at the start
of 5 August but excludes the rest of that day. The next date also includes exactly
its midnight because the boundary is inclusive; use an explicit final timestamp
when that distinction matters.

Use `--sort updated|started|tokens|cost|messages` and `--order asc|desc` to set the
order. Use `--limit N` and `--offset N` for pages. The default limit is 50.
`--all` removes the limit. Equal sort values use agent and then root ID in
ascending order.

For `list`, `show`, and `search`, `--all` sets `page.limit` to `0`. This value is a
sentinel for an unbounded count request, not the number of items returned.
`--limit 0` is a usage error for every command.

## `show`

`show` returns one session node, its direct subagent refs, its attribution split,
and its event timeline.

The `self` values under `totals.tokens` and `totals.cost` cover the selected node.
The `descendants` values cover its recursive children, and `total` is their sum.

Each event always contains integer `index`, timestamp string `timestamp`, event-kind
string `kind`, string `text`, string `model`, and string array `truncated`. The
common empty-value and missing-timestamp rules apply. `truncated` names bounded
fields: `text`, `tool.summary`, `tool.input`, `tool.diff`, or `tool.output`. Event
kinds are
`user`, `assistant-text`, `thinking`, `tool-call`, `tool-result`, `subagent`,
`advisor`, `system`, `compact`, and `usage`.

An event can also contain these fields:

| Field | Type and presence | Meaning |
| --- | --- | --- |
| `tool` | optional object | Tool metadata defined below. |
| `usage` | optional object | Normalized request tokens defined below. |
| `cost` | optional object | Cost for the request represented by `usage`. |
| `record` | optional object | Physical source record defined below. |
| `harness` | optional boolean | Present as `true` on a harness-injected user turn. |
| `subagent_ref` | optional string | Canonical ref for an event-linked child. |
| `compact` | optional object | Compaction metadata defined below. |
| `usage_aggregate` | optional boolean | Present as `true` on session-level fallback usage. |

The nested event objects have these required fields when present:

| Object | Fields |
| --- | --- |
| `tool` | Identity: `name` string and `call_id` string. Text: `summary`, `input`, `diff`, and `output` strings. Duration: `duration_ms` integer. |
| `usage` | Token integers: `uncached_input`, `output`, `cache_write`, `cache_read`, `flow`, and `context`. |
| `cost` | `usd` number, `complete` boolean, `estimated` boolean. |
| `record` | `path` string, `offset` integer byte offset, `length` integer byte length. |
| `compact` | `trigger` string, `post_tokens` integer. |

`usage.context` is the prompt size for the request. `usage.flow` is uncached input,
cache writes, and output added by that request. Event usage is diagnostic and can
include a session aggregate that could not be assigned to a request. Use session
`tokens`, not a sum of events, for totals.

Use `--kind K[,K...]` to keep selected event kinds. `--offset` refers to the full
timeline before kind filtering. `event.index` therefore stays stable across
filters. `page.next_offset` is one past the last returned full-timeline index.
When no event is returned, it equals `page.offset`. The default event limit is
200. `--all` removes the count limit.

`page.total` counts kind-matching events across the full timeline, including
matching events before `page.offset`. `page.complete` is true when no later
kind-matching event remains after this page. `page.has_more` is its inverse.

`--max-text N` limits each text-bearing event field to N runes. The default is
2,000. Zero removes the per-field limit, and `--full` is shorthand for zero. A
bounded field appears in `truncated`.

The complete event-page JSON response is limited to 256 KiB. The page normally
stops before an event that crosses the limit. If the first event alone crosses the
limit, agtlog bounds its text fields and returns it. This total limit also applies
with `--full`. Resume at `page.next_offset`. If required non-text metadata alone
exceeds the limit, the command fails with `internal` instead of emitting an
oversized document.

`--no-events` returns the summary, subagent refs, and attribution totals without
opening the source timeline. Its page echoes the requested `offset` in `offset`
and `next_offset`; `limit`, `returned`, and `total` are zero, `has_more` is false,
and `complete` is true. The page does not describe event availability.
`--no-events` and `--raw` are mutually exclusive.

`--raw INDEX` selects a separate response variant and returns the exact source line
for an event:

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

`raw_json` is a string. agtlog does not decode and re-encode its key order or
spacing. `--raw` requires JSON format; combining it with `--format text` is a usage
error. The event-page response budget does not apply because truncation would
violate byte exactness. An aggregate event without a physical source line returns
`record_unavailable`. A changed source line returns `record_changed`.

## `search`

`search` matches cleaned event text, tool input, diff, output, and result summary.
Cleaned text is the parsed timeline text after agtlog removes harness-only
`system-reminder`, `permission-preamble`, and `local-command-caveat` blocks. The
default match is a case-insensitive substring. `--case-sensitive` keeps case, and
`--regex` treats the pattern as RE2. Case-insensitive literal matching uses Unicode
simple case folding.

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

Each hit contains:

- `session`: `ref`, `agent`, `project`, `title`, and `updated_at`.
- `event`: `index`, `timestamp`, `kind`, and tool name.
- `field`: `text`, `tool.input`, `tool.diff`, `tool.output`, or `tool.summary`.
- `range`: `[start, end]`, the first match's half-open rune offsets in that field.
- `snippet`: text around the first match.
- `matches`: the number of matches in that field.

These fields use the same text projection as `show` before display bounds.

Search accepts the `list` filters except `--query`. `--kind` filters events.
`--session SELECTOR` searches one node and its descendants.

Summary filters select top-level roots, or the selected `--session` node. After a
root passes, search inspects all descendants; `--kind` applies to every inspected
event.

Summary filters run before agtlog opens any timeline. A project, directory, time,
agent, or session scope is the fast path. A corpus-wide search parses every
candidate graph.

Hits have one total order: root `updated_at` descending, canonical ref, event
index, then `text`, `tool.input`, `tool.diff`, `tool.output`, and `tool.summary`.

Use `--limit N`, `--offset N`, and `--all` for hit pages. The default limit is 30.
`--snippet N` sets the context runes on each side of the first match. The default
is 200.

The complete JSON response is limited to 256 KiB, including `--all` responses.
When that budget stops a page, `returned` is the number of emitted hits,
`next_offset` is the first omitted ordered hit, `has_more` is true, `complete` is
false, and `total` is absent. Resume with that `next_offset`. If required hit or
response metadata alone exceeds the budget, the command fails with `internal`.

`page.complete` is true only after an exhaustive, warning-free scan, and
`page.total` is an optional integer present only then. The total counts all ordered
hits, including hits before `page.offset`. `sessions_scanned` counts session nodes
inspected before ordered result collection stopped.
`sessions_matched` counts those nodes that produced at least one hit.
`page.has_more` is true only when the scan found an additional ordered hit beyond
the page. If `complete` is false, `has_more: false` does not prove that unreadable
sessions contain no further hits.

Each invocation discovers the current logs. Event indices remain stable while a
source timeline is unchanged. If a log changes between page requests, callers
must restart paging to obtain a snapshot-consistent result.

## Text pages and cost markers

Paginated text output includes a `PAGE` row. It reports `returned`, `total`,
`has_more`, and `next_offset`. Zero or more `WARNING` rows follow it. Each warning
row contains its code, ref or path, and message. The `show` and `search` page rows
also report `complete`; the search row reports its scan counters. Search uses
`total=-` when the exhaustive total is unavailable.

A cost prefixed with `~` uses a published stand-in price. A cost suffixed with
`!` is incomplete because at least one model has no published price.

Use this command to request a hit without the ordinary per-field bound. The 256
KiB event-page budget can still bound an oversized first event:

```bash
agtlog show <ref> --offset <index> --limit 1 --full
```

## Warnings and errors

Non-fatal problems use warning objects with `code`, `message`, and either `ref` or
`path`. Warning codes are `unreadable_session` for a log that could not be parsed
or loaded and `unaddressable_session` for a parsed graph without a unique stable
ref. Broad `list` and `search` commands continue past either condition. Any warning
makes a search incomplete and omits `page.total`.

If discovery already knows that a descendant of a scoped `--session` target is
unreadable, search fails with `unreadable_session` instead of claiming a complete
scan of that graph.

Errors always use JSON on stderr, even after `--format text`:

```json
{
  "schema_version": 1,
  "error": {
    "code": "not_found",
    "message": "no session matches the selector"
  }
}
```

An `ambiguous_ref` error also contains `candidates`.

| Error code | Exit | Meaning |
| --- | --- | --- |
| `usage` | 2 | Invalid syntax, flags, values, or flag combinations. |
| `not_found` | 3 | No selector match, or no event at a requested `--raw` index. |
| `ambiguous_ref` | 3 | More than one session matches a selector. |
| `unreadable_session` | 1 | A selected log or required session graph could not be read. |
| `unaddressable_session` | 1 | A parsed session has no unique stable canonical ref. |
| `record_changed` | 1 | A raw source record changed after discovery. |
| `record_unavailable` | 1 | An event has no physical source record. |
| `internal` | 1 | An internal invariant or runtime operation failed. |

Exit 0 means success, including help and an empty result.
