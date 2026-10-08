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
	// The base card keeps these bits independent of context-length tiers.
	pricing := Pricing{Input: 5e-6, Output: 3e-5}
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
		{name: "ordinary", wantBits: 0x40a16aab73c92578},
		{name: "fast", speed: "fast", wantBits: 0x40b407785ec0eb16},
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
	calculator := NewCalculator(Table{
		"gpt-5.6": {Input: 1, Tiers: []PriceTier{{Threshold: 272_000, Input: 2}}},
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

func TestApplySessionCodexPricesAggregatesAtBaseCard(t *testing.T) {
	// The cache-read rate differs from its 0.1 × input default, so the base card
	// must carry it over.
	calculator := NewCalculator(Table{"gpt-5.6": {
		Input: 1, CacheRead: new(0.5),
		Tiers: []PriceTier{{Threshold: 272_000, Input: 2, CacheRead: new(0.75)}},
	}})
	for _, test := range []struct {
		name    string
		offset  int64
		wantUSD float64
		want    model.CostBreakdown
	}{
		{
			name: "request", offset: 0, wantUSD: 675_000,
			want: model.CostBreakdown{
				Input:     model.CostBuckets{{RatePerToken: 2, Tokens: 300_000, AboveThreshold: true}},
				CacheRead: model.CostBuckets{{RatePerToken: 0.75, Tokens: 100_000, AboveThreshold: true}},
			},
		},
		{
			name: "aggregate", offset: -1, wantUSD: 350_000,
			want: model.CostBreakdown{
				Input:     model.CostBuckets{{RatePerToken: 1, Tokens: 300_000}},
				CacheRead: model.CostBuckets{{RatePerToken: 0.5, Tokens: 100_000}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &model.Session{Requests: []model.RequestUsage{{
				Offset: test.offset,
				Usage: model.Usage{
					Model: "gpt-5.6", InputTokens: 400_000, CacheReadTokens: 100_000, InputIncludesCacheRead: true,
				},
			}}}

			calculator.ApplySessionCodex(session, "gpt-5")

			if session.Cost.USD != test.wantUSD || session.Requests[0].USD != test.wantUSD {
				t.Fatalf("ApplySessionCodex() cost = %#v, requests %#v, want USD %v", session.Cost, session.Requests, test.wantUSD)
			}
			if got := session.ModelCostBreakdowns["gpt-5.6"]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ApplySessionCodex() breakdown = %#v, want %#v", got, test.want)
			}
		})
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

func TestPricingAppliesOneCardToTheWholeRequest(t *testing.T) {
	fullTier := Pricing{
		Input: 1, Output: 4, CacheWrite: new(1.5), CacheRead: new(0.125),
		Tiers: []PriceTier{{Threshold: 100_000, Input: 2, Output: new(8.0), CacheWrite: new(2.5), CacheRead: new(0.25)}},
	}
	fullTier.ProviderSpecificEntry.Fast = 2
	calculator := NewCalculator(Table{
		// No model has a -fast entry, so a fast row prices from the base entry.
		"full-tier": fullTier,
		"two-tiers": {Input: 1, Tiers: []PriceTier{{Threshold: 100_000, Input: 2}, {Threshold: 200_000, Input: 3}}},
		// input-only-tier lists no cache rate on either card, so both fall back to the base defaults.
		"input-only-tier":  {Input: 2, Output: 3, Tiers: []PriceTier{{Threshold: 100_000, Input: 4}}},
		"base-cache-rates": {Input: 2, CacheWrite: new(3.0), CacheRead: new(0.3), Tiers: []PriceTier{{Threshold: 100_000, Input: 4}}},
		"inclusive-input": {
			Input: 1, Output: 4, CacheRead: new(0.125),
			Tiers: []PriceTier{{Threshold: 272_000, Input: 2, Output: new(8.0), CacheRead: new(0.25)}},
		},
	})
	buckets := func(rate float64, tokens int64, above bool) model.CostBuckets {
		return model.CostBuckets{{RatePerToken: rate, Tokens: tokens, AboveThreshold: above}}
	}

	tests := []struct {
		name  string
		usage model.Usage
		want  model.CostBreakdown
	}{
		{
			name: "explicit cache rates, and the 1-hour write at twice the input rate",
			usage: model.Usage{
				Model: "full-tier", InputTokens: 10, CacheCreation5mTokens: 2, CacheCreation1hTokens: 3, CacheReadTokens: 4,
			},
			want: model.CostBreakdown{
				Input:      buckets(1, 10, false),
				CacheWrite: model.CostBuckets{{RatePerToken: 1.5, Tokens: 2}, {RatePerToken: 2, Tokens: 3}},
				CacheRead:  buckets(0.125, 4, false),
			},
		},
		{
			name:  "missing cache rates default from the input rate",
			usage: model.Usage{Model: "input-only-tier", CacheCreation5mTokens: 4, CacheReadTokens: 5},
			want:  model.CostBreakdown{CacheWrite: buckets(2.5, 4, false), CacheRead: buckets(0.2, 5, false)},
		},
		{
			// No category alone passes 100K, but the prompt does.
			name: "split prompt crosses the tier",
			usage: model.Usage{
				Model: "full-tier", InputTokens: 2, OutputTokens: 269, CacheCreation1hTokens: 12_973, CacheReadTokens: 92_541,
			},
			want: model.CostBreakdown{
				Input: buckets(2, 2, true), Output: buckets(8, 269, true),
				CacheWrite: buckets(4, 12_973, true), CacheRead: buckets(0.25, 92_541, true),
			},
		},
		{
			name:  "prompt at the threshold stays on the base card",
			usage: model.Usage{Model: "full-tier", InputTokens: 100_000, OutputTokens: 10},
			want:  model.CostBreakdown{Input: buckets(1, 100_000, false), Output: buckets(4, 10, false)},
		},
		{
			name:  "one token over the threshold selects the tier",
			usage: model.Usage{Model: "full-tier", InputTokens: 100_001, OutputTokens: 10},
			want:  model.CostBreakdown{Input: buckets(2, 100_001, true), Output: buckets(8, 10, true)},
		},
		{
			name:  "prompt between two tiers selects the lower",
			usage: model.Usage{Model: "two-tiers", InputTokens: 150_000},
			want:  model.CostBreakdown{Input: buckets(2, 150_000, true)},
		},
		{
			name:  "prompt above two tiers selects the higher",
			usage: model.Usage{Model: "two-tiers", InputTokens: 250_000},
			want:  model.CostBreakdown{Input: buckets(3, 250_000, true)},
		},
		{
			name: "tier without a category rate keeps the base rate",
			usage: model.Usage{
				Model: "input-only-tier", InputTokens: 100_000, OutputTokens: 10,
				CacheCreation5mTokens: 20, CacheCreation1hTokens: 30, CacheReadTokens: 40,
			},
			want: model.CostBreakdown{
				Input: buckets(4, 100_000, true), Output: buckets(3, 10, true),
				CacheWrite: model.CostBuckets{{RatePerToken: 2.5, Tokens: 20, AboveThreshold: true}, {RatePerToken: 8, Tokens: 30, AboveThreshold: true}},
				CacheRead:  buckets(0.2, 40, true),
			},
		},
		{
			name:  "tier without a cache rate keeps an explicit base cache rate",
			usage: model.Usage{Model: "base-cache-rates", InputTokens: 100_000, CacheCreation5mTokens: 20, CacheReadTokens: 40},
			want: model.CostBreakdown{
				Input: buckets(4, 100_000, true), CacheWrite: buckets(3, 20, true), CacheRead: buckets(0.3, 40, true),
			},
		},
		{
			name: "inclusive cached input counts once toward the prompt",
			usage: model.Usage{
				Model: "inclusive-input", InputTokens: 272_000, CacheReadTokens: 200_000, InputIncludesCacheRead: true,
			},
			want: model.CostBreakdown{Input: buckets(1, 72_000, false), CacheRead: buckets(0.125, 200_000, false)},
		},
		{
			name: "inclusive input with cached tokens crosses the tier",
			usage: model.Usage{
				Model: "inclusive-input", InputTokens: 300_000, OutputTokens: 10, CacheReadTokens: 200_000, InputIncludesCacheRead: true,
			},
			want: model.CostBreakdown{
				Input: buckets(2, 100_000, true), Output: buckets(8, 10, true), CacheRead: buckets(0.25, 200_000, true),
			},
		},
		{
			name: "fast request without a -fast entry takes the base entry and its multiplier",
			usage: model.Usage{
				Model: "full-tier", Speed: "fast", InputTokens: 10, OutputTokens: 3, CacheReadTokens: 4, InputIncludesCacheRead: true,
			},
			want: model.CostBreakdown{Input: buckets(2, 6, false), Output: buckets(8, 3, false), CacheRead: buckets(0.25, 4, false)},
		},
		{
			name:  "fast multiplier scales the tier",
			usage: model.Usage{Model: "full-tier", Speed: "fast", InputTokens: 100_001, OutputTokens: 10},
			want:  model.CostBreakdown{Input: buckets(4, 100_001, true), Output: buckets(16, 10, true)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := calculator.Breakdown(test.usage)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Breakdown() = %#v, want %#v", got, test.want)
			}
			calculated := calculator.Calculate(test.usage)
			if math.Abs(calculated.USD-test.want.Total()) > 1e-9 {
				t.Fatalf("Calculate().USD = %v, want breakdown total %v", calculated.USD, test.want.Total())
			}

			// A slug without its own entry reaches the row's card through ResolveCodex.
			standIn := test.usage
			standIn.Model = "stand-in"
			if got := calculator.BreakdownCodex(standIn, test.usage.Model); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("BreakdownCodex() = %#v, want %#v", got, test.want)
			}
			if codex := calculator.CalculateCodex(standIn, test.usage.Model); codex.USD != calculated.USD {
				t.Fatalf("CalculateCodex().USD = %v, want Calculate().USD %v", codex.USD, calculated.USD)
			}
		})
	}
}
