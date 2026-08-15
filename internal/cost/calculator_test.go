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
	if got.Estimated || len(got.EstimatedRates) != 0 {
		t.Fatalf("Calculate() = %#v, want no Claude-side estimate metadata", got)
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

func TestApplySessionRebuildsPricingPostOrder(t *testing.T) {
	calculator := NewCalculator(Table{
		"model-root":  {Input: 2},
		"model-child": {Input: 3},
	})
	childUsage := model.Usage{Model: "model-child", InputTokens: 4}
	child := &model.Session{
		Requests:   []model.RequestUsage{{Usage: childUsage, USD: 999}},
		ModelCosts: map[string]float64{"stale": 999},
		ModelCostBreakdowns: map[string]model.CostBreakdown{
			"stale": {Input: model.CostBuckets{{RatePerToken: 999, Tokens: 1}}},
		},
		Cost: model.Cost{USD: 999, Estimated: true, MissingPricingModels: []string{"stale"}},
	}
	group := &model.Session{
		Group:      true,
		Subagents:  []*model.Session{child},
		ModelCosts: map[string]float64{"stale": 999},
		Cost:       model.Cost{USD: 999},
	}
	rootUsage := model.Usage{Model: "model-root", InputTokens: 5}
	root := &model.Session{
		Requests:   []model.RequestUsage{{Usage: rootUsage, USD: 999}},
		Subagents:  []*model.Session{group},
		ModelCosts: map[string]float64{"stale": 999},
		Cost:       model.Cost{USD: 999},
	}

	calculator.ApplySession(root)

	if root.Cost.USD != 10 || root.Requests[0].USD != 10 ||
		!reflect.DeepEqual(root.ModelCosts, map[string]float64{"model-root": 10}) {
		t.Fatalf("root pricing = cost %#v, requests %#v, models %#v", root.Cost, root.Requests, root.ModelCosts)
	}
	if child.Cost.USD != 12 || child.Requests[0].USD != 12 ||
		!reflect.DeepEqual(child.ModelCosts, map[string]float64{"model-child": 12}) {
		t.Fatalf("child pricing = cost %#v, requests %#v, models %#v", child.Cost, child.Requests, child.ModelCosts)
	}
	if group.Cost.USD != 0 || !reflect.DeepEqual(group.ModelCosts, child.ModelCosts) {
		t.Fatalf("group pricing = cost %#v, models %#v, want child rollup %#v", group.Cost, group.ModelCosts, child.ModelCosts)
	}
	if got := root.ModelCostBreakdowns["model-root"].Input; !reflect.DeepEqual(got, model.CostBuckets{{RatePerToken: 2, Tokens: 5}}) {
		t.Fatalf("root breakdown = %#v, want rebuilt input bucket", got)
	}
	if got := child.ModelCostBreakdowns["model-child"].Input; !reflect.DeepEqual(got, model.CostBuckets{{RatePerToken: 3, Tokens: 4}}) {
		t.Fatalf("child breakdown = %#v, want rebuilt input bucket", got)
	}

	want := pricingSnapshot(root)
	calculator.ApplySession(root)
	if got := pricingSnapshot(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("second ApplySession() = %#v, want idempotent %#v", got, want)
	}
}

func TestApplySessionCodexPricesStoredRequestsIndividually(t *testing.T) {
	above := 2.0
	calculator := NewCalculator(Table{
		"gpt-5.6": {Input: 1, InputAbove272K: &above},
	})
	request := model.RequestUsage{Usage: model.Usage{
		Model: "gpt-5.6", InputTokens: 150_000, InputIncludesCacheRead: true,
	}}
	session := &model.Session{
		Usage:    []model.Usage{{Model: "gpt-5.6", InputTokens: 300_000, InputIncludesCacheRead: true}},
		Requests: []model.RequestUsage{request, request},
	}

	calculator.ApplySessionCodex(session, "gpt-5")

	if session.Cost.USD != 300_000 || session.Requests[0].USD != 150_000 || session.Requests[1].USD != 150_000 {
		t.Fatalf("ApplySessionCodex() costs = %#v, requests %#v, want two base-tier requests", session.Cost, session.Requests)
	}
	want := model.CostBuckets{{RatePerToken: 1, Tokens: 300_000}}
	if got := session.ModelCostBreakdowns["gpt-5.6"].Input; !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplySessionCodex() input buckets = %#v, want %#v", got, want)
	}
}

