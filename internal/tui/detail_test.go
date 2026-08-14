package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/jsonl"
	"github.com/muesli/termenv"
)

type detailTestSource struct {
	session *model.Session
}

func writeRawRecord(t testing.TB, raw []byte) model.RecordRef {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := append(append([]byte(nil), raw...), '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return model.RecordRef{Path: path, Length: int64(len(raw)), Digest: sha256.Sum256(raw)}
}

func testCostBuckets(tokens int64, usd float64) model.CostBuckets {
	return model.CostBuckets{{RatePerToken: usd / float64(tokens), Tokens: tokens}}
}

func detailStateFromScreen(t testing.TB, screen detailScreen) *detailState {
	t.Helper()
	detail, ok := screen.(*detailState)
	if !ok {
		t.Fatalf("detail screen type = %T, want *detailState", screen)
	}
	return detail
}

func viewLineY(t testing.TB, view, match string, occurrence int) int {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(line, match) {
			if occurrence == 0 {
				return y
			}
			occurrence--
		}
	}
	t.Fatalf("view has no occurrence %d of %q:\n%s", occurrence, match, ansi.Strip(view))
	return 0
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
		{Type: tea.KeyRunes, Runes: []rune{'J'}},
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	detail := detailStateFromScreen(t, m.detail)
	if detail.session != child {
		t.Fatalf("detail session = %q, want drilled child %q", detail.session.ID, child.ID)
	}
	if title := strings.TrimSpace(detail.headerPanelLines()[0].plain); title != child.Title {
		t.Fatalf("drilled header title = %q, want role title %q", title, child.Title)
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

func TestDrilledSubagentInheritsDefaultExpansion(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child}}
	m := NewModel([]*model.Session{parent}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	detailStateFromScreen(t, m.detail).defaultExpanded = false
	for _, key := range []tea.KeyMsg{{Type: tea.KeyTab}, {Type: tea.KeyEnter}} {
		updated, _ = m.Update(key)
		m = updated.(Model)
	}

	if detailStateFromScreen(t, m.detail).defaultExpanded {
		t.Fatal("drilled subagent did not inherit disabled default expansion")
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

func TestLeftCollapsesFocusedTimelineRow(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventThinking, Text: "Choose the safest route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Output: "route clear"}},
		{Kind: model.EventAssistantText, Text: "The ridge route is clear."},
	}}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	for index, item := range detail.focusables {
		if item.expandable {
			detail.focus = index
			detail.selectedLine = item.line
			break
		}
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	focused := detail.focusables[detail.focus]
	if m.screen != screenDetail || len(m.detailStack) != 0 || !focused.expandable || detail.isExpanded(focused.key) {
		t.Fatalf("left left screen=%v stack=%d expandable=%t expanded=%t, want collapsed root detail", m.screen, len(m.detailStack), focused.expandable, detail.isExpanded(focused.key))
	}
}

func TestHPopsDrilledDetail(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'h'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if m.screen != screenDetail || detailStateFromScreen(t, m.detail).session != parent || len(m.detailStack) != 0 {
		t.Fatalf("h did not pop to parent: screen=%v detail=%q stack=%d", m.screen, detailStateFromScreen(t, m.detail).session.ID, len(m.detailStack))
	}
}

func TestRightExpandsFocusedTimelineRow(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventThinking, Text: "Choose the safest route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Output: "route clear"}},
		{Kind: model.EventAssistantText, Text: "The ridge route is clear."},
	}}
	detail := newDetailState(session, 80, 20, newStyles())
	for index, item := range detail.focusables {
		if item.expandable {
			detail.focus = index
			detail.selectedLine = item.line
			detail.collapseFocused()
			break
		}
	}
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = detail
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	focused := detail.focusables[detail.focus]
	if !focused.expandable || !detail.isExpanded(focused.key) || len(m.detailStack) != 0 {
		t.Fatalf("right left expandable=%t expanded=%t stack=%d, want expanded root detail", focused.expandable, detail.isExpanded(focused.key), len(m.detailStack))
	}
}

func TestRowCounterTracksTimelineCursorNotScroll(t *testing.T) {
	var events []model.Event
	for i := 0; i < 40; i++ {
		events = append(events, model.Event{Kind: model.EventAssistantText, Text: fmt.Sprintf("reply %d", i)})
	}
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude, Events: events}, 80, 20, newStyles())

	rows := len(detail.focusables)
	if rows < 5 {
		t.Fatalf("expected several focusable rows, got %d", rows)
	}

	// Opening a session lands the cursor on the last row, so the counter reads
	// last/last — not the scroll-top line a viewport-based counter would show.
	detail.gotoBottom()
	if c, total := detail.rowCounter(); c != rows || total != rows {
		t.Fatalf("last-row counter = %d/%d, want %d/%d", c, total, rows, rows)
	}
	if view := ansi.Strip(detail.view()); !strings.Contains(view, fmt.Sprintf("%d/%d", rows, rows)) {
		t.Fatalf("last-row border missing %d/%d counter:\n%s", rows, rows, view)
	}

	// Each cursor step walks the counter by exactly one row.
	detail.update(tea.KeyMsg{Type: tea.KeyUp})
	if c, total := detail.rowCounter(); c != rows-1 || total != rows {
		t.Fatalf("after one up counter = %d/%d, want %d/%d", c, total, rows-1, rows)
	}
}

func TestRowCounterCountsSubagentRowsExcludingHeader(t *testing.T) {
	var subs []*model.Session
	for i := 0; i < 15; i++ {
		subs = append(subs, &model.Session{ID: fmt.Sprintf("sub-%d", i), Agent: model.AgentClaude, Title: fmt.Sprintf("worker %d", i)})
	}
	detail := newDetailState(&model.Session{ID: "root", Agent: model.AgentClaude, Subagents: subs}, 80, 20, newStyles())
	detail.tab = tabSubagents
	detail.subagentSelection = len(subs) - 1
	detail.rebuild()

	// The header row is not navigable, so the count is the subagent total and the
	// last subagent reads n/n.
	if c, total := detail.rowCounter(); c != len(subs) || total != len(subs) {
		t.Fatalf("last-subagent counter = %d/%d, want %d/%d", c, total, len(subs), len(subs))
	}
}

func TestArrowsNeverNavigateNonFoldableDetailScreens(t *testing.T) {
	for _, arrow := range []tea.KeyMsg{{Type: tea.KeyLeft}, {Type: tea.KeyRight}} {
		t.Run("timeline/"+arrow.String(), func(t *testing.T) {
			session := &model.Session{ID: "route", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: "Chart the route"}}}
			m := NewModel([]*model.Session{session}, nil)
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, arrow} {
				updated, _ := m.Update(key)
				m = updated.(Model)
			}
			if detailStateFromScreen(t, m.detail).session != session || len(m.detailStack) != 0 {
				t.Fatalf("%s navigated from non-foldable timeline row: detail=%T stack=%d", arrow.String(), m.detail, len(m.detailStack))
			}
		})

		t.Run("item/"+arrow.String(), func(t *testing.T) {
			item := newItemView(model.Event{Kind: model.EventUser, Text: "Chart the route"}, model.AgentClaude, nil, 80, 12, newStyles())
			m := NewModel(nil, nil)
			m.screen = screenDetail
			m.detailStack = []detailScreen{newDetailState(&model.Session{ID: "route"}, 80, 12, m.styles)}
			m.detail = item
			updated, _ := m.Update(arrow)
			m = updated.(Model)
			if m.detail.(*itemView).event.Kind != model.EventUser || len(m.detailStack) != 1 {
				t.Fatalf("%s navigated from item: detail=%T stack=%d", arrow.String(), m.detail, len(m.detailStack))
			}
		})

		t.Run("subagents/"+arrow.String(), func(t *testing.T) {
			child := &model.Session{ID: "scout", Agent: model.AgentClaude}
			root := &model.Session{ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child}}
			m := NewModel([]*model.Session{root}, nil)
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyTab}, arrow} {
				updated, _ := m.Update(key)
				m = updated.(Model)
			}
			detail := detailStateFromScreen(t, m.detail)
			if detail.session != root || detail.tab != tabSubagents || detail.subagentSelection != 0 || len(m.detailStack) != 0 {
				t.Fatalf("%s navigated from Subagents row: session=%q tab=%v selection=%d stack=%d", arrow.String(), detail.session.ID, detail.tab, detail.subagentSelection, len(m.detailStack))
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
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'t'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	want := m.styles.title.Render("tab")
	if got := detailStateFromScreen(t, m.detailStack[0]).styles.title.Render("tab"); got != want {
		t.Fatalf("stacked detail retained stale theme: got %q, want %q", got, want)
	}
}

func TestLDrillsIntoSubagent(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude}
	parent := &model.Session{
		ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{parent}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyRunes, Runes: []rune{'l'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if detailStateFromScreen(t, m.detail).session != child {
		t.Fatalf("l left detail session at %q, want child", detailStateFromScreen(t, m.detail).session.ID)
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
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'G'}}} {
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
		for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
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

	if view := ansi.Strip(detail.view()); detail.tab != tabSubagents || !strings.Contains(view, "╭─ Timeline  [Subagents] ") {
		t.Fatalf("tab did not activate the Subagents panel:\n%s", view)
	}
}

func TestFormatTokenFlowKeepsTypeOrderAndFreshInput(t *testing.T) {
	tests := []struct {
		name  string
		usage model.Usage
		want  string
	}{
		{
			name:  "separate Claude cache read",
			usage: model.Usage{InputTokens: 3_000, OutputTokens: 40, CacheCreation5mTokens: 500, CacheCreation1hTokens: 200, CacheReadTokens: 8_000},
			want:  "↑8000/700/3000 ↓40",
		},
		{
			name:  "inclusive Codex cache read",
			usage: model.Usage{InputTokens: 3_000, OutputTokens: 40, CacheReadTokens: 2_000, InputIncludesCacheRead: true},
			want:  "↑2000/0/1000 ↓40",
		},
		{name: "zero groups", want: "↑0/0/0 ↓0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatTokenFlow(test.usage); got != test.want {
				t.Fatalf("formatTokenFlow() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatMillionRateUsesReadableTokenPricing(t *testing.T) {
	tests := []struct {
		rate float64
		want string
	}{
		{rate: 0.000005, want: "$5/Mtok"},
		{rate: 0.0000005, want: "$0.5/Mtok"},
		{rate: 0.00000625, want: "$6.25/Mtok"},
		{rate: 0, want: "$0/Mtok"},
	}
	for _, test := range tests {
		if got := formatMillionRate(test.rate); got != test.want {
			t.Errorf("formatMillionRate(%v) = %q, want %q", test.rate, got, test.want)
		}
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

func TestThirdDetailTabExplainsCostAndRecursiveTree(t *testing.T) {
	child := &model.Session{
		ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge",
		Usage: []model.Usage{{Model: "model-b", InputTokens: 40, OutputTokens: 10}}, ModelCosts: map[string]float64{"model-b": 0.05},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"model-b": {Input: testCostBuckets(40, 0.04), Output: testCostBuckets(10, 0.01)}}, Cost: model.Cost{USD: 0.05},
	}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Title: "Plan route",
		Usage: []model.Usage{{Model: "model-a", InputTokens: 100, OutputTokens: 20, CacheReadTokens: 10}}, ModelCosts: map[string]float64{"model-a": 0.13},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"model-a": {Input: testCostBuckets(100, 0.10), Output: testCostBuckets(20, 0.02), CacheRead: testCostBuckets(10, 0.01)}}, Cost: model.Cost{USD: 0.13}, Subagents: []*model.Session{child},
	}
	detail := newDetailState(root, 100, 30, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	view := ansi.Strip(detail.view())
	for _, want := range []string{
		"Timeline  Subagents (1)  [Info]",
		"total: $0.18",
		"tokens: ↑10/0/140 ↓30",
		"Own model costs · $0.13",
		"model-a",
		"input        100 × $1000/Mtok = $0.10",
		"cache read    10 × $1000/Mtok = $0.01",
		"output        20 × $1000/Mtok = $0.02",
		"subtotal                      = $0.13",
		"Both agents use the same rate table. ~ means the applied rate",
		"published rate.",
		"own: this session's own turns",
		"subagents: delegated child sessions",
		"total = own + Σ subs · ↑10/0/140 ↓30 / $0.18",
		"├─ own · ↑10/0/100 ↓20 / $0.13",
		"└─ subagent Scout ridge · ↑0/0/40 ↓10 / $0.05",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Info tab missing %q:\n%s", want, view)
		}
	}
	// Scout ridge spawns nothing, so it carries no redundant own row of its own.
	if strings.Contains(view, "└─ own") {
		t.Errorf("leaf subagent kept a redundant own row:\n%s", view)
	}
	if strings.Contains(view, "effective") || strings.Contains(view, "/token") {
		t.Fatalf("Info model math retained a blended per-token rate:\n%s", view)
	}
	if detail.selectedLine != -1 || len(detail.focusables) != 0 {
		t.Fatalf("Info tab selection=%d focusables=%d, want plain unfocused panel", detail.selectedLine, len(detail.focusables))
	}
}

func TestInfoTabDisclosesOwnedGrossAndReplayOwners(t *testing.T) {
	session := &model.Session{
		ID: "replay", Agent: model.AgentClaude,
		Usage:               []model.Usage{{Model: "model-a", InputTokens: 100}},
		ModelCosts:          map[string]float64{"model-a": 0.10},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"model-a": {Input: testCostBuckets(100, 0.10)}},
		Cost:                model.Cost{USD: 0.10},
		DuplicatedUSD:       0.04,
		DuplicatedUsage:     model.Usage{InputTokens: 40},
		DuplicatedCount:     2,
		DuplicatedByModel:   map[string]float64{"model-a": 0.04},
		DuplicatedOwners: []model.DuplicateOwner{
			{SessionID: "session-origin", Title: "Original route", USD: 0.03, Count: 1},
			{SessionID: "session-parent", Title: "Parent branch", USD: 0.01, Count: 1},
		},
	}

	text := ""
	for _, line := range sessionInfoLines(session) {
		text += line.text + "\n"
	}
	for _, want := range []string{
		"owned: $0.06",
		"gross: $0.10",
		"replayed total: −$0.04, 2 requests",
		"replayed −$0.03, 1 request, from Original route (session-origin)",
		"replayed −$0.01, 1 request, from Parent branch (session-parent)",
		"Owned model costs · $0.06",
		"gross tokens:",
		"Gross cost tree",
		"gross total = gross own + Σ gross subs",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("duplicate Info tab missing %q:\n%s", want, text)
		}
	}
	modelText := strings.Join(ownModelCostLines(session), "\n")
	if !strings.Contains(modelText, "replayed") || !strings.Contains(modelText, "−$0.04") ||
		!strings.Contains(modelText, "subtotal") || !strings.Contains(modelText, "$0.06") {
		t.Fatalf("owned model breakdown did not subtract replayed cost:\n%s", modelText)
	}
}

func TestInfoModelSubtotalUsesAuthoritativeLoggedCost(t *testing.T) {
	recorded := 0.10
	session := &model.Session{
		Usage:               []model.Usage{{Model: "model-a", InputTokens: 100, CostUSD: &recorded}},
		ModelCosts:          map[string]float64{"model-a": 0.10},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"model-a": {Input: testCostBuckets(100, 0.08)}},
		Cost:                model.Cost{USD: 0.10},
		DuplicatedUSD:       0.04,
		DuplicatedCount:     1,
		DuplicatedByModel:   map[string]float64{"model-a": 0.04},
	}

	text := strings.Join(ownModelCostLines(session), "\n")
	for _, want := range []string{"logged cost", "$0.10", "replayed", "−$0.04", "subtotal", "$0.06"} {
		if !strings.Contains(text, want) {
			t.Errorf("logged-cost model breakdown missing %q:\n%s", want, text)
		}
	}
}

func TestInfoTabWithoutDuplicatesOmitsOwnershipRows(t *testing.T) {
	session := &model.Session{
		Usage:      []model.Usage{{Model: "model-a", InputTokens: 10}},
		ModelCosts: map[string]float64{"model-a": 0.10},
		Cost:       model.Cost{USD: 0.10},
	}
	text := ""
	for _, line := range sessionInfoLines(session) {
		text += line.text + "\n"
	}

	if !strings.Contains(text, "total: $0.10") || !strings.Contains(text, "Own model costs · $0.10") ||
		strings.Contains(text, "owned:") || strings.Contains(text, "gross:") ||
		strings.Contains(text, "replayed") || strings.Contains(text, "Owned model costs") {
		t.Fatalf("non-duplicate Info tab changed ownership disclosure:\n%s", text)
	}
}

func TestDuplicateInfoRowsWrapWithinNarrowWidth(t *testing.T) {
	session := &model.Session{
		ID: "replay", Agent: model.AgentClaude, Cost: model.Cost{USD: 1},
		DuplicatedUSD: 0.25, DuplicatedCount: 1,
		DuplicatedOwners: []model.DuplicateOwner{{
			SessionID: "session-origin", Title: "Earlier origin route", USD: 0.25, Count: 1,
		}},
	}
	detail := newDetailState(session, 40, 30, newStyles())
	detail.tab = tabInfo
	detail.rebuild()
	view := ansi.Strip(detail.view())

	if !strings.Contains(view, "replayed total") || !strings.Contains(view, "replayed −$0.25") ||
		!strings.Contains(view, "Earlier origin route") {
		t.Fatalf("narrow duplicate Info omitted replay disclosure:\n%s", view)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > 40 {
			t.Errorf("narrow duplicate Info line %d width = %d: %q", lineNumber+1, got, line)
		}
	}
}

func TestInfoTokenFlowNormalizesInclusiveInputBeforeSessionAggregation(t *testing.T) {
	child := &model.Session{Usage: []model.Usage{{
		InputTokens: 80, OutputTokens: 3, CacheReadTokens: 50, InputIncludesCacheRead: true,
	}}}
	root := &model.Session{
		Usage:     []model.Usage{{InputTokens: 100, OutputTokens: 2, CacheReadTokens: 20}},
		Subagents: []*model.Session{child},
	}

	lines := sessionCostTree(root)
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"total = own + Σ subs · ↑70/0/130 ↓5",
		"├─ own · ↑20/0/100 ↓2",
		"└─ subagent",
		"↑50/0/30 ↓3",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("mixed-source cost tree missing %q:\n%s", want, text)
		}
	}
}

func TestInfoTabWithoutSubagentsShowsTotalWithoutOwnRow(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Usage: []model.Usage{{Model: "gpt-5", InputTokens: 80, OutputTokens: 20}}, ModelCosts: map[string]float64{"gpt-5": 0.2}, ModelCostBreakdowns: map[string]model.CostBreakdown{"gpt-5": {Input: testCostBuckets(80, 0.1), Output: testCostBuckets(20, 0.1)}}, Cost: model.Cost{USD: 0.2, Estimated: true}}
	detail := newDetailState(session, 80, 28, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	view := ansi.Strip(detail.view())
	for _, want := range []string{"[Info]", "total = own + Σ subs · ↑0/0/80 ↓20 / ~$0.20"} {
		if !strings.Contains(view, want) {
			t.Errorf("leaf Info tab missing %q:\n%s", want, view)
		}
	}
	// Without subagents own equals the total, so the tree stops at the total line.
	if strings.Contains(view, "─ own · ") {
		t.Fatalf("leaf Info tree kept a redundant own row:\n%s", view)
	}
	if strings.Contains(view, "subagent ") {
		t.Fatalf("leaf Info tree invented a subagent:\n%s", view)
	}
}

func TestInfoModelMathHandlesInclusiveCacheAndMissingPricing(t *testing.T) {
	session := &model.Session{
		Agent: model.AgentCodex,
		Usage: []model.Usage{
			{Model: "gpt-5", InputTokens: 100, OutputTokens: 10, CacheReadTokens: 20, InputIncludesCacheRead: true},
			{Model: "unknown-model", InputTokens: 5},
		},
		ModelCosts:          map[string]float64{"gpt-5": 0.11, "unknown-model": 0},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"gpt-5": {Input: testCostBuckets(80, 0.08), Output: testCostBuckets(10, 0.01), CacheRead: testCostBuckets(20, 0.02)}, "unknown-model": {}},
		Cost:                model.Cost{USD: 0.11, Estimated: true, MissingPricingModels: []string{"unknown-model"}},
	}
	text := ""
	for _, line := range sessionInfoLines(session) {
		text += line.text + "\n"
	}
	for _, want := range []string{
		"total: ~$0.11! · missing pricing: unknown-model",
		"tokens: ↑20/0/85 ↓10",
		"gpt-5",
		"input        80 × $1000/Mtok = $0.08",
		"cache read   20 × $1000/Mtok = $0.02",
		"output       10 × $1000/Mtok = $0.01",
		"subtotal                     = $0.11",
		"unknown-model!",
		"input        5 · price unavailable",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Info model math missing %q:\n%s", want, text)
		}
	}
	readAt := strings.Index(text, "cache read   20")
	inputAt := strings.Index(text, "input        80")
	outputAt := strings.Index(text, "output       10")
	if readAt < 0 || inputAt <= readAt || outputAt <= inputAt || strings.Contains(text, "effective") || strings.Contains(text, "/token") {
		t.Fatalf("Info model groups are out of order or retained a blended rate:\n%s", text)
	}
}

func TestInfoModelMathNamesOnlySubstitutedRate(t *testing.T) {
	session := &model.Session{
		Usage: []model.Usage{
			{Model: "model-a", InputTokens: 10},
			{Model: "agents-a1", InputTokens: 20},
		},
		ModelCosts: map[string]float64{"model-a": 0.01, "agents-a1": 0.02},
		ModelCostBreakdowns: map[string]model.CostBreakdown{
			"model-a":   {Input: testCostBuckets(10, 0.01)},
			"agents-a1": {Input: testCostBuckets(20, 0.02)},
		},
		Cost: model.Cost{
			USD: 0.03, Estimated: true,
			EstimatedRates: []model.EstimatedRate{{Model: "agents-a1", PricingModel: "model-a"}},
		},
	}

	text := strings.Join(ownModelCostLines(session), "\n")
	if !strings.Contains(text, "agents-a1 (est. · priced as model-a)") {
		t.Fatalf("substituted model did not name its stand-in:\n%s", text)
	}
	if strings.Contains(text, "model-a (est.") || !strings.Contains(text, "subtotal                     = $0.01") {
		t.Fatalf("exact model inherited session estimate state:\n%s", text)
	}
}

func TestInfoExplainsEstimateMarker(t *testing.T) {
	text := ""
	for _, line := range sessionInfoLines(&model.Session{}) {
		text += line.text + "\n"
	}
	want := "Both agents use the same rate table. ~ means the applied rate is not the logged model's own published rate."
	if !strings.Contains(text, want) {
		t.Fatalf("Info explanation missing %q:\n%s", want, text)
	}
}

