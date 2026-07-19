package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

const (
	listAgeWidth      = 4
	listMessagesWidth = 5
	listTokensWidth   = 8
	listCostWidth     = 6
)

func listColumns(width int) []table.Column {
	columns := []table.Column{
		{Title: "AGENT", Width: 7},
		{Title: "PROJECT", Width: 10},
		{Title: "TITLE", Width: 20},
		{Title: "MODEL", Width: 12},
		{Title: "AGE", Width: listAgeWidth},
		{Title: "MSGS", Width: listMessagesWidth},
		{Title: "TOKENS", Width: listTokensWidth},
		{Title: "$", Width: listCostWidth},
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
	tokenSuffix := ""
	tokenWidth := 4
	if count := subagentCount(session); count > 0 {
		tokenSuffix = " ⑃" + compactCount(int64(count), 3)
		tokenWidth = min(tokenWidth, listTokensWidth-ansi.StringWidth(tokenSuffix))
	}
	tokens := fmt.Sprintf("%*s", listTokensWidth, compactCount(usage.TotalTokens(), tokenWidth)+tokenSuffix)
	agent := styles.agentLabel(session.Agent)
	if session.HasError {
		agent += styles.warning.Render("⚠")
	}
	cost := fmt.Sprintf("%*s", listCostWidth, formatCost(session.TotalCost()))
	if session.TotalCost().Estimated {
		cost = styles.estimated.Render(cost)
	}
	return table.Row{
		agent,
		terminalText(session.Project, 96),
		terminalText(session.Title, 160),
		terminalText(shortModels(session), 96),
		styles.muted.Render(formatAge(now, session.UpdatedAt)),
		styles.muted.Render(fmt.Sprintf("%*s", listMessagesWidth, compactCount(int64(session.Messages), listMessagesWidth))),
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
	prefix := "$"
	if cost.Estimated {
		prefix = "~$"
	}
	suffix := ""
	if len(cost.MissingPricingModels) > 0 {
		suffix = "!"
	}
	amountWidth := listCostWidth - len(prefix) - len(suffix)
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
	footer := m.listFooter()
	if m.filtering {
		footer = "filter: " + m.filter.View() + "  [enter] apply  [esc] clear"
	}
	body := m.table.View()
	if len(m.sessions) == 0 {
		body = "No sessions found.\n" + ansi.Truncate("Check ~/.claude, ~/.codex, or configured agent roots; press ? for keys.", m.width, "…")
	}
	return body + "\n" + ansi.Truncate(footer, m.width, "…")
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
