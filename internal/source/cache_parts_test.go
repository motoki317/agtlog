package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

import "github.com/motoki317/agtlog/internal/model"

type partCountingSource struct {
	countingSource
	partParses int
	hits       int
}

func (s *partCountingSource) ParseWithPartsContext(ctx context.Context, path string, report func(string, error), load func(string) ([]byte, bool), store func(string, []byte)) (*model.Session, error) {
	if data, ok := load(path); ok {
		s.hits++
		var session model.Session
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, err
		}
		return &session, nil
	}
	s.partParses++
	session, err := s.ParseContext(ctx, path)
	if err != nil {
		return nil, err
	}
	report(path, errors.New("fixture diagnostic"))
	data, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	store(path, data)
	return session, nil
}

func newPartRegistry(t *testing.T) (*Registry, *partCountingSource) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &partCountingSource{countingSource: countingSource{path: path}}
	return NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()}), adapter
}

func TestRegistryPartStoreDiscoveryAndRefresh(t *testing.T) {
	registry, adapter := newPartRegistry(t)
	_, diagnostics, err := registry.discoverSession(adapter, adapter.path)
	if err != nil || len(diagnostics) != 1 || diagnostics[0].Err.Error() != "fixture diagnostic" {
		t.Fatalf("cold discovery diagnostics = %v, error = %v", diagnostics, err)
	}
	if _, _, err := registry.discoverSession(adapter, adapter.path); err != nil {
		t.Fatal(err)
	}
	if adapter.partParses != 1 || adapter.hits != 0 {
		t.Fatalf("summary hit: parses = %d, part hits = %d", adapter.partParses, adapter.hits)
	}
	registry.removeCached(adapter, adapter.path)
	if _, _, err := registry.discoverSession(adapter, adapter.path); err != nil {
		t.Fatal(err)
	}
	sessions, failed := registry.refresh(context.Background(), []string{adapter.path}, make(followCheckpointIndex))
	if len(sessions) != 1 || len(failed) != 0 || adapter.partParses != 1 || adapter.hits != 2 {
		t.Fatalf("refresh: sessions = %v, failed = %v, parses = %d, hits = %d", sessions, failed, adapter.partParses, adapter.hits)
	}
	namespace, _ := registry.cacheNamespaceDir(adapter)
	partPath := filepath.Join(namespace, partEntryName(adapter, adapter.path))
	hash := sha256.Sum256([]byte("part\x00" + string(adapter.Agent()) + "\x00" + adapter.path))
	if filepath.Base(partPath) != hex.EncodeToString(hash[:])+".json" || partPath == registry.cachePath(adapter, adapter.path) {
		t.Fatalf("part entry path = %q", partPath)
	}
	data, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &entry); err != nil || len(entry.Data) == 0 || entry.Data[0] != '{' {
		t.Fatalf("part payload = %q, error = %v, want embedded JSON", entry.Data, err)
	}
	info, err := os.Stat(partPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("part permissions: info = %v, error = %v", info, err)
	}
}

func TestRegistryRejectsInvalidPartEntry(t *testing.T) {
	for _, shape := range []string{"version", "agent", "path", "missing data", "invalid JSON", "trailing JSON", "oversized", "writable", "symlink"} {
		t.Run(shape, func(t *testing.T) {
			registry, adapter := newPartRegistry(t)
			if _, _, err := registry.discoverSession(adapter, adapter.path); err != nil {
				t.Fatal(err)
			}
			namespace, _ := registry.cacheNamespaceDir(adapter)
			partPath := filepath.Join(namespace, partEntryName(adapter, adapter.path))
			data, err := os.ReadFile(partPath)
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "version":
				entry[shape] = json.RawMessage(`-1`)
			case "agent":
				entry[shape] = json.RawMessage(`"codex"`)
			case "path":
				entry[shape] = json.RawMessage(`"/fictional/other.jsonl"`)
			case "missing data":
				delete(entry, "data")
			}
			data, err = json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			if shape == "invalid JSON" {
				data = []byte(`{`)
			} else if shape == "trailing JSON" {
				data = append(data, []byte(`{}`)...)
			}
			if err := os.WriteFile(partPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "oversized":
				if err := os.Truncate(partPath, maxSummaryCacheBytes+1); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(partPath, 0o660); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "protected.json")
				if err := os.WriteFile(target, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(partPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, partPath); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if got, err := os.ReadFile(target); err != nil || string(got) != string(data) {
						t.Errorf("symlink target changed: error = %v", err)
					}
				})
			}
			registry.removeCached(adapter, adapter.path)
			if _, _, err := registry.discoverSession(adapter, adapter.path); err != nil {
				t.Fatal(err)
			}
			if adapter.partParses != 2 || adapter.hits != 0 {
				t.Fatalf("invalid entry: parses = %d, hits = %d", adapter.partParses, adapter.hits)
			}
		})
	}
}

func TestPartStoreRejectsOversizedAndCanceledWrites(t *testing.T) {
	registry, adapter := newPartRegistry(t)
	root, ok := registry.openOrCreateCacheRoot()
	if !ok {
		t.Fatal("open cache root")
	}
	defer root.Close()
	storePartToRootContext(context.Background(), root, adapter, adapter.path, []byte(`"`+strings.Repeat("x", maxSummaryCacheBytes)+`"`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	storePartToRootContext(ctx, root, adapter, adapter.path, []byte(`{}`))
	if _, ok := loadPartFromRootContext(context.Background(), root, adapter, adapter.path); ok {
		t.Fatal("loaded an oversized or canceled part")
	}
	storePartToRootContext(context.Background(), root, adapter, adapter.path, []byte(`{}`))
	if _, ok := loadPartFromRootContext(ctx, root, adapter, adapter.path); ok {
		t.Fatal("loaded a part after cancellation")
	}
}

func TestRegistryPartParserRequiresCacheRoot(t *testing.T) {
	registry, adapter := newPartRegistry(t)
	registry.options.CacheDir = ""
	if _, _, err := registry.discoverSession(adapter, adapter.path); err != nil {
		t.Fatal(err)
	}
	if adapter.parses != 1 || adapter.partParses != 0 {
		t.Fatalf("cache disabled: parses = %d, part parses = %d", adapter.parses, adapter.partParses)
	}
}