func TestInfoModelMathDoesNotTreatRecordedCostAsRatePricing(t *testing.T) {
	session := &model.Session{
		Usage:      []model.Usage{{Model: "unknown-model", InputTokens: 10}},
		ModelCosts: map[string]float64{"unknown-model": 1.23},
		Cost:       model.Cost{USD: 1.23},
	}
	text := ""
	for _, line := range sessionInfoLines(session) {
		text += line.text + "\n"
	}
	if !strings.Contains(text, "input        10 · price unavailable") || strings.Contains(text, "$0/Mtok") || strings.Contains(text, "subtotal =") {
		t.Fatalf("recorded unknown cost was presented as rate pricing:\n%s", text)
	}
}

func TestInfoModelMathJoinsEmptyModelKeysBeforeDisplay(t *testing.T) {
	session := &model.Session{
		Usage:               []model.Usage{{InputTokens: 10}},
		ModelCosts:          map[string]float64{"": 0.02},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"": {}},
		Cost:                model.Cost{USD: 0.02, MissingPricingModels: []string{""}},
	}
	text := ""
	for _, line := range sessionInfoLines(session) {
		text += line.text + "\n"
	}
	if strings.Count(text, "unknown!") != 1 || !strings.Contains(text, "input        10 · price unavailable") || !strings.Contains(text, "missing pricing: unknown") || strings.Contains(text, "subtotal = $0.02") {
		t.Fatalf("empty model key was not joined before display:\n%s", text)
	}
}

func TestInfoTokenFlowRendersWithinNarrowWidths(t *testing.T) {
	session := &model.Session{
		ID: "route", Agent: model.AgentCodex,
		Usage:      []model.Usage{{Model: "gpt-5.6-sol", InputTokens: 48_000_000, OutputTokens: 350_000, CacheReadTokens: 42_000_000, InputIncludesCacheRead: true}},
		ModelCosts: map[string]float64{"gpt-5.6-sol": 60}, ModelCostBreakdowns: map[string]model.CostBreakdown{"gpt-5.6-sol": {Input: testCostBuckets(6_000_000, 30), Output: testCostBuckets(350_000, 10), CacheRead: testCostBuckets(42_000_000, 20)}},
		Cost: model.Cost{USD: 60, Estimated: true},
	}
	for _, width := range []int{40, 80} {
		detail := newDetailState(session, width, 40, newStyles())
		detail.tab = tabInfo
		detail.rebuild()
		view := ansi.Strip(detail.view())
		if !strings.Contains(view, "↑42M/0/6.0M ↓350k") {
			t.Errorf("%d-column Info view missing token flow:\n%s", width, view)
		}
		for lineNumber, line := range strings.Split(view, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("%d-column Info line %d width = %d: %q", width, lineNumber+1, got, line)
			}
		}
	}
}

func TestInfoWordWrappingKeepsRatesAndCurrencyAtomic(t *testing.T) {
	inputAbove272K := 0.000010
	calculator := cost.NewCalculator(cost.Table{"gpt-5.6": {Input: 0.000005, InputAbove272K: &inputAbove272K}})
	usage := model.Usage{Model: "gpt-5.6-sol", InputTokens: 450_000}
	breakdown := calculator.BreakdownCodex(usage, "gpt-5.6")
	calculated := calculator.CalculateCodex(usage, "gpt-5.6")
	session := &model.Session{
		Agent:               model.AgentCodex,
		Usage:               []model.Usage{usage},
		ModelCosts:          map[string]float64{"gpt-5.6-sol": calculated.USD},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"gpt-5.6-sol": breakdown},
		Cost:                calculated,
	}
	currency := formatCost(session.Cost)

	for _, width := range []int{40, 80} {
		detail := newDetailState(session, width, 40, newStyles())
		detail.tab = tabInfo
		detail.rebuild()
		for _, atom := range []string{"272k × $5/Mtok", "178k × $10/Mtok", currency} {
			found := false
			for _, row := range detail.rendered {
				found = found || strings.Contains(ansi.Strip(row.text), atom)
			}
			if !found {
				t.Errorf("%d-column Info wrapping split %q:\n%s", width, atom, ansi.Strip(detail.view()))
			}
		}
	}
}

func TestInfoModelMathAggregatesRealRateBucketsAcrossRecords(t *testing.T) {
	inputAbove272K, cacheRead, cacheReadAbove272K := 0.000010, 0.0000005, 0.000001
	calculator := cost.NewCalculator(cost.Table{"gpt-5.6": {
		Input: 0.000005, InputAbove272K: &inputAbove272K,
		CacheRead: &cacheRead, CacheReadAbove272K: &cacheReadAbove272K,
	}})
	usage := []model.Usage{
		{Model: "gpt-5.6-sol", InputTokens: 1_350_000, CacheReadTokens: 1_000_000, InputIncludesCacheRead: true},
		{Model: "gpt-5.6-sol", InputTokens: 25_100_000, CacheReadTokens: 25_000_000, InputIncludesCacheRead: true},
	}
	var breakdown model.CostBreakdown
	var total model.Cost
	for _, record := range usage {
		breakdown = breakdown.Add(calculator.BreakdownCodex(record, "gpt-5.6"))
		calculated := calculator.CalculateCodex(record, "gpt-5.6")
		total.USD += calculated.USD
		total.Estimated = true
	}
	session := &model.Session{
		Agent: model.AgentCodex, Usage: usage, ModelCosts: map[string]float64{"gpt-5.6-sol": total.USD},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"gpt-5.6-sol": breakdown}, Cost: total,
	}

	text := strings.Join(ownModelCostLines(session), "\n")
	for _, want := range []string{
		"372k × $5/Mtok +  78k × $10/Mtok",
		"544k × $0.5/Mtok +  25M × $1/Mtok",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("real-rate breakdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "$6.") || strings.Contains(text, "$0.9") {
		t.Fatalf("Info model math rendered an effective rate:\n%s", text)
	}
}

func TestInfoModelMathRendersCacheWriteBaseRatesBeforeAboveTierRates(t *testing.T) {
	baseInput, highInput := 0.000002, 0.000003
	baseWrite, highWrite := 0.000006, 0.000007
	calculator := cost.NewCalculator(cost.Table{"model-a": {
		Input: baseInput, InputAbove200K: &highInput,
		CacheWrite: &baseWrite, CacheWriteAbove200K: &highWrite,
	}})
	usage := []model.Usage{
		{Model: "model-a", CacheCreation5mTokens: 250_000},
		{Model: "model-a", CacheCreation1hTokens: 250_000},
	}
	var breakdown model.CostBreakdown
	var total float64
	for _, record := range usage {
		breakdown = breakdown.Add(calculator.Breakdown(record))
		total += calculator.Calculate(record).USD
	}
	session := &model.Session{
		Usage: usage, ModelCosts: map[string]float64{"model-a": total},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"model-a": breakdown},
		Cost:                model.Cost{USD: total},
	}

	text := strings.Join(ownModelCostLines(session), "\n")
	want := "250k × $6/Mtok + 200k × $4/Mtok +  50k × $7/Mtok"
	if !strings.Contains(text, want) {
		t.Fatalf("cache-write rates not rendered base-first; missing %q:\n%s", want, text)
	}
}

func TestInfoTabCyclesAndScrollsWithoutSelection(t *testing.T) {
	usage := make([]model.Usage, 24)
	modelCosts := make(map[string]float64, len(usage))
	for index := range usage {
		name := fmt.Sprintf("model-%02d", index)
		usage[index] = model.Usage{Model: name, InputTokens: int64(index + 1)}
		modelCosts[name] = float64(index+1) / 100
	}
	detail := newDetailState(&model.Session{ID: "route", Usage: usage, ModelCosts: modelCosts, Cost: model.Cost{USD: 3}}, 60, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if detail.viewport.YOffset != 1 || detail.selectedLine != -1 {
		t.Fatalf("Info j state offset=%d selected=%d, want scroll without selection", detail.viewport.YOffset, detail.selectedLine)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	if detail.tab != tabTimeline {
		t.Fatalf("third tab cycle active=%v, want Timeline", detail.tab)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if detail.tab != tabInfo {
		t.Fatalf("reverse tab cycle active=%v, want Info", detail.tab)
	}
}

func TestInfoTabRoundTripPreservesTimelineFocus(t *testing.T) {
	session := &model.Session{ID: "route", Events: []model.Event{
		{Kind: model.EventUser, Text: "Chart the route"},
		{Kind: model.EventAssistantText, Text: "Cross the ridge"},
		{Kind: model.EventUser, Text: "Check the pass"},
	}}
	detail := newDetailState(session, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	wantFocus := detail.focus
	wantKey := detail.focusables[wantFocus].key
	if wantFocus == 0 {
		t.Fatal("focus fixture did not select a non-zero Timeline row")
	}

	detail.update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if detail.tab != tabInfo || detail.selectedLine != -1 {
		t.Fatalf("reverse tab active=%v selected=%d, want unfocused Info", detail.tab, detail.selectedLine)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	if detail.tab != tabTimeline || detail.focus != wantFocus || detail.focusables[detail.focus].key != wantKey {
		t.Fatalf("Timeline round trip tab=%v focus=%d key=%q, want tab=%v focus=%d key=%q", detail.tab, detail.focus, detail.focusables[detail.focus].key, tabTimeline, wantFocus, wantKey)
	}
	for range 3 {
		detail.update(tea.KeyMsg{Type: tea.KeyTab})
	}
	if detail.tab != tabTimeline || detail.focus != wantFocus || detail.focusables[detail.focus].key != wantKey {
		t.Fatalf("forward tab cycle tab=%v focus=%d key=%q, want tab=%v focus=%d key=%q", detail.tab, detail.focus, detail.focusables[detail.focus].key, tabTimeline, wantFocus, wantKey)
	}
}

func TestInfoResizeAndWrapPreserveAValidViewport(t *testing.T) {
	usage := make([]model.Usage, 15)
	modelCosts := make(map[string]float64, len(usage))
	for index := range usage {
		name := fmt.Sprintf("very-long-model-name-%02d-with-extra-detail", index)
		usage[index] = model.Usage{Model: name, InputTokens: int64(index + 1)}
		modelCosts[name] = float64(index+1) / 100
	}
	detail := newDetailState(&model.Session{ID: "route", Usage: usage, ModelCosts: modelCosts, Cost: model.Cost{USD: 4.65}}, 40, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyShiftTab})
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if detail.viewport.YOffset == 0 {
		t.Fatal("Info fixture did not scroll at 40 columns")
	}
	detail.viewport.ScrollUp(5)
	wantAnchor := detail.rendered[detail.viewport.YOffset].detailIndex

	detail.setWrap(false)
	maxOffset := max(0, len(detail.rendered)-detail.viewport.Height)
	if detail.viewport.YOffset < 0 || detail.viewport.YOffset > maxOffset {
		t.Fatalf("unwrapped Info offset=%d, want within 0..%d", detail.viewport.YOffset, maxOffset)
	}
	if got := detail.rendered[detail.viewport.YOffset].detailIndex; got != wantAnchor {
		t.Fatalf("unwrapped Info top detail=%d, want preserved logical line %d", got, wantAnchor)
	}
	detail.resize(100, 80)
	if detail.viewport.YOffset != 0 {
		t.Fatalf("expanded Info offset=%d, want top after all content fits", detail.viewport.YOffset)
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
		cells  []string
	}{
		{prefix: "│› claude Scout ridge", cells: []string{"opus-4.8", "175", "~$1.75"}},
		{prefix: "│  claude └─ Map cavern", cells: []string{"sonnet-4-7", "75", "~$0.75"}},
		{prefix: "│  codex     └─ Verify cavern", cells: []string{"gpt-5.6", "25", "~$0.25"}},
	}
	position := 0
	for _, want := range wants {
		next := strings.Index(view[position:], want.prefix)
		if next < 0 {
			t.Fatalf("Subagents tab missing indented prefix %q:\n%s", want.prefix, view)
		}
		position += next
		end := strings.IndexByte(view[position:], '\n')
		row := view[position : position+max(0, end)]
		if end < 0 {
			t.Fatalf("Subagents row for %q has no line ending:\n%s", want.prefix, view)
		}
		for _, cell := range want.cells {
			if !strings.Contains(row, cell) {
				t.Fatalf("Subagents row %q missing cell %q:\n%s", row, cell, view)
			}
		}
		position += end
	}
}

func TestSubagentsDefaultSortsSiblingsOldestFirstWithinTree(t *testing.T) {
	start := time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC)
	earlyChild := &model.Session{ID: "early-child", StartedAt: start.Add(time.Hour)}
	early := &model.Session{ID: "early", StartedAt: start, Subagents: []*model.Session{earlyChild}}
	lateChild := &model.Session{ID: "late-child", StartedAt: start.Add(3 * time.Hour)}
	laterChild := &model.Session{ID: "later-child", StartedAt: start.Add(4 * time.Hour)}
	late := &model.Session{ID: "late", StartedAt: start.Add(2 * time.Hour), Subagents: []*model.Session{laterChild, lateChild}}
	root := &model.Session{ID: "root", Subagents: []*model.Session{late, early}}

	flattened := flattenSubagents(root, sortState{})
	want := []struct {
		id    string
		depth int
	}{
		{id: "early", depth: 0},
		{id: "early-child", depth: 1},
		{id: "late", depth: 0},
		{id: "late-child", depth: 1},
		{id: "later-child", depth: 1},
	}
	for index, expected := range want {
		if got := flattened[index]; got.s.ID != expected.id || got.depth != expected.depth {
			t.Fatalf("flattened[%d] = %s at depth %d, want %s at depth %d", index, got.s.ID, got.depth, expected.id, expected.depth)
		}
	}
}

func TestSubagentActiveSortPreservesParentChildPreorder(t *testing.T) {
	child := &model.Session{ID: "child", Title: "Aardvark"}
	alpha := &model.Session{ID: "alpha", Title: "Alpha"}
	zulu := &model.Session{ID: "zulu", Title: "Zulu", Subagents: []*model.Session{child}}
	root := &model.Session{ID: "root", Subagents: []*model.Session{zulu, alpha}}

	for _, test := range []struct {
		name string
		desc bool
		want []string
	}{
		{name: "ascending", want: []string{"alpha", "zulu", "child"}},
		{name: "descending", desc: true, want: []string{"zulu", "child", "alpha"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			flattened := flattenSubagents(root, sortState{kind: columnTitle, desc: test.desc, active: true})
			for index, want := range test.want {
				if flattened[index].s.ID != want {
					t.Fatalf("flattened[%d] = %q, want %q", index, flattened[index].s.ID, want)
				}
			}
			for index, item := range flattened {
				if item.s == child && (index == 0 || flattened[index-1].s != zulu || item.depth != 1) {
					t.Fatalf("child at index %d depth %d is not directly under zulu", index, item.depth)
				}
			}
		})
	}
}

func TestSubagentRowsRenderSortedTreeGuides(t *testing.T) {
	deep := &model.Session{ID: "deep", Title: "Deep survey"}
	mid := &model.Session{ID: "mid", Title: "Bravo branch", Subagents: []*model.Session{deep}}
	last := &model.Session{ID: "last", Title: "Zulu branch"}
	alpha := &model.Session{ID: "alpha", Title: "Alpha root", Subagents: []*model.Session{last, mid}}
	omega := &model.Session{ID: "omega", Title: "Omega root"}
	root := &model.Session{ID: "root", Subagents: []*model.Session{omega, alpha}}

	flattened := flattenSubagents(root, sortState{kind: columnTitle, active: true})
	wants := []string{
		"Alpha root",
		"├─ Bravo branch",
		"│  └─ Deep survey",
		"└─ Zulu branch",
		"Omega root",
	}
	columns := []listColumn{{kind: columnTitle, width: 40}}
	for index, want := range wants {
		row := subagentRow(flattened[index], time.Time{}, columns, "", "", "")
		if got := strings.TrimRight(row, " "); got != want {
			t.Errorf("row %d title = %q, want %q", index, got, want)
		}
	}
}

func TestSubagentSortingDoesNotMutateSessionGraph(t *testing.T) {
	firstChild := &model.Session{ID: "first-child", Title: "Zulu"}
	secondChild := &model.Session{ID: "second-child", Title: "Alpha"}
	first := &model.Session{ID: "first", Title: "Zulu", Subagents: []*model.Session{firstChild, secondChild}}
	second := &model.Session{ID: "second", Title: "Alpha"}
	root := &model.Session{ID: "root", Subagents: []*model.Session{first, second}}
	rootBefore := append([]*model.Session(nil), root.Subagents...)
	childBefore := append([]*model.Session(nil), first.Subagents...)

	flattenSubagents(root, sortState{kind: columnTitle, active: true})

	if !slices.Equal(root.Subagents, rootBefore) {
		t.Fatalf("root children mutated from %#v to %#v", rootBefore, root.Subagents)
	}
	if !slices.Equal(first.Subagents, childBefore) {
		t.Fatalf("nested children mutated from %#v to %#v", childBefore, first.Subagents)
	}
}

func TestSubagentSelectionSurvivesResortByIdentity(t *testing.T) {
	first := &model.Session{ID: "first", Title: "Zulu"}
	selected := &model.Session{ID: "selected", Title: "Alpha"}
	detail := newDetailState(&model.Session{ID: "root", Subagents: []*model.Session{first, selected}}, 80, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyDown})

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(sortTitleKey)})

	if detail.subagents[0].s != selected {
		t.Fatalf("title-sorted first subagent = %q, want selected", detail.subagents[0].s.ID)
	}
	if got := detail.focusedSubagent(); got != selected {
		t.Fatalf("focused subagent after sort = %#v, want selected identity", got)
	}
}

func TestSubagentAgeShortcutCyclesBackToStartedOrder(t *testing.T) {
	start := time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC)
	oldUpdate := &model.Session{ID: "old-update", StartedAt: start.Add(time.Hour), UpdatedAt: start}
	newUpdate := &model.Session{ID: "new-update", StartedAt: start, UpdatedAt: start.Add(time.Hour)}
	detail := newDetailState(&model.Session{ID: "root", Subagents: []*model.Session{oldUpdate, newUpdate}}, 100, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	for step, want := range []string{"old-update", "new-update", "new-update"} {
		detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(sortAgeKey)})
		if got := detail.subagents[0].s.ID; got != want {
			t.Fatalf("age shortcut step %d first = %q, want %q", step+1, got, want)
		}
	}
	if detail.subagentSort.active || detail.subagentColumnFocus != columnAge {
		t.Fatalf("age shortcut final state = sort %#v focus %v", detail.subagentSort, detail.subagentColumnFocus)
	}
}

func TestSubagentColumnFocusTracksVisibleColumnsAcrossResize(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "root", Subagents: []*model.Session{{ID: "worker"}}}, 100, 12, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyLeft})
	if detail.subagentColumnFocus != columnAgent {
		t.Fatalf("focus after left boundary = %v, want agent", detail.subagentColumnFocus)
	}
	for range 2 {
		detail.update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if detail.subagentColumnFocus != columnModel {
		t.Fatalf("focus after two right presses = %v, want model", detail.subagentColumnFocus)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(sortColumnKey)})

	detail.resize(35, 12)
	if detail.subagentColumnFocus != columnTitle {
		t.Fatalf("focus after model column dropped = %v, want nearest title", detail.subagentColumnFocus)
	}
	if !detail.subagentSort.active || detail.subagentSort.kind != columnModel {
		t.Fatalf("sort after model column dropped = %#v, want retained model sort", detail.subagentSort)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRight})
	if detail.subagentColumnFocus != columnTokens {
		t.Fatalf("focus after narrow right press = %v, want next visible tokens", detail.subagentColumnFocus)
	}
}

