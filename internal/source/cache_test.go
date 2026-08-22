package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
)

type countingSource struct {
	path             string
	agent            model.AgentKind
	cacheFingerprint string
	parses           int
	details          int
	reprices         int
	reprice          func(*model.Session)
}

type changingFingerprintSource struct {
	countingSource
	fingerprints int
}

type emptyCacheFingerprintSource struct {
	*countingSource
}

type cancelingWriter struct {
	cancel context.CancelFunc
	calls  int
}

func (w *cancelingWriter) Write(data []byte) (int, error) {
	w.calls++
	w.cancel()
	return len(data), nil
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestContextReaderStopsBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &contextReader{ctx: ctx, reader: bytes.NewReader(make([]byte, 256<<10))}
	buffer := make([]byte, 256<<10)
	if read, err := reader.Read(buffer); err != nil || read != 128<<10 {
		t.Fatalf("first Read() = %d, %v; want one bounded chunk", read, err)
	}
	cancel()
	if _, err := reader.Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Read() error = %v, want context canceled", err)
	}
}

func TestWriteFileContextStopsBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelingWriter{cancel: cancel}
	err := writeFileContext(ctx, writer, make([]byte, 256<<10))
	if !errors.Is(err, context.Canceled) || writer.calls != 1 {
		t.Fatalf("writeFileContext() = %v after %d writes, want cancellation after one chunk", err, writer.calls)
	}
}

func TestWriteFileContextRejectsZeroLengthWrite(t *testing.T) {
	if err := writeFileContext(context.Background(), zeroWriter{}, []byte("cache")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeFileContext() error = %v, want short write", err)
	}
}

type barrierSource struct {
	*countingSource
	ready   chan<- struct{}
	release <-chan struct{}
}

type graphSource struct {
	sessions map[string]*model.Session
}

func (s graphSource) Agent() model.AgentKind   { return model.AgentCodex }
func (s graphSource) Roots() []string          { return []string{"/fictional"} }
func (s graphSource) CacheFingerprint() string { return "test-graph-parser-v1" }
func (s graphSource) Discover(context.Context) ([]string, error) {
	paths := make([]string, 0, len(s.sessions))
	for path := range s.sessions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
func (s graphSource) Parse(path string) (*model.Session, error) { return s.sessions[path], nil }
func (s graphSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s graphSource) Reprice(*model.Session) {}

func (s *changingFingerprintSource) Fingerprint(string) (string, error) {
	s.fingerprints++
	if s.fingerprints == 1 {
		return "before", nil
	}
	return "after", nil
}

func (s emptyCacheFingerprintSource) CacheFingerprint() string { return "" }

func (s *barrierSource) Parse(path string) (*model.Session, error) {
	s.ready <- struct{}{}
	<-s.release
	return s.countingSource.Parse(path)
}
func (s *barrierSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}

func (s *countingSource) Agent() model.AgentKind {
	if s.agent == "" {
		return model.AgentClaude
	}
	return s.agent
}
func (s *countingSource) Roots() []string { return []string{filepath.Dir(s.path)} }
func (s *countingSource) CacheFingerprint() string {
	if s.cacheFingerprint == "" {
		return "test-parser-v1:pricing"
	}
	return s.cacheFingerprint
}
func (s *countingSource) Fingerprint(path string) (string, error) {
	fingerprint, err := fileFingerprint(path)
	if err != nil {
		return "", err
	}
	return s.CacheFingerprint() + "\x00" + fingerprint, nil
}
func (s *countingSource) Discover(context.Context) ([]string, error) {
	return []string{s.path}, nil
}
func (s *countingSource) Parse(path string) (*model.Session, error) {
	s.parses++
	return &model.Session{ID: "cached-session", Agent: model.AgentClaude, Path: path}, nil
}
func (s *countingSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s *countingSource) Reprice(session *model.Session) {
	s.reprices++
	if s.reprice != nil {
		s.reprice(session)
	}
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

func TestRegistryIgnoresAndPreservesLegacyCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})
	fingerprint, err := sourceFingerprint(adapter, path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cacheEntry{
		Version: cacheVersion, Agent: adapter.Agent(), Fingerprint: fingerprint,
		Session: &model.Session{ID: "legacy-session", Agent: adapter.Agent(), Path: path},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(cacheDir, cacheEntryName(adapter, path))
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		sessions, err := registry.Discover(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 || sessions[0].ID != "cached-session" {
			t.Fatalf("Discover() sessions = %#v, want parsed current session", sessions)
		}
	}

	if adapter.parses != 1 {
		t.Fatalf("Parse() called %d times, want legacy entry ignored then namespace reused", adapter.parses)
	}
	if got, err := os.ReadFile(legacyPath); err != nil || string(got) != string(data) {
		t.Fatalf("legacy cache changed: data = %q, error = %v", got, err)
	}
}

func TestRegistryKeepsParserFingerprintCachesIndependent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	first := &countingSource{path: path, cacheFingerprint: "test-parser-v1:pricing-a"}
	second := &countingSource{path: path, cacheFingerprint: "test-parser-v2:pricing-b"}
	firstRegistry := NewRegistry([]Source{first}, Options{Workers: 1, CacheDir: cacheDir})
	secondRegistry := NewRegistry([]Source{second}, Options{Workers: 1, CacheDir: cacheDir})

	for _, registry := range []*Registry{firstRegistry, secondRegistry, firstRegistry, secondRegistry} {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
	}

	if first.parses != 1 || second.parses != 1 {
		t.Fatalf("Parse() calls = first %d, second %d, want one each", first.parses, second.parses)
	}
	firstPath := firstRegistry.cachePath(first, path)
	secondPath := secondRegistry.cachePath(second, path)
	if firstPath == secondPath {
		t.Fatalf("cache paths share a namespace: %q", firstPath)
	}
	for _, cachePath := range []string{firstPath, secondPath} {
		if _, err := os.Stat(cachePath); err != nil {
			t.Fatalf("cache entry %q error = %v", cachePath, err)
		}
	}
	expected := sha256.Sum256([]byte(first.CacheFingerprint()))
	if got, want := filepath.Base(filepath.Dir(firstPath)), hex.EncodeToString(expected[:cacheNamespaceBytes]); got != want {
		t.Fatalf("first cache namespace = %q, want parser fingerprint hash %q", got, want)
	}
	other := &countingSource{path: filepath.Join(root, "other.jsonl"), agent: model.AgentCodex, cacheFingerprint: first.CacheFingerprint()}
	if got, want := filepath.Dir(firstRegistry.cachePath(other, other.path)), filepath.Dir(firstPath); got != want {
		t.Fatalf("same parser fingerprint namespace = %q, want %q despite different agent and path", got, want)
	}
}

