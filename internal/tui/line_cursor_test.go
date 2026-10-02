package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/muesli/termenv"
)

func newBodyCursorModel(t *testing.T) Model {
	t.Helper()
	root := &model.Session{ID: "survey", Agent: model.AgentCodex, Path: "/workspace/survey.jsonl", Events: []model.Event{
		{Kind: model.EventAssistantText, Text: "Earlier report\nearlier body"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Output: "first output\nsecond output\nthird output"}},
		{Kind: model.EventAssistantText, Text: "Later report\n" + strings.Repeat("later body\n", 20)},
	}}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 16},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	detail := detailStateFromScreen(t, m.detail)
	for range detail.focusables[1].line + 3 {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}
	detail = detailStateFromScreen(t, m.detail)
	if detail.focus != 1 || strings.TrimSpace(detail.lines[detail.selectedLine].text) != "second output" {
		t.Fatalf("fixture cursor line=%d focus=%d, want second tool output", detail.selectedLine, detail.focus)
	}
	return m
}

func TestTimelineBodyCursorActionsUseOwningEvent(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyLeft}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'C'}}, {Type: tea.KeyRight}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'l'}}} {
		t.Run(key.String(), func(t *testing.T) {
			m := newBodyCursorModel(t)
			before := detailStateFromScreen(t, m.detail)
			line, eventKey := before.selectedLine, before.focusKey()
			updated, _ := m.Update(key)
			m = updated.(Model)
			if key.Type == tea.KeyEnter || key.String() == "l" {
				item, ok := m.detail.(*itemView)
				if !ok || item.event.ToolName != "Read" || item.event.Detail.Output != "first output\nsecond output\nthird output" {
					t.Fatalf("body action opened %T, want the owning Read event", m.detail)
				}
				updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				m = updated.(Model)
				if got := detailStateFromScreen(t, m.detail).selectedLine; got != line {
					t.Fatalf("item return cursor = %d, want %d", got, line)
				}
				return
			}
			detail := detailStateFromScreen(t, m.detail)
			wantLine := detail.focusables[1].line
			if key.Type == tea.KeyRight {
				wantLine = line
			}
			if detail.focusKey() != eventKey || detail.selectedLine != wantLine || detail.isExpanded(eventKey) != (key.Type == tea.KeyRight) {
				t.Fatalf("body fold cursor=%d expanded=%t, want line %d on the same owning event", detail.selectedLine, detail.isExpanded(eventKey), wantLine)
			}
		})
	}
}

func TestTimelineRebuildPreservesBodyCursor(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}},
		tea.WindowSizeMsg{Width: 36, Height: 14},
		tea.WindowSizeMsg{Width: 80, Height: 30},
	} {
		t.Run(fmt.Sprintf("%v", msg), func(t *testing.T) {
			m := newBodyCursorModel(t)
			before := detailStateFromScreen(t, m.detail)
			key := before.focusKey()
			updated, _ := m.Update(msg)
			m = updated.(Model)
			detail := detailStateFromScreen(t, m.detail)
			if detail.focusKey() != key || detail.selectedLine-detail.focusables[detail.focus].line != 3 {
				t.Fatalf("rebuild cursor line=%d focus=%d, want same event offset 3", detail.selectedLine, detail.focus)
			}
		})
	}
}

func TestTimelineOversizedLastLineKeepsEdgeScrolling(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "survey", Agent: model.AgentCodex, Events: []model.Event{{Kind: model.EventAssistantText, Text: strings.Repeat("long body ", 40)}}}, 28, 14, newStyles())
	for _, key := range []rune{'E', 'g', 'j'} {
		detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	}
	first := detail.firstRenderedRow(1)
	if detail.selectedLine != 1 || detail.viewport.YOffset != first || len(detail.rendered)-first <= detail.viewport.Height {
		t.Fatal("a line taller than the viewport must be selected at its first row")
	}
	for range len(detail.rendered) + 2 {
		want := min(detail.viewport.YOffset+1, len(detail.rendered)-detail.viewport.Height)
		detail.update(tea.KeyMsg{Type: tea.KeyDown})
		if detail.selectedLine != 1 || detail.focus != 0 || detail.viewport.YOffset != want {
			t.Fatalf("last-line edge offset=%d cursor=%d, want offset %d and cursor 1", detail.viewport.YOffset, detail.selectedLine, want)
		}
	}
	detail.update(tea.KeyMsg{Type: tea.KeyUp})
	if detail.selectedLine != 0 || detail.viewport.YOffset != 0 {
		t.Fatal("one up press must leave the oversized line and select its header")
	}
}

