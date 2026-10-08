package cost

import (
	"cmp"
	"slices"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
)

type Pricing struct {
	Input      float64  `json:"input_cost_per_token"`
	Output     float64  `json:"output_cost_per_token"`
	CacheWrite *float64 `json:"cache_creation_input_token_cost"`
	CacheRead  *float64 `json:"cache_read_input_token_cost"`
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
	card, ok := c.card(usage, tiered)
	if !ok {
		return model.Cost{MissingPricingModels: []string{usage.Model}}
	}
	return model.Cost{USD: card.cost(usage)}
}

// Breakdown prices usage from the rate table and ignores CostUSD. If a record
// carries CostUSD, the breakdown total can differ from Calculate. No measured
// Claude log carried one.
func (c Calculator) Breakdown(usage model.Usage) model.CostBreakdown {
	return c.breakdown(usage, true)
}

func (c Calculator) breakdown(usage model.Usage, tiered bool) model.CostBreakdown {
	card, ok := c.card(usage, tiered)
	if !ok {
		return model.CostBreakdown{}
	}
	return card.breakdown(usage)
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

type rateCard struct {
	input, output, cacheWrite5m, cacheWrite1h, cacheRead float64

	multiplier     float64
	aboveThreshold bool
}

// card is the one place that selects the rates for a usage record, so its cost
// and its breakdown read the same rates.
func (c Calculator) card(usage model.Usage, tiered bool) (rateCard, bool) {
	pricing, ok := c.resolvePricing(usage)
	if !ok {
		return rateCard{}, false
	}
	card := rateCard{
		input:        pricing.Input,
		output:       pricing.Output,
		cacheWrite5m: rateOr(pricing.CacheWrite, pricing.Input*1.25),
		cacheRead:    rateOr(pricing.CacheRead, pricing.Input*0.1),
		multiplier:   1,
	}
	// As in LiteLLM, a category that the tier leaves out keeps its base rate. The
	// 1-hour write has no rate of its own: it costs twice the selected input rate.
	if tiered {
		if tier, above := pricing.tier(usage.PromptTokens()); above {
			card.input = tier.Input
			card.output = rateOr(tier.Output, card.output)
			card.cacheWrite5m = rateOr(tier.CacheWrite, card.cacheWrite5m)
			card.cacheRead = rateOr(tier.CacheRead, card.cacheRead)
			card.aboveThreshold = true
		}
	}
	card.cacheWrite1h = card.input * 2
	if usage.Speed == "fast" && pricing.ProviderSpecificEntry.Fast != 0 {
		card.multiplier = pricing.ProviderSpecificEntry.Fast
	}
	return card, true
}

// tier returns the highest tier that the prompt exceeds. A fetched payload
// decides how many tiers an entry has, so a binary search bounds the work that
// every request pays.
func (p Pricing) tier(promptTokens int64) (PriceTier, bool) {
	// Every tier before index has a threshold below promptTokens.
	index, _ := slices.BinarySearchFunc(p.Tiers, promptTokens, func(tier PriceTier, tokens int64) int {
		return cmp.Compare(tier.Threshold, tokens)
	})
	if index == 0 {
		return PriceTier{}, false
	}
	return p.Tiers[index-1], true
}

func rateOr(rate *float64, fallback float64) float64 {
	if rate == nil {
		return fallback
	}
	return *rate
}

func (c rateCard) cost(usage model.Usage) float64 {
	// Each conversion rounds its product, which keeps arm64 from fusing it into
	// the sum. A cost then has the same bits on every platform.
	usd := float64(float64(uncachedInputTokens(usage))*c.input) +
		float64(float64(usage.OutputTokens)*c.output) +
		float64(float64(usage.CacheCreation5mTokens)*c.cacheWrite5m) +
		float64(float64(usage.CacheReadTokens)*c.cacheRead) +
		float64(float64(usage.CacheCreation1hTokens)*c.cacheWrite1h)
	return usd * c.multiplier
}

func (c rateCard) breakdown(usage model.Usage) model.CostBreakdown {
	return model.CostBreakdown{
		Input:      c.buckets(uncachedInputTokens(usage), c.input),
		Output:     c.buckets(usage.OutputTokens, c.output),
		CacheWrite: c.buckets(usage.CacheCreation5mTokens, c.cacheWrite5m).Add(c.buckets(usage.CacheCreation1hTokens, c.cacheWrite1h)),
		CacheRead:  c.buckets(usage.CacheReadTokens, c.cacheRead),
	}
}

func (c rateCard) buckets(tokens int64, rate float64) model.CostBuckets {
	if tokens <= 0 {
		return nil
	}
	return model.CostBuckets{{RatePerToken: rate * c.multiplier, Tokens: tokens, AboveThreshold: c.aboveThreshold}}
}

func uncachedInputTokens(usage model.Usage) int64 {
	if usage.InputIncludesCacheRead {
		return max(0, usage.InputTokens-usage.CacheReadTokens)
	}
	return usage.InputTokens
}
