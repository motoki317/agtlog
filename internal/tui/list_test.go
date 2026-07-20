package tui

import (
	"context"
	"fmt"
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

func TestListSummaryReportsWatchingRoots(t *testing.T) {
	m := NewModel(nil, nil).WithWatchingRoots(3)
	if summary := m.listSummary(); !strings.Contains(summary, "watching 3 roots") {
		t.Fatalf("list summary = %q, want watched root count", summary)
	}
}

func TestColoredListFillsWideTerminal(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	m := NewModel([]*model.Session{{ID: "lunar", Agent: model.AgentClaude, Project: "observatory", Title: "Map lunar craters"}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})

	firstLine := strings.Split(ansi.Strip(updated.(Model).View()), "\n")[0]
	if !strings.HasPrefix(firstLine, "╭") || !strings.HasSuffix(firstLine, "╮") || ansi.StringWidth(firstLine) != 160 {
		t.Fatalf("context panel top = %q (width %d), want rounded full-width border", firstLine, ansi.StringWidth(firstLine))
	}
}

func TestCompactListAndDetailKeepCompletePanelsAtLowHeights(t *testing.T) {
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Project: "observatory", Title: "Map lunar craters"}
	for height := 3; height <= 8; height++ {
		t.Run(fmt.Sprintf("height-%d", height), func(t *testing.T) {
			m := newModelWithClockAndTheme([]*model.Session{session}, nil, time.Now, themes["default"])
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: height})
			m = updated.(Model)
			for name, view := range map[string]string{
				"list":   m.View(),
				"detail": newDetailState(session, 60, height, m.styles).view(),
			} {
				plain := ansi.Strip(view)
				lines := strings.Split(plain, "\n")
				if len(lines) != height {
					t.Fatalf("%s lines = %d, want %d:\n%s", name, len(lines), height, plain)
				}
				for index, line := range lines {
					if got := ansi.StringWidth(line); got != 60 {
						t.Fatalf("%s line %d width = %d, want 60: %q", name, index+1, got, line)
					}
				}
				if strings.Count(plain, "╭") != strings.Count(plain, "╰") {
					t.Fatalf("%s has a clipped panel at height %d:\n%s", name, height, plain)
				}
			}
		})
	}
}

func TestColoredWideListKeepsAccountingCellsVisible(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	now := time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)
	subagents := make([]*model.Session, 17)
	for index := range subagents {
		subagents[index] = &model.Session{ID: fmt.Sprintf("worker-%02d", index)}
	}
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude, Project: "observatory", Title: "Map lunar craters", Models: []string{"claude-opus-4-8"},
		UpdatedAt: now.Add(-12 * time.Minute), Messages: 42, Usage: []model.Usage{{InputTokens: 1_021_000_000}}, Cost: model.Cost{USD: 12.34}, Subagents: subagents,
	}
	m := newModelWithClock([]*model.Session{session}, nil, func() time.Time { return now })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	plain := ansi.Strip(updated.(Model).View())

	for _, want := range []string{"claude", "12m", "42", "$12", "17"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("colored row lost %q after ANSI stripping:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "1.0B") || strings.Contains(plain, "TOKENS") {
		t.Fatalf("colored session list retained token data:\n%s", plain)
	}
}

