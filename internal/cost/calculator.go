package cost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type Pricing struct {
	Input                 float64  `json:"input_cost_per_token"`
	Output                float64  `json:"output_cost_per_token"`
	CacheWrite            *float64 `json:"cache_creation_input_token_cost"`
	CacheRead             *float64 `json:"cache_read_input_token_cost"`
	InputAbove200K        *float64 `json:"input_cost_per_token_above_200k_tokens"`
	OutputAbove200K       *float64 `json:"output_cost_per_token_above_200k_tokens"`
	CacheWriteAbove200K   *float64 `json:"cache_creation_input_token_cost_above_200k_tokens"`
	CacheReadAbove200K    *float64 `json:"cache_read_input_token_cost_above_200k_tokens"`
	InputAbove272K        *float64 `json:"input_cost_per_token_above_272k_tokens"`
	OutputAbove272K       *float64 `json:"output_cost_per_token_above_272k_tokens"`
	CacheWriteAbove272K   *float64 `json:"cache_creation_input_token_cost_above_272k_tokens"`
	CacheReadAbove272K    *float64 `json:"cache_read_input_token_cost_above_272k_tokens"`
	ProviderSpecificEntry struct {
		Fast float64 `json:"fast"`
	} `json:"provider_specific_entry"`
}

type Table map[string]Pricing

type Calculator struct {
	table       Table
	fingerprint string
}

func NewCalculator(table Table) Calculator {
	data, _ := json.Marshal(table)
	digest := sha256.Sum256(data)
	return Calculator{table: table, fingerprint: hex.EncodeToString(digest[:])}
}

func (c Calculator) Fingerprint() string {
	return c.fingerprint
}

func (c Calculator) CalculateCodex(usage model.Usage, defaultModel string) model.Cost {
	pricingModel, _, ok := c.table.ResolveCodex(usage.Model, defaultModel)
	if !ok {
		return model.Cost{Estimated: true, MissingPricingModels: []string{usage.Model}}
	}
	mapped := usage
	mapped.Model = pricingModel
	calculated := c.Calculate(mapped)
	calculated.Estimated = true
	return calculated
}

func (c Calculator) BreakdownCodex(usage model.Usage, defaultModel string) model.CostBreakdown {
	pricingModel, _, ok := c.table.ResolveCodex(usage.Model, defaultModel)
	if !ok {
		return model.CostBreakdown{}
	}
	mapped := usage
	mapped.Model = pricingModel
	return c.Breakdown(mapped)
}

func (c Calculator) HasCodexPricing(usage model.Usage, defaultModel string) bool {
	_, _, ok := c.table.ResolveCodex(usage.Model, defaultModel)
	return ok
}

func (c Calculator) Calculate(usage model.Usage) model.Cost {
	if usage.CostUSD != nil {
		return model.Cost{USD: *usage.CostUSD}
	}
	pricing, ok := c.resolvePricing(usage)
	if !ok {
		return model.Cost{MissingPricingModels: []string{usage.Model}}
	}
	return model.Cost{USD: rateCostsFor(usage, pricing).total()}
}

// Breakdown reports the actual token/rate buckets applied and ignores CostUSD.
// No measured Claude usage row supplies a logged cost; if one does, this rate
// estimate may differ from Calculate.
func (c Calculator) Breakdown(usage model.Usage) model.CostBreakdown {
	pricing, ok := c.resolvePricing(usage)
	if !ok {
		return model.CostBreakdown{}
	}
	return bucketBreakdownFor(usage, pricing)
}

func (c Calculator) HasPricing(usage model.Usage) bool {
	_, ok := c.resolvePricing(usage)
	return ok
}

func (c Calculator) resolvePricing(usage model.Usage) (Pricing, bool) {
	modelName := usage.Model
	if usage.Speed == "fast" && !strings.HasSuffix(modelName, "-fast") {
		modelName += "-fast"
	}
	_, pricing, ok := c.table.Resolve(modelName)
	if !ok && usage.Speed == "fast" {
		_, pricing, ok = c.table.Resolve(usage.Model)
	}
	return pricing, ok
}

type rateCosts struct {
	input        float64
	output       float64
	cacheWrite5m float64
	cacheRead    float64
	cacheWrite1h float64
	multiplier   float64
}

