package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

type detailState struct {
	session      *model.Session
	viewport     viewport.Model
	expanded     map[string]bool
	focus        int
	focusables   []detailFocus
	width        int
	height       int
	loading      bool
	err          error
	styles       styles
	lines        []detailLine
	rendered     []string
	selectedLine int
}

type detailLayout struct {
	headerHeight       int
	timelineHeight     int
	keyBarHeight       int
	compact            bool
	compactPanelHeight int
}

func newDetailLayout(height int) detailLayout {
	height = max(3, height)
	if height < 9 {
		keyBarHeight := 1
		if height == 3 {
			keyBarHeight = 0
		}
		return detailLayout{compact: true, compactPanelHeight: height - keyBarHeight, keyBarHeight: keyBarHeight}
	}
	return detailLayout{headerHeight: 5, timelineHeight: height - 6, keyBarHeight: 1}
}

type detailFocus struct {
	key              string
	line             int
	expandable       bool
	subagent         bool
	containsSubagent bool
}

type detailLine struct {
	text             string
	key              string
	expandable       bool
	subagent         bool
	containsSubagent bool
	role             detailRole
	agent            model.AgentKind
}

type detailRole int

const (
	detailRow detailRole = iota
	detailAccent
	detailAssistant
	detailTool
	detailSecondary
	detailWarning
)

func newDetailState(session *model.Session, width, height int, styles styles) *detailState {
	state := &detailState{session: session, expanded: make(map[string]bool), styles: styles}
	state.viewport = viewport.New(max(1, width-2), max(1, height-8))
	state.resize(width, height)
	return state
}

func (d *detailState) clone() *detailState {
	copy := *d
	copy.expanded = make(map[string]bool, len(d.expanded))
	for key, expanded := range d.expanded {
		copy.expanded[key] = expanded
	}
	copy.focusables = append([]detailFocus(nil), d.focusables...)
	copy.lines = append([]detailLine(nil), d.lines...)
	copy.rendered = append([]string(nil), d.rendered...)
	return &copy
}

func (d *detailState) resize(width, height int) {
	d.width, d.height = max(1, width), max(3, height)
	layout := newDetailLayout(d.height)
	d.viewport.Width = max(1, d.width-2)
	if layout.compact {
		d.viewport.Height = max(1, layout.compactPanelHeight-4)
	} else {
		d.viewport.Height = max(1, layout.timelineHeight-2)
	}
	d.rebuild()
}

func (d *detailState) update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		d.viewport, cmd = d.viewport.Update(msg)
		return cmd
	}
	switch key.String() {
	case "j", "down":
		d.moveFocus(1, false)
	case "k", "up":
		d.moveFocus(-1, false)
	case "J":
		d.moveFocus(1, true)
	case "K":
		d.moveFocus(-1, true)
	case " ", "enter":
		if len(d.focusables) > 0 && d.focusables[d.focus].expandable {
			item := d.focusables[d.focus]
			d.expanded[item.key] = !d.expanded[item.key]
			d.rebuildKeeping(item.key)
		}
	default:
		var cmd tea.Cmd
		d.viewport, cmd = d.viewport.Update(msg)
		return cmd
	}
	return nil
}

func (d *detailState) moveFocus(direction int, subagentsOnly bool) {
	if len(d.focusables) == 0 {
		return
	}
	start := d.focus
	for next := start + direction; next >= 0 && next < len(d.focusables); next += direction {
		if !subagentsOnly || d.focusables[next].subagent {
			oldLine := d.focusables[d.focus].line
			d.focus = next
			d.updateSelection(oldLine, d.focusables[d.focus].line)
			return
		}
	}
	if subagentsOnly {
		for next := start + direction; next >= 0 && next < len(d.focusables); next += direction {
			item := d.focusables[next]
			if !item.containsSubagent {
				continue
			}
			d.expanded[item.key] = true
			d.rebuildKeeping(item.key)
			for child := d.focus + 1; child < len(d.focusables); child++ {
				if d.focusables[child].subagent {
					oldLine := d.focusables[d.focus].line
					d.focus = child
					d.updateSelection(oldLine, d.focusables[child].line)
					return
				}
			}
			return
		}
	}
}

func (d *detailState) rebuildKeeping(key string) {
	d.rebuild()
	for index, item := range d.focusables {
		if item.key == key {
			oldLine := d.selectedLine
			d.focus = index
			d.updateSelection(oldLine, item.line)
			return
		}
	}
}

