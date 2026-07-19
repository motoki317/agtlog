package tui

import (
	"context"
	"slices"
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
	if !strings.Contains(keyBar, "w wrap") || !strings.Contains(keyBar, "q quit") || strings.Contains(keyBar, "…") {
		t.Fatalf("80-column detail key bar = %q, want wrap and quit hints without truncation", keyBar)
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

func TestExpandedToolDetailIsPlainTerminalText(t *testing.T) {
	session := &model.Session{ID: "unsafe", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Inspect the route"},
		{Kind: model.EventToolCall, ToolName: "Grep", ToolInput: "ridge", Detail: &model.ToolDetail{
			Diff:   "\x1b[31m-old route\x1b[0m\n\x1b[32m+new  route\u202e\x1b[0m",
			Output: "route\rrewritten\x1b]8;;https://invalid.example\a link\x1b]8;;\a",
			Input:  "{\n\t\"query\": \"ridge\"\n}",
		}},
	}}
	detail := newDetailState(session, 80, 18, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})

	var rows []string
	for _, line := range detail.lines {
		rows = append(rows, line.text)
	}
	plain := strings.Join(rows, "\n")
	if strings.ContainsAny(plain, "\r\t\x1b\a") || strings.ContainsRune(plain, '\u202e') {
		t.Fatalf("expanded detail retained unsafe terminal data %q", plain)
	}
	for _, want := range []string{"-old route", "+new  route", "route rewritten", `"query": "ridge"`} {
		if !strings.Contains(plain, want) {
			t.Errorf("sanitized detail missing %q:\n%s", want, plain)
		}
	}
	wantRoles := map[string]detailRole{"-old route": detailDiffRemove, "+new  route ": detailDiffAdd}
	for _, line := range detail.lines {
		text := strings.TrimPrefix(line.text, "    ")
		if want, ok := wantRoles[text]; ok {
			if line.role != want {
				t.Errorf("sanitized diff line %q role = %v, want %v", text, line.role, want)
			}
			delete(wantRoles, text)
		}
	}
	if len(wantRoles) != 0 {
		t.Fatalf("sanitized diff roles missing for %#v", wantRoles)
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

func TestBoundLinesElidesMiddleWithHeadAndTail(t *testing.T) {
	text := strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven", "eight"}, "\n")
	want := []string{"one", "two", "… 4 lines hidden …", "seven", "eight"}
	if got := boundLines(text, 5); !slices.Equal(got, want) {
		t.Fatalf("boundLines() = %#v, want %#v", got, want)
	}
}

func TestToolExpansionRevealsDiffLinesWithSemanticRoles(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Update the route"},
		{Kind: model.EventToolCall, ToolName: "Edit", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Diff: "-old route\n unchanged\n+new route"}},
		{Kind: model.EventAssistantText, Text: "Route updated"},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	toolIndex := detail.focusables[detail.focus].line
	if line := detail.lines[toolIndex]; !line.expandable || !strings.Contains(line.text, "▸ "+glyphTool+" Edit") {
		t.Fatalf("collapsed tool line = %#v, want expandable marker", line)
	}

	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	toolIndex = detail.focusables[detail.focus].line
	if line := detail.lines[toolIndex]; !strings.Contains(line.text, "▾ "+glyphTool+" Edit") {
		t.Fatalf("expanded tool line = %#v, want expanded marker", line)
	}
	wantRoles := map[string]detailRole{
		"-old route": detailDiffRemove,
		" unchanged": detailDiffContext,
		"+new route": detailDiffAdd,
	}
	for _, line := range detail.lines[toolIndex+1:] {
		text := strings.TrimPrefix(line.text, "    ")
		if want, ok := wantRoles[text]; ok {
			if line.role != want {
				t.Errorf("line %q role = %v, want %v", line.text, line.role, want)
			}
			delete(wantRoles, text)
		}
	}
	if len(wantRoles) != 0 {
		t.Fatalf("expanded diff missing lines: %#v", wantRoles)
	}
}

func TestToolExpansionShowsMutedOutputSection(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
		{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-route", Detail: &model.ToolDetail{Input: "check-route", Output: "route clear\ncommand complete"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})

	want := map[string]bool{"output:": false, "route clear": false, "command complete": false}
	for _, line := range detail.lines {
		text := strings.TrimSpace(line.text)
		if _, ok := want[text]; !ok {
			continue
		}
		if line.role != detailSecondary {
			t.Errorf("output line %q role = %v, want muted", line.text, line.role)
		}
		want[text] = true
	}
	for text, found := range want {
		if !found {
			t.Errorf("expanded tool missing %q", text)
		}
	}
}

func TestToolExpansionShowsNonFileInputSection(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Find the route"},
		{Kind: model.EventToolCall, ToolName: "Grep", ToolInput: "ridge", Detail: &model.ToolDetail{Input: "{\n  \"query\": \"ridge\"\n}"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})

	want := map[string]bool{"input:": false, "{": false, `"query": "ridge"`: false, "}": false}
	for _, line := range detail.lines {
		text := strings.TrimSpace(line.text)
		if _, ok := want[text]; !ok {
			continue
		}
		if line.role != detailSecondary {
			t.Errorf("input line %q role = %v, want muted", line.text, line.role)
		}
		want[text] = true
	}
	for text, found := range want {
		if !found {
			t.Errorf("expanded tool missing %q", text)
		}
	}
}

func TestToolExpansionOmitsSingleLineFileInput(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Read the route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Input: "/workspace/route.go", Output: "route ready"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})

	var rows []string
	for _, line := range detail.lines {
		rows = append(rows, strings.TrimSpace(line.text))
	}
	if !slices.Contains(rows, "output:") || !slices.Contains(rows, "route ready") {
		t.Fatalf("expanded Read missing output section: %#v", rows)
	}
	if slices.Contains(rows, "input:") {
		t.Fatalf("expanded Read repeated its file target under input: %#v", rows)
	}
}

