package model

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestEventRecordRefIsNotSerialized(t *testing.T) {
	event := Event{
		Kind:         EventUser,
		PricingModel: "fictional-pricing-model",
		RecordRef: RecordRef{
			Path:   "/fictional/session.jsonl",
			Offset: 17,
			Length: 23,
			Digest: sha256.Sum256([]byte("original record")),
		},
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fictional") || strings.Contains(string(encoded), "Offset") ||
		strings.Contains(string(encoded), "Digest") || strings.Contains(string(encoded), "PricingModel") {
		t.Fatalf("json.Marshal(Event) included lazy detail metadata: %s", encoded)
	}
}

func TestCostSerializationCarriesEstimatedRates(t *testing.T) {
	want := Cost{Estimated: true, EstimatedRates: []EstimatedRate{{
		Model: "variant-a", PricingModel: "model-a",
	}}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Cost
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Cost JSON round trip = %#v, want %#v; JSON %s", got, want, data)
	}
}

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

func TestSessionDescendantAgentCountExcludesGroups(t *testing.T) {
	session := Session{Subagents: []*Session{
		{ID: "direct"},
		{ID: "wf-river-run", Group: true, Subagents: []*Session{{ID: "nested"}, {ID: "nested-group", Group: true, Subagents: []*Session{{ID: "deep"}}}}},
	}}

	if got := session.DescendantAgentCount(); got != 3 {
		t.Fatalf("DescendantAgentCount() = %d, want three agents excluding groups", got)
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

func TestUsagePromptAndFlowTokens(t *testing.T) {
	tests := []struct {
		name       string
		usage      Usage
		wantPrompt int64
		wantFlow   int64
	}{
		{
			// Claude keeps cache reads separate: input excludes them, so the prompt
			// adds them back and the flow leaves them out.
			name:       "claude separate cache read",
			usage:      Usage{InputTokens: 3_000, OutputTokens: 4_000, CacheCreation5mTokens: 500, CacheReadTokens: 37_000},
			wantPrompt: 40_500,
			wantFlow:   7_500,
		},
		{
			// Codex folds cache reads into input, so the prompt is input as-is and the
			// flow subtracts the cached portion.
			name:       "codex inclusive cache read",
			usage:      Usage{InputTokens: 45_000, OutputTokens: 4_000, CacheReadTokens: 37_000, InputIncludesCacheRead: true},
			wantPrompt: 45_000,
			wantFlow:   12_000,
		},
		{name: "zero usage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.usage.PromptTokens(); got != test.wantPrompt {
				t.Errorf("PromptTokens() = %d, want %d", got, test.wantPrompt)
			}
			if got := test.usage.FlowTokens(); got != test.wantFlow {
				t.Errorf("FlowTokens() = %d, want %d", got, test.wantFlow)
			}
		})
	}
}

func TestCostBreakdownAddAndTotal(t *testing.T) {
	left := CostBreakdown{
		Input:     CostBuckets{{RatePerToken: 1, Tokens: 2}, {RatePerToken: 2, Tokens: 3, AboveThreshold: true}},
		CacheRead: CostBuckets{{RatePerToken: 0.5, Tokens: 4}},
	}
	right := CostBreakdown{
		Input:     CostBuckets{{RatePerToken: 1, Tokens: 5}, {RatePerToken: 3, Tokens: 7}},
		CacheRead: CostBuckets{{RatePerToken: 0.5, Tokens: 6}},
	}

	got := left.Add(right)
	want := CostBreakdown{
		Input:     CostBuckets{{RatePerToken: 1, Tokens: 7}, {RatePerToken: 3, Tokens: 7}, {RatePerToken: 2, Tokens: 3, AboveThreshold: true}},
		CacheRead: CostBuckets{{RatePerToken: 0.5, Tokens: 10}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CostBreakdown.Add() = %#v, want %#v", got, want)
	}
	if tokens := got.Input.TotalTokens(); tokens != 17 {
		t.Fatalf("Input.TotalTokens() = %d, want 17", tokens)
	}
	if total := got.Total(); total != 39 {
		t.Fatalf("CostBreakdown.Total() = %v, want 39", total)
	}
}

func TestSessionTotalCostIncludesNestedSubagents(t *testing.T) {
	session := Session{
		Cost: Cost{USD: 1, EstimatedRates: []EstimatedRate{{Model: "variant-a", PricingModel: "model-a"}}, MissingPricingModels: []string{"unknown-a"}},
		Subagents: []*Session{{
			Cost: Cost{USD: 2, Estimated: true, EstimatedRates: []EstimatedRate{{Model: "variant-b", PricingModel: "model-b"}}},
			Subagents: []*Session{{
				Cost: Cost{USD: 3, EstimatedRates: []EstimatedRate{{Model: "variant-a", PricingModel: "model-a"}}, MissingPricingModels: []string{"unknown-a", "unknown-b"}},
			}},
		}},
	}

	want := Cost{
		USD: 6, Estimated: true,
		EstimatedRates:       []EstimatedRate{{Model: "variant-a", PricingModel: "model-a"}, {Model: "variant-b", PricingModel: "model-b"}},
		MissingPricingModels: []string{"unknown-a", "unknown-b"},
	}
	if got := session.TotalCost(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TotalCost() = %#v, want %#v", got, want)
	}
}

func TestSessionOwnedCostEqualsGrossWithoutDuplicates(t *testing.T) {
	session := Session{
		Cost: Cost{USD: 1, MissingPricingModels: []string{"unknown-a"}},
		Subagents: []*Session{{
			Cost: Cost{USD: 2, Estimated: true},
		}},
	}

	want := session.TotalCost()
	if got := session.OwnedCost(); !reflect.DeepEqual(got, want) {
		t.Fatalf("OwnedCost() = %#v, want gross %#v", got, want)
	}
}

func TestSessionOwnedCostSubtractsDuplicatesAcrossSubagents(t *testing.T) {
	session := Session{
		Cost:          Cost{USD: 5, MissingPricingModels: []string{"unknown-a"}},
		DuplicatedUSD: 2,
		Subagents: []*Session{{
			Cost:          Cost{USD: 3, Estimated: true, MissingPricingModels: []string{"unknown-b"}},
			DuplicatedUSD: 1,
		}},
	}

	want := Cost{USD: 5, Estimated: true, MissingPricingModels: []string{"unknown-a", "unknown-b"}}
	if got := session.OwnedCost(); !reflect.DeepEqual(got, want) {
		t.Fatalf("OwnedCost() = %#v, want gross minus duplicates %#v", got, want)
	}
}

func TestSessionOwnedUsageEqualsGrossWithoutDuplicates(t *testing.T) {
	session := Session{
		Usage: []Usage{{InputTokens: 10, OutputTokens: 2}},
		Subagents: []*Session{{
			Usage: []Usage{{InputTokens: 20, CacheReadTokens: 4}},
		}},
	}

	want := session.TotalUsage()
	if got := session.OwnedUsage(); !reflect.DeepEqual(got, want) {
		t.Fatalf("OwnedUsage() = %#v, want gross %#v", got, want)
	}
}

func TestSessionOwnedUsageSubtractsDuplicatesAcrossSubagents(t *testing.T) {
	session := Session{
		Usage: []Usage{{
			InputTokens: 10, OutputTokens: 5,
			CacheCreation5mTokens: 6, CacheCreation1hTokens: 7, CacheReadTokens: 8,
		}},
		DuplicatedUsage: Usage{
			InputTokens: 2, OutputTokens: 1,
			CacheCreation5mTokens: 2, CacheCreation1hTokens: 3, CacheReadTokens: 4,
		},
		Subagents: []*Session{{
			Usage: []Usage{{
				InputTokens: 20, OutputTokens: 4,
				CacheCreation5mTokens: 8, CacheCreation1hTokens: 10, CacheReadTokens: 12,
			}},
			DuplicatedUsage: Usage{
				InputTokens: 5, OutputTokens: 1,
				CacheCreation5mTokens: 3, CacheCreation1hTokens: 4, CacheReadTokens: 5,
			},
		}},
	}

	want := Usage{
		InputTokens: 23, OutputTokens: 7,
		CacheCreation5mTokens: 9, CacheCreation1hTokens: 10, CacheReadTokens: 11,
	}
	if got := session.OwnedUsage(); !reflect.DeepEqual(got, want) {
		t.Fatalf("OwnedUsage() = %#v, want gross minus duplicates %#v", got, want)
	}
}

func TestSessionJSONCachesRequestsButNotOwnership(t *testing.T) {
	session := Session{
		Requests: []RequestUsage{{
			MessageID: "message-fiction",
			RequestID: "request-fiction",
			Usage:     Usage{Model: "claude-fable-5", InputTokens: 10},
			USD:       0.25,
		}},
		DuplicatedUSD:     0.25,
		DuplicatedUsage:   Usage{InputTokens: 10},
		DuplicatedCount:   1,
		DuplicatedByModel: map[string]float64{"claude-fable-5": 0.25},
		DuplicatedOwners: []DuplicateOwner{{
			SessionID: "session-origin",
			Title:     "Origin",
			USD:       0.25,
			Count:     1,
		}},
	}

	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Session
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Requests, session.Requests) {
		t.Fatalf("cached Requests = %#v, want %#v", decoded.Requests, session.Requests)
	}
	if decoded.DuplicatedUSD != 0 || decoded.DuplicatedUsage.TotalTokens() != 0 ||
		decoded.DuplicatedCount != 0 || decoded.DuplicatedByModel != nil || decoded.DuplicatedOwners != nil {
		t.Fatalf("cached ownership fields = %#v, want zero runtime attribution", decoded)
	}
}

func TestRequestUsageJSONCarriesSourceOffset(t *testing.T) {
	var request RequestUsage
	if err := json.Unmarshal([]byte(`{"Offset":42}`), &request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if got := fields["Offset"]; got != float64(42) {
		t.Fatalf("cached request offset = %#v, want 42", got)
	}
}

func TestSessionJSONCarriesSourceSize(t *testing.T) {
	var session Session
	if err := json.Unmarshal([]byte(`{"SourceSize":8192}`), &session); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if got := fields["SourceSize"]; got != float64(8192) {
		t.Fatalf("cached source size = %#v, want 8192", got)
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

func TestCleanTimelineTextKeepsParagraphsAndIndentation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "paragraph break survives",
			value: "First paragraph.\n\nSecond paragraph.",
			want:  "First paragraph.\n\nSecond paragraph.",
		},
		{
			name:  "indentation survives, trailing blanks do not",
			value: "Steps:\n\n  - nested item\n\ttabbed line   \n\n",
			want:  "Steps:\n\n  - nested item\n\ttabbed line",
		},
		{
			name:  "blank runs left by a removed block collapse to one",
			value: "Before\n\n<system-reminder>hidden</system-reminder>\n\nAfter",
			want:  "Before\n\nAfter",
		},
		{
			name:  "whitespace-only text stays hard noise",
			value: "\n   \n\t\n",
			want:  "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := CleanTimelineText(testCase.value); got != testCase.want {
				t.Fatalf("CleanTimelineText() = %q, want %q", got, testCase.want)
			}
		})
	}
}
