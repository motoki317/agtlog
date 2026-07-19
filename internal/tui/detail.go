package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

type detailState struct {
	session           *model.Session
	crumbs            []string
	viewport          viewport.Model
	expanded          map[string]bool
	defaultExpanded   bool
	focus             int
	focusables        []detailFocus
	width             int
	height            int
	loading           bool
	err               error
	styles            styles
	wrap              bool
	tab               detailTab
	subagentSelection int
	subagents         []flattenedSubagent
	lines             []detailLine
	rendered          []renderedRow
	renderedStarts    []int
	selectedLine      int
}

type renderedRow struct {
	detailIndex int
	text        string
	first       bool
}

type detailLayout struct {
	headerHeight        int
	timelineHeight      int
	keyBarHeight        int
	contentY            int
	contentHeight       int
	compact             bool
	compactPanelHeight  int
	compactHeaderHeight int
}

func newDetailLayout(height int) detailLayout {
	height = max(3, height)
	if height < 9 {
		keyBarHeight := 1
		if height == 3 {
			keyBarHeight = 0
		}
		panelHeight := height - keyBarHeight
		capacity := max(0, panelHeight-2)
		headerHeight := min(3, capacity)
		return detailLayout{
			compact: true, compactPanelHeight: panelHeight, compactHeaderHeight: headerHeight,
			keyBarHeight: keyBarHeight, contentY: 1 + headerHeight, contentHeight: capacity - headerHeight,
		}
	}
	headerHeight := 5
	timelineHeight := height - 6
	return detailLayout{
		headerHeight: headerHeight, timelineHeight: timelineHeight, keyBarHeight: 1,
		contentY: headerHeight + 1, contentHeight: timelineHeight - 2,
	}
}

func (d *detailState) rowAtY(y int) (int, bool) {
	layout := newDetailLayout(d.height)
	visibleRow := y - layout.contentY
	if visibleRow < 0 || visibleRow >= layout.contentHeight {
		return 0, false
	}
	renderedIndex := d.viewport.YOffset + visibleRow
	if renderedIndex < 0 || renderedIndex >= len(d.rendered) {
		return 0, false
	}
	detailIndex := d.rendered[renderedIndex].detailIndex
	if d.tab == tabSubagents {
		if detailIndex >= 0 && detailIndex < len(d.subagents) {
			return detailIndex, true
		}
		return 0, false
	}
	focus := -1
	for index, item := range d.focusables {
		if item.line > detailIndex {
			break
		}
		focus = index
	}
	return focus, focus >= 0
}

type detailFocus struct {
	key             string
	line            int
	expandable      bool
	subagent        bool
	subagentSession *model.Session
	event           model.Event
}

type detailLine struct {
	text            string
	label           string
	key             string
	expandable      bool
	subagent        bool
	subagentSession *model.Session
	subagentTokens  string
	subagentCost    string
	role            detailRole
	agent           model.AgentKind
	event           model.Event
}

type detailRole int

type detailTab int

type flattenedSubagent struct {
	depth int
	s     *model.Session
}

const detailPreviewLineCap = 40

func newViewport(width, height int) viewport.Model {
	view := viewport.New(width, height)
	view.MouseWheelDelta = mouseWheelRows
	return view
}

func scrollViewport(view *viewport.Model, button tea.MouseButton) {
	switch button {
	case tea.MouseButtonWheelUp:
		view.ScrollUp(mouseWheelRows)
	case tea.MouseButtonWheelDown:
		view.ScrollDown(mouseWheelRows)
	}
}

const (
	tabTimeline detailTab = iota
	tabSubagents
)

const (
	detailRow detailRole = iota
	detailAccent
	detailAssistant
	detailUserPrompt
	detailSystemPrompt
	detailTool
	detailSecondary
	detailWarning
	detailDiffAdd
	detailDiffRemove
	detailDiffContext
)

func newDetailState(session *model.Session, width, height int, styles styles) *detailState {
	state := newDetailStateBase(session, width, height, styles)
	state.focus = -1
	state.resize(width, height)
	state.viewport.GotoBottom()
	return state
}

func newDetailStateBase(session *model.Session, width, height int, styles styles) *detailState {
	state := &detailState{session: session, expanded: make(map[string]bool), defaultExpanded: true, styles: styles, wrap: true}
	if project := terminalText(session.Project, 96); project != "" {
		state.crumbs = []string{project}
	}
	state.viewport = newViewport(max(1, width-2), max(1, height-8))
	return state
}

