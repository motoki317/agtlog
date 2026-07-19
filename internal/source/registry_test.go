package source_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/codex"
)

func TestRegistryDiscoversEveryRegisteredSource(t *testing.T) {
	cacheRead := 0.5
	calculator := cost.NewCalculator(cost.Table{
		"claude-opus-4-8": {Input: 1, Output: 1},
		"claude-fable-5":  {Input: 1, Output: 1},
		"claude-sonnet-5": {Input: 1, Output: 1},
		"gpt-5.6":         {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5.4":         {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5":           {Input: 1, Output: 1, CacheRead: &cacheRead},
	})
	sources := []source.Source{
		claude.NewSource(claude.NewParser(calculator), []string{filepath.Join("claude", "testdata")}),
		codex.NewSource(codex.NewParser(calculator, "gpt-5"), []string{filepath.Join("codex", "testdata", "sessions")}),
	}
	registry := source.NewRegistry(sources, source.Options{Workers: 2, CacheDir: t.TempDir()})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	counts := map[model.AgentKind]int{}
	for _, session := range sessions {
		counts[session.Agent]++
	}
	if len(sessions) != 3 || counts[model.AgentClaude] != 1 || counts[model.AgentCodex] != 2 {
		t.Fatalf("Discover() returned %d sessions with counts %v", len(sessions), counts)
	}
}

func TestRegistryInvalidatesClaudeCacheForSubagentChange(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project-alpha")
	subagents := filepath.Join(project, "session-main", "subagents")
	if err := os.MkdirAll(subagents, 0o700); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(project, "session-main.jsonl")
	mainLine := "{\"type\":\"user\",\"timestamp\":\"2026-01-02T00:00:00Z\",\"sessionId\":\"session-main\",\"cwd\":\"/workspace/starship\",\"message\":{\"content\":\"Plan\"}}\n"
	if err := os.WriteFile(mainPath, []byte(mainLine), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentPath := filepath.Join(subagents, "agent-scout.jsonl")
	first := "{\"type\":\"assistant\",\"timestamp\":\"2026-01-02T00:00:01Z\",\"agentId\":\"scout\",\"requestId\":\"request-a\",\"message\":{\"id\":\"message-a\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":10}}}\n"
	if err := os.WriteFile(subagentPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	calculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: 1}})
	adapter := claude.NewSource(claude.NewParser(calculator), []string{root})
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: filepath.Join(root, "cache")})

	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := "{\"type\":\"assistant\",\"timestamp\":\"2026-01-02T00:00:02Z\",\"agentId\":\"scout\",\"requestId\":\"request-b\",\"message\":{\"id\":\"message-b\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":20}}}\n"
	file, err := os.OpenFile(subagentPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(second); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions[0].TotalUsage().InputTokens; got != 30 {
		t.Fatalf("TotalUsage().InputTokens = %d, want 30 after subagent append", got)
	}
}

func TestRegistryInvalidatesCacheWhenPricingChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-main.jsonl")
	line := `{"type":"assistant","timestamp":"2026-01-02T00:00:00Z","sessionId":"session-main","requestId":"request-a","message":{"id":"message-a","model":"claude-opus-4-8","usage":{"input_tokens":10}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(root, "cache")
	discoverCost := func(inputRate float64) float64 {
		calculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: inputRate}})
		adapter := claude.NewSource(claude.NewParser(calculator), []string{root})
		registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: cacheDir})
		sessions, err := registry.Discover(context.Background())
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		return sessions[0].Cost.USD
	}

	if got := discoverCost(1); got != 10 {
		t.Fatalf("first cost = %v, want 10", got)
	}
	if got := discoverCost(2); got != 20 {
		t.Fatalf("cost after pricing change = %v, want 20", got)
	}
}
