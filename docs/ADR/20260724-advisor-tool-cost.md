---
date: "2026-07-24"
author: "@motoki317"
status: "accepted"
---

# Context

The Anthropic Advisor tool runs a second model server-side inside one Messages request. Its usage
is reported in `usage.iterations[]` as an entry with `type: "advisor_message"` and its own `model`,
and — per the Messages API docs — it is billed at that advisor model's rates. The top-level `usage`
fields deliberately exclude it "because they are billed at a different rate", so the true billed
total for a request is the top-level usage plus every advisor iteration.

agtlog previously read only the top-level `usage`, so every advisor turn was uncosted and its
`server_tool_use` block was dropped (server tool blocks fell through to the default skip). Across
the local logs that undercounted ~$1.3k over 53 sessions; on one session it was the whole gap
between agtlog and the reference `ccusage` total. Same-tier Opus↔Opus pairing makes the advisor
bill at full Opus rates, so the omission is not marginal.

# Decision

Count each `advisor_message` iteration as its own usage, priced at the iteration's own model.

- `parseFile` appends one advisor usage record per iteration under a synthetic message key
  (`<messageID>\x00advisor\x00<index>`) so the existing dedup counts it once across re-logged lines
  and the session total, per-model costs, and info-tab breakdown all include it.
- `loadEvents` renders each advisor `server_tool_use` block as an `EventAdvisor` timeline row. The
  block is logged when the call opens but its usage completes on a later line, so advisor usage is
  collected per message across the whole pass and attached to the rows afterward; the Nth advisor
  block maps to the Nth iteration.

The executor side is unchanged: the top-level `usage` already reflects it, and agtlog keeps reading
it as before.

# Consequences

- Session, list, and info-tab totals match the billed total; on the validated session agtlog now
  equals `ccusage` to the cent.
- Each advisor consultation shows as its own `advisor(<model>)` row with its tokens and cost, so a
  same-tier advisor is visible instead of silently inflating the executor model's number.
- When the advisor model differs from the executor (e.g. Haiku main, Opus advisor), the info-tab
  per-model breakdown lists it separately because pricing keys on the iteration's model.

# Impact

The decision touches Claude usage parsing, event loading, and the timeline; it changes cost figures
for sessions that used the Advisor tool. It does not affect Codex parsing, compaction, the read-only
treatment of logs, or the pricing tables. The cache-fingerprint schema bump (parser `v12` → `v13`)
re-parses sessions cached before the change so their totals pick up advisor cost.

# Alternatives

**Trust the top-level `usage` alone** was rejected: the docs are explicit that advisor tokens are
billed and excluded from the top-level totals precisely because they price differently, so ignoring
them undercounts. An earlier reading mistook the advisor's uncached re-read of the transcript for a
double-counted re-representation; the primary source and a controlled `ccusage` experiment refuted
it.

**Fold advisor cost into the executor row/model** was rejected because it hides where advisor spend
happens and misprices any cross-tier pairing.

**Attach advisor usage on the block's own line** was rejected because the advisor usage completes on
a later line, so most rows would show no cost.

# Notes

`fallback_message` iterations (server-side model fallback) share the `iterations[]` shape and are
billed the same way; only `advisor_message` appears in current logs, so the parser counts that type
and can extend to `fallback_message` when it shows up.
