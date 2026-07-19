package tui

import (
	"context"
	"errors"
	"fmt"
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

func detailStateFromScreen(t testing.TB, screen detailScreen) *detailState {
	t.Helper()
	detail, ok := screen.(*detailState)
	if !ok {
		t.Fatalf("detail screen type = %T, want *detailState", screen)
	}
	return detail
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

func TestEnterOnSubagentDrillsIntoChild(t *testing.T) {
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
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if detailStateFromScreen(t, m.detail).session != child {
		t.Fatalf("detail session = %q, want drilled child %q", detailStateFromScreen(t, m.detail).session.ID, child.ID)
	}
}

func TestDrilledSubagentInheritsWrap(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'w'}},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if !detailStateFromScreen(t, m.detail).wrap {
		t.Fatal("drilled subagent did not inherit parent wrap")
	}
}

func TestDrillShowsAncestorBreadcrumb(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout terrain"}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Project: "starship", Title: "Plan route", Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if view := ansi.Strip(m.View()); !strings.Contains(view, "Session · starship › Plan route") {
		t.Fatalf("drilled detail missing ancestor breadcrumb:\n%s", view)
	}
}

func TestEscapePopsThenReturnsToList(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if m.screen != screenDetail || m.detail == nil || detailStateFromScreen(t, m.detail).session != parent {
		t.Fatalf("first escape screen=%v detail=%#v, want parent detail", m.screen, m.detail)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.screen != screenList || m.detail != nil {
		t.Fatalf("second escape screen=%v detail=%#v, want list", m.screen, m.detail)
	}
}

func TestLeftAndHPopDrilledDetail(t *testing.T) {
	for _, back := range []tea.KeyMsg{{Type: tea.KeyLeft}, {Type: tea.KeyRunes, Runes: []rune{'h'}}} {
		t.Run(back.String(), func(t *testing.T) {
			child := &model.Session{ID: "scout", Agent: model.AgentClaude}
			parent := &model.Session{
				ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
				Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
			}
			m := NewModel([]*model.Session{parent}, nil)
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}, back} {
				updated, _ := m.Update(key)
				m = updated.(Model)
			}
			if m.screen != screenDetail || detailStateFromScreen(t, m.detail).session != parent || len(m.detailStack) != 0 {
				t.Fatalf("%q did not pop to parent: screen=%v detail=%q stack=%d", back.String(), m.screen, detailStateFromScreen(t, m.detail).session.ID, len(m.detailStack))
			}
		})
	}
}

func TestWindowSizeResizesStackedDetailScreens(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeySpace},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.WindowSizeMsg{Width: 104, Height: 31},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	if detailStateFromScreen(t, m.detail).width != 104 || detailStateFromScreen(t, m.detail).height != 31 {
		t.Fatalf("top detail size = %dx%d, want 104x31", detailStateFromScreen(t, m.detail).width, detailStateFromScreen(t, m.detail).height)
	}
	if parentDetail := detailStateFromScreen(t, m.detailStack[0]); parentDetail.width != 104 || parentDetail.height != 31 {
		t.Fatalf("stacked detail size = %dx%d, want 104x31", parentDetail.width, parentDetail.height)
	}
}

func TestWindowSizeResizesItemAndParentDetailScreens(t *testing.T) {
	session := &model.Session{
		ID: "route", Agent: model.AgentClaude,
		Events: []model.Event{{Kind: model.EventUser, Text: strings.Repeat("Chart the lunar route ", 20)}},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.WindowSizeMsg{Width: 104, Height: 31},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	item, ok := m.detail.(*itemView)
	if !ok || item.width != 104 || item.height != 31 {
		t.Fatalf("resized active screen = %T %#v, want 104x31 item", m.detail, m.detail)
	}
	parent := detailStateFromScreen(t, m.detailStack[0])
	if parent.width != 104 || parent.height != 31 {
		t.Fatalf("resized item parent = %dx%d, want 104x31", parent.width, parent.height)
	}
	for number, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > 104 {
			t.Fatalf("resized item line %d width = %d, want <= 104: %q", number+1, width, line)
		}
	}
}

func TestThemeCycleUpdatesStackedDetailScreens(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := newModelWithClockAndTheme([]*model.Session{parent}, nil, time.Now, themes["default"])
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'t'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	want := m.styles.title.Render("tab")
	if got := detailStateFromScreen(t, m.detailStack[0]).styles.title.Render("tab"); got != want {
		t.Fatalf("stacked detail retained stale theme: got %q, want %q", got, want)
	}
}

func TestRightAndLDrillIntoSubagents(t *testing.T) {
	for _, open := range []tea.KeyMsg{
		{Type: tea.KeyRight},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
	} {
		t.Run(open.String(), func(t *testing.T) {
			child := &model.Session{ID: "scout", Agent: model.AgentClaude}
			parent := &model.Session{
				ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
				Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
			}
			m := NewModel([]*model.Session{parent}, nil)
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, open} {
				updated, _ := m.Update(key)
				m = updated.(Model)
			}
			if detailStateFromScreen(t, m.detail).session != child {
				t.Fatalf("%q left detail session at %q, want child", open.String(), detailStateFromScreen(t, m.detail).session.ID)
			}
		})
	}
}

func TestDrillDownScreenSupportsVimEdges(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey ridge"},
		{Kind: model.EventAssistantText, Text: "Ridge clear"},
		{Kind: model.EventUser, Text: "Survey valley"},
		{Kind: model.EventAssistantText, Text: "Valley clear"},
	}}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'G'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if want := len(detailStateFromScreen(t, m.detail).focusables) - 1; detailStateFromScreen(t, m.detail).focus != want {
		t.Fatalf("drilled G focus = %d, want %d", detailStateFromScreen(t, m.detail).focus, want)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).focus != 0 {
		t.Fatalf("drilled g focus = %d, want 0", detailStateFromScreen(t, m.detail).focus)
	}
}

