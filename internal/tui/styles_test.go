package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/muesli/termenv"
)

func TestResolveThemeUsesFlagBeforeEnvironment(t *testing.T) {
	t.Setenv("AGTLOG_THEME", "nord")
	unsetEnv(t, "NO_COLOR")

	theme, err := ResolveTheme("dracula")
	if err != nil {
		t.Fatal(err)
	}
	if theme.Name != "dracula" {
		t.Fatalf("ResolveTheme() = %q, want dracula", theme.Name)
	}
}

func TestResolveThemeUsesEnvironmentBeforeDefault(t *testing.T) {
	t.Setenv("AGTLOG_THEME", "nord")
	unsetEnv(t, "NO_COLOR")

	theme, err := ResolveTheme("")
	if err != nil {
		t.Fatal(err)
	}
	if theme.Name != "nord" {
		t.Fatalf("ResolveTheme() = %q, want nord", theme.Name)
	}
}

func TestResolveThemeFallsBackToDefault(t *testing.T) {
	unsetEnv(t, "AGTLOG_THEME")
	unsetEnv(t, "NO_COLOR")

	theme, err := ResolveTheme("")
	if err != nil {
		t.Fatal(err)
	}
	if theme.Name != "default" {
		t.Fatalf("ResolveTheme() = %q, want default", theme.Name)
	}
}

func TestResolveThemeForcesMonoWhenNoColorIsSet(t *testing.T) {
	t.Setenv("AGTLOG_THEME", "nord")
	t.Setenv("NO_COLOR", "1")

	theme, err := ResolveTheme("dracula")
	if err != nil {
		t.Fatal(err)
	}
	if theme.Name != "mono" {
		t.Fatalf("ResolveTheme() = %q, want mono", theme.Name)
	}
}

func TestResolveThemeRejectsUnknownName(t *testing.T) {
	unsetEnv(t, "NO_COLOR")

	_, err := ResolveTheme("solarized")
	if err == nil || !strings.Contains(err.Error(), "default, nord, dracula") {
		t.Fatalf("ResolveTheme() error = %v, want valid theme names", err)
	}
}

func TestBuiltInThemesDefineSemanticPalettes(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	tests := []struct {
		name, claude, codex, warning, muted, estimated, diffAdd, diffRemove, selectedFg, selectedBg, background, userBg, systemBg string
	}{
		{name: "default", claude: "#D19A66", codex: "#61AFEF", warning: "#E06C75", muted: "#8A94A6", estimated: "#8A94A6", diffAdd: "#98C379", diffRemove: "#E06C75", selectedFg: "#FFFFFF", selectedBg: "#3E4451", background: "#1E222A", userBg: "#262B33", systemBg: "#2B2A26"},
		{name: "nord", claude: "#88C0D0", codex: "#81A1C1", warning: "#BF616A", muted: "#8590A8", estimated: "#8590A8", diffAdd: "#A3BE8C", diffRemove: "#BF616A", selectedFg: "#ECEFF4", selectedBg: "#4C566A", background: "#2E3440", userBg: "#353C4A", systemBg: "#3B4252"},
		{name: "dracula", claude: "#BD93F9", codex: "#8BE9FD", warning: "#FF5555", muted: "#7A85B8", estimated: "#7A85B8", diffAdd: "#7FB894", diffRemove: "#E86A6A", selectedFg: "#F8F8F2", selectedBg: "#44475A", background: "#282A36", userBg: "#2E3040", systemBg: "#343646"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			theme, err := ResolveTheme(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if theme.Claude != test.claude || theme.Codex != test.codex || theme.Warning != test.warning || theme.Background != test.background {
				t.Fatalf("ResolveTheme(%q) = %#v", test.name, theme)
			}
			if theme.DiffAdd != test.diffAdd || theme.DiffRemove != test.diffRemove {
				t.Fatalf("ResolveTheme(%q) diff colors = %q/%q, want %q/%q", test.name, theme.DiffAdd, theme.DiffRemove, test.diffAdd, test.diffRemove)
			}
			if theme.Muted != test.muted || theme.Estimated != test.estimated {
				t.Fatalf("ResolveTheme(%q) dim colors = %q/%q, want %q/%q", test.name, theme.Muted, theme.Estimated, test.muted, test.estimated)
			}
			if theme.SelectedFg != test.selectedFg || theme.SelectedBg != test.selectedBg {
				t.Fatalf("ResolveTheme(%q) selection colors = %q/%q, want %q/%q", test.name, theme.SelectedFg, theme.SelectedBg, test.selectedFg, test.selectedBg)
			}
			if theme.UserBg != test.userBg || theme.SystemBg != test.systemBg {
				t.Fatalf("ResolveTheme(%q) prompt backgrounds = %q/%q, want %q/%q", test.name, theme.UserBg, theme.SystemBg, test.userBg, test.systemBg)
			}
			if theme.UserBg == theme.SystemBg || theme.UserBg == theme.Background || theme.SystemBg == theme.Background || theme.UserBg == theme.SelectedBg || theme.SystemBg == theme.SelectedBg {
				t.Fatalf("ResolveTheme(%q) prompt backgrounds are not distinct: %#v", test.name, theme)
			}
			for role, color := range map[string]string{
				"border": theme.Border, "title": theme.Title, "header": theme.Header,
				"row": theme.Row, "selected foreground": theme.SelectedFg, "selected background": theme.SelectedBg,
				"muted": theme.Muted, "estimated": theme.Estimated, "accent": theme.Accent, "key hint": theme.KeyHint,
			} {
				if color == "" {
					t.Errorf("%s theme has empty %s role", test.name, role)
				}
			}
		})
	}
}

