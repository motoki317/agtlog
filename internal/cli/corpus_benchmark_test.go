package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/codex"
)

const (
	referenceSessionCount  = 1_600
	referenceCorpusBytes   = 1_300_000_000
	referenceSubagentBytes = 16 << 10
)

type syntheticLine struct {
	prefix string
	suffix string
}

func TestReferenceCorpusDistribution(t *testing.T) {
	sizes := referenceCorpusSizes()
	total := referenceSubagentBytes
	uniqueSizes := make(map[int]bool)
	eventCounts := make(map[int]int)
	for index, size := range sizes {
		total += size
		uniqueSizes[size] = true
		eventCounts[referenceEventCount(index)]++
	}
	if len(sizes) != referenceSessionCount || total != referenceCorpusBytes || len(uniqueSizes) < 5 {
		t.Fatalf("sizes = %d, total = %d, unique = %d", len(sizes), total, len(uniqueSizes))
	}
	for _, count := range []int{8, 16, 32, 64} {
		if eventCounts[count] == 0 {
			t.Fatalf("event-count distribution = %#v", eventCounts)
		}
	}
}

func BenchmarkMachineReadableCLILatency(b *testing.B) {
	registry := generateReferenceRegistry(b)
	factory := func(context.Context, Options) (Registry, error) { return registry, nil }
	commands := []struct {
		name   string
		args   [][]string
		budget time.Duration
	}{
		{name: "list", args: [][]string{
			{"list", "--all", "--offline"},
			{"list", "--query", "telemetry", "--sort", "tokens", "--all", "--offline"},
			{"list", "--cwd", "/workspace", "--since", "2026-08-01", "--until", "2026-08-06", "--sort", "cost", "--all", "--offline"},
		}, budget: time.Second},
		{name: "show", args: [][]string{{"show", "claude:claude-0000", "--all", "--offline"}}, budget: 1500 * time.Millisecond},
		{name: "search_scoped_64", args: [][]string{{"search", "absent-sentinel", "--project", "project-00", "--all", "--offline"}}, budget: 2 * time.Second},
		{name: "search_corpus", args: [][]string{{"search", "absent-sentinel", "--all", "--offline"}}, budget: 5 * time.Second},
	}
	for _, command := range commands {
		for _, args := range command.args {
			if err := Execute(context.Background(), args, io.Discard, io.Discard, factory); err != nil {
				b.Fatalf("warm %s: %v", command.name, err)
			}
		}
	}
	for _, command := range commands {
		b.Run(command.name, func(b *testing.B) {
			var maximum time.Duration
			b.ResetTimer()
			for range b.N {
				for _, args := range command.args {
					started := time.Now()
					if err := Execute(context.Background(), args, io.Discard, io.Discard, factory); err != nil {
						b.Fatal(err)
					}
					maximum = max(maximum, time.Since(started))
				}
			}
			b.StopTimer()
			b.ReportMetric(command.budget.Seconds(), "budget_s")
			b.ReportMetric(maximum.Seconds(), "max_s")
			if maximum > command.budget {
				b.Fatalf("%s took %s; budget %s", command.name, maximum, command.budget)
			}
		})
	}
}

func generateReferenceRegistry(b *testing.B) *source.Registry {
	b.Helper()
	root := b.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	targets := referenceCorpusSizes()
	for index := range referenceSessionCount {
		var path string
		var err error
		if index < referenceSessionCount/2 {
			projectDir := filepath.Join(claudeRoot, fmt.Sprintf("project-%02d", index%25))
			path = filepath.Join(projectDir, fmt.Sprintf("claude-%04d.jsonl", index))
			err = writeClaudeCorpusFile(path, index, targets[index])
		} else {
			path = filepath.Join(codexRoot, "2026", "08", "05", fmt.Sprintf("rollout-codex-%04d.jsonl", index-referenceSessionCount/2))
			err = writeCodexCorpusFile(path, index-referenceSessionCount/2, targets[index])
		}
		if err != nil {
			b.Fatal(err)
		}
	}
	parentPath := filepath.Join(claudeRoot, "project-00", "claude-0000.jsonl")
	subagentPath := filepath.Join(strings.TrimSuffix(parentPath, filepath.Ext(parentPath)), "subagents", "agent-scout.jsonl")
	if err := writeClaudeSubagentFile(subagentPath); err != nil {
		b.Fatal(err)
	}
	cacheRead := 0.000001
	calculator := cost.NewCalculator(cost.Table{
		"claude-opus-4-8": {Input: 0.000001, Output: 0.000002},
		"gpt-5.6":         {Input: 0.000001, Output: 0.000002, CacheRead: &cacheRead},
	})
	registry := source.NewRegistry([]source.Source{
		claude.NewSource(claude.NewParser(calculator), []string{claudeRoot}),
		codex.NewSource(codex.NewParser(calculator, "gpt-5.6"), []string{codexRoot}),
	}, source.Options{CacheDir: filepath.Join(root, "cache")})
	if sessions, diagnostics, err := registry.DiscoverWithDiagnostics(context.Background()); err != nil || len(diagnostics) != 0 || len(sessions) != referenceSessionCount {
		b.Fatalf("warm discovery returned %d sessions, %d diagnostics, %v", len(sessions), len(diagnostics), err)
	}
	return registry
}

