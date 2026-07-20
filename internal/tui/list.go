package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

const (
	listAgentWidth     = 6
	listProjectWidth   = 8
	listProjectCap     = 18
	listTitleWidth     = 20
	listTitlePreferred = 40
	listModelWidth     = 13
	listAgeWidth       = 4
	listMessagesWidth  = 5
	listSubagentsWidth = 4
	listTokensWidth    = 9
	listCostWidth      = 7
	listCursorWidth    = 2
)

type listColumnKind int

const (
	columnAgent listColumnKind = iota
	columnProject
	columnTitle
	columnModel
	columnAge
	columnMessages
	columnSubagents
	columnTokens
	columnCost
)

type listColumn struct {
	kind  listColumnKind
	title string
	width int
	right bool
}

type sessionPresentation struct {
	usage         model.Usage
	cost          model.Cost
	subagentCount int
	model         string
}

func newSessionPresentation(session *model.Session) sessionPresentation {
	cost := session.TotalCost()
	return sessionPresentation{
		usage: session.TotalUsage(), cost: cost, subagentCount: subagentCount(session),
		model: shortModelsWithCost(session, cost),
	}
}

type listLayout struct {
	contextHeight      int
	sessionsHeight     int
	keyBarHeight       int
	rowCapacity        int
	sessionRowsY       int
	compact            bool
	compactPanelHeight int
}

func newListLayout(height int, filtering bool) listLayout {
	height = max(3, height)
	contextHeight := 3
	if filtering {
		contextHeight++
	}
	if height < contextHeight+5 {
		keyBarHeight := 1
		if height == 3 {
			keyBarHeight = 0
		}
		panelHeight := height - keyBarHeight
		rowCapacity := 0
		if panelHeight >= 4 {
			rowCapacity = 1
		}
		sessionRowsY := 2
		if filtering {
			sessionRowsY++
		}
		return listLayout{compact: true, compactPanelHeight: panelHeight, keyBarHeight: keyBarHeight, rowCapacity: rowCapacity, sessionRowsY: sessionRowsY}
	}
	sessionsHeight := height - contextHeight - 1
	return listLayout{
		contextHeight: contextHeight, sessionsHeight: sessionsHeight, keyBarHeight: 1,
		rowCapacity: max(0, sessionsHeight-3), sessionRowsY: contextHeight + 2,
	}
}

func (m Model) rowAtY(y int) (int, bool) {
	layout := newListLayout(m.height, m.filtering)
	if layout.compact {
		if y != layout.sessionRowsY || layout.sessionRowsY >= layout.compactPanelHeight-1 || layout.rowCapacity == 0 || len(m.visible) == 0 {
			return 0, false
		}
		return m.compactListIndex(), true
	}
	row := y - layout.sessionRowsY
	index := m.listOffset + row
	if row < 0 || row >= layout.rowCapacity || index < 0 || index >= len(m.visible) {
		return 0, false
	}
	return index, true
}

func (m Model) compactListIndex() int {
	return max(0, min(m.listOffset, len(m.visible)-1))
}

func listColumns(width int) []listColumn {
	if width <= 0 {
		return nil
	}
	columns := []listColumn{
		{kind: columnAgent, title: "AGENT", width: listAgentWidth},
		{kind: columnProject, title: "PROJECT", width: listProjectWidth},
		{kind: columnTitle, title: "TITLE", width: listTitleWidth},
		{kind: columnModel, title: "MODEL", width: listModelWidth},
		{kind: columnAge, title: "AGE", width: listAgeWidth, right: true},
		{kind: columnMessages, title: "MSGS", width: listMessagesWidth, right: true},
		{kind: columnSubagents, title: "SUBS", width: listSubagentsWidth, right: true},
		{kind: columnTokens, title: "TOKENS", width: listTokensWidth, right: true},
		{kind: columnCost, title: "$", width: listCostWidth, right: true},
	}
	for _, kind := range []listColumnKind{columnModel, columnMessages, columnSubagents, columnProject, columnAge} {
		if listColumnsWidth(columns) <= width {
			break
		}
		columns = removeListColumn(columns, kind)
	}
	if listColumnsWidth(columns) > width {
		for index := range columns {
			if columns[index].kind == columnTitle {
				columns[index].width = max(1, columns[index].width-(listColumnsWidth(columns)-width))
				break
			}
		}
	}
	for _, kind := range []listColumnKind{columnCost, columnTokens, columnAgent} {
		if listColumnsWidth(columns) <= width {
			break
		}
		columns = removeListColumn(columns, kind)
	}
	if len(columns) == 1 && listColumnsWidth(columns) > width {
		columns[0].width = width
	}

	slack := width - listColumnsWidth(columns)
	for index := range columns {
		if columns[index].kind == columnTitle {
			growth := min(slack, listTitlePreferred-columns[index].width)
			columns[index].width += growth
			slack -= growth
			break
		}
	}
	for index := range columns {
		if columns[index].kind == columnProject {
			growth := min(slack, listProjectCap-columns[index].width)
			columns[index].width += growth
			slack -= growth
			break
		}
	}
	for index := range columns {
		if columns[index].kind == columnTitle {
			columns[index].width += slack
			break
		}
	}
	return columns
}