func TestApplySessionCodexRebuildsMetadataInRequestOrder(t *testing.T) {
	calculator := NewCalculator(Table{"fallback": {Input: 1}})
	requests := []model.RequestUsage{
		{Usage: model.Usage{Model: "future-z", InputTokens: 1}},
		{Usage: model.Usage{Model: "future-a", InputTokens: 2}},
		{Usage: model.Usage{Model: "future-z", InputTokens: 3}},
	}
	session := &model.Session{
		Requests: requests,
		Cost: model.Cost{
			USD:                  999,
			Estimated:            true,
			EstimatedRates:       []model.EstimatedRate{{Model: "stale", PricingModel: "stale"}},
			MissingPricingModels: []string{"stale"},
		},
	}

	calculator.ApplySessionCodex(session, "fallback")

	wantRates := []model.EstimatedRate{
		{Model: "future-z", PricingModel: "fallback"},
		{Model: "future-a", PricingModel: "fallback"},
	}
	if session.Cost.USD != 6 || !session.Cost.Estimated ||
		!reflect.DeepEqual(session.Cost.EstimatedRates, wantRates) || session.Cost.MissingPricingModels != nil {
		t.Fatalf("mapped metadata = %#v, want request-ordered rates %#v", session.Cost, wantRates)
	}

	NewCalculator(nil).ApplySessionCodex(session, "missing-fallback")

	wantMissing := []string{"future-z", "future-a"}
	if session.Cost.USD != 0 || !session.Cost.Estimated || session.Cost.EstimatedRates != nil ||
		!reflect.DeepEqual(session.Cost.MissingPricingModels, wantMissing) {
		t.Fatalf("missing metadata = %#v, want request-ordered missing models %#v", session.Cost, wantMissing)
	}
}

type sessionPricingSnapshot struct {
	Cost                model.Cost
	ModelCosts          map[string]float64
	ModelCostBreakdowns map[string]model.CostBreakdown
	RequestUSD          []float64
	Subagents           []sessionPricingSnapshot
}

func pricingSnapshot(session *model.Session) sessionPricingSnapshot {
	result := sessionPricingSnapshot{
		Cost:                session.Cost,
		ModelCosts:          session.ModelCosts,
		ModelCostBreakdowns: session.ModelCostBreakdowns,
		RequestUSD:          make([]float64, len(session.Requests)),
		Subagents:           make([]sessionPricingSnapshot, len(session.Subagents)),
	}
	for index := range session.Requests {
		result.RequestUSD[index] = session.Requests[index].USD
	}
	for index, subagent := range session.Subagents {
		result.Subagents[index] = pricingSnapshot(subagent)
	}
	return result
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
	wantRates := []model.EstimatedRate{{Model: "gpt-5.6-sol", PricingModel: "gpt-5.6"}}
	if !reflect.DeepEqual(got.EstimatedRates, wantRates) {
		t.Fatalf("CalculateCodex().EstimatedRates = %#v, want %#v", got.EstimatedRates, wantRates)
	}
}

func TestCalculateCodexLeavesOwnPublishedRateExact(t *testing.T) {
	calculator := NewCalculator(Table{"gpt-5.6-sol": {Input: 2}})

	got := calculator.CalculateCodex(model.Usage{Model: "gpt-5.6-sol", InputTokens: 3}, "gpt-5")
	if got.USD != 6 || got.Estimated || len(got.MissingPricingModels) != 0 {
		t.Fatalf("CalculateCodex() = %#v, want USD 6 exact", got)
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
