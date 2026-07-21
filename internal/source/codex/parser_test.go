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

func tieredTestParser() Parser {
	above := 2.0
	return NewParser(cost.NewCalculator(cost.Table{
		"gpt-5.6": {Input: 1, Output: 3, InputAbove272K: &above},
	}), "gpt-5")
}

func parseTieredSession(t *testing.T, events ...string) *model.Session {
	t.Helper()
	lines := append([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"gpt-5.6"}}`,
	}, events...)
	path := filepath.Join(t.TempDir(), "rollout-tiered.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := tieredTestParser().Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return session
}

func TestParserFingerprintInvalidatesCodexPresentation(t *testing.T) {
	if got := testParser().CacheFingerprint(); !strings.HasPrefix(got, "codex-parser-v19:") {
		t.Fatalf("CacheFingerprint() = %q, want v19 distinct-request costs", got)
	}
}

func TestLoadEventsPopulatesBoundedEncryptedElidedRawRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-raw.jsonl")
	token := "gAAAA" + strings.Repeat("A", 70)
	line := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"user_message","message":` + strconv.Quote("Inspect "+token+strings.Repeat(" route", 900)) + `}}`
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
	raw := session.Events[0].Raw
	if raw == "" || strings.Contains(raw, token) || !strings.Contains(raw, "<encrypted 75 chars>") || !json.Valid([]byte(raw)) {
		t.Fatalf("Event.Raw = %q, want populated with encrypted token elided", raw)
	}
	if len([]rune(raw)) > 4096 {
		t.Fatalf("Event.Raw rune count = %d, want bounded", len([]rune(raw)))
	}
}

func TestLoadEventsAttachesTokenCountUsageToAssistantTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-usage.jsonl")
	lines := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-01-02T03:04:01Z","type":"event_msg","payload":{"type":"user_message","message":"Chart the route"}}`,
		`{"timestamp":"2026-01-02T03:04:02Z","type":"event_msg","payload":{"type":"agent_message","message":"Route ready"}}`,
		`{"timestamp":"2026-01-02T03:04:03Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":45000,"cached_input_tokens":37000,"output_tokens":4000,"reasoning_output_tokens":1000,"total_tokens":45000}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatalf("LoadEvents() error = %v", err)
	}
	var withUsage []int
	for index, event := range session.Events {
		if event.Usage != nil {
			withUsage = append(withUsage, index)
		}
	}
	// The token_count closing the request attaches to the assistant reply, not the
	// user prompt, so context stays the whole prompt and flow adds reasoning.
	if len(withUsage) != 1 || session.Events[withUsage[0]].Kind != model.EventAssistantText {
		t.Fatalf("events carrying usage = %v, want the single assistant reply", withUsage)
	}
	usage := session.Events[withUsage[0]].Usage
	if usage.PromptTokens() != 45_000 || usage.FlowTokens() != 13_000 {
		t.Fatalf("attached usage prompt=%d flow=%d, want 45000 and 13000", usage.PromptTokens(), usage.FlowTokens())
	}
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
		OutputTokens:           30,
		CacheReadTokens:        50,
		InputIncludesCacheRead: true,
	}
	if len(session.Usage) != 1 || !reflect.DeepEqual(session.Usage[0], want) {
		t.Fatalf("Parse().Usage = %#v, want %#v", session.Usage, want)
	}
}

func TestCodexUsageCountsReasoningWithinOutput(t *testing.T) {
	got := codexUsage("gpt-5.6-sol", tokenUsage{
		OutputTokens:          100,
		ReasoningOutputTokens: 40,
	})
	want := model.Usage{
		Model:                  "gpt-5.6-sol",
		OutputTokens:           100,
		InputIncludesCacheRead: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codexUsage() = %#v, want %#v", got, want)
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
		OutputTokens:           7,
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
	// One user prompt plus one agent reply — the session's own conversation turns.
	if !reflect.DeepEqual(session.Models, []string{"gpt-5.6-sol"}) || session.Messages != 2 {
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

func TestParseDerivesTitleFromRealTask(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "rollout-work-preamble.jsonl", want: "Real Title"},
		{name: "rollout-advisor-preamble.jsonl", want: "Review request: compare lunar routes"},
		{name: "rollout-plain.jsonl", want: "Plot an imaginary comet route."},
		{name: "rollout-brief.jsonl", want: "Milestone Z"},
		{name: "rollout-preamble-only.jsonl", want: ""},
		{name: "rollout-advisor-preamble-only.jsonl", want: ""},
		{name: "rollout-unicode-preamble-only.jsonl", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join("testdata", "titles", test.name)
			session, err := testParser().Parse(path)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if session.Title != test.want {
				t.Fatalf("Parse().Title = %q, want %q", session.Title, test.want)
			}
		})
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

func TestParseTitlesSubagentFromAgentPath(t *testing.T) {
	session, err := testParser().Parse(filepath.Join("testdata", "rollout-subagent-title.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if session.Title != "review_x" {
		t.Fatalf("Parse().Title = %q, want delegated role leaf review_x", session.Title)
	}
}

func TestParseKeepsFirstSubagentMetadata(t *testing.T) {
	session, err := testParser().Parse(filepath.Join("testdata", "rollout-subagent-title.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "thread-review" || session.ParentID != "thread-root" || session.AgentPath != "/root/review_x" {
		t.Fatalf("Parse() identity = ID %q, parent %q, path %q", session.ID, session.ParentID, session.AgentPath)
	}
	if session.CWD != "/workspace/lunar-lab" || session.Project != "lunar-lab" {
		t.Fatalf("Parse() workspace = CWD %q, project %q", session.CWD, session.Project)
	}
	if got := session.TotalUsage().TotalTokens(); got != 128 {
		t.Fatalf("Parse() usage = %d tokens, want 128", got)
	}
}

func TestParseCalculatesEstimatedCodexCost(t *testing.T) {
	session, err := testParser().Parse(fixture("rollout-session-main.jsonl"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := 515.0
	if math.Abs(session.Cost.USD-want) > 1e-12 || !session.Cost.Estimated {
		t.Fatalf("Parse().Cost = %#v, want USD %v estimated", session.Cost, want)
	}
	wantBreakdowns := map[string]model.CostBreakdown{
		"gpt-5.6-sol": {
			Input: model.CostBuckets{{RatePerToken: 2, Tokens: 200}}, Output: model.CostBuckets{{RatePerToken: 3, Tokens: 30}},
			CacheRead: model.CostBuckets{{RatePerToken: 0.5, Tokens: 50}},
		},
	}
	if !reflect.DeepEqual(session.ModelCostBreakdowns, wantBreakdowns) {
		t.Fatalf("Parse().ModelCostBreakdowns = %#v, want %#v", session.ModelCostBreakdowns, wantBreakdowns)
	}
}

func TestParseDoesNotAlterCleanRootAccounting(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000,"output_tokens":100,"reasoning_output_tokens":40},"last_token_usage":{"input_tokens":150000,"output_tokens":100,"reasoning_output_tokens":40}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000,"output_tokens":200,"reasoning_output_tokens":80},"last_token_usage":{"input_tokens":150000,"output_tokens":100,"reasoning_output_tokens":40}}}}`,
	)
	wantInput := model.CostBuckets{{RatePerToken: 1, Tokens: 300_000}}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, wantInput) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want only base-rate bucket %#v", got, wantInput)
	}
	wantOutput := model.CostBuckets{{RatePerToken: 3, Tokens: 200}}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Output; !reflect.DeepEqual(got, wantOutput) {
		t.Fatalf("Parse().ModelCostBreakdowns output = %#v, want reasoning-inclusive output bucket %#v", got, wantOutput)
	}
	if session.ModelCosts["gpt-5.6"] != 300_600 || session.Cost.USD != 300_600 {
		t.Fatalf("Parse() costs = model %v, session %v; want 300600", session.ModelCosts["gpt-5.6"], session.Cost.USD)
	}
	wantUsage := []model.Usage{{Model: "gpt-5.6", InputTokens: 300_000, OutputTokens: 200, InputIncludesCacheRead: true}}
	if !reflect.DeepEqual(session.Usage, wantUsage) {
		t.Fatalf("Parse().Usage = %#v, want aggregate %#v", session.Usage, wantUsage)
	}
}

