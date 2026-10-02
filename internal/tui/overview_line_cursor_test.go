package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

func overviewCursorSession() *model.Session {
	return &model.Session{ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Usage: []model.Usage{{Model: "model-a", InputTokens: 100, OutputTokens: 20}, {Model: "model-b", InputTokens: 200}},
		Subagents: []*model.Session{
			{ID: "scout-a", Agent: model.AgentClaude, Title: "Zulu"},
			{ID: "scout-b", Agent: model.AgentClaude, Title: "Alpha"},
			{ID: "scout-c", Agent: model.AgentClaude, Title: "Bravo"},
		},
	}
}

func newOverviewCursorModel(t *testing.T) Model {
	t.Helper()
	m := NewModel([]*model.Session{overviewCursorSession()}, nil)
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 80, Height: 16}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyTab}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	return m
}

func overviewCostLine(t *testing.T, detail *detailState) int {
	t.Helper()
	for index, line := range detail.lines {
		if strings.Contains(line.text, "price unavailable") {
			return index
		}
	}
	t.Fatal("fixture has no cost line")
	return -1
}

func TestOverviewCursorActivation(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'l'}}} {
		t.Run(key.String(), func(t *testing.T) {
			m := newOverviewCursorModel(t)
			detail := detailStateFromScreen(t, m.detail)
			for line := detail.focusables[0].line - 1; line >= 0; line-- {
				detail.update(tea.KeyMsg{Type: tea.KeyUp})
				updated, cmd := m.Update(key)
				m = updated.(Model)
				detail = detailStateFromScreen(t, m.detail)
				if detail.session.ID != "route" || detail.selectedLine != line || len(m.detailStack) != 0 || cmd != nil {
					t.Fatalf("open acted on metadata line %d", line)
				}
			}
			detail.selectRow(detail.focusables[1].line)
			selected := detail.focusedSubagent()
			updated, _ := m.Update(key)
			m = updated.(Model)
			if detailStateFromScreen(t, m.detail).session != selected || len(m.detailStack) != 1 {
				t.Fatal("subagent row did not open its child")
			}
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = updated.(Model)
			if got := detailStateFromScreen(t, m.detail); got.tab != tabOverview || got.focusedSubagent() != selected {
				t.Fatal("back lost the selected subagent")
			}
		})
	}
}

func TestOverviewCursorRebuilds(t *testing.T) {
	for _, onChild := range []bool{false, true} {
		for _, msg := range []tea.Msg{
			tea.KeyMsg{Type: tea.KeyRight}, tea.KeyMsg{Type: tea.KeyLeft},
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}},
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}},
			tea.WindowSizeMsg{Width: 28, Height: 14}, tea.WindowSizeMsg{Width: 80, Height: 30},
		} {
			t.Run(fmt.Sprintf("child=%t/%v", onChild, msg), func(t *testing.T) {
				m := newOverviewCursorModel(t)
				before := detailStateFromScreen(t, m.detail)
				line := overviewCostLine(t, before)
				if onChild {
					line = before.focusables[1].line
				}
				before.selectRow(line)
				key := before.focusKey()
				updated, _ := m.Update(msg)
				m = updated.(Model)
				detail := detailStateFromScreen(t, m.detail)
				if detail.focusKey() != key || (!onChild && detail.selectedLine != line) || (onChild && detail.selectedLine != detail.focusables[detail.focus].line) {
					t.Fatalf("rebuild cursor line=%d key=%q, want line=%d key=%q", detail.selectedLine, detail.focusKey(), line, key)
				}
				if keyMsg, ok := msg.(tea.KeyMsg); ok {
					switch keyMsg.String() {
					case "right":
						if detail.subagentColumnFocus != columnTitle {
							t.Fatal("metadata disabled column focus")
						}
					case "O", "A", "N":
						if !detail.subagentSort.active {
							t.Fatal("metadata disabled sorting")
						}
					}
				}
			})
		}
	}
}

