package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

type indexedFollowSource struct {
	root                string
	mu                  sync.Mutex
	sessions            map[string]*model.Session
	discoverReplacement *model.Session
	discovers           int
	parses              map[string]int
	failParses          map[string]int
}

type resumableFollowSource struct {
	root       string
	path       string
	inputs     []any
	failNext   bool
	nilNext    bool
	fullParses int
}

type resumableFollowCheckpoint struct {
	generation int
}

func (s *resumableFollowSource) Agent() model.AgentKind   { return model.AgentCodex }
func (s *resumableFollowSource) Roots() []string          { return []string{s.root} }
func (s *resumableFollowSource) CacheFingerprint() string { return "resumable-follow-v1" }
func (s *resumableFollowSource) Discover(context.Context) ([]string, error) {
	return []string{s.path}, nil
}
func (s *resumableFollowSource) ParseContext(context.Context, string) (*model.Session, error) {
	s.fullParses++
	return &model.Session{ID: "full", Agent: model.AgentCodex, Path: s.path}, nil
}
func (s *resumableFollowSource) ParseResumableContext(ctx context.Context, path string, checkpoint any) (*model.Session, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.inputs = append(s.inputs, checkpoint)
	if s.failNext {
		s.failNext = false
		return nil, nil, errors.New("transient resumable failure")
	}
	if s.nilNext {
		s.nilNext = false
		return &model.Session{ID: "resume-without-checkpoint", Agent: model.AgentCodex, Path: path}, nil, nil
	}
	next := &resumableFollowCheckpoint{generation: len(s.inputs)}
	return &model.Session{ID: fmt.Sprintf("resume-%d", next.generation), Agent: model.AgentCodex, Path: path}, next, nil
}
func (s *resumableFollowSource) Reprice(*model.Session) {}

func (s *indexedFollowSource) Agent() model.AgentKind   { return model.AgentCodex }
func (s *indexedFollowSource) Roots() []string          { return []string{s.root} }
func (s *indexedFollowSource) CacheFingerprint() string { return "indexed-follow-v1" }
func (s *indexedFollowSource) Discover(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discovers++
	if s.discoverReplacement != nil {
		s.sessions[s.discoverReplacement.Path] = s.discoverReplacement
		s.discoverReplacement = nil
	}
	paths := make([]string, 0, len(s.sessions))
	for path := range s.sessions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
func (s *indexedFollowSource) ParseContext(ctx context.Context, path string) (*model.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parses[path]++
	if s.failParses[path] > 0 {
		s.failParses[path]--
		return nil, errors.New("transient parse failure")
	}
	session := s.sessions[path]
	if session == nil {
		return nil, os.ErrNotExist
	}
	copy := *session
	copy.Subagents = append([]*model.Session(nil), session.Subagents...)
	return &copy, nil
}
func (s *indexedFollowSource) Reprice(*model.Session) {}
func (s *indexedFollowSource) set(session *model.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.Path] = session
}
func (s *indexedFollowSource) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, path)
}
func (s *indexedFollowSource) counts(path string) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discovers, s.parses[path]
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
func (s *cancelableFingerprintSource) Reprice(*model.Session) {}
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
func (s *cancelableSnapshotSource) Reprice(*model.Session) {}

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
func (s *cancelableRefreshSource) Reprice(*model.Session) {}

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

