package claude

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
	return NewParser(cost.NewCalculator(cost.Table{
		"claude-opus-4-8": {Input: 1, Output: 2},
		"claude-fable-5":  {Input: 2, Output: 3},
		"claude-sonnet-5": {Input: 3, Output: 4},
	}))
}

func mainFixture() string {
	return filepath.Join("testdata", "project-alpha", "session-main.jsonl")
}

func TestParseUsesAITitle(t *testing.T) {
	session, err := testParser().Parse(mainFixture())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if session.Title != "Launch analysis" {
		t.Fatalf("Parse().Title = %q, want %q", session.Title, "Launch analysis")
	}
}

func TestParseCleansAndCapsAITitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-title.jsonl")
	title := "  " + strings.Repeat("L", 120) + "\nignored"
	line := `{"type":"ai-title","timestamp":"2026-01-02T03:04:05Z","sessionId":"session-title","aiTitle":` + strconv.Quote(title) + `}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := strings.Repeat("L", 95) + "…"
	if session.Title != want {
		t.Fatalf("Parse().Title = %q, want %q", session.Title, want)
	}
}

func TestParseFallsBackToFirstUserText(t *testing.T) {
	path := filepath.Join("testdata", "project-alpha", "session-main", "subagents", "agent-scout.jsonl")
	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if session.Title != "Inspect telemetry" {
		t.Fatalf("Parse().Title = %q, want %q", session.Title, "Inspect telemetry")
	}
}

func TestParseReadsUserTextBlocks(t *testing.T) {
	path := filepath.Join("testdata", "project-alpha", "session-main", "subagents", "agent-builder.jsonl")
	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if session.Title != "Prepare the vehicle" {
		t.Fatalf("Parse().Title = %q, want %q", session.Title, "Prepare the vehicle")
	}
}

func TestParseFlagsAPIErrorSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-error.jsonl")
	line := `{"type":"assistant","timestamp":"2026-01-02T03:04:05Z","sessionId":"session-error","isApiErrorMessage":true,"apiErrorStatus":529,"error":{"message":"overloaded"},"message":{"id":"msg-error","model":"claude-opus-4-8","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if !session.HasError {
		t.Fatalf("Parse().HasError = false for API error record")
	}
}

func TestParseBuildsDeduplicatedBillableUsage(t *testing.T) {
	session, err := testParser().Parse(mainFixture())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	wantModels := []string{"claude-opus-4-8", "claude-fable-5"}
	wantUsage := model.Usage{
		InputTokens:           130,
		OutputTokens:          30,
		CacheCreation5mTokens: 13,
		CacheCreation1hTokens: 2,
		CacheReadTokens:       5,
	}
	var ownUsage model.Usage
	for _, usage := range session.Usage {
		ownUsage = ownUsage.Add(usage)
	}
	if !reflect.DeepEqual(session.Models, wantModels) {
		t.Errorf("Parse().Models = %v, want %v", session.Models, wantModels)
	}
	if len(session.Usage) != 2 || !reflect.DeepEqual(ownUsage, wantUsage) {
		t.Errorf("Parse().Usage = %#v, total %#v, want two records totaling %#v", session.Usage, ownUsage, wantUsage)
	}
}

func TestParseCalculatesOwnCostPerMessageModel(t *testing.T) {
	session, err := testParser().Parse(mainFixture())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := 258.5
	if math.Abs(session.Cost.USD-want) > 1e-12 {
		t.Fatalf("Parse().Cost.USD = %v, want %v", session.Cost.USD, want)
	}
}

func TestParseBuildsUnifiedSessionMetadata(t *testing.T) {
	session, err := testParser().Parse(mainFixture())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
	if session.ID != "session-main" || session.CWD != "/workspace/starship" || session.Project != "starship" {
		t.Errorf("Parse() identity = ID %q, CWD %q, project %q", session.ID, session.CWD, session.Project)
	}
	if session.GitBranch != "orbit/alpha" || !session.StartedAt.Equal(started) || !session.UpdatedAt.Equal(updated) {
		t.Errorf("Parse() metadata = branch %q, started %v, updated %v", session.GitBranch, session.StartedAt, session.UpdatedAt)
	}
	if session.Messages != 3 {
		t.Errorf("Parse().Messages = %d, want 3", session.Messages)
	}
}

