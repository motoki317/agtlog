package cost

import (
	_ "embed"
	"encoding/json"
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
