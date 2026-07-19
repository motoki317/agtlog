package claude

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
