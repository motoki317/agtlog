package cost

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed data/litellm-pricing.json
var embeddedPricing []byte

func EmbeddedTable() (Table, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(embeddedPricing, &raw); err != nil {
		return nil, err
	}
	table := make(Table, len(raw))
	for name, entry := range raw {
		var pricing Pricing
		if err := json.Unmarshal(entry, &pricing); err == nil {
			table[name] = pricing
		}
	}
	return table, nil
}

func (t Table) Resolve(modelName string) (string, Pricing, bool) {
	for _, candidate := range []string{
		modelName,
		"anthropic." + modelName,
		"anthropic/" + modelName,
		"openai/" + modelName,
	} {
		if pricing, ok := t[candidate]; ok {
			return candidate, pricing, true
		}
	}
	return "", Pricing{}, false
}

func (t Table) ResolveCodex(modelName, defaultModel string) (string, Pricing, bool) {
	if modelName == "gpt-5.6-sol" {
		// The -sol slug is a Codex runtime variant; gpt-5.6 is the nearest
		// public API price and keeps the resulting subscription cost estimated.
		return t.Resolve("gpt-5.6")
	}
	if strings.HasPrefix(modelName, "gpt-5") {
		if key, pricing, ok := t.Resolve(modelName); ok {
			return key, pricing, true
		}
		for _, suffix := range []string{"-sol", "-terra", "-luna"} {
			if base, found := strings.CutSuffix(modelName, suffix); found {
				if key, pricing, ok := t.Resolve(base); ok {
					return key, pricing, true
				}
			}
		}
	}
	return t.Resolve(defaultModel)
}