func TestNestedSubagentDrillAccumulatesBreadcrumbs(t *testing.T) {
	verifier := &model.Session{ID: "verify", Agent: model.AgentCodex, Title: "Verify cavern"}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge", Subagents: []*model.Session{verifier},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: verifier}},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Project: "starship", Title: "Plan route", Subagents: []*model.Session{scout},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: scout}},
	}
	m := NewModel([]*model.Session{root}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for range 2 {
		for _, key := range []tea.KeyMsg{{Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
			updated, _ = m.Update(key)
			m = updated.(Model)
		}
	}

	if detailStateFromScreen(t, m.detail).session != verifier || len(m.detailStack) != 2 {
		t.Fatalf("nested drill detail=%q stack=%d, want verifier over two parents", detailStateFromScreen(t, m.detail).session.ID, len(m.detailStack))
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Session · starship › Plan route › Scout ridge") {
		t.Fatalf("nested drill missing accumulated breadcrumb:\n%s", view)
	}
}

func TestTabSwitchesDetailToSubagents(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude}, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	if view := ansi.Strip(detail.view()); !strings.Contains(view, "╭─ Subagents ") {
		t.Fatalf("tab did not activate the Subagents panel:\n%s", view)
	}
}

func TestShiftTabSwitchesDetailBackToTimeline(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude}, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyShiftTab})

	if detail.tab != tabTimeline {
		t.Fatalf("shift+tab active tab = %v, want Timeline", detail.tab)
	}
}

func TestSubagentsTabShowsEmptyState(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude}, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	if view := ansi.Strip(detail.view()); !strings.Contains(view, "No subagents") {
		t.Fatalf("empty Subagents tab missing empty state:\n%s", view)
	}
}

func TestSubagentsEmptyEdgeKeysRemainUnselected(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: "Plan route"}}}
	detail := newDetailState(session, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'G'}}, {Type: tea.KeyRunes, Runes: []rune{'g'}}} {
		detail.update(key)
	}

	if detail.selectedLine != -1 || strings.Contains(ansi.Strip(detail.view()), "› No subagents") {
		t.Fatalf("empty Subagents tab gained a selection:\n%s", ansi.Strip(detail.view()))
	}
}

func TestEmptySubagentsRejectsAllNavigationAndDrillKeys(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: "Plan route"}}}
	m := NewModel([]*model.Session{session}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	for range 2 {
		for _, key := range []tea.KeyMsg{
			{Type: tea.KeyDown}, {Type: tea.KeyUp},
			{Type: tea.KeyRunes, Runes: []rune{'g'}}, {Type: tea.KeyRunes, Runes: []rune{'G'}},
			{Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyRunes, Runes: []rune{'K'}},
			{Type: tea.KeySpace}, {Type: tea.KeyEnter}, {Type: tea.KeyRight}, {Type: tea.KeyRunes, Runes: []rune{'l'}},
		} {
			updated, _ := m.Update(key)
			m = updated.(Model)
		}
	}

	view := ansi.Strip(m.View())
	if detailStateFromScreen(t, m.detail).session != session || detailStateFromScreen(t, m.detail).tab != tabSubagents || len(m.detailStack) != 0 || detailStateFromScreen(t, m.detail).selectedLine != -1 {
		t.Fatalf("empty key storm changed state: session=%q tab=%v stack=%d selected=%d", detailStateFromScreen(t, m.detail).session.ID, detailStateFromScreen(t, m.detail).tab, len(m.detailStack), detailStateFromScreen(t, m.detail).selectedLine)
	}
	if !strings.Contains(view, "No subagents") || strings.Contains(view, "› No subagents") {
		t.Fatalf("empty key storm corrupted empty state:\n%s", view)
	}
}

