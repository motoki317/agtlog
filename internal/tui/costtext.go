package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

// modelCostMarkers indexes the pricing caveats by logged model, so each model
// line carries its own ~ and ! markers instead of the session-wide flag.
func modelCostMarkers(cost model.Cost) (missing map[string]bool, estimatedRates map[string]string) {
	missing = make(map[string]bool, len(cost.MissingPricingModels))
	for _, name := range cost.MissingPricingModels {
		missing[name] = true
	}
	estimatedRates = make(map[string]string, len(cost.EstimatedRates))
	for _, rate := range cost.EstimatedRates {
		estimatedRates[rate.Model] = rate.PricingModel
	}
	return missing, estimatedRates
}

func validCostBreakdown(breakdown model.CostBreakdown) bool {
	for _, buckets := range []model.CostBuckets{breakdown.Input, breakdown.Output, breakdown.CacheWrite, breakdown.CacheRead} {
		for _, bucket := range buckets {
			if bucket.Tokens < 0 || bucket.RatePerToken < 0 || math.IsNaN(bucket.RatePerToken) || math.IsInf(bucket.RatePerToken, 0) {
				return false
			}
		}
	}
	return true
}

func displayModelName(name string) string {
	if name = model.TerminalLine(name, 96); name != "" {
		return name
	}
	return "unknown"
}

func formatMillionRate(rate float64) string {
	text := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", rate*1_000_000), "0"), ".")
	if text == "" {
		text = "0"
	}
	return "$" + text + "/Mtok"
}

func formatRateTerms(buckets model.CostBuckets, tokenWidth int) string {
	terms := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.Tokens <= 0 {
			continue
		}
		terms = append(terms, fmt.Sprintf("%*s × %s", tokenWidth, humanTokens(bucket.Tokens), formatMillionRate(bucket.RatePerToken)))
	}
	return strings.Join(terms, " + ")
}

type costRateGroup struct {
	label   string
	buckets model.CostBuckets
	terms   string
}

func costRateGroups(breakdown model.CostBreakdown) []costRateGroup {
	// The order matches formatTokenFlow. ownModelCosts indexes its token totals
	// by this order.
	return []costRateGroup{
		{label: "cache read", buckets: breakdown.CacheRead},
		{label: "cache write", buckets: breakdown.CacheWrite},
		{label: "input", buckets: breakdown.Input},
		{label: "output", buckets: breakdown.Output},
	}
}

func formatCostRateGroups(groups []costRateGroup) ([]costRateGroup, int) {
	tokenWidth := 0
	for _, group := range groups {
		for _, bucket := range group.buckets {
			if bucket.Tokens > 0 {
				tokenWidth = max(tokenWidth, ansi.StringWidth(humanTokens(bucket.Tokens)))
			}
		}
	}
	termsWidth := 0
	for index := range groups {
		groups[index].terms = formatRateTerms(groups[index].buckets, tokenWidth)
		termsWidth = max(termsWidth, ansi.StringWidth(groups[index].terms))
	}
	return groups, termsWidth
}

func normalizeFlowUsage(usage model.Usage) model.Usage {
	if usage.InputIncludesCacheRead {
		usage.InputTokens = max(0, usage.InputTokens-usage.CacheReadTokens)
		usage.InputIncludesCacheRead = false
	}
	return usage
}

func formatTokenFlow(usage model.Usage) string {
	usage = normalizeFlowUsage(usage)
	cacheWrite := model.Usage{
		CacheCreation5mTokens: usage.CacheCreation5mTokens,
		CacheCreation1hTokens: usage.CacheCreation1hTokens,
	}.TotalTokens()
	return fmt.Sprintf("↑%s/%s/%s ↓%s",
		humanTokens(usage.CacheReadTokens), humanTokens(cacheWrite), humanTokens(usage.InputTokens), humanTokens(usage.OutputTokens))
}
