package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

type refreshTestSource struct {
	session *model.Session
}

func TestEmptyListExplainsWhereToFindSessions(t *testing.T) {
	view := NewModel(nil, nil).View()
	for _, text := range []string{"No sessions found", "~/.claude", "~/.codex", "press ? for keys"} {
		if !strings.Contains(view, text) {
			t.Fatalf("empty list missing %q:\n%s", text, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("empty list line width = %d, want <= 80: %q", width, line)
		}
	}
}

func TestHumanTokensUsesCompactTiers(t *testing.T) {
	tests := []struct {
		name   string
		tokens int64
		want   string
	}{
		{name: "plain", tokens: 999, want: "999"},
		{name: "thousands", tokens: 88_000, want: "88k"},
		{name: "millions", tokens: 1_200_000, want: "1.2M"},
		{name: "billions", tokens: 1_021_000_000, want: "1.0B"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := humanTokens(test.tokens); got != test.want {
				t.Fatalf("humanTokens(%d) = %q, want %q", test.tokens, got, test.want)
			}
		})
	}
}

func TestBillionTokenRowKeepsTwoDigitSubagentIndicator(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	subagents := make([]*model.Session, 16)
	for index := range subagents {
		subagents[index] = &model.Session{ID: fmt.Sprintf("worker-%d", index)}
	}
	session := &model.Session{
		ID:        "lunar",
		Agent:     model.AgentCodex,
		Usage:     []model.Usage{{InputTokens: 1_021_000_000}},
		Subagents: subagents,
	}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})

	if view := ansi.Strip(updated.(Model).View()); !strings.Contains(view, "1.0B ⑃16") {
		t.Fatalf("billion-token row truncated subagent indicator:\n%s", view)
	}
}

func TestNumericCellsFitStandardColumns(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	subagents := func(count int) []*model.Session {
		result := make([]*model.Session, count)
		for index := range result {
			result[index] = &model.Session{ID: fmt.Sprintf("worker-%d", index)}
		}
		return result
	}
	tests := []struct {
		name    string
		session *model.Session
	}{
		{name: "representative", session: &model.Session{UpdatedAt: now.Add(-1_000 * 24 * time.Hour), Messages: 1_000_000, Usage: []model.Usage{{InputTokens: 1_021_000_000}}, Cost: model.Cost{USD: 1_000_000_000, Estimated: true}}},
		{name: "rounding boundary", session: &model.Session{Messages: 999_999, Usage: []model.Usage{{InputTokens: 999_999_999}}, Cost: model.Cost{USD: 9_999, Estimated: true}, Subagents: subagents(16)}},
		{name: "maximum scale", session: &model.Session{Messages: int(^uint(0) >> 1), Usage: []model.Usage{{InputTokens: int64(^uint64(0) >> 1)}}, Cost: model.Cost{USD: 9_000_000_000_000_000_000, Estimated: true, MissingPricingModels: []string{"future-model"}}, Subagents: subagents(100)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.session.ID = "lunar"
			test.session.Agent = model.AgentCodex
			row := sessionRow(test.session, now, newStyles())
			columns := listColumns(80)
			for index := 4; index < len(columns); index++ {
				if width := ansi.StringWidth(row[index]); width > columns[index].Width {
					t.Errorf("%s cell width = %d, want <= %d: %q", columns[index].Title, width, columns[index].Width, row[index])
				}
			}
		})
	}
}

func (s *refreshTestSource) Agent() model.AgentKind { return model.AgentClaude }
func (s *refreshTestSource) Roots() []string        { return []string{"/workspace"} }
func (s *refreshTestSource) Discover(context.Context) ([]string, error) {
	return []string{s.session.Path}, nil
}
func (s *refreshTestSource) Parse(string) (*model.Session, error) { return s.session, nil }

