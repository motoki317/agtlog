package tui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const (
	glyphSubagent  = "⑃"
	glyphWarning   = "!"
	glyphCollapsed = "▸"
	glyphExpanded  = "▾"
	glyphAssistant = "●"
	glyphTool      = "⚙"
	glyphSecondary = "◇"
)

type styles struct {
	mono      bool
	border    lipgloss.Style
	title     lipgloss.Style
	header    lipgloss.Style
	row       lipgloss.Style
	selected  lipgloss.Style
	claude    lipgloss.Style
	codex     lipgloss.Style
	muted     lipgloss.Style
	estimated lipgloss.Style
	emphasis  lipgloss.Style
	warning   lipgloss.Style
	accent    lipgloss.Style
	keyHint   lipgloss.Style
}

type Theme struct {
	Name       string
	Background string
	Border     string
	Title      string
	Header     string
	Row        string
	SelectedFg string
	SelectedBg string
	Claude     string
	Codex      string
	Warning    string
	Muted      string
	Estimated  string
	Accent     string
	KeyHint    string
}

var themes = map[string]Theme{
	"default": {
		Name: "default", Background: "#1E222A", Border: "#5C6370", Title: "#61AFEF", Header: "#C678DD", Row: "#ABB2BF",
		SelectedFg: "#FFFFFF", SelectedBg: "#3E4451", Claude: "#D19A66", Codex: "#61AFEF", Warning: "#E06C75",
		Muted: "#5C6370", Estimated: "#7F848E", Accent: "#61AFEF", KeyHint: "#98C379",
	},
	"nord": {
		Name: "nord", Background: "#2E3440", Border: "#4C566A", Title: "#88C0D0", Header: "#E5E9F0", Row: "#D8DEE9",
		SelectedFg: "#ECEFF4", SelectedBg: "#4C566A", Claude: "#88C0D0", Codex: "#81A1C1", Warning: "#BF616A",
		Muted: "#4C566A", Estimated: "#616E88", Accent: "#88C0D0", KeyHint: "#A3BE8C",
	},
	"dracula": {
		Name: "dracula", Background: "#282A36", Border: "#6272A4", Title: "#FF79C6", Header: "#F8F8F2", Row: "#F8F8F2",
		SelectedFg: "#282A36", SelectedBg: "#50FA7B", Claude: "#BD93F9", Codex: "#8BE9FD", Warning: "#FF5555",
		Muted: "#6272A4", Estimated: "#6272A4", Accent: "#50FA7B", KeyHint: "#FFB86C",
	},
}

var colorThemeOrder = []string{"default", "nord", "dracula"}

func cycleTheme(current Theme) Theme {
	if current.Name == "mono" {
		return current
	}
	for index, name := range colorThemeOrder {
		if current.Name == name {
			return themes[colorThemeOrder[(index+1)%len(colorThemeOrder)]]
		}
	}
	return themes["default"]
}

func ResolveTheme(selected string) (Theme, error) {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return Theme{Name: "mono"}, nil
	}
	if selected == "" {
		selected = os.Getenv("AGTLOG_THEME")
	}
	if selected == "" {
		selected = "default"
	}
	switch selected {
	case "default", "nord", "dracula":
		return themes[selected], nil
	default:
		return Theme{}, fmt.Errorf("unknown theme %q (valid: default, nord, dracula)", selected)
	}
}

func newStyles(selected ...Theme) styles {
	theme, err := ResolveTheme("")
	if len(selected) > 0 {
		theme, err = selected[0], nil
	}
	if err != nil {
		theme = themes["default"]
	}
	result := styles{
		mono:      theme.Name == "mono",
		title:     lipgloss.NewStyle().Bold(true),
		header:    lipgloss.NewStyle().Bold(true),
		row:       lipgloss.NewStyle(),
		selected:  lipgloss.NewStyle().Reverse(true),
		muted:     lipgloss.NewStyle().Faint(true),
		estimated: lipgloss.NewStyle().Faint(true),
		emphasis:  lipgloss.NewStyle().Bold(true),
		warning:   lipgloss.NewStyle().Bold(true),
		accent:    lipgloss.NewStyle().Bold(true),
		keyHint:   lipgloss.NewStyle().Faint(true),
	}
	if theme.Name != "mono" && lipgloss.ColorProfile() != termenv.Ascii {
		background := lipgloss.Color(theme.Background)
		result.border = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border)).Background(background)
		result.title = result.title.Foreground(lipgloss.Color(theme.Title)).Background(background)
		result.header = result.header.Foreground(lipgloss.Color(theme.Header)).Background(background)
		result.row = result.row.Foreground(lipgloss.Color(theme.Row)).Background(background)
		result.selected = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.SelectedFg)).Background(lipgloss.Color(theme.SelectedBg))
		result.claude = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Claude)).Background(background)
		result.codex = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Codex)).Background(background)
		result.warning = result.warning.Foreground(lipgloss.Color(theme.Warning)).Background(background)
		result.muted = result.muted.Foreground(lipgloss.Color(theme.Muted)).Background(background)
		result.estimated = result.estimated.Foreground(lipgloss.Color(theme.Estimated)).Background(background)
		result.emphasis = result.emphasis.Foreground(lipgloss.Color(theme.Row)).Background(background)
		result.accent = result.accent.Foreground(lipgloss.Color(theme.Accent)).Background(background)
		result.keyHint = result.keyHint.Foreground(lipgloss.Color(theme.KeyHint)).Background(background)
	}
	return result
}
