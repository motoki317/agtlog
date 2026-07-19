package cost

import "testing"

func TestEmbeddedTableContainsSupportedModels(t *testing.T) {
	table, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() error = %v", err)
	}

	models := []string{"claude-opus-4-8", "claude-fable-5", "claude-sonnet-5", "gpt-5.6"}
	for _, name := range models {
		if _, ok := table[name]; !ok {
			t.Errorf("EmbeddedTable() missing %q", name)
		}
	}
}

func TestTableResolveUsesProviderQualifiedModel(t *testing.T) {
	table := Table{"anthropic.model-a": {Input: 1}}

	key, pricing, ok := table.Resolve("model-a")
	if !ok || key != "anthropic.model-a" || pricing.Input != 1 {
		t.Fatalf("Resolve() = %q, %#v, %v", key, pricing, ok)
	}
}
