package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

type activityColumn struct {
	title string
	width int
}

func activityLines(session *model.Session, width int) []detailLine {
	columns := []activityColumn{{"Activity", 12}, {"TURNS", 5}, {"TOKENS", 6}, {"COST", 7}}
	columnsWidth := func() int {
		total := max(0, len(columns)-1)
		for _, column := range columns {
			total += column.width
		}
		return total
	}
	if columnsWidth() > width {
		for i, column := range columns {
			if column.title == "TOKENS" {
				columns = append(columns[:i], columns[i+1:]...)
				break
			}
		}
	}
	for _, name := range []string{"Activity", "TURNS", "COST"} {
		for i, column := range columns {
			if column.title == name && columnsWidth() > width {
				columns[i].width -= min(column.width-1, columnsWidth()-width)
			}
		}
	}
	for len(columns) > 0 && columnsWidth() > width {
		columns = columns[:len(columns)-1]
	}
	row := func(label string, turns int, usage model.Usage, cost model.Cost, header bool) detailLine {
		cells := make([]string, len(columns))
		for i, column := range columns {
			value := column.title
			if !header {
				switch column.title {
				case "Activity":
					value = label
				case "TURNS":
					value = compactCount(int64(turns), column.width)
				case "TOKENS":
					value = humanTokens(usage.TotalTokens())
				case "COST":
					value = formatCost(cost)
				}
			}
			cells[i] = fitPlain(value, column.width, i > 0)
		}
		role := detailRow
		if header {
			role = detailHeader
		}
		return detailLine{text: strings.Join(cells, " "), role: role, nowrap: true}
	}
	lines := []detailLine{row("", 0, model.Usage{}, model.Cost{}, true), row("own", session.Turns(), session.OwnedSelfUsage(), session.OwnedSelfCost(), false)}
	if len(session.Subagents) > 0 {
		turns := session.TotalTurns()
		lines = append(lines, row("subagents", turns-session.Turns(), session.OwnedDescendantUsage(), session.OwnedDescendantCost(), false), row("total", turns, session.OwnedUsage(), session.OwnedCost(), false))
	}
	return lines
}

func (d *detailState) overviewLines() []detailLine {
	width := max(0, d.viewport.Width-2)
	lines := activityLines(d.session, width)
	lines = append(lines, overviewAttributionLines(d.session)...)
	lines = append(lines, overviewLine("", detailRow), overviewLine("Own model costs", detailHeader))
	models := ownModelCosts(d.session)
	if len(models) == 0 {
		lines = append(lines, overviewLine("No own model usage.", detailSecondary))
	}
	estimated := false
	for _, item := range models {
		estimated = estimated || item.estimated
		lines = append(lines, overviewLine(item.title, detailRow))
		for _, line := range item.lines {
			lines = append(lines, overviewLine(line, detailRow))
		}
	}
	if estimated {
		lines = append(lines, overviewLine("Both agents use the same rate table. ~ means the applied rate is not the logged model's own published rate.", detailSecondary))
	}
	lines = append(lines, overviewLine("", detailRow), overviewLine(fmt.Sprintf("Subagents (%d)", d.subagentTotal), detailHeader))
	d.subagents = flattenSubagents(d.session, d.subagentSort)
	if len(d.subagents) == 0 {
		return append(lines, overviewLine("No subagents", detailSecondary))
	}
	columns := subagentColumns(width, d.absoluteTime)
	lines = append(lines, detailLine{text: subagentHeader(columns, d.subagentSort, d.subagentColumnFocus, d.styles).plain, nowrap: true, role: detailHeader, subagentHeader: true})
	for _, item := range d.subagents {
		session := item.s
		totalCost := session.TotalCost()
		cost := formatCost(totalCost)
		modelName := model.TerminalLine(shortModelsWithCost(session, totalCost), 96)
		lines = append(lines, detailLine{text: subagentRow(item, d.now, columns, modelName, session.TotalTurns(), cost), nowrap: true, key: sessionIdentity(session), subagent: true, subagentSession: session, subagentCost: cost, role: detailRow, agent: session.Agent})
	}
	return lines
}

