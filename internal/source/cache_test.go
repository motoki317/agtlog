package source

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

type countingSource struct {
	path    string
	parses  int
	details int
}

type changingFingerprintSource struct {
	countingSource
	fingerprints int
}

type graphSource struct {
	sessions map[string]*model.Session
}

func (s graphSource) Agent() model.AgentKind { return model.AgentCodex }
func (s graphSource) Roots() []string        { return []string{"/fictional"} }
func (s graphSource) Discover(context.Context) ([]string, error) {
	paths := make([]string, 0, len(s.sessions))
	for path := range s.sessions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
func (s graphSource) Parse(path string) (*model.Session, error) { return s.sessions[path], nil }

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
func (s *countingSource) LoadEvents(_ context.Context, session *model.Session) error {
	s.details++
	session.Events = []model.Event{{Kind: model.EventUser, Text: "Inspect the horizon"}}
	return nil
}

func TestRegistryReusesUnchangedCachedSummary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
	}
	if adapter.parses != 1 {
		t.Fatalf("Parse() called %d times, want once for unchanged file", adapter.parses)
	}
}

func TestRegistryRejectsClaudeParserV15CacheFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	registry.storeCached(adapter, path, "claude-parser-v15:pricing", &model.Session{ID: "cached-session"}, nil)

	if _, _, ok := registry.loadCached(adapter, path, "claude-parser-v16:pricing"); ok {
		t.Fatal("loadCached() accepted a claude-parser-v15 entry under v16")
	}
	if cacheVersion != 4 {
		t.Fatalf("cacheVersion = %d, want unchanged schema version 4", cacheVersion)
	}
}

func TestRegistrySkipsOversizedSummaryCacheEntry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oversized.jsonl")
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	session := &model.Session{
		ID:    "oversized",
		Agent: model.AgentClaude,
		Path:  path,
		Title: strings.Repeat("x", maxSummaryCacheBytes),
	}

	registry.storeCached(adapter, path, "fingerprint", session, nil)

	if _, err := os.Stat(registry.cachePath(adapter, path)); !os.IsNotExist(err) {
		t.Fatalf("oversized cache path error = %v, want not exist", err)
	}
}

func TestRegistryDoesNotCacheInsideSourceRoots(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		name := "cache directory"
		if ancestor {
			name = "ancestor"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "session.jsonl")
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			linked := filepath.Join(t.TempDir(), "cache")
			if err := os.Symlink(root, linked); err != nil {
				t.Fatal(err)
			}
			cacheDir := linked
			if ancestor {
				cacheDir = filepath.Join(linked, "agtlog")
			}
			adapter := &countingSource{path: path}
			registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

			for range 2 {
				if _, err := registry.Discover(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if adapter.parses != 2 {
				t.Fatalf("Parse() called %d times, want cache disabled", adapter.parses)
			}
			if _, err := os.Stat(registry.cachePath(adapter, path)); !os.IsNotExist(err) {
				t.Fatalf("cache path error = %v, want not exist", err)
			}
		})
	}
}

func TestRegistryCachesThroughSymlinkOutsideSourceRoots(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(target, cacheDir); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if adapter.parses != 1 {
		t.Fatalf("Parse() called %d times, want symlinked cache reused", adapter.parses)
	}
}

func TestCacheDirOutsideRootsRejectsSourceAncestor(t *testing.T) {
	cacheDir := t.TempDir()
	root := filepath.Join(cacheDir, "projects")
	if CacheDirOutsideRoots(cacheDir, []string{root}) {
		t.Fatal("CacheDirOutsideRoots() accepted source-root ancestor")
	}
}

func TestRegistryLoadsDetailOnlyWhenRequested(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1})
	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if adapter.details != 0 || len(sessions[0].Events) != 0 {
		t.Fatalf("Discover() eagerly loaded detail")
	}

	if err := registry.LoadDetail(context.Background(), sessions[0]); err != nil {
		t.Fatalf("LoadDetail() error = %v", err)
	}
	if adapter.details != 1 || len(sessions[0].Events) != 1 {
		t.Fatalf("detail calls = %d, events = %#v", adapter.details, sessions[0].Events)
	}
}

func TestRegistryRefusesDetailOutsideRegisteredRoots(t *testing.T) {
	root := t.TempDir()
	adapter := &countingSource{path: filepath.Join(root, "session.jsonl")}
	registry := NewRegistry([]Source{adapter}, Options{})
	outside := &model.Session{ID: "outside", Agent: model.AgentClaude, Path: filepath.Join(t.TempDir(), "outside.jsonl")}

	if err := registry.LoadDetail(context.Background(), outside); err == nil {
		t.Fatal("LoadDetail() accepted a session outside registered roots")
	}
	if adapter.details != 0 {
		t.Fatalf("outside detail invoked loader %d times", adapter.details)
	}
}