func listColumnsWidth(columns []listColumn) int {
	total := max(0, len(columns)-1)
	for _, column := range columns {
		total += column.width
	}
	return total
}

func removeListColumn(columns []listColumn, kind listColumnKind) []listColumn {
	for index, column := range columns {
		if column.kind == kind {
			return append(columns[:index:index], columns[index+1:]...)
		}
	}
	return columns
}

func sessionCell(session *model.Session, now time.Time, column listColumn) string {
	return sessionCellWithPresentation(session, newSessionPresentation(session), now, column)
}

func sessionCellWithPresentation(session *model.Session, presentation sessionPresentation, now time.Time, column listColumn) string {
	switch column.kind {
	case columnAgent:
		label := terminalText(string(session.Agent), 32)
		if session.HasError {
			label = ansi.Truncate(label, max(0, column.width-1), "") + glyphWarning
		}
		return label
	case columnProject:
		return terminalText(session.Project, 96)
	case columnTitle:
		return terminalText(session.Title, 160)
	case columnModel:
		return terminalText(presentation.model, 96)
	case columnAge:
		return formatAge(now, session.UpdatedAt)
	case columnMessages:
		return compactCount(int64(session.Messages), column.width)
	case columnSubagents:
		if presentation.subagentCount == 0 {
			return ""
		}
		return compactCount(int64(presentation.subagentCount), column.width)
	case columnTokens:
		return formatPresentedTokens(presentation, column.width)
	case columnCost:
		return formatCostWidth(presentation.cost, column.width)
	default:
		return ""
	}
}

func formatTokens(session *model.Session, width int) string {
	return formatPresentedTokens(newSessionPresentation(session), width)
}

func formatPresentedTokens(presentation sessionPresentation, width int) string {
	return compactCount(presentation.usage.TotalTokens(), min(4, max(1, width)))
}

func sessionIdentity(session *model.Session) string {
	return string(session.Agent) + "\x00" + session.Path + "\x00" + session.ID
}

func subagentCount(session *model.Session) int {
	total := 0
	for _, subagent := range session.Subagents {
		total += 1 + subagentCount(subagent)
	}
	return total
}

func shortModels(session *model.Session) string {
	return shortModelsWithCost(session, session.TotalCost())
}

func shortModelsWithCost(session *model.Session, cost model.Cost) string {
	if len(session.Models) == 0 {
		return "—"
	}
	selected := session.Models[0]
	if len(session.ModelCosts) > 0 {
		for _, candidate := range session.Models[1:] {
			if session.ModelCosts[candidate] > session.ModelCosts[selected] {
				selected = candidate
			}
		}
	} else {
		tokens := make(map[string]int64)
		for _, usage := range session.Usage {
			tokens[usage.Model] += usage.TotalTokens()
		}
		for _, candidate := range session.Models[1:] {
			if tokens[candidate] > tokens[selected] {
				selected = candidate
			}
		}
	}
	name := shortModelName(selected)
	if len(session.Models) > 1 {
		name += fmt.Sprintf(" +%d", len(session.Models)-1)
	}
	for _, missing := range cost.MissingPricingModels {
		if missing == selected || len(session.Models) == 1 {
			name += "!"
			break
		}
	}
	return name
}

func shortModelName(name string) string {
	name = strings.TrimPrefix(name, "claude-")
	name = strings.TrimSuffix(name, "-sol")
	if strings.HasSuffix(name, "-4-8") {
		name = strings.TrimSuffix(name, "-4-8") + "-4.8"
	}
	return name
}