func TestSubagentSortStateSurvivesLiveUpdateAndDrill(t *testing.T) {
	child := &model.Session{ID: "worker", Agent: model.AgentCodex, Path: "/workspace/worker.jsonl", Title: "Before"}
	root := &model.Session{ID: "root", Agent: model.AgentClaude, Path: "/workspace/root.jsonl", Subagents: []*model.Session{child}}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyTab},
		{Type: tea.KeyRight},
		{Type: tea.KeyRunes, Runes: []rune(sortColumnKey)},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	wantSort := detailStateFromScreen(t, m.detail).subagentSort
	wantFocus := detailStateFromScreen(t, m.detail).subagentColumnFocus
	if wantFocus != columnTitle || !wantSort.active || wantSort.kind != columnTitle {
		t.Fatalf("Model-level right/sort state = sort %#v focus %v, want active title", wantSort, wantFocus)
	}

	replacement := cloneSession(root)
	replacement.Subagents[0].Title = "After"
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	refreshed := detailStateFromScreen(t, m.detail)
	if refreshed.subagentSort != wantSort || refreshed.subagentColumnFocus != wantFocus {
		t.Fatalf("live-update state = sort %#v focus %v, want sort %#v focus %v", refreshed.subagentSort, refreshed.subagentColumnFocus, wantSort, wantFocus)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	drilled := detailStateFromScreen(t, m.detail)
	if drilled.subagentSort != wantSort || drilled.subagentColumnFocus != wantFocus {
		t.Fatalf("drilled state = sort %#v focus %v, want inherited sort %#v focus %v", drilled.subagentSort, drilled.subagentColumnFocus, wantSort, wantFocus)
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

func TestSubagentEdgeKeysRestoreViewportEdges(t *testing.T) {
	session := &model.Session{ID: "route"}
	for index := range 12 {
		session.Subagents = append(session.Subagents, &model.Session{ID: fmt.Sprintf("scout-%02d", index), Agent: model.AgentClaude})
	}
	detail := newDetailState(session, 40, 10, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.viewport.SetYOffset(4)

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if detail.subagentSelection != 0 || detail.viewport.YOffset != 0 {
		t.Fatalf("g selection=%d offset=%d, want first row at top", detail.subagentSelection, detail.viewport.YOffset)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	detail.viewport.GotoTop()
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if detail.subagentSelection != len(detail.subagents)-1 || !detail.viewport.AtBottom() {
		t.Fatalf("G selection=%d/%d bottom=%t, want last row at bottom", detail.subagentSelection, len(detail.subagents)-1, detail.viewport.AtBottom())
	}
}

func TestDetailPanelTabsMarkActiveAndShowSubagentCount(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	subagents := make([]*model.Session, 13)
	for index := range subagents {
		subagents[index] = &model.Session{ID: fmt.Sprintf("worker-%02d", index)}
	}
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude, Subagents: subagents}, 80, 12, styleSet)

	timeline := detail.tabLabel()
	if timeline.plain != "[Timeline]  Subagents (13)  Info" || !strings.HasPrefix(timeline.styled, styleSet.title.Render("[Timeline]")) || !strings.Contains(timeline.styled, styleSet.muted.Render("Subagents (13)")) || !strings.Contains(timeline.styled, styleSet.muted.Render("Info")) {
		t.Fatalf("Timeline tab label = %#v", timeline)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	subagentTab := detail.tabLabel()
	if subagentTab.plain != "Timeline  [Subagents (13)]  Info" || !strings.HasPrefix(subagentTab.styled, styleSet.muted.Render("Timeline")) || !strings.Contains(subagentTab.styled, styleSet.title.Render("[Subagents (13)]")) {
		t.Fatalf("Subagents tab label = %#v, want active marker in fixed order", subagentTab)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	infoTab := detail.tabLabel()
	if infoTab.plain != "Timeline  Subagents (13)  [Info]" || !strings.Contains(infoTab.styled, styleSet.title.Render("[Info]")) {
		t.Fatalf("Info tab label = %#v, want third active marker", infoTab)
	}

	empty := newDetailState(&model.Session{ID: "empty"}, 80, 12, styleSet).tabLabel()
	if empty.plain != "[Timeline]  Subagents  Info" || strings.Contains(empty.plain, "(") {
		t.Fatalf("zero-subagent tab label = %#v, want no count", empty)
	}
	detail.resize(20, 12)
	if label := detail.tabPanelLabel(); ansi.StringWidth(label.plain) > detail.width-5 {
		t.Fatalf("narrow tab label width = %d, want <= %d: %#v", ansi.StringWidth(label.plain), detail.width-5, label)
	}
	for _, width := range []int{7, 8, 10, 15} {
		detail.resize(width, 12)
		label := detail.tabPanelLabel().plain
		if ansi.StringWidth(label) > max(1, width-5) || !strings.HasPrefix(label, "[") || !strings.HasSuffix(label, "]") {
			t.Errorf("%d-column active label = %q, want bounded paired marker", width, label)
		}
	}
}

func TestCompactDetailTitleShowsActiveAndDimInactiveTab(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	detail := newDetailState(&model.Session{ID: "route", Agent: model.AgentClaude}, 80, 8, styleSet)

	title := strings.Split(detail.view(), "\n")[0]
	if !strings.Contains(ansi.Strip(title), "[Timeline]  Subagents") || !strings.Contains(title, styleSet.muted.Render("Subagents")) {
		t.Fatalf("compact title did not show a dim inactive tab: %q", title)
	}
}

func TestSubagentsRowAppliesCellStylesAfterFitting(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	first := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge"}
	now := time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)
	selected := &model.Session{
		ID: "map", Agent: model.AgentCodex, Title: "Map cavern", Models: []string{"gpt-5.6-sol"},
		UpdatedAt: now.Add(-2 * time.Minute),
		Usage:     []model.Usage{{InputTokens: 2_500}}, Cost: model.Cost{USD: 0.75, Estimated: true},
	}
	styleSet := newStyles(themes["default"])
	detail := newDetailState(&model.Session{ID: "route", Subagents: []*model.Session{first, selected}}, 100, 14, styleSet)
	detail.now = now
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	line := detail.lines[2]
	styled := detail.styleLine(detail.rendered[detail.renderedStarts[2]].text, line, false, true)

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

func TestSubagentsRowsShowAgeAndDropItBeforeUsage(t *testing.T) {
	now := time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)
	child := &model.Session{
		ID: "review", Agent: model.AgentCodex, Title: "Review lunar telemetry", Models: []string{"gpt-5.6-sol"}, UpdatedAt: now.Add(-12 * time.Minute),
		Usage: []model.Usage{{InputTokens: 2_500}}, Cost: model.Cost{USD: 0.75, Estimated: true},
	}
	m := newModelWithClock([]*model.Session{{ID: "route", Subagents: []*model.Session{child}}}, nil, func() time.Time { return now })
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 100, Height: 14}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyTab}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	detail := detailStateFromScreen(t, m.detail)
	if text := detail.lines[1].text; !strings.Contains(text, "12m") {
		t.Fatalf("wide subagent row missing age: %q", text)
	}

	detail.resize(42, 14)
	text := detail.lines[1].text
	if strings.Contains(text, "12m") || !strings.Contains(text, humanTokens(child.TotalUsage().TotalTokens())) || !strings.Contains(text, formatCost(child.TotalCost())) {
		t.Fatalf("narrow subagent row priority = %q", text)
	}
	for _, row := range detail.rendered {
		if width := ansi.StringWidth(row.text); width != detail.viewport.Width {
			t.Fatalf("rendered subagent row width = %d, want %d: %q", width, detail.viewport.Width, row.text)
		}
	}
}

func TestSubagentColumnsStayAlignedAtNarrowWidths(t *testing.T) {
	child := &model.Session{ID: "map", Agent: model.AgentCodex, Title: "Map fictional cavern"}
	root := &model.Session{ID: "scout", Subagents: []*model.Session{{
		ID: "branch", Subagents: []*model.Session{child},
	}}}
	item := flattenSubagents(root, sortState{})[1]

	tests := []struct {
		width int
		want  []listColumn
	}{
		{width: 28, want: []listColumn{
			{kind: columnAgent, title: "AGENT", width: listAgentWidth},
			{kind: columnTitle, title: "TITLE", width: 6},
			{kind: columnTokens, title: "TOKENS", width: 6, right: true},
			{kind: columnCost, title: "COST", width: listCostWidth, right: true},
		}},
		{width: 40, want: []listColumn{
			{kind: columnAgent, title: "AGENT", width: listAgentWidth},
			{kind: columnTitle, title: "TITLE", width: 4},
			{kind: columnModel, title: "MODEL", width: listModelWidth},
			{kind: columnTokens, title: "TOKENS", width: 6, right: true},
			{kind: columnCost, title: "COST", width: listCostWidth, right: true},
		}},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.width), func(t *testing.T) {
			columns := subagentColumns(test.width)
			if !slices.Equal(columns, test.want) {
				t.Fatalf("columns = %#v, want %#v", columns, test.want)
			}
			header := subagentHeader(columns, sortState{}, listColumnKind(-1), newStyles()).plain
			row := subagentRow(item, time.Time{}, columns, "gpt-5.6", "2500", "~$0.75")
			if got := ansi.StringWidth(header); got != test.width {
				t.Errorf("header width = %d, want %d: %q", got, test.width, header)
			}
			if got := ansi.StringWidth(row); got != test.width {
				t.Errorf("row width = %d, want %d: %q", got, test.width, row)
			}

			offset := 0
			for _, column := range columns {
				headerCell := ansi.Cut(header, offset, offset+column.width)
				rowCell := ansi.Cut(row, offset, offset+column.width)
				if ansi.StringWidth(headerCell) != column.width || ansi.StringWidth(rowCell) != column.width {
					t.Errorf("%d-column %v cell widths = header %d row %d, want %d", test.width, column.kind, ansi.StringWidth(headerCell), ansi.StringWidth(rowCell), column.width)
				}
				switch column.kind {
				case columnAgent:
					if got := strings.TrimSpace(rowCell); got != "codex" {
						t.Errorf("%d-column AGENT cell = %q, want codex", test.width, rowCell)
					}
				case columnTitle:
					if !strings.HasPrefix(rowCell, "└─ ") {
						t.Errorf("%d-column TITLE cell = %q, want child connector", test.width, rowCell)
					}
				}
				offset += column.width + 1
			}
		})
	}
}

func TestSubagentsRendersModelessWorkflowGroup(t *testing.T) {
	group := &model.Session{
		ID: "wf-expedition", Agent: model.AgentClaude, Title: "Coastal expedition", Group: true,
		Subagents: []*model.Session{{ID: "observer", Agent: model.AgentClaude, Title: "Observe the inlet"}},
	}
	detail := newDetailState(&model.Session{ID: "route", Subagents: []*model.Session{group}}, 100, 14, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	if len(detail.lines) < 2 || !strings.Contains(detail.lines[1].text, "—") {
		t.Fatalf("modeless workflow row = %#v, want model placeholder", detail.lines)
	}
}

func TestSubagentsHeaderNamesAndAlignsColumns(t *testing.T) {
	now := time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)
	child := &model.Session{
		ID: "map", Agent: model.AgentCodex, Title: "Map fictional cavern", Models: []string{"gpt-5.6-sol"}, UpdatedAt: now.Add(-12 * time.Minute),
		Usage: []model.Usage{{InputTokens: 2_500}}, Cost: model.Cost{USD: 0.75, Estimated: true},
	}
	detail := newDetailState(&model.Session{ID: "route", Subagents: []*model.Session{child}}, 100, 14, newStyles())
	detail.now = now
	detail.update(tea.KeyMsg{Type: tea.KeyTab})

	if len(detail.lines) < 2 {
		t.Fatalf("Subagents lines = %#v, want header and data row", detail.lines)
	}
	header, row := detail.lines[0].text, detail.lines[1].text
	columns := []struct {
		title string
		value string
		right bool
	}{
		{title: "AGENT", value: "codex"},
		{title: "TITLE", value: "Map fictional cavern"},
		{title: "MODEL", value: "gpt-5.6"},
		{title: "TOKENS", value: "2500", right: true},
		{title: "COST", value: "~$0.75", right: true},
		{title: "AGE", value: "12m", right: true},
	}
	previous := -1
	for _, column := range columns {
		headerStart := strings.LastIndex(header, column.title)
		valueStart := strings.Index(row, column.value)
		if headerStart <= previous || valueStart < 0 {
			t.Fatalf("column %s missing or out of order in header %q and row %q", column.title, header, row)
		}
		if (!column.right && valueStart != headerStart) || (column.right && valueStart+len(column.value) != headerStart+len(column.title)) {
			t.Errorf("column %s misaligned: header %q row %q", column.title, header, row)
		}
		previous = headerStart
	}
}

func TestDeepSubagentRowKeepsAgentIdentity(t *testing.T) {
	deep := &model.Session{ID: "depth-0", Agent: model.AgentCodex, Title: "Inspect fictional depth"}
	root := &model.Session{ID: "root", Subagents: []*model.Session{deep}}
	for depth := 1; depth < 12; depth++ {
		child := &model.Session{ID: fmt.Sprintf("depth-%d", depth), Agent: model.AgentCodex, Title: "Inspect fictional depth"}
		deep.Subagents = []*model.Session{child}
		deep = child
	}
	deep.Subagents = []*model.Session{
		{ID: "deep-mid", Agent: model.AgentCodex, Title: "Inspect fictional mid depth"},
		{ID: "deep-last", Agent: model.AgentCodex, Title: "Inspect fictional last depth"},
	}
	items := flattenSubagents(root, sortState{})
	columns := subagentColumns(96)
	titleStart := columns[0].width + 1
	depthElevenRow := subagentRow(
		items[11],
		time.Time{}, columns, "gpt-5.6", "2500", "~$0.75",
	)
	depthElevenTitle := ansi.Cut(depthElevenRow, titleStart, titleStart+columns[1].width)
	if wantPrefix := strings.Repeat(" ", 30) + "└─ "; !strings.HasPrefix(depthElevenTitle, wantPrefix) {
		t.Errorf("depth-11 title cell = %q, want full prefix %q", depthElevenTitle, wantPrefix)
	}
	for index, wantPrefix := range []string{"…├─ ", "…└─ "} {
		row := subagentRow(
			items[12+index],
			time.Time{}, columns, "gpt-5.6", "2500", "~$0.75",
		)
		agentCell := ansi.Cut(row, 0, columns[0].width)
		if got := strings.TrimSpace(agentCell); got != "codex" {
			t.Errorf("deep agent cell = %q, want unindented agent identity", agentCell)
		}
		titleCell := ansi.Cut(row, titleStart, titleStart+columns[1].width)
		if !strings.HasPrefix(titleCell, wantPrefix) {
			t.Errorf("deep title cell = %q, want elided prefix %q", titleCell, wantPrefix)
		}
	}
}

func TestAssistantLineColorsOnlyTheAgentLabel(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	detail := &detailState{styles: styleSet}
	plain := "  codex: ordinary prose"

	got := detail.styleLine(plain, detailLine{label: "codex:", role: detailAssistant, agent: model.AgentCodex}, false, true)
	want := styleSet.row.Render("  ") + styleSet.codex.Render("codex:") + styleSet.row.Render(" ordinary prose")
	if got != want {
		t.Fatalf("assistant line styling = %q, want only the label agent-colored as %q", got, want)
	}
}

func TestWrappedAssistantProseDoesNotColorLabelTextOnContinuation(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	// Long enough to fold, so the reply text renders as wrapping body rows below the
	// nowrap head. A run of filler wider than the body pushes the embedded "codex:"
	// intact onto a wrapped continuation, where it must stay neutral prose.
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{{
		Kind: model.EventAssistantText, Text: strings.Repeat("x", 80) + " codex: still ordinary prose past the preview cap",
	}}}
	detail := newDetailState(session, 80, 16, styleSet)
	found := false
	for rowIndex, row := range detail.rendered {
		if rowIndex == detail.firstRenderedRow(row.detailIndex) || !strings.Contains(row.text, "codex:") {
			continue
		}
		found = true
		gutterWidth := detail.timelineGutterWidth()
		want := styleSet.row.Render(ansi.Cut(row.text, 0, 2)) + styleSet.muted.Render(ansi.Cut(row.text, 2, 2+gutterWidth)) + styleSet.row.Render(ansi.Cut(row.text, 2+gutterWidth, ansi.StringWidth(row.text)))
		if got := detail.styleLine(row.text, detail.lines[row.detailIndex], false, row.first); got != want {
			t.Fatalf("wrapped assistant prose styling = %q, want neutral continuation %q", got, want)
		}
	}
	if !found {
		t.Fatalf("fixture did not place agent label text on a wrapped continuation: %#v", detail.rendered)
	}
}

func TestUserPromptColorsOnlyTheLabelOverTheFullRowTint(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	detail := newDetailState(&model.Session{ID: "lunar", Events: []model.Event{{Kind: model.EventUser, Text: "ordinary prose"}}}, 40, 12, styleSet)
	line := detail.lines[0]
	plain := detail.rendered[0].text
	label := "you:"
	gutterWidth := detail.timelineGutterWidth()
	body := ansi.Cut(plain, 2+gutterWidth, ansi.StringWidth(plain))
	start := strings.Index(body, label)
	base := lipgloss.NewStyle().Foreground(lipgloss.Color("#ABB2BF")).Background(lipgloss.Color("#262B33"))
	labelStyle := base.Foreground(lipgloss.Color("#98C379")).Bold(true)
	want := styleSet.row.Render(ansi.Cut(plain, 0, 2)) + styleSet.muted.Render(ansi.Cut(plain, 2, 2+gutterWidth)) + base.Render(body[:start]) + labelStyle.Render(label) + base.Render(body[start+len(label):])

	if got := detail.styleLine(plain, line, false, true); got != want {
		t.Fatalf("user prompt styling = %q, want neutral prose and a full-row tint as %q", got, want)
	}
}

func TestRoleLabelsCarryDistinctIdentityColors(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	detail := &detailState{styles: newStyles(themes["default"])}

	roles := map[string]detailLine{
		"you":     {role: detailUserPrompt},
		"claude":  {role: detailAssistant, agent: model.AgentClaude},
		"harness": {role: detailSystemPrompt},
		"tool":    {role: detailTool},
	}
	seen := make(map[string]string, len(roles))
	for name, line := range roles {
		color := fmt.Sprint(detail.labelStyle(line).GetForeground())
		if prior, clash := seen[color]; clash {
			t.Fatalf("%s and %s share label color %s; roles must be distinguishable", prior, name, color)
		}
		seen[color] = name
	}
}

func TestUserPromptLabelsHarnessAndHumanTurns(t *testing.T) {
	detail := &detailState{}
	for _, test := range []struct {
		name      string
		event     model.Event
		wantLabel string
		wantRole  detailRole
	}{
		{name: "harness", event: model.Event{Kind: model.EventUser, Text: "Injected instructions", Harness: true}, wantLabel: "harness:", wantRole: detailSystemPrompt},
		{name: "human", event: model.Event{Kind: model.EventUser, Text: "Survey the crater"}, wantLabel: "you:", wantRole: detailUserPrompt},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := detail.userPromptLines(test.event, 0, "prompt", 0)
			if len(lines) != 1 || lines[0].label != test.wantLabel || lines[0].role != test.wantRole {
				t.Fatalf("userPromptLines() = %#v, want one %q line with role %v", lines, test.wantLabel, test.wantRole)
			}
			if !strings.Contains(lines[0].text, test.wantLabel+" "+test.event.Text) {
				t.Errorf("prompt text = %q, want labelled preview", lines[0].text)
			}
		})
	}
}

func TestUserPromptFoldRevealsTheFullPrompt(t *testing.T) {
	prompt := "First instruction line\nSecond instruction line\nThird instruction line"
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: prompt}}}
	detail := newDetailState(session, 80, 14, newStyles())

	if !detail.lines[0].expandable {
		t.Fatalf("multi-line user prompt expandable = false, want true")
	}
	joined := func() string { return strings.Join(timelineLineTexts(detail.lines), "\n") }
	assert := func(marker string, wantFull bool) {
		t.Helper()
		if header := strings.TrimLeft(detail.lines[0].text, " "); !strings.HasPrefix(header, marker) {
			t.Fatalf("prompt header = %q, want leading %q", header, marker)
		}
		if strings.Contains(joined(), "Third instruction line") != wantFull {
			t.Fatalf("full prompt visible = %t, want %t:\n%s", !wantFull, wantFull, joined())
		}
	}

	assert(glyphExpanded, true) // rows expand by default, so the whole prompt shows

	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	assert(glyphCollapsed, false)
	if !strings.Contains(detail.lines[0].text, "First instruction line") {
		t.Fatalf("collapsed prompt = %q, want first-line summary", detail.lines[0].text)
	}

	detail.update(tea.KeyMsg{Type: tea.KeySpace})
	assert(glyphExpanded, true)
}

// TestTimelineFoldGlyphMatchesExpandableRows guards the invariant that broke user
// prompts: a focusable timeline row shows the ▸/▾ affordance if and only if it is
// actually wired to expand. Any future row that prints the glyph without setting
// expandable (or the reverse) fails here.
func TestTimelineFoldGlyphMatchesExpandableRows(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/lunar/session.jsonl", Events: []model.Event{
		{Kind: model.EventUser, Text: "Single line prompt"},
		{Kind: model.EventUser, Text: "Multi\nline\nprompt"},
		{Kind: model.EventThinking, Text: "Consider the route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/lunar/map.go", Detail: &model.ToolDetail{Output: "map ready"}},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/lunar/plain.go"},
		{Kind: model.EventAssistantText, Text: "Route ready"},
	}}
	detail := newDetailState(session, 80, 40, newStyles())

	for _, line := range detail.lines {
		if line.key == "" {
			continue // passive body/summary rows are not focusable
		}
		trimmed := strings.TrimLeft(line.text, " ")
		hasGlyph := strings.HasPrefix(trimmed, glyphCollapsed) || strings.HasPrefix(trimmed, glyphExpanded)
		if hasGlyph != line.expandable {
			t.Errorf("row %q shows fold glyph = %t, but expandable = %t", line.text, hasGlyph, line.expandable)
		}
	}
}

func timelineLineTexts(lines []detailLine) []string {
	texts := make([]string, len(lines))
	for index, line := range lines {
		texts[index] = line.text
	}
	return texts
}

// TestTimelineIsOneFlatChronologicalList pins the shape of the timeline: one row
// per event in log order, no aggregate row above them, and every row carrying its
// own request's figures. The prompt reports only the window the request it
// triggered was sent with, since a user message bills nothing itself.
func TestTimelineIsOneFlatChronologicalList(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge", Models: []string{"claude-opus-4-8"},
		Usage: []model.Usage{{InputTokens: 40_000, OutputTokens: 5_000}}, Cost: model.Cost{USD: 0.32}}
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/lunar/session.jsonl", Subagents: []*model.Session{child}, Events: []model.Event{
		{Kind: model.EventUser, Text: "Chart the route"},
		{Kind: model.EventThinking, Text: "Compare routes"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/lunar/map.go", Priced: true,
			Usage: &model.Usage{InputTokens: 5_000, OutputTokens: 1_000, CacheReadTokens: 20_000},
			Cost:  model.CostBreakdown{Output: model.CostBuckets{{Tokens: 1_000, RatePerToken: 0.00002}}}},
		{Kind: model.EventSubagent, ToolName: "Task", ToolInput: "Scout ridge", Subagent: child},
		{Kind: model.EventAssistantText, Text: "Route ready",
			Usage: &model.Usage{InputTokens: 3_000, OutputTokens: 4_000, CacheReadTokens: 37_000}},
	}}
	detail := newDetailState(session, 110, 40, newStyles())

	type row struct{ text, metrics string }
	var got []row
	for _, line := range detail.lines {
		got = append(got, row{strings.TrimSpace(line.text), line.metrics})
	}
	// Every event is a sibling at one indent, and each row states its own request:
	// the tool ending at 26k of context, the reply ending at 44k. The subagent
	// keeps its own session totals, which the parent log never bills.
	want := []row{
		{"you: Chart the route", "ctx 25k"},
		{"◇ thinking: Compare routes", ""},
		{"⚙ Read(/workspace/lunar/map.go)", "↑20k/0/5000 ↓1000 · $0.02 · ctx 26k"},
		{"⑃ Task(Scout ridge) opus-4.8", "45k · $0.32"},
		{"claude: Route ready", "↑37k/0/3000 ↓4000 · ctx 44k"},
	}
	if len(got) != len(want) {
		t.Fatalf("timeline rows = %d, want %d:\n%s", len(got), len(want), strings.Join(timelineLineTexts(detail.lines), "\n"))
	}
	for index, line := range got {
		if line != want[index] {
			t.Errorf("row %d = %+v, want %+v", index, line, want[index])
		}
	}
}

func TestTimelineLabelsWorkflowAndTaskSubagents(t *testing.T) {
	direct := &model.Session{ID: "direct-scout", Agent: model.AgentClaude, Title: "Inspect shoreline"}
	group := &model.Session{ID: "wf-river-run", Agent: model.AgentClaude, Title: "River survey", Group: true}
	session := &model.Session{ID: "session-workflow", Agent: model.AgentClaude, Subagents: []*model.Session{direct, group}, Events: []model.Event{
		{Kind: model.EventSubagent, ToolName: "Agent", ToolInput: "Inspect shoreline", Subagent: direct},
		{Kind: model.EventSubagent, ToolName: "Workflow", ToolInput: `{"workflow":"unresolved-input"}`, Subagent: group},
	}}
	detail := newDetailState(session, 100, 20, newStyles())
	text := strings.Join(timelineLineTexts(detail.lines), "\n")
	if !strings.Contains(text, "Task(Inspect shoreline)") {
		t.Fatalf("Task row changed:\n%s", text)
	}
	if !strings.Contains(text, "Workflow(River survey)") || strings.Contains(text, "Task({\"workflow\"") {
		t.Fatalf("Workflow row did not use its type and linked title:\n%s", text)
	}
}

func TestWorkflowGroupCostTreeOmitsOwnAndRollsUpChildren(t *testing.T) {
	first := &model.Session{ID: "mapper", Agent: model.AgentClaude, Usage: []model.Usage{{InputTokens: 40, OutputTokens: 10}}, Cost: model.Cost{USD: 0.10}}
	second := &model.Session{ID: "reviewer", Agent: model.AgentClaude, Usage: []model.Usage{{InputTokens: 20, OutputTokens: 5}}, Cost: model.Cost{USD: 0.20}}
	group := &model.Session{ID: "wf-river-run", Agent: model.AgentClaude, Group: true, Subagents: []*model.Session{first, second}}
	if got := group.TotalUsage(); got.InputTokens != 60 || got.OutputTokens != 15 {
		t.Fatalf("group usage = %#v, want child sum", got)
	}
	if got := group.TotalCost().USD; math.Abs(got-0.30) > 1e-12 {
		t.Fatalf("group cost = %v, want child sum 0.30", got)
	}
	text := strings.Join(sessionCostTree(group), "\n")
	if strings.Contains(text, "own ·") {
		t.Fatalf("workflow group rendered a synthetic own row:\n%s", text)
	}
	for _, want := range []string{"↑0/0/60 ↓15 / $0.30", "subagent mapper", "subagent reviewer"} {
		if !strings.Contains(text, want) {
			t.Errorf("workflow group cost tree missing %q:\n%s", want, text)
		}
	}
}