func TestOverviewCursorLiveUpdate(t *testing.T) {
	for _, target := range []string{"cost", "subagent", "shrinking metadata", "no subagents"} {
		t.Run(target, func(t *testing.T) {
			m := newOverviewCursorModel(t)
			before := detailStateFromScreen(t, m.detail)
			line := overviewCostLine(t, before)
			if target == "subagent" {
				line = before.focusables[1].line
			}
			if target == "shrinking metadata" {
				line = before.focusables[0].line - 1
			}
			before.selectRow(line)
			key := before.focusKey()
			replacement := cloneSession(before.session)
			replacement.Usage = append([]model.Usage{{Model: "new-model", InputTokens: 400}}, replacement.Usage...)
			replacement.Subagents[1].UpdatedAt = m.now()
			if target == "shrinking metadata" {
				replacement.Usage = nil
			}
			if target == "no subagents" {
				replacement.Subagents = nil
			}
			updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
			m = updated.(Model)
			detail := detailStateFromScreen(t, m.detail)
			if target == "no subagents" {
				if detail.selectedLine != -1 || len(detail.focusables) != 0 {
					t.Fatal("child removal retained cursor")
				}
				replacement = cloneSession(replacement)
				replacement.Subagents = before.session.Subagents
				updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
				m = updated.(Model)
				detail = detailStateFromScreen(t, m.detail)
				if detail.focus != 0 || detail.selectedLine != detail.focusables[0].line || detail.focusedSubagent() == nil {
					t.Fatal("returning children must select the first subagent")
				}
				return
			}
			if target == "subagent" {
				if detail.focusKey() != key || detail.selectedLine != detail.focusables[detail.focus].line {
					t.Fatal("live reorder lost subagent")
				}
			} else if want := min(line, detail.focusables[0].line-1); detail.selectedLine != want || detail.focusKey() != "" || detail.focusedSubagent() != nil {
				t.Fatalf("live metadata cursor=%d key=%q, want line %d without subagent", detail.selectedLine, detail.focusKey(), want)
			}
		})
	}
}

func TestOverviewCursorTabMemory(t *testing.T) {
	m := newOverviewCursorModel(t)
	detail := detailStateFromScreen(t, m.detail)
	detail.selectRow(detail.focusables[1].line)
	remembered := detail.focusKey()
	for range 2 {
		detail.switchTab()
	}
	if detail.focusKey() != remembered {
		t.Fatal("return lost remembered subagent")
	}
	detail.selectRow(overviewCostLine(t, detail))
	detail.rebuild()
	if detail.focusKey() != "" {
		t.Fatal("stale tab memory replaced a metadata cursor")
	}
	for range 2 {
		detail.switchTab()
	}
	if detail.focus != 0 || detail.selectedLine != detail.focusables[0].line {
		t.Fatal("return from metadata must select first subagent")
	}
}