func TestRegistryWritesParserFingerprintCachesConcurrently(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	first := &barrierSource{countingSource: &countingSource{path: path, cacheFingerprint: "test-parser-v1:pricing-a"}, ready: ready, release: release}
	second := &barrierSource{countingSource: &countingSource{path: path, cacheFingerprint: "test-parser-v2:pricing-b"}, ready: ready, release: release}
	registries := []*Registry{
		NewRegistry([]Source{first}, Options{Workers: 1, CacheDir: cacheDir}),
		NewRegistry([]Source{second}, Options{Workers: 1, CacheDir: cacheDir}),
	}

	var group sync.WaitGroup
	discoverErrors := make(chan error, len(registries))
	for _, registry := range registries {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := registry.Discover(context.Background())
			discoverErrors <- err
		}()
	}
	for range registries {
		<-ready
	}
	close(release)
	group.Wait()
	close(discoverErrors)
	for err := range discoverErrors {
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
	}
	for _, registry := range registries {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatalf("cached Discover() error = %v", err)
		}
	}

	if first.parses != 1 || second.parses != 1 {
		t.Fatalf("concurrent Parse() calls = first %d, second %d, want one each", first.parses, second.parses)
	}
}

func TestRegistryChangedLogMissesWithinParserNamespace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	adapter := &countingSource{path: path, cacheFingerprint: "test-parser-v1:pricing"}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatalf("first Discover() error = %v", err)
	}
	cachePath := registry.cachePath(adapter, path)
	namespace := filepath.Dir(cachePath)
	if namespace == cacheDir {
		t.Fatalf("cache path %q has no parser namespace", cachePath)
	}
	namespaceName := filepath.Base(namespace)
	if _, err := hex.DecodeString(namespaceName); err != nil || len(namespaceName) != 2*cacheNamespaceBytes {
		t.Fatalf("cache namespace = %q, want %d hexadecimal characters", namespaceName, 2*cacheNamespaceBytes)
	}
	if err := os.WriteFile(path, []byte("{\"changed\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover() error = %v", err)
	}

	if adapter.parses != 2 {
		t.Fatalf("Parse() called %d times, want changed log reparsed", adapter.parses)
	}
	if got := filepath.Dir(registry.cachePath(adapter, path)); got != namespace {
		t.Fatalf("changed log namespace = %q, want %q", got, namespace)
	}
}