func TestTimelineLastLineCursorFollowsLiveTail(t *testing.T) {
	m := newBodyCursorModel(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	if detail.selectedLine != len(detail.lines)-1 || !detail.followingTail() {
		t.Fatal("G must select the final body line and follow the tail")
	}
	grown := cloneSession(detail.session)
	grown.Events = append(grown.Events, model.Event{Kind: model.EventAssistantText, Text: "Newest report\nNewest body row"})
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{grown}})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.selectedLine != len(detail.lines)-1 || detail.focus != len(detail.focusables)-1 || !detail.followingTail() || !strings.Contains(ansi.Strip(m.View()), "Newest body row") {
		t.Fatal("a followed live append must select and reveal the new final body line")
	}
}

func TestTimelineTailCursorStaysVisibleAfterWrappedBody(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live=%t", live), func(t *testing.T) {
			text := strings.Repeat("wrapped prose ", 20) + "\nFINAL BODY LINE"
			root := &model.Session{ID: "survey", Agent: model.AgentCodex, Path: "/workspace/survey.jsonl", Events: []model.Event{{Kind: model.EventAssistantText, Text: text}}}
			if live {
				root.Events[0].Text = "Initial report\nInitial body"
			}
			m := NewModel([]*model.Session{root}, nil)
			for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 28, Height: 16}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}} {
				updated, _ := m.Update(msg)
				m = updated.(Model)
			}
			if live {
				replacement := cloneSession(root)
				replacement.Events[0].Text = text
				updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
				m = updated.(Model)
			}
			detail := detailStateFromScreen(t, m.detail)
			if detail.selectedLine != len(detail.lines)-1 || !detail.followingTail() || !strings.Contains(ansi.Strip(m.View()), "FINAL BODY LINE") {
				t.Fatalf("tail cursor line=%d first row=%d viewport=%d..%d, want final body visible", detail.selectedLine, detail.firstRenderedRow(detail.selectedLine), detail.viewport.YOffset, detail.viewport.YOffset+detail.viewport.Height-1)
			}
		})
	}
}

func TestTimelineLiveUpdatePreservesScrollWithinOversizedLine(t *testing.T) {
	root := &model.Session{ID: "survey", Agent: model.AgentCodex, Path: "/workspace/survey.jsonl", Events: []model.Event{{Kind: model.EventAssistantText, Text: strings.Repeat("wrapped prose ", 40)}}}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 28, Height: 16}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	before := detailStateFromScreen(t, m.detail)
	if before.selectedLine != 1 || before.viewport.YOffset != 3 || before.followingTail() {
		t.Fatal("fixture must scroll two rows into the final line without following the tail")
	}
	replacement := cloneSession(root)
	replacement.Title = "Updated survey"
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	if detail.selectedLine != 1 || detail.viewport.YOffset != 3 || detail.followingTail() {
		t.Fatalf("live scroll cursor=%d offset=%d following=%t, want 1/3/false", detail.selectedLine, detail.viewport.YOffset, detail.followingTail())
	}
}

