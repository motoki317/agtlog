package source

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
)

type countingSource struct {
	path   string
	parses int
}

type changingFingerprintSource struct {
	countingSource
	fingerprints int
}

func (s *changingFingerprintSource) Fingerprint(string) (string, error) {
	s.fingerprints++
	if s.fingerprints == 1 {
		return "before", nil
	}
	return "after", nil
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

func TestRegistryInvalidatesUnversionedCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: filepath.Join(root, "cache")})
	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	cachePath := registry.cachePath(adapter, path)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	delete(entry, "version")
	data, err = json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adapter.parses != 2 {
		t.Fatalf("Parse() called %d times, want unversioned cache reparsed", adapter.parses)
	}
}

func TestRegistryDoesNotCacheFileChangedDuringParse(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &changingFingerprintSource{countingSource: countingSource{path: path}}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: filepath.Join(root, "cache")})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
	}
	if adapter.parses != 2 {
		t.Fatalf("Parse() called %d times, want changed first result left uncached", adapter.parses)
	}
}
