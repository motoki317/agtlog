package tui

import (
	"math"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

func TestSortStatePressCyclesAndSwitchesColumns(t *testing.T) {
	state := sortState{}

	state = state.press(columnTitle)
	if !state.active || state.kind != columnTitle || state.desc {
		t.Fatalf("first title press = %#v, want active ascending title sort", state)
	}
	state = state.press(columnTitle)
	if !state.active || state.kind != columnTitle || !state.desc {
		t.Fatalf("second title press = %#v, want active descending title sort", state)
	}
	state = state.press(columnTitle)
	if state != (sortState{}) {
		t.Fatalf("third title press = %#v, want cleared state", state)
	}
	state = state.press(columnCost)
	state = state.press(columnAgent)
	if !state.active || state.kind != columnAgent || state.desc {
		t.Fatalf("column switch = %#v, want fresh ascending agent sort", state)
	}
}

func TestSortColumnsChooseTheirPreferredFirstDirection(t *testing.T) {
	for _, test := range []struct {
		kind listColumnKind
		desc bool
	}{
		{kind: columnAgent},
		{kind: columnProject},
		{kind: columnTitle},
		{kind: columnModel},
		{kind: columnAge},
		{kind: columnMessages, desc: true},
		{kind: columnSubagents, desc: true},
		{kind: columnTokens, desc: true},
		{kind: columnCost, desc: true},
	} {
		if got := preferDescending(test.kind); got != test.desc {
			t.Errorf("preferDescending(%v) = %t, want %t", test.kind, got, test.desc)
		}
	}
}

func TestSortSessionsUsesDisplayedColumnValues(t *testing.T) {
	earlier := time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	for _, test := range []struct {
		name        string
		kind        listColumnKind
		left, right *model.Session
	}{
		{name: "agent", kind: columnAgent, left: &model.Session{Agent: model.AgentClaude}, right: &model.Session{Agent: model.AgentCodex}},
		{name: "project", kind: columnProject, left: &model.Session{Project: "asteroid"}, right: &model.Session{Project: "nebula"}},
		{name: "title", kind: columnTitle, left: &model.Session{Title: "alpha\nignored"}, right: &model.Session{Title: "Beta"}},
		{name: "model", kind: columnModel, left: &model.Session{Models: []string{"claude-alpha"}}, right: &model.Session{Models: []string{"claude-beta"}}},
		{name: "age", kind: columnAge, left: &model.Session{UpdatedAt: earlier}, right: &model.Session{UpdatedAt: later}},
		{name: "messages", kind: columnMessages, left: &model.Session{Messages: 1}, right: &model.Session{Messages: 2}},
		{name: "subagents", kind: columnSubagents, left: &model.Session{Subagents: []*model.Session{{}}}, right: &model.Session{Subagents: []*model.Session{{Subagents: []*model.Session{{}}}}}},
		{name: "tokens", kind: columnTokens, left: &model.Session{Usage: []model.Usage{{InputTokens: 1}}}, right: &model.Session{Subagents: []*model.Session{{Usage: []model.Usage{{OutputTokens: 2}}}}}},
		{name: "cost", kind: columnCost, left: &model.Session{Cost: model.Cost{USD: 1}}, right: &model.Session{Subagents: []*model.Session{{Cost: model.Cost{USD: 2}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.left.ID = "z"
			test.right.ID = "a"
			sessions := []*model.Session{test.right, test.left}
			sortSessions(sessions, sortState{kind: test.kind, active: true})
			if sessions[0] != test.left {
				t.Fatalf("sortSessions(%v) first = %q, want left value first", test.kind, sessions[0].ID)
			}
		})
	}
}

func TestSortSessionsUsesOwnedAccountingValues(t *testing.T) {
	ownedLower := &model.Session{
		ID: "owned-lower", Usage: []model.Usage{{InputTokens: 100}}, DuplicatedUsage: model.Usage{InputTokens: 90},
		Cost: model.Cost{USD: 10}, DuplicatedUSD: 9,
	}
	ownedHigher := &model.Session{
		ID: "owned-higher", Usage: []model.Usage{{InputTokens: 20}},
		Cost: model.Cost{USD: 5},
	}

	for _, kind := range []listColumnKind{columnTokens, columnCost} {
		sessions := []*model.Session{ownedHigher, ownedLower}
		sortSessions(sessions, sortState{kind: kind, active: true})
		if sessions[0] != ownedLower {
			t.Fatalf("owned %s sort first = %q, want lower displayed value", sortColumnLabel(kind), sessions[0].ID)
		}
	}
}

func TestSortSessionsKeepsZeroTimestampsLastInBothDirections(t *testing.T) {
	earlier := &model.Session{ID: "earlier", UpdatedAt: time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC)}
	later := &model.Session{ID: "later", UpdatedAt: earlier.UpdatedAt.Add(time.Hour)}
	unknown := &model.Session{ID: "unknown"}

	for _, test := range []struct {
		name string
		desc bool
		want []string
	}{
		{name: "ascending", want: []string{"earlier", "later", "unknown"}},
		{name: "descending", desc: true, want: []string{"later", "earlier", "unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := []*model.Session{unknown, later, earlier}
			sortSessions(sessions, sortState{kind: columnAge, desc: test.desc, active: true})
			for index, want := range test.want {
				if got := sessions[index].ID; got != want {
					t.Fatalf("sorted[%d] = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func TestSortSessionsNormalizesNonFiniteAndNegativeCosts(t *testing.T) {
	for _, usd := range []float64{math.NaN(), math.Inf(-1), math.Inf(1), -1} {
		invalid := &model.Session{ID: "alpha", Cost: model.Cost{USD: usd}}
		zero := &model.Session{ID: "beta", Cost: model.Cost{USD: 0}}
		for _, desc := range []bool{false, true} {
			sessions := []*model.Session{zero, invalid}
			sortSessions(sessions, sortState{kind: columnCost, desc: desc, active: true})
			if sessions[0] != invalid {
				t.Errorf("desc=%t invalid cost %v sorted after zero; want identity tiebreak", desc, usd)
			}
		}
	}
}

func TestSortSessionsKeepsIdentityTiesDeterministicInBothDirections(t *testing.T) {
	alpha := &model.Session{ID: "alpha", Messages: 2}
	beta := &model.Session{ID: "beta", Messages: 2}
	for _, desc := range []bool{false, true} {
		sessions := []*model.Session{beta, alpha}
		sortSessions(sessions, sortState{kind: columnMessages, desc: desc, active: true})
		if sessions[0] != alpha || sessions[1] != beta {
			t.Fatalf("desc=%t equal-key order = %q, %q; want alpha, beta", desc, sessions[0].ID, sessions[1].ID)
		}
	}
}
