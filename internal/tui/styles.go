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
	glyphTool      = "⚙"
	glyphSecondary = "◇"
)

type styles struct {
	mono         bool
	border       lipgloss.Style
	title        lipgloss.Style
	header       lipgloss.Style
	row          lipgloss.Style
	selected     lipgloss.Style
	claude       lipgloss.Style
	codex        lipgloss.Style
	muted        lipgloss.Style
	estimated    lipgloss.Style
	emphasis     lipgloss.Style
	warning      lipgloss.Style
	accent       lipgloss.Style
	keyHint      lipgloss.Style
	diffAdd      lipgloss.Style
	diffRemove   lipgloss.Style
	userPrompt   lipgloss.Style
	systemPrompt lipgloss.Style
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
	DiffAdd    string
	DiffRemove string
	UserBg     string
	SystemBg   string
}

var themes = map[string]Theme{
	"default": {
		Name: "default", Background: "#1E222A", Border: "#5C6370", Title: "#61AFEF", Header: "#C678DD", Row: "#ABB2BF",
		SelectedFg: "#FFFFFF", SelectedBg: "#3E4451", Claude: "#D19A66", Codex: "#61AFEF", Warning: "#E06C75",
		Muted: "#8A94A6", Estimated: "#8A94A6", Accent: "#61AFEF", KeyHint: "#98C379", DiffAdd: "#98C379", DiffRemove: "#E06C75",
		UserBg: "#262B33", SystemBg: "#2B2A26",
	},
	"nord": {
		Name: "nord", Background: "#2E3440", Border: "#4C566A", Title: "#88C0D0", Header: "#E5E9F0", Row: "#D8DEE9",
		SelectedFg: "#ECEFF4", SelectedBg: "#4C566A", Claude: "#88C0D0", Codex: "#81A1C1", Warning: "#BF616A",
		Muted: "#8590A8", Estimated: "#8590A8", Accent: "#88C0D0", KeyHint: "#A3BE8C", DiffAdd: "#A3BE8C", DiffRemove: "#BF616A",
		UserBg: "#353C4A", SystemBg: "#3B4252",
	},
	"dracula": {
		Name: "dracula", Background: "#282A36", Border: "#6272A4", Title: "#FF79C6", Header: "#F8F8F2", Row: "#F8F8F2",
		SelectedFg: "#F8F8F2", SelectedBg: "#44475A", Claude: "#BD93F9", Codex: "#8BE9FD", Warning: "#FF5555",
		Muted: "#7A85B8", Estimated: "#7A85B8", Accent: "#50FA7B", KeyHint: "#FFB86C", DiffAdd: "#7FB894", DiffRemove: "#E86A6A",
		UserBg: "#2E3040", SystemBg: "#343646",
	},
}

type promptFallback struct {
	userANSI256   string
	systemANSI256 string
	userANSI      string
	systemANSI    string
}

var promptFallbacks = map[string]promptFallback{
	"default": {userANSI256: "233", systemANSI256: "234", userANSI: "4", systemANSI: "5"},
	"nord":    {userANSI256: "24", systemANSI256: "60", userANSI: "4", systemANSI: "5"},
	"dracula": {userANSI256: "23", systemANSI256: "236", userANSI: "6", systemANSI: "5"},
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
		mono:         theme.Name == "mono",
		title:        lipgloss.NewStyle().Bold(true),
		header:       lipgloss.NewStyle().Bold(true),
		row:          lipgloss.NewStyle(),
		selected:     lipgloss.NewStyle().Reverse(true),
		muted:        lipgloss.NewStyle(),
		estimated:    lipgloss.NewStyle(),
		emphasis:     lipgloss.NewStyle().Bold(true),
		warning:      lipgloss.NewStyle().Bold(true),
		accent:       lipgloss.NewStyle().Bold(true),
		keyHint:      lipgloss.NewStyle(),
		diffAdd:      lipgloss.NewStyle(),
		diffRemove:   lipgloss.NewStyle(),
		userPrompt:   lipgloss.NewStyle(),
		systemPrompt: lipgloss.NewStyle(),
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
		result.diffAdd = result.diffAdd.Foreground(lipgloss.Color(theme.DiffAdd)).Background(background)
		result.diffRemove = result.diffRemove.Foreground(lipgloss.Color(theme.DiffRemove)).Background(background)
		userBackground, systemBackground := promptBackgrounds(theme)
		result.userPrompt = result.userPrompt.Foreground(lipgloss.Color(theme.Row)).Background(userBackground)
		result.systemPrompt = result.systemPrompt.Foreground(lipgloss.Color(theme.Row)).Background(systemBackground)
	}
	return result
}

func promptBackgrounds(theme Theme) (lipgloss.TerminalColor, lipgloss.TerminalColor) {
	fallback, ok := promptFallbacks[theme.Name]
	if !ok {
		return lipgloss.Color(theme.UserBg), lipgloss.Color(theme.SystemBg)
	}
	return lipgloss.CompleteColor{TrueColor: theme.UserBg, ANSI256: fallback.userANSI256, ANSI: fallback.userANSI},
		lipgloss.CompleteColor{TrueColor: theme.SystemBg, ANSI256: fallback.systemANSI256, ANSI: fallback.systemANSI}
}