func TestRefreshMaintainsOpaqueResumableCheckpoints(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &resumableFollowSource{root: root, path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1, CacheDir: t.TempDir()})
	checkpoints := make(followCheckpointIndex)

	sessions, failed := registry.refresh(context.Background(), []string{path}, checkpoints)
	if len(sessions) != 1 || len(failed) != 0 || len(adapter.inputs) != 1 || adapter.inputs[0] != nil || adapter.fullParses != 0 {
		t.Fatalf("first refresh = sessions %#v, failed %v, inputs %#v, full parses %d", sessions, failed, adapter.inputs, adapter.fullParses)
	}
	first, ok := checkpoints[path].(*resumableFollowCheckpoint)
	if !ok {
		t.Fatalf("first checkpoint = %#v", checkpoints[path])
	}
	fingerprint, err := sourceFingerprint(adapter, path)
	if err != nil {
		t.Fatal(err)
	}
	if cached, _, ok := registry.loadCached(adapter, path, fingerprint); !ok || cached.ID != "resume-1" {
		t.Fatalf("resumable cache write-through = session %#v, hit %t", cached, ok)
	}

	sessions, failed = registry.refresh(context.Background(), []string{path}, checkpoints)
	if len(sessions) != 1 || len(failed) != 0 || adapter.inputs[1] != first {
		t.Fatalf("second refresh = sessions %#v, failed %v, checkpoint input %#v", sessions, failed, adapter.inputs[1])
	}

	adapter.failNext = true
	sessions, failed = registry.refresh(context.Background(), []string{path}, checkpoints)
	if len(sessions) != 0 || !reflect.DeepEqual(failed, []string{path}) {
		t.Fatalf("failed refresh = sessions %#v, failed %v", sessions, failed)
	}
	if _, exists := checkpoints[path]; exists {
		t.Fatal("failed refresh retained its checkpoint")
	}

	sessions, failed = registry.refresh(context.Background(), []string{path}, checkpoints)
	if len(sessions) != 1 || len(failed) != 0 || adapter.inputs[len(adapter.inputs)-1] != nil {
		t.Fatalf("retry refresh = sessions %#v, failed %v, checkpoint input %#v", sessions, failed, adapter.inputs[len(adapter.inputs)-1])
	}
	seeded := checkpoints[path]
	adapter.nilNext = true
	sessions, failed = registry.refresh(context.Background(), []string{path}, checkpoints)
	if len(sessions) != 1 || len(failed) != 0 || adapter.inputs[len(adapter.inputs)-1] != seeded {
		t.Fatalf("checkpointless success = sessions %#v, failed %v, checkpoint input %#v", sessions, failed, adapter.inputs[len(adapter.inputs)-1])
	}
	if _, exists := checkpoints[path]; exists {
		t.Fatal("checkpointless success retained its previous checkpoint")
	}
	checkpoints[path] = seeded
	checkpoints.drop([]string{path})
	if _, exists := checkpoints[path]; exists {
		t.Fatal("removed path retained its checkpoint")
	}
}