func overviewAttributionLines(session *model.Session) []detailLine {
	grossCost := session.TotalCost()
	var lines []detailLine
	if session.DuplicatedUSD > 0 {
		lines = append(lines,
			overviewLine(infoCostLine("owned", session.OwnedCost()), detailRow),
			overviewLine(infoCostLine("gross", grossCost), detailRow),
			overviewLine(fmt.Sprintf("replayed total: %s, %s", formatReplayedCost(session.DuplicatedUSD, session.Cost.Estimated), requestCount(session.DuplicatedCount)), detailRow),
		)
		for _, owner := range session.DuplicatedOwners {
			lines = append(lines, overviewLine(fmt.Sprintf("  replayed %s, %s, from %s",
				formatReplayedCost(owner.USD, session.Cost.Estimated), requestCount(owner.Count), duplicateOwnerLabel(owner)), detailRow))
		}
	}
	if session.DuplicatedUSD > 0 {
		lines = append(lines, overviewLine("gross tokens: "+formatTokenFlow(sessionFlowUsage(session)), detailRow))
	}
	var unattributedModels []string
	unattributedUsage := make(map[string]model.Usage)
	unattributedUSD := make(map[string]float64)
	for _, request := range session.Requests {
		if request.Offset >= 0 {
			continue
		}
		name := request.Usage.Model
		if _, exists := unattributedUsage[name]; !exists {
			unattributedModels = append(unattributedModels, name)
		}
		unattributedUsage[name] = unattributedUsage[name].Add(request.Usage)
		unattributedUSD[name] += request.USD
	}
	if len(unattributedModels) > 0 {
		missing, estimatedRates := modelCostMarkers(session.Cost)
		for _, name := range unattributedModels {
			_, estimated := estimatedRates[name]
			cost := model.Cost{USD: unattributedUSD[name], Estimated: estimated || missing[name]}
			if missing[name] {
				cost.MissingPricingModels = []string{name}
			}
			lines = append(lines, overviewLine(fmt.Sprintf("unattributed: %s · %s · %s",
				displayModelName(name), formatTokenFlow(unattributedUsage[name]), formatCost(cost)), detailRow))
		}
		lines = append(lines, overviewLine("Codex's cumulative total did not reconcile with per-request usage for that span, so its turn rows carry no cost.", detailSecondary))
	}
	return lines
}

type subagentTreePosition struct {
	parent *subagentTreePosition
	last   bool
}

type flattenedSubagent struct {
	depth  int
	parent *subagentTreePosition
	last   bool
	s      *model.Session
}

func infoCostLine(label string, cost model.Cost) string {
	line := label + ": " + formatCost(cost)
	if len(cost.MissingPricingModels) == 0 {
		return line
	}
	missing := append([]string(nil), cost.MissingPricingModels...)
	sort.Strings(missing)
	for index := range missing {
		missing[index] = displayModelName(missing[index])
	}
	return line + " · missing pricing: " + strings.Join(missing, ", ")
}

func formatReplayedCost(usd float64, estimated bool) string {
	return "−" + formatCost(model.Cost{USD: usd, Estimated: estimated})
}

func requestCount(count int) string {
	if count == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", count)
}

func duplicateOwnerLabel(owner model.DuplicateOwner) string {
	title := model.TerminalLine(owner.Title, 160)
	id := model.TerminalLine(owner.SessionID, 96)
	if title == "" {
		if id != "" {
			return id
		}
		return "unknown session"
	}
	if id == "" || id == title {
		return title
	}
	return title + " (" + id + ")"
}

func overviewLine(text string, role detailRole) detailLine {
	return detailLine{text: model.TerminalText(text), role: role}
}

type ownModelCost struct {
	title     string
	estimated bool
	lines     []string
}

