---
date: "2026-07-24"
author: "@motoki317"
status: "accepted"
---

# Context

The Anthropic Advisor tool runs a second model server-side inside one Messages request. The response
reports its usage in `usage.iterations[]` as an entry with `type: "advisor_message"` and its own
`model`. Per the Messages API docs, Anthropic bills that entry at the advisor model's rates. The
top-level `usage` fields deliberately exclude it "because they are billed at a different rate". The
billed total for a request is therefore the top-level usage plus every advisor iteration.

Before this decision, agtlog read only the top-level `usage`. Every advisor turn had no cost, and
the timeline dropped its `server_tool_use` block with the other server tool blocks.

# Decision

We count each `advisor_message` iteration as its own usage, priced at the iteration's model.

The summary parser appends one usage record per counted advisor iteration. It skips an
`advisor_message` entry without a model, with a negative token count, or with no tokens. The
record's synthetic message ID combines the message ID with the iteration's position among the
counted iterations of that line, not its position in `iterations[]`. Both the per-file
deduplication and the [cross-session owner rule](./20260724-cross-session-cost-dedup.md) key on the
message ID and request ID. Each therefore counts the iteration once across re-logged or replayed
lines. The session total and the per-model costs include it.

The timeline shows each advisor `server_tool_use` block as an `EventAdvisor` row and still drops
every other `server_tool_use` block. Claude Code logs the block when the call opens, and the advisor
usage completes on a later line. The loader therefore collects advisor usage per message across the
whole file and attaches it after the scan. The Nth advisor block of a message gets the Nth counted
iteration.

# Consequences

The per-model cost breakdown keys on the iteration's model. An advisor on the executor's model joins
that model's line. A different advisor model gets its own line, for example an Opus advisor beside a
Haiku executor.

# Alternatives

We rejected trusting the top-level `usage` alone because it excludes billed advisor tokens. An
earlier reading mistook the advisor's uncached re-read of the transcript for a double-counted copy.
The primary source and a controlled `ccusage` experiment refuted that reading.

We rejected folding advisor cost into the executor row and model because it hides where advisor
spend happens and misprices any cross-tier pairing.

We rejected attaching advisor usage on the block's own line because the usage completes on a later
line, so most rows would show no cost.

# Notes

`fallback_message` iterations, which record a server-side model fallback, share the `iterations[]`
shape and are billed the same way. Only `advisor_message` appeared in the measured logs, so the
parser counts only that type. It can add `fallback_message` when that type appears.
