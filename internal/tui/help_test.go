package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestListHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	view := m.helpView()
	for _, want := range []string{"j/k ↑/↓", "pgup/pgdn page", "home/end edge", "/ filter", "s sort", "a agent", "enter open", "r refresh", "t theme", "? help", "q/ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("list help missing %q:\n%s", want, view)
		}
	}
}

func TestDetailHelpIncludesEveryRequiredBinding(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	m.screen = screenDetail
	view := m.helpView()
	for _, want := range []string{"j/k scroll", "space/enter expand", "J/K subagent", "esc/h back", "? help", "ctrl-c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail help missing %q:\n%s", want, view)
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
