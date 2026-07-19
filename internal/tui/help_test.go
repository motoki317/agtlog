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
	for _, want := range []string{"j/k ↑/↓", "pgup/pgdn page", "home/end/g/G edge", "/ filter", "s sort", "a agent", "enter open", "r refresh", "mouse wheel scroll · click select · click again open", "shift+drag terminal copy", "t theme", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("list help missing %q:\n%s", want, view)
		}
	}
}

func TestDetailHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "g/G edge", "space toggle", "enter open", "tab tabs", "w wrap", "J/K subagent", "mouse wheel scroll · click select · click again activate", "esc/h back", "? help", "ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail help missing %q:\n%s", want, view)
		}
	}
}

func TestDetailHelpKeepsBindingsVisibleAtFortyColumns(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.width = 40
	m.screen = screenDetail
	view := ansi.Strip(m.helpView())
	for _, want := range []string{"j/k scroll", "g/G edge", "space toggle", "enter open", "tab tabs", "w wrap", "J/K subagent", "esc/h back", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("40-column detail help missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "…") {
		t.Fatalf("40-column detail help truncated a binding:\n%s", view)
	}
}

func TestItemHelpIncludesOnlyItemBindings(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	m.detail = newItemView(model.Event{Kind: model.EventThinking, Text: "Chart route"}, model.AgentClaude, nil, m.width, m.height, m.styles)
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "g/G edge", "w wrap", "mouse wheel scroll", "esc/h/← back", "t theme", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("item help missing %q:\n%s", want, view)
		}
	}
	for _, sessionOnly := range []string{"space toggle", "enter open", "tab tabs", "J/K subagent"} {
		if strings.Contains(view, sessionOnly) {
			t.Fatalf("item help advertised session-only binding %q:\n%s", sessionOnly, view)
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
