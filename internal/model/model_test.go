package model

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestBoundedRawRecordKeepsValidJSONStructure(t *testing.T) {
	record := map[string]any{
		"type":      "assistant",
		"timestamp": "2026-01-02T03:04:05Z",
		"message": map[string]any{
			"model":   "claude-fable-5",
			"content": "first-" + strings.Repeat("航路", 4_000) + "-last",
		},
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := BoundedRawRecord(string(encoded))
	if len([]rune(raw)) > maxDetailRunes || !json.Valid([]byte(raw)) {
		t.Fatalf("BoundedRawRecord() length=%d valid=%t", len([]rune(raw)), json.Valid([]byte(raw)))
	}
	var bounded map[string]any
	if err := json.Unmarshal([]byte(raw), &bounded); err != nil {
		t.Fatal(err)
	}
	message, ok := bounded["message"].(map[string]any)
	if bounded["type"] != "assistant" || bounded["timestamp"] != "2026-01-02T03:04:05Z" || !ok || message["model"] != "claude-fable-5" {
		t.Fatalf("BoundedRawRecord() lost structure: %#v", bounded)
	}
	content, _ := message["content"].(string)
	if !strings.HasPrefix(content, "first-") || !strings.HasSuffix(content, "-last") || !strings.Contains(content, "…") {
		t.Fatalf("BoundedRawRecord() content = %q, want bounded ends", content)
	}
}

func TestBoundedRawRecordLimitsWideObjectsAndLongKeys(t *testing.T) {
	record := make(map[string]any)
	for index := range 100 {
		record[fmt.Sprintf("field-%03d", index)] = strings.Repeat("route", 100)
	}
	record[strings.Repeat("long-key", 100)] = "value"
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := BoundedRawRecord(string(encoded))
	if len([]rune(raw)) > maxDetailRunes || !json.Valid([]byte(raw)) {
		t.Fatalf("BoundedRawRecord() length=%d valid=%t", len([]rune(raw)), json.Valid([]byte(raw)))
	}
	var bounded map[string]any
	if err := json.Unmarshal([]byte(raw), &bounded); err != nil {
		t.Fatal(err)
	}
	if _, ok := bounded["_agtlog_omitted_fields"]; !ok || len(bounded) > 33 {
		t.Fatalf("BoundedRawRecord() wide object = %d fields, marker=%v", len(bounded), ok)
	}
}

func TestBoundedRawRecordElidesTokenReconstructedByJSONDecoding(t *testing.T) {
	token := "gAAAA" + strings.Repeat("A", 70)
	rawToken := `g\u0041AAA` + strings.Repeat("A", 70)
	record := `{"secret":"` + rawToken + `","padding":"` + strings.Repeat("route", 1_000) + `"}`
	bounded := BoundedRawRecord(record)
	if !json.Valid([]byte(bounded)) || strings.Contains(bounded, token) || !strings.Contains(bounded, "<encrypted 75 chars>") {
		t.Fatalf("BoundedRawRecord() = %q, want decoded token elided in valid JSON", bounded)
	}
}

func TestBoundedRawRecordDisambiguatesTruncatedKeys(t *testing.T) {
	longKey := strings.Repeat("a", 100) + strings.Repeat("z", 100)
	naturalKey := boundJSONText(longKey, 64-utf8.RuneCountInString("#1")) + "#1"
	record := map[string]any{
		longKey:    "long-key-value",
		naturalKey: "natural-key-value",
		"padding":  strings.Repeat("route", 1_000),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := BoundedRawRecord(string(encoded))
	var bounded map[string]any
	if err := json.Unmarshal([]byte(raw), &bounded); err != nil {
		t.Fatal(err)
	}
	values := make(map[string]bool, len(bounded))
	for _, value := range bounded {
		if value, ok := value.(string); ok {
			values[value] = true
		}
	}
	if !values["long-key-value"] || !values["natural-key-value"] {
		t.Fatalf("BoundedRawRecord() lost colliding key: %#v", bounded)
	}
}

func TestCleanTimelineTextRemovesEmbeddedHardNoise(t *testing.T) {
	value := "Keep this line\n<system-reminder>hidden metadata</system-reminder>\nWarmup\nAnd this line"
	if got := CleanTimelineText(value); got != "Keep this line\nAnd this line" {
		t.Fatalf("CleanTimelineText() = %q", got)
	}
}
