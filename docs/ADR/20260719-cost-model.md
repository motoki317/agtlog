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

For a usage record, agtlog follows the ccusage formula with rates in USD per token:

```text
input * inputRate
+ output * outputRate
+ cacheCreate5m * cacheWriteRate
+ cacheCreate1h * (inputRate * 2.0)
+ cacheRead * cacheReadRate
```

If LiteLLM omits cache rates, `cacheWriteRate` defaults to `inputRate * 1.25` and
`cacheReadRate` defaults to `inputRate * 0.1`. When a Claude record contains the structured cache
creation object, its 5-minute and 1-hour fields replace the legacy flat cache-creation count. A
recorded `costUSD` takes precedence when present.

The 200,000-token and 272,000-token rates are marginal tiers: tokens through the threshold use the
base rate and only tokens above it use the higher rate. A `fast` record looks up the model with a
`-fast` suffix where available and multiplies the whole result by the provider's fast multiplier.
Missing pricing produces a zero cost and carries the unresolved model name as a flag. The TUI marks
that partial value with `!`.

Codex cost is an **API-equivalent estimate**. The TUI prefixes it with `~`, as in `~$4.20`. This is
not a subscription charge: users on ChatGPT or another plan pay according to that plan and normally
pay less than the displayed API-rate estimate. agtlog maps the private `gpt-5.6-sol` runtime slug to
the public `gpt-5.6` price and uses `gpt-5` as the configured fallback for other unknown Codex
slugs. Every Codex result remains estimated after mapping.

Codex `token_count` events are cumulative. The adapter treats the last
`total_token_usage` as the authoritative session total and uses summed `last_token_usage` deltas
only when the cumulative total is absent or when the per-model deltas reconcile exactly. Codex
re-bills the full context on later turns, so sessions with hundreds of millions of input tokens are
real accounting outcomes, not a summing bug. In one measured session, sum-of-deltas differed from
the last cumulative value by about 1.3 percent after a mid-session context reset; the cumulative
value remains authoritative.

Codex folds `reasoning_output_tokens` into output. Its normalized usage sets
`InputIncludesCacheRead`, causing cached input to be subtracted from ordinary input before the cache
read rate is applied. Claude leaves this flag unset because its cache-read tokens are separate from
ordinary input.

The binary embeds a LiteLLM snapshot as the release-time floor. At startup, a valid
`$XDG_CACHE_HOME/agtlog/pricing.json` overlays it model by model. When the cache is absent, invalid,
or older than 24 hours, agtlog starts a timeout-bounded background fetch and writes a validated
replacement for the next launch. It does not change prices during a running session. `--offline`
disables the fetch while retaining the embedded and cached tables.

`--refresh-prices` is the user-requested exception to deferred refresh. It fetches and validates the
table synchronously with a 30-second timeout, atomically replaces the cache, and applies the fresh
overlay to the session that then starts. Any refresh-stage failure aborts startup. The default path
keeps its background timing and silent-failure behavior; only an explicit request delays the UI.

# Consequences

- Claude and Codex totals are comparable at public API rates, but only Claude values can represent
  direct recorded cost when the log supplies it.
- The `~` and `!` markers are part of the meaning of a number, not decoration.
- A released binary gains new prices after one successful background refresh and a later launch.
- Existing embedded models remain available when a runtime table omits them.
- Cost can be zero for an unpriced model; callers must inspect the missing-pricing flag.

# Impact

Changing token normalization, model aliases, thresholds, or the overlay table invalidates cached
session summaries because the calculator fingerprint changes. Tests cover the formula, marginal
tiers, defaults, fast mode, Codex cache semantics, missing prices, overlay precedence, freshness,
and offline behavior.

The estimate cannot answer subscription-plan questions such as quota, marginal charge, or invoice
total. The UI and documentation must keep the `~` gloss visible wherever users could mistake the
estimate for money paid.

# Alternatives

**Show no Codex cost** was rejected because it would prevent comparison across the unified session
list. The estimate is useful when its API-rate meaning is explicit.

**Sum every cumulative event** was rejected because it would repeatedly count the same session
total. **Always sum per-turn deltas** was also rejected because context resets can make that sum
diverge from Codex's authoritative cumulative counter.

**Use only embedded prices** was rejected because rates can change between releases. **Drop the
embedded snapshot and rely on runtime downloads** was reconsidered and rejected because a first run
without network access would price every model at zero with a missing-pricing marker, and
`--offline` would no longer retain a usable price floor.

**Fetch before every startup** was rejected because it would delay the UI by default.
`--refresh-prices` accepts that delay only when the user requests it. **Hot-swap a running table**
was rejected because it would change totals during a session.

# Notes

`just update-pricing` manually refreshes the embedded release snapshot from LiteLLM. Runtime caching
complements that manual release process; it does not replace review of the embedded data. A
scheduled refresh workflow remains future work.