func TestFollowerDropsResumableCheckpointWhenPathIsRemoved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &resumableFollowSource{root: root, path: path}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	appendFollowLog(t, path)
	_ = nextFollowerUpdate(t, follower)
	if len(adapter.inputs) == 0 || adapter.inputs[len(adapter.inputs)-1] != nil {
		t.Fatalf("first refresh checkpoint input = %#v, want nil", adapter.inputs)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removed := nextFollowerUpdate(t, follower)
	if !reflect.DeepEqual(removed.RemovedPaths, []string{path}) {
		t.Fatalf("removed paths = %v, want %v", removed.RemovedPaths, []string{path})
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = nextFollowerUpdate(t, follower)
	if adapter.inputs[len(adapter.inputs)-1] != nil {
		t.Fatalf("recreated path received stale checkpoint %#v", adapter.inputs[len(adapter.inputs)-1])
	}
}

func TestFollowerReusesIndexedSessionsForCodexSnapshots(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "root.jsonl")
	childPath := filepath.Join(root, "child.jsonl")
	for _, path := range []string{parentPath, childPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parentUpdated := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	childUpdated := parentUpdated.Add(time.Minute)
	adapter := &indexedFollowSource{
		root: root,
		sessions: map[string]*model.Session{
			parentPath: {ID: "root", Agent: model.AgentCodex, Path: parentPath, Title: "root", UpdatedAt: parentUpdated, Cost: model.Cost{USD: 1}},
			childPath:  {ID: "child", ParentID: "root", Agent: model.AgentCodex, Path: childPath, Title: "child", UpdatedAt: childUpdated, Cost: model.Cost{USD: 2}},
		},
		discoverReplacement: &model.Session{
			ID: "root", Agent: model.AgentCodex, Path: parentPath, Title: "discovered root", UpdatedAt: parentUpdated, Cost: model.Cost{USD: 1},
		},
		parses: make(map[string]int),
	}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 2})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	appendFollowLog(t, parentPath)
	update := nextFollowerUpdate(t, follower)
	parent := sessionWithID(update.Sessions, "root")
	if parent == nil || parent.Title != "discovered root" || len(parent.Subagents) != 1 || parent.Subagents[0].ID != "child" {
		t.Fatalf("first indexed snapshot = %#v, want child nested under root", update.Sessions)
	}
	if !parent.UpdatedAt.Equal(childUpdated) || parent.TotalCost().USD != 3 {
		t.Fatalf("first root roll-up = updated %v, cost %v", parent.UpdatedAt, parent.TotalCost())
	}

	adapter.set(&model.Session{
		ID: "root", Agent: model.AgentCodex, Path: parentPath, Title: "updated root", UpdatedAt: parentUpdated,
		Cost: model.Cost{USD: 1}, Events: []model.Event{{Kind: model.EventAssistantText, Text: "fresh detail"}},
	})
	appendFollowLog(t, parentPath)
	update = nextFollowerUpdate(t, follower)
	parent = sessionWithID(update.Sessions, "root")
	if parent == nil || parent.Title != "updated root" || len(parent.Events) != 1 || len(parent.Subagents) != 1 {
		t.Fatalf("repeated indexed snapshot = %#v, want refreshed root with one child", update.Sessions)
	}
	if !parent.UpdatedAt.Equal(childUpdated) {
		t.Fatalf("repeated root UpdatedAt = %v, want stable child roll-up %v", parent.UpdatedAt, childUpdated)
	}
	if discovers, childParses := adapter.counts(childPath); discovers != 1 || childParses != 1 {
		t.Fatalf("repeated snapshot calls = %d discoveries, %d child parses, want 1 and 1", discovers, childParses)
	}

	newPath := filepath.Join(root, "new-root.jsonl")
	newUpdated := childUpdated.Add(time.Minute)
	adapter.set(&model.Session{ID: "new-root", Agent: model.AgentCodex, Path: newPath, Title: "new root", UpdatedAt: newUpdated, Cost: model.Cost{USD: 4}})
	if err := os.WriteFile(newPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	update = nextFollowerUpdate(t, follower)
	if sessionWithID(update.Sessions, "new-root") == nil {
		t.Fatalf("new session snapshot = %#v, want new root", update.Sessions)
	}
	if discovers, newParses := adapter.counts(newPath); discovers != 1 || newParses != 1 {
		t.Fatalf("new session calls = %d discoveries, %d parses, want 1 and 1", discovers, newParses)
	}

	adapter.remove(childPath)
	if err := os.Remove(childPath); err != nil {
		t.Fatal(err)
	}
	update = nextFollowerUpdate(t, follower)
	parent = sessionWithID(update.Sessions, "root")
	if parent == nil || len(parent.Subagents) != 0 || !parent.UpdatedAt.Equal(parentUpdated) {
		t.Fatalf("post-removal root = %#v, want child removed and parser timestamp restored", parent)
	}
	if !reflect.DeepEqual(update.RemovedPaths, []string{childPath}) {
		t.Fatalf("removed paths = %v, want %v", update.RemovedPaths, []string{childPath})
	}
	if discovers, _ := adapter.counts(childPath); discovers != 1 {
		t.Fatalf("post-removal discoveries = %d, want one initial discovery", discovers)
	}

	adapter.remove(newPath)
	if err := os.Remove(newPath); err != nil {
		t.Fatal(err)
	}
	update = nextFollowerUpdate(t, follower)
	if sessionWithID(update.Sessions, "new-root") != nil || !reflect.DeepEqual(update.RemovedPaths, []string{newPath}) {
		t.Fatalf("removed root update = %#v, want new root absent and its path removed", update)
	}
	if discovers, _ := adapter.counts(newPath); discovers != 1 {
		t.Fatalf("post-root-removal discoveries = %d, want one initial discovery", discovers)
	}
}

func TestFollowerRetriesFailedInitialIndexPaths(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "root.jsonl")
	childPath := filepath.Join(root, "child.jsonl")
	for _, path := range []string{parentPath, childPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &indexedFollowSource{
		root: root,
		sessions: map[string]*model.Session{
			parentPath: {ID: "root", Agent: model.AgentCodex, Path: parentPath},
			childPath:  {ID: "child", ParentID: "root", Agent: model.AgentCodex, Path: childPath},
		},
		parses:     make(map[string]int),
		failParses: map[string]int{childPath: 1},
	}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 1})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	appendFollowLog(t, parentPath)
	first := nextFollowerUpdate(t, follower)
	if parent := sessionWithID(first.Sessions, "root"); parent == nil || len(parent.Subagents) != 0 {
		t.Fatalf("initial partial snapshot = %#v, want root without failed child", first.Sessions)
	}

	appendFollowLog(t, parentPath)
	second := nextFollowerUpdate(t, follower)
	parent := sessionWithID(second.Sessions, "root")
	if parent == nil || len(parent.Subagents) != 1 || parent.Subagents[0].ID != "child" {
		t.Fatalf("retried snapshot = %#v, want recovered child nested under root", second.Sessions)
	}
	if discovers, childParses := adapter.counts(childPath); discovers != 1 || childParses != 2 {
		t.Fatalf("retry calls = %d discoveries, %d child parses, want 1 and 2", discovers, childParses)
	}
}

