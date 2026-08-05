package claude

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

func workflowFixture() string {
	return filepath.Join("testdata", "workflow", "subagents", "session-workflow.jsonl")
}

func TestParserFingerprintInvalidatesRawPresentation(t *testing.T) {
	if got := testParser().CacheFingerprint(); !strings.HasPrefix(got, "claude-parser-v15:") {
		t.Fatalf("CacheFingerprint() = %q, want v15 workflow discovery", got)
	}
}

func TestParseDiscoversWorkflowSubagentGroup(t *testing.T) {
	session, err := testParser().Parse(workflowFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Subagents) != 2 {
		t.Fatalf("subagents = %#v, want one direct child and one workflow group", session.Subagents)
	}
	direct, group := session.Subagents[0], session.Subagents[1]
	if direct.ID != "direct-scout" || direct.Group {
		t.Fatalf("direct child = %#v", direct)
	}
	if group.ID != "wf-river-run" || !group.Group || group.Path != workflowFixture()+"#wf-river-run" {
		t.Fatalf("workflow group = %#v", group)
	}
	if group.Title != "River survey" {
		t.Fatalf("workflow group title = %q, want summary metadata", group.Title)
	}
	if len(group.Subagents) != 1 || group.Subagents[0].ID != "nested-mapper" {
		t.Fatalf("workflow children = %#v", group.Subagents)
	}
	if nested := group.Subagents[0]; len(nested.Subagents) != 1 || nested.Subagents[0].ID != "deep-reviewer" {
		t.Fatalf("nested agent children = %#v, want one owned transcript", nested.Subagents)
	}
	wantStart := time.Date(2026, 2, 3, 4, 2, 30, 0, time.UTC)
	wantUpdate := time.Date(2026, 2, 3, 4, 4, 0, 0, time.UTC)
	if !group.StartedAt.Equal(wantStart) || !group.UpdatedAt.Equal(wantUpdate) {
		t.Fatalf("group timestamps = %s..%s, want %s..%s", group.StartedAt, group.UpdatedAt, wantStart, wantUpdate)
	}
	if got := session.TotalUsage().TotalTokens(); got != 10 {
		t.Fatalf("TotalUsage().TotalTokens() = %d, want every transcript once for 10", got)
	}
	if got := session.TotalCost().USD; got != 14 {
		t.Fatalf("TotalCost().USD = %v, want every transcript once for 14", got)
	}
}

func TestGroupTimestampsCoverEveryImmediateChild(t *testing.T) {
	later := &model.Session{StartedAt: time.Date(2026, 2, 3, 5, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 3, 6, 0, 0, 0, time.UTC)}
	earlier := &model.Session{StartedAt: time.Date(2026, 2, 3, 3, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 3, 4, 0, 0, 0, time.UTC)}
	group := &model.Session{Group: true}

	attachSubagent(group, later)
	attachSubagent(group, earlier)

	if !group.StartedAt.Equal(earlier.StartedAt) || !group.UpdatedAt.Equal(later.UpdatedAt) {
		t.Fatalf("group timestamps = %s..%s, want %s..%s", group.StartedAt, group.UpdatedAt, earlier.StartedAt, later.UpdatedAt)
	}
}

func TestParseExcludesWorkflowJournal(t *testing.T) {
	session, err := testParser().Parse(workflowFixture())
	if err != nil {
		t.Fatal(err)
	}
	group := session.Subagents[1]
	if len(group.Subagents) != 1 || group.Subagents[0].ID == "journal-entry" {
		t.Fatalf("workflow children = %#v, want only agent transcripts", group.Subagents)
	}
}

func TestParseKeepsDepthOneJSONLWithoutAgentPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-main.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","sessionId":"session-main","message":{"content":"Start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentDir := filepath.Join(dir, "session-main", "subagents")
	if err := os.MkdirAll(subagentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	child := []byte(`{"type":"user","agentId":"legacy-child","message":{"content":"Work"}}` + "\n")
	if err := os.WriteFile(filepath.Join(subagentDir, "legacy-child.jsonl"), child, 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Subagents) != 1 || session.Subagents[0].ID != "legacy-child" || session.Subagents[0].Group {
		t.Fatalf("direct children = %#v, want ungrouped legacy transcript", session.Subagents)
	}
}

func TestParseKeysGroupsByImmediateParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-main.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","sessionId":"session-main","message":{"content":"Start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, branch := range []string{"branch-a", "branch-b"} {
		runDir := filepath.Join(dir, "session-main", "subagents", branch, "wf-river-run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		child := fmt.Sprintf(`{"type":"user","agentId":"worker-%d","message":{"content":"Work"}}`, index) + "\n"
		if err := os.WriteFile(filepath.Join(runDir, fmt.Sprintf("agent-worker-%d.jsonl", index)), []byte(child), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Subagents) != 2 || !session.Subagents[0].Group || !session.Subagents[1].Group ||
		len(session.Subagents[0].Subagents) != 1 || len(session.Subagents[1].Subagents) != 1 {
		t.Fatalf("workflow groups = %#v, want one group per immediate parent", session.Subagents)
	}
}

func TestLoadEventsPopulatesClaudeRecordRef(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-raw.jsonl")
	token := "gAAAA" + strings.Repeat("A", 70)
	line := `{"type":"user","timestamp":"2026-01-02T03:04:05Z","message":{"content":` + strconv.Quote("Inspect "+token+strings.Repeat(" route", 900)) + `}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path}
	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	if len(session.Events) != 1 {
		t.Fatalf("LoadEvents().Events = %#v, want one", session.Events)
	}
	want := model.RecordRef{Path: path, Length: int64(len(line)), Digest: sha256.Sum256([]byte(line))}
	if got := session.Events[0].RecordRef; got != want {
		t.Fatalf("Event.RecordRef = %#v, want %#v", got, want)
	}
}

func TestLoadEventsPreservesFullClaudeMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-long-message.jsonl")
	text := "first-" + strings.Repeat("x", 5_000) + "-last"
	line := `{"type":"user","timestamp":"2026-01-02T03:04:05Z","message":{"content":` + strconv.Quote(text) + `}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	if len(session.Events) != 1 || session.Events[0].Text != text {
		t.Fatalf("LoadEvents().Events[0].Text has %d runes, want %d", len([]rune(session.Events[0].Text)), len([]rune(text)))
	}
}

func TestUserTextJoinsClaudeTextBlocks(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"text","text":"Survey the ridge"},
		{"type":"tool_result","text":"ignored"},
		{"type":"text","text":"Then map the basin"}
	]`)

	if got, want := userText(content), "Survey the ridge\nThen map the basin"; got != want {
		t.Fatalf("userText() = %q, want %q", got, want)
	}
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
	wantRequests := []model.RequestUsage{
		{
			MessageID: "message-main-1", RequestID: "request-main-1",
			Usage: model.Usage{
				Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 20,
				CacheCreation5mTokens: 10, CacheReadTokens: 5,
			},
			USD: 153,
		},
		{
			MessageID: "message-main-2", RequestID: "request-main-2",
			Usage: model.Usage{
				Model: "claude-fable-5", InputTokens: 30, OutputTokens: 10,
				CacheCreation5mTokens: 3, CacheCreation1hTokens: 2,
			},
			USD: 105.5,
		},
	}
	if !reflect.DeepEqual(session.Requests, wantRequests) {
		t.Errorf("Parse().Requests = %#v, want %#v", session.Requests, wantRequests)
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
	wantBreakdowns := map[string]model.CostBreakdown{
		"claude-opus-4-8": {
			Input: model.CostBuckets{{RatePerToken: 1, Tokens: 100}}, Output: model.CostBuckets{{RatePerToken: 2, Tokens: 20}},
			CacheWrite: model.CostBuckets{{RatePerToken: 1.25, Tokens: 10}}, CacheRead: model.CostBuckets{{RatePerToken: 0.1, Tokens: 5}},
		},
		"claude-fable-5": {
			Input: model.CostBuckets{{RatePerToken: 2, Tokens: 30}}, Output: model.CostBuckets{{RatePerToken: 3, Tokens: 10}},
			CacheWrite: model.CostBuckets{{RatePerToken: 2.5, Tokens: 3}, {RatePerToken: 4, Tokens: 2}},
		},
	}
	if !reflect.DeepEqual(session.ModelCostBreakdowns, wantBreakdowns) {
		t.Fatalf("Parse().ModelCostBreakdowns = %#v, want %#v", session.ModelCostBreakdowns, wantBreakdowns)
	}
}

func TestParseDoesNotInventBreakdownForRecordedCostWithoutPricing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-recorded-cost.jsonl")
	line := `{"type":"assistant","sessionId":"session-recorded-cost","costUSD":1.23,"message":{"id":"msg-recorded","model":"unknown-model","usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if session.Cost.USD != 1.23 || session.ModelCosts["unknown-model"] != 1.23 {
		t.Fatalf("Parse() recorded cost = %#v / %#v, want 1.23", session.Cost, session.ModelCosts)
	}
	if _, ok := session.ModelCostBreakdowns["unknown-model"]; ok {
		t.Fatalf("Parse().ModelCostBreakdowns = %#v, want no unavailable breakdown", session.ModelCostBreakdowns)
	}
	if len(session.Requests) != 1 {
		t.Fatalf("Parse().Requests = %#v, want recorded-cost request", session.Requests)
	}
	request := session.Requests[0]
	if request.MessageID != "msg-recorded" || request.RequestID != "" ||
		request.Usage.Model != "unknown-model" || request.Usage.InputTokens != 10 ||
		request.Usage.OutputTokens != 2 || request.USD != 1.23 {
		t.Fatalf("Parse().Requests[0] = %#v, want complete recorded-cost ledger entry", request)
	}
}

func TestParseBreakdownTotalMatchesMultiRowModelCostToDisplayedPrecision(t *testing.T) {
	inputAbove272K, outputAbove272K := 1e-5, 4.5e-5
	calculator := cost.NewCalculator(cost.Table{"model-a": {
		Input: 5e-6, Output: 3e-5,
		InputAbove272K: &inputAbove272K, OutputAbove272K: &outputAbove272K,
	}})
	path := filepath.Join(t.TempDir(), "session-multi-row.jsonl")
	lines := []string{
		`{"type":"assistant","sessionId":"session-multi-row","requestId":"req-a","message":{"id":"msg-a","model":"model-a","usage":{"input_tokens":15224662,"output_tokens":47117160,"cache_creation_input_tokens":20882115,"cache_read_input_tokens":95037078,"cache_creation":{"ephemeral_1h_input_tokens":56166500}}}}`,
		`{"type":"assistant","sessionId":"session-multi-row","requestId":"req-b","message":{"id":"msg-b","model":"model-a","usage":{"input_tokens":15224663,"output_tokens":47117161,"cache_creation_input_tokens":20882116,"cache_read_input_tokens":95037079,"cache_creation":{"ephemeral_1h_input_tokens":56166501}}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := NewParser(calculator).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	breakdown := session.ModelCostBreakdowns["model-a"]
	if math.Abs(breakdown.Total()-session.ModelCosts["model-a"]) > 1e-9 {
		t.Fatalf("breakdown total %.17g differs visibly from model cost %.17g", breakdown.Total(), session.ModelCosts["model-a"])
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
	// One user text turn; the assistant records carry usage only, no text blocks.
	if session.Messages != 1 {
		t.Errorf("Parse().Messages = %d, want 1", session.Messages)
	}
}

func TestParseCountsUserAndAssistantMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-count.jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","sessionId":"s","message":{"role":"user","content":"First question"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:10Z","sessionId":"s","message":{"id":"a1","model":"claude-opus-4-8","content":[{"type":"text","text":"Answer one"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:20Z","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file.txt"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:30Z","sessionId":"s","message":{"id":"a2","model":"claude-opus-4-8","content":[{"type":"text","text":"Answer two"}],"usage":{"input_tokens":12,"output_tokens":6}}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:40Z","sessionId":"s","message":{"id":"a3","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"x"}}],"usage":{"input_tokens":8,"output_tokens":4}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	// 1 user prompt + 2 assistant text replies. The tool-result-only user record
	// and the tool-only assistant record are turns of tooling, not messages.
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

func TestParseReportsNestedWalkErrorAndKeepsHealthyChildren(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-main.jsonl")
	subagentDir := filepath.Join(dir, "session-main", "subagents")
	blockedDir := filepath.Join(subagentDir, "blocked")
	if err := os.MkdirAll(blockedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","sessionId":"session-main","message":{"content":"Start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentDir, "agent-healthy.jsonl"), []byte(`{"type":"user","agentId":"healthy","message":{"content":"Work"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parser := testParser()
	parser.walk = func(root string, visit filepath.WalkFunc) error {
		return filepath.Walk(root, func(current string, info os.FileInfo, walkErr error) error {
			if current == blockedDir {
				return visit(current, nil, errors.New("injected walk failure"))
			}
			return visit(current, info, walkErr)
		})
	}
	var diagnostics []string
	session, err := parser.ParseWithDiagnostics(path, func(path string, _ error) {
		diagnostics = append(diagnostics, path)
	})
	if err != nil {
		t.Fatalf("ParseWithDiagnostics() error = %v, want partial session", err)
	}
	if len(diagnostics) != 1 || diagnostics[0] != blockedDir {
		t.Fatalf("diagnostics = %#v, want injected nested path", diagnostics)
	}
	if len(session.Subagents) != 1 || session.Subagents[0].ID != "healthy" {
		t.Fatalf("healthy children = %#v, want retained sibling", session.Subagents)
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

func TestLoadEventsAttachesRequestUsageToAssistantTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-usage.jsonl")
	content := strings.Join([]string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","message":{"content":"Inspect the engine"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"model":"claude-opus-4-8","content":[{"type":"thinking","thinking":"Check the state"},{"type":"text","text":"The engine is ready."}],"usage":{"input_tokens":3000,"output_tokens":4000,"cache_read_input_tokens":37000,"cache_creation_input_tokens":500}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	var withUsage []int
	for index, event := range session.Events {
		if event.Usage != nil {
			withUsage = append(withUsage, index)
		}
	}
	// Usage lands on the first block of the assistant line only, so a turn sums each
	// billed request exactly once rather than per rendered block.
	if len(withUsage) != 1 || session.Events[withUsage[0]].Kind != model.EventThinking {
		t.Fatalf("events carrying usage = %v, want the single thinking block", withUsage)
	}
	usage := session.Events[withUsage[0]].Usage
	if usage.PromptTokens() != 40_500 || usage.FlowTokens() != 7_500 {
		t.Fatalf("attached usage prompt=%d flow=%d, want 40500 and 7500", usage.PromptTokens(), usage.FlowTokens())
	}
}

// TestLoadEventsAttributesStreamedRequestOnceAtMaxUsage guards the fix for the
// double count: one API response is written across content-block lines that share
// a message id and request id, and streaming re-logs it with growing output. It
// must land on a single head event at the highest usage, so a turn totals the
// request once and in full.
func TestLoadEventsAttributesStreamedRequestOnceAtMaxUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-stream.jsonl")
	content := strings.Join([]string{
		`{"type":"user","timestamp":"2026-01-02T03:00:00Z","message":{"content":"Chart the route"}}`,
		`{"type":"assistant","requestId":"req-a","timestamp":"2026-01-02T03:00:01Z","message":{"id":"msg-a","model":"claude-opus-4-8","content":[{"type":"text","text":"Working on it."}],"usage":{"input_tokens":2,"output_tokens":5,"cache_read_input_tokens":1000}}}`,
		`{"type":"assistant","requestId":"req-a","timestamp":"2026-01-02T03:00:02Z","message":{"id":"msg-a","model":"claude-opus-4-8","content":[{"type":"tool_use","id":"tool-read","name":"Read","input":{"file_path":"/workspace/map.go"}}],"usage":{"input_tokens":2,"output_tokens":339,"cache_read_input_tokens":1000}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	var withUsage []int
	for index, event := range session.Events {
		if event.Usage != nil {
			withUsage = append(withUsage, index)
		}
	}
	if len(withUsage) != 1 || session.Events[withUsage[0]].Kind != model.EventAssistantText {
		t.Fatalf("events carrying usage = %v, want the single text head", withUsage)
	}
	if got := session.Events[withUsage[0]].Usage.OutputTokens; got != 339 {
		t.Fatalf("attached output = %d, want the streamed maximum 339", got)
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

func TestLoadEventsTaskFallbackSkipsWorkflowGroup(t *testing.T) {
	parsed, err := testParser().Parse(workflowFixture())
	if err != nil {
		t.Fatal(err)
	}
	direct, group := parsed.Subagents[0], parsed.Subagents[1]
	parsed.Subagents = []*model.Session{group, direct}

	if err := testParser().LoadNodeEvents(context.Background(), parsed); err != nil {
		t.Fatal(err)
	}
	for _, event := range parsed.Events {
		if event.Kind == model.EventSubagent && event.ToolName == "Task" {
			if event.Subagent != direct {
				t.Fatalf("Task fallback linked to %#v, want direct child", event.Subagent)
			}
			return
		}
	}
	t.Fatal("Task event not found")
}

func TestLoadEventsLinksWorkflowByRunIDAndNamesGroup(t *testing.T) {
	session, err := testParser().Parse(workflowFixture())
	if err != nil {
		t.Fatal(err)
	}
	target := session.Subagents[1]
	target.Title = target.ID
	decoy := &model.Session{ID: "wf-decoy-run", Agent: model.AgentClaude, Path: session.Path + "#wf-decoy-run", Title: "Decoy", Group: true}
	session.Subagents = append([]*model.Session{decoy}, session.Subagents...)
	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	for _, event := range session.Events {
		if event.Kind == model.EventSubagent && event.ToolName == "Workflow" {
			if event.Subagent == nil || event.Subagent.ID != "wf-river-run" || event.AgentID != "wf-river-run" {
				t.Fatalf("Workflow event = %#v, want matching run group", event)
			}
			if event.Subagent.Title != "River survey" {
				t.Fatalf("workflow title = %q, want River survey", event.Subagent.Title)
			}
			if event.Subagent != target || decoy.Title != "Decoy" {
				t.Fatalf("run ID binding target = %#v, decoy title = %q", event.Subagent, decoy.Title)
			}
			if len(event.Subagent.Subagents[0].Events) != 1 || event.Subagent.Subagents[0].Events[0].Text != "Map the river channels" {
				t.Fatalf("nested workflow events = %#v, want loaded child timeline", event.Subagent.Subagents[0].Events)
			}
			return
		}
	}
	t.Fatal("Workflow event not found")
}

func TestLoadEventsKeepsInflightWorkflowEventWithoutGroup(t *testing.T) {
	path := filepath.Join("testdata", "workflow", "subagents", "session-workflow", "subagents", "workflows", "fixture-inflight.jsonl")
	session, err := testParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := testParser().LoadNodeEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	for _, event := range session.Events {
		if event.Kind == model.EventSubagent && event.ToolName == "Workflow" {
			if event.Subagent != nil || event.AgentID != "" || event.ResultSummary != "launched" {
				t.Fatalf("in-flight Workflow event = %#v, want associated result without group", event)
			}
			return
		}
	}
	t.Fatalf("in-flight events = %#v, want Workflow subagent event", session.Events)
}

// A Task input carries keys beyond the identifying ones, and their values are
// not all strings. Decoding stops at the first value that does not fit the
// target, so a non-string ahead of subagent_type must not cost the match.
func TestLoadEventsMatchesSpawnPastNonStringToolInput(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-detail.jsonl")
	for _, name := range []string{"builder", "scout"} {
		path := filepath.Join(dir, "agent-"+name+".jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"Work"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parent := `{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"content":[{"type":"tool_use","id":"tool-agent","name":"Agent","input":{"run_in_background":true,"max_turns":12,"subagent_type":"Scout"}}]}}` + "\n"
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
		t.Fatalf("spawn linked to %#v, want scout matched by subagent_type", session.Events[0].Subagent)
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

func TestLoadEventsDropsLocalCommandCaveat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-caveat.jsonl")
	record := `{"type":"user","message":{"content":"<local-command-caveat>Caveat: fictional notice.</local-command-caveat>"}}` + "\n"
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	if len(session.Events) != 0 {
		t.Fatalf("LoadEvents().Events = %#v, want caveat suppressed", session.Events)
	}
}

func TestLoadEventsCleansLocalCommandCaveatBeforeProse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-caveat-prose.jsonl")
	record := `{"type":"user","message":{"content":"<local-command-caveat>Caveat: fictional notice.</local-command-caveat>\nContinue the survey"}}` + "\n"
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	if len(session.Events) != 1 {
		t.Fatalf("LoadEvents().Events = %#v, want one prose event", session.Events)
	}
	event := session.Events[0]
	if event.Text != "Continue the survey" || event.Harness {
		t.Fatalf("event = %#v, want cleaned human prose", event)
	}
}

func TestLoadEventsClassifiesClaudeUserTurns(t *testing.T) {
	tests := []struct {
		name        string
		record      string
		wantHarness bool
	}{
		{name: "meta", record: `{"type":"user","isMeta":true,"message":{"content":"Injected skill body"}}`, wantHarness: true},
		{name: "compact summary", record: `{"type":"user","isCompactSummary":true,"message":{"content":"Prior conversation summary"}}`, wantHarness: true},
		{name: "system prompt source", record: `{"type":"user","promptSource":"system","message":{"content":"Runtime instructions"}}`, wantHarness: true},
		{name: "non-human origin", record: `{"type":"user","origin":{"kind":"task-notification"},"message":{"content":"Background task completed"}}`, wantHarness: true},
		{name: "command wrapper", record: `{"type":"user","message":{"content":"<command-name>survey</command-name>"}}`, wantHarness: true},
		{name: "command wrapper after hard noise", record: `{"type":"user","message":{"content":"<system-reminder>Hidden metadata</system-reminder>\n<command-name>survey</command-name>"}}`, wantHarness: true},
		{name: "bash output wrapper", record: `{"type":"user","message":{"content":"<bash-stdout>route clear</bash-stdout>"}}`, wantHarness: true},
		{name: "interruption notice", record: `{"type":"user","message":{"content":"[Request interrupted by user]"}}`, wantHarness: true},
		{name: "typed prompt", record: `{"type":"user","promptSource":"typed","origin":{"kind":"human"},"message":{"content":"Survey the northern ridge"}}`},
		{name: "missing origin", record: `{"type":"user","message":{"content":"Survey the southern ridge"}}`},
		{name: "malformed marker fields", record: `{"type":"user","isMeta":"true","isCompactSummary":[],"promptSource":7,"origin":{"kind":"human"},"message":{"content":"Survey the eastern ridge"}}`},
		{name: "malformed origin", record: `{"type":"user","origin":"human","message":{"content":"Survey the western ridge"}}`},
		{name: "marker precedes human origin", record: `{"type":"user","isMeta":true,"origin":{"kind":"human"},"message":{"content":"Base directory for this skill: /workspace/skills/survey"}}`, wantHarness: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(test.record+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			session := &model.Session{Path: path, Agent: model.AgentClaude}

			if err := testParser().LoadEvents(context.Background(), session); err != nil {
				t.Fatalf("LoadEvents() error = %v", err)
			}
			if len(session.Events) != 1 || session.Events[0].Kind != model.EventUser {
				t.Fatalf("LoadEvents().Events = %#v, want one user event", session.Events)
			}
			if got := session.Events[0].Harness; got != test.wantHarness {
				t.Errorf("Event.Harness = %t, want %t", got, test.wantHarness)
			}
		})
	}
}

func TestClaudeToolInputSummarizesEditDiff(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/workspace/starship/engine.go","old_string":"old one\nold two","new_string":"new one\nnew two\nnew three"}`)
	if got := claudeToolInput("Edit", input); got != "/workspace/starship/engine.go · +3 −2" {
		t.Fatalf("claudeToolInput() = %q, want terse diff stat", got)
	}
}

func TestLoadEventsExtractsClaudeEditDetail(t *testing.T) {
	session := &model.Session{Path: filepath.Join("testdata", "tool-detail", "subagents", "session-tool-detail.jsonl"), Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	if call.ToolInput != "/workspace/lunar-lab/route.txt · +2 −2" {
		t.Fatalf("ToolInput = %q, want unchanged collapsed summary", call.ToolInput)
	}
	if call.Detail == nil || call.Detail.Diff != "-ridge one\n-ridge two\n+valley one\n+valley two" {
		t.Fatalf("Detail = %#v, want old-to-new replace block", call.Detail)
	}
}

func TestLoadEventsExtractsClaudeMultiEditDetail(t *testing.T) {
	session := &model.Session{Path: filepath.Join("testdata", "tool-detail", "subagents", "session-tool-detail.jsonl"), Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	want := "-scan ridge\n+scan valley\n\n-mark north\n-mark south\n+mark east\n+mark west"
	if detail := session.Events[2].Detail; detail == nil || detail.Diff != want {
		t.Fatalf("Detail = %#v, want separated replace blocks", detail)
	}
}

func TestLoadEventsExtractsClaudeWriteDetail(t *testing.T) {
	session := &model.Session{Path: filepath.Join("testdata", "tool-detail", "subagents", "session-tool-detail.jsonl"), Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[4]
	if call.ToolInput != "/workspace/lunar-lab/beacon.txt" {
		t.Fatalf("ToolInput = %q, want unchanged file path", call.ToolInput)
	}
	if call.Detail == nil || call.Detail.Diff != "+beacon alpha\n+beacon beta" {
		t.Fatalf("Detail = %#v, want all-added block", call.Detail)
	}
}

func TestLoadEventsPreservesClaudeBashInputDetail(t *testing.T) {
	session := &model.Session{Path: filepath.Join("testdata", "tool-detail", "subagents", "session-tool-detail.jsonl"), Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	want := "survey-route --all\nprintf 'done\\n'"
	call := session.Events[6]
	if call.ToolInput != want {
		t.Fatalf("ToolInput = %q, want existing command", call.ToolInput)
	}
	if call.Detail == nil || call.Detail.Input != want {
		t.Fatalf("Detail = %#v, want multiline command", call.Detail)
	}
}

func TestLoadEventsPreservesClaudeBashOutputDetail(t *testing.T) {
	session := &model.Session{Path: filepath.Join("testdata", "tool-detail", "subagents", "session-tool-detail.jsonl"), Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[6]
	if call.ResultSummary != "exit 7" {
		t.Fatalf("ResultSummary = %q, want unchanged exit summary", call.ResultSummary)
	}
	want := "Error: Exit code 7\nroute blocked\nretry advised"
	if call.Detail == nil || call.Detail.Output != want {
		t.Fatalf("Detail = %#v, want newline-preserving output", call.Detail)
	}
}

func TestClaudeResultTextJoinsTextBlocksWithNewlines(t *testing.T) {
	content := json.RawMessage(`[{"type":"text","text":"first line\nsecond line"},{"type":"text","text":"third line"}]`)
	if got := claudeResultText(content); got != "first line\nsecond line\nthird line" {
		t.Fatalf("claudeResultText() = %q, want joined text blocks", got)
	}
}

func TestLoadEventsExtractsClaudeReadDetail(t *testing.T) {
	session := &model.Session{Path: filepath.Join("testdata", "tool-detail", "subagents", "session-tool-detail.jsonl"), Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[8]
	if call.ToolInput != "/workspace/lunar-lab/map.txt" {
		t.Fatalf("ToolInput = %q, want unchanged file path", call.ToolInput)
	}
	want := "/workspace/lunar-lab/map.txt · offset 4 · limit 8"
	if call.Detail == nil || call.Detail.Input != want {
		t.Fatalf("Detail = %#v, want bounded read range", call.Detail)
	}
}

func TestClaudeToolDetailPrettyPrintsOtherInputs(t *testing.T) {
	input := json.RawMessage(`{"query":"ridge","limit":2}`)
	detail := claudeToolDetail("Grep", input)
	want := "{\n  \"query\": \"ridge\",\n  \"limit\": 2\n}"
	if detail == nil || detail.Input != want {
		t.Fatalf("claudeToolDetail() = %#v, want pretty multiline input", detail)
	}
}

func TestClaudeToolDetailLeavesDeepJSONRaw(t *testing.T) {
	input := `{"query":` + strings.Repeat("[", 100) + "0" + strings.Repeat("]", 100) + "}"
	detail := claudeToolDetail("Grep", json.RawMessage(input))
	if detail == nil || detail.Input != input {
		t.Fatalf("claudeToolDetail() expanded deeply nested input to %d bytes", len(detail.Input))
	}
}

func TestClaudePrettyInputReturnsUnboundedRawWhenGuardTrips(t *testing.T) {
	input := json.RawMessage(`{"payload":"` + strings.Repeat("x", 5_000) + `"}`)

	if got := claudePrettyInput(input); got != string(input) {
		t.Fatalf("claudePrettyInput() returned %d bytes, want unbounded %d-byte input", len(got), len(input))
	}
}

func TestClaudeToolDetailOmitsSubagentTools(t *testing.T) {
	for _, name := range []string{"Agent", "Task"} {
		if detail := claudeToolDetail(name, json.RawMessage(`{"description":"Survey the ridge"}`)); detail != nil {
			t.Errorf("claudeToolDetail(%q) = %#v, want nil", name, detail)
		}
	}
}

func TestClaudeToolDetailPreservesEveryField(t *testing.T) {
	value := "start\n" + strings.Repeat("界", 5000) + "\nend"
	bashInput, err := json.Marshal(map[string]string{"command": value})
	if err != nil {
		t.Fatal(err)
	}
	writeInput, err := json.Marshal(map[string]string{"content": value})
	if err != nil {
		t.Fatal(err)
	}
	if got := claudeToolDetail("Bash", bashInput).Input; got != value {
		t.Fatalf("Input has %d runes, want %d", len([]rune(got)), len([]rune(value)))
	}
	unboundedDiff := "+" + strings.ReplaceAll(value, "\n", "\n+")
	if got := claudeToolDetail("Write", writeInput).Diff; got != unboundedDiff {
		t.Fatalf("Diff has %d runes, want %d", len([]rune(got)), len([]rune(unboundedDiff)))
	}

	path := filepath.Join(t.TempDir(), "session-bounded-output.jsonl")
	call := map[string]any{
		"type": "assistant",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "id": "tool-read", "name": "Read",
			"input": map[string]any{"file_path": "/workspace/lunar-lab/map.txt"},
		}}},
	}
	result := map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "tool-read", "content": value,
		}}},
	}
	callLine, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	resultLine, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(callLine, '\n'), append(resultLine, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}
	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if got := session.Events[0].Detail.Output; got != value {
		t.Fatalf("Output has %d runes, want %d", len([]rune(got)), len([]rune(value)))
	}
}

func TestLoadEventsDoesNotAttachClaudeDetailWithoutCallID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-missing-call-id.jsonl")
	content := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-01-02T03:00:01Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"survey-route"}}]}}`,
		`{"type":"user","timestamp":"2026-01-02T03:00:02Z","message":{"content":[{"type":"tool_result","content":"unrelated output"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if output := session.Events[0].Detail.Output; output != "" {
		t.Fatalf("Detail.Output = %q, want unlinked result", output)
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
	line := `{"type":"system","subtype":"compact_boundary","timestamp":"2026-01-02T03:00:00Z","content":"Context compacted","compactMetadata":{"trigger":"manual","preTokens":181900,"postTokens":8389}}` + "\n"
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
	if got := session.Events[0]; got.CompactTrigger != "manual" || got.CompactPostTokens != 8389 {
		t.Fatalf("compact metadata = %q/%d, want manual/8389", got.CompactTrigger, got.CompactPostTokens)
	}
}

// The Advisor tool runs a separate model server-side; its tokens ride in
// usage.iterations[type=advisor_message], excluded from the top-level usage
// because they bill at the advisor model's rates. The advisor block is logged
// when the call opens but its usage completes on a later line, so the row must
// pick the cost up across lines, count it once despite re-logs, and price it at
// the advisor's own model.
func TestLoadEventsCostsAdvisorSubInference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-advisor.jsonl")
	const exec = `"input_tokens":100,"output_tokens":50`
	msgLine := func(content, iterations string) string {
		return `{"type":"assistant","requestId":"req1","message":{"id":"msg1","model":"claude-opus-4-8","content":[` +
			content + `],"usage":{` + exec + `,"iterations":[` + iterations + `]}}}`
	}
	execIter := `{"type":"message","input_tokens":100,"output_tokens":50}`
	advIter := `{"type":"advisor_message","model":"claude-fable-5","input_tokens":1000,"output_tokens":500}`
	lines := strings.Join([]string{
		msgLine(`{"type":"thinking","thinking":"weigh it"}`, execIter),                          // opens the turn
		msgLine(`{"type":"server_tool_use","id":"srv1","name":"advisor","input":{}}`, execIter), // advisor call, usage not yet complete
		msgLine(`{"type":"text","text":"done"}`, execIter+","+advIter),                          // advisor usage completes here
		msgLine(`{"type":"text","text":"done"}`, execIter+","+advIter),                          // re-log must not double count
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	p := testParser()
	session, err := p.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	// Executor opus (100*1 + 50*2 = 200) plus advisor fable (1000*2 + 500*3 = 3500),
	// each counted exactly once across the four lines.
	if got := session.TotalCost().USD; got != 3700 {
		t.Fatalf("TotalCost = %v, want 3700 (executor 200 + advisor 3500)", got)
	}
	if got := session.ModelCosts["claude-fable-5"]; got != 3500 {
		t.Fatalf("advisor model cost = %v, want 3500 priced at the advisor model", got)
	}
	if got := session.TotalUsage(); got.InputTokens != 1100 || got.OutputTokens != 550 {
		t.Fatalf("TotalUsage = %d/%d, want 1100/550", got.InputTokens, got.OutputTokens)
	}
	wantRequests := []model.RequestUsage{
		{MessageID: "msg1", RequestID: "req1", Usage: model.Usage{Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 50}, USD: 200},
		{MessageID: "msg1\x00advisor\x000", RequestID: "req1", Usage: model.Usage{Model: "claude-fable-5", InputTokens: 1000, OutputTokens: 500}, USD: 3500},
	}
	if !reflect.DeepEqual(session.Requests, wantRequests) {
		t.Fatalf("Requests = %#v, want executor and synthetic advisor ledgers %#v", session.Requests, wantRequests)
	}

	if err := p.LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	var advisors []model.Event
	for _, event := range session.Events {
		if event.Kind == model.EventAdvisor {
			advisors = append(advisors, event)
		}
	}
	if len(advisors) != 1 {
		t.Fatalf("advisor events = %d, want 1", len(advisors))
	}
	got := advisors[0]
	if got.Model != "claude-fable-5" || got.Usage == nil || got.Usage.InputTokens != 1000 || got.Usage.OutputTokens != 500 {
		t.Fatalf("advisor row = model %q usage %+v, want fable-5 1000/500", got.Model, got.Usage)
	}
	if !got.Priced || got.Cost.Total() != 3500 {
		t.Fatalf("advisor row cost = %v (priced=%v), want 3500", got.Cost.Total(), got.Priced)
	}
}
