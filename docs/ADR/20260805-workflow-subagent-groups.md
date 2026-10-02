---
date: "2026-08-05"
author: "@motoki317"
status: "accepted"
---

# Context

Claude Code writes a Task or Agent subagent transcript to `<session>/subagents/agent-<id>.jsonl`.
It writes each agent that a Workflow run spawns one level deeper, to
`<session>/subagents/workflows/<runId>/agent-<id>.jsonl`. The run directory also holds
`journal.jsonl`, which records run bookkeeping and is not a transcript.

agtlog read only the first level. In the 2026-08-05 corpus, one workflow run of 32 agents was
missing from every total. That run was the whole difference between the agtlog and ccusage grand
totals.

Every agent in a run receives the same prompt preamble. In the measured run, the first 21 lines
were identical, so a title from the first prompt line gave all 32 agents the same title.

# Decision

We attach one group node per workflow run to the parent session, with the run's agents as its
children. The group mirrors the on-disk layout and gives the run one row for its name and its
rolled-up cost. The existing ref builder gives each agent a nested ref such as
`claude:<session>#<runId>/<agentId>`.

Discovery walks the whole `subagents` subtree instead of a fixed depth. A file directly in
`subagents/` is a direct child, unless its `agent-<id>.meta.json` names a `parentAgentId`. Since
2026-08-14, such a file nests under that subagent. A deeper file must match `agent-*.jsonl`, and it
joins the group of its parent directory. This rule excludes `journal.jsonl`, and it also covers a
future layout that nests at another depth.

A group is a container with no usage of its own. Subagent counts skip groups, so a run of 32 agents
counts 32. A group takes its start time from its earliest child and its update time from its latest
child. It reports the union of its children's models.

The parent's `Workflow` tool call becomes a subagent event. Its tool result carries `runId` and
`workflowName`. We bind the event to the group by `runId` and use `workflowName` as the group
title. Claude Code writes the result at launch, before the transcripts exist, so the event can have
no group yet. The timeline then marks the subagent as unavailable.

An Agent or Task call binds first to the subagent whose `agent-<id>.meta.json` names the call as
`toolUseId`. A subagent that a sidecar assigns this way never binds to another call. Any other
subagent can bind by name, or through the fallback that takes the first unlinked subagent. That
fallback skips groups, so an unrelated Task call cannot bind to a run.

If sibling titles collide, each sibling takes the first prompt line that no other sibling has. In
the measured run, this rule gave 32 distinct titles for 32 agents. The rule also applies to Task
and Agent siblings, but only to a sibling set with a collision, so a unique title keeps its
first-line form. A recorded `ai-title` always wins. The parser keeps each child's prompt only while
it parses the parent, with a byte cap and a line cap. Past either cap, the child keeps its
first-line title.

# Alternatives

We rejected adding the agents as direct children of the session. The run would have no row for
its name or its rolled-up cost, and 32 siblings with one shared title are unreadable.

We rejected the workflow `label` as a title source. On 2026-08-05, `agent-*.meta.json` held only
`agentType` and `spawnDepth`, and `journal.jsonl` held only `started` and `result` records. Neither
file carried the label.

We rejected a title rule that drops the prefix that all siblings share. In the measured run, about
20 of 32 titles stayed identical, because the agents of one phase also share a section header.

We made the run legible in the Subagents tab, where it is one row, and added no filter to the
detail timeline, where the workflow row was event 1,627 of 5,163 in the measured session. The
[Overview tab](./20261001-overview-tab.md) later replaced the Subagents tab and keeps the subagent
table.

# Notes

We deferred two questions from the same investigation on 2026-08-05. The first is an estimated cost
for `/compact`. The second is four resumed-session pairs in which agtlog and ccusage assign a
transcript to different parents. The pairs cancel exactly and change no total.
