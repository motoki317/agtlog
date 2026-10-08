package cost

import (
	"slices"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type Pricing struct {
	Input               float64  `json:"input_cost_per_token"`
	Output              float64  `json:"output_cost_per_token"`
	CacheWrite          *float64 `json:"cache_creation_input_token_cost"`
	CacheRead           *float64 `json:"cache_read_input_token_cost"`
	InputAbove200K      *float64 `json:"input_cost_per_token_above_200k_tokens"`
	OutputAbove200K     *float64 `json:"output_cost_per_token_above_200k_tokens"`
	CacheWriteAbove200K *float64 `json:"cache_creation_input_token_cost_above_200k_tokens"`
	CacheReadAbove200K  *float64 `json:"cache_read_input_token_cost_above_200k_tokens"`
	InputAbove272K      *float64 `json:"input_cost_per_token_above_272k_tokens"`
	OutputAbove272K     *float64 `json:"output_cost_per_token_above_272k_tokens"`
	CacheWriteAbove272K *float64 `json:"cache_creation_input_token_cost_above_272k_tokens"`
	CacheReadAbove272K  *float64 `json:"cache_read_input_token_cost_above_272k_tokens"`
	// Tiers ascend by Threshold. decodePricingTable builds them from the raw
	// keys, because LiteLLM spells each threshold into the key name.
	Tiers                 []PriceTier `json:"-"`
	ProviderSpecificEntry struct {
		Fast float64 `json:"fast"`
	} `json:"provider_specific_entry"`
}

// PriceTier is a context-length rate card. A nil rate keeps the base rate.
type PriceTier struct {
	// Threshold is exclusive: the tier applies when Usage.PromptTokens() exceeds it.
	Threshold  int64
	Input      float64
	Output     *float64
	CacheWrite *float64
	CacheRead  *float64
}

type Table map[string]Pricing

type Calculator struct {
	table Table
}

func NewCalculator(table Table) Calculator {
	return Calculator{table: table}
}

type sessionPricer struct {
	calculate  func(usage model.Usage, tiered bool) model.Cost
	breakdown  func(usage model.Usage, tiered bool) model.CostBreakdown
	hasPricing func(model.Usage) bool
}

func (c Calculator) ApplySession(session *model.Session) {
	applySession(session, sessionPricer{
		calculate:  c.calculate,
		breakdown:  c.breakdown,
		hasPricing: c.HasPricing,
	})
}

func (c Calculator) ApplySessionCodex(session *model.Session, defaultModel string) {
	applySession(session, sessionPricer{
		calculate: func(usage model.Usage, tiered bool) model.Cost {
			return c.calculateCodex(usage, defaultModel, tiered)
		},
		breakdown: func(usage model.Usage, tiered bool) model.CostBreakdown {
			return c.breakdownCodex(usage, defaultModel, tiered)
		},
		hasPricing: func(usage model.Usage) bool {
			return c.HasCodexPricing(usage, defaultModel)
		},
	})
}

func applySession(session *model.Session, pricer sessionPricer) {
	if session == nil {
		return
	}
	for _, subagent := range session.Subagents {
		applySession(subagent, pricer)
	}

	session.Cost = model.Cost{}
	session.ModelCosts = nil
	session.ModelCostBreakdowns = nil
	for index := range session.Requests {
		request := &session.Requests[index]
		// A negative offset marks an aggregate ledger entry. It sums many requests,
		// so its size says nothing about the prompt size that selects a tier.
		tiered := request.Offset >= 0
		calculated := pricer.calculate(request.Usage, tiered)
		request.USD = calculated.USD
		if session.ModelCosts == nil {
			session.ModelCosts = make(map[string]float64)
		}
		session.ModelCosts[request.Usage.Model] += calculated.USD
		if pricer.hasPricing(request.Usage) {
			if session.ModelCostBreakdowns == nil {
				session.ModelCostBreakdowns = make(map[string]model.CostBreakdown)
			}
			current := session.ModelCostBreakdowns[request.Usage.Model]
			session.ModelCostBreakdowns[request.Usage.Model] = current.Add(pricer.breakdown(request.Usage, tiered))
		}
		session.Cost.USD += calculated.USD
		session.Cost.Estimated = session.Cost.Estimated || calculated.Estimated
		for _, rate := range calculated.EstimatedRates {
			if !slices.Contains(session.Cost.EstimatedRates, rate) {
				session.Cost.EstimatedRates = append(session.Cost.EstimatedRates, rate)
			}
		}
		for _, name := range calculated.MissingPricingModels {
			if !slices.Contains(session.Cost.MissingPricingModels, name) {
				session.Cost.MissingPricingModels = append(session.Cost.MissingPricingModels, name)
			}
		}
	}
	if session.Group {
		for _, subagent := range session.Subagents {
			for name, childCost := range subagent.ModelCosts {
				if session.ModelCosts == nil {
					session.ModelCosts = make(map[string]float64)
				}
				session.ModelCosts[name] += childCost
			}
		}
	}
}

func (c Calculator) CalculateCodex(usage model.Usage, defaultModel string) model.Cost {
	return c.calculateCodex(usage, defaultModel, true)
}

func (c Calculator) calculateCodex(usage model.Usage, defaultModel string, tiered bool) model.Cost {
	pricingModel, exact, ok := c.table.ResolveCodex(usage.Model, defaultModel)
	if !ok {
		return model.Cost{Estimated: true, MissingPricingModels: []string{usage.Model}}
	}
	mapped := usage
	mapped.Model = pricingModel
	calculated := c.calculate(mapped, tiered)
	calculated.Estimated = !exact
	if !exact {
		calculated.EstimatedRates = []model.EstimatedRate{{Model: usage.Model, PricingModel: pricingModel}}
	}
	return calculated
}

func (c Calculator) BreakdownCodex(usage model.Usage, defaultModel string) model.CostBreakdown {
	return c.breakdownCodex(usage, defaultModel, true)
}

func (c Calculator) breakdownCodex(usage model.Usage, defaultModel string, tiered bool) model.CostBreakdown {
	pricingModel, _, ok := c.table.ResolveCodex(usage.Model, defaultModel)
	if !ok {
		return model.CostBreakdown{}
	}
	mapped := usage
	mapped.Model = pricingModel
	return c.breakdown(mapped, tiered)
}

func (c Calculator) HasCodexPricing(usage model.Usage, defaultModel string) bool {
	_, _, ok := c.table.ResolveCodex(usage.Model, defaultModel)
	return ok
}

func (c Calculator) Calculate(usage model.Usage) model.Cost {
	return c.calculate(usage, true)
}

func (c Calculator) calculate(usage model.Usage, tiered bool) model.Cost {
	if usage.CostUSD != nil {
		return model.Cost{USD: *usage.CostUSD}
	}
	pricing, ok := c.card(usage, tiered)
	if !ok {
		return model.Cost{MissingPricingModels: []string{usage.Model}}
	}
	return model.Cost{USD: rateCostsFor(usage, pricing).total()}
}

// Breakdown prices usage from the rate table and ignores CostUSD. If a record
// carries CostUSD, the breakdown total can differ from Calculate. No measured
// Claude log carried one.
func (c Calculator) Breakdown(usage model.Usage) model.CostBreakdown {
	return c.breakdown(usage, true)
}

func (c Calculator) breakdown(usage model.Usage, tiered bool) model.CostBreakdown {
	pricing, ok := c.card(usage, tiered)
	if !ok {
		return model.CostBreakdown{}
	}
	return bucketBreakdownFor(usage, pricing)
}

func (c Calculator) HasPricing(usage model.Usage) bool {
	_, ok := c.resolvePricing(usage)
	return ok
}

// card is the one place that decides whether tiers apply, so the cost and the
// breakdown of a record always use the same rates.
func (c Calculator) card(usage model.Usage, tiered bool) (Pricing, bool) {
	pricing, ok := c.resolvePricing(usage)
	if !tiered {
		pricing = Pricing{
			Input: pricing.Input, Output: pricing.Output,
			CacheWrite: pricing.CacheWrite, CacheRead: pricing.CacheRead,
			ProviderSpecificEntry: pricing.ProviderSpecificEntry,
		}
	}
	return pricing, ok
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

type pricingTerms struct {
	cacheWriteRate float64
	cacheReadRate  float64
	inputTokens    int64
	multiplier     float64
}

func pricingTermsFor(usage model.Usage, pricing Pricing) pricingTerms {
	terms := pricingTerms{
		cacheWriteRate: pricing.Input * 1.25,
		cacheReadRate:  pricing.Input * 0.1,
		inputTokens:    usage.InputTokens,
		multiplier:     1,
	}
	if pricing.CacheWrite != nil {
		terms.cacheWriteRate = *pricing.CacheWrite
	}
	if pricing.CacheRead != nil {
		terms.cacheReadRate = *pricing.CacheRead
	}
	if usage.InputIncludesCacheRead {
		terms.inputTokens = max(0, usage.InputTokens-usage.CacheReadTokens)
	}
	if usage.Speed == "fast" && pricing.ProviderSpecificEntry.Fast != 0 {
		terms.multiplier = pricing.ProviderSpecificEntry.Fast
	}
	return terms
}

func rateCostsFor(usage model.Usage, pricing Pricing) rateCosts {
	terms := pricingTermsFor(usage, pricing)
	return rateCosts{
		input:        priceTokens(terms.inputTokens, pricing.Input, pricing.InputAbove200K, pricing.InputAbove272K),
		output:       priceTokens(usage.OutputTokens, pricing.Output, pricing.OutputAbove200K, pricing.OutputAbove272K),
		cacheWrite5m: priceTokens(usage.CacheCreation5mTokens, terms.cacheWriteRate, pricing.CacheWriteAbove200K, pricing.CacheWriteAbove272K),
		cacheRead:    priceTokens(usage.CacheReadTokens, terms.cacheReadRate, pricing.CacheReadAbove200K, pricing.CacheReadAbove272K),
		cacheWrite1h: priceTokens(usage.CacheCreation1hTokens, pricing.Input*2, doubled(pricing.InputAbove200K), doubled(pricing.InputAbove272K)),
		multiplier:   terms.multiplier,
	}
}

func (r rateCosts) total() float64 {
	return (r.input + r.output + r.cacheWrite5m + r.cacheRead + r.cacheWrite1h) * r.multiplier
}

func bucketBreakdownFor(usage model.Usage, pricing Pricing) model.CostBreakdown {
	terms := pricingTermsFor(usage, pricing)
	cacheWrite5mBase, cacheWrite5mAbove := priceTokenBucketTiers(
		usage.CacheCreation5mTokens, terms.cacheWriteRate, pricing.CacheWriteAbove200K, pricing.CacheWriteAbove272K, terms.multiplier,
	)
	cacheWrite1hBase, cacheWrite1hAbove := priceTokenBucketTiers(
		usage.CacheCreation1hTokens, pricing.Input*2, doubled(pricing.InputAbove200K), doubled(pricing.InputAbove272K), terms.multiplier,
	)
	cacheWriteBuckets := cacheWrite5mBase.Add(cacheWrite1hBase)
	cacheWriteBuckets = cacheWriteBuckets.Add(cacheWrite5mAbove)
	cacheWriteBuckets = cacheWriteBuckets.Add(cacheWrite1hAbove)
	return model.CostBreakdown{
		Input:      priceTokenBuckets(terms.inputTokens, pricing.Input, pricing.InputAbove200K, pricing.InputAbove272K, terms.multiplier),
		Output:     priceTokenBuckets(usage.OutputTokens, pricing.Output, pricing.OutputAbove200K, pricing.OutputAbove272K, terms.multiplier),
		CacheWrite: cacheWriteBuckets,
		CacheRead:  priceTokenBuckets(usage.CacheReadTokens, terms.cacheReadRate, pricing.CacheReadAbove200K, pricing.CacheReadAbove272K, terms.multiplier),
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
	threshold, above := marginalTier(above200K, above272K)
	if above == nil || tokens <= threshold {
		return model.CostBuckets{{RatePerToken: base * multiplier, Tokens: tokens}}, nil
	}
	return model.CostBuckets{{RatePerToken: base * multiplier, Tokens: threshold}},
		model.CostBuckets{{RatePerToken: *above * multiplier, Tokens: tokens - threshold, AboveThreshold: true}}
}

func priceTokens(tokens int64, base float64, above200K, above272K *float64) float64 {
	threshold, above := marginalTier(above200K, above272K)
	if above == nil || tokens <= threshold {
		return float64(tokens) * base
	}
	return float64(threshold)*base + float64(tokens-threshold)**above
}

func marginalTier(above200K, above272K *float64) (int64, *float64) {
	if above200K == nil && above272K != nil {
		return 272_000, above272K
	}
	return 200_000, above200K
}

func doubled(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	value := *rate * 2
	return &value
}