func (d *detailState) rebuild() {
	var lines []detailLine
	if d.err != nil {
		lines = []detailLine{{text: "detail error: " + terminalText(d.err.Error(), 512), role: detailWarning}}
	} else if d.loading {
		lines = []detailLine{{text: "Loading timeline…", role: detailSecondary}}
	} else {
		lines = d.sessionLines(d.session, 0, sessionIdentity(d.session))
	}
	d.lines = lines
	d.focusables = d.focusables[:0]
	for index, line := range lines {
		if line.key != "" {
			d.focusables = append(d.focusables, detailFocus{key: line.key, line: index, expandable: line.expandable, subagent: line.subagent, containsSubagent: line.containsSubagent})
		}
	}
	if len(d.focusables) == 0 {
		d.focus = 0
	} else if d.focus >= len(d.focusables) {
		d.focus = len(d.focusables) - 1
	}
	selectedLine := -1
	if len(d.focusables) > 0 {
		selectedLine = d.focusables[d.focus].line
	}
	d.rendered = make([]string, len(lines))
	for index, line := range lines {
		prefix := "  "
		if index == selectedLine {
			prefix = "› "
		}
		d.rendered[index] = fitPlain(prefix+line.text, d.viewport.Width, false)
	}
	d.selectedLine = selectedLine
	d.viewport.SetContent(strings.Join(d.rendered, "\n"))
	if selectedLine >= 0 {
		if selectedLine < d.viewport.YOffset {
			d.viewport.SetYOffset(selectedLine)
		} else if selectedLine >= d.viewport.YOffset+d.viewport.Height {
			d.viewport.SetYOffset(selectedLine - d.viewport.Height + 1)
		}
	}
}

func (d *detailState) updateSelection(oldLine, newLine int) {
	if oldLine >= 0 && oldLine < len(d.lines) {
		d.rendered[oldLine] = fitPlain("  "+d.lines[oldLine].text, d.viewport.Width, false)
	}
	if newLine >= 0 && newLine < len(d.lines) {
		d.rendered[newLine] = fitPlain("› "+d.lines[newLine].text, d.viewport.Width, false)
	}
	d.selectedLine = newLine
	d.viewport.SetContent(strings.Join(d.rendered, "\n"))
	if newLine < d.viewport.YOffset {
		d.viewport.SetYOffset(newLine)
	} else if newLine >= d.viewport.YOffset+d.viewport.Height {
		d.viewport.SetYOffset(newLine - d.viewport.Height + 1)
	}
}

