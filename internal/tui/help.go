package tui

func (m Model) helpView() string {
	listMouseHelp := "mouse wheel scroll · click select · click again open"
	if m.width < 54 {
		listMouseHelp = "wheel · select · again open"
	}
	lines := []string{
		"j/k ↑/↓ move · / filter",
		"pgup/pgdn page · home/end/g/G edge",
		"←/→ column · ⇧" + sortColumnKey + " sort",
		"⇧" + sortAgeKey + " age · ⇧" + sortTitleKey + " title · a agent · " + timeFormatKey + " time",
		"enter open · r refresh",
		listMouseHelp,
		"? help · q/ctrl-c quit",
	}
	if !m.styles.mono {
		lines[len(lines)-3] += " · t theme"
	}
	if m.screen == screenDetail {
		if _, item := m.detail.(*itemView); item {
			lines = []string{
				"j/k scroll · g/G edge",
				"w wrap · " + timeFormatKey + " time",
				"esc/h back",
				"mouse wheel scroll",
				"? help · q/ctrl-c quit",
			}
			if !m.styles.mono {
				lines[1] += " · t theme"
			}
		} else {
			detail, _ := m.detail.(*detailState)
			if detail != nil && detail.tab == tabOverview {
				lines = []string{
					"j/k move/scroll · g/G edge",
					"pgup/pgdn page",
					"←/→ column · ⇧" + sortColumnKey + " sort",
					"⇧" + sortAgeKey + " age · ⇧" + sortTitleKey + " title",
					"enter/l open",
					"w wrap",
					"tab Timeline/Overview · " + timeFormatKey + " time",
					"esc/h back",
					"mouse wheel scroll · click select",
					"? help · q/ctrl-c quit",
				}
				if !m.styles.mono {
					lines[len(lines)-3] += " · t theme"
				}
			} else {
				mouseHelp := "mouse wheel scroll · click select"
				if m.width < 59 {
					mouseHelp = "wheel · select"
				}
				lines = []string{
					"j/k scroll · g/G edge",
					"←/→ fold · space toggle · enter/l open",
					expandAllKey + " expand all · " + collapseAllKey + " collapse all",
					"tab tabs · w wrap · " + timeFormatKey + " time",
					"esc/h back",
					mouseHelp,
					"? help · q/ctrl-c quit",
				}
				if !m.styles.mono {
					lines[len(lines)-3] += " · t theme"
				}
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
