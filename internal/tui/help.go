package tui

func (m Model) helpView() string {
	lines := []string{
		"j/k ↑/↓ move · / filter",
		"pgup/pgdn page · home/end/g/G edge",
		"s sort · a agent",
		"enter open · r refresh",
		"? help · q/ctrl-c quit",
	}
	if !m.styles.mono {
		lines[3] += " · t theme"
	}
	if m.screen == screenDetail {
		lines = []string{
			"j/k scroll · g/G edge",
			"space/enter expand · J/K subagent",
			"esc/h back",
			"? help · q/ctrl-c quit",
		}
		if !m.styles.mono {
			lines[2] += " · t theme"
		}
	}
	innerWidth := max(0, m.width-2)
	content := make([]panelLine, len(lines))
	for index := range lines {
		plain := fitPlain(lines[index], innerWidth, false)
		content[index] = panelLine{plain: plain, styled: m.styles.keyHint.Render(plain)}
	}
	return renderPanel("Help", "? / esc close", content, m.width, m.height, m.styles)
}
