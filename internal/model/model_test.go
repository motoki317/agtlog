package model

import (
	"reflect"
	"testing"
)

func TestSessionTotalUsageIncludesNestedSubagents(t *testing.T) {
	session := Session{
		Usage: []Usage{{InputTokens: 10, OutputTokens: 2}},
		Subagents: []*Session{{
			Usage: []Usage{{InputTokens: 20, CacheReadTokens: 4}},
			Subagents: []*Session{{
				Usage: []Usage{{OutputTokens: 3, CacheCreation1hTokens: 5}},
			}},
		}},
	}

	want := Usage{
		InputTokens:           30,
		OutputTokens:          5,
		CacheCreation1hTokens: 5,
		CacheReadTokens:       4,
	}
	if got := session.TotalUsage(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TotalUsage() = %#v, want %#v", got, want)
	}
}

func TestSessionTotalCostIncludesNestedSubagents(t *testing.T) {
	session := Session{
		Cost: Cost{USD: 1, MissingPricingModels: []string{"unknown-a"}},
		Subagents: []*Session{{
			Cost: Cost{USD: 2, Estimated: true},
			Subagents: []*Session{{
				Cost: Cost{USD: 3, MissingPricingModels: []string{"unknown-a", "unknown-b"}},
			}},
		}},
	}

	want := Cost{USD: 6, Estimated: true, MissingPricingModels: []string{"unknown-a", "unknown-b"}}
	if got := session.TotalCost(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TotalCost() = %#v, want %#v", got, want)
	}
}