func (d *detailState) isExpanded(key string) bool {
	if expanded, ok := d.expanded[key]; ok {
		return expanded
	}
	return d.defaultExpanded
}

func (d *detailState) clone() *detailState {
	copy := *d
	copy.expanded = make(map[string]bool, len(d.expanded))
	for key, expanded := range d.expanded {
		copy.expanded[key] = expanded
	}
	copy.focusables = append([]detailFocus(nil), d.focusables...)
	copy.crumbs = append([]string(nil), d.crumbs...)
	copy.lines = append([]detailLine(nil), d.lines...)
	copy.rendered = append([]renderedRow(nil), d.rendered...)
	copy.renderedStarts = append([]int(nil), d.renderedStarts...)
	return &copy
}

func (d *detailState) scrollWheel(button tea.MouseButton) {
	scrollViewport(&d.viewport, button)
}

func (d *detailState) resize(width, height int) {
	pinned := d.tab == tabTimeline && len(d.rendered) > 0 && d.pinnedToBottom()
	d.width, d.height = max(1, width), max(3, height)
	layout := newDetailLayout(d.height)
	d.viewport.Width = max(1, d.width-2)
	d.viewport.Height = max(1, layout.contentHeight)
	d.rebuild()
	if pinned {
		d.viewport.GotoBottom()
	}
}

func (d *detailState) setWrap(wrap bool) {
	if d.wrap == wrap {
		return
	}
	d.wrap = wrap
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
	case "tab", "shift+tab":
		d.tab = (d.tab + 1) % 2
		d.viewport.SetYOffset(0)
		d.rebuild()
	case "j", "down":
		if d.tab == tabSubagents {
			d.moveSubagentSelection(1)
		} else {
			d.moveFocus(1, false)
		}
	case "k", "up":
		if d.tab == tabSubagents {
			d.moveSubagentSelection(-1)
		} else {
			d.moveFocus(-1, false)
		}
	case "g":
		if d.tab == tabSubagents {
			if len(d.subagents) > 0 && d.subagentSelection != 0 {
				oldLine := d.selectedLine
				d.subagentSelection = 0
				d.updateSelection(oldLine, 0)
			}
			d.viewport.GotoTop()
		} else if len(d.focusables) > 0 {
			oldLine := d.focusables[d.focus].line
			d.focus = 0
			d.updateSelection(oldLine, d.focusables[d.focus].line)
		}
	case "G":
		if d.tab == tabSubagents {
			last := len(d.subagents) - 1
			if last >= 0 && d.subagentSelection != last {
				oldLine := d.selectedLine
				d.subagentSelection = last
				d.updateSelection(oldLine, last)
			}
			d.viewport.GotoBottom()
		} else if len(d.focusables) > 0 {
			d.gotoBottom()
		}
	case "J":
		if d.tab == tabTimeline {
			d.moveFocus(1, true)
		}
	case "K":
		if d.tab == tabTimeline {
			d.moveFocus(-1, true)
		}
	case " ":
		if d.tab == tabTimeline && len(d.focusables) > 0 && d.focusables[d.focus].expandable {
			item := d.focusables[d.focus]
			d.expanded[item.key] = !d.isExpanded(item.key)
			d.rebuildKeeping(item.key)
		}
	case "w":
		d.setWrap(!d.wrap)
	default:
		var cmd tea.Cmd
		d.viewport, cmd = d.viewport.Update(msg)
		return cmd
	}
	return nil
}

func (d *detailState) pinnedToBottom() bool {
	return d.viewport.AtBottom()
}

func (d *detailState) gotoBottom() {
	if len(d.focusables) > 0 {
		oldLine := d.selectedLine
		d.focus = len(d.focusables) - 1
		d.updateSelection(oldLine, d.focusables[d.focus].line)
	}
	d.viewport.GotoBottom()
}

func (d *detailState) moveSubagentSelection(delta int) {
	if len(d.subagents) == 0 {
		return
	}
	next := max(0, min(d.subagentSelection+delta, len(d.subagents)-1))
	if next == d.subagentSelection {
		return
	}
	oldLine := d.selectedLine
	d.subagentSelection = next
	d.updateSelection(oldLine, next)
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
}