func ownModelCosts(session *model.Session) []ownModelCost {
	byModel := make(map[string]model.Usage)
	seen := make(map[string]bool)
	var order []string
	for _, usage := range session.Usage {
		name := usage.Model
		if !seen[name] {
			order = append(order, name)
			seen[name] = true
		}
		inputTokens := usage.InputTokens
		if usage.InputIncludesCacheRead {
			inputTokens = max(0, inputTokens-usage.CacheReadTokens)
		}
		byModel[name] = byModel[name].Add(model.Usage{
			InputTokens:           inputTokens,
			OutputTokens:          usage.OutputTokens,
			CacheCreation5mTokens: usage.CacheCreation5mTokens,
			CacheCreation1hTokens: usage.CacheCreation1hTokens,
			CacheReadTokens:       usage.CacheReadTokens,
		})
	}
	extraModels := make(map[string]bool)
	for name := range session.ModelCosts {
		if !seen[name] {
			extraModels[name] = true
		}
	}
	for name := range session.ModelCostBreakdowns {
		if !seen[name] {
			extraModels[name] = true
		}
	}
	for name := range session.DuplicatedByModel {
		if !seen[name] {
			extraModels[name] = true
		}
	}
	extra := make([]string, 0, len(extraModels))
	for name := range extraModels {
		extra = append(extra, name)
	}
	sort.Strings(extra)
	order = append(order, extra...)
	missing, estimatedRates := modelCostMarkers(session.Cost)
	models := make([]ownModelCost, 0, len(order))
	for _, name := range order {
		usage := byModel[name]
		breakdown, priced := session.ModelCostBreakdowns[name]
		priced = priced && !missing[name] && validCostBreakdown(breakdown)
		header := displayModelName(name)
		if missing[name] {
			header += "!"
		}
		pricingModel, modelEstimated := estimatedRates[name]
		if modelEstimated {
			header += " (est. · priced as " + displayModelName(pricingModel) + ")"
		}
		var lines []string
		cacheWrite := model.Usage{
			CacheCreation5mTokens: usage.CacheCreation5mTokens,
			CacheCreation1hTokens: usage.CacheCreation1hTokens,
		}.TotalTokens()
		groups := costRateGroups(breakdown)
		groupTokens := []int64{usage.CacheReadTokens, cacheWrite, usage.InputTokens, usage.OutputTokens}
		if priced {
			for index, group := range groups {
				if group.buckets.TotalTokens() != groupTokens[index] {
					priced = false
					break
				}
			}
		}
		termsWidth := 0
		if priced {
			groups, termsWidth = formatCostRateGroups(groups)
		}
		for index, group := range groups {
			tokens := groupTokens[index]
			if tokens <= 0 {
				continue
			}
			if !priced {
				lines = append(lines, fmt.Sprintf("  %-12s %s · price unavailable", group.label, humanTokens(tokens)))
				continue
			}
			groupCost := model.Cost{USD: group.buckets.Cost(), Estimated: modelEstimated}
			lines = append(lines, fmt.Sprintf("  %-12s %-*s = %s",
				group.label, termsWidth, group.terms, formatCost(groupCost)))
		}
		duplicatedUSD := session.DuplicatedByModel[name]
		modelUSD, hasModelCost := session.ModelCosts[name]
		if !hasModelCost {
			modelUSD = breakdown.Total()
		}
		if priced && math.Abs(modelUSD-breakdown.Total()) > 1e-9 {
			label := "model cost"
			for _, record := range session.Usage {
				if record.Model == name && record.CostUSD != nil {
					label = "logged cost"
					break
				}
			}
			lines = append(lines, fmt.Sprintf("  %-12s %-*s = %s", label, termsWidth, "", formatCost(model.Cost{USD: modelUSD, Estimated: modelEstimated})))
		}
		if duplicatedUSD > 0 {
			lines = append(lines, fmt.Sprintf("  %-12s %-*s = %s", "replayed", termsWidth, "", formatReplayedCost(duplicatedUSD, modelEstimated)))
		}
		if priced {
			ownedUSD := max(0, modelUSD-duplicatedUSD)
			lines = append(lines, fmt.Sprintf("  %-12s %-*s = %s", "subtotal", termsWidth, "", formatCost(model.Cost{USD: ownedUSD, Estimated: modelEstimated})))
		} else if duplicatedUSD > 0 {
			ownedUSD := max(0, modelUSD-duplicatedUSD)
			lines = append(lines, fmt.Sprintf("  %-12s = %s", "subtotal", formatCost(model.Cost{USD: ownedUSD, Estimated: modelEstimated})))
		}
		models = append(models, ownModelCost{title: header, estimated: modelEstimated, lines: lines})
	}
	return models
}

