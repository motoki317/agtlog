package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

func (d *detailState) sessionLines(session *model.Session, path string) []detailLine {
	var lines []detailLine
	for index, event := range session.Events {
		if event.Kind == model.EventUser {
			lines = append(lines, d.userPromptLines(event, timelineUserKey(path, index), nextRequestContext(session.Events, index))...)
			continue
		}
		lines = append(lines, d.eventLines(session, event, timelineEventKey(path, index))...)
	}
	if len(lines) == 0 {
		lines = append(lines, detailLine{text: "No timeline events.", role: detailSecondary})
	}
	return lines
}

// nextRequestContext returns the prompt tokens of the first billed request after
// a user prompt. It returns 0 if another prompt or the end of the log comes
// first. No log counts the tokens of a user message, so the prompt row shows the
// context of the request that it triggered.
func nextRequestContext(events []model.Event, userIndex int) int64 {
	for _, event := range events[userIndex+1:] {
		if event.Kind == model.EventUser {
			return 0
		}
		if event.Usage != nil {
			return event.Usage.PromptTokens()
		}
	}
	return 0
}

func contextPart(tokens int64) (string, bool) {
	if tokens <= 0 {
		return "", false
	}
	return "ctx " + humanTokens(tokens), true
}

// costPart omits a cost that rounds to $0.00, so that sub-cent costs do not fill
// every row.
func costPart(usd float64, priced, estimated bool) (string, bool) {
	if !priced || usd < 0.005 {
		return "", false
	}
	return formatCost(model.Cost{USD: usd, Estimated: estimated}), true
}

func eventMetricParts(event model.Event) []string {
	if event.Usage == nil {
		return nil
	}
	usage := *event.Usage
	parts := []string{formatTokenFlow(usage)}
	if part, ok := costPart(event.Cost.Total(), event.Priced, event.CostEstimated); ok {
		parts = append(parts, part)
	}
	// A request row reports its context after the output, so ctx includes the
	// output tokens.
	if part, ok := contextPart(usage.TotalTokens()); ok {
		parts = append(parts, part)
	}
	return parts
}

// foldMarker is the only source of the fold glyph for timeline rows, so the
// glyph appears only on a row that can fold.
func foldMarker(expandable, expanded bool) string {
	switch {
	case !expandable:
		return " "
	case expanded:
		return glyphExpanded
	default:
		return glyphCollapsed
	}
}

func timelineUserKey(path string, index int) string {
	return fmt.Sprintf("%s/user/%d", path, index)
}

// timelinePreviewCap is the display width above which a single-line message can
// fold. Its body then shows the full text that the row header truncates.
const timelinePreviewCap = 80

// textExpandable reports whether the body shows more than the collapsed row: a
// second non-blank line, or one line wider than timelinePreviewCap.
func textExpandable(text string) bool {
	seen := false
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if seen {
			return true
		}
		seen = true
	}
	return ansi.StringWidth(strings.TrimSpace(text)) > timelinePreviewCap
}

func (d *detailState) userPromptLines(event model.Event, key string, context int64) []detailLine {
	expandable := textExpandable(event.Text)
	expanded := expandable && d.isExpanded(key)
	label := "you:"
	role := detailUserPrompt
	if event.Harness {
		label = "harness:"
		role = detailSystemPrompt
	}
	prefix := foldMarker(expandable, expanded) + " " + label
	text := prefix
	if !expanded {
		if summary := firstLine(event.Text); summary != "" {
			text = prefix + " " + summary
		}
	}
	metrics := ""
	if part, ok := contextPart(context); ok {
		metrics = part
	}
	lines := []detailLine{{text: text, label: label, metrics: metrics, key: key, nowrap: true, expandable: expandable, role: role, event: event}}
	if !expanded {
		return lines
	}
	for _, line := range timelineBodyLines(event.Text) {
		lines = append(lines, detailLine{text: timelineBodyIndent + model.TerminalText(line), role: role})
	}
	return lines
}