func (d *detailState) selectRow(index int) bool {
	if d.tab == tabSubagents {
		alreadySelected := d.subagentSelection == index
		oldLine := d.selectedLine
		d.subagentSelection = index
		d.updateSelection(oldLine, index)
		return alreadySelected
	}
	alreadySelected := d.focus == index
	oldLine := d.selectedLine
	d.focus = index
	d.updateSelection(oldLine, d.focusables[index].line)
	return alreadySelected
}

func (d *detailState) selectedExpandable() bool {
	return d.tab == tabTimeline && len(d.focusables) > 0 && d.focusables[d.focus].expandable
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
		d.subagents = nil
		d.subagentSelection = 0
		lines = []detailLine{{text: "detail error: " + terminalText(d.err.Error(), 512), role: detailWarning}}
	} else if d.loading {
		d.subagents = nil
		d.subagentSelection = 0
		lines = []detailLine{{text: "Loading timeline…", role: detailSecondary}}
	} else if d.tab == tabSubagents {
		d.rebuildSubagents()
		return
	} else {
		lines = d.sessionLines(d.session, 0, sessionIdentity(d.session))
	}
	d.lines = lines
	d.focusables = d.focusables[:0]
	for index, line := range lines {
		if line.key != "" {
			d.focusables = append(d.focusables, detailFocus{key: line.key, line: index, expandable: line.expandable, subagent: line.subagent, subagentSession: line.subagentSession, event: line.event})
		}
	}
	if len(d.focusables) == 0 {
		d.focus = 0
	} else if d.focus < 0 || d.focus >= len(d.focusables) {
		d.focus = len(d.focusables) - 1
	}
	selectedLine := -1
	if len(d.focusables) > 0 {
		selectedLine = d.focusables[d.focus].line
	}
	d.selectedLine = selectedLine
	d.rebuildRendered()
}

func (d *detailState) rebuildSubagents() {
	d.subagents = flattenSubagents(d.session)
	if len(d.subagents) == 0 {
		d.lines = []detailLine{{text: "No subagents", role: detailSecondary}}
		d.selectedLine = -1
	} else {
		d.subagentSelection = max(0, min(d.subagentSelection, len(d.subagents)-1))
		d.lines = make([]detailLine, len(d.subagents))
		for index, item := range d.subagents {
			session := item.s
			tokens := humanTokens(session.TotalUsage().TotalTokens())
			cost := formatCost(session.TotalCost())
			d.lines[index] = detailLine{
				text: strings.Repeat("  ", item.depth) + terminalText(string(session.Agent), 32) + " " + firstLine(session.Title) + "  " + terminalText(shortModels(session), 96) + " · " + tokens + " · " + cost,
				key:  sessionIdentity(session), subagent: true, subagentSession: session, subagentTokens: tokens, subagentCost: cost, role: detailRow, agent: session.Agent,
			}
		}
		d.selectedLine = d.subagentSelection
	}
	d.rebuildRendered()
}

func flattenSubagents(session *model.Session) []flattenedSubagent {
	var flattened []flattenedSubagent
	var appendChildren func(*model.Session, int)
	appendChildren = func(parent *model.Session, depth int) {
		for _, child := range parent.Subagents {
			flattened = append(flattened, flattenedSubagent{depth: depth, s: child})
			appendChildren(child, depth+1)
		}
	}
	appendChildren(session, 0)
	return flattened
}

func (d *detailState) updateSelection(oldLine, newLine int) {
	d.updateSelectionMarker(oldLine, false)
	d.selectedLine = newLine
	d.updateSelectionMarker(newLine, true)
	selectedRow := d.firstRenderedRow(newLine)
	if selectedRow < d.viewport.YOffset {
		d.viewport.SetYOffset(selectedRow)
	} else if selectedRow >= d.viewport.YOffset+d.viewport.Height {
		d.viewport.SetYOffset(selectedRow - d.viewport.Height + 1)
	}
}

func (d *detailState) updateSelectionMarker(detailIndex int, selected bool) {
	rowIndex := d.firstRenderedRow(detailIndex)
	if rowIndex < 0 {
		return
	}
	text := d.rendered[rowIndex].text
	if strings.HasPrefix(text, "› ") {
		text = strings.TrimPrefix(text, "› ")
	} else {
		text = strings.TrimPrefix(text, "  ")
	}
	prefix := "  "
	if selected {
		prefix = "› "
	}
	d.rendered[rowIndex].text = prefix + text
}

