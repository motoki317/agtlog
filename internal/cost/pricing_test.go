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

func TestCodexResolutionMapsSolToPublicGPT56(t *testing.T) {
	table := Table{"gpt-5.6": {Input: 2}}

	key, pricing, ok := table.ResolveCodex("gpt-5.6-sol", "gpt-5")
	if !ok || key != "gpt-5.6" || pricing.Input != 2 {
		t.Fatalf("ResolveCodex() = %q, %#v, %v", key, pricing, ok)
	}
}

func TestCodexResolutionKeepsPublicGPT5Models(t *testing.T) {
	table := Table{"gpt-5.4": {Input: 2}}

	key, _, ok := table.ResolveCodex("gpt-5.4", "gpt-5")
	if !ok || key != "gpt-5.4" {
		t.Fatalf("ResolveCodex() = %q, _, %v, want public model", key, ok)
	}
}

func TestCodexResolutionStripsPrivateGPT5Variant(t *testing.T) {
	table := Table{"gpt-5.4": {Input: 2}}

	key, _, ok := table.ResolveCodex("gpt-5.4-sol", "gpt-5")
	if !ok || key != "gpt-5.4" {
		t.Fatalf("ResolveCodex() = %q, _, %v, want base public model", key, ok)
	}
}

func TestCodexResolutionUsesConfiguredDefault(t *testing.T) {
	table := Table{"gpt-5": {Input: 2}}

	key, _, ok := table.ResolveCodex("future-codex-model", "gpt-5")
	if !ok || key != "gpt-5" {
		t.Fatalf("ResolveCodex() = %q, _, %v, want configured default", key, ok)
	}
}