func TestParseExcludesForkReplayFromUsageAndCost(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent","agent_path":"/root/scout"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":450000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:20Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":600000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	wantUsage := []model.Usage{{Model: "gpt-5.6", InputTokens: 300_000, InputIncludesCacheRead: true}}
	if !reflect.DeepEqual(session.Usage, wantUsage) {
		t.Fatalf("Parse().Usage = %#v, want fork-owned aggregate %#v", session.Usage, wantUsage)
	}
	wantBuckets := model.CostBuckets{{RatePerToken: 1, Tokens: 300_000}}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, wantBuckets) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want fork-owned base tier %#v", got, wantBuckets)
	}
}

func TestParseDeduplicatesNonAdvancingRootTokenCount(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-root","thread_source":"user"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000,"cached_input_tokens":100000,"total_tokens":150000},"last_token_usage":{"input_tokens":150000,"cached_input_tokens":100000,"total_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000,"cached_input_tokens":100000,"total_tokens":150000},"last_token_usage":{"input_tokens":150000,"cached_input_tokens":100000,"total_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000,"cached_input_tokens":200000,"total_tokens":300000},"last_token_usage":{"input_tokens":150000,"cached_input_tokens":100000,"total_tokens":150000}}}}`,
	)

	wantInput := model.CostBuckets{{RatePerToken: 1, Tokens: 100_000}}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, wantInput) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want distinct-request base tier %#v", got, wantInput)
	}
	wantCache := model.CostBuckets{{RatePerToken: 0.1, Tokens: 200_000}}
	if got := session.ModelCostBreakdowns["gpt-5.6"].CacheRead; !reflect.DeepEqual(got, wantCache) {
		t.Fatalf("Parse().ModelCostBreakdowns cache read = %#v, want deduplicated base tier %#v", got, wantCache)
	}
	if got := session.TotalUsage().TotalTokens(); got != 300_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want final cumulative 300000", got)
	}
}

func TestParseExcludesForkReplayMessagesAndSubagents(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent","agent_path":"/root/scout"}}`,
		`{"timestamp":"2026-01-02T03:05:00.010Z","type":"event_msg","payload":{"type":"user_message","message":"Review the parent route"}}`,
		`{"timestamp":"2026-01-02T03:05:00.020Z","type":"event_msg","payload":{"type":"agent_message","message":"The parent route is ready"}}`,
		`{"timestamp":"2026-01-02T03:05:00.030Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-inherited","agent_path":"/root/scout/inherited","kind":"started"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:05Z","type":"event_msg","payload":{"type":"user_message","message":"Review the child route"}}`,
		`{"timestamp":"2026-01-02T03:05:06Z","type":"event_msg","payload":{"type":"agent_message","message":"The child route is ready"}}`,
		`{"timestamp":"2026-01-02T03:05:07Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-mapper","agent_path":"/root/scout/mapper","kind":"started"}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":450000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	if session.Messages != 2 {
		t.Fatalf("Parse().Messages = %d, want only two fork-owned messages", session.Messages)
	}
	if len(session.Subagents) != 1 || session.Subagents[0].ID != "thread-mapper" {
		t.Fatalf("Parse().Subagents = %#v, want only fork-owned mapper", session.Subagents)
	}
}

func TestParseUsesLastReplayTotalAsBaseline(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.200Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.300Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":50000}}}}`,
	)

	if got := session.TotalUsage().TotalTokens(); got != 50_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want 50000 tokens after replay baseline", got)
	}
}

func TestParseSubtractsReplayBaselineAcrossUsageFields(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000,"cached_input_tokens":75000,"total_tokens":100000},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":75000,"total_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000,"cached_input_tokens":150000,"total_tokens":200000},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":75000,"total_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000,"cached_input_tokens":225000,"total_tokens":300000},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":75000,"total_tokens":100000}}}}`,
	)

	want := []model.Usage{{Model: "gpt-5.6", InputTokens: 100_000, CacheReadTokens: 75_000, InputIncludesCacheRead: true}}
	if !reflect.DeepEqual(session.Usage, want) {
		t.Fatalf("Parse().Usage = %#v, want field-wise child aggregate %#v", session.Usage, want)
	}
}

func TestParseForkFallbackUsesBaselineSubtractedCumulative(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":450000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":600000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	wantUsage := []model.Usage{{Model: "gpt-5.6", InputTokens: 300_000, InputIncludesCacheRead: true}}
	if !reflect.DeepEqual(session.Usage, wantUsage) {
		t.Fatalf("Parse().Usage = %#v, want baseline-subtracted fallback %#v", session.Usage, wantUsage)
	}
	wantBuckets := model.CostBuckets{
		{RatePerToken: 1, Tokens: 272_000},
		{RatePerToken: 2, Tokens: 28_000, AboveThreshold: true},
	}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, wantBuckets) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want child-only fallback %#v", got, wantBuckets)
	}
}