func (d *detailState) rebuildRendered() {
	d.rendered = d.rendered[:0]
	d.renderedStarts = d.renderedStarts[:0]
	content := make([]string, 0, len(d.lines))
	for detailIndex, line := range d.lines {
		d.renderedStarts = append(d.renderedStarts, len(d.rendered))
		prefix := "  "
		if detailIndex == d.selectedLine {
			prefix = "› "
		}
		plain := prefix + line.text
		rows := []string{plain}
		if d.wrap && ansi.StringWidth(plain) > d.viewport.Width {
			rows = strings.Split(ansi.Hardwrap(plain, d.viewport.Width, true), "\n")
		}
		for rowIndex, row := range rows {
			row = fitPlain(row, d.viewport.Width, false)
			d.rendered = append(d.rendered, renderedRow{detailIndex: detailIndex, text: row, first: rowIndex == 0})
			content = append(content, row)
		}
	}
	d.viewport.SetContent(strings.Join(content, "\n"))
	selectedRow := d.firstRenderedRow(d.selectedLine)
	if selectedRow < 0 {
		return
	}
	if selectedRow < d.viewport.YOffset {
		d.viewport.SetYOffset(selectedRow)
	} else if selectedRow >= d.viewport.YOffset+d.viewport.Height {
		d.viewport.SetYOffset(selectedRow - d.viewport.Height + 1)
	}
}

func (d *detailState) firstRenderedRow(detailIndex int) int {
	if detailIndex < 0 || detailIndex >= len(d.renderedStarts) {
		return -1
	}
	return d.renderedStarts[detailIndex]
}

func (d *detailState) sessionLines(session *model.Session, indent int, path string) []detailLine {
	var lines []detailLine
	for index := 0; index < len(session.Events); {
		event := session.Events[index]
		if event.Kind == model.EventUser {
			key := fmt.Sprintf("%s/user/%d", path, index)
			label := glyphCollapsed + " you:"
			lines = append(lines, detailLine{text: strings.Repeat(" ", indent) + label + " " + firstLine(event.Text), label: label, key: key, role: detailUserPrompt, event: event})
			index++
			continue
		}
		start := index
		for index < len(session.Events) && session.Events[index].Kind != model.EventUser {
			index++
		}
		turn := session.Events[start:index]
		key := fmt.Sprintf("%s/turn/%d", path, start)
		expanded := d.isExpanded(key)
		label := glyphAssistant + " " + terminalText(string(session.Agent), 32) + ":"
		lines = append(lines, detailLine{text: d.turnSummary(session, turn, indent, expanded), label: label, key: key, expandable: turnExpandable(turn), role: detailAssistant, agent: session.Agent, event: turnItemEvent(turn)})
		if expanded {
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

func turnItemEvent(events []model.Event) model.Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == model.EventAssistantText {
			return events[index]
		}
	}
	for _, event := range events {
		if event.Kind != model.EventSubagent {
			return event
		}
	}
	return model.Event{}
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
		label := terminalText(string(session.Agent), 32) + ":"
		return []detailLine{{text: padding + label + " " + firstLine(event.Text), label: label, key: key, role: detailAssistant, agent: session.Agent, event: event}}
	case model.EventThinking:
		return []detailLine{{text: padding + glyphSecondary + " thinking: " + firstLine(event.Text), key: key, role: detailSecondary, event: event}}
	case model.EventToolCall:
		return d.toolEventLines(event, indent, key)
	case model.EventSubagent:
		if event.Subagent == nil {
			return []detailLine{{text: padding + glyphSubagent + " subagent unavailable", key: key, subagent: true, role: detailWarning, event: event}}
		}
		childKey := key + "/subagent/" + sessionIdentity(event.Subagent)
		label := firstLine(event.ToolInput)
		if label == "" {
			label = firstLine(event.Subagent.Title)
		}
		label = ansi.Truncate(label, 28, "…")
		return []detailLine{{text: padding + glyphSubagent + " Task(" + label + ") " + terminalText(shortModels(event.Subagent), 96) + " · " + humanTokens(event.Subagent.TotalUsage().TotalTokens()) + " · " + formatCost(event.Subagent.TotalCost()), key: childKey, subagent: true, subagentSession: event.Subagent, role: detailAccent, event: event}}
	case model.EventCompact:
		return []detailLine{{text: padding + glyphSecondary + " compact: " + firstLine(event.Text), key: key, role: detailSystemPrompt, event: event}}
	case model.EventSystem:
		return []detailLine{{text: padding + glyphSecondary + " " + firstLine(event.Text), key: key, role: detailSystemPrompt, event: event}}
	default:
		return nil
	}
}