func TestTimelineKeepsGrossEventCostWhenHeadlineIsOwned(t *testing.T) {
	session := &model.Session{
		ID: "replay", Agent: model.AgentClaude,
		Cost: model.Cost{USD: 0.10}, DuplicatedUSD: 0.04,
		Events: []model.Event{{
			Kind: model.EventAssistantText, Text: "Route ready", Priced: true,
			Usage: &model.Usage{OutputTokens: 10},
			Cost:  model.CostBreakdown{Output: model.CostBuckets{{Tokens: 10, RatePerToken: 0.01}}},
		}},
	}

	detail := newDetailState(session, 100, 20, newStyles())

	if len(detail.lines) != 1 || !strings.Contains(detail.lines[0].metrics, "$0.10") {
		t.Fatalf("timeline metrics = %#v, want gross event cost", detail.lines)
	}
	if headline := detail.headerPanelLines()[2].plain; !strings.Contains(headline, "total $0.06") {
		t.Fatalf("detail headline = %q, want owned cost", headline)
	}
}

func TestHarnessPromptKeepsNextRequestContext(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Injected instructions", Harness: true},
		{Kind: model.EventAssistantText, Text: "Ready", Usage: &model.Usage{InputTokens: 2_000, CacheReadTokens: 40_000}},
	}}
	detail := newDetailState(session, 80, 20, newStyles())

	if line := detail.lines[0]; line.event.Kind != model.EventUser || line.metrics != "ctx 42k" {
		t.Fatalf("harness prompt = %#v, want EventUser with next request context", line)
	}
}

// TestContextColumnNeverShrinksDownAnExpandedTimeline pins how a sequential
// expanded timeline reads: each prompt reports the next request's starting
// context, followed by each request's post-output total.
func TestContextColumnNeverShrinksDownAnExpandedTimeline(t *testing.T) {
	request := func(kind model.EventKind, context int64) model.Event {
		return model.Event{Kind: kind, ToolName: "Read", Text: "Route ready",
			Usage: &model.Usage{InputTokens: 1_000, OutputTokens: 1_000, CacheReadTokens: context - 1_000}}
	}
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/lunar/session.jsonl", Events: []model.Event{
		{Kind: model.EventUser, Text: "Chart the route"},
		request(model.EventToolCall, 10_000),
		request(model.EventAssistantText, 20_000),
		{Kind: model.EventUser, Text: "Now plot the descent"},
		request(model.EventToolCall, 30_000),
		request(model.EventAssistantText, 40_000),
	}}
	detail := newDetailState(session, 120, 40, newStyles())

	var got []string
	for _, line := range detail.lines {
		_, context, found := strings.Cut(line.metrics, "ctx ")
		if !found {
			continue
		}
		got = append(got, context)
	}
	// Each prompt borrows the starting context of the request it triggered, then
	// each request reports its post-output total.
	want := []string{"10k", "11k", "21k", "30k", "31k", "41k"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("context column = %v, want %v:\n%s", got, want, strings.Join(timelineLineTexts(detail.lines), "\n"))
	}
}

// TestEventRowsShowTheirOwnRequestMetrics verifies each request's usage renders on
// the event that carries it — a tool call and an assistant reply here — with the
// context it reached, so an expanded turn shows per-request tokens, cost, and
// context, not only the turn aggregate.
func TestEventRowsShowTheirOwnRequestMetrics(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/lunar/session.jsonl", Events: []model.Event{
		{Kind: model.EventUser, Text: "Chart the route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/lunar/map.go",
			Usage: &model.Usage{InputTokens: 2, OutputTokens: 400, CacheReadTokens: 60_000}},
		{Kind: model.EventAssistantText, Text: "Route ready",
			Usage: &model.Usage{InputTokens: 2, OutputTokens: 3_134, CacheReadTokens: 70_000}},
	}}
	detail := newDetailState(session, 120, 40, newStyles())

	var tool, reply detailLine
	for _, line := range detail.lines {
		switch {
		case strings.Contains(line.text, "Read("):
			tool = line
		case strings.HasPrefix(strings.TrimSpace(line.text), "claude:"):
			reply = line
		}
	}
	// Each request's metrics ride its row's right-aligned column. The tool call ends
	// at 60k of context after rounding; the reply ends at 73k.
	for _, want := range []string{"↓400", "ctx 60k"} {
		if !strings.Contains(tool.metrics, want) {
			t.Fatalf("tool metrics = %q, want %q", tool.metrics, want)
		}
	}
	for _, want := range []string{"↓3134", "ctx 73k"} {
		if !strings.Contains(reply.metrics, want) {
			t.Fatalf("reply metrics = %q, want %q", reply.metrics, want)
		}
	}
}

// TestTurnSummaryOmitsTokensWithoutUsage keeps the affordance honest: a turn whose
// events carry no usage shows neither figure, so the row is unchanged when a log
// lacks token counts.
func TestTurnSummaryOmitsTokensWithoutUsage(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Chart the route"},
		{Kind: model.EventAssistantText, Text: "Route ready"},
	}}
	detail := newDetailState(session, 80, 40, newStyles())

	for _, line := range detail.lines {
		if strings.Contains(line.text, "claude: Route ready") && (strings.Contains(line.text, "ctx ") || strings.Contains(line.text, "+")) {
			t.Fatalf("turn summary invented token figures without usage: %q", line.text)
		}
	}
}

func TestNextRequestContextSkipsAggregateUsage(t *testing.T) {
	usage := model.Usage{InputTokens: 30}
	var aggregate model.Event
	if err := json.Unmarshal([]byte(`{"Kind":"usage","UsageAggregate":true}`), &aggregate); err != nil {
		t.Fatal(err)
	}
	aggregate.Usage = &usage
	events := []model.Event{
		{Kind: model.EventUser, Text: "Chart the route"},
		{Kind: model.EventAssistantText, Text: "Route ready"},
		aggregate,
	}

	if got := nextRequestContext(events, 0); got != 0 {
		t.Fatalf("nextRequestContext() = %d, want no context from session aggregate", got)
	}
}

func TestSystemAndCompactRowsUseTheSystemPromptTint(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	detail := &detailState{styles: styleSet}
	wantStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ABB2BF")).Background(lipgloss.Color("#2B2A26"))

	for _, kind := range []model.EventKind{model.EventSystem, model.EventCompact} {
		line := detail.eventLines(&model.Session{}, model.Event{Kind: kind, Text: "runtime notice"}, 0, "event")[0]
		plain := fitPlain(line.text, 36, false)
		if got, want := detail.styleLine(plain, line, false, true), wantStyle.Render(plain); got != want {
			t.Errorf("%s row styling = %q, want system tint %q", kind, got, want)
		}
	}
}

func TestUsageRowShowsStandardMetricsWithSystemPromptRole(t *testing.T) {
	usage := model.Usage{
		InputTokens:            100,
		OutputTokens:           20,
		CacheReadTokens:        40,
		InputIncludesCacheRead: true,
	}
	event := model.Event{
		Kind:          model.EventUsage,
		Text:          "unattributed usage",
		Model:         "gpt-5.6-sol",
		Usage:         &usage,
		Cost:          model.CostBreakdown{Input: model.CostBuckets{{RatePerToken: 0.001, Tokens: 10}}},
		Priced:        true,
		CostEstimated: true,
	}
	lines := (&detailState{}).eventLines(&model.Session{}, event, 0, "event")
	if len(lines) != 1 || lines[0].role != detailSystemPrompt ||
		!strings.Contains(lines[0].text, "unattributed usage") ||
		!strings.Contains(lines[0].text, "gpt-5.6") ||
		!strings.Contains(lines[0].metrics, "↓20") ||
		!strings.Contains(lines[0].metrics, "ctx 120") ||
		!strings.Contains(lines[0].metrics, "~$0.01") {
		t.Fatalf("usage row = %#v, want system row with request metrics", lines)
	}
}

func TestAggregateUsageRowOmitsRequestContext(t *testing.T) {
	usage := model.Usage{InputTokens: 30}
	event := model.Event{Kind: model.EventUsage, Usage: &usage, UsageAggregate: true}

	if metrics := metricsText(eventMetricParts(event)); strings.Contains(metrics, "ctx ") {
		t.Fatalf("aggregate usage metrics = %q, want no per-request context", metrics)
	}
}

func TestCompactRowShowsTriggerAndContext(t *testing.T) {
	detail := &detailState{}
	for _, test := range []struct {
		name    string
		trigger string
		post    int64
		want    string
	}{
		{name: "manual", trigger: "manual", post: 20000, want: "Session manually compacted · ctx 20k"},
		{name: "auto", trigger: "auto", post: 8389, want: "Session automatically compacted · ctx 8389"},
		{name: "unknown trigger, no context", trigger: "", post: 0, want: "Session compacted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := model.Event{Kind: model.EventCompact, CompactTrigger: test.trigger, CompactPostTokens: test.post}
			line := detail.eventLines(&model.Session{}, event, 0, "event")[0]
			if got := ansi.Strip(line.text); !strings.Contains(got, test.want) {
				t.Errorf("compact row = %q, want to contain %q", got, test.want)
			}
		})
	}
}

func TestAdvisorRowShowsModelAndMetrics(t *testing.T) {
	detail := &detailState{}
	usage := model.Usage{Model: "claude-fable-5", InputTokens: 1000, OutputTokens: 500}
	event := model.Event{Kind: model.EventAdvisor, Model: "claude-fable-5", Usage: &usage}
	line := detail.eventLines(&model.Session{}, event, 0, "event")[0]
	if got := ansi.Strip(line.text); !strings.Contains(got, "advisor(") || !strings.Contains(got, shortModelName("claude-fable-5")) {
		t.Errorf("advisor row = %q, want advisor(model) label", got)
	}
	if line.metrics == "" {
		t.Errorf("advisor row metrics empty, want token flow from usage")
	}
}

func TestSelectionOverridesPromptTints(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	detail := &detailState{styles: styleSet}
	for _, role := range []detailRole{detailUserPrompt, detailSystemPrompt} {
		line := detailLine{text: "prompt", role: role}
		plain := fitPlain(line.text, 24, false)
		if got, want := detail.styleLine(plain, line, true, true), styleSet.selected.Render(plain); got != want {
			t.Errorf("selected role %v styling = %q, want selection %q", role, got, want)
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
	if !strings.Contains(keyBar, "←/→ fold") || !strings.Contains(keyBar, "space toggle") || !strings.Contains(keyBar, "E/C all") || !strings.Contains(keyBar, "↵ inspect") || !strings.Contains(keyBar, "q quit") || strings.Contains(keyBar, "…") {
		t.Fatalf("80-column detail key bar = %q, want folding, inspect, and quit hints without truncation", keyBar)
	}
}

func TestDetailKeyBarUsesContextualNewcomerHints(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "route", Subagents: []*model.Session{{ID: "scout"}}}, 160, 12, newStyles())
	keyBar := strings.Split(ansi.Strip(detail.view()), "\n")[11]
	for _, want := range []string{"space toggle", "↵ inspect", "tab switch", "w nowrap", "T time"} {
		if !strings.Contains(keyBar, want) {
			t.Fatalf("wrapped Timeline key bar missing %q: %q", want, keyBar)
		}
	}

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	keyBar = strings.Split(ansi.Strip(detail.view()), "\n")[11]
	if !strings.Contains(keyBar, "w wrap") || strings.Contains(keyBar, "w nowrap") {
		t.Fatalf("unwrapped Timeline key bar did not expose wrap action: %q", keyBar)
	}

	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	keyBar = strings.Split(ansi.Strip(detail.view()), "\n")[11]
	if !strings.Contains(keyBar, "↵ open") || !strings.Contains(keyBar, "T time") || strings.Contains(keyBar, "↵ inspect") || strings.Contains(keyBar, "space toggle") || strings.Contains(keyBar, "w wrap") || strings.Contains(keyBar, "w nowrap") {
		t.Fatalf("Subagents key bar did not limit hints to applicable actions: %q", keyBar)
	}
}

func TestNarrowKeyBarsKeepWholeEssentialHints(t *testing.T) {
	t.Run("detail", func(t *testing.T) {
		for _, test := range []struct {
			width int
			wants []string
		}{
			{width: 40, wants: []string{"←/→ fold", "↵ inspect", "esc back"}},
			{width: 20, wants: []string{"↵ inspect", "esc back"}},
			{width: 10, wants: []string{"esc back"}},
		} {
			text := detailKeyText(test.width, true, tabTimeline, true)
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
		item := newItemView(model.Event{
			Kind: model.EventThinking, Text: "Chart route",
			RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: 1},
		}, model.AgentClaude, nil, 20, 8, newStyles())
		keyBar := strings.TrimSpace(strings.Split(ansi.Strip(item.view()), "\n")[7])
		if !strings.Contains(keyBar, "esc back") || strings.Contains(keyBar, "R raw") || strings.Contains(keyBar, "…") || ansi.StringWidth(keyBar) > 20 {
			t.Fatalf("20-column item key bar lost a whole back hint: %q", keyBar)
		}
	})
}

func TestDetailKeyBarsAdvertiseOnlyLiveContextualBindings(t *testing.T) {
	for _, tab := range []detailTab{tabTimeline, tabSubagents} {
		keyBar := detailKeyText(160, true, tab, true)
		for _, unwanted := range []string{"J/K", "subagent"} {
			if strings.Contains(keyBar, unwanted) {
				t.Errorf("%s key bar retained removed hint %q: %q", tab.title(), unwanted, keyBar)
			}
		}
		if tab == tabTimeline {
			for _, want := range []string{"←/→ fold", "E/C all"} {
				if !strings.Contains(keyBar, want) {
					t.Errorf("Timeline key bar missing %q: %q", want, keyBar)
				}
			}
		} else {
			for _, want := range []string{"←/→ column", "⇧O sort"} {
				if !strings.Contains(keyBar, want) {
					t.Errorf("Subagents key bar missing %q: %q", want, keyBar)
				}
			}
			if strings.Contains(keyBar, "E/C all") || strings.Contains(keyBar, "←/→ fold") {
				t.Errorf("Subagents key bar advertised Timeline-only bulk action: %q", keyBar)
			}
		}
	}
}