func (d *detailState) sessionLines(session *model.Session, indent int, path string) []detailLine {
	var lines []detailLine
	for index := 0; index < len(session.Events); {
		event := session.Events[index]
		if event.Kind == model.EventUser {
			key := fmt.Sprintf("%s/user/%d", path, index)
			lines = append(lines, detailLine{text: strings.Repeat(" ", indent) + glyphCollapsed + " you: " + firstLine(event.Text), key: key, role: detailAccent})
			index++
			continue
		}
		start := index
		for index < len(session.Events) && session.Events[index].Kind != model.EventUser {
			index++
		}
		turn := session.Events[start:index]
		key := fmt.Sprintf("%s/turn/%d", path, start)
		lines = append(lines, detailLine{text: d.turnSummary(session, turn, indent, d.expanded[key]), key: key, expandable: turnExpandable(turn), containsSubagent: turnContainsSubagent(turn), role: detailAssistant, agent: session.Agent})
		if d.expanded[key] {
			for eventIndex, item := range turn {
				lines = append(lines, d.eventLines(session, item, indent+2, fmt.Sprintf("%s/event/%d", key, eventIndex))...)
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, detailLine{text: strings.Repeat(" ", indent) + "No timeline events.", role: detailSecondary})
	}
	return lines
}

func (d *detailState) turnSummary(session *model.Session, events []model.Event, indent int, expanded bool) string {
	thinking, tools, subagents := 0, 0, 0
	last := "completed"
	for _, event := range events {
		switch event.Kind {
		case model.EventThinking:
			thinking++
		case model.EventToolCall:
			tools++
		case model.EventSubagent:
			subagents++
		case model.EventAssistantText:
			last = lastLine(event.Text)
		}
	}
	marker := " "
	if turnExpandable(events) {
		marker = glyphCollapsed
		if expanded {
			marker = glyphExpanded
		}
	}
	parts := []string{glyphAssistant + " " + terminalText(string(session.Agent), 32) + ": " + firstLine(last)}
	if thinking > 0 {
		parts = append(parts, fmt.Sprintf("%d thinking", thinking))
	}
	if tools > 0 {
		parts = append(parts, fmt.Sprintf("%d tools", tools))
	}
	if subagents > 0 {
		parts = append(parts, fmt.Sprintf("%d subagents", subagents))
	}
	return strings.Repeat(" ", indent) + marker + " " + strings.Join(parts, " · ")
}

func (d *detailState) eventLines(session *model.Session, event model.Event, indent int, key string) []detailLine {
	padding := strings.Repeat(" ", indent)
	switch event.Kind {
	case model.EventAssistantText:
		return []detailLine{{text: padding + terminalText(string(session.Agent), 32) + ": " + firstLine(event.Text), key: key, role: detailAssistant, agent: session.Agent}}
	case model.EventThinking:
		return []detailLine{{text: padding + glyphSecondary + " thinking: " + firstLine(event.Text), key: key, role: detailSecondary}}
	case model.EventToolCall:
		return []detailLine{{text: padding + toolLine(event), key: key, role: detailTool}}
	case model.EventSubagent:
		if event.Subagent == nil {
			return []detailLine{{text: padding + glyphSubagent + " subagent unavailable", key: key, role: detailWarning}}
		}
		childKey := key + "/subagent/" + sessionIdentity(event.Subagent)
		marker := glyphCollapsed
		if d.expanded[childKey] {
			marker = glyphExpanded
		}
		label := firstLine(event.ToolInput)
		if label == "" {
			label = firstLine(event.Subagent.Title)
		}
		label = ansi.Truncate(label, 28, "…")
		line := detailLine{text: padding + glyphSubagent + " Task(" + label + ") " + marker + " " + terminalText(shortModels(event.Subagent), 96) + " · " + humanTokens(event.Subagent.TotalUsage().TotalTokens()) + " · " + formatCost(event.Subagent.TotalCost()), key: childKey, expandable: true, subagent: true, role: detailAccent}
		lines := []detailLine{line}
		if d.expanded[childKey] {
			lines = append(lines, d.sessionLines(event.Subagent, indent+2, childKey)...)
		}
		return lines
	case model.EventCompact:
		return []detailLine{{text: padding + glyphSecondary + " compact: " + firstLine(event.Text), key: key, role: detailSecondary}}
	case model.EventSystem:
		return []detailLine{{text: padding + glyphSecondary + " " + firstLine(event.Text), key: key, role: detailSecondary}}
	default:
		return nil
	}
}

func turnExpandable(events []model.Event) bool {
	for _, event := range events {
		if event.Kind == model.EventThinking || event.Kind == model.EventToolCall || event.Kind == model.EventSubagent || event.Kind == model.EventCompact {
			return true
		}
	}
	return false
}

func turnContainsSubagent(events []model.Event) bool {
	for _, event := range events {
		if event.Kind == model.EventSubagent {
			return true
		}
	}
	return false
}

func toolLine(event model.Event) string {
	name := terminalText(event.ToolName, 96)
	if name == "exec_command" {
		name = "Bash"
	} else if name == "apply_patch" {
		name = "Edit"
	}
	line := glyphTool + " " + name
	if input := firstLine(event.ToolInput); input != "" {
		line += "(" + input + ")"
	}
	if event.ResultSummary != "" {
		line += " → " + firstLine(event.ResultSummary)
	}
	if event.Duration > 0 {
		line += " · " + formatDuration(event.Duration)
	}
	return line
}

func formatDuration(duration time.Duration) string {
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", duration.Seconds())
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = terminalText(line, 512); line != "" {
			return line
		}
	}
	return ""
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return terminalText(line, 512)
		}
	}
	return "completed"
}

func (d *detailState) view() string {
	layout := newDetailLayout(d.height)
	if layout.compact {
		return d.compactView(layout)
	}
	header := d.header()
	timelineHeight := layout.timelineHeight
	visiblePlain := strings.Split(d.viewport.View(), "\n")
	visible := make([]panelLine, len(visiblePlain))
	for index, plain := range visiblePlain {
		lineIndex := d.viewport.YOffset + index
		if lineIndex >= len(d.lines) {
			visible[index] = panelLine{plain: plain, styled: d.styles.row.Render(plain)}
			continue
		}
		visible[index] = panelLine{plain: plain, styled: d.styleLine(plain, d.lines[lineIndex], lineIndex == d.selectedLine)}
	}
	for len(visible) < max(0, timelineHeight-2) {
		plain := strings.Repeat(" ", d.viewport.Width)
		visible = append(visible, panelLine{plain: plain, styled: d.styles.row.Render(plain)})
	}
	if len(visible) > max(0, timelineHeight-2) {
		visible = visible[:timelineHeight-2]
	}
	hint := ""
	if len(d.lines) > d.viewport.Height {
		hint = fmt.Sprintf("%d/%d", min(len(d.lines), d.viewport.YOffset+1), len(d.lines))
	}
	timeline := renderPanel("Timeline", hint, visible, d.width, timelineHeight, d.styles)
	keyText := "j/k scroll   ↵ expand   J/K subagent   esc back"
	if !d.styles.mono {
		keyText += "   t theme"
	}
	keyText += "   ? help   q quit"
	keyBar := d.styles.keyHint.Render(fitPlain(keyText, d.width, false))
	return strings.Join([]string{header, timeline, keyBar}, "\n")
}