func TestFilterNarrowsRowsByFuzzyTitle(t *testing.T) {
	sessions := []*model.Session{
		{ID: "lunar", Agent: model.AgentClaude, Project: "observatory", Title: "Map lunar craters"},
		{ID: "ocean", Agent: model.AgentCodex, Project: "harbor", Title: "Chart ocean currents"},
	}
	m := NewModel(sessions, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)
	for _, r := range "lncr" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	if len(m.visible) != 1 || m.visible[0].ID != "lunar" {
		t.Fatalf("visible sessions = %#v, want lunar session", m.visible)
	}
	if view := m.View(); !strings.Contains(view, "Map lunar craters") || strings.Contains(view, "Chart ocean currents") {
		t.Fatalf("filtered table did not match visible sessions:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.detail == nil || m.detail.session.ID != "lunar" {
		t.Fatalf("filtered open selected %#v, want lunar", m.detail)
	}
}

func TestEscapeClearsActiveFilter(t *testing.T) {
	m := NewModel([]*model.Session{
		{ID: "lunar", Title: "Map lunar craters"},
		{ID: "ocean", Title: "Chart ocean currents"},
	}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
		{Type: tea.KeyEsc},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if m.filtering || m.filter.Value() != "" || len(m.visible) != 2 {
		t.Fatalf("filter state = active %v, value %q, visible %d", m.filtering, m.filter.Value(), len(m.visible))
	}
}

func TestQRemainsFilterTextAndControlCQuits(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Map lunar craters"}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(Model)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q quit while filter input was active")
		}
	}
	if got := m.filter.Value(); got != "q" {
		t.Fatalf("filter value = %q, want q", got)
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("control-c from filter returned no quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("control-c from filter did not return tea.QuitMsg")
	}
}

func TestNoResultFilterCannotOpenStaleRow(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude, Title: "Monitor lunar telemetry"}}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
		{Type: tea.KeyRunes, Runes: []rune{'z'}},
		{Type: tea.KeyRunes, Runes: []rune{'z'}},
		{Type: tea.KeyRunes, Runes: []rune{'z'}},
		{Type: tea.KeyEnter},
		{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if m.screen != screenList || len(m.visible) != 0 || len(m.table.Rows()) != 0 {
		t.Fatalf("no-result filter opened stale row: screen %v visible %d rows %d", m.screen, len(m.visible), len(m.table.Rows()))
	}
}

func TestFilterAcceptsCoalescedRapidInput(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Monitor lunar telemetry"}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/zzz")})
	m = updated.(Model)
	if !m.filtering || m.filter.Value() != "zzz" || len(m.visible) != 0 || len(m.table.Rows()) != 0 {
		t.Fatalf("coalesced filter state = active %v query %q visible %d rows %d", m.filtering, m.filter.Value(), len(m.visible), len(m.table.Rows()))
	}
}

func TestSortCyclesFromRecentToTokens(t *testing.T) {
	sessions := []*model.Session{
		{ID: "large", UpdatedAt: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC), Usage: []model.Usage{{InputTokens: 900}}},
		{ID: "recent", UpdatedAt: time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC), Usage: []model.Usage{{InputTokens: 100}}},
	}
	m := NewModel(sessions, nil)
	if m.visible[0].ID != "recent" {
		t.Fatalf("default first session = %q, want recent", m.visible[0].ID)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.visible[0].ID != "large" {
		t.Fatalf("token-sorted first session = %q, want large", m.visible[0].ID)
	}
}

func TestAgentFilterCyclesAllClaudeCodex(t *testing.T) {
	m := NewModel([]*model.Session{
		{ID: "claude", Agent: model.AgentClaude},
		{ID: "codex", Agent: model.AgentCodex},
	}, nil)
	for step, want := range []string{"claude", "codex", ""} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		m = updated.(Model)
		if want == "" {
			if len(m.visible) != 2 {
				t.Fatalf("step %d visible = %#v, want all", step, m.visible)
			}
		} else if len(m.visible) != 1 || m.visible[0].ID != want {
			t.Fatalf("step %d visible = %#v, want %s", step, m.visible, want)
		}
	}
}

func TestShortModelsUsesCostliestModel(t *testing.T) {
	session := &model.Session{
		Models:     []string{"claude-cheap", "claude-expensive"},
		ModelCosts: map[string]float64{"claude-cheap": 0.10, "claude-expensive": 2.40},
	}
	if got := shortModels(session); got != "expensive +1" {
		t.Fatalf("shortModels() = %q, want costliest model", got)
	}
}