func metricsText(parts []string) string {
	return strings.Join(parts, " · ")
}

// composeMetricRow right-aligns the metrics so that they form one column across
// rows. If the row cannot hold both parts, the left text yields, because readers
// scan the metrics column.
func composeMetricRow(left, metrics string, width int) string {
	if width <= 0 {
		return ""
	}
	if metrics == "" {
		return fitPlain(left, width, false)
	}
	metricWidth := ansi.StringWidth(metrics)
	if metricWidth+1 >= width {
		return fitPlain(metrics, width, true)
	}
	left = ansi.Truncate(left, width-metricWidth-1, "…")
	pad := width - ansi.StringWidth(left) - metricWidth
	return left + strings.Repeat(" ", pad) + metrics
}

func timelineEventKey(path string, index int) string {
	return fmt.Sprintf("%s/event/%d", path, index)
}

func (d *detailState) eventLines(session *model.Session, event model.Event, key string) []detailLine {
	switch event.Kind {
	case model.EventAssistantText:
		return d.assistantTextLines(event, key, session.Agent)
	case model.EventThinking:
		text := foldMarker(false, false) + " " + glyphSecondary + " thinking: " + firstLine(event.Text)
		return []detailLine{{text: text, metrics: metricsText(eventMetricParts(event)), key: key, nowrap: true, role: detailSecondary, event: event}}
	case model.EventToolCall:
		return d.toolEventLines(event, key)
	case model.EventSubagent:
		childMarker := foldMarker(false, false) + " "
		if event.Subagent == nil {
			return []detailLine{{text: childMarker + glyphSubagent + " subagent unavailable", key: key, subagent: true, role: detailWarning, event: event}}
		}
		childKey := key + "/subagent/" + sessionIdentity(event.Subagent)
		title := firstLine(event.ToolInput)
		toolName := "Task"
		if event.ToolName == "Workflow" {
			toolName = event.ToolName
			if workflowName := firstLine(event.Subagent.Title); workflowName != "" {
				title = workflowName
			}
		}
		if title == "" {
			title = firstLine(event.Subagent.Title)
		}
		title = ansi.Truncate(title, 28, "…")
		typeLabel := glyphSubagent + " " + toolName
		text := childMarker + typeLabel + "(" + title + ")"
		if model := model.TerminalLine(shortModels(event.Subagent), 96); model != "" {
			text += " " + model
		}
		metrics := humanTokens(event.Subagent.TotalUsage().TotalTokens()) + " · " + formatCost(event.Subagent.TotalCost())
		return []detailLine{{text: text, label: typeLabel, metrics: metrics, key: childKey, nowrap: true, subagent: true, subagentSession: event.Subagent, role: detailAccent, event: event}}
	case model.EventAdvisor:
		label := "advisor"
		if event.Model != "" {
			label = "advisor(" + model.TerminalLine(shortModelName(event.Model), 96) + ")"
		}
		label = glyphSubagent + " " + label
		text := foldMarker(false, false) + " " + label
		return []detailLine{{text: text, label: label, metrics: metricsText(eventMetricParts(event)), key: key, nowrap: true, role: detailAccent, event: event}}
	case model.EventCompact:
		text := foldMarker(false, false) + " " + glyphSecondary + " " + compactTitle(event.CompactTrigger)
		if event.CompactPostTokens > 0 {
			text += " · ctx " + humanTokens(event.CompactPostTokens)
		}
		return []detailLine{{text: text, key: key, role: detailSystemPrompt, event: event}}
	case model.EventSystem:
		return []detailLine{{text: foldMarker(false, false) + " " + glyphSecondary + " " + firstLine(event.Text), key: key, role: detailSystemPrompt, event: event}}
	case model.EventUsage:
		title := firstLine(event.Text)
		if event.Model != "" {
			title += " (" + model.TerminalLine(shortModelName(event.Model), 96) + ")"
		}
		text := foldMarker(false, false) + " " + glyphSecondary + " " + title
		return []detailLine{{text: text, metrics: metricsText(eventMetricParts(event)), key: key, nowrap: true, role: detailSystemPrompt, event: event}}
	default:
		return nil
	}
}