func TestWideKeyBarsAdvertiseMouse(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentClaude}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 12})
	m = updated.(Model)
	if keyBar := strings.Split(ansi.Strip(m.View()), "\n")[11]; !strings.Contains(keyBar, "mouse scroll/click") {
		t.Fatalf("wide list key bar missing mouse hint: %q", keyBar)
	}
	if keyBar := detailKeyText(160, false, tabTimeline, true); !strings.Contains(keyBar, "mouse scroll/click") {
		t.Fatalf("wide detail key bar missing mouse hint: %q", keyBar)
	}
	if keyBar := detailKeyText(160, false, tabInfo, true); !strings.Contains(keyBar, "mouse wheel scroll") || strings.Contains(keyBar, "click") {
		t.Fatalf("wide Info key bar advertises an inactive mouse action: %q", keyBar)
	}
	if keyBar := itemKeyText(160); !strings.Contains(keyBar, "wheel scroll") {
		t.Fatalf("wide item key bar missing mouse hint: %q", keyBar)
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
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}} {
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

func TestDetailLoadPreservesSubagentSortInput(t *testing.T) {
	current := &model.Session{ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl"}
	m := NewModel([]*model.Session{current}, nil)
	m.screen = screenDetail
	m.detail = newDetailState(current, m.width, m.height, m.styles)
	detail := detailStateFromScreen(t, m.detail)
	detail.update(tea.KeyMsg{Type: tea.KeyTab})
	detail.update(tea.KeyMsg{Type: tea.KeyRight})
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(sortColumnKey)})
	m.detailGeneration = 1
	loaded := cloneSession(current)
	loaded.Subagents = []*model.Session{
		{ID: "zulu", Agent: model.AgentClaude, Title: "Zulu"},
		{ID: "alpha", Agent: model.AgentClaude, Title: "Alpha"},
		{ID: "mike", Agent: model.AgentClaude, Title: "Mike"},
	}

	updated, _ := m.Update(detailLoadedMsg{generation: 1, identity: sessionIdentity(current), session: loaded})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.tab != tabSubagents || detail.subagentColumnFocus != columnTitle || detail.subagentSort != (sortState{kind: columnTitle, active: true}) {
		t.Fatalf("detail load state = tab %v sort %#v focus %v, want Subagents with active title sort", detail.tab, detail.subagentSort, detail.subagentColumnFocus)
	}
	if got := []string{detail.subagents[0].s.ID, detail.subagents[1].s.ID, detail.subagents[2].s.ID}; !slices.Equal(got, []string{"alpha", "mike", "zulu"}) {
		t.Fatalf("loaded subagent order = %v, want alpha, mike, zulu", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if got := detailStateFromScreen(t, m.detail).session.ID; got != "alpha" {
		t.Fatalf("immediate drill opened %q, want first sorted subagent alpha", got)
	}
}

func TestDrilledDetailFollowsRootLiveUpdate(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Path: "/workspace/scout.jsonl", Title: "Before"}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl", Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
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
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	running := []byte(`{"status":"running","padding":"` + strings.Repeat("x", 300) + `"}`)
	recordRef := writeRawRecord(t, running)
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: recordRef.Path, Project: "starship", Title: "Plan route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Timestamp: now.Add(-5 * time.Minute), Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route", RecordRef: recordRef, Detail: &model.ToolDetail{Input: "check-route", Output: "running"}},
		},
	}
	m := newModelWithClock([]*model.Session{root}, nil, func() time.Time { return now })
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 80, Height: 10}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}} {
		updated, cmd := m.Update(msg)
		m = updated.(Model)
		if cmd != nil {
			updated, _ = m.Update(cmd())
			m = updated.(Model)
		}
	}
	before := m.detail.(*itemView)
	oldOffset := before.viewport.YOffset
	oldGeneration := before.generation
	if before.wrap || oldOffset == 0 {
		t.Fatalf("pre-update item state wrap=%t offset=%d", before.wrap, oldOffset)
	}

	finished := []byte(`{"status":"finished","padding":"` + strings.Repeat("y", 300) + `"}`)
	if err := os.WriteFile(recordRef.Path, append(finished, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	finishedRef := model.RecordRef{Path: recordRef.Path, Length: int64(len(finished)), Digest: sha256.Sum256(finished)}
	replacement := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: recordRef.Path, Project: "voyage", Title: "Plan updated route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Timestamp: now.Add(-7 * time.Minute), Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route", RecordRef: finishedRef, Detail: &model.ToolDetail{Input: "check-route", Output: "finished"}},
		},
	}
	updated, cmd := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("live refresh did not re-request the visible raw record")
	}
	firstRawCmd := cmd
	updated, cmd = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("second live refresh did not re-request the visible raw record")
	}
	updated, _ = m.Update(firstRawCmd())
	m = updated.(Model)
	if item := m.detail.(*itemView); !item.rawLoading || len(item.raw) != 0 {
		t.Fatalf("refreshed item accepted generation %d into generation %d", oldGeneration+1, item.generation)
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	view := ansi.Strip(m.View())
	item, ok := m.detail.(*itemView)
	if !ok || len(m.detailStack) != 1 {
		t.Fatalf("live update left screen=%T stack=%d, want refreshed item", m.detail, len(m.detailStack))
	}
	if item.wrap || item.viewport.YOffset != oldOffset {
		t.Fatalf("refreshed item state wrap=%t offset=%d, want wrap=false offset=%d", item.wrap, item.viewport.YOffset, oldOffset)
	}
	for _, want := range []string{"voyage › Plan updated route › Bash", "Raw"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed item missing %q:\n%s", want, view)
		}
	}
	var refreshedLines []string
	for _, line := range item.lines {
		refreshedLines = append(refreshedLines, line.text)
	}
	content := strings.Join(refreshedLines, "\n")
	for _, want := range []string{"finished", "relative time  7m", `"status": "finished"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("refreshed item content missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "running") {
		t.Fatalf("refreshed item retained stale output:\n%s", content)
	}
}

func TestItemNavigationDuringRawRefreshKeepsLatestOffset(t *testing.T) {
	item := newItemView(model.Event{
		Kind:      model.EventSystem,
		Text:      strings.Repeat("route status\n", 24),
		RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: 18},
	}, model.AgentCodex, nil, 80, 8, newStyles())
	if cmd := item.requestRaw(); cmd == nil {
		t.Fatal("raw request did not enter the loading state")
	}
	item.viewport.SetYOffset(5)
	restoreOffset := 9
	item.restoreYOffset = &restoreOffset

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	item.update(rawRecordLoadedMsg{record: []byte(`{"status":"ready"}`)})

	if item.viewport.YOffset != 0 {
		t.Fatalf("raw completion restored offset %d after navigation, want latest offset 0", item.viewport.YOffset)
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
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}} {
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

func TestSubagentSelectionRemainsVisibleAcrossSortedLiveMove(t *testing.T) {
	target := &model.Session{ID: "target", Agent: model.AgentClaude, Path: "/workspace/target.jsonl", Title: "A target"}
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Subagents: []*model.Session{target},
	}
	for index := 1; index < 12; index++ {
		root.Subagents = append(root.Subagents, &model.Session{
			ID: fmt.Sprintf("worker-%02d", index), Agent: model.AgentClaude,
			Path: fmt.Sprintf("/workspace/worker-%02d.jsonl", index), Title: fmt.Sprintf("Worker %02d", index),
		})
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 12},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(sortTitleKey)},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	replacement := cloneSession(root)
	replacement.Subagents[0].Title = "Zulu target"
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	if got := detail.focusedSubagent(); sessionIdentity(got) != sessionIdentity(target) {
		t.Fatalf("selected identity after live move = %#v, want target", got)
	}
	selectedRow := detail.firstRenderedRow(detail.selectedLine)
	if selectedRow < detail.viewport.YOffset || selectedRow >= detail.viewport.YOffset+detail.viewport.Height {
		t.Fatalf("selected row %d outside viewport offset=%d height=%d", selectedRow, detail.viewport.YOffset, detail.viewport.Height)
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
	if got := detailStateFromScreen(t, m.detail).lines[1].subagentTokens; got != "350" {
		t.Fatalf("refreshed scout recursive tokens = %q, want 350", got)
	}
	if got := detailStateFromScreen(t, m.detail).lines[1].subagentCost; got != "~$0.35" {
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
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
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
		for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'J'}}, {Type: tea.KeyEnter}} {
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
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}} {
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
	detail.moveFocus(1)
	detail.moveFocus(1)

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
		for text, want := range wantRoles {
			if !strings.HasSuffix(line.text, text) {
				continue
			}
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

func TestExpandedMessageKeepsBlankLinesAndIndentation(t *testing.T) {
	text := "First paragraph.\n\n    indented line\n\nLast paragraph."
	session := &model.Session{ID: "paragraphs", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: text},
		{Kind: model.EventAssistantText, Text: text},
	}}
	detail := newDetailState(session, 80, 24, newStyles())

	var body []string
	for _, line := range detail.lines {
		if line.expandable {
			body = body[:0]
			continue
		}
		body = append(body, strings.TrimRight(line.text, " "))
	}
	rendered := strings.Join(body, "\n")
	if !strings.Contains(rendered, "First paragraph.\n\n") || !strings.Contains(rendered, "\n\n  Last paragraph.") {
		t.Errorf("expanded message lost its paragraph breaks:\n%q", rendered)
	}
	if !strings.Contains(rendered, "    indented line") {
		t.Errorf("expanded message lost its indentation:\n%q", rendered)
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
		"claude │ mission-control (/srv/fictional/deep/telemetry)",
		"opus-4.8, fable-5",
		"branch orbit/alpha",
		"Jan 02 03:04→Jan 02 03:14",
	} {
		if !strings.Contains(header, want) {
			t.Fatalf("detail header missing %q:\n%s", want, header)
		}
	}
}

func TestDetailHeaderShowsOnlyPerNodeCosts(t *testing.T) {
	child := &model.Session{Usage: []model.Usage{{InputTokens: 2_000}}, Cost: model.Cost{USD: 2}}
	session := &model.Session{
		ID: "mission", Agent: model.AgentClaude, Usage: []model.Usage{{InputTokens: 1_000}}, Cost: model.Cost{USD: 1}, Subagents: []*model.Session{child},
	}
	line := strings.TrimSpace(newDetailState(session, 120, 12, newStyles()).headerPanelLines()[2].plain)
	if !strings.Contains(line, "total $3.00 │ own $1.00 │ subagents $2.00") || strings.Contains(line, "tokens") || strings.Contains(line, "1000") || strings.Contains(line, "3000") {
		t.Fatalf("detail header accounting = %q, want cost-only nodes", line)
	}
}

func TestDetailHeaderUsesOwnedCost(t *testing.T) {
	child := &model.Session{Cost: model.Cost{USD: 2}}
	session := &model.Session{
		ID: "mission", Agent: model.AgentClaude,
		Cost: model.Cost{USD: 10}, DuplicatedUSD: 4, Subagents: []*model.Session{child},
	}

	line := strings.TrimSpace(newDetailState(session, 120, 12, newStyles()).headerPanelLines()[2].plain)
	if !strings.Contains(line, "total $8.00 │ own $6.00 │ subagents $2.00") ||
		strings.Contains(line, "total $12") || strings.Contains(line, "own $10") {
		t.Fatalf("detail header accounting = %q, want owned cost nodes", line)
	}
}

func TestDetailHeaderLeadsWithEmphasizedSessionTitle(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["default"])
	detail := newDetailState(&model.Session{ID: "mission", Agent: model.AgentClaude, Title: "Plan the lunar route"}, 80, 12, styleSet)

	line := detail.headerPanelLines()[0]
	if strings.TrimSpace(line.plain) != "Plan the lunar route" || line.styled != styleSet.emphasis.Render(line.plain) {
		t.Fatalf("first header line = %#v, want emphasized session title", line)
	}
}

func TestDetailHeaderOmitsUnavailableMetadata(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &model.Session{ID: "mission", Agent: model.AgentClaude}

	header := ansi.Strip(newDetailState(session, 200, 12, newStyles()).header())
	for _, artifact := range []string{"()", "—", " │  │ ", "│  │"} {
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

func TestTimelineBodyLinesOverLineCapElidesWholeLines(t *testing.T) {
	lines := make([]string, detailPreviewLineCap+2)
	for index := range lines {
		lines[index] = fmt.Sprintf("route-%02d", index)
	}
	want := append(slices.Clone(lines[:19]), "… 3 lines hidden …")
	want = append(want, lines[22:]...)

	if got := timelineBodyLines(strings.Join(lines, "\n")); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want %#v", got, want)
	}
}

func TestTimelineBodyLinesOverRuneBudgetElidesWholeLines(t *testing.T) {
	head := strings.Repeat("h", 2_000)
	tail := strings.Repeat("t", 2_000)
	text := strings.Join([]string{head, strings.Repeat("m", 2_000), tail}, "\n")
	want := []string{head, "… 1 line hidden …", tail}

	if got := timelineBodyLines(text); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want %#v", got, want)
	}
}

func TestTimelineBodyLinesCountsLineBreaksAgainstRuneBudget(t *testing.T) {
	lines := make([]string, detailPreviewLineCap)
	for index := range lines {
		lines[index] = strings.Repeat("x", 102)
	}
	want := append(slices.Clone(lines[:19]), "… 1 line hidden …")
	want = append(want, lines[20:]...)

	if got := timelineBodyLines(strings.Join(lines, "\n")); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() returned %d lines, want one source line elided", len(got))
	}
}

func TestTimelineBodyLinesIncludesMarkerInRuneBudget(t *testing.T) {
	lines := make([]string, detailPreviewLineCap)
	for index := range lines {
		lines[index] = strings.Repeat("x", 104)
	}
	want := append(slices.Clone(lines[:19]), "… 2 lines hidden …")
	want = append(want, lines[21:]...)

	got := timelineBodyLines(strings.Join(lines, "\n"))
	if !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() returned %d lines, want two source lines elided", len(got))
	}
	if runes := utf8.RuneCountInString(strings.Join(got, "\n")); runes > detailPreviewRuneCap {
		t.Fatalf("timelineBodyLines() rendered %d runes, want <= %d", runes, detailPreviewRuneCap)
	}
}

func TestTimelineBodyLinesOverBothLimitsUsesOneAccurateMarker(t *testing.T) {
	lines := make([]string, 50)
	for index := range lines {
		lines[index] = fmt.Sprintf("route-%02d-", index) + strings.Repeat("x", 191)
	}
	want := append(slices.Clone(lines[:10]), "… 30 lines hidden …")
	want = append(want, lines[40:]...)

	if got := timelineBodyLines(strings.Join(lines, "\n")); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want %#v", got, want)
	}
}

func TestTimelineBodyLinesLongSingleLineEndsWithEllipsis(t *testing.T) {
	text := strings.Repeat("航", detailPreviewRuneCap+100)
	want := []string{strings.Repeat("航", detailPreviewRuneCap-1) + "…"}

	if got := timelineBodyLines(text); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() returned %d lines with %d runes, want one line with %d runes", len(got), len([]rune(strings.Join(got, "\n"))), len([]rune(want[0])))
	}
}

func TestTimelineBodyLinesLongANSISequenceKeepsVisibleContent(t *testing.T) {
	control := "\x1b]0;" + strings.Repeat("x", 5_000) + "\x07"
	text := control + strings.Repeat("A", 5_000)
	want := []string{strings.Repeat("A", detailPreviewRuneCap-1) + "…"}

	got := timelineBodyLines(text)
	if !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() rendered %q, want visible content with trailing ellipsis", detailPlainText(strings.Join(got, "\n")))
	}
}

func TestTimelineBodyLinesTwoLongLinesKeepsTruncatedHead(t *testing.T) {
	marker := "… 1 line hidden …"
	lineRunes := detailPreviewRuneCap - utf8.RuneCountInString(marker) - 1
	text := strings.Repeat("A", 5_000) + "\n" + strings.Repeat("B", 5_000)
	want := []string{strings.Repeat("A", lineRunes-1) + "…", marker}

	if got := timelineBodyLines(text); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want a truncated head line and marker", got)
	}
}

func TestTimelineBodyLinesThreeLongLinesKeepsTruncatedHead(t *testing.T) {
	marker := "… 2 lines hidden …"
	lineRunes := detailPreviewRuneCap - utf8.RuneCountInString(marker) - 1
	text := strings.Join([]string{strings.Repeat("A", 5_000), strings.Repeat("B", 5_000), strings.Repeat("C", 5_000)}, "\n")
	want := []string{strings.Repeat("A", lineRunes-1) + "…", marker}

	if got := timelineBodyLines(text); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want a truncated head line and marker", got)
	}
}

func TestTimelineBodyLinesLongHeadAndShortTailKeepsBoth(t *testing.T) {
	tail := "short tail line"
	lineRunes := detailPreviewRuneCap - utf8.RuneCountInString(tail) - 1
	text := strings.Repeat("A", 5_000) + "\n" + tail
	want := []string{strings.Repeat("A", lineRunes-1) + "…", tail}

	if got := timelineBodyLines(text); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want truncated head and whole tail", got)
	}
}

func TestTimelineBodyLinesBlankHeadStillKeepsContent(t *testing.T) {
	text := "\n" + strings.Repeat("A", 5_000)
	want := []string{"", strings.Repeat("A", detailPreviewRuneCap-2) + "…"}

	if got := timelineBodyLines(text); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want blank head and truncated content", got)
	}
}

func TestTimelineBodyLinesBlankWindowsStillKeepMiddleContent(t *testing.T) {
	lines := make([]string, detailPreviewLineCap)
	lines[19] = strings.Repeat("A", 5_000)
	want := slices.Clone(lines)
	want[19] = strings.Repeat("A", 4_056) + "…"

	if got := timelineBodyLines(strings.Join(lines, "\n")); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() returned %d rows without the expected middle content", len(got))
	}
}

func TestTimelineBodyLinesBlankWindowsOverLineCapKeepContent(t *testing.T) {
	lines := make([]string, detailPreviewLineCap+1)
	lines[19] = strings.Repeat("A", 5_000)
	want := make([]string, detailPreviewLineCap)
	want[19] = strings.Repeat("A", 4_038) + "…"
	want[20] = "… 2 lines hidden …"

	if got := timelineBodyLines(strings.Join(lines, "\n")); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() returned %d rows without the expected content and marker", len(got))
	}
}

func TestTimelineBodyLinesBlankWindowsKeepShortMiddleWhole(t *testing.T) {
	lines := make([]string, detailPreviewLineCap+1)
	lines[19] = "visible middle"
	want := make([]string, detailPreviewLineCap)
	want[19] = lines[19]
	want[20] = "… 2 lines hidden …"

	if got := timelineBodyLines(strings.Join(lines, "\n")); !slices.Equal(got, want) {
		t.Fatalf("timelineBodyLines() = %#v, want whole middle line and marker", got)
	}
}

func TestTimelineBodyLinesFormattingOnlyWindowsKeepVisibleContent(t *testing.T) {
	lines := make([]string, detailPreviewLineCap+1)
	for index := range lines {
		lines[index] = " \t\x1b[31m\x1b[0m"
	}
	lines[19] = strings.Repeat("A", 5_000)

	got := timelineBodyLines(strings.Join(lines, "\n"))
	if !slices.ContainsFunc(got, func(line string) bool { return strings.Contains(line, "A") }) {
		t.Fatalf("timelineBodyLines() hid the only visibly non-blank source line")
	}
	if len(got) > detailPreviewLineCap {
		t.Fatalf("timelineBodyLines() returned %d rows, want <= %d", len(got), detailPreviewLineCap)
	}
	if runes := utf8.RuneCountInString(strings.Join(got, "\n")); runes > detailPreviewRuneCap {
		t.Fatalf("timelineBodyLines() rendered %d runes, want <= %d", runes, detailPreviewRuneCap)
	}
}

func TestDetailHasBodyUsesTimelineInputProjection(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "rune-bounded lines", input: strings.Repeat("a", 2_500) + "\n" + strings.Repeat("b", 2_500), want: true},
		{name: "newline retained in head", input: "first\n" + strings.Repeat("b", 5_000), want: true},
		{name: "long multiline preview", input: strings.Repeat("a", 5_000) + "\n" + strings.Repeat("b", 5_000), want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := model.Event{
				Kind: model.EventToolCall, ToolName: "exec_command",
				Detail: &model.ToolDetail{Input: test.input},
			}
			if got := detailHasBody(event); got != test.want {
				t.Fatalf("detailHasBody() = %t, want %t for timeline projection %#v", got, test.want, timelineBodyLines(test.input))
			}
		})
	}
}

func TestDetailTimelineExpandsByDefault(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventThinking, Text: "Choose the safest route"},
		{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "survey --ridge", Detail: &model.ToolDetail{Output: "route clear"}},
		{Kind: model.EventAssistantText, Text: "The ridge route is clear."},
	}}
	detail := newDetailState(session, 80, 20, newStyles())

	var rows []string
	for _, line := range detail.lines {
		rows = append(rows, strings.TrimSpace(line.text))
	}
	if !slices.Contains(rows, glyphSecondary+" thinking: Choose the safest route") || !slices.Contains(rows, "route clear") {
		t.Fatalf("default timeline rows = %#v, want expanded turn and tool output", rows)
	}
	if len(detail.expanded) != 0 {
		t.Fatalf("default expansion overrides = %#v, want empty map", detail.expanded)
	}
}

func TestDetailTimelineOpensAtNewestEvent(t *testing.T) {
	events := []model.Event{{Kind: model.EventUser, Text: "Survey the crater"}}
	for index := range 8 {
		events = append(events, model.Event{Kind: model.EventThinking, Text: fmt.Sprintf("Observation %02d", index)})
	}
	events = append(events, model.Event{Kind: model.EventAssistantText, Text: "Newest route confirmed"})
	detail := newDetailState(&model.Session{ID: "lunar", Agent: model.AgentCodex, Events: events}, 80, 10, newStyles())

	wantOffset := max(0, len(detail.rendered)-detail.viewport.Height)
	if detail.focus != len(detail.focusables)-1 || detail.viewport.YOffset != wantOffset {
		t.Fatalf("open state = focus %d/%d offset %d/%d, want last focusable at bottom", detail.focus, len(detail.focusables)-1, detail.viewport.YOffset, wantOffset)
	}
	if view := ansi.Strip(detail.view()); !strings.Contains(view, "Newest route confirmed") {
		t.Fatalf("newest event is not visible on open:\n%s", view)
	}
}

func TestDetailTimelineBottomAnchorStartsAtLogicalLine(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{{
		Kind: model.EventAssistantText, Text: strings.Repeat("newest wrapped telemetry ", 12),
	}}}
	detail := newDetailState(session, 28, 10, newStyles())

	if detail.viewport.YOffset >= len(detail.rendered) || !detail.rendered[detail.viewport.YOffset].first {
		t.Fatalf("bottom anchor offset %d starts on continuation: %#v", detail.viewport.YOffset, detail.rendered)
	}
	if maxOffset := max(0, len(detail.rendered)-detail.viewport.Height); detail.viewport.YOffset > maxOffset {
		t.Fatalf("bottom anchor offset = %d, want <= %d", detail.viewport.YOffset, maxOffset)
	}
}

func TestDetailMouseWheelDownScrollsTimeline(t *testing.T) {
	events := make([]model.Event, 20)
	for index := range events {
		events[index] = model.Event{Kind: model.EventUser, Text: fmt.Sprintf("Instruction %02d", index)}
	}
	m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentCodex, Events: events}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	detail.viewport.GotoTop()
	focus, selectedLine, subagentSelection := detail.focus, detail.selectedLine, detail.subagentSelection

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	if detail.viewport.YOffset != mouseWheelRows || detail.focus != focus || detail.selectedLine != selectedLine || detail.subagentSelection != subagentSelection {
		t.Fatalf("detail wheel down offset=%d focus=%d selected=%d subagent=%d", detail.viewport.YOffset, detail.focus, detail.selectedLine, detail.subagentSelection)
	}
}

func TestDetailMouseWheelUpScrollsTimeline(t *testing.T) {
	events := make([]model.Event, 20)
	for index := range events {
		events[index] = model.Event{Kind: model.EventUser, Text: fmt.Sprintf("Instruction %02d", index)}
	}
	m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentCodex, Events: events}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	detail.viewport.SetYOffset(6)
	focus, selectedLine, subagentSelection := detail.focus, detail.selectedLine, detail.subagentSelection

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	if detail.viewport.YOffset != 3 || detail.focus != focus || detail.selectedLine != selectedLine || detail.subagentSelection != subagentSelection {
		t.Fatalf("detail wheel up offset=%d focus=%d selected=%d subagent=%d", detail.viewport.YOffset, detail.focus, detail.selectedLine, detail.subagentSelection)
	}
}

func TestDetailMouseWheelModifiersStillScrollVertically(t *testing.T) {
	events := make([]model.Event, 20)
	for index := range events {
		events[index] = model.Event{Kind: model.EventUser, Text: fmt.Sprintf("Instruction %02d", index)}
	}

	for _, test := range []struct {
		name  string
		mouse tea.MouseMsg
	}{
		{name: "shift", mouse: tea.MouseMsg{Shift: true}},
		{name: "alt", mouse: tea.MouseMsg{Alt: true}},
		{name: "control", mouse: tea.MouseMsg{Ctrl: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentCodex, Events: events}}, nil)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
			m = updated.(Model)
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			detail := detailStateFromScreen(t, m.detail)
			detail.viewport.GotoTop()

			test.mouse.Action = tea.MouseActionPress
			test.mouse.Button = tea.MouseButtonWheelDown
			updated, _ = m.Update(test.mouse)
			m = updated.(Model)

			if got := detailStateFromScreen(t, m.detail).viewport.YOffset; got != mouseWheelRows {
				t.Fatalf("modified detail wheel offset = %d, want %d", got, mouseWheelRows)
			}
		})
	}
}

func TestSubagentsMouseWheelScrollsWithoutChangingSelection(t *testing.T) {
	children := make([]*model.Session, 12)
	for index := range children {
		children[index] = &model.Session{ID: fmt.Sprintf("worker-%02d", index), Agent: model.AgentClaude}
	}
	m := NewModel([]*model.Session{{ID: "route", Subagents: children}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	detail.viewport.GotoTop()

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.viewport.YOffset != mouseWheelRows || detail.subagentSelection != 0 || detail.selectedLine != 1 {
		t.Fatalf("subagents wheel offset=%d selection=%d line=%d", detail.viewport.YOffset, detail.subagentSelection, detail.selectedLine)
	}
}

func TestItemMouseWheelScrollsAndClicksDoNothing(t *testing.T) {
	item := newItemView(model.Event{Kind: model.EventThinking, Text: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine"}, model.AgentCodex, nil, 80, 8, newStyles())
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if got := m.detail.(*itemView).viewport.YOffset; got != mouseWheelRows {
		t.Fatalf("item wheel down offset = %d, want %d", got, mouseWheelRows)
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: 2})
	m = updated.(Model)
	if _, ok := m.detail.(*itemView); !ok || m.detail.(*itemView).viewport.YOffset != mouseWheelRows {
		t.Fatalf("item click detail=%T offset=%d, want unchanged item", m.detail, m.detail.(*itemView).viewport.YOffset)
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = updated.(Model)
	if got := m.detail.(*itemView).viewport.YOffset; got != 0 {
		t.Fatalf("item wheel up offset = %d, want 0", got)
	}
}

func TestItemShiftWheelStillScrollsVertically(t *testing.T) {
	item := newItemView(model.Event{Kind: model.EventThinking, Text: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine"}, model.AgentCodex, nil, 80, 8, newStyles())
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, Shift: true})
	m = updated.(Model)
	if got := m.detail.(*itemView).viewport.YOffset; got != mouseWheelRows {
		t.Fatalf("shift+wheel item offset = %d, want %d", got, mouseWheelRows)
	}
}

func TestMouseHotPathAvoidsCloningRenderedTimeline(t *testing.T) {
	events := make([]model.Event, 2000)
	for index := range events {
		events[index] = model.Event{Kind: model.EventUser, Text: fmt.Sprintf("Instruction %04d", index)}
	}
	m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentCodex, Events: events}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	before := detailStateFromScreen(t, m.detail)
	before.viewport.GotoTop()

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, X: 2, Y: 6})
	m = updated.(Model)
	if detailStateFromScreen(t, m.detail) != before {
		t.Fatal("ignored mouse motion cloned the detail timeline")
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	after := detailStateFromScreen(t, m.detail)
	if after == before || &after.rendered[0] != &before.rendered[0] || after.viewport.YOffset != mouseWheelRows {
		t.Fatalf("wheel clone detail=%t rows shared=%t offset=%d", after != before, &after.rendered[0] == &before.rendered[0], after.viewport.YOffset)
	}
}

func TestDetailMouseClickSelectsRow(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventAssistantText, Text: "Route prepared"},
		{Kind: model.EventToolCall, ToolName: "Read", Detail: &model.ToolDetail{Output: "map ready"}},
	}}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	target := 1
	y := viewLineY(t, m.View(), "Route prepared", 0)

	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = updated.(Model)

	detail := detailStateFromScreen(t, m.detail)
	if detail.focus != target {
		t.Fatalf("click focus = %d, want the clicked reply row %d", detail.focus, target)
	}
}

func TestDetailMouseClicksNeverFoldTheClickedRow(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		event model.Event
		label string
	}{
		{name: "prompt", event: model.Event{Kind: model.EventUser, Text: strings.Repeat("Compare every route ", 12)}, label: "Compare every route"},
		{name: "tool", event: model.Event{Kind: model.EventToolCall, ToolName: "Read", Detail: &model.ToolDetail{Output: "map ready"}}, label: "Read"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
				{Kind: model.EventUser, Text: "Survey the crater"},
				testCase.event,
				{Kind: model.EventAssistantText, Text: "Route prepared"},
			}}
			m := NewModel([]*model.Session{session}, nil)
			for _, msg := range []tea.Msg{
				tea.WindowSizeMsg{Width: 80, Height: 16},
				tea.KeyMsg{Type: tea.KeyEnter},
				tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}},
			} {
				updated, _ := m.Update(msg)
				m = updated.(Model)
			}
			click := tea.MouseMsg{X: 2, Y: viewLineY(t, m.View(), testCase.label, 0), Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
			for range 3 {
				updated, _ := m.Update(click)
				m = updated.(Model)
			}

			detail := detailStateFromScreen(t, m.detail)
			focused := detail.focusables[detail.focus]
			if !focused.expandable || !detail.isExpanded(focused.key) || m.screen != screenDetail {
				t.Fatalf("repeated clicks left expandable=%t expanded=%t screen=%v, want the row selected and still expanded", focused.expandable, detail.isExpanded(focused.key), m.screen)
			}
		})
	}
}

func TestDetailMouseClickNeverOpensPlainEvent(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventAssistantText, Text: "Route prepared"},
	}}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	target := len(detail.focusables) - 1
	y := viewLineY(t, m.View(), "Route prepared", 0)
	click := tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
	updated, _ = m.Update(click)
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.focus != target || detail.session != session || len(m.detailStack) != 0 {
		t.Fatalf("first plain-event click focus=%d session=%q stack=%d", detail.focus, detail.session.ID, len(m.detailStack))
	}

	updated, _ = m.Update(click)
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	if detail.focus != target || detail.session != session || len(m.detailStack) != 0 {
		t.Fatalf("second plain-event click focus=%d session=%q stack=%d, want selected timeline row", detail.focus, detail.session.ID, len(m.detailStack))
	}
}

func TestSubagentsMouseClickNeverDrillsIntoSelectedRow(t *testing.T) {
	first := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge"}
	second := &model.Session{ID: "mapper", Agent: model.AgentCodex, Title: "Map crater"}
	root := &model.Session{ID: "route", Agent: model.AgentClaude, Subagents: []*model.Session{first, second}}
	m := NewModel([]*model.Session{root}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	y := viewLineY(t, m.View(), "Map crater", 0)
	click := tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
	updated, _ = m.Update(click)
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.subagentSelection != 1 || detail.session != root || len(m.detailStack) != 0 {
		t.Fatalf("first subagents click selection=%d session=%q stack=%d", detail.subagentSelection, detail.session.ID, len(m.detailStack))
	}

	updated, _ = m.Update(click)
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	if detail.session != root || detail.subagentSelection != 1 || len(m.detailStack) != 0 {
		t.Fatalf("second subagents click detail=%q selection=%d stack=%d, want selected root row", detail.session.ID, detail.subagentSelection, len(m.detailStack))
	}
}

func TestTimelineMouseClickNeverDrillsIntoSubagentRow(t *testing.T) {
	child := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge"}
	root := &model.Session{ID: "route", Agent: model.AgentCodex, Subagents: []*model.Session{child}, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventSubagent, Subagent: child},
	}}
	m := NewModel([]*model.Session{root}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	target := len(detail.focusables) - 1
	y := viewLineY(t, m.View(), "Task(Scout ridge)", 0)
	click := tea.MouseMsg{X: 2, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
	updated, _ = m.Update(click)
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.focus != target || detail.session != root || len(m.detailStack) != 0 {
		t.Fatalf("first timeline subagent click focus=%d session=%q stack=%d", detail.focus, detail.session.ID, len(m.detailStack))
	}
	updated, _ = m.Update(click)
	m = updated.(Model)

	detail = detailStateFromScreen(t, m.detail)
	if detail.session != root || detail.focus != target || len(m.detailStack) != 0 {
		t.Fatalf("second timeline subagent click detail=%q focus=%d stack=%d, want selected root row", detail.session.ID, detail.focus, len(m.detailStack))
	}
}

func TestDetailRowAtYMapsCompactTimeline(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventAssistantText, Text: "Route prepared"},
	}}
	detail := newDetailState(session, 80, 8, newStyles())
	detail.viewport.GotoTop()

	index, ok := detail.rowAtY(4)
	if !ok || index != 0 {
		t.Fatalf("compact detail rowAtY(4) = %d, %t, want first focusable", index, ok)
	}
}

func TestDetailRowAtYHonorsPanelBoundariesAndOffset(t *testing.T) {
	session := &model.Session{ID: "route", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventAssistantText, Text: "Route prepared"},
		{Kind: model.EventToolCall, ToolName: "Read", Detail: &model.ToolDetail{Output: "one\ntwo\nthree\nfour\nfive"}},
	}}
	detail := newDetailState(session, 80, 12, newStyles())
	detail.viewport.SetYOffset(4)

	for _, test := range []struct {
		y     int
		index int
		ok    bool
	}{
		{y: 0}, {y: 4}, {y: 5},
		{y: 6, index: 2, ok: true},
		{y: 9, index: 2, ok: true},
		{y: 10}, {y: 11},
	} {
		index, ok := detail.rowAtY(test.y)
		if index != test.index || ok != test.ok {
			t.Errorf("rowAtY(%d) = %d, %t, want %d, %t", test.y, index, ok, test.index, test.ok)
		}
	}
}

func TestPinnedDetailTimelineFollowsLiveUpdate(t *testing.T) {
	events := []model.Event{{Kind: model.EventUser, Text: "Survey the crater"}}
	for index := range 8 {
		events = append(events, model.Event{Kind: model.EventThinking, Text: fmt.Sprintf("Observation %02d", index)})
	}
	root := &model.Session{ID: "lunar", Agent: model.AgentCodex, Path: "/workspace/crater/rollout.jsonl", Events: events}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 80, Height: 10}, tea.KeyMsg{Type: tea.KeyEnter}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	replacement := cloneSession(root)
	replacement.Events = append(replacement.Events, model.Event{Kind: model.EventAssistantText, Text: "Newest telemetry sample"})

	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	if !detail.viewport.AtBottom() || detail.focus != len(detail.focusables)-1 {
		t.Fatalf("tail-follow state = bottom %t focus %d/%d", detail.viewport.AtBottom(), detail.focus, len(detail.focusables)-1)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Newest telemetry sample") {
		t.Fatalf("tail-follow did not reveal newest event:\n%s", view)
	}
}

func TestCursorMovedOffTheNewestEventStopsTailFollowing(t *testing.T) {
	events := []model.Event{{Kind: model.EventUser, Text: "Survey the crater"}}
	for index := range 60 {
		events = append(events, model.Event{Kind: model.EventThinking, Text: fmt.Sprintf("Observation %02d", index)})
	}
	root := &model.Session{ID: "lunar", Agent: model.AgentCodex, Path: "/workspace/crater/rollout.jsonl", Events: events}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyUp},
		tea.KeyMsg{Type: tea.KeyUp},
		tea.KeyMsg{Type: tea.KeyUp},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	// Moving the cursor up inside the last screenful leaves the viewport at the
	// bottom, so tail-following must key off the cursor rather than the viewport.
	before := detailStateFromScreen(t, m.detail)
	offset, focusKey := before.viewport.YOffset, before.focusables[before.focus].key
	if !before.pinnedToBottom() {
		t.Fatalf("test setup viewport offset = %d, want a bottom-pinned viewport", offset)
	}
	replacement := cloneSession(root)
	replacement.Events = append(replacement.Events, model.Event{Kind: model.EventAssistantText, Text: "Newest telemetry sample"})

	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	after := detailStateFromScreen(t, m.detail)
	if after.viewport.YOffset != offset || after.focusables[after.focus].key != focusKey {
		t.Fatalf("live update moved from offset %d key %q to offset %d key %q", offset, focusKey, after.viewport.YOffset, after.focusables[after.focus].key)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(Model)
	if resumed := detailStateFromScreen(t, m.detail); !resumed.followingTail() {
		t.Fatalf("G did not resume tail-following: focus %d/%d offset %d", resumed.focus, len(resumed.focusables)-1, resumed.viewport.YOffset)
	}
}

func TestScrolledDetailTimelinePreservesPositionOnLiveUpdate(t *testing.T) {
	events := []model.Event{{Kind: model.EventUser, Text: "Survey the crater"}}
	for index := range 8 {
		events = append(events, model.Event{Kind: model.EventThinking, Text: fmt.Sprintf("Observation %02d", index)})
	}
	root := &model.Session{ID: "lunar", Agent: model.AgentCodex, Path: "/workspace/crater/rollout.jsonl", Events: events}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 10},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	before := detailStateFromScreen(t, m.detail)
	offset := before.viewport.YOffset
	focusKey := before.focusables[before.focus].key
	if offset == 0 || before.pinnedToBottom() {
		t.Fatalf("test setup offset = %d with bottom %t, want a non-bottom scrolled position", offset, before.pinnedToBottom())
	}
	replacement := cloneSession(root)
	replacement.Events = append(replacement.Events, model.Event{Kind: model.EventAssistantText, Text: "Newest telemetry sample"})

	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	after := detailStateFromScreen(t, m.detail)
	if after.viewport.YOffset != offset || after.focusables[after.focus].key != focusKey {
		t.Fatalf("scrolled update moved from offset %d key %q to offset %d key %q", offset, focusKey, after.viewport.YOffset, after.focusables[after.focus].key)
	}
}

func TestPinnedDetailTimelineStaysPinnedAcrossResize(t *testing.T) {
	root := &model.Session{
		ID: "lunar", Agent: model.AgentCodex, Path: "/workspace/crater/rollout.jsonl",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Survey the crater"},
			{Kind: model.EventAssistantText, Text: "Telemetry nominal"},
			{Kind: model.EventUser, Text: "Continue mapping"},
			{Kind: model.EventAssistantText, Text: "Ridge mapped cleanly"},
			{Kind: model.EventUser, Text: "Report status"},
			{Kind: model.EventAssistantText, Text: "Newest telemetry sample"},
		},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 100, Height: 10},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.WindowSizeMsg{Width: 28, Height: 10},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	if detail := detailStateFromScreen(t, m.detail); !detail.pinnedToBottom() {
		t.Fatalf("resized pinned timeline offset = %d/%d, want bottom anchor", detail.viewport.YOffset, len(detail.rendered))
	}

	replacement := cloneSession(root)
	replacement.Events = append(replacement.Events, model.Event{Kind: model.EventAssistantText, Text: "Final telemetry sample"})
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	view := ansi.Strip(m.View())
	// The newest reply appends to the last turn, whose header previews it at the very
	// bottom; tail-following holds if that preview stays on screen after the resize.
	if !detail.pinnedToBottom() || !strings.Contains(view, "Final") {
		t.Fatalf("post-resize tail-follow bottom=%t:\n%s", detail.pinnedToBottom(), view)
	}
}

func TestSpaceCollapsesDefaultExpandedTool(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the crater"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Output: "the ridge route is clear"}},
		{Kind: model.EventAssistantText, Text: "The ridge route is clear."},
	}}
	detail := newDetailState(session, 80, 20, newStyles())
	for index, item := range detail.focusables {
		if item.event.Kind == model.EventToolCall {
			detail.focus = index
			detail.selectedLine = item.line
			break
		}
	}
	toolKey := detail.focusables[detail.focus].key

	detail.update(tea.KeyMsg{Type: tea.KeySpace})

	if expanded, ok := detail.expanded[toolKey]; !ok || expanded || detail.isExpanded(toolKey) {
		t.Fatalf("tool override = %v, present %t, effective %t; want explicit collapse", expanded, ok, detail.isExpanded(toolKey))
	}
	for _, line := range detail.lines {
		if strings.Contains(line.text, "the ridge route is clear") {
			t.Fatalf("collapsed tool retained body row %q", line.text)
		}
	}
}

func TestTimelineGutterShowsRelativeEventTime(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	session := &model.Session{ID: "route", Agent: model.AgentClaude, Events: []model.Event{{
		Timestamp: now.Add(-5 * time.Minute), Kind: model.EventUser, Text: "Survey the crater",
	}}}
	detail := newDetailState(session, 80, 12, newStyles())
	detail.now = now
	detail.rebuild()

	line := strings.TrimRight(detail.rendered[0].text, " ")
	if !strings.HasPrefix(line, "›   5m ") {
		t.Fatalf("relative timeline row = %q, want fixed right-aligned 5m gutter", line)
	}
}

func TestTimelineGutterKeepsAbsoluteAndContinuationWidthsFixed(t *testing.T) {
	started := time.Date(2026, time.July, 19, 23, 55, 0, 0, time.UTC)
	eventTime := time.Date(2026, time.July, 20, 11, 55, 7, 0, time.UTC)
	session := &model.Session{
		ID: "route", Agent: model.AgentClaude, StartedAt: started, UpdatedAt: eventTime,
		Events: []model.Event{
			{Timestamp: eventTime, Kind: model.EventUser, Text: strings.Repeat("charted route ", 12)},
			{Kind: model.EventSystem, Text: "timestamp unavailable"},
		},
	}
	detail := newDetailState(session, 40, 16, newStyles())
	detail.absoluteTime = true
	detail.rebuild()

	gutterWidth := detail.timelineGutterWidth()
	if gutterWidth != detailDatedTimeWidth+detailTimeGapWidth {
		t.Fatalf("dated gutter width = %d, want %d", gutterWidth, detailDatedTimeWidth+detailTimeGapWidth)
	}
	first := detail.rendered[0].text
	if got := ansi.Cut(first, 2, 2+gutterWidth); got != "Jul 20 11:55:07 " {
		t.Fatalf("absolute gutter = %q, want dated seconds clock", got)
	}
	for index, row := range detail.rendered {
		if width := ansi.StringWidth(row.text); width != detail.viewport.Width {
			t.Errorf("rendered row %d width = %d, want %d", index, width, detail.viewport.Width)
		}
		if !row.first && strings.TrimSpace(ansi.Cut(row.text, 2, 2+gutterWidth)) != "" {
			t.Errorf("continuation row %d gutter is not blank: %q", index, ansi.Cut(row.text, 2, 2+gutterWidth))
		}
	}
	zeroStart := detail.firstRenderedRow(len(detail.lines) - 1)
	if got := strings.TrimSpace(ansi.Cut(detail.rendered[zeroStart].text, 2, 2+gutterWidth)); got != "" {
		t.Fatalf("zero-timestamp gutter = %q, want blank", got)
	}
}

func TestTimeToggleUpdatesListAndTimelineTogether(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	eventTime := now.Add(-5 * time.Minute)
	session := &model.Session{
		ID: "route", Agent: model.AgentClaude, Project: "starship", Title: "Chart route",
		StartedAt: now.Add(-time.Hour), UpdatedAt: eventTime,
		Events: []model.Event{{Timestamp: eventTime, Kind: model.EventUser, Text: "Survey the crater"}},
	}
	m := newModelWithClock([]*model.Session{session}, nil, func() time.Time { return now })
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "  5m ") || strings.Contains(view, "11:55:00") {
		t.Fatalf("relative detail did not start in age mode:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(timeFormatKey[0])}})
	m = updated.(Model)
	if view := ansi.Strip(m.View()); !m.absoluteTime || !strings.Contains(view, "11:55:00") || strings.Contains(view, "  5m ") {
		t.Fatalf("absolute detail did not switch with global flag:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(Model)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "TIME") || !strings.Contains(view, "11:55:00") {
		t.Fatalf("list did not share absolute time mode:\n%s", view)
	}
}

func TestTimeRefreshPreservesScrolledViewportAwayFromFocus(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	events := make([]model.Event, 20)
	for index := range events {
		events[index] = model.Event{Timestamp: now.Add(-time.Duration(index) * time.Minute), Kind: model.EventUser, Text: fmt.Sprintf("Survey sector %02d", index)}
	}
	m := newModelWithClock([]*model.Session{{ID: "route", Agent: model.AgentClaude, Events: events}}, nil, func() time.Time { return now })
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 40, Height: 10}, tea.KeyMsg{Type: tea.KeyEnter}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	detail := detailStateFromScreen(t, m.detail)
	detail.viewport.GotoTop()
	if detail.focus != len(detail.focusables)-1 || detail.viewport.YOffset != 0 {
		t.Fatalf("fixture focus=%d/%d offset=%d, want distant focus with top viewport", detail.focus, len(detail.focusables)-1, detail.viewport.YOffset)
	}

	updated, _ := m.Update(ageTickMsg{})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.viewport.YOffset != 0 || detail.rendered[0].detailIndex != 0 {
		t.Fatalf("age tick moved scrolled viewport to offset=%d detail=%d", detail.viewport.YOffset, detail.rendered[detail.viewport.YOffset].detailIndex)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = updated.(Model)
	detail = detailStateFromScreen(t, m.detail)
	if detail.viewport.YOffset != 0 || detail.rendered[0].detailIndex != 0 {
		t.Fatalf("time toggle moved scrolled viewport to offset=%d detail=%d", detail.viewport.YOffset, detail.rendered[detail.viewport.YOffset].detailIndex)
	}
}

func TestSelectedTimelineGutterKeepsMutedForeground(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	styleSet := newStyles(themes["default"])
	detail := newDetailState(&model.Session{ID: "route", Events: []model.Event{{Timestamp: now.Add(-5 * time.Minute), Kind: model.EventUser, Text: "Survey"}}}, 40, 12, styleSet)
	detail.now = now
	detail.rebuild()
	plain := detail.rendered[0].text
	gutterWidth := detail.timelineGutterWidth()
	mutedSelection := styleSet.selected.Foreground(styleSet.muted.GetForeground())
	wantGutter := mutedSelection.Render(ansi.Cut(plain, 2, 2+gutterWidth))

	if styled := detail.styleLine(plain, detail.lines[0], true, true); !strings.Contains(styled, wantGutter) {
		t.Fatalf("selected row did not retain muted gutter foreground: %q", styled)
	}
}

func TestToolExpansionRevealsDiffLinesWithSemanticRoles(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Update the route"},
		{Kind: model.EventToolCall, ToolName: "Edit", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Diff: "-old route\n unchanged\n+new route"}},
		{Kind: model.EventAssistantText, Text: "Route updated"},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	for index, item := range detail.focusables {
		if item.event.Kind == model.EventToolCall {
			detail.focus = index
			detail.selectedLine = item.line
			break
		}
	}
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
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
		for text, want := range wantRoles {
			if !strings.HasSuffix(line.text, text) {
				continue
			}
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

func TestExpandAllMarksEveryExpandableTimelineRow(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
		{Kind: model.EventThinking, Text: "Inspect the ridge"},
		{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-route", Detail: &model.ToolDetail{Input: "check-route", Output: "route clear"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	var expandableKeys []string
	for _, item := range detail.focusables {
		if item.expandable {
			expandableKeys = append(expandableKeys, item.key)
			detail.expanded[item.key] = false
		}
	}
	detail.rebuild()

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})

	for _, key := range expandableKeys {
		if !detail.isExpanded(key) {
			t.Errorf("expandable key %q stayed collapsed after expand-all", key)
		}
	}
	lines := make([]string, len(detail.lines))
	for index, line := range detail.lines {
		lines[index] = line.text
	}
	visible := strings.Join(lines, "\n")
	if !strings.Contains(visible, glyphExpanded+" "+glyphTool+" Bash") || !strings.Contains(visible, "route clear") {
		t.Errorf("expand-all did not reveal nested tool body:\n%s", visible)
	}
}

func TestCollapseAllClearsEveryExpandableTimelineRow(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
		{Kind: model.EventThinking, Text: "Inspect the ridge"},
		{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-route", Detail: &model.ToolDetail{Input: "check-route", Output: "route clear"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	var expandableKeys []string
	for _, item := range detail.focusables {
		if item.expandable {
			expandableKeys = append(expandableKeys, item.key)
		}
	}
	detail.expanded[expandableKeys[0]] = false
	detail.rebuild()

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})

	for _, key := range expandableKeys {
		if detail.isExpanded(key) {
			t.Errorf("expandable key %q stayed expanded after collapse-all", key)
		}
	}
	lines := make([]string, len(detail.lines))
	for index, line := range detail.lines {
		lines[index] = line.text
	}
	visible := strings.Join(lines, "\n")
	if !strings.Contains(visible, glyphCollapsed+" "+glyphTool) || strings.Contains(visible, "route clear") {
		t.Errorf("collapse-all left nested tool body visible:\n%s", visible)
	}
}

func TestRowArrivingAfterCollapseAllStaysCollapsed(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Path: "/workspace/lunar.jsonl", Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
	}}
	m := NewModel([]*model.Session{session}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'C'}}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	grown := cloneSession(session)
	grown.Events = append(grown.Events, model.Event{
		Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-ridge",
		Detail: &model.ToolDetail{Input: "check-ridge", Output: "ridge clear"},
	})
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{grown}})
	m = updated.(Model)

	detail := detailStateFromScreen(t, m.detail)
	lines := make([]string, len(detail.lines))
	for index, line := range detail.lines {
		lines[index] = line.text
	}
	visible := strings.Join(lines, "\n")
	if !strings.Contains(visible, glyphCollapsed+" "+glyphTool) || strings.Contains(visible, "ridge clear") {
		t.Errorf("row appended after collapse-all arrived expanded:\n%s", visible)
	}
}

func TestCollapseAllKeepsFocusWhereItWas(t *testing.T) {
	session := &model.Session{ID: "lunar", Path: "/workspace/event/session.jsonl", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the first route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/first.go", Detail: &model.ToolDetail{Output: "first route clear"}},
		{Kind: model.EventUser, Text: "Check the second route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/second.go", Detail: &model.ToolDetail{Output: "second route clear"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	wantKey := ""
	for index, item := range detail.focusables {
		if strings.HasSuffix(item.key, "/event/1") {
			detail.focus = index
			detail.selectedLine = item.line
			wantKey = item.key
			break
		}
	}
	if wantKey == "" {
		t.Fatalf("first tool row not found: %#v", detail.focusables)
	}

	// A flat timeline folds only a row's own body, so no focusable can disappear
	// under the cursor and focus stays put.
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})

	if got := detail.focusables[detail.focus].key; got != wantKey {
		t.Errorf("collapse-all focus = %q, want %q", got, wantKey)
	}
}

func TestTimelineUppercaseAgentJumpKeysAreNoops(t *testing.T) {
	first := &model.Session{ID: "scout", Agent: model.AgentClaude, Title: "Scout ridge"}
	second := &model.Session{ID: "mapper", Agent: model.AgentCodex, Title: "Map ridge"}
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the route"},
		{Kind: model.EventSubagent, Subagent: first},
		{Kind: model.EventAssistantText, Text: "Continue the survey"},
		{Kind: model.EventSubagent, Subagent: second},
	}}
	detail := newDetailState(session, 80, 14, newStyles())
	for _, test := range []struct {
		name  string
		key   rune
		focus int
	}{
		{name: "J", key: 'J', focus: 0},
		{name: "K", key: 'K', focus: len(detail.focusables) - 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail.focus = test.focus
			detail.selectedLine = detail.focusables[test.focus].line
			detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{test.key}})
			if detail.focus != test.focus {
				t.Errorf("%c changed Timeline focus from %d to %d", test.key, test.focus, detail.focus)
			}
		})
	}
}

func TestToolExpansionShowsReadableOutputUnderSecondaryLabel(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
		{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-route", Detail: &model.ToolDetail{Input: "check-route", Output: "route clear\ncommand complete"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())

	want := map[string]detailRole{"output:": detailSecondary, "route clear": detailRow, "command complete": detailRow}
	found := make(map[string]bool, len(want))
	for _, line := range detail.lines {
		text := strings.TrimSpace(line.text)
		role, ok := want[text]
		if !ok {
			continue
		}
		if line.role != role {
			t.Errorf("output line %q role = %v, want %v", line.text, line.role, role)
		}
		found[text] = true
	}
	for text := range want {
		if !found[text] {
			t.Errorf("expanded tool missing %q", text)
		}
	}
}

func TestExpandedToolHeaderStaysOnOneRowWhileBodyWraps(t *testing.T) {
	input := strings.Repeat("inspect the fictional ridge ", 8)
	output := strings.Repeat("fictional telemetry remains clear ", 8)
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
		{Kind: model.EventToolCall, ToolName: "update_plan", ToolInput: input, Detail: &model.ToolDetail{Input: input, Output: output}},
	}}
	detail := newDetailState(session, 32, 14, newStyles())

	headerIndex := -1
	for index, line := range detail.lines {
		if line.role == detailTool {
			headerIndex = index
			break
		}
	}
	if headerIndex < 0 {
		t.Fatal("expanded tool header not found")
	}
	if strings.Contains(detail.lines[headerIndex].text, "inspect the fictional ridge") {
		t.Fatalf("expanded tool header duplicated input before truncation: %q", detail.lines[headerIndex].text)
	}
	headerStart := detail.renderedStarts[headerIndex]
	headerEnd := len(detail.rendered)
	if headerIndex+1 < len(detail.renderedStarts) {
		headerEnd = detail.renderedStarts[headerIndex+1]
	}
	if rows := detail.rendered[headerStart:headerEnd]; len(rows) != 1 || !rows[0].first || !strings.Contains(rows[0].text, "update_plan") || strings.Contains(rows[0].text, input) || ansi.StringWidth(rows[0].text) != detail.viewport.Width {
		t.Fatalf("tool header rows = %#v, want one first viewport-width row without duplicated input", rows)
	}

	for _, body := range []string{input, output} {
		foundWrappedBody := false
		for index, line := range detail.lines {
			if !strings.Contains(line.text, body) {
				continue
			}
			start := detail.renderedStarts[index]
			end := len(detail.rendered)
			if index+1 < len(detail.renderedStarts) {
				end = detail.renderedStarts[index+1]
			}
			foundWrappedBody = end-start > 1
			break
		}
		if !foundWrappedBody {
			t.Fatalf("expanded body %q did not retain full text and wrap normally", body)
		}
	}
}

func TestToolHeaderPreviewReflectsVisibleInputBody(t *testing.T) {
	execEvent := model.Event{
		Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-route", ResultSummary: "exit 0", Duration: 1200 * time.Millisecond,
		Detail: &model.ToolDetail{Input: "check-route", Output: "route clear"},
	}
	detail := &detailState{expanded: make(map[string]bool), defaultExpanded: true}

	expanded := detail.toolEventLines(execEvent, 0, "exec")[0].text
	if strings.Contains(expanded, "(check-route)") || strings.Contains(expanded, "→ exit 0") || !strings.Contains(expanded, glyphTool+" Bash · 1.2s") {
		t.Errorf("expanded exec header = %q, want input preview and result summary omitted when body shown", expanded)
	}

	detail.expanded["exec"] = false
	collapsed := detail.toolEventLines(execEvent, 0, "exec")[0].text
	if !strings.Contains(collapsed, glyphTool+" Bash(check-route) → exit 0 · 1.2s") {
		t.Errorf("collapsed exec header = %q, want input preview", collapsed)
	}

	editEvent := model.Event{
		Kind: model.EventToolCall, ToolName: "Edit", ToolInput: "/workspace/route.go (+1 -1)",
		Detail: &model.ToolDetail{Input: "{\n  \"path\": \"/workspace/route.go\"\n}", Diff: "-old route\n+new route"},
	}
	expandedEdit := detail.toolEventLines(editEvent, 0, "edit")[0].text
	if !strings.Contains(expandedEdit, glyphTool+" Edit(/workspace/route.go (+1 -1))") {
		t.Errorf("expanded Edit header = %q, want non-echo preview preserved", expandedEdit)
	}
}

func TestFileToolWithoutRenderedBodyIsNotExpandable(t *testing.T) {
	event := model.Event{
		Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go",
		Detail: &model.ToolDetail{Input: "{\n  \"path\": \"/workspace/route.go\"\n}"},
	}
	detail := &detailState{expanded: make(map[string]bool), defaultExpanded: true}

	lines := detail.toolEventLines(event, 0, "read")

	if len(lines) != 1 || lines[0].expandable || !strings.Contains(lines[0].text, glyphTool+" Read(/workspace/route.go)") {
		t.Errorf("file tool without rendered body = %#v, want one non-expandable header with preview", lines)
	}
}

func TestOnlyShellToolsDisplayAsBash(t *testing.T) {
	for _, test := range []struct {
		event model.Event
		want  string
	}{
		{event: model.Event{ToolName: "exec_command", ToolInput: "go test"}, want: glyphTool + " Bash(go test)"},
		{event: model.Event{ToolName: "update_plan"}, want: glyphTool + " update_plan"},
		{event: model.Event{ToolName: "exec", ToolInput: "unresolved wrapper"}, want: glyphTool + " exec(unresolved wrapper)"},
	} {
		if got := toolLine(test.event, false); got != test.want {
			t.Errorf("toolLine(%#v) = %q, want %q", test.event, got, test.want)
		}
	}
}

func TestToolExpansionShowsReadableInputUnderSecondaryLabel(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Find the route"},
		{Kind: model.EventToolCall, ToolName: "Grep", ToolInput: "ridge", Detail: &model.ToolDetail{Input: "{\n  \"query\": \"ridge\"\n}"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())

	want := map[string]detailRole{"input:": detailSecondary, "{": detailRow, `"query": "ridge"`: detailRow, "}": detailRow}
	found := make(map[string]bool, len(want))
	for _, line := range detail.lines {
		text := strings.TrimSpace(line.text)
		role, ok := want[text]
		if !ok {
			continue
		}
		if line.role != role {
			t.Errorf("input line %q role = %v, want %v", line.text, line.role, role)
		}
		found[text] = true
	}
	for text := range want {
		if !found[text] {
			t.Errorf("expanded tool missing %q", text)
		}
	}
}

func TestToolExpansionShowsInputBeforeOutput(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentCodex, Events: []model.Event{
		{Kind: model.EventUser, Text: "Check the route"},
		{Kind: model.EventToolCall, ToolName: "exec", ToolInput: "make build", Detail: &model.ToolDetail{
			Input: "make build\nmake test", Output: "build ready",
		}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())

	inputIndex, outputIndex := -1, -1
	for index, line := range detail.lines {
		switch strings.TrimSpace(line.text) {
		case "input:":
			inputIndex = index
		case "output:":
			outputIndex = index
		}
	}
	if inputIndex < 0 || outputIndex < 0 || inputIndex >= outputIndex {
		t.Fatalf("section indexes input=%d output=%d, want input before output", inputIndex, outputIndex)
	}
}

func TestToolExpansionOmitsSingleLineFileInput(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Read the route"},
		{Kind: model.EventToolCall, ToolName: "Read", ToolInput: "/workspace/route.go", Detail: &model.ToolDetail{Input: "/workspace/route.go", Output: "route ready"}},
	}}
	detail := newDetailState(session, 80, 14, newStyles())

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
	detail.moveFocus(1)
	detail.moveFocus(1)
	detail.update(tea.KeyMsg{Type: tea.KeySpace})
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
		tea.KeyMsg{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	view := ansi.Strip(m.View())
	if len(m.detailStack) != 1 {
		t.Fatalf("tool open stack depth = %d, want 1:\n%s", len(m.detailStack), view)
	}
	for _, want := range []string{"starship › Plan route › Bash", "Input", "command-41", "+route-41", "Output", "result-41"} {
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
			for _, want := range []string{"Input", "fallback input", "Output", "fallback output"} {
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

func TestItemToolLinesShowInputAndDiffBeforeOutput(t *testing.T) {
	lines := itemEventLines(model.Event{
		Kind: model.EventToolCall, ToolName: "exec", ToolInput: "make build", Detail: &model.ToolDetail{
			Input: "make build\nmake test", Diff: "+build target", Output: "build ready",
		},
	}, model.AgentCodex)
	indexes := map[string]int{"Input": -1, "+build target": -1, "Output": -1}
	for index, line := range lines {
		if _, ok := indexes[line.text]; ok {
			indexes[line.text] = index
		}
	}
	if indexes["Input"] < 0 || indexes["+build target"] < 0 || indexes["Output"] < 0 ||
		indexes["Input"] >= indexes["Output"] || indexes["+build target"] >= indexes["Output"] {
		t.Fatalf("section indexes = %#v, want input and diff before output", indexes)
	}
}

func TestItemViewStartsWithPresentMetadataAndOmitsEmptyFields(t *testing.T) {
	event := model.Event{
		Kind: model.EventToolCall, Model: "gpt-5.6-sol", ToolName: "exec_command",
		CallID: "call-route", AgentID: "agent-scout", Duration: 1250 * time.Millisecond,
		ToolInput: "check route",
	}
	item := newItemView(event, model.AgentCodex, nil, 80, 18, newStyles())

	var lines []string
	for _, line := range item.lines {
		lines = append(lines, line.text)
	}
	for _, want := range []string{
		"Event",
		"kind      tool-call",
		"model     gpt-5.6-sol",
		"tool      exec_command",
		"call-id   call-route",
		"agent-id  agent-scout",
		"duration  1.2s",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("item metadata missing %q: %#v", want, lines)
		}
	}
	for _, omitted := range []string{"relative time", "absolute time"} {
		for _, line := range lines {
			if strings.HasPrefix(line, omitted) {
				t.Errorf("zero timestamp rendered %q", line)
			}
		}
	}
}

func TestItemViewUsesOrderedDetailSections(t *testing.T) {
	item := newItemView(model.Event{
		Kind: model.EventToolCall, ToolName: "exec_command",
		Detail: &model.ToolDetail{Input: "check route", Diff: "+route ready", Output: "checks passed"},
	}, model.AgentCodex, nil, 80, 18, newStyles())

	var headers []string
	for _, line := range item.lines {
		if line.role == detailHeader {
			headers = append(headers, line.text)
		}
	}
	want := []string{"Event", "Input", "Diff", "Output"}
	if !slices.Equal(headers, want) {
		t.Fatalf("item section headers = %#v, want %#v", headers, want)
	}
}

func TestItemViewSeparatesToolContentFromPrecedingSections(t *testing.T) {
	usage := model.Usage{Model: "model-a", InputTokens: 10}
	for _, test := range []struct {
		name  string
		usage *model.Usage
	}{
		{name: "event only"},
		{name: "request", usage: &usage},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := newItemView(model.Event{
				Kind: model.EventToolCall, ToolName: "exec_command", Usage: test.usage,
				ToolInput: "check route",
			}, model.AgentCodex, nil, 80, 18, newStyles())

			for index, line := range item.lines {
				if line.role != detailHeader || line.text != "Input" {
					continue
				}
				if index == 0 || item.lines[index-1].text != "" {
					t.Fatalf("Input section is not separated:\n%s", itemLinesText(item.lines))
				}
				return
			}
			t.Fatalf("Input section missing:\n%s", itemLinesText(item.lines))
		})
	}
}

func TestItemRequestSectionShowsAuditableRateArithmetic(t *testing.T) {
	usage := model.Usage{
		Model: "model-a", InputTokens: 1_200, OutputTokens: 20,
		CacheReadTokens: 1_000, InputIncludesCacheRead: true,
	}
	item := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "model-a", Usage: &usage, Priced: true,
		Cost: model.CostBreakdown{
			Input:     model.CostBuckets{{RatePerToken: 0.000005, Tokens: 200}},
			CacheRead: model.CostBuckets{{RatePerToken: 0.0000005, Tokens: 1_000}},
			Output:    model.CostBuckets{{RatePerToken: 0.000030, Tokens: 20}},
		},
	}, model.AgentCodex, nil, 80, 18, newStyles())

	text := ""
	for _, line := range item.lines {
		text += line.text + "\n"
	}
	for _, want := range []string{
		"Request",
		"tokens  ↑1000/0/200 ↓20 · ctx 1220",
		"input         200 × $5/Mtok   = $0.001",
		"cache read   1000 × $0.5/Mtok = $0.0005",
		"output         20 × $30/Mtok  = $0.0006",
		"total                         = $0.0021",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Request section missing %q:\n%s", want, text)
		}
	}
	equalsColumn := -1
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, " = $") {
			continue
		}
		if column := ansi.StringWidth(line[:strings.Index(line, "=")]); equalsColumn < 0 {
			equalsColumn = column
		} else if column != equalsColumn {
			t.Errorf("Request arithmetic equals columns = %d and %d:\n%s", equalsColumn, column, text)
		}
	}

	outputOnly := model.Event{Usage: &model.Usage{OutputTokens: 20}}
	if text := itemLinesText(itemRequestLines(outputOnly, model.AgentCodex)); !strings.Contains(text, "ctx 20") {
		t.Fatalf("output-only Request section omitted post-output context:\n%s", text)
	}
}

func TestItemRequestSectionShowsUnavailablePriceAndRequiresUsage(t *testing.T) {
	usage := model.Usage{Model: "future-model", InputTokens: 25}
	withUsage := newItemView(model.Event{
		Kind: model.EventUsage, Model: "future-model", Usage: &usage,
	}, model.AgentCodex, nil, 80, 14, newStyles())
	withoutUsage := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "future-model",
	}, model.AgentCodex, nil, 80, 14, newStyles())

	withText := itemLinesText(withUsage.lines)
	if !strings.Contains(withText, "Request") || !strings.Contains(withText, "price unavailable for future-model") {
		t.Fatalf("unpriced request did not name unavailable model:\n%s", withText)
	}
	if strings.Contains(itemLinesText(withoutUsage.lines), "Request") {
		t.Fatalf("event without usage rendered a Request section:\n%s", itemLinesText(withoutUsage.lines))
	}
}

func TestAggregateUsageItemExplainsScopeAndOmitsContext(t *testing.T) {
	usage := model.Usage{Model: "model-a", InputTokens: 30}
	item := newItemView(model.Event{
		Kind: model.EventUsage, Model: "model-a", Usage: &usage, UsageAggregate: true,
	}, model.AgentCodex, nil, 80, 14, newStyles())

	text := itemLinesText(item.lines)
	if !strings.Contains(text, "scope  session-level fallback usage, not one request") {
		t.Fatalf("aggregate usage did not explain its scope:\n%s", text)
	}
	if strings.Contains(text, "ctx ") {
		t.Fatalf("aggregate usage rendered per-request context:\n%s", text)
	}
}

func TestItemRequestNamesSubstitutionOnlyWhenApplied(t *testing.T) {
	usage := model.Usage{Model: "agents-a1", InputTokens: 10}
	breakdown := model.CostBreakdown{Input: model.CostBuckets{{RatePerToken: 0.000005, Tokens: 10}}}
	substituted := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "agents-a1", Usage: &usage,
		Cost: breakdown, Priced: true, CostEstimated: true, PricingModel: "gpt-5",
	}, model.AgentCodex, nil, 80, 14, newStyles())
	exactUsage := usage
	exactUsage.Model = "gpt-5"
	exact := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "gpt-5", Usage: &exactUsage,
		Cost: breakdown, Priced: true,
	}, model.AgentCodex, nil, 80, 14, newStyles())

	substitutedText := itemLinesText(substituted.lines)
	if !strings.Contains(substitutedText, "rate  priced as gpt-5 — no published rate for agents-a1") ||
		!strings.Contains(substitutedText, "~$0.00005") {
		t.Fatalf("substituted request did not disclose its rate:\n%s", substitutedText)
	}
	exactText := itemLinesText(exact.lines)
	if strings.Contains(exactText, "priced as") || strings.Contains(exactText, "published rate") || strings.Contains(exactText, "~$") {
		t.Fatalf("exact request contained estimate language:\n%s", exactText)
	}
}

func TestItemRequestWrapsWithinFortyColumns(t *testing.T) {
	usage := model.Usage{
		Model: "agents-a1", InputTokens: 450_000, OutputTokens: 20_000,
		CacheReadTokens: 300_000, InputIncludesCacheRead: true,
	}
	item := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "agents-a1", Usage: &usage,
		Cost: model.CostBreakdown{
			Input: model.CostBuckets{
				{RatePerToken: 0.000005, Tokens: 100_000},
				{RatePerToken: 0.000010, Tokens: 50_000, AboveThreshold: true},
			},
			CacheRead: model.CostBuckets{{RatePerToken: 0.0000005, Tokens: 300_000}},
			Output:    model.CostBuckets{{RatePerToken: 0.000030, Tokens: 20_000}},
		},
		Priced: true, CostEstimated: true, PricingModel: "gpt-5",
	}, model.AgentCodex, nil, 40, 18, newStyles())

	view := ansi.Strip(item.view())
	for number, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("40-column item line %d width = %d: %q", number+1, width, line)
		}
	}
	var wrappedBody strings.Builder
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
			wrappedBody.WriteString(strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│"), " "))
		}
	}
	if !strings.Contains(wrappedBody.String(), "rate  priced as gpt-5 — no published rate for agents-a1") {
		t.Fatalf("40-column wrapped view lost rate substitution:\n%s", view)
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	unwrapped := ansi.Strip(item.view())
	for number, line := range strings.Split(unwrapped, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("40-column unwrapped item line %d width = %d: %q", number+1, width, line)
		}
	}
	if !strings.Contains(unwrapped, "rate  priced as gpt-5") {
		t.Fatalf("40-column unwrapped view lost visible substitution prefix:\n%s", unwrapped)
	}
	if !strings.Contains(itemLinesText(item.lines), "rate  priced as gpt-5 — no published rate for agents-a1") {
		t.Fatalf("40-column source lines lost rate substitution:\n%s", itemLinesText(item.lines))
	}
}

func itemLinesText(lines []detailLine) string {
	text := make([]string, len(lines))
	for index, line := range lines {
		text[index] = line.text
	}
	return strings.Join(text, "\n")
}

func TestOpeningItemLoadsTerminalSafePrettyRawRecord(t *testing.T) {
	raw := []byte(`{"type":"system","message":{"content":"first\nsecond\u001b[2J` + "\u202e" + `unsafe"},"ready":true}`)
	session := &model.Session{
		ID: "route", Agent: model.AgentCodex,
		Events: []model.Event{{
			Kind: model.EventSystem, Text: "Route ready", RecordRef: writeRawRecord(t, raw),
		}},
	}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("opening an item with a record did not request raw")
	}
	if content := itemLinesText(m.detail.(*itemView).lines); !strings.HasSuffix(content, "Raw\nloading raw…") {
		t.Fatalf("opening item content did not end with the loading Raw section:\n%s", content)
	}

	updated, _ = m.Update(cmd())
	item := updated.(Model).detail.(*itemView)
	want := "{\n" +
		`  "type": "system",` + "\n" +
		`  "message": {` + "\n" +
		`    "content": "first\\nsecond\\u001b[2J\u202eunsafe"` + "\n" +
		"  },\n" +
		`  "ready": true` + "\n" +
		"}"
	if content := itemLinesText(item.lines); !strings.HasSuffix(content, "Raw\n"+want) {
		t.Fatalf("loaded item content did not end with terminal-safe pretty raw:\n%s", content)
	}
	if strings.Contains(itemLinesText(item.lines), "\u202e") {
		t.Fatal("loaded item content retained a terminal-unsafe format rune")
	}
}

func TestItemViewShowsBothTimesAndLoadsRawRecordAsynchronously(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"type":"user","message":{"content":"Inspect the route"}}`)
	event := model.Event{
		Timestamp: now.Add(-5 * time.Minute), Kind: model.EventUser, Text: "Inspect the route",
		RecordRef: writeRawRecord(t, raw),
	}
	item := newItemView(event, model.AgentClaude, nil, 80, 18, newStyles())
	item.setNow(now)

	plain := ansi.Strip(item.view())
	for _, want := range []string{"relative time  5m", "absolute time  Jan 2 11:55:00"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("item view missing %q before raw load:\n%s", want, plain)
		}
	}

	cmd := item.requestRaw()
	if cmd == nil || !strings.Contains(ansi.Strip(item.view()), "loading raw…") {
		t.Fatalf("raw request did not start an asynchronous load:\n%s", ansi.Strip(item.view()))
	}
	item.update(cmd())
	plain = ansi.Strip(item.view())
	for _, want := range []string{"Raw", `"type": "user"`, `"message": {`, `"content": "Inspect the route"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("item view missing %q after raw load:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "R raw") || strings.Contains(plain, "R hide raw") {
		t.Fatalf("item view retained a raw toggle hint:\n%s", plain)
	}
}

func TestItemWithoutReadableRecordOmitsRawSection(t *testing.T) {
	for name, ref := range map[string]model.RecordRef{
		"aggregate":        {},
		"unmatched offset": {Path: "/fictional/session.jsonl", Offset: 42},
	} {
		t.Run(name, func(t *testing.T) {
			item := newItemView(model.Event{
				Kind: model.EventUsage, Text: "session usage", RecordRef: ref,
			}, model.AgentCodex, nil, 80, 12, newStyles())

			if content := itemLinesText(item.lines); strings.Contains(content, "\nRaw\n") {
				t.Fatalf("usage item rendered unavailable Raw section:\n%s", content)
			}
		})
	}
}

func TestItemRawRequestReusesInFlightRead(t *testing.T) {
	raw := []byte(`{"status":"ready"}`)
	item := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, raw),
	}, model.AgentCodex, nil, 80, 12, newStyles())

	cmd := item.requestRaw()
	if cmd == nil || !item.rawLoading {
		t.Fatal("first raw request did not schedule one visible load")
	}
	if duplicate := item.requestRaw(); duplicate != nil {
		t.Fatal("second raw request duplicated the in-flight load")
	}
	item.update(cmd())
	if item.rawLoading || !bytes.Equal(item.raw, raw) {
		t.Fatal("raw completion did not cache the complete record")
	}
	if duplicate := item.requestRaw(); duplicate != nil {
		t.Fatal("loaded raw request scheduled another read")
	}
	if !strings.Contains(itemLinesText(item.lines), `"status": "ready"`) {
		t.Fatal("cached raw record was not visible after loading")
	}
}

func TestItemViewIgnoresRawKey(t *testing.T) {
	event := model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, []byte(`{"status":"ready"}`)),
	}
	m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentCodex, Events: []model.Event{event}}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	before := m.View()

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("R returned a command in the item view")
	}
	if after := m.View(); after != before {
		t.Fatalf("R changed the item view:\nbefore:\n%s\nafter:\n%s", ansi.Strip(before), ansi.Strip(after))
	}
}

func TestItemConstructorDoesNotClaimUnscheduledRawLoad(t *testing.T) {
	event := model.Event{
		Kind:      model.EventSystem,
		RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: 24},
	}
	item := newItemViewWithState(event, model.AgentCodex, nil, 80, 12, newStyles(), time.Time{}, true)

	if item.rawLoading {
		t.Fatal("state-preserving constructor marked raw loading without returning a command")
	}
	if content := itemLinesText(item.lines); !strings.HasSuffix(content, "\nRaw\n") {
		t.Fatalf("valid record reference did not render an unconditional Raw section:\n%s", content)
	}
	if cmd := item.requestRaw(); cmd == nil || !item.rawLoading {
		t.Fatal("requestRaw did not pair the loading state with a command")
	}
	if cmd := item.requestRaw(); cmd != nil {
		t.Fatal("requestRaw scheduled a duplicate in-flight load")
	}
}

func TestItemViewReportsRawReadFailuresWithoutFallback(t *testing.T) {
	tests := []struct {
		name    string
		replace func(t *testing.T, path string)
	}{
		{
			name: "missing file",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "non-regular file",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{"status":"authoritative"}`)
			ref := writeRawRecord(t, raw)
			test.replace(t, ref.Path)
			item := newItemView(model.Event{
				Kind: model.EventSystem, Text: "derived fallback", RecordRef: ref,
			}, model.AgentCodex, nil, 80, 12, newStyles())

			cmd := item.requestRaw()
			if cmd == nil {
				t.Fatal("raw request did not schedule the failing read")
			}
			item.update(cmd())
			texts := make([]string, 0, len(item.lines))
			for _, line := range item.lines {
				texts = append(texts, line.text)
			}
			content := strings.Join(texts, "\n")
			rawIndex := slices.Index(texts, "Raw")
			if rawIndex < 0 || rawIndex+1 >= len(texts) || texts[rawIndex+1] != "raw unavailable: read failed" {
				t.Fatalf("item content did not name the read failure:\n%s", content)
			}
			if strings.Contains(content, string(raw)) {
				t.Fatalf("item content presented fallback raw after a read failure:\n%s", content)
			}
		})
	}
}