func TestColoredWideListRowsAlignWithHeader(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	now := time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)
	sessions := []*model.Session{
		{ID: "one", Agent: model.AgentClaude, Project: "観測所", Title: "Map cafe\u0301 craters", Models: []string{"claude-opus-4-8"}, UpdatedAt: now.Add(-time.Minute), Messages: 7, Usage: []model.Usage{{InputTokens: 88_000}}, Cost: model.Cost{USD: 1.23}},
		{ID: "two", Agent: model.AgentCodex, Project: "harbor", Title: "Chart route", Models: []string{"gpt-5.6-sol"}, UpdatedAt: now.Add(-2 * time.Hour), Messages: 42, Usage: []model.Usage{{InputTokens: 1_021_000_000}}, Cost: model.Cost{USD: 9.87, Estimated: true}},
	}
	m := newModelWithClockAndTheme(sessions, nil, func() time.Time { return now }, themes["default"])
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	raw := updated.(Model).View()
	if !strings.Contains(raw, "\x1b[") {
		t.Fatal("colored-path render emitted no ANSI styling")
	}

	var rendered []string
	for _, line := range strings.Split(raw, "\n") {
		plain := ansi.Strip(line)
		if strings.HasPrefix(plain, "│  AGENT") || strings.HasPrefix(plain, "│› claude") || strings.HasPrefix(plain, "│  codex") {
			if ansi.StringWidth(plain) != 160 {
				t.Fatalf("rendered table line width = %d, want 160: %q", ansi.StringWidth(plain), plain)
			}
			rendered = append(rendered, ansi.Cut(plain, 1, 159))
		}
	}
	if len(rendered) != len(sessions)+1 {
		t.Fatalf("rendered header/rows = %d, want %d", len(rendered), len(sessions)+1)
	}
	columns := listColumns(158 - listCursorWidth)
	separator := listCursorWidth
	for index, column := range columns[:len(columns)-1] {
		separator += column.width
		for lineIndex, line := range rendered {
			if got := ansi.Cut(line, separator, separator+1); got != " " {
				t.Errorf("line %d separator after column %d = %q, want space at display column %d", lineIndex, index, got, separator)
			}
		}
		separator++
	}
	for rowIndex, row := range rendered[1:] {
		start := listCursorWidth
		for _, column := range columns {
			if column.kind == columnAgent || column.kind == columnAge || column.kind == columnMessages || column.kind == columnCost {
				if cell := strings.TrimSpace(ansi.Cut(row, start, start+column.width)); cell == "" {
					t.Errorf("row %d %s cell is empty", rowIndex, column.title)
				}
			}
			start += column.width + 1
		}
	}
}

func TestSelectedRowUsesOneFullWidthStyle(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["dracula"])
	columns := listColumns(158 - listCursorWidth)
	row := renderSessionRow(&model.Session{ID: "lunar", Agent: model.AgentClaude, Title: "Map craters"}, time.Now(), columns, 158, true, styleSet)
	plain := ansi.Strip(row)

	if ansi.StringWidth(plain) != 158 || row != styleSet.selected.Render(plain) {
		t.Fatalf("selected row was not styled once across 158 cells: width=%d", ansi.StringWidth(plain))
	}
}

func TestListRowsReserveFixedCursorMarkerWidth(t *testing.T) {
	styleSet := newStyles()
	width := 60
	columns := listColumns(width - 2)
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, Title: "Map lunar craters"}
	selected := renderSessionPanelLine(session, time.Now(), columns, width, true, styleSet)
	unselected := renderSessionPanelLine(session, time.Now(), columns, width, false, styleSet)

	if !strings.HasPrefix(selected.plain, "› ") || !strings.HasPrefix(unselected.plain, "  ") {
		t.Fatalf("row markers = selected %q, unselected %q", ansi.Cut(selected.plain, 0, 2), ansi.Cut(unselected.plain, 0, 2))
	}
	selectedContent := strings.TrimPrefix(selected.plain, "› ")
	unselectedContent := strings.TrimPrefix(unselected.plain, "  ")
	if ansi.StringWidth(selected.plain) != width || ansi.StringWidth(unselected.plain) != width || selectedContent != unselectedContent {
		t.Fatalf("fixed-width rows = selected %q, unselected %q", selected.plain, unselected.plain)
	}
	if selected.styled != styleSet.selected.Render(selected.plain) {
		t.Fatalf("selected row was not styled once across cursor and content: %q", selected.styled)
	}
}

func TestListFooterKeepsMovementHintUnderPressure(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel([]*model.Session{{ID: "lunar"}}, nil)
	m.width = 50
	footer := ansi.Strip(m.renderKeyBar())

	move, filter := strings.Index(footer, "↑/↓ move"), strings.Index(footer, "/ filter")
	if move < 0 || filter < 0 || move > filter || ansi.StringWidth(footer) > m.width || strings.Contains(footer, "…") {
		t.Fatalf("50-column list footer = %q", footer)
	}
}

