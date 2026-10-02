---
date: "2026-07-19"
author: "@motoki317"
status: "accepted"
---

# Context

Agent logs contain token usage, but they do not provide one stable, cross-agent cost field. Claude
Code separates ordinary input, cache creation, and cache reads. Codex reports OpenAI-style input in
which cached input is a subset, and its users often run under a subscription rather than direct API
billing. Model rates also change after a binary is released.

agtlog needs a comparable estimate without claiming that a subscription user incurred the displayed
API charge.

# Decision

We price each usage record with the ccusage formula. Rates are in USD per token:

```text
input * inputRate
+ output * outputRate
+ cacheCreate5m * cacheWriteRate
+ cacheCreate1h * (inputRate * 2.0)
+ cacheRead * cacheReadRate
```

If LiteLLM omits cache rates, `cacheWriteRate` defaults to `inputRate * 1.25` and `cacheReadRate`
defaults to `inputRate * 0.1`. If a Claude record contains the structured cache creation object, its
5-minute and 1-hour fields replace the legacy flat cache-creation count. A `costUSD` value in a
Claude record takes precedence over the formula. The Codex parser reads no recorded cost.

LiteLLM lists a higher rate above 200,000 tokens, above 272,000 tokens, or both for some models. We
apply the higher rate as a marginal tier within one usage record. Each token category counts only
its own tokens against the threshold. It uses the base rate up to the threshold and the higher rate
above it. If a category lists both thresholds, the 200,000-token tier applies.

A record whose `speed` is `fast` uses the model's `-fast` entry if one exists and the base entry
otherwise. If the entry in use has a `provider_specific_entry.fast` multiplier, that multiplier
scales the whole result, also for a `-fast` entry. A model without a price costs zero and carries
its name as a missing-pricing flag. The TUI marks that partial value with `!`, and every caller
must check the flag before it treats zero as a real cost.

Every cost that agtlog computes from rates is an **API-equivalent estimate**, not a subscription
charge. A Codex user on ChatGPT or another plan pays according to that plan and normally pays less
than the displayed estimate.

A Codex slug without its own LiteLLM entry gets the rate of a stand-in model. For a `gpt-5` family
slug, agtlog strips a known runtime suffix such as `-sol` and uses the base model's rate. Any other
unknown slug uses the fallback model `gpt-5`, which `cmd/agtlog` passes to the Codex parser. The TUI
prefixes such a cost with `~`, as in `~$4.20`. The `~` marks only a substituted rate: a cost at the
logged model's own published rate has no `~`, although it is also an estimate. The detail view
names both the logged model and the stand-in model.

Each Codex `token_count` event carries a cumulative `total_token_usage` and a per-request
`last_token_usage`. We treat the final `total_token_usage` value as authoritative. We use the
per-request values only if that total is absent, or if their sum across all models equals it
exactly. [Codex counter segments](./20260905-codex-counter-segments.md) applies this rule to each
segment, a run of records between two restarts of the cumulative counter. Codex bills the full
context again on every later turn. A session with hundreds of millions of input tokens is therefore
a real accounting outcome, not a summing bug.

Codex counts `reasoning_output_tokens` inside `output_tokens`. Codex usage sets
`InputIncludesCacheRead`, so the calculator subtracts cached input from input before it applies the
input rate. Claude reports cache reads separately from input and leaves the flag unset.

The binary embeds a LiteLLM snapshot as the release-time floor. At startup, a valid
`$XDG_CACHE_HOME/agtlog/pricing.json` overlays it model by model, so a model that the cached table
omits keeps its embedded price. If the cache is absent, invalid, or older than 24 hours, the TUI
starts a timeout-bounded background fetch. The fetch writes a validated replacement for the next
launch. Prices do not change while agtlog runs.

`--offline` disables the fetch and keeps the embedded and cached tables. As
[Machine-readable CLI](./20260805-machine-readable-cli.md) records, the `list`, `show`, and
`search` subcommands never start the fetch.

`--refresh-prices` is the user-requested exception to deferred refresh. It fetches and validates the
table synchronously with a 30-second timeout and atomically replaces the cache. The run that then
starts uses the fresh overlay. Any failure in this refresh aborts startup. The default path keeps
its background timing and silent failures.

[Summary cache repricing](./20260815-summary-cache-repricing.md) defines how a pricing change
reaches cached session summaries.

# Consequences

The `~` and `!` markers are part of the meaning of a number, not decoration.

# Impact

The estimate cannot answer subscription-plan questions such as quota, marginal charge, or invoice
total. Wherever users can mistake an estimate for money paid, the UI and documentation must state
that costs use public API rates.

# Alternatives

We rejected showing no Codex cost because it prevents comparison across the unified session list.

We first prefixed every Codex cost with `~`. We dropped that rule because an exact published rate is
not approximate. A reader instead needs the logged and applied model names to audit a substitution.

We rejected summing every cumulative event because it counts the same session total many times. We
also rejected always summing per-request values because a context reset can make that sum diverge
from the cumulative counter. In one measured session, the difference was about 1.3 percent.

We rejected using only embedded prices because rates can change between releases. We reconsidered
and rejected dropping the embedded snapshot in favor of runtime downloads. Without network access, a
first run then prices every model at zero with a missing-pricing marker, and `--offline` has no
usable price floor.

We rejected fetching before every startup because it delays the UI by default. `--refresh-prices`
accepts that delay only on request. We rejected replacing the table in a running process because it
changes totals while the user reads them.

# Notes

`just update-pricing` refreshes the embedded release snapshot from LiteLLM by hand. The runtime
cache does not replace review of the embedded data.