func rateCostsFor(usage model.Usage, pricing Pricing) rateCosts {
	cacheWrite := pricing.Input * 1.25
	if pricing.CacheWrite != nil {
		cacheWrite = *pricing.CacheWrite
	}
	cacheRead := pricing.Input * 0.1
	if pricing.CacheRead != nil {
		cacheRead = *pricing.CacheRead
	}
	inputTokens := usage.InputTokens
	if usage.InputIncludesCacheRead {
		// OpenAI reports cached input as a subset of input; subtract it before
		// applying the ordinary input rate to avoid billing those tokens twice.
		inputTokens = max(0, inputTokens-usage.CacheReadTokens)
	}
	multiplier := 1.0
	if usage.Speed == "fast" && pricing.ProviderSpecificEntry.Fast != 0 {
		multiplier = pricing.ProviderSpecificEntry.Fast
	}
	return rateCosts{
		input:        priceTokens(inputTokens, pricing.Input, pricing.InputAbove200K, pricing.InputAbove272K),
		output:       priceTokens(usage.OutputTokens, pricing.Output, pricing.OutputAbove200K, pricing.OutputAbove272K),
		cacheWrite5m: priceTokens(usage.CacheCreation5mTokens, cacheWrite, pricing.CacheWriteAbove200K, pricing.CacheWriteAbove272K),
		cacheRead:    priceTokens(usage.CacheReadTokens, cacheRead, pricing.CacheReadAbove200K, pricing.CacheReadAbove272K),
		cacheWrite1h: priceTokens(usage.CacheCreation1hTokens, pricing.Input*2, doubled(pricing.InputAbove200K), doubled(pricing.InputAbove272K)),
		multiplier:   multiplier,
	}
}

func (r rateCosts) total() float64 {
	usd := r.input
	usd += r.output
	usd += r.cacheWrite5m
	usd += r.cacheRead
	usd += r.cacheWrite1h
	return usd * r.multiplier
}

func bucketBreakdownFor(usage model.Usage, pricing Pricing) model.CostBreakdown {
	cacheWrite := pricing.Input * 1.25
	if pricing.CacheWrite != nil {
		cacheWrite = *pricing.CacheWrite
	}
	cacheRead := pricing.Input * 0.1
	if pricing.CacheRead != nil {
		cacheRead = *pricing.CacheRead
	}
	inputTokens := usage.InputTokens
	if usage.InputIncludesCacheRead {
		inputTokens = max(0, inputTokens-usage.CacheReadTokens)
	}
	multiplier := 1.0
	if usage.Speed == "fast" && pricing.ProviderSpecificEntry.Fast != 0 {
		multiplier = pricing.ProviderSpecificEntry.Fast
	}
	cacheWrite5mBase, cacheWrite5mAbove := priceTokenBucketTiers(
		usage.CacheCreation5mTokens, cacheWrite, pricing.CacheWriteAbove200K, pricing.CacheWriteAbove272K, multiplier,
	)
	cacheWrite1hBase, cacheWrite1hAbove := priceTokenBucketTiers(
		usage.CacheCreation1hTokens, pricing.Input*2, doubled(pricing.InputAbove200K), doubled(pricing.InputAbove272K), multiplier,
	)
	cacheWriteBuckets := cacheWrite5mBase.Add(cacheWrite1hBase)
	cacheWriteBuckets = cacheWriteBuckets.Add(cacheWrite5mAbove)
	cacheWriteBuckets = cacheWriteBuckets.Add(cacheWrite1hAbove)
	return model.CostBreakdown{
		Input:      priceTokenBuckets(inputTokens, pricing.Input, pricing.InputAbove200K, pricing.InputAbove272K, multiplier),
		Output:     priceTokenBuckets(usage.OutputTokens, pricing.Output, pricing.OutputAbove200K, pricing.OutputAbove272K, multiplier),
		CacheWrite: cacheWriteBuckets,
		CacheRead:  priceTokenBuckets(usage.CacheReadTokens, cacheRead, pricing.CacheReadAbove200K, pricing.CacheReadAbove272K, multiplier),
	}
}

func priceTokenBuckets(tokens int64, base float64, above200K, above272K *float64, multiplier float64) model.CostBuckets {
	baseBuckets, aboveBuckets := priceTokenBucketTiers(tokens, base, above200K, above272K, multiplier)
	return baseBuckets.Add(aboveBuckets)
}

func priceTokenBucketTiers(tokens int64, base float64, above200K, above272K *float64, multiplier float64) (model.CostBuckets, model.CostBuckets) {
	if tokens <= 0 {
		return nil, nil
	}
	threshold := int64(200_000)
	above := above200K
	if above == nil && above272K != nil {
		threshold = 272_000
		above = above272K
	}
	if above == nil || tokens <= threshold {
		return model.CostBuckets{{RatePerToken: base * multiplier, Tokens: tokens}}, nil
	}
	return model.CostBuckets{{RatePerToken: base * multiplier, Tokens: threshold}},
		model.CostBuckets{{RatePerToken: *above * multiplier, Tokens: tokens - threshold, AboveThreshold: true}}
}

func priceTokens(tokens int64, base float64, above200K, above272K *float64) float64 {
	threshold := int64(200_000)
	above := above200K
	if above == nil && above272K != nil {
		threshold = 272_000
		above = above272K
	}
	if above == nil || tokens <= threshold {
		return float64(tokens) * base
	}
	return float64(threshold)*base + float64(tokens-threshold)**above
}

func doubled(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	value := *rate * 2
	return &value
}
