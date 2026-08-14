package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

type cancelableRefreshSource struct {
	root        string
	path        string
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

type cancelableFingerprintSource struct {
	root        string
	path        string
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func (s *cancelableFingerprintSource) Agent() model.AgentKind { return model.AgentClaude }
func (s *cancelableFingerprintSource) Roots() []string        { return []string{s.root} }
func (s *cancelableFingerprintSource) CacheFingerprint() string {
	return "cancelable-fingerprint-v1"
}
func (s *cancelableFingerprintSource) Discover(context.Context) ([]string, error) {
	return []string{s.path}, nil
}
func (s *cancelableFingerprintSource) Parse(string) (*model.Session, error) {
	return &model.Session{Agent: model.AgentClaude, Path: s.path}, nil
}
func (s *cancelableFingerprintSource) ParseContext(context.Context, string) (*model.Session, error) {
	return &model.Session{Agent: model.AgentClaude, Path: s.path}, nil
}
func (s *cancelableFingerprintSource) Fingerprint(string) (string, error) {
	s.startedOnce.Do(func() { close(s.started) })
	<-s.release
	return "released", nil
}
func (s *cancelableFingerprintSource) FingerprintContext(ctx context.Context, _ string) (string, error) {
	s.startedOnce.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.release:
		return "released", nil
	}
}

type cancelableSnapshotSource struct {
	root        string
	path        string
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	parses      atomic.Int32
}

func (s *cancelableSnapshotSource) Agent() model.AgentKind { return model.AgentCodex }
func (s *cancelableSnapshotSource) Roots() []string        { return []string{s.root} }
func (s *cancelableSnapshotSource) CacheFingerprint() string {
	return "cancelable-snapshot-v1"
}
func (s *cancelableSnapshotSource) Discover(context.Context) ([]string, error) {
	return []string{s.path}, nil
}
func (s *cancelableSnapshotSource) Parse(string) (*model.Session, error) {
	s.startedOnce.Do(func() { close(s.started) })
	<-s.release
	return &model.Session{Agent: model.AgentCodex, Path: s.path}, nil
}
func (s *cancelableSnapshotSource) ParseContext(ctx context.Context, _ string) (*model.Session, error) {
	if s.parses.Add(1) == 1 {
		return &model.Session{Agent: model.AgentCodex, Path: s.path}, nil
	}
	s.startedOnce.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return &model.Session{Agent: model.AgentCodex, Path: s.path}, nil
	}
}

func (s *cancelableRefreshSource) Agent() model.AgentKind { return model.AgentClaude }
func (s *cancelableRefreshSource) Roots() []string        { return []string{s.root} }
func (s *cancelableRefreshSource) CacheFingerprint() string {
	return "cancelable-refresh-v1"
}
func (s *cancelableRefreshSource) Discover(context.Context) ([]string, error) {
	return []string{s.path}, nil
}
func (s *cancelableRefreshSource) Parse(string) (*model.Session, error) {
	s.startedOnce.Do(func() { close(s.started) })
	<-s.release
	return &model.Session{Agent: model.AgentClaude, Path: s.path}, nil
}
func (s *cancelableRefreshSource) ParseContext(ctx context.Context, _ string) (*model.Session, error) {
	s.startedOnce.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return &model.Session{Agent: model.AgentClaude, Path: s.path}, nil
	}
}

func TestWatcherEmitsDebouncedAppend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher([]string{root}, WatchOptions{Debounce: 20 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case change := <-watcher.Events():
		if !reflect.DeepEqual(change.Paths, []string{path}) {
			t.Fatalf("change paths = %v, want %v", change.Paths, []string{path})
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for append event")
	}
}

func TestNewWatcherStopsInitialScanForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	watcher, err := newWatcher(ctx, []string{t.TempDir()}, WatchOptions{})
	if watcher != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("newWatcher() = %#v, %v; want nil, context canceled", watcher, err)
	}
}

func TestWalkTreeContextStopsAfterCancellation(t *testing.T) {
	root := t.TempDir()
	for index := range walkBatchEntries * 2 {
		path := filepath.Join(root, fmt.Sprintf("entry-%03d", index))
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	visited := 0
	err := walkTreeContext(ctx, root, func(_ string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited++
		if visited == 10 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("walkTreeContext() error = %v, want context canceled", err)
	}
	if visited >= walkBatchEntries {
		t.Fatalf("walkTreeContext() visited %d entries after cancellation", visited)
	}
}

func TestWalkTreeContextReportsCancellationFromLastVisit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "only-entry")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := walkTreeContext(ctx, root, func(visited string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if visited == path {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("walkTreeContext() error = %v, want context canceled from final visit", err)
	}
}

func TestWatcherRescanFindsMissedAppend(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher([]string{root}, WatchOptions{Debounce: 5 * time.Millisecond, RescanInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.watcher.Remove(root); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case change := <-watcher.Events():
		if !reflect.DeepEqual(change.Paths, []string{path}) {
			t.Fatalf("change paths = %v, want %v", change.Paths, []string{path})
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for rescan event")
	}
}

func TestFollowerReparsesChangedSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case update := <-follower.Updates():
		if len(update.Sessions) != 1 || update.Sessions[0].ID != "cached-session" {
			t.Fatalf("update sessions = %#v", update.Sessions)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for parsed session update")
	}
}

func TestFollowerCloseCancelsRefreshBeforeClosingUpdates(t *testing.T) {
	testFollowerCloseCancellation(t, func(root, path string, started, release chan struct{}) Source {
		return &cancelableRefreshSource{root: root, path: path, started: started, release: release}
	})
}

func TestFollowerCloseCancelsCodexReconciliationBeforeClosingUpdates(t *testing.T) {
	testFollowerCloseCancellation(t, func(root, path string, started, release chan struct{}) Source {
		return &cancelableSnapshotSource{root: root, path: path, started: started, release: release}
	})
}

func TestFollowerCloseCancelsRefreshFingerprintBeforeClosingUpdates(t *testing.T) {
	testFollowerCloseCancellation(t, func(root, path string, started, release chan struct{}) Source {
		return &cancelableFingerprintSource{root: root, path: path, started: started, release: release}
	})
}

func testFollowerCloseCancellation(t *testing.T, newAdapter func(string, string, chan struct{}, chan struct{}) Source) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedParse := make(chan struct{})
	releaseParse := make(chan struct{})
	adapter := newAdapter(root, path, startedParse, releaseParse)
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 5 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = follower.Close() }()
	defer close(releaseParse)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-startedParse:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for refresh parse")
	}

	closed := make(chan error, 1)
	started := time.Now()
	go func() { closed <- follower.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
			t.Fatalf("Follower.Close() took %v, want less than 100ms", elapsed)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Follower.Close() did not cancel follower work within 100ms")
	}
	select {
	case _, ok := <-follower.Updates():
		if ok {
			t.Fatal("Updates() remained open after Follower.Close()")
		}
	default:
		t.Fatal("Updates() was not closed before Follower.Close() returned")
	}
}

func TestWatcherEmitsRemovedSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher([]string{root}, WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	select {
	case change := <-watcher.Events():
		if !reflect.DeepEqual(change.RemovedPaths, []string{path}) {
			t.Fatalf("removed paths = %v, want %v", change.RemovedPaths, []string{path})
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for removal event")
	}
}

func TestFollowerDeliversRemovalOnlyUpdate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &countingSource{path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-follower.Updates():
		if len(update.Sessions) != 0 || !reflect.DeepEqual(update.RemovedPaths, []string{path}) {
			t.Fatalf("update = %#v, want removal-only update", update)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for removal update")
	}
}