func compactTitle(trigger string) string {
	switch trigger {
	case "manual":
		return "Session manually compacted"
	case "auto":
		return "Session automatically compacted"
	default:
		return "Session compacted"
	}
}

func (d *detailState) toolEventLines(event model.Event, key string) []detailLine {
	expandable := detailHasBody(event)
	expanded := expandable && d.isExpanded(key)
	text := foldMarker(expandable, expanded) + " " + toolLine(event, expanded)
	label := glyphTool + " " + toolDisplayName(event.ToolName)
	lines := []detailLine{{text: text, label: label, metrics: metricsText(eventMetricParts(event)), key: key, nowrap: true, expandable: expandable, role: detailTool, event: event}}
	if !expanded {
		return lines
	}
	if event.Detail.Diff != "" {
		for _, text := range timelineBodyLines(event.Detail.Diff) {
			plain := model.TerminalText(text)
			role := detailDiffContext
			if strings.HasPrefix(plain, "+") {
				role = detailDiffAdd
			} else if strings.HasPrefix(plain, "-") {
				role = detailDiffRemove
			}
			lines = append(lines, detailLine{text: timelineBodyIndent + plain, role: role})
		}
	}
	for _, section := range []struct {
		label string
		text  string
	}{{label: "input:", text: detailInputBody(event)}, {label: "output:", text: event.Detail.Output}} {
		if section.text == "" {
			continue
		}
		lines = append(lines, detailLine{text: timelineBodyIndent + section.label, role: detailSecondary})
		for _, text := range timelineBodyLines(section.text) {
			lines = append(lines, detailLine{text: timelineBodyIndent + model.TerminalText(text), role: detailRow})
		}
	}
	return lines
}

func detailInputBody(event model.Event) string {
	if event.Detail == nil || event.Detail.Input == "" {
		return ""
	}
	switch event.ToolName {
	case "Read", "Edit", "MultiEdit", "Write", "apply_patch":
		return ""
	default:
		return event.Detail.Input
	}
}

func detailHasBody(event model.Event) bool {
	detail := event.Detail
	return detail != nil && (detail.Diff != "" || detail.Output != "" || detailInputBody(event) != "" && strings.Contains(detail.Input, "\n"))
}

func (d *detailState) assistantTextLines(event model.Event, key string, agent model.AgentKind) []detailLine {
	label := model.TerminalLine(string(agent), 32) + ":"
	expandable := textExpandable(event.Text)
	expanded := expandable && d.isExpanded(key)
	text := foldMarker(expandable, expanded) + " " + label
	if !expanded {
		if summary := firstLine(event.Text); summary != "" {
			text += " " + summary
		}
	}
	lines := []detailLine{{text: text, label: label, metrics: metricsText(eventMetricParts(event)), key: key, nowrap: true, expandable: expandable, role: detailAssistant, agent: agent, event: event}}
	if !expanded {
		return lines
	}
	for _, line := range timelineBodyLines(event.Text) {
		lines = append(lines, detailLine{text: timelineBodyIndent + model.TerminalText(line), role: detailAssistant})
	}
	return lines
}

func toolLine(event model.Event, expanded bool) string {
	name := toolDisplayName(event.ToolName)
	line := glyphTool + " " + name
	bodyShowsInput := expanded && detailInputBody(event) != ""
	bodyShowsOutput := expanded && event.Detail != nil && event.Detail.Output != ""
	if input := firstLine(event.ToolInput); input != "" && !bodyShowsInput {
		line += "(" + input + ")"
	}
	if event.ResultSummary != "" && !bodyShowsOutput {
		line += " → " + firstLine(event.ResultSummary)
	}
	if event.Duration > 0 {
		line += " · " + formatDuration(event.Duration)
	}
	return line
}

