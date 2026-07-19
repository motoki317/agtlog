package model

import (
	"math"
	"reflect"
	"strings"
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

func TestUsageAggregationSaturatesInsteadOfOverflowing(t *testing.T) {
	got := (Usage{InputTokens: math.MaxInt64}).Add(Usage{InputTokens: 1})
	if got.InputTokens != math.MaxInt64 {
		t.Fatalf("Usage.Add().InputTokens = %d, want MaxInt64", got.InputTokens)
	}
	if total := (Usage{InputTokens: math.MaxInt64, OutputTokens: 1}).TotalTokens(); total != math.MaxInt64 {
		t.Fatalf("Usage.TotalTokens() = %d, want MaxInt64", total)
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

func TestCleanTitleRemovesClosingXMLTag(t *testing.T) {
	if got := CleanTitle("<command-name>/goal</command-name>"); got != "/goal" {
		t.Fatalf("CleanTitle() = %q, want command title without XML", got)
	}
}

func TestCleanTitleBoundsRepeatedClosingTags(t *testing.T) {
	value := "Lunar survey" + strings.Repeat("</wrapper>", 100_000)
	if got := CleanTitle(value); got != "Lunar survey" {
		t.Fatalf("CleanTitle() = %q, want title without repeated wrappers", got)
	}
}

func TestBoundedDetailTextKeepsBothEnds(t *testing.T) {
	value := "first\n" + strings.Repeat("x", 10_000) + "\nlast"
	got := BoundedDetailText(value)
	if !strings.HasPrefix(got, "first\n") || !strings.HasSuffix(got, "\nlast") || len([]rune(got)) != maxDetailRunes {
		t.Fatalf("BoundedDetailText() did not preserve bounded ends: length %d", len([]rune(got)))
	}
}

func TestCleanTimelineTextRemovesEmbeddedHardNoise(t *testing.T) {
	value := "Keep this line\n<system-reminder>hidden metadata</system-reminder>\nWarmup\nAnd this line"
	if got := CleanTimelineText(value); got != "Keep this line\nAnd this line" {
		t.Fatalf("CleanTimelineText() = %q", got)
	}
}
