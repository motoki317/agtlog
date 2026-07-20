package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

type itemView struct {
	event    model.Event
	agent    model.AgentKind
	crumbs   []string
	focusKey string
	viewport viewport.Model
	width    int
	height   int
	styles   styles
	wrap     bool
	now      time.Time
	showRaw  bool
	lines    []detailLine
	rendered []renderedRow
}

var (
	_ detailScreen = (*detailState)(nil)
	_ detailScreen = (*itemView)(nil)
)

func newItemView(event model.Event, agent model.AgentKind, crumbs []string, width, height int, styles styles) *itemView {
	return newItemViewWithState(event, agent, crumbs, width, height, styles, event.Timestamp, false, true)
}

func newItemViewWithState(event model.Event, agent model.AgentKind, crumbs []string, width, height int, styles styles, now time.Time, showRaw, wrap bool) *itemView {
	item := &itemView{
		event: event, agent: agent, crumbs: append([]string(nil), crumbs...), styles: styles,
		viewport: newViewport(max(1, width-2), max(1, height-3)), wrap: wrap, now: now, showRaw: showRaw,
	}
	item.rebuildLines()
	item.resize(width, height)
	return item
}

func (i *itemView) clone() *itemView {
	copy := *i
	copy.crumbs = append([]string(nil), i.crumbs...)
	copy.lines = append([]detailLine(nil), i.lines...)
	copy.rendered = append([]renderedRow(nil), i.rendered...)
	return &copy
}

func (i *itemView) resize(width, height int) {
	i.width, i.height = max(1, width), max(3, height)
	i.viewport.Width = max(1, i.width-2)
	i.viewport.Height = max(1, i.panelHeight()-2)
	i.rebuild()
}

func (i *itemView) setWrap(wrap bool) {
	if i.wrap == wrap {
		return
	}
	i.wrap = wrap
	i.rebuild()
}

func (i *itemView) setNow(now time.Time) {
	if i.now.Equal(now) {
		return
	}
	i.now = now
	i.rebuildLines()
	i.rebuild()
}

func (i *itemView) scrollWheel(button tea.MouseButton) {
	scrollViewport(&i.viewport, button)
}

func (i *itemView) update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		i.viewport, cmd = i.viewport.Update(msg)
		return cmd
	}
	switch key.String() {
	case "j", "down":
		i.viewport.ScrollDown(1)
	case "k", "up":
		i.viewport.ScrollUp(1)
	case "g":
		i.viewport.GotoTop()
	case "G":
		i.viewport.GotoBottom()
	case "w":
		i.setWrap(!i.wrap)
	case rawRecordKey:
		if i.event.Raw != "" {
			i.showRaw = !i.showRaw
			i.rebuildLines()
			i.rebuild()
		}
	}
	return nil
}

func (i *itemView) rebuild() {
	i.rendered = i.rendered[:0]
	content := make([]string, 0, len(i.lines))
	for detailIndex, line := range i.lines {
		plain := line.text
		rows := []string{plain}
		if i.wrap && ansi.StringWidth(plain) > i.viewport.Width {
			rows = strings.Split(ansi.Hardwrap(plain, i.viewport.Width, true), "\n")
		}
		for rowIndex, row := range rows {
			row = fitPlain(row, i.viewport.Width, false)
			i.rendered = append(i.rendered, renderedRow{detailIndex: detailIndex, text: row, first: rowIndex == 0})
			content = append(content, row)
		}
	}
	i.viewport.SetContent(strings.Join(content, "\n"))
	i.viewport.SetYOffset(i.viewport.YOffset)
}

func (i *itemView) view() string {
	panelHeight := i.panelHeight()
	capacity := max(0, panelHeight-2)
	visible := make([]panelLine, 0, capacity)
	for rowIndex := i.viewport.YOffset; len(visible) < capacity && rowIndex < len(i.rendered); rowIndex++ {
		row := i.rendered[rowIndex]
		line := i.lines[row.detailIndex]
		visible = append(visible, panelLine{plain: row.text, styled: i.styleLine(row.text, line)})
	}
	for len(visible) < capacity {
		plain := strings.Repeat(" ", i.viewport.Width)
		visible = append(visible, panelLine{plain: plain, styled: i.styles.row.Render(plain)})
	}
	hint := ""
	if len(i.rendered) > i.viewport.Height {
		hint = fmt.Sprintf("%d/%d", min(len(i.rendered), i.viewport.YOffset+1), len(i.rendered))
	}
	panel := renderPanel(i.title(), hint, visible, i.width, panelHeight, i.styles)
	if panelHeight == i.height {
		return panel
	}
	keyBar := i.styles.keyHint.Render(fitPlain(itemKeyText(i.width, i.showRaw), i.width, false))
	return panel + "\n" + keyBar
}

func (i *itemView) panelHeight() int {
	if i.height <= 3 {
		return i.height
	}
	return i.height - 1
}

func (i *itemView) title() string {
	label := terminalText(itemLabel(i.event, i.agent), 96)
	parts := make([]string, 0, len(i.crumbs)+1)
	for _, crumb := range i.crumbs {
		if crumb := terminalText(crumb, 96); crumb != "" {
			parts = append(parts, crumb)
		}
	}
	parts = append(parts, label)
	title := strings.Join(parts, " › ")
	width := max(1, i.width-5)
	if ansi.StringWidth(title) <= width {
		return title
	}
	if ansi.StringWidth(label) >= width {
		return ansi.Truncate(label, width, "…")
	}
	removed := ansi.StringWidth(title) - width + ansi.StringWidth("…")
	return ansi.TruncateLeft(title, removed, "…")
}

