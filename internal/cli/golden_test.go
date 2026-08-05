package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/codex"
)

func TestCommandGoldenOutput(t *testing.T) {
	originalNow := textNow
	textNow = func() time.Time { return time.Date(2026, 8, 5, 11, 12, 4, 0, time.FixedZone("fictional", 9*60*60)) }
	t.Cleanup(func() { textNow = originalNow })
	tests := []struct {
		name string
		args []string
	}{
		{name: "list-json", args: []string{"list", "--all"}},
		{name: "list-text", args: []string{"list", "--all", "--format", "text"}},
		{name: "show-json", args: []string{"show", "claude:session-claude", "--all"}},
		{name: "show-text", args: []string{"show", "claude:session-claude", "--all", "--format", "text"}},
		{name: "search-json", args: []string{"search", "relay", "--all"}},
		{name: "search-text", args: []string{"search", "relay", "--all", "--format", "text"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := goldenRegistry()
			var output bytes.Buffer
			if err := Execute(context.Background(), test.args, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", test.name+".golden")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatalf("output differs from %s\n--- want ---\n%s\n--- got ---\n%s", path, want, output.Bytes())
			}
		})
	}
}

func TestFormatsContainNoRawTerminalEscapes(t *testing.T) {
	session := &model.Session{ID: "unsafe-session", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: "before\x1b[31mred\x1b[0m\nafter"}}}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := Execute(context.Background(), []string{"show", "unsafe-session", "--format", format}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
				t.Fatal(err)
			}
			if bytes.ContainsRune(output.Bytes(), '\x1b') {
				t.Fatalf("%s output contains a raw escape: %q", format, output.Bytes())
			}
		})
	}
}

func goldenRegistry() *source.Registry {
	cacheRead := 0.0005
	calculator := cost.NewCalculator(cost.Table{
		"claude-opus-4-8": {Input: 0.001, Output: 0.002, CacheRead: &cacheRead},
		"gpt-5.6":         {Input: 0.002, Output: 0.003, CacheRead: &cacheRead},
	})
	corpus := filepath.Join("testdata", "corpus")
	return source.NewRegistry([]source.Source{
		claude.NewSource(claude.NewParser(calculator), []string{filepath.Join(corpus, "claude")}),
		codex.NewSource(codex.NewParser(calculator, "gpt-5.6"), []string{filepath.Join(corpus, "codex")}),
	}, source.Options{Workers: 1})
}