func (d *detailState) focusedSubagent() *model.Session {
	if d.tab == tabSubagents {
		if len(d.subagents) == 0 {
			return nil
		}
		return d.subagents[d.subagentSelection].s
	}
	if len(d.focusables) == 0 {
		return nil
	}
	return d.focusables[d.focus].subagentSession
}

func (d *detailState) focusedEvent() (model.Event, bool) {
	if d.tab != tabTimeline || len(d.focusables) == 0 {
		return model.Event{}, false
	}
	focused := d.focusables[d.focus]
	if focused.subagent || focused.event.Kind == "" {
		return model.Event{}, false
	}
	return focused.event, true
}

func (d *detailState) eventForKey(key string) (model.Event, bool) {
	for _, focused := range d.focusables {
		if focused.key == key && !focused.subagent && focused.event.Kind != "" {
			return focused.event, true
		}
	}
	return model.Event{}, false
}

func (d *detailState) toolEventLines(event model.Event, indent int, key string) []detailLine {
	padding := strings.Repeat(" ", indent)
	expandable := detailHasBody(event.Detail)
	text := padding + toolLine(event)
	if expandable {
		marker := glyphCollapsed
		if d.isExpanded(key) {
			marker = glyphExpanded
		}
		text = padding + marker + " " + toolLine(event)
	}
	lines := []detailLine{{text: text, key: key, expandable: expandable, role: detailTool, event: event}}
	if !d.isExpanded(key) || event.Detail == nil {
		return lines
	}
	childPadding := strings.Repeat(" ", indent+2)
	if event.Detail.Diff != "" {
		for _, text := range boundLines(event.Detail.Diff, detailPreviewLineCap) {
			plain := detailPlainText(text)
			role := detailDiffContext
			if strings.HasPrefix(plain, "+") {
				role = detailDiffAdd
			} else if strings.HasPrefix(plain, "-") {
				role = detailDiffRemove
			}
			lines = append(lines, detailLine{text: childPadding + plain, role: role})
		}
	}
	for _, section := range []struct {
		label string
		text  string
	}{{label: "input:", text: detailInputBody(event)}, {label: "output:", text: event.Detail.Output}} {
		if section.text == "" {
			continue
		}
		lines = append(lines, detailLine{text: childPadding + section.label, role: detailSecondary})
		for _, text := range boundLines(section.text, detailPreviewLineCap) {
			lines = append(lines, detailLine{text: childPadding + detailPlainText(text), role: detailSecondary})
		}
	}
	return lines
}

func detailInputBody(event model.Event) string {
	if event.Detail == nil || event.Detail.Input == "" {
		return ""
	}
	if strings.Contains(event.Detail.Input, "\n") {
		return event.Detail.Input
	}
	switch event.ToolName {
	case "Read", "Edit", "MultiEdit", "Write", "apply_patch":
		return ""
	default:
		return event.Detail.Input
	}
}

func detailPlainText(text string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || unicode.In(char, unicode.Cf) {
			return ' '
		}
		return char
	}, ansi.Strip(text))
}

func detailHasBody(detail *model.ToolDetail) bool {
	return detail != nil && (detail.Diff != "" || detail.Output != "" || strings.Contains(detail.Input, "\n"))
}

func turnExpandable(events []model.Event) bool {
	for _, event := range events {
		if event.Kind == model.EventThinking || event.Kind == model.EventToolCall || event.Kind == model.EventSubagent || event.Kind == model.EventCompact {
			return true
		}
	}
	return false
}

