package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

func TestOverviewActivityUsesOwnedSplits(t *testing.T) {
	leaf := &model.Session{Messages: 2, ToolCalls: 3, Usage: []model.Usage{{InputTokens: 100}}, Cost: model.Cost{USD: 1}}
	child := &model.Session{Messages: 3, ToolCalls: 4, Usage: []model.Usage{{InputTokens: 200}}, Cost: model.Cost{USD: 2}, Subagents: []*model.Session{leaf}}
	root := &model.Session{Messages: 5, ToolCalls: 7, Usage: []model.Usage{{InputTokens: 1000}}, Cost: model.Cost{USD: 10}, DuplicatedUsage: model.Usage{InputTokens: 400}, DuplicatedUSD: 4, Subagents: []*model.Session{child}}
	for _, test := range []struct {
		name    string
		session *model.Session
		rows    []string
	}{
		{"leaf", leaf, []string{"Activity TURNS TOKENS COST", "own 5 100 $1.00"}},
		{"nested replay", root, []string{"Activity TURNS TOKENS COST", "own 12 600 $6.00", "subagents 12 300 $3.00", "total 24 900 $9.00"}},
		{"group", &model.Session{Group: true, Subagents: []*model.Session{leaf}}, []string{"Activity TURNS TOKENS COST", "own 0 0 $0.00", "subagents 5 100 $1.00", "total 5 100 $1.00"}},
		{"missing", &model.Session{Cost: model.Cost{Estimated: true, MissingPricingModels: []string{"fictional"}}}, []string{"Activity TURNS TOKENS COST", "own 0 0 ~$0.00!"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := activityLines(test.session, 86)
			if len(lines) != len(test.rows) {
				t.Fatalf("rows = %d, want %d", len(lines), len(test.rows))
			}
			for i, line := range lines {
				if got := strings.Join(strings.Fields(line.text), " "); got != test.rows[i] {
					t.Errorf("row %d = %q, want %q", i, got, test.rows[i])
				}
			}
		})
	}
}

func TestOverviewActivityDropsTokens(t *testing.T) {
	for _, test := range []struct {
		width  int
		tokens bool
	}{{86, true}, {33, true}, {32, false}, {26, false}} {
		header := activityLines(&model.Session{}, test.width)[0].text
		if strings.Contains(header, "MSGS") || strings.Contains(header, "TOKENS") != test.tokens {
			t.Errorf("width %d header %q", test.width, header)
		}
	}
}

func TestOverviewAlwaysShowsFullModelBlocksAndFocusesOnlySubagents(t *testing.T) {
	root := &model.Session{Usage: []model.Usage{{Model: "claude-opus-4-8", InputTokens: 100}}, ModelCosts: map[string]float64{"claude-opus-4-8": 1}, ModelCostBreakdowns: map[string]model.CostBreakdown{"claude-opus-4-8": {Input: model.CostBuckets{{Tokens: 100, RatePerToken: 0.01}}}}, Subagents: []*model.Session{{ID: "child"}}}
	detail := newDetailState(root, 100, 30, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	if len(detail.focusables) != 1 || detail.focusedSubagent() != root.Subagents[0] || detail.selectedExpandable() {
		t.Fatal("Overview must focus only its first subagent")
	}
	text := strings.Join(timelineLineTexts(detail.lines), "\n")
	for _, want := range []string{"claude-opus-4-8", "100 × $10000", "subtotal", "$1.00"} {
		if !strings.Contains(text, want) {
			t.Fatalf("full model block missing %q: %s", want, text)
		}
	}
	for _, msg := range []tea.KeyMsg{{Type: tea.KeyUp}, {Type: tea.KeySpace}, {Type: tea.KeyEnter}} {
		detail.update(msg)
		if detail.focusedSubagent() != root.Subagents[0] || strings.Join(timelineLineTexts(detail.lines), "\n") != text || len(detail.expanded) != 0 {
			t.Fatal("Overview keys changed plain model blocks or subagent-only selection")
		}
	}
	detail.resize(100, 12)
	detail.update(tea.KeyMsg{Type: tea.KeyHome})
	detail.update(tea.KeyMsg{Type: tea.KeyPgDown})
	if detail.viewport.YOffset == 0 || detail.focusedSubagent() != root.Subagents[0] {
		t.Fatal("page scrolling must expose model blocks independently of subagent selection")
	}
	detail.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if detail.viewport.YOffset != 0 || detail.focusedSubagent() != root.Subagents[0] {
		t.Fatal("page scrolling did not restore top without changing selection")
	}

}

func overviewSessionLines(session *model.Session) []detailLine {
	detail := newDetailState(session, 120, 80, newStyles())
	detail.tab = tabOverview
	detail.rebuild()
	return detail.lines
}

func ownModelCostLinesForTest(session *model.Session) []string {
	var lines []string
	for _, item := range ownModelCosts(session) {
		lines = append(lines, item.title)
		lines = append(lines, item.lines...)
	}
	return lines
}

func subagentTableLines(detail *detailState) []detailLine {
	for i, line := range detail.lines {
		if line.subagentHeader {
			return detail.lines[i:]
		}
	}
	return nil
}

func TestOverviewFullModelBlocksRefreshWithoutFocus(t *testing.T) {
	root := &model.Session{ID: "root", Agent: model.AgentClaude, Path: "/workspace/root.jsonl", Usage: []model.Usage{{Model: "model-a", InputTokens: 100}}}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyTab}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	replacement := cloneSession(root)
	replacement.Usage = []model.Usage{{Model: "new-model", InputTokens: 20}, {Model: "model-a", InputTokens: 200}}
	replacement.Messages = 4
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	if len(detail.focusables) != 0 || detail.selectedLine != -1 {
		t.Fatal("leaf model blocks became focusable after refresh")
	}
	text := strings.Join(timelineLineTexts(detail.lines), "\n")
	for _, want := range []string{"new-model", "20 · price unavailable", "model-a", "200 · price unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stale model block missing %q: %s", want, text)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(detail.lines[1].text), " "), "own 4 220") {
		t.Fatalf("stale Activity: %q", detail.lines[1].text)
	}
}

