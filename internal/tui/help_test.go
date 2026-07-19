package tui

import (
	"strings"
	"testing"
)

func TestListHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := NewModel(nil, nil)
	view := m.helpView()
	for _, want := range []string{"j/k ↑/↓", "/ filter", "s sort", "a agent", "enter open", "r refresh", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("list help missing %q:\n%s", want, view)
		}
	}
}

func TestDetailHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := NewModel(nil, nil)
	m.screen = screenDetail
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "space/enter expand", "J/K subagent", "esc/h back", "? help", "ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail help missing %q:\n%s", want, view)
		}
	}
}