func TestRegistryLinksSubagentSummariesAndKeepsOnlyTopLevel(t *testing.T) {
	child := &model.Session{ID: "child", ParentID: "root", Agent: model.AgentCodex, Path: "/fictional/child.jsonl", Usage: []model.Usage{{InputTokens: 20}}, Cost: model.Cost{USD: 2, Estimated: true}}
	parent := &model.Session{ID: "root", Agent: model.AgentCodex, Path: "/fictional/root.jsonl", Usage: []model.Usage{{InputTokens: 10}}, Cost: model.Cost{USD: 1, Estimated: true}, Subagents: []*model.Session{{ID: "child", Agent: model.AgentCodex}}}
	adapter := graphSource{sessions: map[string]*model.Session{parent.Path: parent, child.Path: child}}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 2})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0] != parent || sessions[0].Subagents[0] != child {
		t.Fatalf("top-level graph = %#v, want linked parent only", sessions)
	}
	if got := sessions[0].TotalUsage().InputTokens; got != 30 {
		t.Fatalf("rolled-up input = %d, want 30", got)
	}
	if got := sessions[0].TotalCost().USD; got != 3 {
		t.Fatalf("rolled-up cost = %v, want 3", got)
	}
}

func TestRegistryKeepsOrphanedCodexChildInspectable(t *testing.T) {
	orphan := &model.Session{
		ID:       "child",
		ParentID: "missing-parent",
		Agent:    model.AgentCodex,
		Path:     "/fictional/orphan.jsonl",
		Title:    "orphaned scout",
	}
	adapter := graphSource{sessions: map[string]*model.Session{orphan.Path: orphan}}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0] != orphan {
		t.Fatalf("orphan discovery = %#v, want inspectable child", sessions)
	}
}

func TestLinkSessionGraphsRevertsMissingCachedChildToSpawnPlaceholder(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "root.jsonl")
	missingChildPath := filepath.Join(root, "child.jsonl")
	parent := &model.Session{
		ID:    "root",
		Agent: model.AgentCodex,
		Path:  parentPath,
		Subagents: []*model.Session{{
			ID:        "child",
			Agent:     model.AgentCodex,
			Path:      missingChildPath,
			AgentPath: "/root/scout",
		}},
	}

	linkSessionGraphs([]*model.Session{parent})

	if got := parent.Subagents[0].Path; got != parentPath+"#scout" {
		t.Fatalf("missing cached child path = %q, want spawn placeholder", got)
	}
}

func TestRegistryInvalidatesUnversionedCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
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

func TestRegistryInvalidatesVersionThreeCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
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
	entry["version"] = float64(3)
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
		t.Fatalf("Parse() called %d times, want version 3 cache reparsed", adapter.parses)
	}
}

func TestRegistryRoundTripsWorkflowGroupInCurrentCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 2, 3, 4, 0, 0, 0, time.UTC)
	updated := started.Add(time.Hour)
	session := &model.Session{ID: "session-workflow", Agent: model.AgentClaude, Path: path, Subagents: []*model.Session{{
		ID: "wf-river-run", Agent: model.AgentClaude, Path: path + "#wf-river-run", Title: "River survey", Group: true,
		StartedAt: started, UpdatedAt: updated, Subagents: []*model.Session{{ID: "nested-mapper", Agent: model.AgentClaude, Path: filepath.Join(root, "agent-nested-mapper.jsonl")}},
	}}}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	registry.storeCached(adapter, path, "fingerprint", session, nil)

	loaded, _, ok := registry.loadCached(adapter, path, "fingerprint")
	if !ok || loaded == nil || len(loaded.Subagents) != 1 {
		t.Fatalf("loadCached() = %#v, %t", loaded, ok)
	}
	group := loaded.Subagents[0]
	if !group.Group || group.Path != path+"#wf-river-run" || !group.StartedAt.Equal(started) || !group.UpdatedAt.Equal(updated) ||
		len(group.Subagents) != 1 || loaded.DescendantAgentCount() != 1 {
		t.Fatalf("cached workflow graph = %#v", loaded)
	}
}

func TestRegistryDoesNotCacheFileChangedDuringParse(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &changingFingerprintSource{countingSource: countingSource{path: path}}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
	}
	if adapter.parses != 2 {
		t.Fatalf("Parse() called %d times, want changed first result left uncached", adapter.parses)
	}
}