func (d *detailState) compactView(layout detailLayout) string {
	capacity := max(0, layout.compactPanelHeight-2)
	headerLines := d.headerPanelLines()
	content := make([]panelLine, 0, capacity)
	for _, index := range []int{0, 2, 1} {
		if len(content) >= capacity {
			break
		}
		content = append(content, headerLines[index])
	}
	for lineIndex := d.viewport.YOffset; len(content) < capacity && lineIndex < len(d.rendered); lineIndex++ {
		plain := fitPlain(d.rendered[lineIndex], max(0, d.width-2), false)
		content = append(content, panelLine{plain: plain, styled: d.styleLine(plain, d.lines[lineIndex], lineIndex == d.selectedLine)})
	}
	for len(content) < capacity {
		plain := strings.Repeat(" ", max(0, d.width-2))
		content = append(content, panelLine{plain: plain, styled: d.styles.row.Render(plain)})
	}
	panel := renderPanel("Session · Timeline", "", content, d.width, layout.compactPanelHeight, d.styles)
	if layout.keyBarHeight == 0 {
		return panel
	}
	keyBar := d.styles.keyHint.Render(fitPlain("j/k scroll   esc back   ? help   q quit", d.width, false))
	return panel + "\n" + keyBar
}

func (d *detailState) header() string {
	return renderPanel("Session", "", d.headerPanelLines(), d.width, 5, d.styles)
}

func (d *detailState) headerPanelLines() []panelLine {
	session := d.session
	totalUsage := session.TotalUsage()
	var ownUsage model.Usage
	for _, usage := range session.Usage {
		ownUsage = ownUsage.Add(usage)
	}
	totalCost := session.TotalCost()
	subagentCost := model.Cost{}
	missing := make(map[string]bool)
	for _, subagent := range session.Subagents {
		cost := subagent.TotalCost()
		subagentCost.USD += cost.USD
		subagentCost.Estimated = subagentCost.Estimated || cost.Estimated
		for _, name := range cost.MissingPricingModels {
			if !missing[name] {
				subagentCost.MissingPricingModels = append(subagentCost.MissingPricingModels, name)
				missing[name] = true
			}
		}
	}
	line1 := fmt.Sprintf("%s · %s (%s)", terminalText(string(session.Agent), 32), terminalText(session.Project, 96), terminalText(session.CWD, 256))
	line2 := fmt.Sprintf("%s · %s · %s→%s", terminalText(detailModels(session), 256), terminalText(session.GitBranch, 96), formatDetailTime(session.StartedAt), formatDetailTime(session.UpdatedAt))
	line3 := fmt.Sprintf("%s tokens / %s · own %s / %s · subagents %s / %s",
		humanTokens(totalUsage.TotalTokens()), formatCost(totalCost), humanTokens(ownUsage.TotalTokens()), formatCost(session.Cost),
		humanTokens(totalUsage.TotalTokens()-ownUsage.TotalTokens()), formatCost(subagentCost))
	innerWidth := max(0, d.width-2)
	plainLines := []string{
		fitPlain(line1, innerWidth, false),
		fitPlain(line2, innerWidth, false),
		fitPlain(line3, innerWidth, false),
	}
	lines := []panelLine{
		{plain: plainLines[0], styled: d.agentStyle(session.Agent).Render(plainLines[0])},
		{plain: plainLines[1], styled: d.styles.row.Render(plainLines[1])},
		{plain: plainLines[2], styled: d.styles.emphasis.Render(plainLines[2])},
	}
	return lines
}

func (d *detailState) styleLine(line string, detail detailLine, selected bool) string {
	if selected {
		return d.styles.selected.Render(line)
	}
	switch detail.role {
	case detailAccent, detailTool:
		return d.styles.accent.Render(line)
	case detailAssistant:
		return d.agentStyle(detail.agent).Render(line)
	case detailSecondary:
		return d.styles.muted.Render(line)
	case detailWarning:
		return d.styles.warning.Render(line)
	default:
		return d.styles.row.Render(line)
	}
}

func (d *detailState) agentStyle(agent model.AgentKind) lipgloss.Style {
	if agent == model.AgentClaude {
		return d.styles.claude
	}
	if agent == model.AgentCodex {
		return d.styles.codex
	}
	return d.styles.row
}

func detailModels(session *model.Session) string {
	if len(session.Models) == 0 {
		return "—"
	}
	missing := make(map[string]bool, len(session.Cost.MissingPricingModels))
	for _, name := range session.Cost.MissingPricingModels {
		missing[name] = true
	}
	labels := make([]string, 0, len(session.Models))
	for _, name := range session.Models {
		label := shortModelName(name)
		if missing[name] {
			label += "!"
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, ", ")
}

func formatDetailTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Format("Jan 02 15:04")
}
