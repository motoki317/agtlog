package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/motoki317/agtlog/internal/model"
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
			Usage: []model.Usage{{InputTokens: 88_000, OutputTokens: 4_000}}, Cost: model.Cost{USD: 0.76, Estimated: true},
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
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/starship/route.jsonl", CWD: "/workspace/starship", Project: "starship", Title: "Plan route",
		Models: []string{"claude-opus-4-8"}, GitBranch: "orbit/alpha", StartedAt: goldenNow.Add(-20 * time.Minute), UpdatedAt: goldenNow,
		Usage: []model.Usage{{InputTokens: 120_000, OutputTokens: 8_000}}, Cost: model.Cost{USD: 0.84}, Subagents: []*model.Session{child},
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Delegate the survey"},
			{Kind: model.EventThinking, Text: "Select a safe path"},
			{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/starship/map.go", ResultSummary: "map ready", Duration: 400 * time.Millisecond},
			{Kind: model.EventAssistantText, Text: "Survey delegated"},
			{Kind: model.EventSubagent, ToolName: "Agent", Subagent: child},
		},
	}
	m := newModelWithClock([]*model.Session{parent}, nil, func() time.Time { return goldenNow })
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 18))
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyDown},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeySpace},
	} {
		tm.Send(key)
	}
	teatest.WaitFor(t, tm.Output(), func(output []byte) bool { return strings.Contains(string(output), "Inspect the ridge") }, teatest.WithDuration(time.Second))
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