func TestSubagentColumnUsesAccentStyle(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["dracula"])
	column := listColumn{kind: columnSubagents, width: listSubagentsWidth, right: true}
	cell := fitPlain("17", column.width, column.right)
	presentation := newSessionPresentation(&model.Session{ID: "lunar", Subagents: []*model.Session{{ID: "scout"}}})

	if got, want := styleSessionCell(cell, &model.Session{ID: "lunar"}, presentation, column, styleSet), styleSet.accent.Render(cell); got != want {
		t.Fatalf("subagent cell style = %q, want accent %q", got, want)
	}
}

func TestSubagentCellIsBlankRecursiveAndCompact(t *testing.T) {
	nested := &model.Session{ID: "parent", Subagents: []*model.Session{{ID: "child", Subagents: []*model.Session{{ID: "grandchild"}}}}}
	tests := []struct {
		name         string
		presentation sessionPresentation
		want         string
	}{
		{name: "none", presentation: sessionPresentation{}, want: ""},
		{name: "recursive", presentation: newSessionPresentation(nested), want: "2"},
		{name: "compact", presentation: sessionPresentation{subagentCount: 12_345}, want: "12k"},
	}
	column := listColumn{kind: columnSubagents, width: listSubagentsWidth, right: true}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sessionCellWithPresentation(&model.Session{}, test.presentation, time.Time{}, column); got != test.want {
				t.Fatalf("subagent cell = %q, want %q", got, test.want)
			}
		})
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

func TestSessionListOmitsTokensAndKeepsSubagentCount(t *testing.T) {
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

	if view := ansi.Strip(updated.(Model).View()); strings.Contains(view, "1.0B") || !strings.Contains(view, "16") || strings.Contains(view, "TOKENS") || strings.Contains(view, glyphSubagent) {
		t.Fatalf("session list retained tokens or lost the subagent count:\n%s", view)
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
			columns := listColumns(160)
			for index := 4; index < len(columns); index++ {
				cell := fitPlain(sessionCell(test.session, now, columns[index]), columns[index].width, columns[index].right)
				if width := ansi.StringWidth(cell); width > columns[index].width {
					t.Errorf("%s cell width = %d, want <= %d: %q", columns[index].title, width, columns[index].width, cell)
				}
			}
		})
	}
}

func TestListColumnsFillWidthAndDropLowValueFieldsInOrder(t *testing.T) {
	wide := listColumns(158)
	if got := listColumnsWidth(wide); got != 158 {
		t.Fatalf("wide columns use %d cells, want 158", got)
	}
	if wide[1].kind != columnProject || wide[1].width != listProjectCap || wide[2].kind != columnTitle || wide[2].width <= listTitlePreferred {
		t.Fatalf("wide flex columns = %#v, want capped project and slack-absorbing title", wide)
	}
	boundaries := []struct {
		width int
		want  []listColumnKind
	}{
		{width: 74, want: []listColumnKind{columnAgent, columnProject, columnTitle, columnModel, columnAge, columnMessages, columnSubagents, columnCost}},
		{width: 73, want: []listColumnKind{columnAgent, columnProject, columnTitle, columnAge, columnMessages, columnSubagents, columnCost}},
		{width: 59, want: []listColumnKind{columnAgent, columnProject, columnTitle, columnAge, columnSubagents, columnCost}},
		{width: 53, want: []listColumnKind{columnAgent, columnProject, columnTitle, columnAge, columnCost}},
		{width: 48, want: []listColumnKind{columnAgent, columnTitle, columnAge, columnCost}},
		{width: 39, want: []listColumnKind{columnAgent, columnTitle, columnCost}},
	}
	for _, boundary := range boundaries {
		columns := listColumns(boundary.width)
		if got := listColumnsWidth(columns); got != boundary.width || len(columns) != len(boundary.want) {
			t.Fatalf("columns at width %d = %#v (width %d), want %v", boundary.width, columns, got, boundary.want)
		}
		for index := range boundary.want {
			if columns[index].kind != boundary.want[index] {
				t.Fatalf("column %d at width %d = %v, want %v", index, boundary.width, columns[index].kind, boundary.want[index])
			}
		}
	}

	narrow := listColumns(48)
	want := []listColumnKind{columnAgent, columnTitle, columnAge, columnCost}
	if got := listColumnsWidth(narrow); got != 48 || len(narrow) != len(want) {
		t.Fatalf("narrow columns = %#v (width %d), want %v at width 48", narrow, got, want)
	}
	for index := range want {
		if narrow[index].kind != want[index] {
			t.Fatalf("narrow column %d = %v, want %v", index, narrow[index].kind, want[index])
		}
	}
}

