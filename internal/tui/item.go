package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

type itemView struct {
	event          model.Event
	agent          model.AgentKind
	crumbs         []string
	focusKey       string
	viewport       viewport.Model
	width          int
	height         int
	styles         styles
	wrap           bool
	now            time.Time
	ctx            context.Context
	generation     uint64
	raw            []byte
	rawLines       []detailLine
	rawLoading     bool
	rawUnavailable string
	restoreYOffset *int
	lines          []detailLine
	rendered       []renderedRow
}

var (
	_ detailScreen = (*detailState)(nil)
	_ detailScreen = (*itemView)(nil)
)

func newItemView(event model.Event, agent model.AgentKind, crumbs []string, width, height int, styles styles) *itemView {
	return newItemViewWithState(event, agent, crumbs, width, height, styles, event.Timestamp, true)
}

func newItemViewWithState(event model.Event, agent model.AgentKind, crumbs []string, width, height int, styles styles, now time.Time, wrap bool) *itemView {
	item := &itemView{
		event: event, agent: agent, crumbs: append([]string(nil), crumbs...), styles: styles,
		viewport: newViewport(max(1, width-2), max(1, height-3)), wrap: wrap, now: now,
		ctx: context.Background(),
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
	i.restoreYOffset = nil
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
	i.restoreYOffset = nil
	scrollViewport(&i.viewport, button)
}

func (i *itemView) intendedYOffset() int {
	if i.restoreYOffset != nil {
		return *i.restoreYOffset
	}
	return i.viewport.YOffset
}

func (i *itemView) update(msg tea.Msg) tea.Cmd {
	if loaded, ok := msg.(rawRecordLoadedMsg); ok {
		if loaded.generation != i.generation {
			return nil
		}
		i.rawLoading = false
		i.raw = loaded.record
		i.rawLines = rawDetailLines(loaded.safeLines, i.agent)
		i.rawUnavailable = rawUnavailableMessage(loaded.err)
		i.rebuildLines()
		i.rebuild()
		if i.restoreYOffset != nil {
			i.viewport.SetYOffset(*i.restoreYOffset)
			i.restoreYOffset = nil
		}
		return nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		i.viewport, cmd = i.viewport.Update(msg)
		return cmd
	}
	switch key.String() {
	case "j", "down":
		i.restoreYOffset = nil
		i.viewport.ScrollDown(1)
	case "k", "up":
		i.restoreYOffset = nil
		i.viewport.ScrollUp(1)
	case "g":
		i.restoreYOffset = nil
		i.viewport.GotoTop()
	case "G":
		i.restoreYOffset = nil
		i.viewport.GotoBottom()
	case "w":
		i.setWrap(!i.wrap)
	}
	return nil
}

func (i *itemView) requestRaw() tea.Cmd {
	if !validRecordRef(i.event.RecordRef) || i.raw != nil || i.rawUnavailable != "" || i.rawLoading {
		return nil
	}
	i.rawLoading = true
	i.rebuildLines()
	i.rebuild()
	return loadRawRecord(i.ctx, i.event.RecordRef, i.generation)
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
	return styleDetailRole(i.styles, line.role, plain)
}

func (i *itemView) rebuildLines() {
	metadata := itemMetadataLines(i.event, i.now, i.agent)
	content := itemEventLines(i.event, i.agent)
	i.lines = i.lines[:0]
	i.lines = appendItemLinesSection(i.lines, "Event", metadata, i.agent)
	i.lines = appendItemLinesSection(i.lines, "Request", itemRequestLines(i.event, i.agent), i.agent)
	if i.event.Kind == model.EventToolCall {
		if len(content) > 0 && len(i.lines) > 0 {
			i.lines = append(i.lines, detailLine{text: "", role: detailRow, agent: i.agent})
		}
		i.lines = append(i.lines, content...)
	} else {
		i.lines = appendItemLinesSection(i.lines, itemContentTitle(i.event), content, i.agent)
	}
	if validRecordRef(i.event.RecordRef) {
		var rawLines []detailLine
		if i.rawLoading {
			rawLines = itemTextLines("loading raw…", detailRow, i.agent)
		} else if i.rawUnavailable != "" {
			rawLines = itemTextLines(i.rawUnavailable, detailRow, i.agent)
		} else {
			rawLines = i.rawLines
		}
		if len(rawLines) == 0 {
			rawLines = []detailLine{{role: detailRow, agent: i.agent}}
		}
		i.lines = appendItemLinesSection(i.lines, "Raw", rawLines, i.agent)
	}
}

func itemMetadataLines(event model.Event, now time.Time, agent model.AgentKind) []detailLine {
	fields := []struct {
		label string
		value string
	}{
		{label: "kind", value: string(event.Kind)},
		{label: "model", value: event.Model},
		{label: "tool", value: event.ToolName},
		{label: "call-id", value: event.CallID},
		{label: "agent-id", value: event.AgentID},
	}
	if event.Duration > 0 {
		fields = append(fields, struct{ label, value string }{label: "duration", value: formatDuration(event.Duration)})
	}
	if !event.Timestamp.IsZero() {
		fields = append(fields,
			struct{ label, value string }{label: "absolute time", value: formatDetailTime(event.Timestamp, true)},
			struct{ label, value string }{label: "relative time", value: formatAge(now, event.Timestamp)},
		)
	}
	if event.Kind == model.EventCompact {
		fields = append(fields, struct{ label, value string }{label: "trigger", value: event.CompactTrigger})
		if event.CompactPostTokens > 0 {
			fields = append(fields, struct{ label, value string }{label: "post tokens", value: humanTokens(event.CompactPostTokens)})
		}
	}
	if event.UsageAggregate {
		fields = append(fields, struct{ label, value string }{label: "scope", value: "session-level fallback usage, not one request"})
	}
	labelWidth := 0
	for _, field := range fields {
		if field.value != "" {
			labelWidth = max(labelWidth, ansi.StringWidth(field.label))
		}
	}
	lines := make([]detailLine, 0, len(fields))
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		lines = append(lines, detailLine{
			text: detailPlainText(fmt.Sprintf("%-*s  %s", labelWidth, field.label, field.value)),
			role: detailRow, agent: agent,
		})
	}
	return lines
}