func TestSubagentsErrorStateClearsHiddenSelection(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	detail := newDetailState(&model.Session{ID: "route", Subagents: []*model.Session{child}}, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.err = errors.New("fictional load failure")
	detail.rebuild()

	if subagent := detail.focusedSubagent(); subagent != nil {
		t.Fatalf("error state retained hidden subagent selection %q", subagent.ID)
	}
}

func TestLoadingSubagentsRejectStaleDrillInput(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	root := &model.Session{ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child}}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	detailStateFromScreen(t, m.detail).loading = true
	detailStateFromScreen(t, m.detail).rebuild()
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRight}, {Type: tea.KeyRunes, Runes: []rune{'l'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if detailStateFromScreen(t, m.detail).session != root || len(m.detailStack) != 0 || detailStateFromScreen(t, m.detail).focusedSubagent() != nil {
		t.Fatalf("loading drill input changed state: session=%q stack=%d focused=%#v", detailStateFromScreen(t, m.detail).session.ID, len(m.detailStack), detailStateFromScreen(t, m.detail).focusedSubagent())
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Loading timeline…") {
		t.Fatalf("loading state lost authoritative message:\n%s", view)
	}
}

func TestSubagentsTabListsAllDescendantsInPreOrder(t *testing.T) {
	verifier := &model.Session{
		ID: "verify", Agent: model.AgentCodex, Title: "Verify cavern", Models: []string{"gpt-5.6-sol"},
		Usage: []model.Usage{{InputTokens: 25}}, Cost: model.Cost{USD: 0.25, Estimated: true},
	}
	mapper := &model.Session{
		ID: "map", Agent: model.AgentClaude, Title: "Map cavern", Models: []string{"claude-sonnet-4-7"},
		Usage: []model.Usage{{InputTokens: 50}}, Cost: model.Cost{USD: 0.50}, Subagents: []*model.Session{verifier},
	}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge", Models: []string{"claude-opus-4-8"},
		Usage: []model.Usage{{InputTokens: 100}}, Cost: model.Cost{USD: 1.00}, Subagents: []*model.Session{mapper},
	}
	root := &model.Session{ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{scout}}
	detail := newDetailState(root, 120, 16, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	view := ansi.Strip(detail.view())

	wants := []struct {
		prefix string
		row    string
	}{
		{prefix: "│› claude", row: "Scout ridge  opus-4.8 · 175 · ~$1.75"},
		{prefix: "│    claude", row: "Map cavern  sonnet-4-7 · 75 · ~$0.75"},
		{prefix: "│      codex", row: "Verify cavern  gpt-5.6 · 25 · ~$0.25"},
	}
	position := 0
	for _, want := range wants {
		next := strings.Index(view[position:], want.prefix)
		if next < 0 {
			t.Fatalf("Subagents tab missing indented prefix %q:\n%s", want.prefix, view)
		}
		position += next
		end := strings.IndexByte(view[position:], '\n')
		if end < 0 || !strings.Contains(view[position:position+end], want.row) {
			t.Fatalf("Subagents row %q missing totals or model in %q:\n%s", want.row, view[position:position+max(0, end)], view)
		}
		position += end
	}
}

func TestMaximumSupportedSubagentDepthDrillsDirectly(t *testing.T) {
	deepest := &model.Session{ID: "node-64", Agent: model.AgentCodex, Title: "Node 64"}
	child := deepest
	for depth := 63; depth >= 1; depth-- {
		child = &model.Session{
			ID: fmt.Sprintf("node-%02d", depth), Agent: model.AgentClaude, Title: fmt.Sprintf("Node %02d", depth),
			Subagents: []*model.Session{child},
		}
	}
	root := &model.Session{ID: "root", Agent: model.AgentClaude, Title: "Root", Subagents: []*model.Session{child}}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}, {Type: tea.KeyRunes, Runes: []rune{'G'}}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if detailStateFromScreen(t, m.detail).session != deepest || len(m.detailStack) != 1 || len(detailStateFromScreen(t, m.detail).crumbs) != 64 {
		t.Fatalf("deep direct drill: session=%q stack=%d crumbs=%d", detailStateFromScreen(t, m.detail).session.ID, len(m.detailStack), len(detailStateFromScreen(t, m.detail).crumbs))
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 1, Height: 3})
	m = updated.(Model)
	for number, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > 1 {
			t.Fatalf("deep drill line %d width = %d, want <= 1: %q", number+1, width, line)
		}
	}
	if detailStateFromScreen(t, m.detail).width != 1 || detailStateFromScreen(t, m.detailStack[0]).width != 1 {
		t.Fatalf("deep resize widths: top=%d stack=%d, want 1", detailStateFromScreen(t, m.detail).width, detailStateFromScreen(t, m.detailStack[0]).width)
	}
}

func TestEnterOnSubagentsTabDrillsIntoSelection(t *testing.T) {
	mapper := &model.Session{ID: "map", Agent: model.AgentClaude, Title: "Map cavern"}
	scout := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge", Subagents: []*model.Session{mapper}}
	root := &model.Session{ID: "route", Agent: model.AgentClaude, Project: "starship", Title: "Plan route", Subagents: []*model.Session{scout}}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyTab},
		{Type: tea.KeyDown},
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if detailStateFromScreen(t, m.detail).session != mapper || detailStateFromScreen(t, m.detail).tab != tabTimeline || len(m.detailStack) != 1 {
		t.Fatalf("drilled detail=%q tab=%v stack=%d, want mapper Timeline over one parent", detailStateFromScreen(t, m.detail).session.ID, detailStateFromScreen(t, m.detail).tab, len(m.detailStack))
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Session · starship › Plan route › Scout ridge") {
		t.Fatalf("direct nested drill omitted intermediate breadcrumb:\n%s", view)
	}
}

