package tui

import (
	"fmt"
	"strings"

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
	columns := subagentColumns(width)
	lines = append(lines, detailLine{text: subagentHeader(columns, d.subagentSort, d.subagentColumnFocus, d.styles).plain, nowrap: true, role: detailHeader, subagentHeader: true})
	for _, item := range d.subagents {
		session := item.s
		totalCost := session.TotalCost()
		cost := formatCost(totalCost)
		modelName := terminalText(shortModelsWithCost(session, totalCost), 96)
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