func TestFollowerRetriesFailedRefreshBeforeInitialIndex(t *testing.T) {
	codexRoot := t.TempDir()
	codexPath := filepath.Join(codexRoot, "rollout.jsonl")
	claudeRoot := t.TempDir()
	claudePath := filepath.Join(claudeRoot, "session.jsonl")
	for _, path := range []string{codexPath, claudePath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	codex := &indexedFollowSource{
		root: codexRoot,
		sessions: map[string]*model.Session{
			codexPath: {ID: "rollout", Agent: model.AgentCodex, Path: codexPath},
		},
		parses:     make(map[string]int),
		failParses: map[string]int{codexPath: 1},
	}
	claude := &countingSource{path: claudePath}
	registry := NewRegistry([]Source{codex, claude}, Options{Workers: 1})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	follower.watcher.events <- Change{Paths: []string{codexPath}}
	follower.watcher.events <- Change{Paths: []string{claudePath}}
	update := nextFollowerUpdate(t, follower)
	if !reflect.DeepEqual(update.Paths, []string{claudePath}) {
		t.Fatalf("first update paths = %v, want unrelated path %v", update.Paths, []string{claudePath})
	}
	if sessionWithID(update.Sessions, "rollout") == nil {
		t.Fatalf("retried snapshot = %#v, want recovered rollout", update.Sessions)
	}
	if _, parses := codex.counts(codexPath); parses != 3 {
		t.Fatalf("rollout parses = %d, want failed refresh, retry, and discovery", parses)
	}
}

func TestFollowerInitialDiscoverySupersedesRefreshFailures(t *testing.T) {
	codexRoot := t.TempDir()
	failedPath := filepath.Join(codexRoot, "failed.jsonl")
	changedPath := filepath.Join(codexRoot, "changed.jsonl")
	claudeRoot := t.TempDir()
	claudePath := filepath.Join(claudeRoot, "session.jsonl")
	for _, path := range []string{failedPath, changedPath, claudePath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	codex := &indexedFollowSource{
		root: codexRoot,
		sessions: map[string]*model.Session{
			failedPath:  {ID: "failed", Agent: model.AgentCodex, Path: failedPath},
			changedPath: {ID: "changed", Agent: model.AgentCodex, Path: changedPath},
		},
		parses:     make(map[string]int),
		failParses: map[string]int{failedPath: 1},
	}
	claude := &countingSource{path: claudePath}
	registry := NewRegistry([]Source{codex, claude}, Options{Workers: 1})
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: 10 * time.Millisecond, RescanInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	follower.watcher.events <- Change{Paths: []string{failedPath, changedPath}}
	first := nextFollowerUpdate(t, follower)
	if sessionWithID(first.Sessions, "failed") == nil {
		t.Fatalf("initial snapshot = %#v, want failed refresh recovered by discovery", first.Sessions)
	}
	if _, parses := codex.counts(failedPath); parses != 2 {
		t.Fatalf("initial failed path parses = %d, want failed refresh plus discovery", parses)
	}

	follower.watcher.events <- Change{Paths: []string{claudePath}}
	second := nextFollowerUpdate(t, follower)
	if sessionWithID(second.Sessions, "failed") == nil {
		t.Fatalf("later snapshot = %#v, want discovered session retained", second.Sessions)
	}
	if _, parses := codex.counts(failedPath); parses != 2 {
		t.Fatalf("later failed path parses = %d, want no retry after successful discovery", parses)
	}
}

func appendFollowLog(t *testing.T, path string) {
	t.Helper()
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
}

func nextFollowerUpdate(t *testing.T, follower *Follower) SessionUpdate {
	t.Helper()
	select {
	case update, ok := <-follower.Updates():
		if !ok {
			t.Fatal("follower updates closed")
		}
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for follower update")
		return SessionUpdate{}
	}
}

func sessionWithID(sessions []*model.Session, id string) *model.Session {
	for _, session := range sessions {
		if session.ID == id {
			return session
		}
	}
	return nil
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