func TestTimelineDrillStartsAtLastBodyLine(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl", Events: []model.Event{{Kind: model.EventAssistantText, Text: "Child report\nFirst body\nLast body"}}}
	root := &model.Session{ID: "survey", Agent: model.AgentClaude, Path: "/workspace/survey.jsonl", Subagents: []*model.Session{child}, Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}}}
	registry := source.NewRegistry([]source.Source{detailTestSource{
		session: root,
		loadNodeEvents: func(_ context.Context, loaded *model.Session) error {
			if loaded.ID == child.ID {
				loaded.Events = append([]model.Event(nil), child.Events...)
			}
			return nil
		},
	}}, source.Options{})
	m := NewModel([]*model.Session{root}, registry)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'E'}}, {Type: tea.KeyEnter}} {
		updated, cmd := m.Update(key)
		m = updated.(Model)
		if cmd != nil {
			updated, _ = m.Update(cmd())
			m = updated.(Model)
		}
	}
	detail := detailStateFromScreen(t, m.detail)
	if detail.session.ID != child.ID || detail.loadStatus != detailStatusLoaded || detail.selectedLine != len(detail.lines)-1 || strings.TrimSpace(detail.lines[detail.selectedLine].text) != "Last body" || !detail.followingTail() {
		t.Fatalf("child=%s status=%v cursor=%d/%d expanded=%t tail=%t lines=%v, want the expanded child's final body line", detail.session.ID, detail.loadStatus, detail.selectedLine, len(detail.lines), detail.defaultExpanded, detail.followingTail(), timelineLineTexts(detail.lines))
	}
}

func TestTimelineLiveUpdatePreservesBodyCursor(t *testing.T) {
	m := newBodyCursorModel(t)
	before := detailStateFromScreen(t, m.detail)
	before.viewport.SetYOffset(2)
	key, offset := before.focusKey(), before.viewport.YOffset
	replacement := cloneSession(before.session)
	replacement.Events[0].Text += "\nnew earlier body"
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	if detail.focusKey() != key || detail.selectedLine-detail.focusables[detail.focus].line != 3 || detail.viewport.YOffset != offset {
		t.Fatalf("live cursor line=%d focus=%d viewport=%d, want same event offset 3 and viewport %d", detail.selectedLine, detail.focus, detail.viewport.YOffset, offset)
	}
	if strings.TrimSpace(detail.lines[detail.selectedLine].text) != "second output" {
		t.Fatal("live cursor no longer selects the same body line")
	}

	replacement = cloneSession(replacement)
	replacement.Events[1].Detail = &model.ToolDetail{Output: "only output"}
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.focusKey() != key || strings.TrimSpace(detail.lines[detail.selectedLine].text) != "only output" {
		t.Fatal("a shortened event did not clamp the cursor to its last body line")
	}
}

func TestTimelineClickSelectsBodyLine(t *testing.T) {
	m := newBodyCursorModel(t)
	for _, text := range []string{"first output", "third output"} {
		y := viewLineY(t, m.View(), text, 0)
		updated, _ := m.Update(tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
		m = updated.(Model)
		detail := detailStateFromScreen(t, m.detail)
		if detail.focus != 1 || strings.TrimSpace(detail.lines[detail.selectedLine].text) != text || !detail.isExpanded(detail.focusKey()) {
			t.Fatalf("click cursor line=%d focus=%d, want expanded tool body %q", detail.selectedLine, detail.focus, text)
		}
	}
}

func TestTimelineWrappedLineMovesAndHighlightsAsAUnit(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	detail := newDetailState(&model.Session{ID: "survey", Agent: model.AgentCodex, Events: []model.Event{{Kind: model.EventAssistantText, Text: "first body\n" + strings.Repeat("wrapped prose ", 4) + "\nlast body"}}}, 28, 16, newStyles(Theme{Name: "mono"}))
	for _, key := range []rune{'E', 'g', 'j', 'j'} {
		detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	}
	start, end := detail.firstRenderedRow(2), detail.firstRenderedRow(3)
	if detail.selectedLine != 2 || end-start < 2 || strings.Count(detail.view(), "\x1b[7m") != end-start {
		t.Fatalf("wrapped cursor line=%d highlighted=%d, want all %d rows of line 2", detail.selectedLine, strings.Count(detail.view(), "\x1b[7m"), end-start)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyDown})
	if detail.selectedLine != 3 || !strings.Contains(ansi.Strip(detail.view()), "last body") {
		t.Fatal("one press did not move past the complete wrapped line")
	}
	m := NewModel(nil, nil)
	m.screen, m.detail = screenDetail, detail
	m.width, m.height = 28, 16
	y := newDetailLayout(detail.height).contentY + start + 1 - detail.viewport.YOffset
	updated, _ := m.Update(tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.selectedLine != 2 || detail.focus != 0 || !detail.isExpanded(detail.focusKey()) {
		t.Fatal("clicking a continuation must select its text line without folding")
	}
}