func TestSessionListNeverIncludesTokenColumn(t *testing.T) {
	for _, width := range []int{40, 80, 160} {
		for _, column := range listColumns(width) {
			if column.kind == columnTokens || column.title == "TOKENS" {
				t.Fatalf("%d-column session list retained token column: %#v", width, column)
			}
		}
	}
}

func TestAbsoluteListTimeUsesDatedSecondsForMultiDateSession(t *testing.T) {
	session := &model.Session{
		StartedAt: time.Date(2026, time.September, 29, 23, 55, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.September, 30, 1, 2, 3, 0, time.UTC),
	}
	columns := listColumns(160, true)
	for _, column := range columns {
		if column.kind != columnAge {
			continue
		}
		if got := sessionCell(session, time.Time{}, column); got != "Sep 30 01:02:03" || ansi.StringWidth(got) != column.width {
			t.Fatalf("absolute list time = %q (width %d/%d), want dated seconds", got, ansi.StringWidth(got), column.width)
		}
		return
	}
	t.Fatal("absolute list columns omitted TIME")
}

func TestTimeAndThemeKeysRemainDistinct(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = updated.(Model)
	if !m.absoluteTime || m.theme.Name != "default" {
		t.Fatalf("T set absolute=%t theme=%q, want time-only toggle", m.absoluteTime, m.theme.Name)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = updated.(Model)
	if !m.absoluteTime || m.theme.Name == "default" {
		t.Fatalf("t set absolute=%t theme=%q, want theme-only toggle", m.absoluteTime, m.theme.Name)
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
	if m.detail == nil || detailStateFromScreen(t, m.detail).session.ID != "lunar" {
		t.Fatalf("filtered open selected %#v, want lunar", m.detail)
	}
}

func TestFilteringContextShowsLiveQueryOnce(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Map moon"}}, nil)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
		{Type: tea.KeyRunes, Runes: []rune("moon")},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	plain := ansi.Strip(m.View())
	if strings.Count(plain, "/moon") != 1 || !strings.Contains(plain, "/moon▊") {
		t.Fatalf("filter context did not show one live query:\n%s", plain)
	}
}

func TestFilteringKeepsLongQueryAndCursorVisibleOnDedicatedLine(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Map moon"}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = updated.(Model)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
		{Type: tea.KeyRunes, Runes: []rune("0123456789abcdefghijklmnopqrstuvwxyz")},
		{Type: tea.KeyHome},
		{Type: tea.KeyRight},
		{Type: tea.KeyRight},
		{Type: tea.KeyRight},
		{Type: tea.KeyRight},
	} {
		updated, _ = m.Update(key)
		m = updated.(Model)
	}

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) < 3 || !strings.Contains(lines[2], "/0123▊4") {
		t.Fatalf("long filter cursor is not visible on its own line:\n%s", m.View())
	}
	if strings.Contains(lines[1], "/0123") {
		t.Fatalf("filter query was appended to summary instead of dedicated line:\n%s", m.View())
	}
}

func TestCompactFilteringPrioritizesLiveInput(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Map moon"}}, nil)
	for _, key := range []tea.Msg{
		tea.WindowSizeMsg{Width: 40, Height: 3},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/moon")},
	} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "/moon▊") {
		t.Fatalf("compact filtering hid live input:\n%s", view)
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

func TestTransientNoResultFilterRestoresSelectedIdentity(t *testing.T) {
	m := NewModel([]*model.Session{
		{ID: "first", Agent: model.AgentClaude, Path: "/workspace/first.jsonl", Title: "First lunar survey"},
		{ID: "target", Agent: model.AgentCodex, Path: "/workspace/target.jsonl", Title: "Target lunar survey"},
		{ID: "third", Agent: model.AgentClaude, Path: "/workspace/third.jsonl", Title: "Third ocean survey"},
	}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	want := m.selectedIdentity()
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("/zzz")},
		{Type: tea.KeyBackspace},
		{Type: tea.KeyBackspace},
		{Type: tea.KeyBackspace},
		{Type: tea.KeyEnter},
	} {
		updated, _ = m.Update(key)
		m = updated.(Model)
	}
	if got := m.selectedIdentity(); got != want {
		t.Fatalf("selection after transient empty result = %q, want %q", got, want)
	}
}

