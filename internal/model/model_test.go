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

func TestCostBreakdownAddAndTotal(t *testing.T) {
	left := CostBreakdown{Input: 1, Output: 2, CacheWrite: 3, CacheRead: 4}
	right := CostBreakdown{Input: 0.5, Output: 1, CacheWrite: 1.5, CacheRead: 2}

	got := left.Add(right)
	want := CostBreakdown{Input: 1.5, Output: 3, CacheWrite: 4.5, CacheRead: 6}
	if got != want {
		t.Fatalf("CostBreakdown.Add() = %#v, want %#v", got, want)
	}
	if total := got.Total(); total != 15 {
		t.Fatalf("CostBreakdown.Total() = %v, want 15", total)
	}
}

func TestCostBreakdownReconcileTotalAbsorbsAggregationRounding(t *testing.T) {
	breakdown := CostBreakdown{
		Input: 150.88662000000002, Output: 2116.1922,
		CacheWrite: 1251.1232187500002, CacheRead: 47.518538999999997,
	}
	want := math.Float64frombits(0x40abdb70ef911cf4)

	got := breakdown.ReconcileTotal(want)
	if total := got.Total(); total != want {
		t.Fatalf("ReconcileTotal().Total() = %.17g (%#x), want %.17g (%#x)", total, math.Float64bits(total), want, math.Float64bits(want))
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
