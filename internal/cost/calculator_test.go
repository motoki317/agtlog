package cost

import (
	"math"
	"reflect"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
)

func TestCalculateUsesBaseInputAndOutputRates(t *testing.T) {
	calculator := NewCalculator(Table{
		"model-a": {Input: 0.002, Output: 0.004},
	})

	got := calculator.Calculate(model.Usage{Model: "model-a", InputTokens: 10, OutputTokens: 5})
	want := 0.04
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculateUsesExplicitCacheRates(t *testing.T) {
	cacheWrite := 4.0
	cacheRead := 0.5
	calculator := NewCalculator(Table{
		"model-a": {Input: 1, CacheWrite: &cacheWrite, CacheRead: &cacheRead},
	})

	usage := model.Usage{Model: "model-a", InputTokens: 10, CacheCreation5mTokens: 2, CacheReadTokens: 3}
	got := calculator.Calculate(usage)
	want := 19.5
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculatePricesOneHourCacheAtTwiceInputRate(t *testing.T) {
	cacheWrite := 9.0
	calculator := NewCalculator(Table{
		"model-a": {Input: 2, CacheWrite: &cacheWrite},
	})

	got := calculator.Calculate(model.Usage{Model: "model-a", CacheCreation1hTokens: 3})
	want := 12.0
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculateDefaultsMissingCacheRatesFromInput(t *testing.T) {
	calculator := NewCalculator(Table{
		"model-a": {Input: 2},
	})

	usage := model.Usage{Model: "model-a", CacheCreation5mTokens: 4, CacheReadTokens: 5}
	got := calculator.Calculate(usage)
	want := 11.0
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculateAppliesMarginalRatesAbove200K(t *testing.T) {
	base := 1.0
	above := 2.0
	tests := []struct {
		name    string
		pricing Pricing
		usage   model.Usage
	}{
		{name: "input", pricing: Pricing{Input: base, InputAbove200K: &above}, usage: model.Usage{InputTokens: 250_000}},
		{name: "output", pricing: Pricing{Output: base, OutputAbove200K: &above}, usage: model.Usage{OutputTokens: 250_000}},
		{name: "cache write", pricing: Pricing{CacheWrite: &base, CacheWriteAbove200K: &above}, usage: model.Usage{CacheCreation5mTokens: 250_000}},
		{name: "cache read", pricing: Pricing{CacheRead: &base, CacheReadAbove200K: &above}, usage: model.Usage{CacheReadTokens: 250_000}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.usage.Model = "model-a"
			got := NewCalculator(Table{"model-a": test.pricing}).Calculate(test.usage)
			want := 300_000.0
			if math.Abs(got.USD-want) > 1e-12 {
				t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
			}
		})
	}
}

func TestCalculateUsesFastModelAndMultiplier(t *testing.T) {
	pricing := Pricing{Input: 1}
	pricing.ProviderSpecificEntry.Fast = 3
	calculator := NewCalculator(Table{"model-a-fast": pricing})

	got := calculator.Calculate(model.Usage{Model: "model-a", InputTokens: 2, Speed: "fast"})
	want := 6.0
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculateFallsBackToBaseFastPricing(t *testing.T) {
	pricing := Pricing{Input: 1}
	pricing.ProviderSpecificEntry.Fast = 3
	calculator := NewCalculator(Table{"model-a": pricing})

	got := calculator.Calculate(model.Usage{Model: "model-a", InputTokens: 2, Speed: "fast"})
	want := 6.0
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculateFlagsMissingPricing(t *testing.T) {
	got := NewCalculator(nil).Calculate(model.Usage{Model: "unknown-model", InputTokens: 10})

	want := []string{"unknown-model"}
	if got.USD != 0 || !reflect.DeepEqual(got.MissingPricingModels, want) {
		t.Fatalf("Calculate() = %#v, want zero USD and missing model %v", got, want)
	}
}

func TestCalculatePrefersRecordedCost(t *testing.T) {
	recorded := 1.23
	got := NewCalculator(nil).Calculate(model.Usage{Model: "unknown-model", InputTokens: 10, CostUSD: &recorded})

	if got.USD != recorded || len(got.MissingPricingModels) != 0 {
		t.Fatalf("Calculate() = %#v, want recorded USD %v", got, recorded)
	}
}

func TestCalculateSubtractsCachedTokensFromInclusiveInput(t *testing.T) {
	cacheRead := 0.1
	calculator := NewCalculator(Table{"model-a": {Input: 1, CacheRead: &cacheRead}})
	usage := model.Usage{
		Model:                  "model-a",
		InputTokens:            10,
		CacheReadTokens:        4,
		InputIncludesCacheRead: true,
	}

	got := calculator.Calculate(usage)
	want := 6.4
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("Calculate().USD = %v, want %v", got.USD, want)
	}
}

func TestCalculateCodexMarksMappedCostEstimated(t *testing.T) {
	calculator := NewCalculator(Table{"gpt-5.6": {Input: 2}})

	got := calculator.CalculateCodex(model.Usage{Model: "gpt-5.6-sol", InputTokens: 3}, "gpt-5")
	if got.USD != 6 || !got.Estimated || len(got.MissingPricingModels) != 0 {
		t.Fatalf("CalculateCodex() = %#v, want USD 6 estimated", got)
	}
}