func TestItemViewReportsChangedRawWithoutFallback(t *testing.T) {
	original := []byte(`{"message":"original"}`)
	ref := writeRawRecord(t, original)
	changed := []byte(`{"message":"modified"}`)
	if err := os.WriteFile(ref.Path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	item := newItemView(model.Event{Kind: model.EventSystem, RecordRef: ref}, model.AgentClaude, nil, 80, 12, newStyles())

	cmd := item.requestRaw()
	item.update(cmd())

	content := make([]string, 0, len(item.lines))
	for _, line := range item.lines {
		content = append(content, line.text)
	}
	rendered := strings.Join(content, "\n")
	if !strings.Contains(rendered, "raw unavailable: source changed") || strings.Contains(rendered, "original") || strings.Contains(rendered, "modified") {
		t.Fatalf("changed raw rendered a fallback body:\n%s", rendered)
	}
}

func TestCurrentItemDiscardsPreviousItemsRawResult(t *testing.T) {
	rawA := []byte(`{"item":"a"}`)
	itemA := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, rawA),
	}, model.AgentCodex, nil, 80, 12, newStyles())
	itemA.generation = 1
	cmdA := itemA.requestRaw()

	rawB := []byte(`{"item":"b"}`)
	itemB := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, rawB),
	}, model.AgentCodex, nil, 80, 12, newStyles())
	itemB.generation = 2
	cmdB := itemB.requestRaw()
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = itemB

	updated, _ := m.Update(cmdA())
	m = updated.(Model)
	current := m.detail.(*itemView)
	if !current.rawLoading || len(current.raw) != 0 {
		t.Fatalf("current item accepted generation 1 while generation %d was loading", current.generation)
	}

	updated, _ = m.Update(cmdB())
	current = updated.(Model).detail.(*itemView)
	if current.rawLoading || !bytes.Equal(current.raw, rawB) {
		t.Fatalf("current item raw = %q loading=%t, want item B", current.raw, current.rawLoading)
	}
}