func itemRequestLines(event model.Event, agent model.AgentKind) []detailLine {
	if event.Usage == nil {
		return nil
	}
	tokenLine := "tokens  " + formatTokenFlow(*event.Usage)
	if !event.UsageAggregate && event.Usage.TotalTokens() > 0 {
		tokenLine += " · ctx " + humanTokens(event.Usage.TotalTokens())
	}
	lines := []detailLine{{text: detailPlainText(tokenLine), role: detailRow, agent: agent}}
	if event.PricingModel != "" {
		lines = append(lines, detailLine{
			text: detailPlainText(fmt.Sprintf("rate  priced as %s — no published rate for %s",
				displayModelName(event.PricingModel), displayModelName(event.Model))),
			role: detailRow, agent: agent,
		})
	}
	if !event.Priced || !validCostBreakdown(event.Cost) {
		return append(lines, detailLine{
			text: detailPlainText("price unavailable for " + displayModelName(event.Model)),
			role: detailRow, agent: agent,
		})
	}
	groups, termsWidth := formatCostRateGroups(costRateGroups(event.Cost))
	for _, group := range groups {
		if group.terms == "" {
			continue
		}
		lines = append(lines, detailLine{
			text: detailPlainText(fmt.Sprintf("  %-12s %-*s = %s",
				group.label, termsWidth, group.terms, formatPreciseCost(group.buckets.Cost(), event.CostEstimated))),
			role: detailRow, agent: agent,
		})
	}
	lines = append(lines, detailLine{
		text: detailPlainText(fmt.Sprintf("  %-12s %-*s = %s",
			"total", termsWidth, "", formatPreciseCost(event.Cost.Total(), event.CostEstimated))),
		role: detailRow, agent: agent,
	})
	return lines
}