func ownSessionFlowUsage(session *model.Session) model.Usage {
	var own model.Usage
	for _, usage := range session.Usage {
		own = own.Add(normalizeFlowUsage(usage))
	}
	return own
}

func sessionFlowUsage(session *model.Session) model.Usage {
	total := ownSessionFlowUsage(session)
	for _, child := range session.Subagents {
		total = total.Add(sessionFlowUsage(child))
	}
	return total
}

func (d *detailState) sortSubagents(kind listColumnKind) {
	if columnVisible(kind, d.visibleSubagentColumns()) {
		d.subagentColumnFocus = kind
	}
	d.subagentSort = d.subagentSort.press(kind)
	d.rebuild()
}

func subagentColumns(width int, absoluteTime bool) []listColumn {
	if width <= 0 {
		return nil
	}
	timeTitle, timeWidth := "AGE", listAgeWidth
	if absoluteTime {
		timeTitle, timeWidth = "TIME", listTimeWidth
	}
	columns := []listColumn{
		{kind: columnAgent, title: "AGENT", width: listAgentWidth},
		{kind: columnTitle, title: "TITLE", width: 20},
		{kind: columnModel, title: "MODEL", width: listModelWidth},
		{kind: columnTurns, title: "TURNS", width: listTurnsWidth, right: true},
		{kind: columnCost, title: "COST", width: listCostWidth, right: true},
		{kind: columnAge, title: timeTitle, width: timeWidth, right: true, absoluteTime: absoluteTime},
	}
	if listColumnsWidth(columns) > width {
		columns = removeListColumn(columns, columnAge)
	}
	if listColumnsWidth(columns) > width {
		columns = removeListColumn(columns, columnTurns)
	}
	for _, shrink := range []struct {
		kind  listColumnKind
		width int
	}{{columnTitle, 4}, {columnModel, 9}} {
		for index := range columns {
			if columns[index].kind == shrink.kind && listColumnsWidth(columns) > width {
				columns[index].width -= min(columns[index].width-shrink.width, listColumnsWidth(columns)-width)
				break
			}
		}
	}
	if listColumnsWidth(columns) > width {
		columns = removeListColumn(columns, columnModel)
	}
	for _, kind := range []listColumnKind{columnTitle, columnAgent, columnCost} {
		for index := range columns {
			if columns[index].kind == kind && listColumnsWidth(columns) > width {
				columns[index].width -= min(columns[index].width-1, listColumnsWidth(columns)-width)
				break
			}
		}
	}
	for len(columns) > 0 && listColumnsWidth(columns) > width {
		columns = columns[:len(columns)-1]
	}
	slack := width - listColumnsWidth(columns)
	for index := range columns {
		if columns[index].kind == columnTitle {
			columns[index].width += slack
			break
		}
	}
	return columns
}

var subagentColumnOrder = []listColumnKind{
	columnAgent,
	columnTitle,
	columnModel,
	columnTurns,
	columnCost,
	columnAge,
}

func (d *detailState) visibleSubagentColumns() []listColumn {
	return subagentColumns(max(0, d.viewport.Width-2), d.absoluteTime)
}

func subagentHeader(columns []listColumn, state sortState, focus listColumnKind, styles styles) panelLine {
	plainCells := make([]string, len(columns))
	styledCells := make([]string, len(columns))
	for index, column := range columns {
		cell := renderHeaderCell(column, state, column.kind == focus, styles)
		plainCells[index] = cell.plain
		styledCells[index] = cell.styled
	}
	return panelLine{
		plain:  strings.Join(plainCells, " "),
		styled: strings.Join(styledCells, styles.header.Render(" ")),
	}
}

