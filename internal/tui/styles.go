package tui

import (
	"os"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/motoki317/agtlog/internal/model"
)

type styles struct {
	table     table.Styles
	claude    lipgloss.Style
	codex     lipgloss.Style
	muted     lipgloss.Style
	estimated lipgloss.Style
	emphasis  lipgloss.Style
	warning   lipgloss.Style
}

func (s styles) agentLabel(agent model.AgentKind) string {
	label := terminalText(string(agent), 32)
	if agent == model.AgentClaude {
		return s.claude.Render(label)
	}
	if agent == model.AgentCodex {
		return s.codex.Render(label)
	}
	return label
}

func newStyles() styles {
	tableStyles := table.DefaultStyles()
	tableStyles.Header = lipgloss.NewStyle().Bold(true).PaddingRight(1)
	tableStyles.Cell = lipgloss.NewStyle().PaddingRight(1)
	tableStyles.Selected = lipgloss.NewStyle().Reverse(true).PaddingRight(1)
	result := styles{
		table:     tableStyles,
		muted:     lipgloss.NewStyle().Faint(true),
		estimated: lipgloss.NewStyle().Faint(true),
		emphasis:  lipgloss.NewStyle().Bold(true),
	}
	if _, disabled := os.LookupEnv("NO_COLOR"); !disabled {
		result.claude = lipgloss.NewStyle().Foreground(lipgloss.Color("#D19A66"))
		result.codex = lipgloss.NewStyle().Foreground(lipgloss.Color("#61AFEF"))
		result.warning = lipgloss.NewStyle().Foreground(lipgloss.Color("#E06C75"))
	}
	return result
}
