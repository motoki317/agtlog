package tui

import (
	"fmt"
	"strings"

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
	lines    []detailLine
	rendered []renderedRow
}

var (
	_ detailScreen = (*detailState)(nil)
	_ detailScreen = (*itemView)(nil)
)

func newItemView(event model.Event, agent model.AgentKind, crumbs []string, width, height int, styles styles) *itemView {
	item := &itemView{
		event: event, agent: agent, crumbs: append([]string(nil), crumbs...), styles: styles,
		viewport: viewport.New(max(1, width-2), max(1, height-3)),
	}
	item.lines = itemEventLines(event, agent)
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
		for _, row := range rows {
			row = fitPlain(row, i.viewport.Width, false)
			i.rendered = append(i.rendered, renderedRow{detailIndex: detailIndex, text: row})
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
	keyBar := i.styles.keyHint.Render(fitPlain(itemKeyText(i.width), i.width, false))
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
	return styleDetailRole(i.styles, i.agent, line.role, plain)
}

func itemEventLines(event model.Event, agent model.AgentKind) []detailLine {
	if event.Kind != model.EventToolCall {
		role := detailSecondary
		if event.Kind == model.EventUser {
			role = detailAccent
		} else if event.Kind == model.EventAssistantText {
			role = detailAssistant
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
	lines = appendItemSection(lines, "input:", input, detailSecondary, agent)
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
	return appendItemSection(lines, "output:", output, detailSecondary, agent)
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

func itemKeyText(width int) string {
	return fitKeyHints(width, []string{"j/k scroll", "w wrap", "esc back"}, []string{"j/k scroll", "w wrap"})
}