func TestMissingPricingIsFlaggedInRowAndFooter(t *testing.T) {
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude, Project: "starship", Models: []string{"unknown-model"},
		Cost: model.Cost{MissingPricingModels: []string{"unknown-model"}},
	}
	m := NewModel([]*model.Session{session}, nil)
	row := sessionRow(session, time.Now(), m.styles)
	if !strings.Contains(row[3], "!") || !strings.Contains(row[7], "!") {
		t.Fatalf("missing-pricing row = %#v, want model and cost warning", row)
	}
	if footer := m.listFooter(); !strings.Contains(footer, "partial") {
		t.Fatalf("missing-pricing footer = %q, want partial-total warning", footer)
	}
}

func TestAPIErrorSessionHasSingleWarningGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, HasError: true}
	row := sessionRow(session, time.Now(), newStyles())
	if got := strings.Count(row[0], "⚠"); got != 1 {
		t.Fatalf("error row agent = %q, want one warning glyph", row[0])
	}
}

func TestEnterOpensSelectedSessionDetail(t *testing.T) {
	m := NewModel([]*model.Session{
		{ID: "first", Agent: model.AgentClaude},
		{ID: "second", Agent: model.AgentCodex},
	}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if m.screen != screenDetail || m.detail == nil || m.detail.session.ID != "second" {
		t.Fatalf("screen = %v, detail = %#v", m.screen, m.detail)
	}
}

func TestEscapeReturnsFromDetailToList(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)

	if m.screen != screenList || m.detail != nil {
		t.Fatalf("screen = %v, detail = %#v", m.screen, m.detail)
	}
}

func TestQQuitsFromDetail(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q from detail returned no quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("q from detail command returned %T, want tea.QuitMsg", msg)
	}
}

func TestSessionUpdateUpsertsRowAndKeepsSelection(t *testing.T) {
	m := NewModel([]*model.Session{
		{ID: "first", Agent: model.AgentClaude, Path: "/workspace/first.jsonl", Usage: []model.Usage{{InputTokens: 10}}},
		{ID: "second", Agent: model.AgentCodex, Path: "/workspace/second.jsonl", Usage: []model.Usage{{InputTokens: 20}}},
	}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)

	replacement := &model.Session{ID: "second", Agent: model.AgentCodex, Path: "/workspace/second.jsonl", Usage: []model.Usage{{InputTokens: 300}}}
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	if len(m.sessions) != 2 || m.sessions[1].TotalUsage().InputTokens != 300 {
		t.Fatalf("sessions after update = %#v", m.sessions)
	}
	if got := m.selectedIdentity(); got != sessionIdentity(replacement) {
		t.Fatalf("selected identity = %q, want updated second row", got)
	}
}

