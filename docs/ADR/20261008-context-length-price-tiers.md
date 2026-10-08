---
date: "2026-10-08"
author: "@motoki317"
status: "accepted"
---

# Context

A rate card holds a model's per-token rates for input, output, cache writes, and cache reads. Two
providers bill a whole request at a higher rate card once its prompt passes a token threshold:

- Anthropic, Claude Haiku 5.5: "a prompt of over 100,000 tokens pays higher prices". The
  [pricing page](https://platform.claude.com/docs/en/about-claude/pricing) lists higher prices for
  input, both cache writes, cache reads, and output.
- OpenAI, the long-context gpt-5.4, gpt-5.5, gpt-5.6, and gpt-6 models: "Prompts with >272K input
  tokens are priced at 2x input and 1.5x output for the full request"
  ([gpt-5.6](https://developers.openai.com/api/docs/models/gpt-5.6)). The GPT-6 pages, such as
  [gpt-6.1-sol](https://developers.openai.com/api/docs/models/gpt-6.1-sol), also double the cache
  rates.

LiteLLM stores the base card under keys such as `input_cost_per_token`, and each higher card as a
tier: the same keys with `_above_<N>_tokens` appended, where N is the threshold, such as `100k`. Its
cost function,
[`_get_token_base_cost`](https://github.com/BerriAI/litellm/blob/33d908e0ae2c0a257eeb5d546df08527d348a670/litellm/litellm_core_utils/llm_cost_calc/utils.py#L625),
takes the highest threshold that the prompt exceeds and uses that tier for every token type. For
Anthropic,
[`AnthropicConfig.calculate_usage`](https://github.com/BerriAI/litellm/blob/33d908e0ae2c0a257eeb5d546df08527d348a670/litellm/llms/anthropic/chat/transformation.py#L2431-L2473)
adds cache writes and cache reads to the input tokens to form the prompt. LiteLLM applies the same
whole-request rule to every tier it lists.

[Cost model](./20260719-cost-model.md) applied a marginal tier instead. Each token category counted
only its own tokens against the threshold, and only the tokens above it used the higher rate. One
measured Haiku 5.5 request had a 105,516-token prompt: 92,541 cache-read tokens, 12,973 1-hour
cache-write tokens, and 2 input tokens. It also produced 269 output tokens, which are not part of
the prompt. No category alone passes 100,000, so the marginal rule leaves the request on the base
card: $0.00365 instead of $0.01827.

# Decision

The prompt size of a request selects at most one tier. Each rate that the tier lists replaces the
base rate for every token of the request. Every other category keeps its base rate, including a
cache rate that the cost model derives from the base input. A 1-hour cache write costs 2 times the
selected input rate. This replaces the marginal-tier paragraph of
[Cost model](./20260719-cost-model.md), and the rest of that record stands.

The selected tier has the highest N that the prompt size strictly exceeds. `Usage.PromptTokens()`
computes the prompt size from input, cache writes, and cache reads. LiteLLM compares with ≥ for
xAI models instead. We keep > for every model because one comparison is simpler: agtlog reads only
Claude Code and Codex logs, and the two rules differ only for a prompt of exactly N tokens.

A tier for threshold N exists when `input_cost_per_token_above_<N>_tokens` holds a number, where N
is digits with an optional `k` for thousands. A null input rate counts as absent. The tier's output,
cache-write, and cache-read rates come only from keys with the input key's exact spelling of N, as
LiteLLM looks them up. We ignore every key with a further suffix after `_tokens`, such as
`_priority`, `_flex`, `_batches`, or `_ultrafast`, because those price batch processing or another
service tier. We also ignore the `_above_1hr` keys.

A tier rate that is present must be a valid rate, like a base rate. Two spellings of one threshold,
such as `100k` and `100000`, also make an entry invalid. A downloaded table with an invalid entry is
rejected whole: the background refresh keeps the previous cache, and `--refresh-prices` aborts
startup.

A Codex aggregate ledger entry (`RequestUsage.Offset < 0`) stays on the base card. Such an entry
sums the requests of a [counter segment](./20260905-codex-counter-segments.md) whose request usage
does not add up to its cumulative total.

`AboveThreshold` marks every breakdown bucket of a request priced at a tier, including a bucket that
keeps a base rate. `CostBuckets.Add` lists marked buckets after unmarked ones.

# Consequences

A Haiku 5.5 request whose prompt passes 100K tokens pays the over-100K card, 5 times the base card,
for every token. A Codex request above 272K pays the tier rates on all of its tokens, not only on
the input above 272K. A Codex aggregate entry that the old rule priced partly at a higher rate can
only get cheaper.

# Impact

The Anthropic page says "prompt" without naming cache tokens. Counting them rests on two facts: the
over-100K card lists cache prices, and LiteLLM counts them. If billing evidence contradicts that,
change the quantity that `Calculator.card` passes to `Pricing.tier`. `PromptTokens()` must keep
counting cache tokens, because it also defines the CLI's `usage.context`.

An aggregate can hide a request above 272K, which the base card then underprices. Codex sessions
with a context window above 272K can hold such requests.

# Alternatives

We rejected the per-category marginal tier because neither provider bills that way.

We rejected tiering aggregates because a sum of requests can pass a threshold that no request
reached. The result bills a card that the provider never charged.

We rejected a deterministic tie-break between two spellings of one threshold. It is no shorter than
rejecting the entry, and it would still differ from LiteLLM, which picks whichever spelling it
stores first.

We rejected honoring the `_above_1hr` keys because they repeat the 1-hour rule. Anthropic lists a
1-hour cache write at 2 times the uncached input price. In the 2026-10-08 LiteLLM snapshot, every
such key equals twice the input rate it pairs with. A test checks this on the embedded snapshot. If
a refresh breaks it, read the keys instead.