func TestParseStopsReplaySkipAtFirstLaterSecond(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.950Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":400000},"last_token_usage":{"input_tokens":100000}}}}`,
	)

	if got := session.TotalUsage().TotalTokens(); got != 200_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want both 100000-token post-boundary requests", got)
	}
}

func TestParseDoesNotSkipWithoutCollapsedForkPrefix(t *testing.T) {
	tests := []struct {
		name   string
		meta   string
		second string
	}{
		{
			name:   "fork timestamps differ",
			meta:   `{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
			second: "2026-01-02T03:05:01Z",
		},
		{
			name:   "root timestamps match",
			meta:   `{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-root","thread_source":"user","source":"exec"}}`,
			second: "2026-01-02T03:05:00.900Z",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := parseTieredSession(t,
				test.meta,
				`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
				`{"timestamp":"`+test.second+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
			)

			if got := session.TotalUsage().TotalTokens(); got != 300_000 {
				t.Fatalf("Parse().TotalUsage() = %d, want all 300000 tokens", got)
			}
		})
	}
}

func TestParseDetectsForkedFromIDWithObjectSource(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","forked_from_id":"thread-root","source":{"subagent":{"thread_spawn":{"parent":"thread-root"}}}}}`,
		`{"timestamp":"2026-01-02T03:04:05.100Z","type":"session_meta","payload":{"id":"thread-root","thread_source":"user","source":"exec"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":450000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	if got := session.TotalUsage().TotalTokens(); got != 150_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want 150000 fork-owned tokens", got)
	}
}

func TestParseIgnoresForkMarkersAfterFirstSessionMetadata(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-root","thread_source":"user","source":"exec"}}`,
		`{"timestamp":"2026-01-02T03:04:05.100Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent","forked_from_id":"thread-root"}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	if got := session.TotalUsage().TotalTokens(); got != 300_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want all 300000 root tokens", got)
	}
}

func TestParseDoesNotSkipSingleForkTokenCount(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	if got := session.TotalUsage().TotalTokens(); got != 150_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want the only 150000-token request", got)
	}
}

func TestParseDoesNotSkipPastFirstTokenCountWithoutTimestampSecond(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-scout","thread_source":"subagent"}}`,
		`{"timestamp":"short","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.100Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:00.900Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":100000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":400000},"last_token_usage":{"input_tokens":100000}}}}`,
	)

	if got := session.TotalUsage().TotalTokens(); got != 400_000 {
		t.Fatalf("Parse().TotalUsage() = %d, want all 400000 tokens when the first timestamp has no second", got)
	}
}

func TestParseTracksCumulativeOnlyRecordForRelogDedup(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"thread-root","thread_source":"user"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:05:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000}}}}`,
		`{"timestamp":"2026-01-02T03:05:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
	)

	want := model.CostBuckets{
		{RatePerToken: 1, Tokens: 272_000},
		{RatePerToken: 2, Tokens: 28_000, AboveThreshold: true},
	}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want cumulative fallback %#v", got, want)
	}
}

func TestParsePricesOnlyLargeTurnAboveTier(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":450000},"last_token_usage":{"input_tokens":300000}}}}`,
	)
	want := model.CostBuckets{
		{RatePerToken: 1, Tokens: 422_000},
		{RatePerToken: 2, Tokens: 28_000, AboveThreshold: true},
	}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want only the large turn above tier %#v", got, want)
	}
}

func TestParsePricesMismatchedDeltasAsCumulativeFallback(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000,"output_tokens":400000},"last_token_usage":{"input_tokens":150000}}}}`,
	)
	want := model.CostBuckets{
		{RatePerToken: 1, Tokens: 272_000},
		{RatePerToken: 2, Tokens: 28_000, AboveThreshold: true},
	}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want cumulative fallback %#v", got, want)
	}
	if session.ModelCosts["gpt-5.6"] != 1_528_000 || session.Cost.USD != 1_528_000 {
		t.Fatalf("Parse() fallback costs = model %v, session %v; want 1528000", session.ModelCosts["gpt-5.6"], session.Cost.USD)
	}
	wantUsage := []model.Usage{{Model: "gpt-5.6", InputTokens: 300_000, OutputTokens: 400_000, InputIncludesCacheRead: true}}
	if !reflect.DeepEqual(session.Usage, wantUsage) {
		t.Fatalf("Parse().Usage = %#v, want full cumulative fallback %#v", session.Usage, wantUsage)
	}
}

func TestParsePricesDeltasWithoutCumulativeAsAggregateFallback(t *testing.T) {
	session := parseTieredSession(t,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":150000}}}}`,
	)
	want := model.CostBuckets{
		{RatePerToken: 1, Tokens: 272_000},
		{RatePerToken: 2, Tokens: 28_000, AboveThreshold: true},
	}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse().ModelCostBreakdowns input = %#v, want unverified deltas lumped %#v", got, want)
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

func TestParseDoesNotAddReasoningToMaximumOutput(t *testing.T) {
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
	want := []model.Usage{{Model: "gpt-5.4", OutputTokens: math.MaxInt64, InputIncludesCacheRead: true}}
	if !reflect.DeepEqual(session.Usage, want) {
		t.Fatalf("Parse().Usage = %#v, want reasoning included in maximum output %#v", session.Usage, want)
	}
}

func TestSubtractTokenUsageSubtractsEveryField(t *testing.T) {
	total := tokenUsage{
		InputTokens: 100, CachedInputTokens: 80, OutputTokens: 60, ReasoningOutputTokens: 40, TotalTokens: 160,
	}
	baseline := tokenUsage{
		InputTokens: 10, CachedInputTokens: 20, OutputTokens: 30, ReasoningOutputTokens: 40, TotalTokens: 50,
	}
	want := tokenUsage{
		InputTokens: 90, CachedInputTokens: 60, OutputTokens: 30, ReasoningOutputTokens: 0, TotalTokens: 110,
	}

	if !subtractTokenUsage(&total, &baseline) || total != want {
		t.Fatalf("subtractTokenUsage() = %#v, want %#v", total, want)
	}
}

