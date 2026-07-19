package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
)

type countingSource struct {
	path   string
	parses int
}

func (s *countingSource) Agent() model.AgentKind { return model.AgentClaude }
func (s *countingSource) Roots() []string        { return []string{filepath.Dir(s.path)} }
func (s *countingSource) Discover(context.Context) ([]string, error) {
	return []string{s.path}, nil
}
func (s *countingSource) Parse(path string) (*model.Session, error) {
	s.parses++
	return &model.Session{ID: "cached-session", Agent: model.AgentClaude, Path: path}, nil
}

func TestRegistryReusesUnchangedCachedSummary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: filepath.Join(root, "cache")})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
	}
	if adapter.parses != 1 {
		t.Fatalf("Parse() called %d times, want once for unchanged file", adapter.parses)
	}
}
