---
date: "2026-07-26"
author: "@motoki317"
status: "accepted"
---

# Context

Codex log payloads are polymorphic. The timeline decoder uses one union struct, but fields with the
same name can have different JSON shapes. For example, `turn_context.summary` is a string, while
`reasoning.summary` is an array. The decoder read `summary` as an array, so a `turn_context` record
with a string `summary` failed to decode, and the decoder dropped it. Every measured timeline usage
row then had no model, and pricing fell back to `gpt-5`.

Codex reports `reasoning_output_tokens` as a detail within `output_tokens`. Among 60,369 measured
`last_token_usage` records, 60,138 satisfied `total_tokens == input_tokens + output_tokens`. The
other 231 had zero input and output tokens and a nonzero `total_tokens`, which agtlog does not
price. Reasoning tokens exceeded output tokens in zero records.

# Decision

The Codex timeline decoder keeps `summary` as raw JSON and decodes it only in a `reasoning` record,
where its shape is known. A `turn_context` record with a string `summary` therefore decodes. The
decoder still drops any record that fails to decode for another reason.

Model context is file-wide state. A sidecar is the rollout file of a Codex subagent. At the
sidecar's bridge record, the loader rolls back the timeline rows that it built before the bridge,
as the [usage ledger](./20260726-codex-usage-ledger.md) describes. The rollback keeps the model
from a `turn_context` record before the bridge.

Timeline usage copies `output_tokens` without adding `reasoning_output_tokens`. The session total
and the timeline therefore both count reasoning tokens as part of output tokens, which matches the
OpenAI Responses API subset semantics.

# Consequences

Each timeline row records the active model in `Event.Model`, and the detail view shows that model.
The [usage ledger](./20260726-codex-usage-ledger.md) prices each usage row with the model that
`Parse` stores in its ledger entry.

The fix covers one polymorphic field. A newly found field with more than one shape needs the same
raw treatment, or its records fail to decode.

# Alternatives

We first kept a partially populated record when decoding returned a type error. We replaced it
because neither `encoding/json` nor the jsoniter decoder that agtlog uses guarantees that fields
after the mismatched one are filled. Whether the model survived depended on where `summary` sat in
the record.

We rejected separate structs for every payload variant. They avoid the shape conflict, but they
duplicate the envelope and dispatch logic.

We rejected adding the reasoning detail to output because the measured records show that it is a
subset, not a disjoint token count.