func subagentRow(item flattenedSubagent, now time.Time, columns []listColumn, modelName string, turns int, cost string) string {
	session := item.s
	cells := make([]string, len(columns))
	for index, column := range columns {
		value := ""
		switch column.kind {
		case columnAgent:
			value = model.TerminalLine(string(session.Agent), 32)
		case columnTitle:
			value = subagentTitleCell(item, column.width)
		case columnModel:
			value = modelName
		case columnTurns:
			value = compactCount(int64(turns), column.width)
		case columnCost:
			value = cost
		case columnAge:
			if column.absoluteTime {
				value = formatDetailTime(session.UpdatedAt, sessionSpansMultipleDates(session))
			} else if !now.IsZero() {
				value = formatAge(now, session.UpdatedAt)
			}
		}
		cells[index] = fitPlain(value, column.width, column.right)
	}
	return strings.Join(cells, " ")
}

func subagentTitleCell(item flattenedSubagent, width int) string {
	title := firstLine(item.s.Title)
	if item.depth == 0 {
		return title
	}
	connector := "├─ "
	if item.last {
		connector = "└─ "
	}
	minimumTitleWidth := min(subagentTitleMinVisibleWidth, width/2)
	if item.depth > 1 && item.depth*3+minimumTitleWidth > width {
		return "…" + connector + title
	}
	continuations := make([]bool, max(0, item.depth-1))
	ancestor := item.parent
	for index := len(continuations) - 1; index >= 0; index-- {
		continuations[index] = ancestor != nil && !ancestor.last
		if ancestor != nil {
			ancestor = ancestor.parent
		}
	}
	var guides strings.Builder
	guides.Grow(item.depth * 3)
	for _, continues := range continuations {
		if continues {
			guides.WriteString("│  ")
		} else {
			guides.WriteString("   ")
		}
	}
	guides.WriteString(connector)
	return guides.String() + title
}

func flattenSubagents(session *model.Session, state sortState) []flattenedSubagent {
	if !state.active {
		state = sortState{kind: columnAge, desc: true, active: true}
	}
	var flattened []flattenedSubagent
	var appendChildren func(*model.Session, int, *subagentTreePosition)
	appendChildren = func(parent *model.Session, depth int, parentPosition *subagentTreePosition) {
		children := append([]*model.Session(nil), parent.Subagents...)
		sortSessions(children, state)
		for index, child := range children {
			last := index == len(children)-1
			flattened = append(flattened, flattenedSubagent{
				depth: depth, parent: parentPosition, last: last, s: child,
			})
			position := &subagentTreePosition{parent: parentPosition, last: last}
			appendChildren(child, depth+1, position)
		}
	}
	appendChildren(session, 0, nil)
	return flattened
}

func (d *detailState) styleSubagentLine(line string, detail detailLine) string {
	session := detail.subagentSession
	cost := detail.subagentCost
	var cells []styleCell
	columnOffset := min(2, ansi.StringWidth(line))
	for _, column := range d.visibleSubagentColumns() {
		end := columnOffset + column.width
		if column.kind == columnTurns {
			startByte := len(ansi.Cut(line, 0, columnOffset))
			endByte := startByte + len(ansi.Cut(line, columnOffset, end))
			cells = append(cells, styleCell{start: startByte, end: endByte, style: d.styles.muted})
		}
		columnOffset = end + 1
	}
	agent := model.TerminalLine(string(session.Agent), 32)
	if start := strings.Index(line, agent); start >= 0 {
		cells = append(cells, styleCell{start: start, end: start + len(agent), style: d.agentStyle(session.Agent)})
	}
	if start := strings.LastIndex(line, cost); start >= 0 {
		style := d.styles.row
		if strings.HasPrefix(cost, "~$") {
			style = d.styles.estimated
		}
		cells = append(cells, styleCell{start: start, end: start + len(cost), style: style})
	}
	if len(cells) == 0 {
		return d.styles.row.Render(line)
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].start < cells[j].start })
	return renderStyleCells(line, d.styles.row, cells)
}