func TestSubagentDrillRestoresParentTabSelectionAndWrap(t *testing.T) {
	mapper := &model.Session{ID: "map", Agent: model.AgentCodex, Title: "Map cavern"}
	scout := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge", Subagents: []*model.Session{mapper}}
	root := &model.Session{ID: "route", Agent: model.AgentClaude, Project: "starship", Title: "Plan route", Subagents: []*model.Session{scout}}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'w'}},
		{Type: tea.KeyTab},
		{Type: tea.KeyRunes, Runes: []rune{'G'}},
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if detailStateFromScreen(t, m.detail).session != mapper || detailStateFromScreen(t, m.detail).tab != tabTimeline || !detailStateFromScreen(t, m.detail).wrap {
		t.Fatalf("drilled child state: session=%q tab=%v wrap=%t, want mapper Timeline with wrap", detailStateFromScreen(t, m.detail).session.ID, detailStateFromScreen(t, m.detail).tab, detailStateFromScreen(t, m.detail).wrap)
	}

	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'w'}}, {Type: tea.KeyTab}, {Type: tea.KeyRunes, Runes: []rune{'h'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if detailStateFromScreen(t, m.detail).session != root || detailStateFromScreen(t, m.detail).tab != tabSubagents || detailStateFromScreen(t, m.detail).subagentSelection != 1 || !detailStateFromScreen(t, m.detail).wrap {
		t.Fatalf("restored parent state: session=%q tab=%v selection=%d wrap=%t", detailStateFromScreen(t, m.detail).session.ID, detailStateFromScreen(t, m.detail).tab, detailStateFromScreen(t, m.detail).subagentSelection, detailStateFromScreen(t, m.detail).wrap)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).session != mapper || detailStateFromScreen(t, m.detail).tab != tabTimeline || !detailStateFromScreen(t, m.detail).wrap {
		t.Fatalf("repeat drill state: session=%q tab=%v wrap=%t, want fresh mapper Timeline with inherited wrap", detailStateFromScreen(t, m.detail).session.ID, detailStateFromScreen(t, m.detail).tab, detailStateFromScreen(t, m.detail).wrap)
	}
}

func TestSubagentsTabNavigationUsesOwnSelection(t *testing.T) {
	session := &model.Session{ID: "route", Subagents: []*model.Session{
		{ID: "scout", Agent: model.AgentClaude},
		{ID: "map", Agent: model.AgentCodex},
		{ID: "verify", Agent: model.AgentClaude},
	}}
	detail := newDetailState(session, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	detail.update(tea.KeyMsg{Type: tea.KeyDown})
	if detail.subagentSelection != 1 {
		t.Fatalf("j selection = %d, want 1", detail.subagentSelection)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyUp})
	if detail.subagentSelection != 0 {
		t.Fatalf("k selection = %d, want 0", detail.subagentSelection)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if detail.subagentSelection != 2 {
		t.Fatalf("G selection = %d, want 2", detail.subagentSelection)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if detail.subagentSelection != 0 {
		t.Fatalf("g selection = %d, want 0", detail.subagentSelection)
	}
}

func TestDetailPanelTitleShowsActiveAndFaintInactiveTab(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude}, 80, 12, newStyles(themes["default"]))

	timelineTitle := strings.Split(detail.view(), "\n")[5]
	if !strings.Contains(ansi.Strip(timelineTitle), "Timeline · Subagents") || !strings.Contains(timelineTitle, "\x1b[2;") {
		t.Fatalf("Timeline title did not show a faint inactive tab: %q", timelineTitle)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	subagentsTitle := strings.Split(detail.view(), "\n")[5]
	if !strings.Contains(ansi.Strip(subagentsTitle), "Subagents · Timeline") || !strings.Contains(subagentsTitle, "\x1b[2;") {
		t.Fatalf("Subagents title did not show a faint inactive tab: %q", subagentsTitle)
	}
}

func TestCompactDetailTitleShowsActiveAndFaintInactiveTab(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude}, 80, 8, newStyles(themes["default"]))

	title := strings.Split(detail.view(), "\n")[0]
	if !strings.Contains(ansi.Strip(title), "Timeline · Subagents") || !strings.Contains(title, "\x1b[2;") {
		t.Fatalf("compact title did not show a faint inactive tab: %q", title)
	}
}

func TestSubagentsRowAppliesCellStylesAfterFitting(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	first := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge"}
	selected := &model.Session{
		ID: "map", Agent: model.AgentCodex, Title: "Map cavern", Models: []string{"gpt-5.6-sol"},
		Usage: []model.Usage{{InputTokens: 2_500}}, Cost: model.Cost{USD: 0.75, Estimated: true},
	}
	styleSet := newStyles(themes["default"])
	detail := newDetailState(&model.Session{ID: "route", Subagents: []*model.Session{first, selected}}, 100, 14, styleSet)
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	line := detail.lines[1]
	styled := detail.styleLine(detail.rendered[1].text, line, false)

	for name, want := range map[string]string{
		"agent":  styleSet.codex.Render("codex"),
		"tokens": styleSet.accent.Render(humanTokens(selected.TotalUsage().TotalTokens())),
		"cost":   styleSet.estimated.Render(formatCost(selected.TotalCost())),
	} {
		if !strings.Contains(styled, want) {
			t.Errorf("Subagents row missing %s cell style in %q", name, styled)
		}
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
	if !strings.Contains(keyBar, "space expand") || !strings.Contains(keyBar, "↵ open") || !strings.Contains(keyBar, "tab tabs") || !strings.Contains(keyBar, "w wrap") || !strings.Contains(keyBar, "q quit") || strings.Contains(keyBar, "…") {
		t.Fatalf("80-column detail key bar = %q, want expand, open, tabs, wrap, and quit hints without truncation", keyBar)
	}
}

func TestNarrowKeyBarsKeepWholeEssentialHints(t *testing.T) {
	t.Run("detail", func(t *testing.T) {
		for _, test := range []struct {
			width int
			wants []string
		}{
			{width: 40, wants: []string{"space expand", "↵ open", "esc back"}},
			{width: 20, wants: []string{"↵ open", "esc back"}},
			{width: 10, wants: []string{"esc back"}},
		} {
			text := detailKeyText(test.width, true, tabTimeline)
			if width := ansi.StringWidth(text); width > test.width {
				t.Fatalf("%d-column detail hints width = %d: %q", test.width, width, text)
			}
			for _, want := range test.wants {
				if !strings.Contains(text, want) {
					t.Fatalf("%d-column detail hints missing %q: %q", test.width, want, text)
				}
			}
		}
	})

	t.Run("item", func(t *testing.T) {
		item := newItemView(model.Event{Kind: model.EventThinking, Text: "Chart route"}, model.AgentClaude, nil, 20, 8, newStyles())
		keyBar := strings.TrimSpace(strings.Split(ansi.Strip(item.view()), "\n")[7])
		if !strings.Contains(keyBar, "esc back") || strings.Contains(keyBar, "…") || ansi.StringWidth(keyBar) > 20 {
			t.Fatalf("20-column item key bar lost a whole back hint: %q", keyBar)
		}
	})
}

func TestSubagentsKeyBarOmitsTimelineOnlyJumpHint(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "route"}, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	keyBar := strings.Split(ansi.Strip(detail.view()), "\n")[11]

	if strings.Contains(keyBar, "J/K subagent") {
		t.Fatalf("Subagents key bar advertised Timeline-only J/K binding: %q", keyBar)
	}
}