func TestCommittedNoResultFilterKeepsIdentityThroughTickAndClear(t *testing.T) {
	m := NewModel([]*model.Session{
		{ID: "first", Agent: model.AgentClaude, Path: "/workspace/first.jsonl", Title: "First lunar survey"},
		{ID: "target", Agent: model.AgentCodex, Path: "/workspace/target.jsonl", Title: "Target lunar survey"},
	}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	want := m.selectedIdentity()
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/zzz")},
		tea.KeyMsg{Type: tea.KeyEnter},
		ageTickMsg{},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}},
		tea.KeyMsg{Type: tea.KeyEsc},
	} {
		updated, _ = m.Update(msg)
		m = updated.(Model)
	}
	if got := m.selectedIdentity(); got != want {
		t.Fatalf("selection after committed empty filter clear = %q, want %q", got, want)
	}
}

func TestFilterSanitizesFormatControlsBeforeMatching(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Map moon"}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/moon\u202e")})
	m = updated.(Model)
	if len(m.visible) != 1 || m.visible[0].ID != "lunar" {
		t.Fatalf("sanitized visible sessions = %#v, want lunar", m.visible)
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
	if m.screen != screenList || len(m.visible) != 0 {
		t.Fatalf("no-result filter opened stale row: screen %v visible %d", m.screen, len(m.visible))
	}
}

func TestFilterAcceptsCoalescedRapidInput(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "lunar", Title: "Monitor lunar telemetry"}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/zzz")})
	m = updated.(Model)
	if !m.filtering || m.filter.Value() != "zzz" || len(m.visible) != 0 {
		t.Fatalf("coalesced filter state = active %v query %q visible %d", m.filtering, m.filter.Value(), len(m.visible))
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

func TestSelectionIdentitySurvivesSortFilterAndResize(t *testing.T) {
	now := time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)
	sessions := []*model.Session{
		{ID: "recent", Agent: model.AgentClaude, Path: "/workspace/recent.jsonl", Title: "Recent lunar survey", UpdatedAt: now, Usage: []model.Usage{{InputTokens: 10}}},
		{ID: "target", Agent: model.AgentCodex, Path: "/workspace/target.jsonl", Title: "Target lunar survey", UpdatedAt: now.Add(-time.Hour), Usage: []model.Usage{{InputTokens: 500}}},
		{ID: "large", Agent: model.AgentClaude, Path: "/workspace/large.jsonl", Title: "Large ocean survey", UpdatedAt: now.Add(-2 * time.Hour), Usage: []model.Usage{{InputTokens: 900}}},
	}
	m := newModelWithClock(sessions, nil, func() time.Time { return now })
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	want := sessionIdentity(sessions[1])

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'s'}},
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
		{Type: tea.KeyRunes, Runes: []rune("lunar")},
	} {
		updated, _ = m.Update(key)
		m = updated.(Model)
		if got := m.selectedIdentity(); got != want {
			t.Fatalf("selection after %q = %q, want %q", key.String(), got, want)
		}
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 58, Height: 9})
	m = updated.(Model)
	if got := m.selectedIdentity(); got != want {
		t.Fatalf("selection after resize = %q, want %q", got, want)
	}
	if capacity := m.listRowCapacity(); capacity > 0 && (m.cursor < m.listOffset || m.cursor >= m.listOffset+capacity) {
		t.Fatalf("selected row is outside window: cursor=%d offset=%d capacity=%d", m.cursor, m.listOffset, capacity)
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
	columns := listColumns(160)
	modelCell := sessionCell(session, time.Now(), columns[3])
	costCell := sessionCell(session, time.Now(), columns[len(columns)-1])
	if !strings.Contains(modelCell, "!") || !strings.Contains(costCell, "!") {
		t.Fatalf("missing-pricing cells = %q / %q, want model and cost warning", modelCell, costCell)
	}
	if summary := m.listSummary(); !strings.Contains(summary, "partial") {
		t.Fatalf("missing-pricing summary = %q, want partial-total warning", summary)
	}
}

