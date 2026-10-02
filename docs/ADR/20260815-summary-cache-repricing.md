---
date: "2026-08-15"
author: "@motoki317"
status: "accepted"
---

# Context

Each adapter's `CacheFingerprint()` selects its summary cache namespace. Before this decision, it
included a digest of the full LiteLLM pricing table, and the runtime pricing cache refreshes after
24 hours. Each price update therefore moved both adapters to new cache namespaces and sent every
session through summary parsing again. Namespace isolation left the old summaries on disk.

Cost is a function of the billed request ledger in `Session.Requests`. The summary cache can keep
that ledger and apply the current pricing policy without parsing the source logs again. This follows
the runtime-attribution pattern in
[cross-session cost deduplication](./20260724-cross-session-cost-dedup.md): cache stable inputs and
recompute derived values after load.

# Decision

Each adapter's `CacheFingerprint()` covers parsing only. The Codex fallback model is pricing policy
and stays outside it. Any parser or persisted `Session` change that can alter a cached field, its
meaning, or a repricing input must bump the affected adapter's `CacheFingerprint()`. Repricing
reads `Requests`, `Subagents`, and `Group`. `cacheVersion` owns the outer `cacheEntry` envelope
that all adapters share, and an incompatible envelope change must bump it.

Summary JSON excludes every priced field, including the per-request `RequestUsage.USD`, and keeps
request identity and usage. Summary parsing leaves `Session.Events` empty, so summary entries
contain no events. `Event` price fields other than `PricingModel` keep their JSON encoding, but no
summary entry stores them. Detail loading always prices events with the live calculator.

`internal/cost.Calculator` owns the one session repricing implementation. It clears all priced
summary fields, prices `Requests` in stored order, and rebuilds costs and breakdowns. It prices
subagents before their parent. A [workflow group](./20260805-workflow-subagent-groups.md) is a
synthetic `Session` node with `Group` set and no requests, so it takes each child's `ModelCosts`
after the children are priced. Child-first traversal keeps Claude group totals correct.

Pricing `Requests` instead of `Session.Usage` keeps Codex request-tier boundaries. The exception is
a Codex [counter segment](./20260905-codex-counter-segments.md) whose request usage does not
reconcile with its cumulative total. Its `Requests` entries are aggregates.

Every source adapter implements the required `Source.Reprice` method. Discovery calls it immediately
after a summary cache hit, before graph linking and cross-session ownership attribution. A missing
implementation is therefore a compile error, not zero prices.

# Consequences

Each cache hit pays a small repricing cost, and fresh parses and cached sessions share one pricing
path. Cached-summary JSON benchmarks measured a 13 to 16 percent decode improvement.

A pricing change no longer invalidates the cache. Before this decision, each price update forced a
reparse that also hid a missed `CacheFingerprint()` bump. Now a missed bump keeps summaries from the
old parser until their log files change.

# Impact

Namespace eviction and cleanup of root entries from before namespaces stay outside this decision.
Two pricing costs also stay outside it: allocation in `Table.Resolve`, and startup cost in
`RuntimeTable`. Repricing calls `Table.Resolve` for every request, and `RuntimeTable` decodes the
embedded LiteLLM table on every launch.

A future eviction pass must keep the namespaces for the running binary's Claude and Codex
`CacheFingerprint()` values, regardless of `--agent`. It can evict other namespaces by directory
modification time after a grace period. A cache-entry write updates the modification time of its
namespace directory, and a cache read does not, so eviction needs no marker file.

# Alternatives

We rejected keeping priced values with a stored pricing fingerprint. Conditional repricing on a
fingerprint mismatch can silently keep stale values if one field or comparison path is missed. It
saves about 1.5 ms on the measured warm-discovery workload but creates two cache-load paths.

We rejected repricing after discovery because ownership attribution reads `RequestUsage.USD`.
Attribution must see current request prices, or it subtracts a stale duplicate deduction from a
fresh gross cost.

We rejected pricing `Session.Usage` because Codex stores per-model aggregates there. A pricing tier
applied to an aggregate can produce a different charge from the ordered per-request ledger.
