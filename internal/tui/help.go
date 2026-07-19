package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) helpView() string {
	lines := []string{
		"agtlog keys",
		"",
		"j/k ↑/↓ move · / filter",
		"s sort · a agent",
		"enter open · r refresh",
		"? help · q/ctrl-c quit",
	}
	if m.screen == screenDetail {
		lines = []string{
			"agtlog keys",
			"",
			"j/k scroll · space/enter expand",
			"J/K subagent · esc/h back",
			"? help · q/ctrl-c quit",
		}
	}
	lines = append(lines, "", "[? / esc] close")
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], m.width, "…")
	}
	return strings.Join(lines, "\n")
}