func formatPreciseCost(usd float64, estimated bool) string {
	prefix := "$"
	if estimated {
		prefix = "~$"
	}
	if math.IsInf(usd, 1) {
		return prefix + "∞"
	}
	usd = normalizedUSD(usd)
	text := strconv.FormatFloat(usd, 'f', 9, 64)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	if usd > 0 && text == "0" {
		text = strconv.FormatFloat(usd, 'f', -1, 64)
	}
	if dot := strings.IndexByte(text, '.'); dot < 0 {
		text += ".00"
	} else if decimals := len(text) - dot - 1; decimals < 2 {
		text += strings.Repeat("0", 2-decimals)
	}
	return prefix + text
}

type rawRecordLoadedMsg struct {
	generation uint64
	record     []byte
	safeLines  []string
	err        error
}

func loadRawRecord(ctx context.Context, ref model.RecordRef, generation uint64) tea.Cmd {
	return func() tea.Msg {
		record, err := source.ReadRecord(ctx, ref)
		var safeLines []string
		if err == nil {
			safeLines = terminalSafePrettyRawRecordLines(record)
		}
		return rawRecordLoadedMsg{generation: generation, record: record, safeLines: safeLines, err: err}
	}
}

func validRecordRef(ref model.RecordRef) bool {
	return ref.Path != "" && ref.Offset >= 0 && ref.Length > 0
}

func rawUnavailableMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, source.ErrRecordChanged) {
		return "raw unavailable: source changed"
	}
	return "raw unavailable: read failed"
}

func terminalSafeRawRecord(raw []byte) string {
	var safe strings.Builder
	safe.Grow(len(raw))
	for len(raw) > 0 {
		if raw[0] < utf8.RuneSelf {
			if raw[0] == '\\' {
				run := 1
				for run < len(raw) && raw[run] == '\\' {
					run++
				}
				if rawRecordEscapeSuffix(raw[run:]) || rawRecordNeedsEscape(raw[run:]) {
					for range run {
						safe.WriteString(`\\`)
					}
				} else {
					safe.Write(raw[:run])
				}
				raw = raw[run:]
				continue
			}
			char := raw[0]
			raw = raw[1:]
			switch char {
			case '\n':
				safe.WriteString(`\n`)
			case '\r':
				safe.WriteString(`\r`)
			case '\t':
				safe.WriteString(`\t`)
			default:
				if char < ' ' || char == 0x7f {
					fmt.Fprintf(&safe, `\x%02x`, char)
				} else {
					safe.WriteByte(char)
				}
			}
			continue
		}
		char, size := utf8.DecodeRune(raw)
		if char == utf8.RuneError && size == 1 {
			fmt.Fprintf(&safe, `\x%02x`, raw[0])
			raw = raw[1:]
			continue
		}
		raw = raw[size:]
		if unicode.IsControl(char) || unicode.In(char, unicode.Cf) {
			if char <= '\uffff' {
				fmt.Fprintf(&safe, `\u%04x`, char)
			} else {
				fmt.Fprintf(&safe, `\U%08x`, char)
			}
			continue
		}
		safe.WriteRune(char)
	}
	return safe.String()
}

func terminalSafePrettyRawRecordLines(raw []byte) []string {
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		return []string{terminalSafeRawRecord(raw)}
	}
	lines := bytes.Split(indented.Bytes(), []byte{'\n'})
	safe := make([]string, len(lines))
	for index, line := range lines {
		safe[index] = terminalSafeRawRecord(line)
	}
	return safe
}

func rawDetailLines(lines []string, agent model.AgentKind) []detailLine {
	result := make([]detailLine, len(lines))
	for index, line := range lines {
		result[index] = detailLine{text: line, role: detailRow, agent: agent}
	}
	return result
}