func TestNewRegistrySweepsOnlyStaleSummaryTemps(t *testing.T) {
	cacheDir := t.TempDir()
	stalePath := filepath.Join(cacheDir, "summary-stale.tmp")
	freshPath := filepath.Join(cacheDir, "summary-fresh.tmp")
	for _, path := range []string{stalePath, freshPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	staleTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: filepath.Join(t.TempDir(), "session.jsonl")}
	namespacePath, ok := (&Registry{options: Options{CacheDir: cacheDir}}).cacheNamespaceDir(adapter)
	if !ok {
		t.Fatal("cacheNamespaceDir() rejected test fingerprint")
	}
	if err := os.Mkdir(namespacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	currentTempDir := filepath.Join(namespacePath, cacheTempDirName)
	if err := os.Mkdir(currentTempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	currentFreshPath := filepath.Join(currentTempDir, "summary-fresh.tmp")
	if err := os.WriteFile(currentFreshPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := &countingSource{path: adapter.path, cacheFingerprint: "test-parser-previous:pricing"}
	previousNamespace, ok := (&Registry{options: Options{CacheDir: cacheDir}}).cacheNamespaceDir(previous)
	if !ok {
		t.Fatal("cacheNamespaceDir() rejected previous test fingerprint")
	}
	previousTempDir := filepath.Join(previousNamespace, cacheTempDirName)
	if err := os.MkdirAll(previousTempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	previousStalePath := filepath.Join(previousTempDir, "summary-stale.tmp")
	if err := os.WriteFile(previousStalePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(previousStalePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	namespaceEntry := filepath.Join(namespacePath, "entry.json")
	legacyEntry := filepath.Join(cacheDir, "legacy.json")
	unrelatedTemp := filepath.Join(cacheDir, "other.tmp")
	for _, path := range []string{namespaceEntry, legacyEntry, unrelatedTemp} {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	linkedTarget := filepath.Join(t.TempDir(), "target.tmp")
	if err := os.WriteFile(linkedTarget, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedTemp := filepath.Join(cacheDir, "summary-linked.tmp")
	if err := os.Symlink(linkedTarget, linkedTemp); err != nil {
		t.Fatal(err)
	}
	directoryTemp := filepath.Join(cacheDir, "summary-directory.tmp")
	if err := os.Mkdir(directoryTemp, 0o700); err != nil {
		t.Fatal(err)
	}

	NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale temp error = %v, want not exist", err)
	}
	if _, err := os.Stat(previousStalePath); !os.IsNotExist(err) {
		t.Fatalf("previous-namespace stale temp error = %v, want not exist", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh temp error = %v, want preserved", err)
	}
	for _, path := range []string{currentFreshPath, namespaceEntry, legacyEntry, unrelatedTemp, linkedTemp, directoryTemp, linkedTarget} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("preserved path %q error = %v", path, err)
		}
	}
}

func TestCacheTempSweepBoundsRemovals(t *testing.T) {
	cacheDir := t.TempDir()
	adapter := &countingSource{path: filepath.Join(t.TempDir(), "session.jsonl")}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})
	namespaceDir, ok := registry.cacheNamespaceDir(adapter)
	if !ok {
		t.Fatal("cacheNamespaceDir() rejected test fingerprint")
	}
	if err := os.Mkdir(namespaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	temporaryDir := filepath.Join(namespaceDir, cacheTempDirName)
	if err := os.Mkdir(temporaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	staleTime := now.Add(-staleCacheTempAge)
	for index := range maxCacheTempSweepRemovals + 1 {
		path := filepath.Join(temporaryDir, fmt.Sprintf("summary-%03d.tmp", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, staleTime, staleTime); err != nil {
			t.Fatal(err)
		}
	}

	registry.sweepStaleCacheTemps(now)

	entries, err := os.ReadDir(temporaryDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(entries); got != 1 {
		t.Fatalf("remaining stale temps = %d, want one after %d-removal bound", got, maxCacheTempSweepRemovals)
	}
}

func TestCacheTempSweepBoundsInspectedEntries(t *testing.T) {
	cacheDir := t.TempDir()
	temporaryDir := filepath.Join(cacheDir, cacheTempDirName)
	if err := os.Mkdir(temporaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"summary-a.tmp", "summary-b.tmp"} {
		path := filepath.Join(temporaryDir, name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		staleTime := now.Add(-staleCacheTempAge)
		if err := os.Chtimes(path, staleTime, staleTime); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	remainingEntries := 1
	remainingRemovals := 2

	sweepCacheTempDirectory(root, cacheTempDirName, now, &remainingEntries, &remainingRemovals)

	entries, err := os.ReadDir(temporaryDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || remainingEntries != 0 || remainingRemovals != 1 {
		t.Fatalf("sweep result = %d files, %d entry budget, %d removal budget", len(entries), remainingEntries, remainingRemovals)
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

func TestRegistryRejectsCacheEntryTrailingJSON(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	registry.storeCached(adapter, path, "fingerprint", &model.Session{ID: "cached-session"}, nil)

	file, err := os.OpenFile(registry.cachePath(adapter, path), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n{}"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := registry.loadCached(adapter, path, "fingerprint"); ok {
		t.Fatal("loadCached() accepted a second JSON value after the cache entry")
	}
}

func TestRegistryRejectsUnsafeCacheNamespace(t *testing.T) {
	for _, shape := range []string{"symlink", "regular file", "writable directory"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "session.jsonl")
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cacheDir := t.TempDir()
			adapter := &countingSource{path: path}
			registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})
			namespacePath, ok := registry.cacheNamespaceDir(adapter)
			if !ok {
				t.Fatal("cacheNamespaceDir() rejected test fingerprint")
			}
			fingerprint := "source-fingerprint"
			data, err := json.Marshal(cacheEntry{
				Version: cacheVersion, Agent: adapter.Agent(), Fingerprint: fingerprint,
				Session: &model.Session{ID: "unsafe", Agent: adapter.Agent(), Path: path},
			})
			if err != nil {
				t.Fatal(err)
			}
			protectedPath := namespacePath
			protectedData := []byte("preserve")
			switch shape {
			case "symlink":
				target := t.TempDir()
				protectedPath = filepath.Join(target, cacheEntryName(adapter, path))
				protectedData = data
				if err := os.WriteFile(protectedPath, protectedData, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, namespacePath); err != nil {
					t.Fatal(err)
				}
			case "regular file":
				if err := os.WriteFile(namespacePath, protectedData, 0o600); err != nil {
					t.Fatal(err)
				}
			case "writable directory":
				if err := os.Mkdir(namespacePath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(namespacePath, 0o770); err != nil {
					t.Fatal(err)
				}
				protectedPath = filepath.Join(namespacePath, cacheEntryName(adapter, path))
				protectedData = data
				if err := os.WriteFile(protectedPath, protectedData, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, _, loaded := registry.loadCached(adapter, path, fingerprint); loaded {
				t.Fatal("loadCached() accepted an unsafe namespace")
			}
			registry.storeCached(adapter, path, fingerprint, &model.Session{ID: "replacement"}, nil)
			registry.topLevelRemoved(context.Background(), []string{path})

			got, err := os.ReadFile(protectedPath)
			if err != nil || string(got) != string(protectedData) {
				t.Fatalf("protected path changed: data = %q, error = %v", got, err)
			}
		})
	}
}

func TestRegistryRejectsUnsafeCacheTempDirectory(t *testing.T) {
	for _, shape := range []string{"symlink", "regular file", "writable directory"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "session.jsonl")
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cacheDir := t.TempDir()
			adapter := &countingSource{path: path}
			registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})
			namespacePath, ok := registry.cacheNamespaceDir(adapter)
			if !ok {
				t.Fatal("cacheNamespaceDir() rejected test fingerprint")
			}
			if err := os.Mkdir(namespacePath, 0o700); err != nil {
				t.Fatal(err)
			}
			temporaryPath := filepath.Join(namespacePath, cacheTempDirName)
			protectedPath := temporaryPath
			protectedData := []byte("preserve")
			switch shape {
			case "symlink":
				target := t.TempDir()
				protectedPath = filepath.Join(target, "preserve")
				if err := os.WriteFile(protectedPath, protectedData, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, temporaryPath); err != nil {
					t.Fatal(err)
				}
			case "regular file":
				if err := os.WriteFile(temporaryPath, protectedData, 0o600); err != nil {
					t.Fatal(err)
				}
			case "writable directory":
				if err := os.Mkdir(temporaryPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(temporaryPath, 0o770); err != nil {
					t.Fatal(err)
				}
				protectedPath = filepath.Join(temporaryPath, "preserve")
				if err := os.WriteFile(protectedPath, protectedData, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			registry.storeCached(adapter, path, "source-fingerprint", &model.Session{ID: "replacement"}, nil)
			registry.sweepStaleCacheTemps(time.Now().Add(48 * time.Hour))

			if _, err := os.Stat(registry.cachePath(adapter, path)); !os.IsNotExist(err) {
				t.Fatalf("cache entry error = %v, want not exist", err)
			}
			got, err := os.ReadFile(protectedPath)
			if err != nil || string(got) != string(protectedData) {
				t.Fatalf("protected path changed: data = %q, error = %v", got, err)
			}
		})
	}
}

func TestRegistryDisablesCacheForEmptyParserFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := emptyCacheFingerprintSource{countingSource: &countingSource{path: path}}
	cacheDir := t.TempDir()
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if adapter.parses != 2 {
		t.Fatalf("Parse() called %d times, want cache disabled", adapter.parses)
	}
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) != 0 {
		t.Fatalf("cache directory entries = %#v, error = %v, want empty", entries, err)
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

func TestRegistryRechecksCacheSafetyBetweenDiscoveryPasses(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheParent := t.TempDir()
	cacheDir := filepath.Join(cacheParent, "cache")
	if err := os.Mkdir(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cacheDir, filepath.Join(cacheParent, "old-cache")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, cacheDir); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if adapter.parses != 2 {
		t.Fatalf("Parse() called %d times, want unsafe moved cache rejected on the next pass", adapter.parses)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("source root entries = %v, want only the session log", entries)
	}
}

func TestRegistryCreatesMissingCacheDirectoryComponents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache", "agtlog")
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: cacheDir})

	for range 2 {
		if _, err := registry.Discover(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if adapter.parses != 1 {
		t.Fatalf("Parse() called %d times, want missing cache directory created", adapter.parses)
	}
	if _, err := os.Stat(registry.cachePath(adapter, path)); err != nil {
		t.Fatalf("cache entry error = %v", err)
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
	usage := model.Usage{Model: "claude-opus-4-8", InputTokens: 3}
	session := &model.Session{ID: "session-workflow", Agent: model.AgentClaude, Path: path, Subagents: []*model.Session{{
		ID: "wf-river-run", Agent: model.AgentClaude, Path: path + "#wf-river-run", Title: "River survey", Group: true,
		StartedAt: started, UpdatedAt: updated, Subagents: []*model.Session{{
			ID: "nested-mapper", Agent: model.AgentClaude, Path: filepath.Join(root, "agent-nested-mapper.jsonl"),
			Requests: []model.RequestUsage{{Usage: usage}},
		}},
	}}}
	calculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: 2}})
	calculator.ApplySession(session)
	adapter := &countingSource{path: path, reprice: calculator.ApplySession}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	fingerprint, err := sourceFingerprint(adapter, path)
	if err != nil {
		t.Fatal(err)
	}
	registry.storeCached(adapter, path, fingerprint, session, nil)

	loaded, _, err := registry.discoverSession(adapter, path)
	if err != nil || loaded == nil || len(loaded.Subagents) != 1 {
		t.Fatalf("discoverSession() = %#v, %v", loaded, err)
	}
	group := loaded.Subagents[0]
	if !group.Group || group.Path != path+"#wf-river-run" || !group.StartedAt.Equal(started) || !group.UpdatedAt.Equal(updated) ||
		len(group.Subagents) != 1 || loaded.DescendantAgentCount() != 1 {
		t.Fatalf("cached workflow graph = %#v", loaded)
	}
	if want := map[string]float64{"claude-opus-4-8": 6}; !reflect.DeepEqual(group.ModelCosts, want) {
		t.Fatalf("cached workflow ModelCosts = %#v, want child rollup %#v", group.ModelCosts, want)
	}
	if adapter.parses != 0 || adapter.reprices != 1 {
		t.Fatalf("cache path calls = %d parses, %d reprices, want 0 and 1", adapter.parses, adapter.reprices)
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
