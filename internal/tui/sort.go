package tui

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type sortState struct {
	kind   listColumnKind
	desc   bool
	active bool
}

func (s sortState) press(kind listColumnKind) sortState {
	if !s.active || s.kind != kind {
		return sortState{kind: kind, desc: preferDescending(kind), active: true}
	}
	if s.desc == preferDescending(kind) {
		s.desc = !s.desc
		return s
	}
	return sortState{}
}

func preferDescending(kind listColumnKind) bool {
	switch kind {
	case columnMessages, columnSubagents, columnTokens, columnCost:
		return true
	default:
		return false
	}
}

func compareSessionColumnValue(kind listColumnKind, left, right *model.Session) int {
	result := 0
	switch kind {
	case columnAgent:
		result = cmp.Compare(left.Agent, right.Agent)
	case columnProject:
		result = strings.Compare(left.Project, right.Project)
	case columnTitle:
		result = strings.Compare(strings.ToLower(firstLine(left.Title)), strings.ToLower(firstLine(right.Title)))
	case columnModel:
		result = strings.Compare(shortModelsWithCost(left, left.OwnedCost()), shortModelsWithCost(right, right.OwnedCost()))
	case columnAge:
		result = left.UpdatedAt.Compare(right.UpdatedAt)
	case columnMessages:
		result = cmp.Compare(left.Messages, right.Messages)
	case columnSubagents:
		result = cmp.Compare(subagentCount(left), subagentCount(right))
	case columnTokens:
		result = cmp.Compare(left.OwnedUsage().TotalTokens(), right.OwnedUsage().TotalTokens())
	case columnCost:
		result = cmp.Compare(normalizedUSD(left.OwnedCost().USD), normalizedUSD(right.OwnedCost().USD))
	}
	return result
}

func normalizedUSD(usd float64) float64 {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0
	}
	return usd
}

func sortColumnLabel(kind listColumnKind) string {
	switch kind {
	case columnAgent:
		return "agent"
	case columnProject:
		return "project"
	case columnTitle:
		return "title"
	case columnModel:
		return "model"
	case columnAge:
		return "age"
	case columnMessages:
		return "msgs"
	case columnSubagents:
		return "subs"
	case columnTokens:
		return "tokens"
	case columnCost:
		return "cost"
	default:
		return ""
	}
}

func sortArrow(state sortState) string {
	if state.desc {
		return "↓"
	}
	return "↑"
}

func columnVisible(kind listColumnKind, columns []listColumn) bool {
	for _, column := range columns {
		if column.kind == kind {
			return true
		}
	}
	return false
}

func snapColumnFocus(focus listColumnKind, columns []listColumn, order []listColumnKind) listColumnKind {
	if len(columns) == 0 || columnVisible(focus, columns) {
		return focus
	}
	focusPosition := 0
	for index, kind := range order {
		if kind == focus {
			focusPosition = index
			break
		}
	}
	nearest := columns[0].kind
	nearestDistance := len(order) + 1
	for _, column := range columns {
		for index, kind := range order {
			if kind != column.kind {
				continue
			}
			distance := index - focusPosition
			if distance < 0 {
				distance = -distance
			}
			if distance < nearestDistance {
				nearest, nearestDistance = column.kind, distance
			}
			break
		}
	}
	return nearest
}

func moveColumnFocus(focus listColumnKind, columns []listColumn, order []listColumnKind, delta int) listColumnKind {
	if len(columns) == 0 {
		return focus
	}
	focus = snapColumnFocus(focus, columns, order)
	for index, column := range columns {
		if column.kind == focus {
			return columns[max(0, min(index+delta, len(columns)-1))].kind
		}
	}
	return focus
}

func sortSessions(sessions []*model.Session, state sortState) {
	if !state.active {
		return
	}
	slices.SortStableFunc(sessions, func(left, right *model.Session) int {
		if state.kind == columnAge && left.UpdatedAt.IsZero() != right.UpdatedAt.IsZero() {
			if left.UpdatedAt.IsZero() {
				return 1
			}
			return -1
		}
		result := compareSessionColumnValue(state.kind, left, right)
		if state.desc {
			result = -result
		}
		if result != 0 {
			return result
		}
		return strings.Compare(sessionIdentity(left), sessionIdentity(right))
	})
}
