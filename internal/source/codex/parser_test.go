package codex

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
)

func testParser() Parser {
	cacheRead := 0.5
	return NewParser(cost.NewCalculator(cost.Table{
		"gpt-5.6": {Input: 2, Output: 3, CacheRead: &cacheRead},
		"gpt-5.4": {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5":   {Input: 1, Output: 1, CacheRead: &cacheRead},
	}), "gpt-5")
}

func fixture(name string) string {
	return filepath.Join("testdata", "sessions", "2026", "01", "02", name)
}

func TestParseUsesLastCumulativeTokenCount(t *testing.T) {
	session, err := testParser().Parse(fixture("rollout-session-main.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := model.Usage{
		Model:                  "gpt-5.6-sol",
		InputTokens:            250,
		OutputTokens:           40,
		CacheReadTokens:        50,
		InputIncludesCacheRead: true,
	}
	if len(session.Usage) != 1 || !reflect.DeepEqual(session.Usage[0], want) {
		t.Fatalf("Parse().Usage = %#v, want %#v", session.Usage, want)
	}
}

func TestParseSumsLastUsageWhenCumulativeTotalMissing(t *testing.T) {
	session, err := testParser().Parse(fixture("rollout-deltas-only.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := model.Usage{
		Model:                  "gpt-5.4",
		InputTokens:            30,
		OutputTokens:           10,
		CacheReadTokens:        5,
		InputIncludesCacheRead: true,
	}
	if len(session.Usage) != 1 || !reflect.DeepEqual(session.Usage[0], want) {
		t.Fatalf("Parse().Usage = %#v, want %#v", session.Usage, want)
	}
}

func TestParseBuildsUnifiedSessionMetadata(t *testing.T) {
	session, err := testParser().Parse(fixture("rollout-session-main.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 3, 6, 0, 0, time.UTC)
	if session.ID != "session-main" || session.CWD != "/workspace/starship" || session.Project != "starship" {
		t.Errorf("Parse() identity = ID %q, CWD %q, project %q", session.ID, session.CWD, session.Project)
	}
	if session.Title != "Design a rover" || session.GitBranch != "orbit/alpha" {
		t.Errorf("Parse() label = title %q, branch %q", session.Title, session.GitBranch)
	}
	if !reflect.DeepEqual(session.Models, []string{"gpt-5.6-sol"}) || session.Messages != 1 {
		t.Errorf("Parse() models/messages = %v, %d", session.Models, session.Messages)
	}
	if !session.StartedAt.Equal(started) || !session.UpdatedAt.Equal(updated) {
		t.Errorf("Parse() timestamps = started %v, updated %v", session.StartedAt, session.UpdatedAt)
	}
}

func TestParseDerivesTitleFromMeaningfulBriefLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-delegated.jsonl")
	message := strings.Join([]string{
		"<instructions>",
		"You are an autonomous implementation agent from a different model family.",
		"Deliver the complete implementation and work non-interactively.",
		"</instructions>",
		"# Brief — Add lunar telemetry dashboard",
	}, "\n")
	line := `{"timestamp":"2026-01-02T03:04:15Z","type":"event_msg","payload":{"type":"user_message","message":` + strconv.Quote(message) + `}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if session.Title != "Add lunar telemetry dashboard" {
		t.Fatalf("Parse().Title = %q, want meaningful brief title", session.Title)
	}
}

func TestParseCleansAndCapsFallbackTitle(t *testing.T) {
	message := "<task>\n" + strings.Repeat("L", 120) + "\nMore detail"
	want := strings.Repeat("L", 95) + "…"
	if got := titleFromUserMessage(message); got != want {
		t.Fatalf("titleFromUserMessage() = %q, want %q", got, want)
	}
}

func TestTitleUsesTaskHeadingAfterDelegationPreamble(t *testing.T) {
	message := strings.Join([]string{
		"You are an autonomous implementation agent from a different model family.",
		"Deliver the complete implementation and work non-interactively.",
		"---",
		"# Implement: stabilize lunar telemetry",
	}, "\n")
	if got := titleFromUserMessage(message); got != "Implement: stabilize lunar telemetry" {
		t.Fatalf("titleFromUserMessage() = %q, want task heading", got)
	}
}

func TestTitlePrefersBriefOverUnrelatedPreambleHeadings(t *testing.T) {
	message := strings.Join([]string{
		"<recommended_plugins>",
		"# Available plugins",
		"- Fictional Drive",
		"</recommended_plugins>",
		"# AGENTS.md instructions for /workspace/telemetry",
		"Before the first substantive task, read the house rules.",
		"# Brief — Stabilize lunar telemetry",
	}, "\n")
	if got := titleFromUserMessage(message); got != "Stabilize lunar telemetry" {
		t.Fatalf("titleFromUserMessage() = %q, want brief over preamble", got)
	}
}

func TestTitleSkipsPlainDelegationBoilerplate(t *testing.T) {
	message := strings.Join([]string{
		"You are an autonomous implementation agent from a different model family.",
		"Deliver the complete implementation and work non-interactively.",
		"Stabilize lunar telemetry without losing samples.",
	}, "\n")
	if got := titleFromUserMessage(message); got != "Stabilize lunar telemetry without losing samples." {
		t.Fatalf("titleFromUserMessage() = %q, want first meaningful task line", got)
	}
}

func TestParseCalculatesEstimatedCodexCost(t *testing.T) {
	session, err := testParser().Parse(fixture("rollout-session-main.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := 545.0
	if math.Abs(session.Cost.USD-want) > 1e-12 || !session.Cost.Estimated {
		t.Fatalf("Parse().Cost = %#v, want USD %v estimated", session.Cost, want)
	}
}

func TestParseBuildsInlineSubagentTree(t *testing.T) {
	session, err := testParser().Parse(fixture("rollout-session-main.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(session.Subagents) != 1 {
		t.Fatalf("Parse().Subagents = %#v, want one", session.Subagents)
	}
	subagent := session.Subagents[0]
	if subagent.ID != "thread-scout" || subagent.Title != "scout" || subagent.Agent != model.AgentCodex {
		t.Errorf("subagent = %#v, want Codex thread-scout/scout", subagent)
	}
	if len(subagent.Subagents) != 1 || subagent.Subagents[0].ID != "thread-mapper" {
		t.Errorf("nested subagents = %#v, want thread-mapper under scout", subagent.Subagents)
	}
	if math.Abs(session.TotalCost().USD-session.Cost.USD) > 1e-12 {
		t.Errorf("inline subagent double-counted cumulative session cost")
	}
}

func TestParseMarksEmptySessionCostEstimated(t *testing.T) {
	session, err := testParser().Parse(filepath.Join("testdata", "rollout-no-usage.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !session.Cost.Estimated || session.Cost.USD != 0 {
		t.Fatalf("Parse().Cost = %#v, want estimated zero", session.Cost)
	}
}

func TestParseAttributesPerTurnUsageToActiveModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-model-switch.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"gpt-5.4"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10},"last_token_usage":{"input_tokens":10}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-01-02T03:07:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30},"last_token_usage":{"input_tokens":20}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []model.Usage{
		{Model: "gpt-5.4", InputTokens: 10, InputIncludesCacheRead: true},
		{Model: "gpt-5.6-sol", InputTokens: 20, InputIncludesCacheRead: true},
	}
	if !reflect.DeepEqual(session.Usage, want) {
		t.Fatalf("Parse().Usage = %#v, want %#v", session.Usage, want)
	}
	if session.Cost.USD != 50 || !session.Cost.Estimated {
		t.Fatalf("Parse().Cost = %#v, want USD 50 estimated", session.Cost)
	}
}

func TestParseRejectsExcessivelyDeepSubagentPath(t *testing.T) {
	root := &model.Session{}
	agentPath := "root/" + strings.Repeat("child/", 1000)

	addSubagent(root, "rollout.jsonl", agentPath, "thread-deep", time.Time{})
	if len(root.Subagents) != 0 {
		t.Fatalf("addSubagent() created %d children for excessive path", len(root.Subagents))
	}
}

func TestParseSkipsOversizedRecordAndContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-oversized.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"turn_context","payload":{"model":"gpt-5.4"}}` + "\n",
		`{"timestamp":"2026-01-02T03:00:30Z","type":"response_item","payload":{"message":"` + strings.Repeat("x", 17*1024*1024) + `"}}` + "\n",
		`{"timestamp":"2026-01-02T03:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":3}}}}` + "\n",
	}
	for _, line := range lines {
		if _, err := file.WriteString(line); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Usage) != 1 || session.Usage[0].InputTokens != 3 {
		t.Fatalf("Parse().Usage = %#v, want later cumulative usage", session.Usage)
	}
}

func TestParsePrefersCumulativeTotalWhenDeltasAreIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-incomplete-deltas.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"gpt-5.4"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10},"last_token_usage":{"input_tokens":10}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Usage) != 1 || session.Usage[0].InputTokens != 30 {
		t.Fatalf("Parse().Usage = %#v, want final cumulative 30", session.Usage)
	}
}

func TestParseSkipsNegativeTokenCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-negative.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"gpt-5.4"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Usage) != 1 || session.Usage[0].InputTokens != 3 {
		t.Fatalf("Parse().Usage = %#v, want only valid token count", session.Usage)
	}
}

func TestParseSkipsOverflowingOutputCombination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-overflow.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"gpt-5.4"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":9223372036854775807,"reasoning_output_tokens":1}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Usage) != 0 {
		t.Fatalf("Parse().Usage = %#v, want overflowing record skipped", session.Usage)
	}
}

func TestLoadEventsBuildsLinkedCodexTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-detail.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"event_msg","payload":{"type":"user_message","message":"Survey the crater"}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Check the terrain"}]}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"response_item","payload":{"type":"function_call","call_id":"call-survey","name":"exec_command","arguments":"{\"cmd\":\"survey --ridge\"}"}}`,
		`{"timestamp":"2026-01-02T03:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-survey","output":"Process exited with code 0\nFinal output:\nroute clear"}}`,
		`{"timestamp":"2026-01-02T03:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The route is clear."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	wantKinds := []model.EventKind{model.EventUser, model.EventThinking, model.EventToolCall, model.EventToolResult, model.EventAssistantText}
	gotKinds := make([]model.EventKind, 0, len(session.Events))
	for _, event := range session.Events {
		gotKinds = append(gotKinds, event.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("LoadEvents() kinds = %v, want %v", gotKinds, wantKinds)
	}
	call := session.Events[2]
	if call.ToolName != "exec_command" || call.ToolInput != "survey --ridge" || call.ResultSummary != "exit 0" || call.Duration != 3*time.Second {
		t.Fatalf("linked tool call = %#v", call)
	}
}

func TestLoadEventsCoalescesMirroredMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-mirrored.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"event_msg","payload":{"type":"user_message","message":"Survey the crater"}}`,
		`{"timestamp":"2026-01-02T03:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Survey the crater"}]}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":"The route is clear."}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The route is clear."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 2 || session.Events[0].Kind != model.EventUser || session.Events[1].Kind != model.EventAssistantText {
		t.Fatalf("mirrored events = %#v, want one user and one assistant event", session.Events)
	}
}