func TestCycleThemeFollowsBuiltInOrder(t *testing.T) {
	for _, test := range []struct{ current, want string }{
		{current: "default", want: "nord"},
		{current: "nord", want: "dracula"},
		{current: "dracula", want: "default"},
		{current: "mono", want: "mono"},
	} {
		got := cycleTheme(Theme{Name: test.current})
		if got.Name != test.want {
			t.Errorf("cycleTheme(%q) = %q, want %q", test.current, got.Name, test.want)
		}
	}
}

func TestUIGlyphsHaveStableDisplayWidths(t *testing.T) {
	glyphs := map[string]int{
		glyphSubagent: 1, glyphWarning: 1, glyphCollapsed: 1, glyphExpanded: 1,
		glyphTool: 1, glyphSecondary: 1,
		"⑂": 1, "↵": 1, "▊": 1, "›": 1, "→": 1, "·": 1,
		"—": 1, "…": 1, "∞": 1, "↑": 1, "↓": 1,
	}
	border := widthSafeBorder(lipgloss.RoundedBorder())
	for _, glyph := range []string{border.Top, border.Bottom, border.Left, border.Right, border.TopLeft, border.TopRight, border.BottomLeft, border.BottomRight} {
		glyphs[glyph] = 1
	}
	for glyph, want := range glyphs {
		if got := ansi.StringWidth(glyph); got != want {
			t.Errorf("ansi.StringWidth(%q) = %d, want %d", glyph, got, want)
		}
	}
}

func TestSortedFocusedHeaderCellUsesArrowAtOneCellWidth(t *testing.T) {
	styleSet := newStyles(Theme{Name: "mono"})
	cell := renderHeaderCell(
		listColumn{kind: columnAgent, title: "AGENT", width: 1},
		sortState{kind: columnAgent, active: true},
		true,
		styleSet,
	)

	if cell.plain != "↑" || ansi.StringWidth(cell.plain) != 1 {
		t.Fatalf("one-cell sorted header = %q (width %d), want ↑", cell.plain, ansi.StringWidth(cell.plain))
	}
	want := styleSet.selected.Bold(true).Render("↑")
	if cell.styled != want {
		t.Fatalf("focused sorted header style = %q, want %q", cell.styled, want)
	}
}