func toolDisplayName(name string) string {
	name = model.TerminalLine(name, 96)
	if name == "exec_command" {
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
		if line = model.TerminalLine(line, 512); line != "" {
			return line
		}
	}
	return ""
}

func timelineBodyLines(text string) []string {
	type sourceLine struct {
		start int
		end   int
		runes int
	}

	headLines := make([]sourceLine, 0, detailPreviewLineCap)
	tailLines := make([]sourceLine, detailPreviewLineCap)
	totalLines, totalRunes := 0, 0
	var firstContent sourceLine
	hasContent := false
	for start := 0; ; {
		end := len(text)
		if newline := strings.IndexByte(text[start:], '\n'); newline >= 0 {
			end = start + newline
		}
		runes := utf8.RuneCountInString(text[start:end])
		if end < len(text) {
			runes++
		}
		line := sourceLine{start: start, end: end, runes: runes}
		if !hasContent && timelineLineHasVisibleContent(text[start:end]) {
			firstContent, hasContent = line, true
		}
		if len(headLines) < detailPreviewLineCap {
			headLines = append(headLines, line)
		}
		tailLines[totalLines%detailPreviewLineCap] = line
		totalLines++
		totalRunes += runes
		if end == len(text) {
			break
		}
		start = end + 1
	}
	if totalRunes <= detailPreviewRuneCap && totalLines <= detailPreviewLineCap {
		bounded := make([]string, len(headLines))
		for index, line := range headLines {
			bounded[index] = text[line.start:line.end]
		}
		return bounded
	}
	if totalLines == 1 {
		return []string{truncateTimelineLine(text, detailPreviewRuneCap)}
	}

	headLimit := (detailPreviewLineCap - 1) / 2
	tailLimit := detailPreviewLineCap - 1 - headLimit
	head, tail := make([]sourceLine, 0, headLimit), make([]sourceLine, 0, tailLimit)
	selectedRunes := 0
	fits := func(candidateRunes, headCount, tailCount int) bool {
		hidden := totalLines - headCount - tailCount
		markerRunes := utf8.RuneCountInString(timelineHiddenMarker(hidden))
		if tailCount > 0 {
			markerRunes++
		}
		return candidateRunes+markerRunes <= detailPreviewRuneCap
	}
	for headIndex, tailIndex := 0, totalLines-1; headIndex <= tailIndex; {
		progressed := false
		if headIndex < headLimit {
			line := headLines[headIndex]
			if fits(selectedRunes+line.runes, len(head)+1, len(tail)) {
				head = append(head, line)
				selectedRunes += line.runes
				headIndex++
				progressed = true
			}
		}
		if headIndex <= tailIndex && len(tail) < tailLimit {
			line := tailLines[tailIndex%detailPreviewLineCap]
			if fits(selectedRunes+line.runes, len(head), len(tail)+1) {
				tail = append(tail, line)
				selectedRunes += line.runes
				tailIndex--
				progressed = true
			}
		}
		if !progressed {
			break
		}
		if headIndex == headLimit && len(tail) == tailLimit {
			break
		}
	}
	selectedHasContent := false
	for _, line := range head {
		selectedHasContent = selectedHasContent || timelineLineHasVisibleContent(text[line.start:line.end])
	}
	for _, line := range tail {
		selectedHasContent = selectedHasContent || timelineLineHasVisibleContent(text[line.start:line.end])
	}
	if hasContent && !selectedHasContent {
		for {
			hidden := totalLines - len(head) - len(tail) - 1
			lineRunes := detailPreviewRuneCap - selectedRunes
			rows := len(head) + 1 + len(tail)
			if hidden > 0 {
				lineRunes -= utf8.RuneCountInString(timelineHiddenMarker(hidden)) + 1
				rows++
				if len(tail) > 0 {
					lineRunes--
				}
			} else if len(tail) > 0 {
				lineRunes--
			}
			if rows <= detailPreviewLineCap && lineRunes > 1 {
				bounded := make([]string, 0, rows)
				for _, line := range head {
					bounded = append(bounded, text[line.start:line.end])
				}
				bounded = append(bounded, truncateTimelineLine(text[firstContent.start:firstContent.end], lineRunes))
				if hidden > 0 {
					bounded = append(bounded, timelineHiddenMarker(hidden))
				}
				for index := len(tail) - 1; index >= 0; index-- {
					line := tail[index]
					bounded = append(bounded, text[line.start:line.end])
				}
				return bounded
			}
			if len(tail) > 0 {
				selectedRunes -= tail[len(tail)-1].runes
				tail = tail[:len(tail)-1]
			} else if len(head) > 0 {
				selectedRunes -= head[len(head)-1].runes
				head = head[:len(head)-1]
			} else {
				break
			}
		}
	}
	if len(head) == 0 {
		hidden := totalLines - 1 - len(tail)
		lineRunes := detailPreviewRuneCap - selectedRunes - 1
		if hidden > 0 {
			lineRunes -= utf8.RuneCountInString(timelineHiddenMarker(hidden))
			if len(tail) > 0 {
				lineRunes--
			}
		}
		if lineRunes > 1 {
			line := headLines[0]
			bounded := []string{truncateTimelineLine(text[line.start:line.end], lineRunes)}
			if hidden > 0 {
				bounded = append(bounded, timelineHiddenMarker(hidden))
			}
			for index := len(tail) - 1; index >= 0; index-- {
				line = tail[index]
				bounded = append(bounded, text[line.start:line.end])
			}
			return bounded
		}
	}

	bounded := make([]string, 0, len(head)+1+len(tail))
	for _, line := range head {
		bounded = append(bounded, text[line.start:line.end])
	}
	bounded = append(bounded, timelineHiddenMarker(totalLines-len(head)-len(tail)))
	for index := len(tail) - 1; index >= 0; index-- {
		line := tail[index]
		bounded = append(bounded, text[line.start:line.end])
	}
	return bounded
}

