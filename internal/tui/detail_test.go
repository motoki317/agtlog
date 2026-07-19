package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/muesli/termenv"
)

type detailTestSource struct {
	session *model.Session
}

func (s detailTestSource) Agent() model.AgentKind { return model.AgentClaude }
func (s detailTestSource) Roots() []string        { return []string{"/workspace"} }
func (s detailTestSource) Discover(context.Context) ([]string, error) {
	return []string{s.session.Path}, nil
}
func (s detailTestSource) Parse(string) (*model.Session, error) { return s.session, nil }
func (s detailTestSource) LoadEvents(_ context.Context, session *model.Session) error {
	session.Events = []model.Event{{Kind: model.EventUser, Text: "Loaded on open"}}
	return nil
}

func TestDetailExpandsNestedSubagent(t *testing.T) {
	child := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout terrain",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Inspect the ridge"},
			{Kind: model.EventAssistantText, Text: "The ridge is clear"},
		},
	}
	parent := &model.Session{
		ID: "parent", Agent: model.AgentClaude, Title: "Plan route", Subagents: []*model.Session{child},
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Delegate the survey"},
			{Kind: model.EventAssistantText, Text: "Survey delegated"},
			{Kind: model.EventSubagent, ToolName: "Agent", Subagent: child},
		},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyDown},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeySpace},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if view := m.View(); !strings.Contains(view, "Inspect the ridge") {
		t.Fatalf("detail view did not expand nested subagent:\n%s", view)
	}
}

func TestColoredDetailFillsWideTerminalAndBoundsLines(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude, Project: "observatory", CWD: "/workspace/observatory", Models: []string{"claude-opus-4-8"},
		Events: []model.Event{{Kind: model.EventUser, Text: strings.Repeat("Map lunar craters ", 30)}},
	}
	detail := newDetailState(session, 160, 40, newStyles(themes["default"]))
	raw := detail.view()
	if !strings.Contains(raw, "\x1b[") {
		t.Fatal("colored-path detail render emitted no ANSI styling")
	}
	plain := ansi.Strip(raw)
	lines := strings.Split(plain, "\n")
	if !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") || ansi.StringWidth(lines[0]) != 160 {
		t.Fatalf("detail header top = %q (width %d), want rounded full-width border", lines[0], ansi.StringWidth(lines[0]))
	}
	for index, line := range lines {
		if width := ansi.StringWidth(line); width > 160 {
			t.Fatalf("detail line %d width = %d, want <= 160", index+1, width)
		}
	}
}

func TestDetailKeyBarKeepsQuitVisibleAtEightyColumns(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "lunar"}, 80, 12, newStyles())
	keyBar := strings.Split(ansi.Strip(detail.view()), "\n")[11]
	if !strings.Contains(keyBar, "q quit") || strings.Contains(keyBar, "…") {
		t.Fatalf("80-column detail key bar = %q, want visible quit hint without truncation", keyBar)
	}
}

func TestNextSubagentRevealsCollapsedSpawn(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout", Events: []model.Event{{Kind: model.EventUser, Text: "Inspect the ridge"}}}
	parent := &model.Session{
		ID: "parent", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Delegate"},
			{Kind: model.EventAssistantText, Text: "Delegated"},
			{Kind: model.EventSubagent, Subagent: child},
		},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeySpace},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if view := m.View(); !strings.Contains(view, "Inspect the ridge") {
		t.Fatalf("J did not reveal and focus collapsed subagent:\n%s", view)
	}
}

func TestSubagentLineKeepsToggleVisibleAtEightyColumns(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: strings.Repeat("Long delegated prompt ", 20), Models: []string{"claude-opus-4-8"}}
	parent := &model.Session{
		ID: "parent", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, ToolInput: "Scout terrain", Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if view := m.View(); !strings.Contains(view, "Task(Scout terrain) ▸ opus-4.8") {
		t.Fatalf("subagent affordance was hidden:\n%s", view)
	}
}

func TestEnterLoadsDetailLazily(t *testing.T) {
	session := &model.Session{ID: "lazy", Agent: model.AgentClaude, Path: "/workspace/session.jsonl"}
	registry := source.NewRegistry([]source.Source{detailTestSource{session: session}}, source.Options{})
	m := NewModel([]*model.Session{session}, registry)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter returned no detail-loading command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	if view := m.View(); !strings.Contains(view, "Loaded on open") {
		t.Fatalf("detail view did not apply lazy events:\n%s", view)
	}
}