func TestSpaceOnSubagentDoesNotExpandInline(t *testing.T) {
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
		{Type: tea.KeyDown},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeySpace},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if view := m.View(); strings.Contains(view, "Inspect the ridge") {
		t.Fatalf("space expanded a subagent inline:\n%s", view)
	}
}

func TestSubagentLineHasNoInlineToggle(t *testing.T) {
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

	view := m.View()
	if !strings.Contains(view, "Task(Scout terrain) opus-4.8") || strings.Contains(view, "Task(Scout terrain) ▸") {
		t.Fatalf("subagent line retained an inline toggle:\n%s", view)
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

func TestDetailLoadPreservesSubagentsTab(t *testing.T) {
	current := &model.Session{ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl"}
	m := NewModel([]*model.Session{current}, nil)
	m.screen = screenDetail
	m.detail = newDetailState(current, m.width, m.height, m.styles)
	detailStateFromScreen(t, m.detail).update(tea.KeyMsg{Type: tea.KeyTab})
	m.detailGeneration = 1
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge"}
	loaded := cloneSession(current)
	loaded.Subagents = []*model.Session{child}

	updated, _ := m.Update(detailLoadedMsg{generation: 1, identity: sessionIdentity(current), session: loaded})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).tab != tabSubagents || !strings.Contains(ansi.Strip(m.View()), "Scout ridge") {
		t.Fatalf("detail load did not preserve and rebuild Subagents tab:\n%s", ansi.Strip(m.View()))
	}
}

func TestDrilledDetailFollowsRootLiveUpdate(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl", Title: "Before"}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	replacement := cloneSession(root)
	replacement.Subagents[0].Title = "After"

	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).session != replacement.Subagents[0] || detailStateFromScreen(t, m.detail).session.Title != "After" || detailStateFromScreen(t, m.detailStack[0]).session != replacement {
		t.Fatalf("drilled detail did not rebind updated root graph: detail=%#v stack=%#v", detailStateFromScreen(t, m.detail).session, detailStateFromScreen(t, m.detailStack[0]).session)
	}
}

func TestOpenItemFollowsRootLiveUpdate(t *testing.T) {
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Project: "starship", Title: "Plan route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route", Detail: &model.ToolDetail{Input: "check-route", Output: "running"}},
		},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyDown}, {Type: tea.KeySpace}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	replacement := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Project: "voyage", Title: "Plan updated route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route", Detail: &model.ToolDetail{Input: "check-route", Output: "finished"}},
		},
	}
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	view := ansi.Strip(m.View())
	if _, ok := m.detail.(*itemView); !ok || len(m.detailStack) != 1 {
		t.Fatalf("live update left screen=%T stack=%d, want refreshed item", m.detail, len(m.detailStack))
	}
	for _, want := range []string{"voyage › Plan updated route › Bash", "finished"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed item missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "running") {
		t.Fatalf("refreshed item retained stale output:\n%s", view)
	}
}

func TestOpenItemFallsBackWhenEventDisappears(t *testing.T) {
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route"},
		},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyDown}, {Type: tea.KeySpace}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	replacement := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Events: []model.Event{{Kind: model.EventUser, Text: "Check the route"}},
	}
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	if detailStateFromScreen(t, m.detail).session != replacement || len(m.detailStack) != 0 {
		t.Fatalf("removed item event left screen=%T stack=%d, want refreshed root detail", m.detail, len(m.detailStack))
	}
}

func TestSubagentSelectionFollowsIdentityAcrossLiveReorder(t *testing.T) {
	scout := &model.Session{ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl"}
	mapper := &model.Session{ID: "mapper", Agent: model.AgentCodex, Path: "/workspace/mapper.jsonl"}
	verifier := &model.Session{ID: "verifier", Agent: model.AgentClaude, Path: "/workspace/verifier.jsonl"}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Subagents: []*model.Session{scout, mapper, verifier},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}, {Type: tea.KeyDown}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	replacement := cloneSession(root)
	replacement.Subagents = []*model.Session{
		replacement.Subagents[2],
		replacement.Subagents[0],
		replacement.Subagents[1],
	}
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	if got := detailStateFromScreen(t, m.detail).focusedSubagent(); got != replacement.Subagents[2] {
		t.Fatalf("live reorder selected %#v, want replacement mapper %#v", got, replacement.Subagents[2])
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).session != replacement.Subagents[2] {
		t.Fatalf("drill after live reorder opened %q, want mapper", detailStateFromScreen(t, m.detail).session.ID)
	}
}