func (i *itemView) styleLine(plain string, line detailLine) string {
	return styleDetailRole(i.styles, line.role, plain)
}

func (i *itemView) rebuildLines() {
	metadata := itemMetadataLines(i.event, i.now, i.agent)
	content := itemEventLines(i.event, i.agent)
	i.lines = append(i.lines[:0], metadata...)
	if len(content) > 0 {
		i.lines = append(i.lines, detailLine{text: "", role: detailSecondary, agent: i.agent})
		i.lines = append(i.lines, content...)
	}
	if i.showRaw && i.event.Raw != "" {
		i.lines = append(i.lines, detailLine{text: "", role: detailSecondary, agent: i.agent})
		i.lines = appendItemSection(i.lines, "raw:", prettyRawRecord(i.event.Raw), detailRow, i.agent)
	}
}

func itemMetadataLines(event model.Event, now time.Time, agent model.AgentKind) []detailLine {
	fields := []struct {
		label string
		value string
	}{
		{label: "kind", value: string(event.Kind)},
	}
	if !event.Timestamp.IsZero() {
		fields = append(fields,
			struct{ label, value string }{label: "relative time", value: formatAge(now, event.Timestamp)},
			struct{ label, value string }{label: "absolute time", value: formatDetailTime(event.Timestamp, true)},
		)
	}
	if event.Kind == model.EventToolCall && event.Duration > 0 {
		fields = append(fields, struct{ label, value string }{label: "duration", value: formatDuration(event.Duration)})
	}
	fields = append(fields,
		struct{ label, value string }{label: "model", value: event.Model},
		struct{ label, value string }{label: "tool", value: event.ToolName},
		struct{ label, value string }{label: "call-id", value: event.CallID},
		struct{ label, value string }{label: "agent-id", value: event.AgentID},
	)
	lines := make([]detailLine, 0, len(fields))
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		lines = append(lines, detailLine{text: detailPlainText(field.label + ": " + field.value), role: detailSecondary, agent: agent})
	}
	return lines
}

func prettyRawRecord(raw string) string {
	raw = model.BoundedRawRecord(raw)
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(raw), "", "  ") == nil {
		return pretty.String()
	}
	return raw
}

func itemEventLines(event model.Event, agent model.AgentKind) []detailLine {
	if event.Kind != model.EventToolCall {
		role := detailSecondary
		if event.Kind == model.EventUser {
			role = detailUserPrompt
		} else if event.Kind == model.EventAssistantText {
			role = detailRow
		} else if event.Kind == model.EventSystem || event.Kind == model.EventCompact {
			role = detailSystemPrompt
		}
		return itemTextLines(event.Text, role, agent)
	}

	input, diff, output := event.ToolInput, "", event.ResultSummary
	if event.Detail != nil {
		if event.Detail.Input != "" {
			input = event.Detail.Input
		}
		diff = event.Detail.Diff
		if event.Detail.Output != "" {
			output = event.Detail.Output
		}
	}
	var lines []detailLine
	lines = appendItemSection(lines, "input:", input, detailRow, agent)
	if diff != "" {
		for _, text := range strings.Split(diff, "\n") {
			plain := detailPlainText(text)
			role := detailDiffContext
			if strings.HasPrefix(plain, "+") {
				role = detailDiffAdd
			} else if strings.HasPrefix(plain, "-") {
				role = detailDiffRemove
			}
			lines = append(lines, detailLine{text: plain, role: role, agent: agent})
		}
	}
	lines = appendItemSection(lines, "output:", output, detailRow, agent)
	if event.ResultSummary != "" && event.Detail != nil && event.Detail.Output != "" {
		lines = appendItemSection(lines, "result-summary:", event.ResultSummary, detailRow, agent)
	}
	return lines
}

func appendItemSection(lines []detailLine, label, text string, role detailRole, agent model.AgentKind) []detailLine {
	if text == "" {
		return lines
	}
	lines = append(lines, detailLine{text: label, role: detailSecondary, agent: agent})
	return append(lines, itemTextLines(text, role, agent)...)
}

func itemTextLines(text string, role detailRole, agent model.AgentKind) []detailLine {
	lines := strings.Split(text, "\n")
	result := make([]detailLine, len(lines))
	for index, line := range lines {
		result[index] = detailLine{text: detailPlainText(line), role: role, agent: agent}
	}
	return result
}

func itemLabel(event model.Event, agent model.AgentKind) string {
	switch event.Kind {
	case model.EventToolCall:
		return toolDisplayName(event.ToolName)
	case model.EventUser:
		return "User"
	case model.EventAssistantText:
		if agent != "" {
			return terminalText(string(agent), 32) + " message"
		}
		return "Assistant"
	case model.EventThinking:
		return "Thinking"
	case model.EventCompact:
		return "Compact"
	case model.EventSystem:
		return "System"
	default:
		return "Item"
	}
}

func itemKeyText(width int, showRaw bool) string {
	timeHint := timeFormatKey + " time"
	rawHint := rawRecordKey + " raw"
	if showRaw {
		rawHint = rawRecordKey + " hide raw"
		if width < ansi.StringWidth(rawHint+"   esc back") {
			rawHint = rawRecordKey + " raw*"
		}
	}
	return fitKeyHints(width, []string{"j/k scroll", "w wrap", rawHint, timeHint, "esc back", "wheel scroll"}, []string{"wheel scroll", "j/k scroll", "w wrap", timeHint, rawHint})
}
