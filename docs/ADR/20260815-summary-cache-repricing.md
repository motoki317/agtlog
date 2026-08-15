---
date: "2026-08-15"
author: "@motoki317"
status: "accepted"
---

# Context

Summary cache fingerprints included a digest of the full LiteLLM pricing table. The runtime pricing
cache refreshes after 24 hours. A price update then moved both adapters to new cache namespaces and
forced every session through summary parsing. Namespace isolation left the old summaries on disk.

Cost is a function of the billed request ledger in `Session.Requests`. The summary cache can retain
that ledger and apply the current pricing policy without parsing the source logs again. This follows
the runtime-attribution pattern in
[cross-session cost deduplication](./20260724-cross-session-cost-dedup.md): cache stable inputs and
recompute derived values after load.

# Decision

Claude uses the parse-only fingerprint `claude-parser-v18`. Codex uses the parse-only fingerprint
`codex-parser-v23`. The Codex fallback model is pricing policy and stays outside its fingerprint.
Any parser or persisted `Session` change that can alter a cached field, its meaning, or repricing
inputs must bump the affected adapter fingerprint. An incompatible cache-envelope change must bump
`cacheVersion`. Adapter fingerprints own each `Session` payload and its parse meaning.
`cacheVersion` owns the outer `cacheEntry` envelope shared by all adapters.

Summary JSON excludes `Session.Cost`, `Session.ModelCosts`, `Session.ModelCostBreakdowns`, and
`RequestUsage.USD`. It retains request identity and usage. Summary parsing leaves `Session.Events`
empty, so summary entries contain no events. Event price fields remain serializable for other model
uses, and detail loading always applies the live calculator.

`internal/cost.Calculator` owns one session repricing implementation. It clears all priced summary
fields, prices `Requests` in stored order, and rebuilds costs and breakdowns. A workflow group is a
synthetic `Session` node with `Group` set and no requests. It processes subagents before the group,
then rolls each child's `ModelCosts` into the group. Pricing `Requests` instead of `Session.Usage`
preserves Codex request-tier boundaries. Child-first traversal preserves Claude group totals.

Every source adapter implements the required `Source.Reprice` method. Discovery calls it
immediately after a summary cache hit, before graph linking and cross-session ownership attribution.
A missing implementation therefore causes a compile error instead of zero prices.

# Consequences

- Price, alias, fallback-model, and tier changes reuse parsed summaries and update displayed costs.
- A stale summary price is structurally absent, including the per-request value used for ownership
  deductions.
- Cached-summary JSON benchmarks measured a 13 to 16 percent decode improvement.
- Each cache hit pays a small repricing cost. Avoiding conditional checks keeps one pricing path for
  fresh parses and cached sessions.
- Parser version discipline becomes mandatory because pricing changes no longer hide a missed
  parser fingerprint bump.

# Impact

The change affects summary serialization, both parser fingerprints, pricing aggregation, and the
cache-hit boundary. It does not alter source logs, event pricing, detail loading, live refresh, or
the pricing-table refresh policy.

Namespace eviction remains separate work. A future eviction pass must preserve namespaces for the
running binary's Claude and Codex parser fingerprints, regardless of `--agent`. It can evict other
namespaces by directory modification time after a grace period. A cache-entry write updates its
namespace directory modification time. A cache read does not, so eviction needs no marker file.

# Alternatives

**Keep priced values and store a pricing fingerprint** was rejected. Conditional repricing on a
fingerprint mismatch can silently retain stale values if a field or comparison path is missed. It
saves about 1.5 ms on the measured warm-discovery workload but creates two cache-load paths.

**Apply repricing after discovery** was rejected because ownership attribution reads
`RequestUsage.USD`. Attribution must see current request prices to avoid a fresh gross cost minus a
stale duplicate deduction.

**Price `Session.Usage`** was rejected because Codex stores per-model aggregates there. Applying a
pricing tier to an aggregate can produce a different charge from the ordered per-request ledger.

# Notes

Namespace eviction, cleanup of pre-namespace root entries, allocation in `Table.Resolve`, and
startup cost in `RuntimeTable` remain outside this decision.