func TestLiveUpdateRefreshesDrilledBreadcrumbs(t *testing.T) {
	mapper := &model.Session{ID: "mapper", Agent: model.AgentCodex, Path: "/workspace/mapper.jsonl", Title: "Map before"}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl", Title: "Scout before",
		Subagents: []*model.Session{mapper},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Project: "voyage-before", Title: "Plan before",
		Subagents: []*model.Session{scout},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	replacement := cloneSession(root)
	replacement.Project = "voyage-after"
	replacement.Title = "Plan after"
	replacement.Subagents[0].Title = "Scout after"
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	want := []string{"voyage-after", "Plan after", "Scout after"}
	if !slices.Equal(detailStateFromScreen(t, m.detail).crumbs, want) {
		t.Fatalf("live breadcrumb = %#v, want %#v", detailStateFromScreen(t, m.detail).crumbs, want)
	}
	if got := detailStateFromScreen(t, m.detailStack[0]).crumbs; !slices.Equal(got, []string{"voyage-after"}) {
		t.Fatalf("live root breadcrumb = %#v, want updated project", got)
	}
}

func TestLiveUpdateRefreshesRecursiveSubagentTotals(t *testing.T) {
	mapper := &model.Session{
		ID: "mapper", Agent: model.AgentCodex, Path: "/workspace/mapper.jsonl",
		Usage: []model.Usage{{InputTokens: 50}}, Cost: model.Cost{USD: 0.05, Estimated: true},
	}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl",
		Usage: []model.Usage{{InputTokens: 100}}, Cost: model.Cost{USD: 0.10}, Subagents: []*model.Session{mapper},
	}
	root := &model.Session{ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Subagents: []*model.Session{scout}}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	replacement := cloneSession(root)
	replacement.Subagents[0].Subagents[0].Usage = []model.Usage{{InputTokens: 250}}
	replacement.Subagents[0].Subagents[0].Cost = model.Cost{USD: 0.25, Estimated: true}
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	for _, line := range detailStateFromScreen(t, m.detail).lines {
		if line.subagentSession == nil {
			continue
		}
		wantTokens := humanTokens(line.subagentSession.TotalUsage().TotalTokens())
		wantCost := formatCost(line.subagentSession.TotalCost())
		if line.subagentTokens != wantTokens || line.subagentCost != wantCost {
			t.Errorf("%s cached totals = %q/%q, want %q/%q", line.subagentSession.ID, line.subagentTokens, line.subagentCost, wantTokens, wantCost)
		}
	}
	if got := detailStateFromScreen(t, m.detail).lines[0].subagentTokens; got != "350" {
		t.Fatalf("refreshed scout recursive tokens = %q, want 350", got)
	}
	if got := detailStateFromScreen(t, m.detail).lines[0].subagentCost; got != "~$0.35" {
		t.Fatalf("refreshed scout recursive cost = %q, want ~$0.35", got)
	}
}

func TestRemovingDrilledRootClearsDetailStack(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl"}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	updated, _ := m.Update(source.SessionUpdate{RemovedPaths: []string{root.Path}})
	m = updated.(Model)
	if m.screen != screenList || m.detail != nil || len(m.detailStack) != 0 {
		t.Fatalf("removed drilled root left detail state: screen=%v detail=%#v stack=%d", m.screen, m.detail, len(m.detailStack))
	}
}