func formatAge(now, updated time.Time) string {
	if updated.IsZero() {
		return "—"
	}
	age := now.Sub(updated)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	case age < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	case age < 10*365*24*time.Hour:
		return fmt.Sprintf("%.1fy", age.Hours()/(24*365))
	default:
		return fmt.Sprintf("%.0fy", age.Hours()/(24*365))
	}
}

func humanTokens(tokens int64) string {
	return compactCount(tokens, 4)
}

func compactCount(value int64, maxWidth int) string {
	if value < 0 {
		value = 0
	}
	exact := strconv.FormatInt(value, 10)
	if len(exact) <= maxWidth {
		return exact
	}
	units := []string{"", "k", "M", "B", "T", "P", "E"}
	divisor := float64(1)
	unit := 0
	for unit+1 < len(units) && float64(value) >= divisor*1_000 {
		divisor *= 1_000
		unit++
	}
	for unit < len(units) {
		scaled := float64(value) / divisor
		for precision := 1; precision >= 0; precision-- {
			if precision == 1 && scaled >= 10 {
				continue
			}
			candidate := fmt.Sprintf("%.*f%s", precision, scaled, units[unit])
			if len(candidate) <= maxWidth {
				return candidate
			}
		}
		unit++
		divisor *= 1_000
	}
	return strings.Repeat("9", max(1, maxWidth))
}

func formatCost(cost model.Cost) string {
	return formatCostWidth(cost, listCostWidth)
}

func formatCostWidth(cost model.Cost, width int) string {
	prefix := "$"
	if cost.Estimated {
		prefix = "~$"
	}
	suffix := ""
	if len(cost.MissingPricingModels) > 0 {
		suffix = "!"
	}
	amountWidth := width - len(prefix) - len(suffix)
	return prefix + compactDollars(cost.USD, amountWidth) + suffix
}

func compactDollars(usd float64, maxWidth int) string {
	if math.IsNaN(usd) || usd < 0 {
		usd = 0
	}
	if math.IsInf(usd, 1) {
		return "∞"
	}
	units := []string{"", "k", "M", "B", "T", "P", "E"}
	divisor := float64(1)
	unit := 0
	for unit+1 < len(units) && usd >= divisor*1_000 {
		divisor *= 1_000
		unit++
	}
	for unit < len(units) {
		scaled := usd / divisor
		precision := 0
		if unit == 0 && scaled < 10 {
			precision = 2
		} else if unit > 0 && scaled < 10 {
			precision = 1
		}
		for ; precision >= 0; precision-- {
			candidate := fmt.Sprintf("%.*f%s", precision, scaled, units[unit])
			if len(candidate) <= maxWidth {
				return candidate
			}
		}
		unit++
		divisor *= 1_000
	}
	return strings.Repeat("9", max(1, maxWidth))
}

func terminalText(value string, maxRunes int) string {
	var output strings.Builder
	count := 0
	for _, r := range value {
		if count >= maxRunes {
			break
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			r = ' '
		}
		output.WriteRune(r)
		count++
	}
	return strings.Join(strings.Fields(output.String()), " ")
}

func (m Model) listView() string {
	layout := newListLayout(m.height, m.filtering)
	if layout.compact {
		return m.compactListView(layout)
	}
	context := m.listSummary()
	innerWidth := max(0, m.width-2)
	contextLine := fitPlain(context, innerWidth, false)
	contextLines := []panelLine{{plain: contextLine, styled: m.styles.row.Render(contextLine)}}
	contextHeight := layout.contextHeight
	if m.filtering {
		filterLine := m.filterInputLine(innerWidth)
		contextLines = append(contextLines, panelLine{plain: filterLine, styled: m.styles.accent.Render(filterLine)})
	}
	contextPanel := renderPanel("agtlog", "", contextLines, m.width, contextHeight, m.styles)
	sessionsPanel := m.sessionsPanel(layout.sessionsHeight)
	keyBar := m.renderKeyBar()
	return strings.Join([]string{contextPanel, sessionsPanel, keyBar}, "\n")
}