func TestAdvanceTokenUsageMaxTracksTotalTokensIndependently(t *testing.T) {
	runningMax := tokenUsage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 60, ReasoningOutputTokens: 40, TotalTokens: 150}
	candidate := tokenUsage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 60, ReasoningOutputTokens: 40, TotalTokens: 160}

	if !advanceTokenUsageMax(&runningMax, &candidate) || runningMax != candidate {
		t.Fatalf("advanceTokenUsageMax() = %#v, want %#v", runningMax, candidate)
	}
	if advanceTokenUsageMax(&runningMax, &candidate) {
		t.Fatal("advanceTokenUsageMax() reported an unchanged duplicate as advancing")
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
	path := filepath.Join("testdata", "rollout-mirrored-messages.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 2 || session.Events[0].Kind != model.EventUser || session.Events[1].Kind != model.EventAssistantText {
		t.Fatalf("mirrored events = %#v, want one user and one assistant event", session.Events)
	}
	wantTimestamps := []time.Time{
		time.Date(2026, 1, 2, 3, 0, 0, 5_000_000, time.UTC),
		time.Date(2026, 1, 2, 3, 0, 1, 7_000_000, time.UTC),
	}
	for index, want := range wantTimestamps {
		if !session.Events[index].Timestamp.Equal(want) {
			t.Fatalf("mirrored event %d timestamp = %v, want preferred response_item timestamp %v", index, session.Events[index].Timestamp, want)
		}
	}
}