func TestAPIErrorSessionHasSingleWarningGlyph(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &model.Session{ID: "lunar", Agent: model.AgentClaude, HasError: true}
	column := listColumns(160)[0]
	cell := sessionCell(session, time.Now(), column)
	if got := strings.Count(cell, glyphWarning); got != 1 {
		t.Fatalf("error row agent = %q, want one warning glyph", cell)
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

	if m.screen != screenDetail || m.detail == nil || detailStateFromScreen(t, m.detail).session.ID != "second" {
		t.Fatalf("screen = %v, detail = %#v", m.screen, m.detail)
	}
}

func TestListNavigationScrollsWindowAndSupportsHomeEnd(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), Title: fmt.Sprintf("Survey %02d", index), UpdatedAt: time.Unix(int64(20-index), 0)}
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(Model)
	if m.cursor != 19 || m.listOffset != 15 || !strings.Contains(m.View(), "Survey 19") || strings.Contains(m.View(), "Survey 00") {
		t.Fatalf("end navigation cursor=%d offset=%d:\n%s", m.cursor, m.listOffset, m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(Model)
	if m.cursor != 0 || m.listOffset != 0 || !strings.Contains(m.View(), "Survey 00") {
		t.Fatalf("home navigation cursor=%d offset=%d:\n%s", m.cursor, m.listOffset, m.View())
	}
}

func TestListNavigationSupportsVimEdges(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), Title: fmt.Sprintf("Survey %02d", index), UpdatedAt: time.Unix(int64(20-index), 0)}
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(Model)
	if m.cursor != 19 || m.listOffset != 15 || !strings.Contains(m.View(), "Survey 19") || strings.Contains(m.View(), "Survey 00") {
		t.Fatalf("G navigation cursor=%d offset=%d:\n%s", m.cursor, m.listOffset, m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	if m.cursor != 0 || m.listOffset != 0 || !strings.Contains(m.View(), "Survey 00") {
		t.Fatalf("g navigation cursor=%d offset=%d:\n%s", m.cursor, m.listOffset, m.View())
	}
}

func TestListMouseWheelDownScrollsContent(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), UpdatedAt: time.Unix(int64(20-index), 0)}
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)

	if m.listOffset != 3 || m.cursor != 0 {
		t.Fatalf("wheel down offset=%d cursor=%d, want offset 3 with unchanged selection", m.listOffset, m.cursor)
	}
}

func TestListMouseWheelUpScrollsContent(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), UpdatedAt: time.Unix(int64(20-index), 0)}
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	m.listOffset = 6

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = updated.(Model)

	if m.listOffset != 3 || m.cursor != 0 {
		t.Fatalf("wheel up offset=%d cursor=%d, want offset 3 with unchanged selection", m.listOffset, m.cursor)
	}
}

func TestListMouseClickSelectsRow(t *testing.T) {
	sessions := []*model.Session{
		{ID: "first", UpdatedAt: time.Unix(2, 0)},
		{ID: "second", UpdatedAt: time.Unix(1, 0)},
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)

	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: 6, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = updated.(Model)

	if m.cursor != 1 || m.screen != screenList {
		t.Fatalf("click selection cursor=%d screen=%v, want second row on list", m.cursor, m.screen)
	}
}

func TestListMouseSecondClickOpensSelectedRow(t *testing.T) {
	sessions := []*model.Session{
		{ID: "first", UpdatedAt: time.Unix(2, 0)},
		{ID: "second", UpdatedAt: time.Unix(1, 0)},
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	click := tea.MouseMsg{X: 2, Y: 6, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
	updated, _ = m.Update(click)
	m = updated.(Model)

	updated, _ = m.Update(click)
	m = updated.(Model)

	if m.screen != screenDetail || detailStateFromScreen(t, m.detail).session != sessions[1] {
		t.Fatalf("second click screen=%v detail=%#v, want second session detail", m.screen, m.detail)
	}
}

func TestFilteringIgnoresMouseClicksButAllowsWheel(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), UpdatedAt: time.Unix(int64(20-index), 0)}
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)

	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: 7, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = updated.(Model)
	if m.cursor != 0 || m.screen != screenList {
		t.Fatalf("filter click cursor=%d screen=%v, want unchanged list selection", m.cursor, m.screen)
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if m.listOffset != mouseWheelRows {
		t.Fatalf("filter wheel offset=%d, want %d", m.listOffset, mouseWheelRows)
	}
}