func rawRecordEscapeSuffix(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case 'n', 'r', 't':
		return true
	case 'x':
		return hasLowerHexPrefix(raw[1:], 2)
	case 'u':
		return hasLowerHexPrefix(raw[1:], 4)
	case 'U':
		return hasLowerHexPrefix(raw[1:], 8)
	default:
		return false
	}
}

func rawRecordNeedsEscape(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] < utf8.RuneSelf {
		return raw[0] < ' ' || raw[0] == 0x7f
	}
	char, size := utf8.DecodeRune(raw)
	return char == utf8.RuneError && size == 1 || unicode.IsControl(char) || unicode.In(char, unicode.Cf)
}

func hasLowerHexPrefix(text []byte, size int) bool {
	if len(text) < size {
		return false
	}
	for _, char := range text[:size] {
		if !('0' <= char && char <= '9' || 'a' <= char && char <= 'f') {
			return false
		}
	}
	return true
}

func itemEventLines(event model.Event, agent model.AgentKind) []detailLine {
	if event.Kind != model.EventToolCall {
		role := detailSecondary
		if event.Kind == model.EventUser {
			if event.Harness {
				role = detailSystemPrompt
			} else {
				role = detailUserPrompt
			}
		} else if event.Kind == model.EventAssistantText {
			role = detailRow
		} else if event.Kind == model.EventSystem || event.Kind == model.EventCompact || event.Kind == model.EventUsage {
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
	lines = appendItemSection(lines, "Input", input, detailRow, agent)
	if diff != "" {
		if len(lines) > 0 {
			lines = append(lines, detailLine{text: "", role: detailRow, agent: agent})
		}
		lines = append(lines, detailLine{text: "Diff", role: detailHeader, agent: agent})
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
	lines = appendItemSection(lines, "Output", output, detailRow, agent)
	if event.ResultSummary != "" && event.Detail != nil && event.Detail.Output != "" {
		lines = appendItemSection(lines, "Result summary", event.ResultSummary, detailRow, agent)
	}
	return lines
}

func appendItemSection(lines []detailLine, title, text string, role detailRole, agent model.AgentKind) []detailLine {
	if text == "" {
		return lines
	}
	return appendItemLinesSection(lines, title, itemTextLines(text, role, agent), agent)
}

func appendItemLinesSection(lines []detailLine, title string, content []detailLine, agent model.AgentKind) []detailLine {
	if title == "" || len(content) == 0 {
		return lines
	}
	if len(lines) > 0 {
		lines = append(lines, detailLine{text: "", role: detailRow, agent: agent})
	}
	lines = append(lines, detailLine{text: title, role: detailHeader, agent: agent})
	return append(lines, content...)
}

func itemContentTitle(event model.Event) string {
	switch event.Kind {
	case model.EventUser:
		if event.Harness {
			return "Harness"
		}
		return "Prompt"
	case model.EventAssistantText:
		return "Message"
	case model.EventThinking:
		return "Thinking"
	case model.EventAdvisor:
		return "Advisor"
	case model.EventSystem:
		return "System"
	case model.EventCompact:
		return "Compact"
	case model.EventUsage:
		return "Usage"
	default:
		return ""
	}
}

func itemTextLines(text string, role detailRole, agent model.AgentKind) []detailLine {
	if text == "" {
		return nil
	}
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
		if event.Harness {
			return "Harness"
		}
		return "User"
	case model.EventAssistantText:
		if agent != "" {
			return terminalText(string(agent), 32) + " message"
		}
		return "Assistant"
	case model.EventThinking:
		return "Thinking"
	case model.EventAdvisor:
		return "Advisor"
	case model.EventCompact:
		return "Compact"
	case model.EventSystem:
		return "System"
	case model.EventUsage:
		return "Usage"
	default:
		return "Item"
	}
}

func itemKeyText(width int) string {
	timeHint := timeFormatKey + " time"
	return fitKeyHints(width, []string{"j/k scroll", "w wrap", timeHint, "esc back", "wheel scroll"}, []string{"wheel scroll", "j/k scroll", "w wrap", timeHint})
}
