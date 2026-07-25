---
date: "2026-07-26"
author: "@motoki317"
status: "accepted"
---

# Context

Codex log payloads are polymorphic. The timeline decoder uses one union struct, but fields with the
same name can have different JSON shapes: `turn_context.summary` is a string while
`reasoning.summary` is an array. `encoding/json` returns an `*json.UnmarshalTypeError` for the
mismatched field after populating independent fields such as the model. Dropping the whole record
left every measured timeline usage row without a model, so pricing fell back to `gpt-5`.

Codex also reports `reasoning_output_tokens` as a detail within `output_tokens`. Among 60,369
measured `last_token_usage` records, 60,138 satisfied
`total_tokens == input_tokens + output_tokens`. The other 231 had zero input and output and are
already rejected. Reasoning tokens exceeded output tokens in zero records.

The combined corrections produced this measured comparison across 711 priced sessions from 725
local logs:

| | sessions matching within 0.5% | aggregate row sum / Info total |
| --- | ---: | ---: |
| before | 0 / 711 (0.0%) | **0.2872** |
| after | 695 / 711 (97.7%) | **0.9938** |

# Decision

The Codex timeline decoder keeps a partially populated record when decoding returns
`*json.UnmarshalTypeError`; it still drops records for every other JSON error. Model context is
file-wide state, so it is tracked before the subagent active-window gate.

Timeline usage copies `output_tokens` without adding `reasoning_output_tokens`. The session-total
and timeline paths therefore share the OpenAI Responses API subset semantics.

# Consequences

Timeline rows retain the active model and use its public pricing alias. Subagent rows keep the model
even when `turn_context` precedes their bridge record. Reasoning output is no longer double-counted.

A type-mismatched field remains unavailable, but independent decoded fields can still drive an
existing event case.

# Impact

The tolerance applies only to the Codex timeline union decoder. Summary parsing and Claude parsing
remain strict. Replay-prefix filtering and empty attribution windows remain separate work.

# Alternatives

Separate structs for every payload variant would avoid partial decoding but duplicate the envelope
and dispatch logic. Changing `summary` to an untyped field would fix one collision while leaving
other polymorphic fields vulnerable.

Adding reasoning detail to output was rejected because the measured records establish that it is a
subset, not a disjoint token count.

# Notes

The remaining 16 mismatched sessions differ in token attribution rather than pricing semantics.