func TestListRowAtYMapsCompactSession(t *testing.T) {
	m := NewModel([]*model.Session{{ID: "route"}}, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 7})
	m = updated.(Model)

	index, ok := m.rowAtY(2)
	if !ok || index != 0 {
		t.Fatalf("compact rowAtY(2) = %d, %t, want first session", index, ok)
	}
}

func TestCompactListMouseWheelScrollsDisplayedRow(t *testing.T) {
	sessions := []*model.Session{
		{ID: "first", Title: "First survey", UpdatedAt: time.Unix(3, 0)},
		{ID: "second", Title: "Second survey", UpdatedAt: time.Unix(2, 0)},
		{ID: "third", Title: "Third survey", UpdatedAt: time.Unix(1, 0)},
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 7})
	m = updated.(Model)

	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)

	index, ok := m.rowAtY(2)
	if m.cursor != 0 || index != 2 || !ok || !strings.Contains(m.View(), "Third survey") || strings.Contains(m.View(), "First survey") {
		t.Fatalf("compact wheel cursor=%d row=%d/%t:\n%s", m.cursor, index, ok, m.View())
	}
}

func TestListRowAtYHonorsPanelBoundariesAndOffset(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), UpdatedAt: time.Unix(int64(20-index), 0)}
	}
	m := NewModel(sessions, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(Model)
	m.listOffset = 3

	for _, test := range []struct {
		y     int
		index int
		ok    bool
	}{
		{y: 0}, {y: 2}, {y: 3}, {y: 4},
		{y: 5, index: 3, ok: true},
		{y: 7, index: 5, ok: true},
		{y: 10}, {y: 11},
	} {
		index, ok := m.rowAtY(test.y)
		if index != test.index || ok != test.ok {
			t.Errorf("rowAtY(%d) = %d, %t, want %d, %t", test.y, index, ok, test.index, test.ok)
		}
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
	detail := detailStateFromScreen(t, m.detail)
	detail.expanded["kept"] = true

	replacement := cloneSession(other)
	replacement.Title = "Updated other"
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	if m.detail == detail || !detailStateFromScreen(t, m.detail).expanded["kept"] {
		t.Fatalf("unrelated update replaced open detail state: %#v", m.detail)
	}
}

func TestMatchingLiveUpdatePreservesDetailAndRemovalReturnsToList(t *testing.T) {
	open := &model.Session{ID: "open", Agent: model.AgentClaude, Path: "/workspace/open.jsonl", Title: "Before", Events: []model.Event{{Kind: model.EventUser, Text: "Open"}}}
	m := NewModel([]*model.Session{open}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	detail := detailStateFromScreen(t, m.detail)
	detail.expanded["kept"] = true
	detail.defaultExpanded = false

	replacement := cloneSession(open)
	replacement.Title = "After"
	updated, _ = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if m.detail == nil || detailStateFromScreen(t, m.detail).session.Title != "After" || !detailStateFromScreen(t, m.detail).expanded["kept"] || detailStateFromScreen(t, m.detail).defaultExpanded {
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

	if got := detailStateFromScreen(t, m.detail).session.Events[0].Text; got != "newer" {
		t.Fatalf("detail after stale load = %q, want newer", got)
	}
}

func TestDetailLoadPreservesWrapToggle(t *testing.T) {
	current := &model.Session{ID: "lunar", Agent: model.AgentClaude, Path: "/workspace/session.jsonl", Title: "Current"}
	m := NewModel([]*model.Session{current}, nil)
	m.screen = screenDetail
	m.detail = newDetailState(current, m.width, m.height, m.styles)
	detailStateFromScreen(t, m.detail).setWrap(false)
	m.detailGeneration = 1

	loaded := cloneSession(current)
	loaded.Events = []model.Event{{Kind: model.EventAssistantText, Text: strings.Repeat("wrapped route ", 20)}}
	updated, _ := m.Update(detailLoadedMsg{generation: 1, identity: sessionIdentity(current), session: loaded})
	m = updated.(Model)

	if detailStateFromScreen(t, m.detail).wrap {
		t.Fatal("detail load cleared the disabled wrap toggle")
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