func referenceCorpusSizes() []int {
	weights := make([]int64, referenceSessionCount)
	var totalWeight int64
	for index := range weights {
		switch bucket := index % 100; {
		case bucket < 50:
			weights[index] = 1
		case bucket < 75:
			weights[index] = 2
		case bucket < 90:
			weights[index] = 4
		case bucket < 97:
			weights[index] = 8
		default:
			weights[index] = 16
		}
		totalWeight += weights[index]
	}
	target := int64(referenceCorpusBytes - referenceSubagentBytes)
	sizes := make([]int, referenceSessionCount)
	var assigned int64
	for index, weight := range weights {
		size := target * weight / totalWeight
		if index == len(weights)-1 {
			size = target - assigned
		}
		sizes[index] = int(size)
		assigned += size
	}
	return sizes
}

func referenceEventCount(index int) int {
	switch bucket := index % 100; {
	case bucket < 50:
		return 8
	case bucket < 78:
		return 16
	case bucket < 94:
		return 32
	default:
		return 64
	}
}

func writeClaudeCorpusFile(path string, index, targetBytes int) error {
	id := fmt.Sprintf("claude-%04d", index)
	project := fmt.Sprintf("project-%02d", index%25)
	fixed := fmt.Sprintf("{\"type\":\"user\",\"uuid\":\"user-%04d\",\"timestamp\":\"2026-08-05T%02d:00:00Z\",\"sessionId\":%q,\"cwd\":%q,\"message\":{\"content\":\"Survey fictional telemetry %04d\"}}\n", index, index%24, id, "/workspace/"+project, index)
	lines := make([]syntheticLine, referenceEventCount(index))
	for event := range lines {
		lines[event] = syntheticLine{
			prefix: fmt.Sprintf("{\"type\":\"assistant\",\"uuid\":\"assistant-%04d-%03d\",\"timestamp\":\"2026-08-05T%02d:%02d:00Z\",\"sessionId\":%q,\"cwd\":%q,\"requestId\":\"request-%04d-%03d\",\"message\":{\"id\":\"message-%04d-%03d\",\"model\":\"claude-opus-4-8\",\"content\":[{\"type\":\"text\",\"text\":\"", index, event, index%24, event%60, id, "/workspace/"+project, index, event, index, event),
			suffix: "\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n",
		}
	}
	return writeSizedCorpusFile(path, fixed, lines, targetBytes)
}

func writeCodexCorpusFile(path string, index, targetBytes int) error {
	id := fmt.Sprintf("codex-%04d", index)
	project := fmt.Sprintf("project-%02d", (index+referenceSessionCount/2)%25)
	fixed := fmt.Sprintf("{\"timestamp\":\"2026-08-05T%02d:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":%q,\"session_id\":%q,\"cwd\":%q,\"git\":{\"branch\":\"main\"}}}\n{\"timestamp\":\"2026-08-05T%02d:00:01Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-5.6\"}}\n{\"timestamp\":\"2026-08-05T%02d:00:02Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"Survey fictional telemetry %04d\"}}\n", index%24, id, id, "/workspace/"+project, index%24, index%24, index)
	lines := make([]syntheticLine, referenceEventCount(index+referenceSessionCount/2))
	for event := range lines {
		lines[event] = syntheticLine{
			prefix: fmt.Sprintf("{\"timestamp\":\"2026-08-05T%02d:%02d:03Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"", index%24, event%60),
			suffix: "\"}]}}\n",
		}
	}
	return writeSizedCorpusFile(path, fixed, lines, targetBytes)
}

func writeClaudeSubagentFile(path string) error {
	fixed := "{\"type\":\"user\",\"timestamp\":\"2026-08-05T00:00:00Z\",\"sessionId\":\"claude-0000\",\"agentId\":\"scout\",\"isSidechain\":true,\"cwd\":\"/workspace/project-00\",\"message\":{\"content\":\"Inspect fictional relay\"}}\n"
	lines := []syntheticLine{{
		prefix: "{\"type\":\"assistant\",\"timestamp\":\"2026-08-05T00:00:01Z\",\"sessionId\":\"claude-0000\",\"agentId\":\"scout\",\"isSidechain\":true,\"message\":{\"model\":\"claude-opus-4-8\",\"content\":[{\"type\":\"text\",\"text\":\"",
		suffix: "\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n",
	}}
	return writeSizedCorpusFile(path, fixed, lines, referenceSubagentBytes)
}

func writeSizedCorpusFile(path, fixed string, lines []syntheticLine, targetBytes int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 256<<10)
	closeWithError := func(writeErr error) error {
		flushErr := writer.Flush()
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if flushErr != nil {
			return flushErr
		}
		return closeErr
	}
	if _, err := writer.WriteString(fixed); err != nil {
		return closeWithError(err)
	}
	written := len(fixed)
	for index, line := range lines {
		remainingLines := len(lines) - index
		lineBytes := (targetBytes - written) / remainingLines
		if index == len(lines)-1 {
			lineBytes = targetBytes - written
		}
		fillerBytes := lineBytes - len(line.prefix) - len(line.suffix)
		if fillerBytes < 0 {
			return closeWithError(fmt.Errorf("target file size is too small"))
		}
		if _, err := writer.WriteString(line.prefix); err != nil {
			return closeWithError(err)
		}
		if _, err := writer.WriteString(strings.Repeat("fictional-telemetry ", fillerBytes/20)); err != nil {
			return closeWithError(err)
		}
		if remainder := fillerBytes % 20; remainder > 0 {
			if _, err := writer.WriteString(strings.Repeat("x", remainder)); err != nil {
				return closeWithError(err)
			}
		}
		if _, err := writer.WriteString(line.suffix); err != nil {
			return closeWithError(err)
		}
		written += lineBytes
	}
	return closeWithError(nil)
}
