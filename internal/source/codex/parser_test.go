package codex

import (
	"math"
	"path/filepath"
	"reflect"
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
