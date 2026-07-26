---
date: "2026-07-26"
author: "@motoki317"
status: "accepted"
---

# Context

The item view exposed its source record through an `R` toggle.
That key controlled only one trailing section, so inspecting a row required an extra mode change and the compact JSONL source was difficult to scan.

# Decision

An item with a valid record reference always renders Raw as its final section and requests the record asynchronously when the item opens.
The existing loading and unavailability text remains in that section while the read is pending or has failed.

Valid JSON is indented with `encoding/json` using two spaces.
Each structural line is sanitized separately for terminal output, so indentation remains visible and JSON string escapes stay within their value.
Invalid JSON remains one sanitized line.

Remove the `R` binding, help text, key hint, and display state.

# Consequences

Raw context is visible without a mode change and structured records are easier to scan.
Opening any item backed by a record now starts an asynchronous read and adds the complete Raw section to the item viewport.
Prepared terminal-safe lines are cached after the read so later item rebuilds do not repeat JSON formatting.

# Impact

Agent logs remain read-only, and raw reads retain the file-type, digest, and source-change checks.
Pretty-printing changes only JSON whitespace; string contents are not decoded or reformatted.
Non-JSON records retain their prior one-line terminal-safe representation.

# Alternatives

**Keep the `R` toggle.**
This preserves the old interaction but spends a key and a mode change on one section.

**Show the source JSON without indentation.**
This avoids extra formatting work but leaves nested records difficult to inspect.

**Decode and re-encode JSON.**
This could alter number or string representation, while `json.Indent` changes only insignificant whitespace.

# Notes

This decision supersedes [Full-fidelity item view](./_2026-07-25-full-fidelity-item-view.md) only where that record made Raw optional and source-exact in presentation.
Its file integrity, full-fidelity field, and failure-handling decisions remain in force.