func TestParseLinksSubagentFiles(t *testing.T) {
	session, err := testParser().Parse(mainFixture())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if len(session.Subagents) != 2 {
		t.Fatalf("Parse().Subagents = %#v, want two", session.Subagents)
	}
	if session.Subagents[0].ID != "builder" || session.Subagents[1].ID != "scout" {
		t.Errorf("subagent IDs = %q, %q, want builder, scout", session.Subagents[0].ID, session.Subagents[1].ID)
	}
	if session.Subagents[0].Title != "Prepare the vehicle" || session.Subagents[1].Title != "Inspect telemetry" {
		t.Errorf("subagent titles = %q, %q", session.Subagents[0].Title, session.Subagents[1].Title)
	}
}

func TestParsePropagatesSubagentUpdateTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-main.jsonl")
	subagentDir := filepath.Join(dir, "session-main", "subagents")
	if err := os.MkdirAll(subagentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-01-02T03:00:00Z","sessionId":"session-main","message":{"content":"Start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentDir, "agent-scout.jsonl"), []byte(`{"type":"user","timestamp":"2026-01-02T04:00:00Z","agentId":"scout","message":{"content":"Scout"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
	if !session.UpdatedAt.Equal(want) {
		t.Fatalf("Parse().UpdatedAt = %v, want %v", session.UpdatedAt, want)
	}
}

func TestParseDoesNotFollowSymlinkedSubagentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-main.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","sessionId":"session-main","message":{"content":"Start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(dir, "external")
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "agent-linked.jsonl"), []byte(`{"type":"user","agentId":"linked","message":{"content":"Linked"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	companion := filepath.Join(dir, "session-main")
	if err := os.MkdirAll(companion, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(companion, "subagents")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Subagents) != 0 {
		t.Fatalf("Parse().Subagents = %#v, want symlink ignored", session.Subagents)
	}
}

func TestParseSkipsOversizedRecordAndContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-oversized.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","sessionId":"session-main","message":{"content":"Start"}}` + "\n",
		`{"type":"progress","data":"` + strings.Repeat("x", 17*1024*1024) + `"}` + "\n",
		`{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","sessionId":"session-main","requestId":"request-a","message":{"id":"message-a","model":"claude-opus-4-8","usage":{"input_tokens":3}}}` + "\n",
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
		t.Fatalf("Parse().Usage = %#v, want later usage record", session.Usage)
	}
}

func TestParseSkipsNegativeUsageRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-negative.jsonl")
	content := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-01-02T03:00:00Z","requestId":"request-bad","message":{"id":"message-bad","model":"claude-opus-4-8","usage":{"input_tokens":-1}}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","requestId":"request-good","message":{"id":"message-good","model":"claude-opus-4-8","usage":{"input_tokens":3}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Usage) != 1 || session.Usage[0].InputTokens != 3 {
		t.Fatalf("Parse().Usage = %#v, want only valid usage", session.Usage)
	}
}

func TestLoadEventsBuildsLinkedClaudeTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-detail.jsonl")
	content := strings.Join([]string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","message":{"content":"Inspect the engine"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"model":"claude-opus-4-8","content":[{"type":"thinking","thinking":"Check the engine state"},{"type":"tool_use","id":"tool-read","name":"Read","input":{"file_path":"/workspace/starship/engine.go"}}]}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"tool-read","content":"engine ready"}]}}`,
		`{"type":"system","timestamp":"2026-01-02T03:00:04Z","message":{"content":"<system-reminder>ignore this</system-reminder>"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:05Z","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"The engine is ready."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

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
	if call.ToolName != "Read" || call.ToolInput != "/workspace/starship/engine.go" || call.ResultSummary != "engine ready" || call.Duration != 2*time.Second {
		t.Fatalf("linked tool call = %#v", call)
	}
}

func TestLoadEventsLinksAndLoadsSubagentAtSpawn(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-detail.jsonl")
	subagentPath := filepath.Join(dir, "agent-scout.jsonl")
	parent := strings.Join([]string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","message":{"content":"Survey the route"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tool-agent","name":"Agent","input":{"description":"Scout terrain","subagent_type":"Explore"}}]}}`,
	}, "\n") + "\n"
	child := `{"type":"user","timestamp":"2026-01-02T03:00:02Z","message":{"content":"Inspect the ridge"}}` + "\n"
	if err := os.WriteFile(parentPath, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subagentPath, []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	subagent := &model.Session{ID: "scout", Title: "Scout terrain", Path: subagentPath, Agent: model.AgentClaude}
	session := &model.Session{Path: parentPath, Agent: model.AgentClaude, Subagents: []*model.Session{subagent}}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	if len(session.Events) != 2 || session.Events[1].Kind != model.EventSubagent || session.Events[1].Subagent != subagent {
		t.Fatalf("spawn event = %#v", session.Events)
	}
	if len(subagent.Events) != 1 || subagent.Events[0].Text != "Inspect the ridge" {
		t.Fatalf("subagent events = %#v", subagent.Events)
	}
}