func TestDetailSanitizesToolAndModelFields(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &model.Session{
		ID: "unsafe", Agent: model.AgentKind("claude\nforged"), Models: []string{"model\x1b[31mred"},
		Events: []model.Event{{Kind: model.EventToolCall, ToolName: "Read\x1b]8;;invalid\a", ToolInput: "safe\u202ereversed"}},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	view := m.View()
	if strings.ContainsAny(view, "\r\x1b\a") || strings.ContainsRune(view, '\u202e') {
		t.Fatalf("detail emitted unsafe terminal data %q", view)
	}
}

func TestDetailHeaderIncludesProjectFullCWDAndAllModels(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &model.Session{
		ID:        "mission",
		Agent:     model.AgentClaude,
		Project:   "mission-control",
		CWD:       "/srv/fictional/deep/telemetry",
		Models:    []string{"claude-opus-4-8", "claude-fable-5"},
		GitBranch: "orbit/alpha",
		StartedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 2, 3, 14, 0, 0, time.UTC),
	}

	header := newDetailState(session, 200, 12, newStyles()).header()
	for _, want := range []string{
		"claude · mission-control (/srv/fictional/deep/telemetry)",
		"opus-4.8, fable-5",
		"orbit/alpha",
		"Jan 02 03:04→Jan 02 03:14",
	} {
		if !strings.Contains(header, want) {
			t.Fatalf("detail header missing %q:\n%s", want, header)
		}
	}
}

func TestFirstLineKeepsTimelineTerse(t *testing.T) {
	if got := firstLine("First instruction\nSecond instruction"); got != "First instruction" {
		t.Fatalf("firstLine() = %q, want first line only", got)
	}
}

func TestDetailScrollVisitsExpandedToolRows(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey"},
		{Kind: model.EventThinking, Text: "Choose a route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/map.go"},
		{Kind: model.EventAssistantText, Text: "Done"},
	}}
	detail := newDetailState(session, 80, 8, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	if !strings.Contains(detail.focusables[detail.focus].key, "/event/0") {
		t.Fatalf("first expanded focus = %#v, want thinking row", detail.focusables[detail.focus])
	}
	detail.moveFocus(1, false)
	if !strings.Contains(detail.focusables[detail.focus].key, "/event/1") {
		t.Fatalf("second expanded focus = %#v, want tool row", detail.focusables[detail.focus])
	}
}

func TestDetailNavigationSupportsVimEdges(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the ridge"},
		{Kind: model.EventAssistantText, Text: "The ridge is clear"},
		{Kind: model.EventUser, Text: "Survey the valley"},
		{Kind: model.EventAssistantText, Text: "The valley is clear"},
	}}
	detail := newDetailState(session, 80, 8, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if want := len(detail.focusables) - 1; detail.focus != want {
		t.Fatalf("G focus = %d, want %d", detail.focus, want)
	}
	if want := detail.focusables[detail.focus].line; detail.selectedLine != want || detail.viewport.YOffset == 0 || want < detail.viewport.YOffset || want >= detail.viewport.YOffset+detail.viewport.Height {
		t.Fatalf("G selection line=%d offset=%d height=%d, want visible line %d below top", detail.selectedLine, detail.viewport.YOffset, detail.viewport.Height, want)
	}
	if view := ansi.Strip(detail.view()); !strings.Contains(view, "›   ● claude: The valley is clear") {
		t.Fatalf("G did not keep the last focusable visible:\n%s", view)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if detail.focus != 0 {
		t.Fatalf("g focus = %d, want 0", detail.focus)
	}
	if want := detail.focusables[detail.focus].line; detail.selectedLine != want || detail.viewport.YOffset != 0 {
		t.Fatalf("g selection line=%d offset=%d, want line %d at top", detail.selectedLine, detail.viewport.YOffset, want)
	}
	if view := ansi.Strip(detail.view()); !strings.Contains(view, "› ▸ you: Survey the ridge") {
		t.Fatalf("g did not keep the first focusable visible:\n%s", view)
	}
}

func TestCloneSessionRebindsSubagentEvents(t *testing.T) {
	child := &model.Session{ID: "scout"}
	parent := &model.Session{ID: "root", Subagents: []*model.Session{child}, Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}}}
	cloned := cloneSession(parent)

	if cloned.Subagents[0] == child || cloned.Events[0].Subagent != cloned.Subagents[0] {
		t.Fatalf("cloned graph retained original link: %#v", cloned)
	}
}