func TestRemovingActiveDescendantFallsBackToSurvivingAncestor(t *testing.T) {
	mapper := &model.Session{ID: "mapper", Agent: model.AgentCodex, Path: "/workspace/mapper.jsonl"}
	scout := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl", Subagents: []*model.Session{mapper},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: mapper}},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Subagents: []*model.Session{scout},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: scout}},
	}
	m := NewModel([]*model.Session{root}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for range 2 {
		for _, key := range []tea.KeyMsg{{Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
			updated, _ = m.Update(key)
			m = updated.(Model)
		}
	}

	replacement := cloneSession(root)
	replacement.Subagents[0].Subagents = nil
	replacement.Subagents[0].Events = nil
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).session != replacement.Subagents[0] || len(m.detailStack) != 1 || detailStateFromScreen(t, m.detailStack[0]).session != replacement {
		t.Fatalf("descendant removal fallback: detail=%#v stack=%#v", detailStateFromScreen(t, m.detail).session, m.detailStack)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail).session != replacement || len(m.detailStack) != 0 {
		t.Fatalf("back after fallback: detail=%#v stack=%d", detailStateFromScreen(t, m.detail).session, len(m.detailStack))
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

func TestSubagentsAndBreadcrumbsSanitizeTerminalText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	child := &model.Session{
		ID:     "mapper",
		Agent:  model.AgentKind("codex\x1b[31m\nforged"),
		Title:  "Map\x1b]8;;https://invalid.example\a ridge\u202e",
		Models: []string{"gpt\t5\x1b[2J"},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Project: "voyage\x1b[31m\u202e", Title: "Plan route\nforged",
		Subagents: []*model.Session{child},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 20, Height: 8},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyTab},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	assertSafe := func(name, view string) {
		t.Helper()
		if strings.ContainsAny(view, "\r\x1b\a") || strings.ContainsRune(view, '\u202e') {
			t.Fatalf("%s emitted unsafe terminal data %q", name, view)
		}
		for number, line := range strings.Split(view, "\n") {
			if width := ansi.StringWidth(line); width > 20 {
				t.Fatalf("%s line %d width = %d, want <= 20: %q", name, number+1, width, line)
			}
		}
	}
	assertSafe("Subagents tab", m.View())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	assertSafe("drilled breadcrumb", m.View())
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

func TestDetailHeaderOmitsUnavailableMetadata(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &model.Session{ID: "mission", Agent: model.AgentClaude}

	header := ansi.Strip(newDetailState(session, 200, 12, newStyles()).header())
	for _, artifact := range []string{"()", "—", " ·  · ", "·  ·"} {
		if strings.Contains(header, artifact) {
			t.Fatalf("sparse detail header contains %q:\n%s", artifact, header)
		}
	}
	if !strings.Contains(header, "claude") {
		t.Fatalf("sparse detail header omitted available agent:\n%s", header)
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

func TestEnterDoesNotExpandToolInPlace(t *testing.T) {
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
	if detail.expanded[toolKey] {
		t.Fatal("enter expanded a tool in place; only space may expand")
	}
	line := detail.lines[detail.focusables[detail.focus].line]
	if !strings.Contains(line.text, glyphCollapsed+" "+glyphTool) {
		t.Fatalf("tool line after enter = %q, want collapsed marker", line.text)
	}
}

func TestEnterOnToolOpensFullItemView(t *testing.T) {
	input := make([]string, detailPreviewLineCap+2)
	diff := make([]string, detailPreviewLineCap+2)
	output := make([]string, detailPreviewLineCap+2)
	for index := range input {
		input[index] = fmt.Sprintf("command-%02d", index)
		diff[index] = fmt.Sprintf("+route-%02d", index)
		output[index] = fmt.Sprintf("result-%02d", index)
	}
	session := &model.Session{
		ID: "lunar", Agent: model.AgentCodex, Project: "starship", Title: "Plan route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Update the route"},
			{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check route", Detail: &model.ToolDetail{
				Input: strings.Join(input, "\n"), Diff: strings.Join(diff, "\n"), Output: strings.Join(output, "\n"),
			}},
		},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 140},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeySpace},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	view := ansi.Strip(m.View())
	if len(m.detailStack) != 1 {
		t.Fatalf("tool open stack depth = %d, want 1:\n%s", len(m.detailStack), view)
	}
	for _, want := range []string{"starship › Plan route › Bash", "input:", "command-41", "+route-41", "output:", "result-41"} {
		if !strings.Contains(view, want) {
			t.Fatalf("tool item view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "lines hidden") {
		t.Fatalf("tool item view applied the timeline line cap:\n%s", view)
	}
}

func TestItemToolLinesUseFallbacksAndDiffRoles(t *testing.T) {
	for _, test := range []struct {
		name   string
		detail *model.ToolDetail
		roles  map[string]detailRole
	}{
		{name: "nil detail"},
		{name: "partial detail", detail: &model.ToolDetail{Diff: "-old route\n context route\n+new route"}, roles: map[string]detailRole{
			"-old route": detailDiffRemove, " context route": detailDiffContext, "+new route": detailDiffAdd,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := itemEventLines(model.Event{
				Kind: model.EventToolCall, ToolName: "Bash", ToolInput: "fallback input", ResultSummary: "fallback output", Detail: test.detail,
			}, model.AgentClaude)
			texts := make([]string, len(lines))
			seenRoles := make(map[string]bool, len(test.roles))
			for index, line := range lines {
				texts[index] = line.text
				if want, ok := test.roles[line.text]; ok {
					seenRoles[line.text] = true
					if line.role != want {
						t.Errorf("diff line %q role = %v, want %v", line.text, line.role, want)
					}
				}
			}
			for _, want := range []string{"input:", "fallback input", "output:", "fallback output"} {
				if !slices.Contains(texts, want) {
					t.Errorf("item tool lines missing fallback %q: %#v", want, texts)
				}
			}
			for text := range test.roles {
				if !seenRoles[text] {
					t.Errorf("item tool lines missing diff role for %q: %#v", text, texts)
				}
			}
		})
	}
}

func TestItemViewContentIsPlainTerminalTextAndWidthBounded(t *testing.T) {
	item := newItemView(model.Event{
		Kind: model.EventToolCall, ToolName: "Bash\x1b[2J", ToolInput: "fallback",
		Detail: &model.ToolDetail{
			Input:  "route\tinput\u202e\nsecond line",
			Diff:   "\x1b[31m-old\x1b[0m\n context\n\x1b[32m+new\x1b[0m",
			Output: "route\rready\x1b]8;;https://invalid.example\a link\x1b]8;;\a 航路🚀",
		},
	}, model.AgentClaude, []string{"starship\x1b[2J", "Plan\u202e route"}, 20, 10, newStyles())
	item.setWrap(true)

	for name, value := range map[string]string{
		"title": item.title(),
		"lines": func() string {
			lines := make([]string, len(item.lines))
			for index, line := range item.lines {
				lines[index] = line.text
			}
			return strings.Join(lines, "\n")
		}(),
		"view": item.view(),
	} {
		if strings.ContainsAny(value, "\r\t\x1b\a") || strings.ContainsRune(value, '\u202e') {
			t.Fatalf("item %s retained unsafe terminal data %q", name, value)
		}
	}
	for number, line := range strings.Split(item.view(), "\n") {
		if width := ansi.StringWidth(line); width > 20 {
			t.Fatalf("item line %d width = %d, want <= 20: %q", number+1, width, line)
		}
	}
}

func TestEnterOnTextRowOpensFullItemView(t *testing.T) {
	text := make([]string, detailPreviewLineCap+2)
	for index := range text {
		text[index] = fmt.Sprintf("observation-%02d", index)
	}
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude, Project: "starship", Title: "Survey ridge",
		Events: []model.Event{{Kind: model.EventAssistantText, Text: strings.Join(text, "\n")}},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 50},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	view := ansi.Strip(m.View())
	for _, want := range []string{"starship › Survey ridge › claude message", "observation-00", "observation-41"} {
		if !strings.Contains(view, want) {
			t.Fatalf("text item view missing %q:\n%s", want, view)
		}
	}
	if len(m.detailStack) != 1 {
		t.Fatalf("text open stack depth = %d, want 1", len(m.detailStack))
	}
}

