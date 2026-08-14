package tui

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

var goldenNow = time.Date(2026, 1, 2, 6, 4, 0, 0, time.UTC)

func TestGoldenListFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	subagent := &model.Session{
		ID: "builder", Agent: model.AgentClaude, Models: []string{"claude-opus-4-8"},
		Usage: []model.Usage{{InputTokens: 180_000, OutputTokens: 20_000}}, Cost: model.Cost{USD: 1.02},
	}
	sessions := []*model.Session{
		{
			ID: "launch", Agent: model.AgentClaude, Path: "/workspace/starship/launch.jsonl", Project: "starship", Title: "Plan the lunar launch",
			Models: []string{"claude-opus-4-8"}, UpdatedAt: goldenNow.Add(-10 * time.Minute), Messages: 12,
			Usage: []model.Usage{{InputTokens: 900_000, OutputTokens: 100_000}}, Cost: model.Cost{USD: 4.21}, Subagents: []*model.Session{subagent},
		},
		{
			ID: "harbor", Agent: model.AgentCodex, Path: "/workspace/harbor/route.jsonl", Project: "harbor", Title: "Chart autonomous route",
			Models: []string{"gpt-5.6-sol"}, UpdatedAt: goldenNow.Add(-2 * time.Hour), Messages: 8,
			Usage: []model.Usage{{InputTokens: 88_000, OutputTokens: 4_000}}, Cost: model.Cost{USD: 0.76},
		},
	}
	m := newModelWithClock(sessions, nil, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 16))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)

	teatest.RequireEqualOutput(t, []byte(normalizeGolden(final.View())))
}

func TestGoldenDetailFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	child := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout terrain", Models: []string{"claude-opus-4-8"},
		Usage: []model.Usage{{InputTokens: 42_000, OutputTokens: 3_000}}, Cost: model.Cost{USD: 0.32},
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Inspect the ridge"},
			{Kind: model.EventAssistantText, Text: "The ridge is clear"},
		},
	}
	unattributedUsage := model.Usage{InputTokens: 120_000, OutputTokens: 8_000, InputIncludesCacheRead: true}
	parent := &model.Session{
		ID: "route", Agent: model.AgentCodex, Path: "/workspace/starship/route.jsonl", CWD: "/workspace/starship", Project: "starship", Title: "Plan route",
		Models: []string{"gpt-5.6-sol"}, GitBranch: "orbit/alpha", StartedAt: goldenNow.Add(-20 * time.Minute), UpdatedAt: goldenNow,
		Usage: []model.Usage{{InputTokens: 120_000, OutputTokens: 8_000}}, Cost: model.Cost{USD: 0.84}, Subagents: []*model.Session{child},
		Events: []model.Event{
			{Timestamp: goldenNow.Add(-14 * time.Minute), Kind: model.EventUser, Text: "Delegate the survey"},
			{Timestamp: goldenNow.Add(-13 * time.Minute), Kind: model.EventThinking, Text: "Select a safe path"},
			{Timestamp: goldenNow.Add(-12 * time.Minute), Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/starship/map.go", ResultSummary: "map ready", Duration: 400 * time.Millisecond, Detail: &model.ToolDetail{Input: "/workspace/starship/map.go", Output: "map ready"}},
			{Timestamp: goldenNow.Add(-11 * time.Minute), Kind: model.EventUsage, Model: "gpt-5.6-sol", Text: "unattributed usage", Usage: &unattributedUsage, Cost: model.CostBreakdown{Input: model.CostBuckets{{RatePerToken: 0.000007, Tokens: 120_000}}}, Priced: true},
			{Timestamp: goldenNow.Add(-11 * time.Minute), Kind: model.EventAssistantText, Text: "Survey delegated"},
			{Timestamp: goldenNow.Add(-10 * time.Minute), Kind: model.EventSubagent, ToolName: "Agent", Subagent: child},
		},
	}
	m := newModelWithClock([]*model.Session{parent}, nil, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 18))
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyDown},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
	} {
		tm.Send(key)
	}
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(string(output), "Task(Scout terrain)") }, teatest.WithDuration(time.Second))
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)

	teatest.RequireEqualOutput(t, []byte(normalizeGolden(final.View())))
}

func TestGoldenLazySubagentFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	child := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Path: "/workspace/starship/scout.jsonl", Project: "starship", Title: "Scout terrain",
		Models: []string{"claude-opus-4-8"}, UpdatedAt: goldenNow.Add(-2 * time.Minute),
		Usage: []model.Usage{{InputTokens: 42_000, OutputTokens: 3_000}}, Cost: model.Cost{USD: 0.32},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/starship/route.jsonl", Project: "starship", Title: "Plan route",
		Models: []string{"claude-opus-4-8"}, UpdatedAt: goldenNow, Subagents: []*model.Session{child},
	}
	childGate := make(chan struct{})
	defer func() {
		select {
		case <-childGate:
		default:
			close(childGate)
		}
	}()
	registry := source.NewRegistry([]source.Source{detailTestSource{
		session: root,
		loadNodeEvents: func(ctx context.Context, loaded *model.Session) error {
			switch loaded.ID {
			case root.ID:
				loaded.Events = []model.Event{
					{Kind: model.EventUser, Text: "Delegate the survey"},
					{Kind: model.EventSubagent, ToolName: "Agent", Subagent: loaded.Subagents[0]},
				}
			case child.ID:
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-childGate:
				}
				loaded.Events = []model.Event{
					{Kind: model.EventUser, Text: "Inspect the ridge"},
					{Kind: model.EventAssistantText, Text: "The ridge is clear"},
				}
			}
			return nil
		},
	}}, source.Options{})
	m := newModelWithClock([]*model.Session{root}, registry, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 18))
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return strings.Contains(string(output), "Delegate the survey")
	}, teatest.WithDuration(time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return strings.Contains(string(output), "Loading timeline…")
	}, teatest.WithDuration(time.Second))
	close(childGate)
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool {
		return strings.Contains(string(output), "The ridge is clear")
	}, teatest.WithDuration(time.Second))
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)

	teatest.RequireEqualOutput(t, []byte(normalizeGolden(final.View())))
}

func TestGoldenItemFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	raw := []byte(`{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"tool_use","id":"call-route","name":"Edit"}]}}`)
	toolUsage := model.Usage{Model: "claude-opus-4-8", InputTokens: 32_000, OutputTokens: 4_000, CacheReadTokens: 32_000}
	path := filepath.Join(t.TempDir(), "route.jsonl")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: path, CWD: "/workspace/starship", Project: "starship", Title: "Plot route",
		Models: []string{"claude-opus-4-8"}, GitBranch: "orbit/alpha", StartedAt: goldenNow.Add(-12 * time.Minute), UpdatedAt: goldenNow,
		Usage: []model.Usage{{InputTokens: 64_000, OutputTokens: 4_000}}, Cost: model.Cost{USD: 0.48},
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Adjust the lunar route"},
			{Timestamp: goldenNow.Add(-5 * time.Minute), Kind: model.EventToolCall, Model: "claude-opus-4-8", ToolName: "Edit", ToolInput: "/workspace/starship/route.go", CallID: "call-route", AgentID: "agent-builder", ResultSummary: "route updated", Duration: 750 * time.Millisecond,
				Usage: &toolUsage, Priced: true, Cost: model.CostBreakdown{
					Input: model.CostBuckets{{RatePerToken: 0.000005, Tokens: 32_000}}, CacheRead: model.CostBuckets{{RatePerToken: 0.0000005, Tokens: 32_000}},
					Output: model.CostBuckets{{RatePerToken: 0.000025, Tokens: 4_000}},
				},
				RecordRef: model.RecordRef{Path: path, Length: int64(len(raw)), Digest: sha256.Sum256(raw)}, Detail: &model.ToolDetail{
					Input:  "/workspace/starship/route.go",
					Diff:   "-burn = 3\n burn = estimate\n+burn = 4",
					Output: "route updated\nchecks ready",
				}},
			{Kind: model.EventAssistantText, Text: "The lunar route is ready"},
		},
	}
	m := newModelWithClock([]*model.Session{session}, nil, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 48))
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyUp},
		{Type: tea.KeyEnter},
	} {
		tm.Send(key)
	}
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(string(output), `"type": "assistant"`) }, teatest.WithDuration(time.Second))
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)

	teatest.RequireEqualOutput(t, []byte(normalizeGolden(final.View())))
}

func TestGoldenSubagentsFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	inspector := &model.Session{
		ID: "inspector", Agent: model.AgentClaude, Title: "Inspect the cave map", Models: []string{"claude-sonnet-4-7"},
		UpdatedAt: goldenNow.Add(-2 * time.Minute),
		Usage:     []model.Usage{{InputTokens: 1_500, OutputTokens: 500}}, Cost: model.Cost{USD: 0.08, Estimated: true},
	}
	mapper := &model.Session{
		ID: "mapper", Agent: model.AgentCodex, Title: "Map the cavern", Models: []string{"gpt-5.6-sol"},
		UpdatedAt: goldenNow.Add(-3 * time.Minute),
		Usage:     []model.Usage{{InputTokens: 8_000, OutputTokens: 2_000}}, Cost: model.Cost{USD: 0.12, Estimated: true},
		Subagents: []*model.Session{inspector},
	}
	reviewer := &model.Session{
		ID: "reviewer", Agent: model.AgentCodex, Title: "Review the findings", Models: []string{"gpt-5.6-sol"},
		UpdatedAt: goldenNow.Add(-4 * time.Minute),
		Usage:     []model.Usage{{InputTokens: 2_500, OutputTokens: 500}}, Cost: model.Cost{USD: 0.16, Estimated: true},
	}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout the ridge", Models: []string{"claude-opus-4-8"},
		UpdatedAt: goldenNow.Add(-12 * time.Minute),
		Usage:     []model.Usage{{InputTokens: 40_000, OutputTokens: 5_000}}, Cost: model.Cost{USD: 0.32}, Subagents: []*model.Session{mapper, reviewer},
	}
	shipper := &model.Session{
		ID: "shipper", Agent: model.AgentCodex, Title: "Ship the expedition", Models: []string{"gpt-5.6-sol"},
		UpdatedAt: goldenNow.Add(-1 * time.Minute),
		Usage:     []model.Usage{{InputTokens: 3_000, OutputTokens: 1_000}}, Cost: model.Cost{USD: 0.20},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/starship/route.jsonl", CWD: "/workspace/starship", Project: "starship", Title: "Plan route",
		Models: []string{"claude-opus-4-8"}, GitBranch: "orbit/alpha", StartedAt: goldenNow.Add(-20 * time.Minute), UpdatedAt: goldenNow,
		Usage: []model.Usage{{InputTokens: 120_000, OutputTokens: 8_000}}, Cost: model.Cost{USD: 0.84}, Subagents: []*model.Session{scout, shipper},
	}
	m := newModelWithClock([]*model.Session{root}, nil, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(90, 18))
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(string(output), "Map the cavern") }, teatest.WithDuration(time.Second))
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)

	teatest.RequireEqualOutput(t, []byte(normalizeGolden(final.View())))
}

func TestGoldenInfoFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	pricing, err := cost.EmbeddedTable()
	if err != nil {
		t.Fatal(err)
	}
	calculator := cost.NewCalculator(pricing)
	rootUsage := model.Usage{
		Model: "claude-opus-4-8", InputTokens: 120_000, OutputTokens: 8_000,
		CacheCreation5mTokens: 4_000, CacheReadTokens: 30_000,
	}
	rootCost := calculator.Calculate(rootUsage)
	mapper := &model.Session{
		ID: "mapper", Agent: model.AgentCodex, Title: "Map the cavern", Models: []string{"gpt-5.6-sol"},
		Usage: []model.Usage{{Model: "gpt-5.6-sol", InputTokens: 8_000, OutputTokens: 2_000}}, ModelCosts: map[string]float64{"gpt-5.6-sol": 0.12},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"gpt-5.6-sol": {Input: testCostBuckets(8_000, 0.08), Output: testCostBuckets(2_000, 0.04)}}, Cost: model.Cost{USD: 0.12},
	}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout the ridge", Models: []string{"claude-opus-4-8"},
		Usage: []model.Usage{{Model: "claude-opus-4-8", InputTokens: 40_000, OutputTokens: 5_000}}, ModelCosts: map[string]float64{"claude-opus-4-8": 0.32},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"claude-opus-4-8": {Input: testCostBuckets(40_000, 0.20), Output: testCostBuckets(5_000, 0.12)}}, Cost: model.Cost{USD: 0.32}, Subagents: []*model.Session{mapper},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/starship/route.jsonl", CWD: "/workspace/starship", Project: "starship", Title: "Plan route",
		Models: []string{"claude-opus-4-8"}, GitBranch: "orbit/alpha", StartedAt: goldenNow.Add(-20 * time.Minute), UpdatedAt: goldenNow,
		Usage: []model.Usage{rootUsage}, ModelCosts: map[string]float64{"claude-opus-4-8": rootCost.USD},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"claude-opus-4-8": calculator.Breakdown(rootUsage)}, Cost: rootCost, Subagents: []*model.Session{scout},
	}
	m := newModelWithClock([]*model.Session{root}, nil, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 38))
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	for range 2 {
		tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	}
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(string(output), "total = own") }, teatest.WithDuration(time.Second))
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)

	teatest.RequireEqualOutput(t, []byte(normalizeGolden(final.View())))
}

func normalizeGolden(view string) string {
	lines := strings.Split(ansi.Strip(view), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return strings.Join(lines, "\n") + "\n"
}