func toolLine(event model.Event) string {
	name := toolDisplayName(event.ToolName)
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

func toolDisplayName(name string) string {
	name = terminalText(name, 96)
	if name == "exec_command" || name == "exec" {
		return "Bash"
	}
	if name == "apply_patch" {
		return "Edit"
	}
	return name
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

func boundLines(text string, maxLines int) []string {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return lines
	}
	if maxLines <= 0 {
		return nil
	}
	head := (maxLines - 1) / 2
	tail := maxLines - 1 - head
	bounded := make([]string, 0, maxLines)
	bounded = append(bounded, lines[:head]...)
	bounded = append(bounded, fmt.Sprintf("… %d lines hidden …", len(lines)-head-tail))
	bounded = append(bounded, lines[len(lines)-tail:]...)
	return bounded
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
	visiblePlain := make([]string, 0, d.viewport.Height)
	for rowIndex := d.viewport.YOffset; rowIndex < len(d.rendered) && len(visiblePlain) < d.viewport.Height; rowIndex++ {
		visiblePlain = append(visiblePlain, d.rendered[rowIndex].text)
	}
	visible := make([]panelLine, len(visiblePlain))
	for index, plain := range visiblePlain {
		rowIndex := d.viewport.YOffset + index
		if rowIndex >= len(d.rendered) {
			visible[index] = panelLine{plain: plain, styled: d.styles.row.Render(plain)}
			continue
		}
		detailIndex := d.rendered[rowIndex].detailIndex
		visible[index] = panelLine{plain: plain, styled: d.styleLine(plain, d.lines[detailIndex], detailIndex == d.selectedLine, d.rendered[rowIndex].first)}
	}
	for len(visible) < layout.contentHeight {
		plain := strings.Repeat(" ", d.viewport.Width)
		visible = append(visible, panelLine{plain: plain, styled: d.styles.row.Render(plain)})
	}
	if len(visible) > layout.contentHeight {
		visible = visible[:layout.contentHeight]
	}
	hint := ""
	if len(d.rendered) > d.viewport.Height {
		hint = fmt.Sprintf("%d/%d", min(len(d.rendered), d.viewport.YOffset+1), len(d.rendered))
	}
	timeline := renderPanelWithLabel(d.tabPanelLabel(), hint, visible, d.width, timelineHeight, d.styles)
	keyText := detailKeyText(d.width, d.styles.mono, d.tab)
	keyBar := d.styles.keyHint.Render(fitPlain(keyText, d.width, false))
	return strings.Join([]string{header, timeline, keyBar}, "\n")
}

func (d *detailState) compactView(layout detailLayout) string {
	capacity := max(0, layout.compactPanelHeight-2)
	headerLines := d.headerPanelLines()
	content := make([]panelLine, 0, capacity)
	for _, index := range []int{0, 2, 1} {
		if len(content) >= layout.compactHeaderHeight {
			break
		}
		content = append(content, headerLines[index])
	}
	for rowIndex := d.viewport.YOffset; len(content) < capacity && rowIndex < len(d.rendered); rowIndex++ {
		row := d.rendered[rowIndex]
		plain := fitPlain(row.text, max(0, d.width-2), false)
		content = append(content, panelLine{plain: plain, styled: d.styleLine(plain, d.lines[row.detailIndex], row.detailIndex == d.selectedLine, row.first)})
	}
	for len(content) < capacity {
		plain := strings.Repeat(" ", max(0, d.width-2))
		content = append(content, panelLine{plain: plain, styled: d.styles.row.Render(plain)})
	}
	panel := renderPanelWithLabel(d.compactPanelLabel(), "", content, d.width, layout.compactPanelHeight, d.styles)
	if layout.keyBarHeight == 0 {
		return panel
	}
	keyBar := d.styles.keyHint.Render(fitPlain(detailKeyText(d.width, d.styles.mono, d.tab), d.width, false))
	return panel + "\n" + keyBar
}

func (t detailTab) title() string {
	if t == tabSubagents {
		return "Subagents"
	}
	return "Timeline"
}

func (d *detailState) tabPanelLabel() panelLabel {
	label := d.tabLabel()
	plain := label.plain
	if ansi.StringWidth(plain) > max(1, d.width-5) {
		return panelLabel{plain: d.tab.title()}
	}
	return label
}

func (d *detailState) compactPanelLabel() panelLabel {
	label := d.tabLabel()
	plain, styled := label.plain, label.styled
	if len(d.crumbs) > 0 {
		crumbs := terminalText(strings.Join(d.crumbs, " › "), 256)
		plain += " · " + crumbs
		styled += d.styles.title.Render(" · " + crumbs)
	}
	if ansi.StringWidth(plain) <= max(1, d.width-5) {
		return panelLabel{plain: plain, styled: styled}
	}
	return panelLabel{plain: d.panelTitle(d.tab.title())}
}

func (d *detailState) tabLabel() panelLabel {
	timelineStyle := d.styles.title
	subagentsStyle := d.styles.muted
	if d.tab == tabSubagents {
		timelineStyle = d.styles.muted
		subagentsStyle = d.styles.title
	}
	return panelLabel{
		plain:  "Timeline · Subagents",
		styled: timelineStyle.Render("Timeline") + d.styles.title.Render(" · ") + subagentsStyle.Render("Subagents"),
	}
}

func detailKeyText(width int, mono bool, tab detailTab) string {
	hints := []string{"j/k scroll", "space toggle", "↵ open", "tab tabs"}
	if tab == tabTimeline {
		hints = append(hints, "J/K subagent")
	}
	hints = append(hints, "w wrap", "esc back", "mouse scroll/click")
	if !mono {
		hints = append(hints, "t theme")
	}
	hints = append(hints, "? help", "q quit")
	return fitKeyHints(width, hints, []string{
		"mouse scroll/click", "t theme", "? help", "J/K subagent", "j/k scroll", "q quit", "tab tabs", "w wrap", "space toggle", "↵ open",
	})
}

func fitKeyHints(width int, hints, dropOrder []string) string {
	for _, drop := range dropOrder {
		if ansi.StringWidth(strings.Join(hints, "   ")) <= width {
			break
		}
		for index, hint := range hints {
			if hint == drop {
				hints = append(hints[:index], hints[index+1:]...)
				break
			}
		}
	}
	text := strings.Join(hints, "   ")
	if ansi.StringWidth(text) <= width {
		return text
	}
	if len(hints) == 1 && hints[0] == "esc back" && width >= ansi.StringWidth("esc") {
		return "esc"
	}
	return ""
}

func (d *detailState) header() string {
	return renderPanel(d.panelTitle("Session"), "", d.headerPanelLines(), d.width, 5, d.styles)
}

func (d *detailState) panelTitle(name string) string {
	title := name
	if len(d.crumbs) > 0 {
		title += " · " + strings.Join(d.crumbs, " › ")
	}
	return ansi.Truncate(terminalText(title, 256), max(1, d.width-5), "…")
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
	innerWidth := max(0, d.width-2)
	line1 := firstLine(session.Title)

	line2Parts := make([]string, 0, 3)
	if agent := terminalText(string(session.Agent), 32); agent != "" {
		line2Parts = append(line2Parts, agent)
	}
	project, cwd := terminalText(session.Project, 96), terminalText(session.CWD, 256)
	workspaceIndex := -1
	if project != "" && cwd != "" {
		workspaceIndex = len(line2Parts)
		line2Parts = append(line2Parts, project+" ("+cwd+")")
	} else if project != "" {
		line2Parts = append(line2Parts, project)
	} else if cwd != "" {
		line2Parts = append(line2Parts, cwd)
	}
	if models := terminalText(detailModels(session), 256); models != "" {
		line2Parts = append(line2Parts, models)
	}
	if workspaceIndex >= 0 && ansi.StringWidth(strings.Join(line2Parts, " · ")) > innerWidth {
		line2Parts[workspaceIndex] = project
	}
	line2 := strings.Join(line2Parts, " · ")

	line3Parts := make([]string, 0, 3)
	if branch := terminalText(session.GitBranch, 96); branch != "" {
		line3Parts = append(line3Parts, branch)
	}
	started, updated := formatDetailTime(session.StartedAt), formatDetailTime(session.UpdatedAt)
	if started != "" && updated != "" {
		line3Parts = append(line3Parts, started+"→"+updated)
	} else if started != "" {
		line3Parts = append(line3Parts, started)
	} else if updated != "" {
		line3Parts = append(line3Parts, updated)
	}
	detailedUsage := fmt.Sprintf("%s tokens / %s · own %s / %s · subagents %s / %s",
		humanTokens(totalUsage.TotalTokens()), formatCost(totalCost), humanTokens(ownUsage.TotalTokens()), formatCost(session.Cost),
		humanTokens(totalUsage.TotalTokens()-ownUsage.TotalTokens()), formatCost(subagentCost))
	compactUsage := fmt.Sprintf("%s tokens / %s", humanTokens(totalUsage.TotalTokens()), formatCost(totalCost))
	line3Parts = append(line3Parts, detailedUsage)
	if ansi.StringWidth(strings.Join(line3Parts, " · ")) > innerWidth {
		line3Parts[len(line3Parts)-1] = compactUsage
	}
	for len(line3Parts) > 1 && ansi.StringWidth(strings.Join(line3Parts, " · ")) > innerWidth {
		line3Parts = line3Parts[1:]
	}
	line3 := strings.Join(line3Parts, " · ")
	plainLines := []string{
		fitPlain(line1, innerWidth, false),
		fitPlain(line2, innerWidth, false),
		fitPlain(line3, innerWidth, false),
	}
	lines := []panelLine{
		{plain: plainLines[0], styled: d.styles.emphasis.Render(plainLines[0])},
		{plain: plainLines[1], styled: d.agentStyle(session.Agent).Render(plainLines[1])},
		{plain: plainLines[2], styled: d.styles.emphasis.Render(plainLines[2])},
	}
	return lines
}

func (d *detailState) styleLine(line string, detail detailLine, selected, first bool) string {
	if selected {
		return d.styles.selected.Render(line)
	}
	if detail.role == detailRow && detail.subagentSession != nil {
		return d.styleSubagentLine(line, detail)
	}
	if detail.role == detailAssistant {
		if !first {
			return d.styles.row.Render(line)
		}
		return styleLabelLine(line, detail.label, d.styles.row, d.agentStyle(detail.agent))
	}
	if detail.role == detailUserPrompt {
		if !first {
			return d.styles.userPrompt.Render(line)
		}
		labelStyle := d.styles.userPrompt.Foreground(d.styles.accent.GetForeground()).Bold(d.styles.accent.GetBold())
		return styleLabelLine(line, detail.label, d.styles.userPrompt, labelStyle)
	}
	return styleDetailRole(d.styles, detail.role, line)
}

func styleDetailRole(styleSet styles, role detailRole, line string) string {
	switch role {
	case detailAccent, detailTool:
		return styleSet.accent.Render(line)
	case detailUserPrompt:
		return styleSet.userPrompt.Render(line)
	case detailSystemPrompt:
		return styleSet.systemPrompt.Render(line)
	case detailSecondary:
		return styleSet.muted.Render(line)
	case detailWarning:
		return styleSet.warning.Render(line)
	case detailDiffAdd:
		return styleSet.diffAdd.Render(line)
	case detailDiffRemove:
		return styleSet.diffRemove.Render(line)
	case detailDiffContext:
		return styleSet.muted.Render(line)
	}
	return styleSet.row.Render(line)
}

func styleLabelLine(line, label string, base, labelStyle lipgloss.Style) string {
	start := strings.Index(line, label)
	if label == "" || start < 0 {
		return base.Render(line)
	}
	end := start + len(label)
	return base.Render(line[:start]) + labelStyle.Render(line[start:end]) + base.Render(line[end:])
}

func (d *detailState) styleSubagentLine(line string, detail detailLine) string {
	session := detail.subagentSession
	tokens, cost := detail.subagentTokens, detail.subagentCost
	type cell struct {
		start int
		end   int
		style lipgloss.Style
	}
	var cells []cell
	agent := terminalText(string(session.Agent), 32)
	if start := strings.Index(line, agent); start >= 0 {
		cells = append(cells, cell{start: start, end: start + len(agent), style: d.agentStyle(session.Agent)})
	}
	if marker := strings.LastIndex(line, " · "+tokens+" · "); marker >= 0 {
		start := marker + len(" · ")
		cells = append(cells, cell{start: start, end: start + len(tokens), style: d.styles.accent})
	}
	if marker := strings.LastIndex(line, " · "+cost); marker >= 0 {
		start := marker + len(" · ")
		style := d.styles.row
		if strings.HasPrefix(cost, "~$") {
			style = d.styles.estimated
		}
		cells = append(cells, cell{start: start, end: start + len(cost), style: style})
	}
	if len(cells) == 0 {
		return d.styles.row.Render(line)
	}
	var styled strings.Builder
	position := 0
	for _, item := range cells {
		if item.start < position || item.end > len(line) {
			continue
		}
		styled.WriteString(d.styles.row.Render(line[position:item.start]))
		styled.WriteString(item.style.Render(line[item.start:item.end]))
		position = item.end
	}
	styled.WriteString(d.styles.row.Render(line[position:]))
	return styled.String()
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
		return ""
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
		return ""
	}
	return value.Format("Jan 02 15:04")
}