func TestItemViewScrollsWithStepAndEdgeKeys(t *testing.T) {
	lines := make([]string, 12)
	for index := range lines {
		lines[index] = fmt.Sprintf("observation-%02d", index)
	}
	item := newItemView(model.Event{Kind: model.EventThinking, Text: strings.Join(lines, "\n")}, model.AgentClaude, nil, 40, 7, newStyles())

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if item.viewport.YOffset != 1 {
		t.Fatalf("j offset = %d, want 1", item.viewport.YOffset)
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if item.viewport.YOffset != 0 {
		t.Fatalf("k offset = %d, want 0", item.viewport.YOffset)
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if item.viewport.YOffset == 0 || !strings.Contains(ansi.Strip(item.view()), "observation-11") {
		t.Fatalf("G did not reveal the final item row at offset %d:\n%s", item.viewport.YOffset, ansi.Strip(item.view()))
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if item.viewport.YOffset != 0 || !strings.Contains(ansi.Strip(item.view()), "observation-00") {
		t.Fatalf("g did not restore the first item row at offset %d:\n%s", item.viewport.YOffset, ansi.Strip(item.view()))
	}
}

func TestItemViewWrapToggleRebuildsFlatPlainRows(t *testing.T) {
	text := strings.Repeat("charted route ", 12)
	item := newItemView(model.Event{Kind: model.EventAssistantText, Text: text}, model.AgentClaude, nil, 24, 8, newStyles())
	if len(item.rendered) != 1 {
		t.Fatalf("unwrapped item rows = %d, want 1", len(item.rendered))
	}

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !item.wrap || len(item.rendered) <= 1 {
		t.Fatalf("wrapped item state = wrap %t rows %d, want multiple rows", item.wrap, len(item.rendered))
	}
	for index, row := range item.rendered {
		if strings.Contains(row.text, "\x1b") || ansi.StringWidth(row.text) != item.viewport.Width {
			t.Fatalf("wrapped row %d = %q width %d, want plain width %d", index, row.text, ansi.StringWidth(row.text), item.viewport.Width)
		}
	}

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if item.wrap || len(item.rendered) != 1 {
		t.Fatalf("second wrap toggle state = wrap %t rows %d, want one truncated row", item.wrap, len(item.rendered))
	}
}

func TestItemViewLayoutChangesClampScrollOffset(t *testing.T) {
	lines := make([]string, 12)
	for index := range lines {
		lines[index] = fmt.Sprintf("observation-%02d %s", index, strings.Repeat("route ", 8))
	}

	t.Run("resize", func(t *testing.T) {
		item := newItemView(model.Event{Kind: model.EventThinking, Text: strings.Join(lines, "\n")}, model.AgentClaude, nil, 80, 7, newStyles())
		item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		item.resize(80, 10)
		maxOffset := max(0, len(item.rendered)-item.viewport.Height)
		if item.viewport.YOffset > maxOffset {
			t.Fatalf("resized item offset = %d, want <= %d", item.viewport.YOffset, maxOffset)
		}
	})

	t.Run("disable wrap", func(t *testing.T) {
		item := newItemView(model.Event{Kind: model.EventThinking, Text: strings.Join(lines[:10], "\n")}, model.AgentClaude, nil, 30, 7, newStyles())
		item.setWrap(true)
		item.viewport.SetYOffset(8)
		item.setWrap(false)
		maxOffset := max(0, len(item.rendered)-item.viewport.Height)
		if item.viewport.YOffset > maxOffset {
			t.Fatalf("unwrapped item offset = %d, want <= %d", item.viewport.YOffset, maxOffset)
		}
	})
}

func TestItemViewNarrowTitleKeepsItemLabel(t *testing.T) {
	crumbs := make([]string, 20)
	for index := range crumbs {
		crumbs[index] = fmt.Sprintf("fictional-observatory-%02d", index)
	}
	item := newItemView(
		model.Event{Kind: model.EventThinking, Text: "Chart route"},
		model.AgentClaude,
		crumbs,
		32,
		8,
		newStyles(),
	)
	title := item.title()
	if !strings.Contains(title, "Thinking") {
		t.Fatalf("narrow item title hid its label: %q", title)
	}
	if width := ansi.StringWidth(title); width > 27 {
		t.Fatalf("narrow item title width = %d, want <= 27: %q", width, title)
	}
}

func TestEscapePopsItemViewToDetail(t *testing.T) {
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude,
		Events: []model.Event{{Kind: model.EventUser, Text: "Chart the route"}},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyEnter}, {Type: tea.KeyEsc}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if m.screen != screenDetail || len(m.detailStack) != 0 || detailStateFromScreen(t, m.detail).session != session {
		t.Fatalf("escape from item restored screen=%v detail=%T stack=%d, want root detail", m.screen, m.detail, len(m.detailStack))
	}
}

func TestOpenKeysPushNonSubagentItemView(t *testing.T) {
	for _, open := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRight},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
	} {
		t.Run(open.String(), func(t *testing.T) {
			session := &model.Session{
				ID: "lunar", Agent: model.AgentClaude,
				Events: []model.Event{{Kind: model.EventUser, Text: "Chart the route"}},
			}
			m := NewModel([]*model.Session{session}, nil)
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, open} {
				updated, _ := m.Update(key)
				m = updated.(Model)
			}

			if _, ok := m.detail.(*itemView); !ok || len(m.detailStack) != 1 {
				t.Fatalf("%q opened screen=%T stack=%d, want item over one parent", open.String(), m.detail, len(m.detailStack))
			}
		})
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