func TestSortedLeftAlignedHeaderKeepsArrowBesideTitle(t *testing.T) {
	cell := renderHeaderCell(
		listColumn{kind: columnTitle, title: "TITLE", width: 12},
		sortState{kind: columnTitle, active: true},
		false,
		newStyles(Theme{Name: "mono"}),
	)

	if cell.plain != "TITLE↑      " {
		t.Fatalf("left-aligned sorted header = %q, want arrow next to title", cell.plain)
	}
	if got := ansi.StringWidth(cell.plain); got != 12 {
		t.Fatalf("left-aligned sorted header width = %d, want 12", got)
	}
}

func TestThemeKeyCyclesActivePalette(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, themes["default"])
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = updated.(Model)

	if m.ThemeName() != "nord" {
		t.Fatalf("theme after t = %q, want nord", m.ThemeName())
	}
}

func TestThemeKeyIsNoopForMonoTheme(t *testing.T) {
	m := newModelWithClockAndTheme(nil, nil, time.Now, Theme{Name: "mono"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = updated.(Model)

	if m.ThemeName() != "mono" {
		t.Fatalf("theme after t = %q, want mono", m.ThemeName())
	}
}

func TestMonoViewsExplainThemeWithoutAdvertisingCycle(t *testing.T) {
	m := newModelWithClockAndTheme([]*model.Session{{ID: "lunar"}}, nil, time.Now, Theme{Name: "mono"})
	list := ansi.Strip(m.View())
	detail := ansi.Strip(newDetailState(m.sessions[0], 80, 12, m.styles).view())
	help := ansi.Strip(m.helpView())
	for name, view := range map[string]string{"list": list, "detail": detail, "help": help} {
		if strings.Contains(view, "t theme") {
			t.Errorf("mono %s still advertises theme cycling:\n%s", name, view)
		}
	}
	if !strings.Contains(list, "theme:mono") {
		t.Fatalf("mono list does not explain active theme:\n%s", list)
	}
}

func TestColorThemeStylesUsePaletteBackground(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	styleSet := newStyles(themes["nord"])
	if got := fmt.Sprint(styleSet.row.GetBackground()); got != "#2E3440" {
		t.Fatalf("Nord row background = %q, want #2E3440", got)
	}
}

func TestSecondaryStylesUseColorWithoutFaint(t *testing.T) {
	styleSet := newStyles(themes["default"])
	for name, style := range map[string]lipgloss.Style{
		"muted": styleSet.muted, "estimated": styleSet.estimated, "key hint": styleSet.keyHint,
	} {
		if style.GetFaint() {
			t.Errorf("%s style is faint, want color-only hierarchy", name)
		}
	}
}

func TestDiffStylesUseSemanticPalette(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	styleSet := newStyles(themes["default"])
	if got := fmt.Sprint(styleSet.diffAdd.GetForeground()); got != "#98C379" {
		t.Fatalf("diff add foreground = %q, want #98C379", got)
	}
	if got := fmt.Sprint(styleSet.diffRemove.GetForeground()); got != "#E06C75" {
		t.Fatalf("diff remove foreground = %q, want #E06C75", got)
	}
	if styleSet.diffRemove.GetFaint() {
		t.Fatal("diff remove is faint, want solid red")
	}
}

func TestDiffAddStyleAvoidsBold(t *testing.T) {
	if styleSet := newStyles(themes["default"]); styleSet.diffAdd.GetBold() {
		t.Fatal("diff add style is bold, want color-only emphasis")
	}
}

func TestMonoDiffStylesAvoidColor(t *testing.T) {
	styleSet := newStyles(Theme{Name: "mono"})
	if _, ok := styleSet.diffAdd.GetForeground().(lipgloss.NoColor); !ok {
		t.Fatalf("mono diff add foreground = %#v, want no color", styleSet.diffAdd.GetForeground())
	}
	if _, ok := styleSet.diffRemove.GetForeground().(lipgloss.NoColor); !ok {
		t.Fatalf("mono diff remove foreground = %#v, want no color", styleSet.diffRemove.GetForeground())
	}
	if styleSet.diffAdd.GetBold() || styleSet.diffRemove.GetFaint() {
		t.Fatal("mono diff styles must avoid terminal intensity attributes")
	}
}

func TestMonoProseAndPromptStylesAvoidColor(t *testing.T) {
	styleSet := newStyles(Theme{Name: "mono"})
	for name, style := range map[string]lipgloss.Style{
		"row": styleSet.row, "user prompt": styleSet.userPrompt, "system prompt": styleSet.systemPrompt,
	} {
		if _, ok := style.GetForeground().(lipgloss.NoColor); !ok {
			t.Errorf("mono %s foreground = %#v, want no color", name, style.GetForeground())
		}
		if _, ok := style.GetBackground().(lipgloss.NoColor); !ok {
			t.Errorf("mono %s background = %#v, want no color", name, style.GetBackground())
		}
	}
}

func TestAsciiProfileKeepsSelectedRowsDistinct(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	styleSet := newStyles(themes["default"])
	if !styleSet.selected.GetReverse() {
		t.Fatal("ASCII profile selection does not use reverse video")
	}
}

func TestPromptBackgroundsStayDistinctAcrossColorProfiles(t *testing.T) {
	profile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	for _, name := range colorThemeOrder {
		for _, candidate := range []termenv.Profile{termenv.ANSI, termenv.ANSI256, termenv.TrueColor} {
			lipgloss.SetColorProfile(candidate)
			styleSet := newStyles(themes[name])
			backgrounds := []string{
				lipgloss.NewStyle().Background(styleSet.row.GetBackground()).Render("x"),
				lipgloss.NewStyle().Background(styleSet.userPrompt.GetBackground()).Render("x"),
				lipgloss.NewStyle().Background(styleSet.systemPrompt.GetBackground()).Render("x"),
				lipgloss.NewStyle().Background(styleSet.selected.GetBackground()).Render("x"),
			}
			for left := range backgrounds {
				for right := left + 1; right < len(backgrounds); right++ {
					if backgrounds[left] == backgrounds[right] {
						t.Errorf("%s profile %v backgrounds %d and %d collapse to %q", name, candidate, left, right, backgrounds[left])
					}
				}
			}
		}
	}
}

func TestUnsafeRoundedBorderFallsBackToASCII(t *testing.T) {
	unsafe := lipgloss.RoundedBorder()
	unsafe.Top = "界"
	border := widthSafeBorder(unsafe)
	if border.Top != "-" || border.Left != "|" || border.TopLeft != "+" {
		t.Fatalf("unsafe border fallback = %#v, want ASCII", border)
	}
}

func TestEveryThemeRendersListAndDetailAtNarrowAndWideSizes(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude, Project: "observatory", CWD: "/workspace/observatory", Title: "Map lunar craters", Models: []string{"claude-opus-4-8"},
		Events: []model.Event{{Kind: model.EventUser, Text: "Inspect the ridge"}},
	}
	for _, name := range colorThemeOrder {
		for _, size := range []struct{ width, height int }{{width: 40, height: 12}, {width: 160, height: 40}} {
			t.Run(fmt.Sprintf("%s-%d", name, size.width), func(t *testing.T) {
				m := newModelWithClockAndTheme([]*model.Session{session}, nil, time.Now, themes[name])
				updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				m = updated.(Model)
				for _, view := range []string{m.View(), newDetailState(session, size.width, size.height, m.styles).view()} {
					lines := strings.Split(ansi.Strip(view), "\n")
					if len(lines) != size.height {
						t.Fatalf("rendered height = %d, want %d", len(lines), size.height)
					}
					for lineNumber, line := range lines {
						if got := ansi.StringWidth(line); got > size.width {
							t.Fatalf("line %d width = %d, want <= %d", lineNumber+1, got, size.width)
						}
					}
				}
			})
		}
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	value, present := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
