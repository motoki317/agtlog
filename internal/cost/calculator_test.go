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

func TestCalculateAppliesMarginalRatesAbove272K(t *testing.T) {
	above := 2.0
	pricing := Pricing{Input: 1, InputAbove272K: &above}
	got := NewCalculator(Table{"gpt-5.6": pricing}).CalculateCodex(model.Usage{
		Model:       "gpt-5.6-sol",
		InputTokens: 300_000,
	}, "gpt-5")

	want := 328_000.0
	if math.Abs(got.USD-want) > 1e-12 {
		t.Fatalf("CalculateCodex().USD = %v, want %v", got.USD, want)
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

func TestCalculatePreservesLegacyFloatingPointOrder(t *testing.T) {
	inputAbove272K := 1e-5
	outputAbove272K := 4.5e-5
	pricing := Pricing{
		Input: 5e-6, Output: 3e-5,
		InputAbove272K: &inputAbove272K, OutputAbove272K: &outputAbove272K,
	}
	fastPricing := pricing
	fastPricing.ProviderSpecificEntry.Fast = 2.3
	calculator := NewCalculator(Table{"gpt-5.6": pricing, "gpt-5.6-fast": fastPricing})
	usage := model.Usage{
		Model: "gpt-5.6", InputTokens: 15_224_662, OutputTokens: 47_117_160,
		CacheCreation5mTokens: 20_882_115, CacheCreation1hTokens: 56_166_500,
		CacheReadTokens: 95_037_078,
	}

	for _, test := range []struct {
		name     string
		speed    string
		wantBits uint64
	}{
		{name: "ordinary", wantBits: 0x40abdb70ef911cf4},
		{name: "fast", speed: "fast", wantBits: 0x40c004942359d70c},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage.Speed = test.speed
			got := calculator.Calculate(usage)
			if math.Float64bits(got.USD) != test.wantBits {
				t.Fatalf("Calculate().USD = %.17g (%#x), want legacy bits %#x", got.USD, math.Float64bits(got.USD), test.wantBits)
			}
			if total := calculator.Breakdown(usage).Total(); math.Abs(total-got.USD) > 1e-9 {
				t.Fatalf("Breakdown().Total() = %.17g, want displayed-cost equivalent to %.17g", total, got.USD)
			}
		})
	}
}

func TestCalculatorReportsRateAvailabilitySeparatelyFromRecordedCost(t *testing.T) {
	recorded := 1.23
	usage := model.Usage{Model: "unknown-model", InputTokens: 10, CostUSD: &recorded}
	calculator := NewCalculator(nil)

	if calculator.HasPricing(usage) {
		t.Fatal("HasPricing() = true for unknown recorded-cost model")
	}
	if calculator.HasCodexPricing(usage, "unknown-default") {
		t.Fatal("HasCodexPricing() = true for unknown Codex pricing model")
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

func TestBreakdownSeparatesRateDerivedTokenCosts(t *testing.T) {
	baseInput, highInput := 2.0, 3.0
	baseOutput, highOutput := 4.0, 5.0
	baseWrite, highWrite := 6.0, 7.0
	baseRead, highRead := 0.5, 0.75
	pricing := Pricing{
		Input:               baseInput,
		Output:              baseOutput,
		CacheWrite:          &baseWrite,
		CacheRead:           &baseRead,
		InputAbove200K:      &highInput,
		OutputAbove200K:     &highOutput,
		CacheWriteAbove200K: &highWrite,
		CacheReadAbove200K:  &highRead,
	}
	pricing.ProviderSpecificEntry.Fast = 2
	calculator := NewCalculator(Table{"model-a": pricing, "model-a-fast": pricing})

	tests := []struct {
		name  string
		usage model.Usage
		want  model.CostBreakdown
	}{
		{
			name: "tiered rates and one-hour cache",
			usage: model.Usage{
				Model: "model-a", InputTokens: 250_000, OutputTokens: 250_000,
				CacheCreation5mTokens: 250_000, CacheCreation1hTokens: 250_000, CacheReadTokens: 250_000,
			},
			want: model.CostBreakdown{
				Input:      model.CostBuckets{{RatePerToken: 2, Tokens: 200_000}, {RatePerToken: 3, Tokens: 50_000, AboveThreshold: true}},
				Output:     model.CostBuckets{{RatePerToken: 4, Tokens: 200_000}, {RatePerToken: 5, Tokens: 50_000, AboveThreshold: true}},
				CacheWrite: model.CostBuckets{{RatePerToken: 6, Tokens: 250_000}, {RatePerToken: 4, Tokens: 200_000}, {RatePerToken: 7, Tokens: 50_000, AboveThreshold: true}},
				CacheRead:  model.CostBuckets{{RatePerToken: 0.5, Tokens: 200_000}, {RatePerToken: 0.75, Tokens: 50_000, AboveThreshold: true}},
			},
		},
		{
			name: "fast multiplier and inclusive cached input",
			usage: model.Usage{
				Model: "model-a", Speed: "fast", InputTokens: 10, OutputTokens: 3,
				CacheReadTokens: 4, InputIncludesCacheRead: true,
			},
			want: model.CostBreakdown{
				Input: model.CostBuckets{{RatePerToken: 4, Tokens: 6}}, Output: model.CostBuckets{{RatePerToken: 8, Tokens: 3}},
				CacheRead: model.CostBuckets{{RatePerToken: 1, Tokens: 4}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := calculator.Breakdown(test.usage)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Breakdown() = %#v, want %#v", got, test.want)
			}
			calculated := calculator.Calculate(test.usage)
			total := got.Total()
			if math.Abs(total-calculated.USD) > 1e-9 {
				t.Fatalf("Breakdown total = %v, Calculate().USD = %v", total, calculated.USD)
			}
		})
	}
}
