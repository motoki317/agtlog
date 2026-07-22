package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

func TestListHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	view := m.helpView()
	for _, want := range []string{"j/k ↑/↓", "pgup/pgdn page", "home/end/g/G edge", "/ filter", "←/→ column", "⇧O sort", "⇧A age", "⇧N title", "a agent", "T time", "enter open", "r refresh", "mouse wheel scroll · click select · click again open", "shift+drag terminal copy", "t theme", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("list help missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "s sort") {
		t.Fatalf("list help retained deleted sort key:\n%s", view)
	}
}

func TestDetailHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "g/G edge", "←/→ fold", "space toggle", "E expand all", "C collapse all", "enter/l open", "tab tabs", "w wrap", "T time", "mouse wheel scroll · click select", "esc/h back", "? help", "ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail help missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"J/K", "subagent"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("detail help retained removed hint %q:\n%s", unwanted, view)
		}
	}
}

func TestDetailHelpKeepsBindingsVisibleAtFortyColumns(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.width = 40
	m.screen = screenDetail
	view := ansi.Strip(m.helpView())
	for _, want := range []string{"j/k scroll", "g/G edge", "←/→ fold", "space toggle", "E expand all", "C collapse all", "enter/l open", "tab tabs", "w wrap", "T time", "esc/h back", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("40-column detail help missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "…") {
		t.Fatalf("40-column detail help truncated a binding:\n%s", view)
	}
}

func TestSubagentsHelpIncludesOnlyTableBindings(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	detail := newDetailState(&model.Session{ID: "route"}, m.width, m.height, m.styles)
	detail.tab = tabSubagents
	detail.rebuild()
	m.detail = detail
	view := m.helpView()

	for _, want := range []string{"j/k scroll", "g/G edge", "←/→ column", "⇧O sort", "⇧A age", "⇧N title", "enter/l open", "tab tabs", "T time", "mouse wheel scroll · click select", "esc/h back"} {
		if !strings.Contains(view, want) {
			t.Errorf("Subagents help missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"←/→ fold", "space toggle", "expand all", "collapse all"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("Subagents help advertises inapplicable %q:\n%s", unwanted, view)
		}
	}
}

func TestItemHelpIncludesOnlyItemBindings(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	m.detail = newItemView(model.Event{Kind: model.EventThinking, Text: "Chart route"}, model.AgentClaude, nil, m.width, m.height, m.styles)
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "g/G edge", "w wrap", "R raw", "T time", "mouse wheel scroll", "esc/h back", "t theme", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("item help missing %q:\n%s", want, view)
		}
	}
	for _, sessionOnly := range []string{"space toggle", "E expand all", "C collapse all", "enter open", "tab tabs"} {
		if strings.Contains(view, sessionOnly) {
			t.Fatalf("item help advertised session-only binding %q:\n%s", sessionOnly, view)
		}
	}
}

func TestInfoHelpIncludesOnlyApplicableBindings(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	detail := newDetailState(&model.Session{ID: "route"}, m.width, m.height, m.styles)
	detail.tab = tabInfo
	detail.rebuild()
	m.detail = detail
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "g/G edge", "tab tabs", "w wrap", "T time", "mouse wheel scroll", "esc/h back"} {
		if !strings.Contains(view, want) {
			t.Errorf("Info help missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"←/→ fold", "space toggle", "expand all", "enter/l open"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("Info help advertises inapplicable %q:\n%s", unwanted, view)
		}
	}
}

func TestHelpRendersAsFullWidthBorderedPanel(t *testing.T) {
	m := NewModel(nil, nil)
	lines := strings.Split(ansi.Strip(m.helpView()), "\n")
	if !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") || ansi.StringWidth(lines[0]) != 80 {
		t.Fatalf("help top border = %q (width %d), want rounded full width", lines[0], ansi.StringWidth(lines[0]))
	}
}