func TestOverviewMouseSelectsOnlySubagents(t *testing.T) {
	root := &model.Session{Usage: []model.Usage{{Model: "model-a", InputTokens: 10}, {Model: "model-b", InputTokens: 20}}, Subagents: []*model.Session{{ID: "child"}}}
	detail := newDetailState(root, 100, 40, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	layout := newDetailLayout(detail.height)
	for index, line := range detail.lines {
		y := layout.contentY + detail.firstRenderedRow(index) - detail.viewport.YOffset
		focus, ok := detail.rowAtY(y)
		if line.subagent {
			if !ok || focus != 0 {
				t.Fatalf("subagent click = %d/%t", focus, ok)
			}
		} else if ok {
			t.Fatalf("plain row %q is focusable", line.text)
		}
	}
}

func TestOverviewActivityFitsNarrowWidths(t *testing.T) {
	for width := 0; width <= 86; width++ {
		for _, line := range activityLines(&model.Session{Messages: 123456789, Cost: model.Cost{USD: 99999}}, width) {
			if got := ansi.StringWidth(line.text); got > width {
				t.Fatalf("width %d rendered %d: %q", width, got, line.text)
			}
		}
	}
}

func TestOverviewWithoutSubagentsScrollsFullModelBlocks(t *testing.T) {
	detail := newDetailState(&model.Session{Usage: []model.Usage{{Model: "model-a", InputTokens: 100, OutputTokens: 20}}}, 40, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	if len(detail.focusables) != 0 || detail.selectedLine != -1 {
		t.Fatal("leaf Overview has focus")
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if detail.viewport.YOffset != 1 {
		t.Fatal("j did not scroll leaf Overview")
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if detail.viewport.YOffset != 0 {
		t.Fatal("k did not scroll leaf Overview back")
	}
	for range 20 {
		detail.update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if !strings.Contains(ansi.Strip(detail.view()), "No subagents") || detail.selectedLine != -1 {
		t.Fatal("leaf Overview cannot reach its empty state")
	}
	before := detail.viewport.YOffset
	for _, key := range []tea.KeyType{tea.KeySpace, tea.KeyEnter} {
		detail.update(tea.KeyMsg{Type: key})
	}
	if detail.viewport.YOffset != before || len(detail.expanded) != 0 {
		t.Fatal("leaf Overview has model expansion state")
	}
}

func TestOverviewRoundTripDoesNotDisableTimelineTailFollowing(t *testing.T) {
	root := &model.Session{ID: "root", Agent: model.AgentClaude, Path: "/workspace/root.jsonl", Events: []model.Event{{Kind: model.EventUser, Text: "First"}}}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyTab}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	replacement := cloneSession(root)
	replacement.Events = append(replacement.Events, model.Event{Kind: model.EventAssistantText, Text: "Second"})
	restore := captureDetailRestoreState(detailStateFromScreen(t, m.detail))
	if !restore.pinned {
		t.Fatal("fixture must follow tail before refresh")
	}
	detail := m.replacementDetailStateFromRestore(restore, replacement, nil)
	if detail.focus != len(detail.focusables)-1 || !detail.followingTail() {
		t.Fatalf("refresh stopped following: focus=%d total=%d following=%t", detail.focus, len(detail.focusables), detail.followingTail())
	}
}