func TestLoadEventsCoalescesLongMirroredUserMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-long-mirrored-message.jsonl")
	message := strings.TrimSpace(strings.Repeat("  lunar telemetry segment\n", 300))
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00.000Z","type":"event_msg","payload":{"type":"user_message","message":` + strconv.Quote(message) + `}}`,
		`{"timestamp":"2026-01-02T03:00:00.009Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":` + strconv.Quote(message) + `}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	want := model.BoundedDetailText(model.CleanTimelineText(message))
	if len(session.Events) != 1 || session.Events[0].Kind != model.EventUser || session.Events[0].Text != want {
		t.Fatalf("long mirrored events = %#v, want one normalized user event", session.Events)
	}
}

func TestAppendCodexMessagePreservesDistinctSameKindMessages(t *testing.T) {
	session := &model.Session{}
	dedupTextByEvent := make(map[int]string)
	appendCodexMessage(session, model.Event{Kind: model.EventUser, Text: "Survey the northern ridge"}, false, "Survey the northern ridge", dedupTextByEvent)
	appendCodexMessage(session, model.Event{Kind: model.EventUser, Text: "Survey the southern ridge"}, false, "Survey the southern ridge", dedupTextByEvent)

	if len(session.Events) != 2 || session.Events[0].Text != "Survey the northern ridge" || session.Events[1].Text != "Survey the southern ridge" {
		t.Fatalf("same-kind messages = %#v, want both distinct messages", session.Events)
	}
}

func TestAppendCodexMessageDoesNotDeduplicateBeyondWindow(t *testing.T) {
	session := &model.Session{Events: []model.Event{{Kind: model.EventUser, Text: "Repeat the survey"}}}
	dedupTextByEvent := make(map[int]string)
	for index := range 16 {
		session.Events = append(session.Events, model.Event{Kind: model.EventThinking, Text: "Checkpoint " + strconv.Itoa(index)})
	}
	appendCodexMessage(session, model.Event{Kind: model.EventUser, Text: "Repeat the survey"}, false, "Repeat the survey", dedupTextByEvent)

	if len(session.Events) != 18 || session.Events[len(session.Events)-1].Text != "Repeat the survey" {
		t.Fatalf("events outside the dedup window = %#v, want repeated message appended", session.Events)
	}
}

func TestLoadEventsSuppressesWrapperPreamble(t *testing.T) {
	path := filepath.Join("testdata", "rollout-wrapper-preamble.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 || session.Events[0].Kind != model.EventUser || session.Events[0].Text != "Calibrate the orbital beacon." {
		t.Fatalf("timeline events = %#v, want only the genuine task body", session.Events)
	}
}

func TestLoadEventsPreservesTaskSuffixAfterWrapperPreamble(t *testing.T) {
	path := filepath.Join("testdata", "rollout-mixed-wrapper-task.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"# Brief — Calibrate lunar telemetry\nKeep every sample.",
		"# Brief — Chart the crater route\nAvoid the southern ridge.",
	}
	if len(session.Events) != len(want) {
		t.Fatalf("timeline events = %#v, want %d task suffixes", session.Events, len(want))
	}
	for index, text := range want {
		if session.Events[index].Kind != model.EventUser || session.Events[index].Text != text {
			t.Errorf("timeline event %d = %#v, want user text %q", index, session.Events[index], text)
		}
	}
}

func TestCodexTimelineUserMessagePreservesOrdinaryTurns(t *testing.T) {
	for _, message := range []string{
		"Deliver the complete implementation of the orbital clock.",
		"Work from the task below to chart the lunar pass.",
	} {
		if got := codexTimelineUserMessage(message); got != message {
			t.Errorf("codexTimelineUserMessage(%q) = %q, want ordinary turn preserved", message, got)
		}
	}
}

func TestCodexTimelineUserMessageHandlesTruncatedTag(t *testing.T) {
	message := "<instructions>\nFictional wrapper text\n<"
	if got := codexTimelineUserMessage(message); got != "" {
		t.Fatalf("codexTimelineUserMessage() = %q, want empty truncated preamble", got)
	}
}

func TestCodexTimelineUserMessageStripsConcatenatedTags(t *testing.T) {
	message := strings.Repeat("<INSTRUCTIONS></INSTRUCTIONS>", 1_000) + "Chart the fictional moon."
	if got := codexTimelineUserMessage(message); got != "Chart the fictional moon." {
		t.Fatalf("codexTimelineUserMessage() = %q, want task after concatenated tags", got)
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

func TestLoadEventsPreservesCodexPatchDetail(t *testing.T) {
	path := filepath.Join("testdata", "tool-detail", "rollout-tool-detail.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	if call.ToolInput != "+1 −1" {
		t.Fatalf("ToolInput = %q, want unchanged diff statistic", call.ToolInput)
	}
	want := "*** Begin Patch\n*** Update File: /workspace/lunar-lab/route.txt\n@@\n-ridge\n+valley\n*** End Patch"
	if call.Detail == nil || call.Detail.Diff != want {
		t.Fatalf("Detail = %#v, want unchanged patch body", call.Detail)
	}
}

func TestLoadEventsPreservesCodexExecInputDetail(t *testing.T) {
	path := filepath.Join("testdata", "tool-detail", "rollout-tool-detail.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	want := "survey-route --all\nprintf 'done\\n'"
	call := session.Events[2]
	if call.ToolInput != want {
		t.Fatalf("ToolInput = %q, want unchanged command", call.ToolInput)
	}
	if call.Detail == nil || call.Detail.Input != want {
		t.Fatalf("Detail = %#v, want multiline command", call.Detail)
	}
}

func TestLoadEventsExtractsCommandFromCodexExecTool(t *testing.T) {
	path := filepath.Join("testdata", "tool-detail", "rollout-exec-tool.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 {
		t.Fatalf("events = %#v, want one exec call", session.Events)
	}
	call := session.Events[0]
	if call.ToolName != "exec_command" || call.ToolInput != "go test ./..." {
		t.Fatalf("tool call = %#v, want wrapped shell summarized as exec_command(go test ./...)", call)
	}
	if call.Detail == nil || call.Detail.Input != "go test ./..." {
		t.Fatalf("Detail = %#v, want extracted command", call.Detail)
	}
}

func TestLoadEventsResolvesNonShellCodexExecTool(t *testing.T) {
	path := filepath.Join("testdata", "tool-detail", "rollout-exec-update-plan.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 {
		t.Fatalf("events = %#v, want one update_plan call", session.Events)
	}
	call := session.Events[0]
	fullInput := `const r = await tools.update_plan({"plan":[{"step":"Chart the fictional ridge","status":"in_progress"}]}); text(r);`
	if call.ToolName != "update_plan" {
		t.Fatalf("ToolName = %q, want resolved update_plan", call.ToolName)
	}
	if call.ToolInput == fullInput || strings.Contains(call.ToolInput, "const r = await") {
		t.Fatalf("ToolInput = %q, want a short semantic hint or no preview", call.ToolInput)
	}
	if call.Detail == nil || call.Detail.Input != fullInput {
		t.Fatalf("Detail = %#v, want full wrapper input %q", call.Detail, fullInput)
	}
}

func TestLoadEventsResolvesOnlyOneExecutableNestedTool(t *testing.T) {
	path := filepath.Join("testdata", "tool-detail", "rollout-exec-call-sites.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 2 {
		t.Fatalf("events = %#v, want two exec calls", session.Events)
	}
	decoy, mixed := session.Events[0], session.Events[1]
	if decoy.ToolName != "exec_command" || decoy.ToolInput != "go test" || decoy.Detail == nil || decoy.Detail.Input != "go test" {
		t.Errorf("decoy wrapper = %#v, want the only executable call resolved as exec_command(go test)", decoy)
	}
	if mixed.ToolName != "exec" || mixed.ToolInput != "" || mixed.Detail == nil || !strings.Contains(mixed.Detail.Input, "tools.update_plan") || !strings.Contains(mixed.Detail.Input, "tools.exec_command") {
		t.Errorf("mixed wrapper = %#v, want unresolved exec with no raw-JS preview and full detail", mixed)
	}
}

func TestCodexExecToolRendersWrappedApplyPatch(t *testing.T) {
	wantDiff := "*** Begin Patch\n*** Update File: /workspace/orbit/route.go\n@@\n-old ridge\n+new ridge\n*** End Patch"
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "variable reference", input: `const patch = "*** Begin Patch\n*** Update File: /workspace/orbit/route.go\n@@\n-old ridge\n+new ridge\n*** End Patch"; text(await tools.apply_patch(patch));`},
		{name: "inline argument", input: `text(await tools.apply_patch("*** Begin Patch\n*** Update File: /workspace/orbit/route.go\n@@\n-old ridge\n+new ridge\n*** End Patch"));`},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview, detail := codexExecToolPresentation(test.input)
			if preview != "+1 −1" {
				t.Errorf("preview = %q, want +1 −1", preview)
			}
			if detail == nil || detail.Diff != wantDiff {
				t.Errorf("Detail = %#v, want diff %q", detail, wantDiff)
			}
			if detail != nil && strings.Contains(detail.Input, "tools.apply_patch") {
				t.Errorf("Detail.Input = %q, want the diff envelope, not the raw JS wrapper", detail.Input)
			}
		})
	}
}

func TestCodexExecToolRejectsOversizedNestedName(t *testing.T) {
	input := "const r = await tools." + strings.Repeat("a", 97) + "({}); text(r);"
	if name, _, ok := codexExecTool(input); ok {
		t.Fatalf("codexExecTool() resolved oversized name %q", name)
	}
}

func TestCodexExecToolIgnoresToolsWithinUnicodeIdentifier(t *testing.T) {
	input := `const πtools = {exec_command: () => "local"}; πtools.exec_command({"cmd":"printf FICTIONAL_DECOY"}); const r = await tools.update_plan({"plan":[]}); text(r);`
	name, _, ok := codexExecTool(input)
	if !ok || name != "update_plan" {
		t.Fatalf("codexExecTool() = %q, %v, want only global update_plan call", name, ok)
	}
}

func TestCodexExecToolExtractsJavaScriptCommandStrings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "unquoted key",
			input: `const r = await tools.exec_command({cmd:"go test ./...", workdir:"/x"}); text(r.output);`,
			want:  "go test ./...",
		},
		{
			name:  "quoted JSON key",
			input: `const r = await tools.exec_command({"cmd":"make"}); text(r.output);`,
			want:  "make",
		},
		{
			name:  "quoted JSON Unicode escape",
			input: `const r = await tools.exec_command({"cmd":"printf \uD83D\uDE80"}); text(r.output);`,
			want:  "printf 🚀",
		},
		{
			name:  "quoted JSON isolated surrogate",
			input: `const r = await tools.exec_command({"cmd":"printf \uD800"}); text(r.output);`,
			want:  "printf �",
		},
		{
			name:  "JavaScript hex escape",
			input: `const r = await tools.exec_command({cmd:"printf \xE9"}); text(r.output);`,
			want:  "printf é",
		},
		{
			name:  "multiline double quoted",
			input: `const r = await tools.exec_command({cmd:"make build\nprintf 'done'"}); text(r.output);`,
			want:  "make build\nprintf 'done'",
		},
		{
			name:  "braces and escaped quotes",
			input: `const r = await tools.exec_command({cmd:"printf \"{ridge}\" && printf '}'", workdir:"/x"}); text(r.output);`,
			want:  `printf "{ridge}" && printf '}'`,
		},
		{
			name:  "single quoted",
			input: `const r = await tools.exec_command({cmd:'printf \'{ridge}\''}); text(r.output);`,
			want:  `printf '{ridge}'`,
		},
		{
			name:  "backtick quoted",
			input: "const r = await tools.exec_command({cmd:`make build\\nprintf \\`{ridge}\\``}); text(r.output);",
			want:  "make build\nprintf `{ridge}`",
		},
		{
			name:  "escaped backtick interpolation",
			input: "const r = await tools.exec_command({cmd:`printf \\${target}`}); text(r.output);",
			want:  "printf ${target}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview, detail := codexExecToolPresentation(test.input)
			if want := strings.SplitN(test.want, "\n", 2)[0]; preview != want {
				t.Errorf("preview = %q, want %q", preview, want)
			}
			if detail == nil || detail.Input != test.want {
				t.Errorf("Detail = %#v, want full command %q", detail, test.want)
			}
		})
	}
}

func TestCodexExecToolFallsBackForMalformedWrapper(t *testing.T) {
	input := `const r = await tools.exec_command({"cmd":); text(r.output);`
	if got := codexToolInput("exec", input); got != input {
		t.Fatalf("codexToolInput() = %q, want compact raw fallback", got)
	}
	if got := codexToolDetail("exec", input).Input; got != input {
		t.Fatalf("Detail.Input = %q, want pretty-input fallback", got)
	}
}

func TestCodexExecToolFallsBackForDynamicCommand(t *testing.T) {
	tests := []string{
		`const r = await tools.exec_command({workdir: ok ? "cmd" : "/tmp"}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test " + target}); text(r.output);`,
		"const r = await tools.exec_command({cmd:`go test ${target}`}); text(r.output);",
		`const r = await tools.exec_command({cmd:"go test", cmd:null}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test", workdir:}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test", ...options}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test", ["cmd"]:"stop"}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test", ...ok ? {cmd:"stop"} : {}}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test", broken:@}); text(r.output);`,
		`const r = await tools.exec_command({cmd:"go test"} && {cmd:"stop"}); text(r.output);`,
		"const r = await tools.exec_command({cmd:\"go test\nnext\"}); text(r.output);",
		"const r = await tools.exec_command({cmd:'go test\nnext'}); text(r.output);",
	}
	for _, input := range tests {
		preview, detail := codexExecToolPresentation(input)
		if want := strings.Join(strings.Fields(input), " "); preview != want {
			t.Errorf("preview for %q = %q, want raw fallback %q", input, preview, want)
		}
		if detail == nil || detail.Input != input {
			t.Errorf("Detail for %q = %#v, want raw wrapper fallback", input, detail)
		}
	}
}