func TestOverviewCursorEdgeKeysAndClicks(t *testing.T) {
	m := newOverviewCursorModel(t)
	detail := detailStateFromScreen(t, m.detail)
	for _, keys := range [][2]tea.KeyMsg{
		{{Type: tea.KeyRunes, Runes: []rune{'g'}}, {Type: tea.KeyRunes, Runes: []rune{'G'}}},
		{{Type: tea.KeyHome}, {Type: tea.KeyEnd}},
	} {
		detail.update(keys[0])
		if detail.selectedLine != 0 || detail.viewport.YOffset != 0 {
			t.Fatal("g did not select Activity")
		}
		if !strings.Contains(detail.view(), "0/3") {
			t.Fatal("metadata counter must read 0/3")
		}
		detail.update(keys[1])
		if detail.selectedLine != len(detail.lines)-1 || detail.focus != 2 || !detail.viewport.AtBottom() {
			t.Fatal("G did not select last subagent")
		}
	}
	detail.viewport.GotoTop()
	updated, _ := m.Update(tea.MouseMsg{X: 2, Y: newDetailLayout(detail.height).contentY, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.session.ID != "route" || len(m.detailStack) != 0 || detail.selectedLine != 0 || detail.focusedSubagent() != nil {
		t.Fatal("Activity click did not select its line without opening")
	}
}

func TestOverviewCursorEdgesScrollDisplacedViewport(t *testing.T) {
	m := newOverviewCursorModel(t)
	detail := detailStateFromScreen(t, m.detail)
	detail.update(tea.KeyMsg{Type: tea.KeyHome})
	detail.scrollWheel(tea.MouseButtonWheelDown)
	offset := detail.viewport.YOffset
	detail.update(tea.KeyMsg{Type: tea.KeyUp})
	if offset == 0 || detail.selectedLine != 0 || detail.viewport.YOffset != offset-1 {
		t.Fatal("outward up must scroll one row with the cursor stationary")
	}
	detail.update(tea.KeyMsg{Type: tea.KeyEnd})
	detail.scrollWheel(tea.MouseButtonWheelUp)
	offset = detail.viewport.YOffset
	detail.update(tea.KeyMsg{Type: tea.KeyDown})
	if detail.selectedLine != len(detail.lines)-1 || detail.viewport.YOffset != offset+1 {
		t.Fatal("outward down must scroll one row with the cursor stationary")
	}
}

func TestOverviewWrappedCursorClick(t *testing.T) {
	m := newOverviewCursorModel(t)
	detail := detailStateFromScreen(t, m.detail)
	detail.resize(28, 16)
	line := overviewCostLine(t, detail)
	first, end := detail.firstRenderedRow(line), detail.firstRenderedRow(line+1)
	if end-first < 2 {
		t.Fatal("fixture cost line must wrap")
	}
	detail.selectRow(line - 1)
	detail.update(tea.KeyMsg{Type: tea.KeyDown})
	if detail.selectedLine != line || first < detail.viewport.YOffset || end > detail.viewport.YOffset+detail.viewport.Height {
		t.Fatal("wrapped line must move as one visible unit")
	}
	detail.selectRow(line - 1)
	y := newDetailLayout(detail.height).contentY + first + 1 - detail.viewport.YOffset
	updated, _ := m.Update(tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.session.ID != "route" || detail.selectedLine != line || len(m.detailStack) != 0 {
		t.Fatal("wrapped continuation did not select its line")
	}
	detail.update(tea.KeyMsg{Type: tea.KeyDown})
	if detail.selectedLine != line+1 {
		t.Fatal("down did not leave wrapped line in one press")
	}
}

func TestOverviewMetadataCursorSurvivesAsyncLoad(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", child), func(t *testing.T) {
			session := overviewCursorSession()
			root := session
			if child {
				root = &model.Session{ID: "parent", Agent: model.AgentClaude, Path: "/workspace/parent.jsonl", Subagents: []*model.Session{session}, Events: []model.Event{{Kind: model.EventSubagent, Subagent: session}}}
			}
			registry := source.NewRegistry([]source.Source{detailTestSource{session: root}}, source.Options{})
			m := NewModel([]*model.Session{root}, registry)
			if child {
				m.screen = screenDetail
				m.detail = newDetailState(root, m.width, m.height, m.styles)
				m.detailGeneration = 1
			}
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if cmd == nil {
				t.Fatal("fixture must load asynchronously")
			}
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(Model)
			detail := detailStateFromScreen(t, m.detail)
			line := overviewCostLine(t, detail)
			detail.selectRow(line)
			updated, _ = m.Update(cmd())
			m = updated.(Model)
			detail = detailStateFromScreen(t, m.detail)
			if detail.loadStatus != detailStatusLoaded || detail.tab != tabOverview || detail.selectedLine != line || detail.focusedSubagent() != nil {
				t.Fatalf("async load lost metadata cursor: status=%v tab=%v line=%d, want %d", detail.loadStatus, detail.tab, detail.selectedLine, line)
			}
		})
	}
}