func (m Model) compactListView(layout listLayout) string {
	innerWidth := max(0, m.width-2)
	summary := fitPlain(m.listSummary(), innerWidth, false)
	content := make([]panelLine, 0, max(0, layout.compactPanelHeight-2))
	if m.filtering {
		plain := m.filterInputLine(innerWidth)
		content = append(content, panelLine{plain: plain, styled: m.styles.accent.Render(plain)})
	}
	if len(content) < layout.compactPanelHeight-2 {
		content = append(content, panelLine{plain: summary, styled: m.styles.row.Render(summary)})
	}
	if len(content) < layout.compactPanelHeight-2 {
		if len(m.visible) > 0 {
			columns := listColumns(max(0, innerWidth-listCursorWidth))
			index := m.compactListIndex()
			content = append(content, renderSessionPanelLine(m.visible[index], m.now(), columns, innerWidth, index == m.cursor, m.styles))
		} else {
			plain := fitPlain("No sessions found.", innerWidth, false)
			content = append(content, panelLine{plain: plain, styled: m.styles.muted.Render(plain)})
		}
	}
	for len(content) < layout.compactPanelHeight-2 {
		plain := strings.Repeat(" ", innerWidth)
		content = append(content, panelLine{plain: plain, styled: m.styles.row.Render(plain)})
	}
	panel := renderPanel("agtlog · Sessions", "", content, m.width, layout.compactPanelHeight, m.styles)
	if layout.keyBarHeight == 0 {
		return panel
	}
	return panel + "\n" + m.renderKeyBar()
}

func (m Model) filterInputLine(width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(m.filter.Value())
	for index, r := range runes {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			runes[index] = ' '
		}
	}
	position := max(0, min(m.filter.Position(), len(runes)))
	before, after := string(runes[:position]), string(runes[position:])
	available := max(0, width-1)
	input := before + "▊" + after
	cursorColumn := ansi.StringWidth(before)
	start := max(0, cursorColumn-max(0, available-2))
	visible := ansi.Cut(input, start, start+available)
	if start > 0 && available > 0 {
		visible = "…" + ansi.Cut(input, start+1, start+available)
	}
	return fitPlain("/"+visible, width, false)
}

func (m Model) sessionsPanel(height int) string {
	innerWidth := max(0, m.width-2)
	innerHeight := max(0, height-2)
	columns := listColumns(max(0, innerWidth-listCursorWidth))
	content := make([]panelLine, 0, innerHeight)
	if innerHeight > 0 {
		content = append(content, renderListHeaderLine(columns, innerWidth, m.styles))
	}
	rowCapacity := max(0, innerHeight-1)
	start := min(max(0, m.listOffset), max(0, len(m.visible)-rowCapacity))
	end := min(len(m.visible), start+rowCapacity)
	for index := start; index < end; index++ {
		content = append(content, renderSessionPanelLine(m.visible[index], m.now(), columns, innerWidth, index == m.cursor, m.styles))
	}
	if len(m.visible) == 0 && rowCapacity > 0 {
		plain := fitPlain("No sessions found.", innerWidth, false)
		content = append(content, panelLine{plain: plain, styled: m.styles.muted.Render(plain)})
		if len(m.sessions) == 0 && rowCapacity > 1 {
			plain := fitPlain("Check ~/.claude, ~/.codex, or configured agent roots; press ? for keys.", innerWidth, false)
			content = append(content, panelLine{plain: plain, styled: m.styles.muted.Render(plain)})
		}
	}
	for len(content) < innerHeight {
		plain := strings.Repeat(" ", innerWidth)
		content = append(content, panelLine{plain: plain, styled: m.styles.row.Render(plain)})
	}
	title := "Sessions"
	if m.filter.Value() != "" || m.agent != agentAll {
		title += " · filtered"
	}
	hint := ""
	if len(m.visible) > rowCapacity && len(m.visible) > 0 {
		hint = fmt.Sprintf("%d/%d", m.cursor+1, len(m.visible))
	}
	return renderPanel(title, hint, content, m.width, height, m.styles)
}

func renderListHeader(columns []listColumn, width int, styles styles) string {
	return renderListHeaderLine(columns, width, styles).styled
}

