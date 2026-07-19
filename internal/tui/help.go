package tui

func (m Model) helpView() string {
	listMouseHelp := "mouse wheel scroll · click select · click again open"
	if m.width < 54 {
		listMouseHelp = "wheel · select · again open"
	}
	lines := []string{
		"j/k ↑/↓ move · / filter",
		"pgup/pgdn page · home/end/g/G edge",
		"s sort · a agent",
		"enter open · r refresh",
		listMouseHelp,
		"? help · q/ctrl-c quit",
	}
	if !m.styles.mono {
		lines[3] += " · t theme"
	}
	if m.screen == screenDetail {
		if _, item := m.detail.(*itemView); item {
			lines = []string{
				"j/k scroll · g/G edge",
				"w wrap · esc/h/← back",
				"mouse wheel scroll",
				"? help · q/ctrl-c quit",
			}
			if !m.styles.mono {
				lines[1] += " · t theme"
			}
		} else {
			mouseHelp := "mouse wheel scroll · click select · click again activate"
			if m.width < 59 {
				mouseHelp = "wheel · select · again activate"
			}
			lines = []string{
				"j/k scroll · g/G edge",
				"space toggle · enter open",
				"tab tabs · w wrap",
				"J/K subagent · esc/h back",
				mouseHelp,
				"? help · q/ctrl-c quit",
			}
			if !m.styles.mono {
				lines[3] += " · t theme"
			}
		}
	}
	lines = append(lines, "shift+drag terminal copy")
	innerWidth := max(0, m.width-2)
	content := make([]panelLine, len(lines))
	for index := range lines {
		plain := fitPlain(lines[index], innerWidth, false)
		content[index] = panelLine{plain: plain, styled: m.styles.keyHint.Render(plain)}
	}
	return renderPanel("Help", "? / esc close", content, m.width, m.height, m.styles)
}