func TestEnterStillExpandsToolInMilestoneB(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Update the route"},
		{Kind: model.EventToolCall, ToolName: "Edit", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Diff: "-old\n+new"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	detail.moveFocus(1, false)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	detail.moveFocus(1, false)
	toolKey := detail.focusables[detail.focus].key

	detail.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !detail.expanded[toolKey] {
		t.Fatal("enter did not retain its Milestone B in-place expansion behavior")
	}
	line := detail.lines[detail.focusables[detail.focus].line]
	if !strings.Contains(line.text, glyphExpanded+" "+glyphTool) {
		t.Fatalf("enter-expanded tool line = %q, want expanded marker", line.text)
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

func TestWrapToggleUsesFlatRowsAndHighlightsEverySelectedRow(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{{
		Kind: model.EventUser, Text: strings.Repeat("charted route ", 12),
	}}}
	detail := newDetailState(session, 28, 16, newStyles(Theme{Name: "mono"}))
	if len(detail.rendered) != 1 {
		t.Fatalf("truncated rendered rows = %d, want one per detail line", len(detail.rendered))
	}

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !detail.wrap || len(detail.rendered) < 3 {
		t.Fatalf("wrapped state = %v with %d rows, want multiple flat rows", detail.wrap, len(detail.rendered))
	}
	selectedRows := 0
	for _, row := range detail.rendered {
		if row.detailIndex == detail.selectedLine {
			selectedRows++
		}
	}
	if selectedRows != len(detail.rendered) {
		t.Fatalf("selected rows = %d/%d, want every wrapped row selected", selectedRows, len(detail.rendered))
	}
	if highlighted := strings.Count(detail.view(), "\x1b[7m"); highlighted != selectedRows {
		t.Fatalf("highlighted rows = %d, want %d selected rows", highlighted, selectedRows)
	}

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if detail.wrap || len(detail.rendered) != 1 {
		t.Fatalf("second toggle left wrap=%v with %d rows, want truncation", detail.wrap, len(detail.rendered))
	}
}

func TestWrappedEdgeNavigationUsesFlatRowOffsets(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Start the survey"},
		{Kind: model.EventAssistantText, Text: strings.Repeat("charted southern route ", 10)},
	}}
	detail := newDetailState(session, 28, 10, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})

	selectedRow := detail.firstRenderedRow(detail.selectedLine)
	if selectedRow <= 0 || selectedRow < detail.viewport.YOffset || selectedRow >= detail.viewport.YOffset+detail.viewport.Height {
		t.Fatalf("G selected flat row %d outside offset=%d height=%d", selectedRow, detail.viewport.YOffset, detail.viewport.Height)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if selectedRow := detail.firstRenderedRow(detail.selectedLine); selectedRow != 0 || detail.viewport.YOffset != 0 {
		t.Fatalf("g selected flat row %d at offset %d, want top row", selectedRow, detail.viewport.YOffset)
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