func renderListHeaderLine(columns []listColumn, width int, styles styles) panelLine {
	markerWidth := min(listCursorWidth, max(0, width))
	bodyWidth := max(0, width-markerWidth)
	marker := strings.Repeat(" ", markerWidth)
	plainCells := make([]string, len(columns))
	cells := make([]string, len(columns))
	for index, column := range columns {
		plain := fitPlain(column.title, column.width, column.right)
		plainCells[index] = plain
		cells[index] = styles.header.Render(plain)
	}
	if len(columns) == 0 {
		plain := strings.Repeat(" ", width)
		return panelLine{plain: plain, styled: styles.header.Render(plain)}
	}
	plainBody := fitPlain(strings.Join(plainCells, " "), bodyWidth, false)
	styledBody := strings.Join(cells, styles.header.Render(" "))
	return panelLine{plain: marker + plainBody, styled: styles.header.Render(marker) + styledBody}
}

func renderSessionRow(session *model.Session, now time.Time, columns []listColumn, width int, selected bool, styles styles) string {
	return renderSessionPanelLine(session, now, columns, width, selected, styles).styled
}

func renderSessionPanelLine(session *model.Session, now time.Time, columns []listColumn, width int, selected bool, styles styles) panelLine {
	markerWidth := min(listCursorWidth, max(0, width))
	bodyWidth := max(0, width-markerWidth)
	presentation := newSessionPresentation(session)
	plainCells := make([]string, len(columns))
	for index, column := range columns {
		plainCells[index] = fitPlain(sessionCellWithPresentation(session, presentation, now, column), column.width, column.right)
	}
	marker := fitPlain("  ", markerWidth, false)
	if selected {
		marker = fitPlain("› ", markerWidth, false)
	}
	plainBody := fitPlain(strings.Join(plainCells, " "), bodyWidth, false)
	plainRow := marker + plainBody
	if selected {
		return panelLine{plain: plainRow, styled: styles.selected.Render(plainRow)}
	}
	styledCells := make([]string, len(columns))
	for index, column := range columns {
		styledCells[index] = styleSessionCell(plainCells[index], session, presentation, column, styles)
	}
	return panelLine{plain: plainRow, styled: styles.row.Render(marker) + strings.Join(styledCells, styles.row.Render(" "))}
}

func styleSessionCell(cell string, session *model.Session, presentation sessionPresentation, column listColumn, styles styles) string {
	switch column.kind {
	case columnAgent:
		agentStyle := styles.row
		if session.Agent == model.AgentClaude {
			agentStyle = styles.claude
		} else if session.Agent == model.AgentCodex {
			agentStyle = styles.codex
		}
		if marker := strings.Index(cell, glyphWarning); marker >= 0 {
			return agentStyle.Render(cell[:marker]) + styles.warning.Render(glyphWarning) + agentStyle.Render(cell[marker+len(glyphWarning):])
		}
		return agentStyle.Render(cell)
	case columnAge, columnMessages:
		return styles.muted.Render(cell)
	case columnSubagents:
		return styles.accent.Render(cell)
	case columnCost:
		if presentation.cost.Estimated {
			return styles.estimated.Render(cell)
		}
	case columnModel:
		if len(presentation.cost.MissingPricingModels) > 0 {
			return styles.warning.Render(cell)
		}
	}
	return styles.row.Render(cell)
}

func (m Model) renderKeyBar() string {
	hints := []string{"↑/↓ move", "/ filter", "s sort", "a agent", "↵ open", "r refresh", "mouse scroll/click"}
	if !m.styles.mono {
		hints = append(hints, "t theme")
	}
	hints = append(hints, "? help", "q quit")
	plain := fitKeyHints(m.width, hints, []string{"mouse scroll/click", "t theme", "r refresh", "a agent", "s sort", "? help", "/ filter", "↑/↓ move", "q quit", "↵ open"})
	return m.styles.keyHint.Render(fitPlain(plain, m.width, false))
}

func (m Model) listSummary() string {
	totalLabel := formatCost(m.visibleCost)
	if len(m.visibleCost.MissingPricingModels) > 0 {
		totalLabel += " partial"
	}
	state := fmt.Sprintf("%d sessions · %d projects · %s total · watching %d roots", len(m.visible), m.visibleProjects, totalLabel, m.watchedRootCount())
	if query := terminalText(m.filter.Value(), 24); query != "" && !m.filtering {
		state += " · /" + query
	}
	if m.sort != sortAge {
		label := "tokens"
		if m.sort == sortCost {
			label = "cost"
		}
		state += " · sort:" + label
	}
	if m.agent != agentAll {
		label := "claude"
		if m.agent == agentCodex {
			label = "codex"
		}
		state += " · agent:" + label
	}
	if m.status != "" {
		state += " · " + m.status
	}
	if m.theme.Name == "mono" {
		state += " · theme:mono"
	}
	return state
}