func timelineLineHasVisibleContent(line string) bool {
	var state byte
	for len(line) > 0 {
		sequence, width, consumed, nextState := ansi.DecodeSequence(line, state, nil)
		if consumed == 0 {
			return false
		}
		line = line[consumed:]
		state = nextState
		if width == 0 {
			continue
		}
		for _, char := range sequence {
			if !unicode.IsSpace(char) && !unicode.IsControl(char) && !unicode.In(char, unicode.Cf) {
				return true
			}
		}
	}
	return false
}

func truncateTimelineLine(line string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}

	var output strings.Builder
	output.Grow(min(len(line), maxRunes*utf8.UTFMax))
	state, runes, lastRuneStart := byte(0), 0, 0
	for len(line) > 0 {
		sequence, width, consumed, nextState := ansi.DecodeSequence(line, state, nil)
		if consumed == 0 {
			break
		}
		line = line[consumed:]
		state = nextState

		if width == 0 {
			char, size := utf8.DecodeRuneInString(sequence)
			if size != len(sequence) || char == utf8.RuneError && size == 1 || char == '\x1b' {
				continue
			}
		}
		for _, char := range sequence {
			if unicode.IsControl(char) || unicode.In(char, unicode.Cf) {
				char = ' '
			}
			if runes == maxRunes {
				bounded := output.String()
				return bounded[:lastRuneStart] + "…"
			}
			lastRuneStart = output.Len()
			output.WriteRune(char)
			runes++
		}
	}
	return output.String()
}

func timelineHiddenMarker(hidden int) string {
	if hidden == 1 {
		return "… 1 line hidden …"
	}
	return fmt.Sprintf("… %d lines hidden …", hidden)
}