func TestCodexExecToolDoesNotBorrowCommandFromComment(t *testing.T) {
	input := `const payload = makeArgs(); const r = await tools.exec_command(payload /* {"cmd":"printf FICTIONAL_DECOY"} */); text(r.output);`
	if command, ok := codexExecCommand(input); ok || command != "" {
		t.Fatalf("codexExecCommand() = %q, %v, want no command for indirect arguments", command, ok)
	}
	if got := codexToolInput("exec", input); got == "printf FICTIONAL_DECOY" {
		t.Fatalf("codexToolInput() borrowed comment JSON: %q", got)
	}
	if detail := codexToolDetail("exec", input); detail == nil || detail.Input != input {
		t.Fatalf("Detail = %#v, want full wrapper for audit", detail)
	}
}

func TestCodexExecToolFallsBackWithoutStringCommand(t *testing.T) {
	input := `const r = await tools.exec_command({}); text(r.output);`
	if got := codexToolInput("exec", input); got != input {
		t.Fatalf("codexToolInput() = %q, want raw fallback without cmd", got)
	}
	if got := codexToolDetail("exec", input).Input; got != input {
		t.Fatalf("Detail.Input = %q, want raw fallback without cmd", got)
	}
}

func TestCodexExecToolFallsBackForNullOrEmptyCommand(t *testing.T) {
	for _, input := range []string{
		`const r = await tools.exec_command({"cmd":null}); text(r.output);`,
		`const r = await tools.exec_command({"cmd":""}); text(r.output);`,
	} {
		if got := codexToolInput("exec", input); got != input {
			t.Errorf("codexToolInput(%q) = %q, want raw fallback", input, got)
		}
		if got := codexToolDetail("exec", input).Input; got != input {
			t.Errorf("Detail.Input for %q = %q, want raw fallback", input, got)
		}
	}
}

func TestLoadEventsPreservesCodexExecOutputDetail(t *testing.T) {
	path := filepath.Join("testdata", "tool-detail", "rollout-tool-detail.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[2]
	if call.ResultSummary != "exit 3" {
		t.Fatalf("ResultSummary = %q, want unchanged exit summary", call.ResultSummary)
	}
	want := "Process exited with code 3\nFinal output:\nroute blocked\nretry advised"
	if call.Detail == nil || call.Detail.Output != want {
		t.Fatalf("Detail = %#v, want newline-preserving output", call.Detail)
	}
}

func TestLoadEventsStripsCodexExecCompletedPreamble(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-exec-output.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call-exec","name":"exec","input":"const r = await tools.exec_command({\"cmd\":\"make build\"}); text(r.output);"}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-exec","output":"Script completed\nWall time 2.1 seconds\nOutput:\nroute clear\nretry advised"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	if call.Detail == nil || call.Detail.Output != "route clear\nretry advised" {
		t.Fatalf("Detail.Output = %q, want preamble-free body", call.Detail.Output)
	}
	if call.ResultSummary != "route clear retry advised" {
		t.Fatalf("ResultSummary = %q, want body summary", call.ResultSummary)
	}
}

func TestLoadEventsPreservesCodexExecPreambleExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-exec-failure.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call-exec","name":"exec","input":"const r = await tools.exec_command({\"cmd\":\"make build\"}); text(r.output);"}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-exec","output":"Process exited with code 2\nWall time 0.2 seconds\nFinal output:\nbuild failed\nretry advised"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	if call.Detail == nil || call.Detail.Output != "build failed\nretry advised" {
		t.Fatalf("Detail.Output = %q, want preamble-free failure body", call.Detail.Output)
	}
	if call.ResultSummary != "exit 2" {
		t.Fatalf("ResultSummary = %q, want preserved preamble exit", call.ResultSummary)
	}
}