func (m Model) watchedRootCount() int {
	return m.watchingRoots
}

type panelLine struct {
	plain  string
	styled string
}

type panelLabel struct {
	plain  string
	styled string
}

func renderPanel(title, bottomHint string, content []panelLine, width, height int, styles styles) string {
	return renderPanelWithLabel(panelLabel{plain: title}, bottomHint, content, width, height, styles)
}

func renderPanelWithLabel(title panelLabel, bottomHint string, content []panelLine, width, height int, styles styles) string {
	width, height = max(1, width), max(1, height)
	if width == 1 {
		return strings.TrimSuffix(strings.Repeat("|\n", height), "\n")
	}
	if height == 1 {
		return ansi.Truncate(title.plain, width, "…")
	}
	border := widthSafeBorder(lipgloss.RoundedBorder())
	innerWidth := width - 2
	lines := make([]string, 0, height)
	lines = append(lines, styles.border.Render(border.TopLeft)+panelRuleLabel(border.Top, title, innerWidth, false, styles)+styles.border.Render(border.TopRight))
	for index := 0; index < height-2; index++ {
		line := panelLine{}
		if index < len(content) {
			line = content[index]
		}
		fitted := fitPlain(line.plain, innerWidth, false)
		rendered := line.styled
		if rendered == "" || fitted != line.plain {
			rendered = styles.row.Render(fitted)
		}
		lines = append(lines, styles.border.Render(border.Left)+rendered+styles.border.Render(border.Right))
	}
	lines = append(lines, styles.border.Render(border.BottomLeft)+panelRule(border.Bottom, bottomHint, innerWidth, true, styles)+styles.border.Render(border.BottomRight))
	return strings.Join(lines, "\n")
}

func widthSafeBorder(candidate lipgloss.Border) lipgloss.Border {
	glyphs := []string{
		candidate.Top, candidate.Bottom, candidate.Left, candidate.Right,
		candidate.TopLeft, candidate.TopRight, candidate.BottomLeft, candidate.BottomRight,
	}
	for _, glyph := range glyphs {
		if ansi.StringWidth(glyph) != 1 {
			return lipgloss.Border{
				Top: "-", Bottom: "-", Left: "|", Right: "|",
				TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
				MiddleLeft: "+", MiddleRight: "+", Middle: "+", MiddleTop: "+", MiddleBottom: "+",
			}
		}
	}
	return candidate
}

func panelRule(rule, label string, width int, right bool, styles styles) string {
	return panelRuleLabel(rule, panelLabel{plain: label}, width, right, styles)
}

func panelRuleLabel(rule string, label panelLabel, width int, right bool, styles styles) string {
	if label.plain == "" || width < 4 {
		return styles.border.Render(strings.Repeat(rule, width))
	}
	plain := ansi.Truncate(panelLabelText(label.plain, 256), max(1, width-3), "…")
	styled := styles.title.Render(plain)
	if plain == label.plain && label.styled != "" {
		styled = label.styled
	}
	decorated := " " + plain + " "
	remaining := max(0, width-ansi.StringWidth(decorated))
	decoratedStyled := styles.title.Render(" ") + styled + styles.title.Render(" ")
	if right {
		return styles.border.Render(strings.Repeat(rule, remaining)) + decoratedStyled
	}
	return styles.border.Render(rule) + decoratedStyled + styles.border.Render(strings.Repeat(rule, max(0, remaining-1)))
}

func panelLabelText(value string, maxRunes int) string {
	var output strings.Builder
	count := 0
	for _, char := range ansi.Strip(value) {
		if count >= maxRunes {
			break
		}
		if unicode.IsControl(char) || unicode.In(char, unicode.Cf) {
			char = ' '
		}
		output.WriteRune(char)
		count++
	}
	return strings.TrimSpace(output.String())
}

func fitPlain(value string, width int, right bool) string {
	if width <= 0 {
		return ""
	}
	value = ansi.Truncate(value, width, "…")
	padding := strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
	if right {
		return padding + value
	}
	return value + padding
}