func TestLoadEventsNestsInlineSubagentsAtSpawn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-subagents.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:01Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-scout","agent_path":"/root/scout","kind":"started"}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-mapper","agent_path":"/root/scout/mapper","kind":"started"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mapper := &model.Session{ID: "thread-mapper", Title: "mapper", Agent: model.AgentCodex}
	scout := &model.Session{ID: "thread-scout", Title: "scout", Agent: model.AgentCodex, Subagents: []*model.Session{mapper}}
	session := &model.Session{Path: path, Agent: model.AgentCodex, Subagents: []*model.Session{scout}}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	if len(session.Events) != 1 || session.Events[0].Kind != model.EventSubagent || session.Events[0].Subagent != scout {
		t.Fatalf("root events = %#v", session.Events)
	}
	if len(scout.Events) != 1 || scout.Events[0].Kind != model.EventSubagent || scout.Events[0].Subagent != mapper {
		t.Fatalf("scout events = %#v", scout.Events)
	}
}

func TestLoadEventsPopulatesCodexSubagentFromItsRollout(t *testing.T) {
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "rollout-thread-root.jsonl")
	childPath := filepath.Join(dir, "rollout-thread-scout.jsonl")
	root := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"session_meta","payload":{"id":"thread-root","session_id":"thread-root","cwd":"/workspace/starship"}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-scout","agent_path":"/root/scout","kind":"started"}}`,
	}, "\n") + "\n"
	child := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"session_meta","payload":{"id":"thread-scout","session_id":"thread-root","thread_source":"subagent","agent_path":"/root/scout"}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-scout","agent_path":"/root/scout","kind":"started"}}`,
		`{"timestamp":"2026-01-02T03:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Inherited root prompt"}]}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"inter_agent_communication_metadata","payload":{"trigger_turn":true}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: /root/scout\nSender: /root\nPayload:\nInspect the ridge"}]}}`,
		`{"timestamp":"2026-01-02T03:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Choose a route"}]}}`,
		`{"timestamp":"2026-01-02T03:00:04Z","type":"response_item","payload":{"type":"function_call","call_id":"call-read","name":"Read","arguments":"{\"path\":\"/workspace/starship/map.go\"}"}}`,
		`{"timestamp":"2026-01-02T03:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-read","output":"map ready"}}`,
		`{"timestamp":"2026-01-02T03:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The ridge is clear."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(rootPath, []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	parsedChild, err := testParser().Parse(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsedChild.Subagents) != 0 {
		t.Fatalf("child summary nested its inherited self: %#v", parsedChild.Subagents)
	}
	if parsedChild.Title != "scout" {
		t.Fatalf("child title = %q, want agent path label", parsedChild.Title)
	}
	session, err := testParser().Parse(rootPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 || session.Events[0].Kind != model.EventSubagent {
		t.Fatalf("root events = %#v, want subagent spawn", session.Events)
	}
	scout := session.Events[0].Subagent
	if scout == nil || scout.Path != childPath {
		t.Fatalf("spawn subagent = %#v, want rollout %q", scout, childPath)
	}
	wantKinds := []model.EventKind{model.EventUser, model.EventThinking, model.EventToolCall, model.EventToolResult, model.EventAssistantText}
	gotKinds := make([]model.EventKind, 0, len(scout.Events))
	for _, event := range scout.Events {
		gotKinds = append(gotKinds, event.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) || scout.Events[0].Text != "Inspect the ridge" {
		t.Fatalf("subagent events = %#v, want own nested timeline %v", scout.Events, wantKinds)
	}
}

func TestCodexToolInputSummarizesPatchDiff(t *testing.T) {
	patch := "*** Begin Patch\n-old route\n+new route\n+extra beacon\n*** End Patch"
	if got := codexToolInput("apply_patch", patch); got != "+2 −1" {
		t.Fatalf("codexToolInput() = %q, want terse diff stat", got)
	}
}

func TestCodexResultAcceptsContentBlockOutput(t *testing.T) {
	output := json.RawMessage(`[{"type":"output_text","text":"Process exited with code 7\nFinal output:\nfailed"}]`)
	if got := codexResultSummary(codexOutputText(output)); got != "exit 7" {
		t.Fatalf("content-block result = %q, want exit summary", got)
	}
}

func TestLoadEventsKeepsCodexCompactionBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-compact.jsonl")
	line := `{"timestamp":"2026-01-02T03:00:00Z","type":"event_msg","payload":{"type":"context_compacted"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 || session.Events[0].Kind != model.EventCompact {
		t.Fatalf("events = %#v, want compact boundary", session.Events)
	}
}

func TestLoadEventsRejectsSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.jsonl")
	linkPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(realPath, []byte(`{"type":"event_msg","payload":{"type":"user_message","message":"hidden"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: linkPath, Agent: model.AgentCodex}
	if err := testParser().LoadEvents(context.Background(), session); err == nil {
		t.Fatal("LoadEvents() accepted a symlinked session")
	}
}