func TestLoadEventsStripsCodexWaitRunningPreamble(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-wait-output.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call-wait","name":"wait","input":"{\"cell_id\":\"17\"}"}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-wait","output":"Script running with cell ID 17\nWall time 30 seconds\nOutput:\nstill running"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	if call.Detail == nil || call.Detail.Output != "still running" || call.ResultSummary != "still running" {
		t.Fatalf("wait call = %#v, want preamble-free output and summary", call)
	}
}

func TestCodexStripExecPreambleLeavesOrdinaryOutputUnchanged(t *testing.T) {
	output := "route clear\n  retry advised  "
	if got := codexStripExecPreamble(output); got != output {
		t.Fatalf("codexStripExecPreamble() = %q, want exact ordinary output", got)
	}
}

func TestCodexStripExecPreambleKeepsHeaderLikeOutputWithoutWallTime(t *testing.T) {
	output := "Script completed\nimportant diagnostic\nOutput:\nroute clear"
	if got := codexStripExecPreamble(output); got != output {
		t.Fatalf("codexStripExecPreamble() = %q, want non-preamble output unchanged", got)
	}
}

func TestCodexStripExecPreambleRequiresRunningCellID(t *testing.T) {
	output := "Script running with cell ID \nWall time 2.1 seconds\nOutput:\nroute clear"
	if got := codexStripExecPreamble(output); got != output {
		t.Fatalf("codexStripExecPreamble() = %q, want invalid running header unchanged", got)
	}
}

func TestCodexStripExecPreambleAcceptsOpaqueRunningCellID(t *testing.T) {
	output := "Script running with cell ID cell-alpha-17\nWall time 2.1 seconds\nOutput:\nroute clear"
	if got := codexStripExecPreamble(output); got != "route clear" {
		t.Fatalf("codexStripExecPreamble() = %q, want body for opaque cell ID", got)
	}
}

func TestCodexStripExecPreambleHandlesImmediateProcessOutput(t *testing.T) {
	output := "Process exited with code 2\nFinal output:\nbuild failed"
	if got := codexStripExecPreamble(output); got != "build failed" {
		t.Fatalf("codexStripExecPreamble() = %q, want failure body", got)
	}
	if code, ok := codexExecPreambleExit(output); !ok || code != "2" {
		t.Fatalf("codexExecPreambleExit() = %q, %v, want 2, true", code, ok)
	}
}

func TestCodexToolDetailPrettyPrintsOtherInputs(t *testing.T) {
	detail := codexToolDetail("view_image", `{"path":"/workspace/lunar-lab/map.png","detail":"high"}`)
	want := "{\n  \"path\": \"/workspace/lunar-lab/map.png\",\n  \"detail\": \"high\"\n}"
	if detail == nil || detail.Input != want {
		t.Fatalf("codexToolDetail() = %#v, want pretty multiline input", detail)
	}
}

func TestCodexToolDetailLeavesDeepJSONRaw(t *testing.T) {
	input := `{"query":` + strings.Repeat("[", 100) + "0" + strings.Repeat("]", 100) + "}"
	detail := codexToolDetail("custom_tool", input)
	if detail == nil || detail.Input != input {
		t.Fatalf("codexToolDetail() expanded deeply nested input to %d bytes", len(detail.Input))
	}
}

func TestCodexToolDetailPreservesRawInputWhitespace(t *testing.T) {
	input := "  custom call\n    indented body\n"
	detail := codexToolDetail("custom_tool", input)
	if detail == nil || detail.Input != input {
		t.Fatalf("codexToolDetail() = %#v, want exact raw input", detail)
	}
}