func TestUnrelatedLiveUpdateKeepsOpenDetailState(t *testing.T) {
	open := &model.Session{ID: "open", Agent: model.AgentClaude, Path: "/workspace/open.jsonl", Events: []model.Event{{Kind: model.EventUser, Text: "Open"}}}
	other := &model.Session{ID: "other", Agent: model.AgentCodex, Path: "/workspace/other.jsonl"}
	m := NewModel([]*model.Session{open, other}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	detail := m.detail
	detail.expanded["kept"] = true

	replacement := cloneSession(other)
	replacement.Title = "Updated other"
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	if m.detail == detail || !m.detail.expanded["kept"] {
		t.Fatalf("unrelated update replaced open detail state: %#v", m.detail)
	}
}

func TestMatchingLiveUpdatePreservesDetailAndRemovalReturnsToList(t *testing.T) {
	open := &model.Session{ID: "open", Agent: model.AgentClaude, Path: "/workspace/open.jsonl", Title: "Before", Events: []model.Event{{Kind: model.EventUser, Text: "Open"}}}
	m := NewModel([]*model.Session{open}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	m.detail.expanded["kept"] = true

	replacement := cloneSession(open)
	replacement.Title = "After"
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if m.detail == nil || m.detail.session.Title != "After" || !m.detail.expanded["kept"] {
		t.Fatalf("matching update lost detail state: %#v", m.detail)
	}

	updated, _ = m.Update(source.SessionUpdate{RemovedPaths: []string{open.Path}})
	m = updated.(Model)
	if m.screen != screenList || m.detail != nil {
		t.Fatalf("removed open session left detail active: screen %v detail %#v", m.screen, m.detail)
	}
}

func TestHelpOverlayListsSecondaryKeys(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(Model)

	if view := m.View(); !strings.Contains(view, "refresh") || !strings.Contains(view, "agent") {
		t.Fatalf("help overlay missing secondary keys:\n%s", view)
	}
}

func TestManualRefreshRediscoversSessions(t *testing.T) {
	adapter := &refreshTestSource{session: &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Old title"}}
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1})
	m := NewModel([]*model.Session{adapter.session}, registry)
	adapter.session = &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Fresh title"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("refresh returned no command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	if len(m.sessions) != 1 || m.sessions[0].Title != "Fresh title" {
		t.Fatalf("sessions after refresh = %#v", m.sessions)
	}
}

func TestLiveUpdateInvalidatesOlderRefreshResult(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Initial"}}, nil)
	m.refreshGeneration = 1

	live := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Live"}
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{live}})
	m = updated.(Model)
	stale := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Stale refresh"}
	updated, _ = m.Update(refreshedMsg{generation: 1, sessions: []*model.Session{stale}})
	m = updated.(Model)

	if got := m.sessions[0].Title; got != "Live" {
		t.Fatalf("title after stale refresh = %q, want live update", got)
	}
}

func TestNewerDetailLoadSupersedesOlderResult(t *testing.T) {
	current := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Current"}
	m := NewModel([]*model.Session{current}, nil)
	m.screen = screenDetail
	m.detail = newDetailState(current, m.width, m.height, m.styles)
	m.detailGeneration = 2

	newer := cloneSession(current)
	newer.Events = []model.Event{{Kind: model.EventAssistantText, Text: "newer"}}
	updated, _ := m.Update(detailLoadedMsg{generation: 2, identity: sessionIdentity(current), session: newer})
	m = updated.(Model)
	older := cloneSession(current)
	older.Events = []model.Event{{Kind: model.EventAssistantText, Text: "older"}}
	updated, _ = m.Update(detailLoadedMsg{generation: 1, identity: sessionIdentity(current), session: older})
	m = updated.(Model)

	if got := m.detail.session.Events[0].Text; got != "newer" {
		t.Fatalf("detail after stale load = %q, want newer", got)
	}
}

func TestListNeverWrapsAtEightyColumns(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel([]*model.Session{{
		ID: "lunar", Agent: model.AgentClaude,
		Project: strings.Repeat("project", 20), Title: strings.Repeat("telemetry ", 30), Models: []string{"claude-opus-4-8"},
	}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)

	for number, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("line %d width = %d, want <= 80: %q", number+1, width, line)
		}
	}
}

func TestListWithRowsDoesNotPanicAtNarrowWidths(t *testing.T) {
	for _, width := range []int{79, 60, 20, 1} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude, Title: "Survey"}}, nil)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 8})
			_ = updated.(Model).View()
		})
	}
}

func TestAgeTickRefreshesRelativeAge(t *testing.T) {
	now := time.Date(2026, 1, 2, 6, 0, 30, 0, time.UTC)
	m := newModelWithClock([]*model.Session{{ID: "lunar", UpdatedAt: time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)}}, nil, func() time.Time { return now })
	if !strings.Contains(m.View(), "now") {
		t.Fatalf("initial view missing now age:\n%s", m.View())
	}
	now = now.Add(2 * time.Minute)

	updated, _ := m.Update(ageTickMsg{})
	m = updated.(Model)
	if !strings.Contains(m.View(), "2m") {
		t.Fatalf("ticked view missing updated age:\n%s", m.View())
	}
}