func TestLoadEventsUsesAgentIDToDisambiguateSpawn(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-detail.jsonl")
	for _, name := range []string{"builder", "scout"} {
		path := filepath.Join(dir, "agent-"+name+".jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"Work"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parent := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"content":[{"type":"tool_use","id":"tool-agent","name":"Agent","input":{"description":"Investigate"}}]}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:02Z","toolUseResult":{"agentId":"scout"},"message":{"content":[{"type":"tool_result","tool_use_id":"tool-agent","content":"done"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(parentPath, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	builder := &model.Session{ID: "builder", Title: "Build", Path: filepath.Join(dir, "agent-builder.jsonl"), Agent: model.AgentClaude}
	scout := &model.Session{ID: "scout", Title: "Scout", Path: filepath.Join(dir, "agent-scout.jsonl"), Agent: model.AgentClaude}
	session := &model.Session{Path: parentPath, Agent: model.AgentClaude, Subagents: []*model.Session{builder, scout}}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if session.Events[0].Subagent != scout {
		t.Fatalf("spawn linked to %#v, want scout", session.Events[0].Subagent)
	}
}

func TestLoadEventsDropsHardNoise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-noise.jsonl")
	content := strings.Join([]string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","message":{"content":"Warmup"}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:01Z","message":{"content":"<system-reminder>hidden metadata</system-reminder>"}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:02Z","message":{"content":"<permission-preamble>hidden policy</permission-preamble>"}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:03Z","message":{"content":"Plot the trajectory"}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:04Z","message":{"content":[{"type":"text","text":"<system-reminder>hidden metadata</system-reminder>"},{"type":"text","text":"Confirm the orbit"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:05Z","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"Trajectory ready\n<system-reminder>hidden metadata</system-reminder>"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	want := []string{"Plot the trajectory", "Confirm the orbit", "Trajectory ready"}
	if len(session.Events) != len(want) {
		t.Fatalf("LoadEvents() = %#v, want %d meaningful events", session.Events, len(want))
	}
	for index, text := range want {
		if session.Events[index].Text != text {
			t.Fatalf("event %d text = %q, want %q", index, session.Events[index].Text, text)
		}
	}
	if session.Events[1].Kind != model.EventUser || session.Events[2].Kind != model.EventAssistantText {
		t.Fatalf("mixed-content event kinds = %q, %q", session.Events[1].Kind, session.Events[2].Kind)
	}
}

func TestClaudeToolInputSummarizesEditDiff(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/workspace/starship/engine.go","old_string":"old one\nold two","new_string":"new one\nnew two\nnew three"}`)
	if got := claudeToolInput("Edit", input); got != "/workspace/starship/engine.go · +3 −2" {
		t.Fatalf("claudeToolInput() = %q, want terse diff stat", got)
	}
}

func TestLoadEventsSummarizesBashExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-bash.jsonl")
	content := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"content":[{"type":"tool_use","id":"tool-bash","name":"Bash","input":{"command":"check-route"}}]}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"tool-bash","content":"Error: Exit code 7\nroute blocked"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if session.Events[0].ResultSummary != "exit 7" {
		t.Fatalf("ResultSummary = %q, want exit 7", session.Events[0].ResultSummary)
	}
}

func TestLoadEventsKeepsCompactionBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-compact.jsonl")
	line := `{"type":"system","subtype":"compact_boundary","timestamp":"2026-01-02T03:00:00Z","content":"Context compacted"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 || session.Events[0].Kind != model.EventCompact || session.Events[0].Text != "Context compacted" {
		t.Fatalf("events = %#v, want compact boundary", session.Events)
	}
}