func TestCodexToolDetailBoundsEveryField(t *testing.T) {
	value := "start\n" + strings.Repeat("界", 5000) + "\nend"
	execInput, err := json.Marshal(map[string]string{"cmd": value})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := codexToolDetail("exec_command", string(execInput)).Input, model.BoundedDetailText(value); got != want {
		t.Fatalf("bounded Input has %d runes, want %d", len([]rune(got)), len([]rune(want)))
	}
	if got, want := codexToolDetail("apply_patch", value).Diff, model.BoundedDetailText(value); got != want {
		t.Fatalf("bounded Diff has %d runes, want %d", len([]rune(got)), len([]rune(want)))
	}

	path := filepath.Join(t.TempDir(), "rollout-bounded-output.jsonl")
	call := map[string]any{
		"type": "response_item",
		"payload": map[string]any{
			"type": "function_call", "call_id": "call-exec", "name": "exec_command",
			"arguments": string(execInput),
		},
	}
	result := map[string]any{
		"type": "response_item",
		"payload": map[string]any{
			"type": "function_call_output", "call_id": "call-exec", "output": value,
		},
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
	session := &model.Session{Path: path, Agent: model.AgentCodex}
	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if got, want := session.Events[0].Detail.Output, model.BoundedDetailText(value); got != want {
		t.Fatalf("bounded Output has %d runes, want %d", len([]rune(got)), len([]rune(want)))
	}
}

func TestCodexResultAcceptsContentBlockOutput(t *testing.T) {
	output := json.RawMessage(`[{"type":"output_text","text":"Process exited with code 7\nFinal output:\nfailed"}]`)
	if got := codexResultSummary(codexOutputText(output)); got != "exit 7" {
		t.Fatalf("content-block result = %q, want exit summary", got)
	}
}

func TestCodexOutputTextPreservesBlockBoundaryNewlines(t *testing.T) {
	output := json.RawMessage(`[{"type":"output_text","text":"\nfirst\n"},{"type":"output_text","text":"second\n"}]`)
	if got := codexOutputText(output); got != "\nfirst\n\nsecond\n" {
		t.Fatalf("codexOutputText() = %q, want exact joined blocks", got)
	}
}

func TestLoadEventsDoesNotAttachCodexDetailWithoutCallID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-missing-call-id.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"survey-route\"}"}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"response_item","payload":{"type":"function_call_output","output":"unrelated output"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if output := session.Events[0].Detail.Output; output != "" {
		t.Fatalf("Detail.Output = %q, want unlinked result", output)
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

func TestCodexElideEncryptedPreservesShortAndPlainText(t *testing.T) {
	for _, input := range []string{
		"ordinary lunar telemetry",
		"gAAAAshort",
		"gAAAA" + strings.Repeat("a", 58),
	} {
		if got := codexElideEncrypted(input); got != input {
			t.Errorf("codexElideEncrypted(%q) = %q, want unchanged", input, got)
		}
	}
}

func TestCodexElideEncryptedReplacesFernetToken(t *testing.T) {
	token := "gAAAA" + strings.Repeat("a", 59)
	if got := codexElideEncrypted(token); got != "<encrypted 64 chars>" {
		t.Fatalf("codexElideEncrypted() = %q, want 64-character placeholder", got)
	}
}

func TestCodexElideEncryptedPreservesSurroundingJSON(t *testing.T) {
	token := "gAAAA" + strings.Repeat("Ab9_-", 12) + "=="
	input := `{"target":"/root/x","message":"` + token + `","plain":"visible"}`
	want := `{"target":"/root/x","message":"<encrypted 67 chars>","plain":"visible"}`
	got := codexElideEncrypted(input)
	if got != want {
		t.Fatalf("codexElideEncrypted() = %q, want %q", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("codexElideEncrypted() produced invalid JSON: %q", got)
	}
}

func TestCodexElideEncryptedReplacesMultipleTokens(t *testing.T) {
	first := "gAAAA" + strings.Repeat("b", 59)
	second := "gAAAA" + strings.Repeat("C7_-", 15) + "="
	input := "gAAAAshort " + first + " between " + second + " done"
	want := "gAAAAshort <encrypted 64 chars> between <encrypted 66 chars> done"
	if got := codexElideEncrypted(input); got != want {
		t.Fatalf("codexElideEncrypted() = %q, want %q", got, want)
	}
}

func TestCodexElideEncryptedPreservesEmbeddedPrefix(t *testing.T) {
	input := "prefixgAAAA" + strings.Repeat("a", 59)
	if got := codexElideEncrypted(input); got != input {
		t.Fatalf("codexElideEncrypted() = %q, want embedded prefix unchanged", got)
	}
}

func TestLoadEventsElidesEncryptedToolInput(t *testing.T) {
	path := filepath.Join("testdata", "rollout-encrypted-payloads.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	for label, input := range map[string]string{
		"ToolInput":    call.ToolInput,
		"Detail.Input": call.Detail.Input,
	} {
		if !strings.Contains(input, `"target"`) || !strings.Contains(input, "/root/x") || !strings.Contains(input, "<encrypted 64 chars>") {
			t.Errorf("%s = %q, want target and encrypted placeholder", label, input)
		}
		if strings.Contains(input, "gAAAA") {
			t.Errorf("%s = %q, want encrypted token removed", label, input)
		}
		if !json.Valid([]byte(input)) {
			t.Errorf("%s = %q, want valid JSON", label, input)
		}
	}
}

func TestLoadEventsElidesEncryptedToolOutput(t *testing.T) {
	path := filepath.Join("testdata", "rollout-encrypted-payloads.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	call := session.Events[0]
	for label, output := range map[string]string{
		"Detail.Output": call.Detail.Output,
		"ResultSummary": call.ResultSummary,
	} {
		if !strings.Contains(output, "<encrypted 64 chars>") || !strings.Contains(output, "delivered") {
			t.Errorf("%s = %q, want encrypted placeholder and sibling status", label, output)
		}
		if strings.Contains(output, "gAAAA") {
			t.Errorf("%s = %q, want encrypted token removed", label, output)
		}
	}
}

func TestLoadEventsElidesEncryptedMessageText(t *testing.T) {
	path := filepath.Join("testdata", "rollout-encrypted-payloads.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	wantSibling := map[model.EventKind]string{
		model.EventUser:          `"sender":"/root/x"`,
		model.EventThinking:      `"phase":"planning"`,
		model.EventAssistantText: `"reply":"ready"`,
	}
	for kind, sibling := range wantSibling {
		var text string
		for _, event := range session.Events {
			if event.Kind == kind {
				text = event.Text
				break
			}
		}
		if !strings.Contains(text, "<encrypted 64 chars>") || !strings.Contains(text, sibling) {
			t.Errorf("%s text = %q, want encrypted placeholder and sibling field", kind, text)
		}
		if strings.Contains(text, "gAAAA") {
			t.Errorf("%s text = %q, want encrypted token removed", kind, text)
		}
	}
}

func TestLoadEventsPreservesPlaintextToolMessage(t *testing.T) {
	path := filepath.Join("testdata", "rollout-encrypted-payloads.jsonl")
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	want := `{"target":"/root/x","message":"Review the lunar route."}`
	for _, event := range session.Events {
		if event.Kind == model.EventToolCall && event.ToolName == "send_message" {
			if event.ToolInput != want {
				t.Fatalf("plaintext ToolInput = %q, want %q", event.ToolInput, want)
			}
			if event.Detail == nil || !strings.Contains(event.Detail.Input, "Review the lunar route.") {
				t.Fatalf("plaintext Detail.Input = %#v, want message preserved", event.Detail)
			}
			return
		}
	}
	t.Fatal("plaintext send_message tool call not found")
}

func TestLoadEventsPreservesDistinctEncryptedMessages(t *testing.T) {
	first := `{"encrypted_content":"gAAAA` + strings.Repeat("a", 59) + `"}`
	second := `{"encrypted_content":"gAAAA` + strings.Repeat("b", 59) + `"}`
	path := filepath.Join(t.TempDir(), "rollout-encrypted-mirrors.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00.000Z","type":"event_msg","payload":{"type":"user_message","message":` + strconv.Quote(first) + `}}`,
		`{"timestamp":"2026-01-02T03:00:00.005Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":` + strconv.Quote(first) + `}]}}`,
		`{"timestamp":"2026-01-02T03:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":` + strconv.Quote(second) + `}}`,
		`{"timestamp":"2026-01-02T03:00:01.005Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":` + strconv.Quote(second) + `}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentCodex}

	if err := testParser().LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 2 {
		t.Fatalf("encrypted messages = %#v, want two distinct events with mirrored copies coalesced", session.Events)
	}
	wantText := `{"encrypted_content":"<encrypted 64 chars>"}`
	for index, event := range session.Events {
		if event.Kind != model.EventUser || event.Text != wantText {
			t.Errorf("encrypted event %d = %#v, want elided user message", index, event)
		}
		wantTimestamp := time.Date(2026, 1, 2, 3, 0, index, 5_000_000, time.UTC)
		if !event.Timestamp.Equal(wantTimestamp) {
			t.Errorf("encrypted event %d timestamp = %v, want preferred mirror timestamp %v", index, event.Timestamp, wantTimestamp)
		}
	}
}