func TestFullFidelityCompactionSummaryAcrossParserTimelineAndItem(t *testing.T) {
	summary := "first-" + strings.Repeat("界", 5_000) + "-last"
	line := `{"type":"user","timestamp":"2026-01-02T03:04:05Z","isCompactSummary":true,"message":{"content":` + strconv.Quote(summary) + `}}`
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}
	parser := claude.NewParser(cost.NewCalculator(cost.Table{}))

	if err := parser.LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 || session.Events[0].Text != summary {
		t.Fatalf("parsed compaction summary has %d runes, want %d", len([]rune(session.Events[0].Text)), len([]rune(summary)))
	}
	bounded := string([]rune(summary)[:detailPreviewRuneCap-1]) + "…"
	detail := newDetailState(session, 80, 12, newStyles())
	foundBounded := false
	for _, timelineLine := range detail.lines {
		foundBounded = foundBounded || strings.Contains(timelineLine.text, bounded)
		if strings.Contains(timelineLine.text, summary) {
			t.Fatal("timeline rendered the unbounded compaction summary")
		}
	}
	if !foundBounded {
		t.Fatal("timeline did not render the 4096-rune trailing-ellipsis form")
	}

	item := newItemView(session.Events[0], model.AgentClaude, nil, 80, 12, newStyles())
	foundFull := false
	for _, itemLine := range item.lines {
		foundFull = foundFull || itemLine.text == summary
	}
	if !foundFull {
		t.Fatal("item view did not retain the complete compaction summary")
	}
	cmd := item.requestRaw()
	item.update(cmd())
	if string(item.raw) != line {
		t.Fatalf("raw item bytes differ from fixture line: got %d bytes, want %d", len(item.raw), len(line))
	}

	changed := []byte(line)
	changed[len(changed)-2] ^= 1
	if err := os.WriteFile(path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	changedItem := newItemView(session.Events[0], model.AgentClaude, nil, 80, 12, newStyles())
	cmd = changedItem.requestRaw()
	changedItem.update(cmd())
	content := make([]string, 0, len(changedItem.lines))
	for _, itemLine := range changedItem.lines {
		content = append(content, itemLine.text)
	}
	if got := strings.Join(content, "\n"); !strings.Contains(got, "raw unavailable: source changed") || strings.Contains(got, line) {
		t.Fatalf("rewritten fixture rendered stale raw content:\n%s", got)
	}
}

