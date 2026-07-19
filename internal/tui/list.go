package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

func listColumns(width int) []table.Column {
	columns := []table.Column{
		{Title: "AGENT", Width: 7},
		{Title: "PROJECT", Width: 10},
		{Title: "TITLE", Width: 20},
		{Title: "MODEL", Width: 12},
		{Title: "AGE", Width: 4},
		{Title: "MSGS", Width: 5},
		{Title: "TOKENS", Width: 8},
		{Title: "$", Width: 6},
	}
	total := 0
	for _, column := range columns {
		total += column.Width + 1
	}
	for total > width && len(columns) > 1 {
		total -= columns[len(columns)-1].Width + 1
		columns = columns[:len(columns)-1]
	}
	if len(columns) == 1 && total > width {
		columns[0].Width = max(0, width-1)
	}
	return columns
}

func sessionRow(session *model.Session, now time.Time, styles styles) table.Row {
	usage := session.TotalUsage()
	tokens := humanTokens(usage.TotalTokens())
	if count := subagentCount(session); count > 0 {
		tokens += fmt.Sprintf(" ⑃%d", count)
	}
	tokens = fmt.Sprintf("%8s", tokens)
	agent := styles.agentLabel(session.Agent)
	if session.HasError {
		agent += styles.warning.Render("⚠")
	}
	cost := fmt.Sprintf("%6s", formatCost(session.TotalCost()))
	if session.TotalCost().Estimated {
		cost = styles.estimated.Render(cost)
	}
	return table.Row{
		agent,
		terminalText(session.Project, 96),
		terminalText(session.Title, 160),
		terminalText(shortModels(session), 96),
		styles.muted.Render(formatAge(now, session.UpdatedAt)),
		styles.muted.Render(fmt.Sprintf("%5d", session.Messages)),
		tokens,
		cost,
	}
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
	for _, missing := range session.TotalCost().MissingPricingModels {
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
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func humanTokens(tokens int64) string {
	switch {
	case tokens >= 10_000_000:
		return fmt.Sprintf("%.0fM", float64(tokens)/1_000_000)
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.0fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func formatCost(cost model.Cost) string {
	prefix := "$"
	if cost.Estimated {
		prefix = "~$"
	}
	if len(cost.MissingPricingModels) > 0 {
		if cost.Estimated {
			if cost.USD < 10 {
				return fmt.Sprintf("~$%.1f!", cost.USD)
			}
			return fmt.Sprintf("~$%.0f!", cost.USD)
		}
		if cost.USD < 10 {
			return fmt.Sprintf("$%.2f!", cost.USD)
		}
		return fmt.Sprintf("$%.0f!", cost.USD)
	}
	if cost.USD < 10 {
		return fmt.Sprintf("%s%.2f", prefix, cost.USD)
	}
	return fmt.Sprintf("%s%.0f", prefix, cost.USD)
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
	footer := m.listFooter()
	if m.filtering {
		footer = "filter: " + m.filter.View() + "  [enter] apply  [esc] clear"
	}
	return m.table.View() + "\n" + ansi.Truncate(footer, m.width, "…")
}

func (m Model) listFooter() string {
	projects := make(map[string]bool)
	var total model.Cost
	missing := make(map[string]bool)
	for _, session := range m.visible {
		projects[session.Project] = true
		cost := session.TotalCost()
		total.USD += cost.USD
		total.Estimated = total.Estimated || cost.Estimated
		for _, name := range cost.MissingPricingModels {
			if !missing[name] {
				total.MissingPricingModels = append(total.MissingPricingModels, name)
				missing[name] = true
			}
		}
	}
	totalLabel := formatCost(total)
	if len(total.MissingPricingModels) > 0 {
		totalLabel += " partial"
	}
	state := fmt.Sprintf("— %d sessions · %d projects · %s", len(m.visible), len(projects), m.styles.emphasis.Render(totalLabel))
	if query := terminalText(m.filter.Value(), 24); query != "" {
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
	hints := []string{"[/] filter", "[s] sort", "[a] agent", "[↵] open", "[?] help", "[q] quit"}
	footer := state
	for _, hint := range hints {
		candidate := footer + " " + hint
		if ansi.StringWidth(candidate) > m.width {
			break
		}
		footer = candidate
	}
	return footer
}
