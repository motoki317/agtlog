package cost

import (
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
	MaxInputTokens        int64    `json:"max_input_tokens"`
	ProviderSpecificEntry struct {
		Fast float64 `json:"fast"`
	} `json:"provider_specific_entry"`
}

type Table map[string]Pricing

type Calculator struct {
	table Table
}

func NewCalculator(table Table) Calculator {
	return Calculator{table: table}
}

func (c Calculator) Calculate(usage model.Usage) model.Cost {
	if usage.CostUSD != nil {
		return model.Cost{USD: *usage.CostUSD}
	}
	modelName := usage.Model
	if usage.Speed == "fast" && !strings.HasSuffix(modelName, "-fast") {
		modelName += "-fast"
	}
	_, pricing, ok := c.table.Resolve(modelName)
	if !ok && usage.Speed == "fast" {
		_, pricing, ok = c.table.Resolve(usage.Model)
	}
	if !ok {
		return model.Cost{MissingPricingModels: []string{usage.Model}}
	}
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
	usd := priceTokens(inputTokens, pricing.Input, pricing.InputAbove200K)
	usd += priceTokens(usage.OutputTokens, pricing.Output, pricing.OutputAbove200K)
	usd += priceTokens(usage.CacheCreation5mTokens, cacheWrite, pricing.CacheWriteAbove200K)
	usd += priceTokens(usage.CacheReadTokens, cacheRead, pricing.CacheReadAbove200K)
	usd += priceTokens(usage.CacheCreation1hTokens, pricing.Input*2, doubled(pricing.InputAbove200K))
	if usage.Speed == "fast" && pricing.ProviderSpecificEntry.Fast != 0 {
		usd *= pricing.ProviderSpecificEntry.Fast
	}
	return model.Cost{USD: usd}
}

func priceTokens(tokens int64, base float64, above *float64) float64 {
	if above == nil || tokens <= 200_000 {
		return float64(tokens) * base
	}
	return 200_000*base + float64(tokens-200_000)**above
}

func doubled(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	value := *rate * 2
	return &value
}