func TestItemViewDiscardsRawResultAfterBackNavigation(t *testing.T) {
	raw := []byte(`{"message":"source"}`)
	event := model.Event{Kind: model.EventSystem, Text: "Derived", RecordRef: writeRawRecord(t, raw)}
	session := &model.Session{ID: "route", Agent: model.AgentClaude, Events: []model.Event{event}}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("opening the item did not return a load command")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	if _, itemOpen := m.detail.(*itemView); itemOpen {
		t.Fatal("late raw result reopened or mutated the item after back navigation")
	}
}

func TestItemRawPanelKeepsEncryptedTokensSourceExact(t *testing.T) {
	token := "gAAAA" + strings.Repeat("A", 70)
	raw := []byte(`{"secret":` + strconv.Quote(token) + `}`)
	item := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, raw),
	}, model.AgentCodex, nil, 80, 12, newStyles())
	cmd := item.requestRaw()
	item.update(cmd())

	plain := ansi.Strip(item.view())
	if string(item.raw) != string(raw) || strings.Contains(plain, "<encrypted 75 chars>") {
		t.Fatalf("raw panel did not keep the encrypted token source-exact:\n%s", plain)
	}
}

func TestItemRawPanelSanitizesMalformedTerminalText(t *testing.T) {
	raw := []byte("malformed\x1b[2J\u202erecord")
	item := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, raw),
	}, model.AgentCodex, nil, 40, 10, newStyles())
	cmd := item.requestRaw()
	item.update(cmd())

	view := item.view()
	lines := item.lines
	if len(lines) < 2 || lines[len(lines)-2].text != "Raw" || lines[len(lines)-1].text != `malformed\x1b[2J\u202erecord` {
		t.Fatalf("malformed raw did not render as one sanitized line: %#v", lines)
	}
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\u202e") || !strings.Contains(ansi.Strip(view), `malformed\x1b[2J\u202erecord`) {
		t.Fatalf("raw panel did not sanitize terminal text:\n%q", view)
	}
}

func TestTerminalSafeRawRecordEscapesOnlyUnsafeCharacters(t *testing.T) {
	raw := append([]byte(`safe \ path "界"`), '\t', '\n', '\r', 0, 0x1b, 0x7f)
	raw = append(raw, []byte("\u202e")...)
	raw = append(raw, 0xff)
	want := `safe \ path "界"\t\n\r\x00\x1b\x7f\u202e\xff`

	if got := terminalSafeRawRecord(raw); got != want {
		t.Fatalf("terminalSafeRawRecord() = %q, want %q", got, want)
	}
}

func TestTerminalSafeRawRecordDisambiguatesEscapedText(t *testing.T) {
	tests := []struct {
		name    string
		unsafe  []byte
		literal []byte
	}{
		{name: "tab", unsafe: []byte{'\t'}, literal: []byte(`\t`)},
		{name: "newline", unsafe: []byte{'\n'}, literal: []byte(`\n`)},
		{name: "escape", unsafe: []byte{0x1b}, literal: []byte(`\x1b`)},
		{name: "format rune", unsafe: []byte("\u202e"), literal: []byte(`\u202e`)},
		{name: "invalid UTF-8", unsafe: []byte{0xff}, literal: []byte(`\xff`)},
		{name: "backslash then tab", unsafe: []byte{'\\', '\t'}, literal: []byte(`\t`)},
		{name: "backslash then newline", unsafe: []byte{'\\', '\n'}, literal: []byte(`\n`)},
		{name: "backslash then escape", unsafe: []byte{'\\', 0x1b}, literal: []byte(`\x1b`)},
		{name: "backslash then format rune", unsafe: append([]byte{'\\'}, []byte("\u202e")...), literal: []byte(`\u202e`)},
		{name: "backslash then invalid UTF-8", unsafe: []byte{'\\', 0xff}, literal: []byte(`\xff`)},
		{name: "repeated backslash then newline", unsafe: []byte{'\\', '\\', '\n'}, literal: []byte(`\\n`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			escapedUnsafe := terminalSafeRawRecord(test.unsafe)
			escapedLiteral := terminalSafeRawRecord(test.literal)
			if escapedUnsafe == escapedLiteral {
				t.Fatalf("unsafe %q and literal %q both rendered as %q", test.unsafe, test.literal, escapedUnsafe)
			}
		})
	}
}

func TestItemToolLinesKeepResultSummarySeparateFromFullOutput(t *testing.T) {
	lines := itemEventLines(model.Event{
		Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check route",
		ResultSummary: "exit 0", Detail: &model.ToolDetail{Output: "route ready"},
	}, model.AgentCodex)
	var texts []string
	for _, line := range lines {
		texts = append(texts, line.text)
	}
	for _, want := range []string{"Output", "route ready", "Result summary", "exit 0"} {
		if !slices.Contains(texts, want) {
			t.Errorf("tool item missing %q: %#v", want, texts)
		}
	}
}

func TestItemToolBodyRendersReadableContent(t *testing.T) {
	lines := itemEventLines(model.Event{
		Kind: model.EventToolCall, ToolName: "exec", ToolInput: "make build",
		Detail: &model.ToolDetail{Input: "make build", Output: "build ready"},
	}, model.AgentCodex)
	roles := make(map[string]detailRole, len(lines))
	for _, line := range lines {
		roles[line.text] = line.role
	}
	// Section titles use the Info-tab header role; bodies stay readable.
	for text, want := range map[string]detailRole{
		"Input": detailHeader, "make build": detailRow,
		"Output": detailHeader, "build ready": detailRow,
	} {
		if roles[text] != want {
			t.Errorf("item body %q role = %v, want %v", text, roles[text], want)
		}
	}
}

func TestItemTextRolesMatchTimelinePromptSemantics(t *testing.T) {
	for _, test := range []struct {
		kind model.EventKind
		want detailRole
	}{
		{kind: model.EventUser, want: detailUserPrompt},
		{kind: model.EventAssistantText, want: detailRow},
		{kind: model.EventThinking, want: detailSecondary},
		{kind: model.EventSystem, want: detailSystemPrompt},
		{kind: model.EventCompact, want: detailSystemPrompt},
		{kind: model.EventUsage, want: detailSystemPrompt},
	} {
		lines := itemEventLines(model.Event{Kind: test.kind, Text: "ordinary prose"}, model.AgentCodex)
		if len(lines) != 1 || lines[0].role != test.want {
			t.Errorf("item %s roles = %#v, want one role %v", test.kind, lines, test.want)
		}
	}
}

func TestItemLabelsMatchTimelineRoles(t *testing.T) {
	for _, test := range []struct {
		name      string
		event     model.Event
		wantLabel string
		wantRole  detailRole
	}{
		{name: "harness", event: model.Event{Kind: model.EventUser, Text: "Injected instructions", Harness: true}, wantLabel: "Harness", wantRole: detailSystemPrompt},
		{name: "human", event: model.Event{Kind: model.EventUser, Text: "Survey the crater"}, wantLabel: "User", wantRole: detailUserPrompt},
		{name: "usage", event: model.Event{Kind: model.EventUsage, Text: "unattributed usage"}, wantLabel: "Usage", wantRole: detailSystemPrompt},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := itemLabel(test.event, model.AgentClaude); got != test.wantLabel {
				t.Errorf("itemLabel() = %q, want %q", got, test.wantLabel)
			}
			lines := itemEventLines(test.event, model.AgentClaude)
			if len(lines) != 1 || lines[0].role != test.wantRole {
				t.Errorf("itemEventLines() = %#v, want one line with role %v", lines, test.wantRole)
			}
		})
	}
}

func TestItemContentSectionTitlesReflectKind(t *testing.T) {
	tests := []struct {
		event model.Event
		want  string
	}{
		{event: model.Event{Kind: model.EventAssistantText}, want: "Message"},
		{event: model.Event{Kind: model.EventUser}, want: "Prompt"},
		{event: model.Event{Kind: model.EventUser, Harness: true}, want: "Harness"},
		{event: model.Event{Kind: model.EventThinking}, want: "Thinking"},
		{event: model.Event{Kind: model.EventAdvisor}, want: "Advisor"},
		{event: model.Event{Kind: model.EventSystem}, want: "System"},
		{event: model.Event{Kind: model.EventCompact}, want: "Compact"},
		{event: model.Event{Kind: model.EventUsage}, want: "Usage"},
	}
	for _, test := range tests {
		test.event.Text = "section body"
		item := newItemView(test.event, model.AgentCodex, nil, 80, 12, newStyles())
		var headers []string
		for _, line := range item.lines {
			if line.role == detailHeader {
				headers = append(headers, line.text)
			}
		}
		if !slices.Contains(headers, test.want) {
			t.Errorf("%s item headers = %#v, want %q", test.event.Kind, headers, test.want)
		}
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
	if item.viewport.YOffset != 0 || !strings.Contains(ansi.Strip(item.view()), "Event") {
		t.Fatalf("g did not restore the first item section at offset %d:\n%s", item.viewport.YOffset, ansi.Strip(item.view()))
	}
}

func TestItemViewWrapToggleRebuildsFlatPlainRows(t *testing.T) {
	text := strings.Repeat("charted route ", 12)
	item := newItemView(model.Event{Kind: model.EventAssistantText, Text: text}, model.AgentClaude, nil, 24, 8, newStyles())
	if !item.wrap || len(item.rendered) <= 1 {
		t.Fatalf("default item state = wrap %t rows %d, want wrapped rows", item.wrap, len(item.rendered))
	}
	for index, row := range item.rendered {
		if strings.Contains(row.text, "\x1b") || ansi.StringWidth(row.text) != item.viewport.Width {
			t.Fatalf("wrapped row %d = %q width %d, want plain width %d", index, row.text, ansi.StringWidth(row.text), item.viewport.Width)
		}
	}

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if item.wrap || len(item.rendered) != len(item.lines) {
		t.Fatalf("first wrap toggle state = wrap %t rows %d, want one row per source line (%d)", item.wrap, len(item.rendered), len(item.lines))
	}

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !item.wrap || len(item.rendered) <= 1 {
		t.Fatalf("second wrap toggle state = wrap %t rows %d, want wrapped rows", item.wrap, len(item.rendered))
	}
}

func TestDetailAndItemWrapByDefault(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "lunar", Agent: model.AgentCodex}, 80, 12, newStyles())
	item := newItemView(model.Event{Kind: model.EventAssistantText, Text: "Route ready"}, model.AgentCodex, nil, 80, 12, newStyles())

	if !detail.wrap || !item.wrap {
		t.Fatalf("default wrap = detail %t, item %t; want both enabled", detail.wrap, item.wrap)
	}
}

func TestLoadedItemModelUpdateSharesImmutableRawRecord(t *testing.T) {
	item := newItemView(model.Event{Kind: model.EventSystem}, model.AgentCodex, nil, 80, 12, newStyles())
	item.raw = []byte(`{"route":"ready"}`)
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item

	updated, _ := m.Update(struct{}{})
	cloned := updated.(Model).detail.(*itemView)
	if &cloned.raw[0] != &item.raw[0] {
		t.Fatal("Model.Update copied an immutable loaded raw record")
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

func BenchmarkItemRenderer16MiB(b *testing.B) {
	const envelope = `{"payload":""}`
	raw := []byte(`{"payload":"` + strings.Repeat("x", jsonl.MaxLineBytes-len(envelope)) + `"}`)
	item := newItemView(model.Event{
		Kind:      model.EventSystem,
		RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: int64(len(raw)), Digest: sha256.Sum256(raw)},
	}, model.AgentCodex, nil, 120, 24, newStyles())
	item.raw = raw
	item.rawLines = rawDetailLines(terminalSafePrettyRawRecordLines(raw), item.agent)
	item.rebuildLines()
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		item.rebuildLines()
		item.rebuild()
	}
}

func BenchmarkLoadedItemModelUpdate16MiB(b *testing.B) {
	raw := make([]byte, 16<<20)
	item := newItemView(model.Event{Kind: model.EventSystem}, model.AgentCodex, nil, 120, 24, newStyles())
	item.raw = raw
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		updated, _ := m.Update(struct{}{})
		m = updated.(Model)
	}
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
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	detail.moveFocus(1)
	if !strings.Contains(detail.focusables[detail.focus].key, "/event/1") {
		t.Fatalf("first expanded focus = %#v, want thinking row", detail.focusables[detail.focus])
	}
	detail.moveFocus(1)
	if !strings.Contains(detail.focusables[detail.focus].key, "/event/2") {
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
	if view := ansi.Strip(detail.view()); !strings.Contains(view, strings.TrimRight(detail.rendered[detail.firstRenderedRow(detail.selectedLine)].text, " ")) {
		t.Fatalf("G did not keep the last focusable visible:\n%s", view)
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if detail.focus != 0 {
		t.Fatalf("g focus = %d, want 0", detail.focus)
	}
	if want := detail.focusables[detail.focus].line; detail.selectedLine != want || detail.viewport.YOffset != 0 {
		t.Fatalf("g selection line=%d offset=%d, want line %d at top", detail.selectedLine, detail.viewport.YOffset, want)
	}
	if view := ansi.Strip(detail.view()); !strings.Contains(view, strings.TrimRight(detail.rendered[detail.firstRenderedRow(detail.selectedLine)].text, " ")) {
		t.Fatalf("g did not keep the first focusable visible:\n%s", view)
	}
}

func TestDetailNavigationMovesTheVisibleSelectionMarker(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Survey the northern ridge"},
		{Kind: model.EventAssistantText, Text: "Northern ridge is clear"},
		{Kind: model.EventUser, Text: "Survey the southern ridge"},
		{Kind: model.EventAssistantText, Text: "Southern ridge is clear"},
	}}
	detail := newDetailState(session, 80, 20, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	oldText := detail.lines[detail.selectedLine].text
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	newText := detail.lines[detail.selectedLine].text
	view := ansi.Strip(detail.view())

	gutter := strings.Repeat(" ", detail.timelineGutterWidth())
	if strings.Contains(view, "› "+gutter+oldText) {
		t.Fatalf("navigation left the visible marker on %q:\n%s", oldText, view)
	}
	if !strings.Contains(view, "› "+gutter+newText) {
		t.Fatalf("navigation did not move the visible marker to %q:\n%s", newText, view)
	}
}

func TestWrapToggleWrapsBodyRowsAndHighlightsSelectedHead(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	// Metrics-bearing heads are nowrap and stay one row, so wrapping happens on the
	// body. A long single-line reply folds open by default; its full text renders as
	// body rows that wrap or truncate with the wrap toggle, while the focused head
	// stays a single highlighted row.
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{{
		Kind: model.EventAssistantText, Text: strings.Repeat("charted route ", 12),
	}}}
	detail := newDetailState(session, 28, 40, newStyles(Theme{Name: "mono"}))
	bodyRows := func() int {
		for index, line := range detail.lines {
			if line.key != "" || !strings.Contains(line.text, "charted") {
				continue
			}
			rows := 0
			for _, row := range detail.rendered {
				if row.detailIndex == index {
					rows++
				}
			}
			return rows
		}
		return 0
	}
	selectedRows := func() int {
		rows := 0
		for _, row := range detail.rendered {
			if row.detailIndex == detail.selectedLine {
				rows++
			}
		}
		return rows
	}

	if selectedRows() != 1 {
		t.Fatalf("selected head spans %d rows, want a single nowrap row", selectedRows())
	}
	if highlighted := strings.Count(detail.view(), "\x1b[7m"); highlighted != 1 {
		t.Fatalf("highlighted rows = %d, want the single selected head", highlighted)
	}
	if !detail.wrap || bodyRows() < 3 {
		t.Fatalf("default wrapped body = %v with %d rows, want multiple flat rows", detail.wrap, bodyRows())
	}

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if detail.wrap || bodyRows() != 1 {
		t.Fatalf("first toggle left wrap=%v with %d body rows, want truncation", detail.wrap, bodyRows())
	}

	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !detail.wrap || bodyRows() < 3 {
		t.Fatalf("second toggle left wrap=%v with %d body rows, want wrapped rows", detail.wrap, bodyRows())
	}
	if selectedRows() != 1 {
		t.Fatalf("selected head spans %d rows after toggles, want a single nowrap row", selectedRows())
	}
}

func TestWrappedEdgeNavigationUsesFlatRowOffsets(t *testing.T) {
	// The reply folds open into wrapped body rows; navigation counts rendered rows,
	// not detail lines. G lands on the reply head at the bottom, g on the prompt top.
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: "Start the survey"},
		{Kind: model.EventAssistantText, Text: strings.Repeat("charted southern route ", 4)},
	}}
	detail := newDetailState(session, 28, 16, newStyles())
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})

	selectedVisible := false
	for rowIndex := detail.viewport.YOffset; rowIndex < min(len(detail.rendered), detail.viewport.YOffset+detail.viewport.Height); rowIndex++ {
		selectedVisible = selectedVisible || detail.rendered[rowIndex].detailIndex == detail.selectedLine
	}
	if !selectedVisible || !detail.pinnedToBottom() {
		t.Fatalf("G selected line %d visibility=%t bottom=%t", detail.selectedLine, selectedVisible, detail.pinnedToBottom())
	}
	detail.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if selectedRow := detail.firstRenderedRow(detail.selectedLine); selectedRow != 0 || detail.viewport.YOffset != 0 {
		t.Fatalf("g selected flat row %d at offset %d, want top row", selectedRow, detail.viewport.YOffset)
	}
}

func TestCloneSessionRebindsSubagentEvents(t *testing.T) {
	child := &model.Session{ID: "scout"}
	parent := &model.Session{
		ID: "root", Subagents: []*model.Session{child}, Events: []model.Event{{Kind: model.EventSubagent, Subagent: child}},
		ModelCostBreakdowns: map[string]model.CostBreakdown{"model-a": {Input: model.CostBuckets{{RatePerToken: 1, Tokens: 2}}}},
	}
	cloned := cloneSession(parent)

	if cloned.Subagents[0] == child || cloned.Events[0].Subagent != cloned.Subagents[0] {
		t.Fatalf("cloned graph retained original link: %#v", cloned)
	}
	breakdown := cloned.ModelCostBreakdowns["model-a"]
	breakdown.Input[0].Tokens = 9
	if parent.ModelCostBreakdowns["model-a"].Input[0].Tokens != 2 {
		t.Fatal("cloned cost buckets retained original backing slice")
	}
}
