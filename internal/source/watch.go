package source

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/motoki317/agtlog/internal/model"
)

type WatchOptions struct {
	Debounce       time.Duration
	RescanInterval time.Duration
}

type Change struct {
	Paths        []string
	RemovedPaths []string
}

type SessionUpdate struct {
	Paths             []string
	RemovedPaths      []string
	Sessions          []*model.Session
	DiscoveryComplete bool
	DiscoveryErr      error
}

type followSessionIndex map[string]*model.Session
type followCheckpointIndex map[string]any

type Follower struct {
	watcher *Watcher
	updates chan SessionUpdate
	done    chan struct{}
	cancel  context.CancelFunc
	once    sync.Once
	group   sync.WaitGroup
}

type Watcher struct {
	watcher *fsnotify.Watcher
	options WatchOptions
	roots   []string
	known   map[string]string
	watched map[string]bool
	events  chan Change
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	group   sync.WaitGroup
}

func NewWatcher(roots []string, options WatchOptions) (*Watcher, error) {
	return newWatcher(context.Background(), roots, options)
}

func newWatcher(ctx context.Context, roots []string, options WatchOptions) (*Watcher, error) {
	if options.Debounce <= 0 {
		options.Debounce = 300 * time.Millisecond
	}
	if options.RescanInterval <= 0 {
		options.RescanInterval = 2 * time.Second
	}
	native, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	watcher := &Watcher{
		watcher: native,
		options: options,
		roots:   append([]string(nil), roots...),
		known:   make(map[string]string),
		watched: make(map[string]bool),
		events:  make(chan Change, 16),
		done:    make(chan struct{}),
		ctx:     watchCtx,
		cancel:  cancel,
	}
	known, err := watcher.scanFiles(true)
	if err != nil {
		cancel()
		_ = native.Close()
		return nil, err
	}
	watcher.known = known
	watcher.group.Add(1)
	go watcher.run()
	return watcher, nil
}

func (w *Watcher) Events() <-chan Change {
	return w.events
}

func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() {
		w.cancel()
		close(w.done)
		err = w.watcher.Close()
		w.group.Wait()
	})
	return err
}

func (r *Registry) Follow(ctx context.Context, options WatchOptions) (*Follower, error) {
	var roots []string
	for _, adapter := range r.sources {
		roots = append(roots, adapter.Roots()...)
	}
	followCtx, cancel := context.WithCancel(ctx)
	watcher, err := newWatcher(followCtx, roots, options)
	if err != nil {
		cancel()
		return nil, err
	}
	follower := &Follower{
		watcher: watcher,
		updates: make(chan SessionUpdate, 16),
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	follower.group.Add(1)
	go func() {
		defer follower.group.Done()
		defer close(follower.updates)
		var sessionIndex followSessionIndex
		checkpoints := make(followCheckpointIndex)
		retryPaths := make(map[string]bool)
		for {
			select {
			case <-followCtx.Done():
				return
			case <-follower.done:
				return
			case change, ok := <-watcher.Events():
				if !ok {
					return
				}
				changed := append([]string(nil), change.Paths...)
				changed = append(changed, r.removedParentPaths(followCtx, change.RemovedPaths)...)
				changed = append(changed, sortedPathSet(retryPaths)...)
				checkpoints.drop(change.RemovedPaths)
				sessions, refreshFailures := r.refresh(followCtx, changed, checkpoints)
				needsSnapshot := r.pathsUseAgent(change.RemovedPaths, model.AgentCodex)
				for _, session := range sessions {
					needsSnapshot = needsSnapshot || session.Agent == model.AgentCodex
				}
				initialized := false
				if sessionIndex == nil && needsSnapshot {
					allSessions, _, discoveryFailures, discoverErr := r.discoverSessionsWithDiagnostics(followCtx)
					if discoverErr == nil {
						sessionIndex = indexFollowSessions(allSessions)
						retryPaths = pathSet(discoveryFailures)
						initialized = true
					}
				}
				if sessionIndex != nil {
					if !initialized {
						sessionIndex.apply(sessions, change.RemovedPaths)
						updateRetryPaths(retryPaths, sessions, refreshFailures, change.RemovedPaths)
					}
					var snapshotErr error
					sessions, snapshotErr = sessionIndex.snapshot(followCtx)
					if snapshotErr != nil {
						sessions = nil
					}
				} else if needsSnapshot {
					sessions = nil
				}
				removed := r.topLevelRemoved(followCtx, change.RemovedPaths)
				if len(sessions) == 0 && len(removed) == 0 {
					continue
				}
				if followCtx.Err() != nil {
					return
				}
				select {
				case follower.updates <- SessionUpdate{Paths: change.Paths, RemovedPaths: removed, Sessions: sessions}:
				case <-followCtx.Done():
					return
				case <-follower.done:
					return
				}
			}
		}
	}()
	return follower, nil
}

func (index followCheckpointIndex) drop(paths []string) {
	for _, path := range paths {
		delete(index, path)
	}
}

func pathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, path := range paths {
		set[path] = true
	}
	return set
}

func sortedPathSet(paths map[string]bool) []string {
	sorted := make([]string, 0, len(paths))
	for path := range paths {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)
	return sorted
}

func updateRetryPaths(retryPaths map[string]bool, refreshed []*model.Session, failedPaths, removedPaths []string) {
	for _, session := range refreshed {
		if session != nil {
			delete(retryPaths, session.Path)
		}
	}
	for _, path := range failedPaths {
		retryPaths[path] = true
	}
	for _, path := range removedPaths {
		delete(retryPaths, path)
	}
}

func indexFollowSessions(sessions []*model.Session) followSessionIndex {
	index := make(followSessionIndex, len(sessions))
	for _, session := range sessions {
		if session != nil {
			index[session.Path] = session
		}
	}
	return index
}

func (index followSessionIndex) apply(refreshed []*model.Session, removedPaths []string) {
	for _, path := range removedPaths {
		delete(index, path)
	}
	for _, session := range refreshed {
		if session != nil {
			index[session.Path] = session
		}
	}
}

func (index followSessionIndex) snapshot(ctx context.Context) ([]*model.Session, error) {
	paths := make([]string, 0, len(index))
	for path := range index {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	sessions := make([]*model.Session, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sessions = append(sessions, index[path])
	}
	return buildSessionSnapshotContext(ctx, sessions)
}

func (r *Registry) pathsUseAgent(paths []string, agent model.AgentKind) bool {
	for _, path := range paths {
		for _, adapter := range r.sources {
			if adapter.Agent() == agent && insideAnyRoot(path, adapter.Roots()) {
				return true
			}
		}
	}
	return false
}

func (r *Registry) topLevelRemoved(ctx context.Context, paths []string) []string {
	var removed []string
	for _, path := range paths {
		for _, adapter := range r.sources {
			if !insideAnyRoot(path, adapter.Roots()) {
				continue
			}
			affected, err := affectedPath(ctx, adapter, path)
			if err != nil {
				return removed
			}
			if affected == path {
				removed = append(removed, path)
				if r.options.CacheDir != "" && r.cacheDirSafe() {
					r.removeCached(adapter, path)
				}
			}
			break
		}
	}
	sort.Strings(removed)
	return removed
}

func (r *Registry) removedParentPaths(ctx context.Context, paths []string) []string {
	seen := make(map[string]bool)
	var parents []string
	for _, path := range paths {
		for _, adapter := range r.sources {
			if !insideAnyRoot(path, adapter.Roots()) {
				continue
			}
			affected, err := affectedPath(ctx, adapter, path)
			if err != nil {
				return parents
			}
			if affected != path && !seen[affected] {
				parents = append(parents, affected)
				seen[affected] = true
			}
			break
		}
	}
	sort.Strings(parents)
	return parents
}

func (f *Follower) Updates() <-chan SessionUpdate {
	return f.updates
}

func (f *Follower) Close() error {
	var err error
	f.once.Do(func() {
		f.cancel()
		close(f.done)
		err = f.watcher.Close()
		f.group.Wait()
	})
	return err
}

type affectedPathMapper interface {
	AffectedPath(string) string
}

type affectedPathContextMapper interface {
	AffectedPathContext(context.Context, string) (string, error)
}

func affectedPath(ctx context.Context, adapter Source, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if mapper, ok := adapter.(affectedPathContextMapper); ok {
		return mapper.AffectedPathContext(ctx, path)
	}
	if mapper, ok := adapter.(affectedPathMapper); ok {
		return mapper.AffectedPath(path), nil
	}
	return path, nil
}

func (r *Registry) refresh(ctx context.Context, changedPaths []string, checkpoints followCheckpointIndex) ([]*model.Session, []string) {
	type target struct {
		adapter Source
		path    string
	}
	seen := make(map[string]bool)
	var targets []target
	for _, changedPath := range changedPaths {
		for _, adapter := range r.sources {
			if !insideAnyRoot(changedPath, adapter.Roots()) {
				continue
			}
			affectedPath, err := affectedPath(ctx, adapter, changedPath)
			if err != nil {
				return nil, nil
			}
			key := string(adapter.Agent()) + "\x00" + affectedPath
			if !seen[key] {
				targets = append(targets, target{adapter: adapter, path: affectedPath})
				seen[key] = true
			}
			break
		}
	}

	var sessions []*model.Session
	var failedPaths []string
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		before, fingerprintErr := sourceFingerprintContext(ctx, target.adapter, target.path)
		if ctx.Err() != nil {
			break
		}
		var session *model.Session
		var err error
		if resumable, ok := target.adapter.(resumableContextParser); ok {
			var next any
			session, next, err = r.parseAndCacheResumableContext(ctx, resumable, target.path, before, fingerprintErr == nil, checkpoints[target.path])
			if err == nil && next != nil {
				checkpoints[target.path] = next
			} else {
				delete(checkpoints, target.path)
			}
		} else {
			session, _, err = r.parseAndCacheContext(ctx, target.adapter, target.path, before, fingerprintErr == nil)
			delete(checkpoints, target.path)
		}
		if err != nil {
			failedPaths = append(failedPaths, target.path)
			continue
		}
		if ctx.Err() != nil {
			break
		}
		sessions = append(sessions, session)
	}
	return sessions, failedPaths
}

func insideAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *Watcher) addTree(root string) error {
	err := walkTreeContext(w.ctx, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && !w.watched[path] {
			if err := w.watcher.Add(path); err != nil {
				return err
			}
			w.watched[path] = true
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (w *Watcher) run() {
	defer w.group.Done()
	defer close(w.events)
	pending := make(map[string]bool)
	var timer *time.Timer
	var timerC <-chan time.Time
	rescan := time.NewTicker(w.options.RescanInterval)
	defer rescan.Stop()
	queue := func(path string) {
		if filepath.Ext(path) != ".jsonl" {
			return
		}
		_, statErr := os.Stat(path)
		pending[path] = errors.Is(statErr, os.ErrNotExist)
		if timer == nil {
			timer = time.NewTimer(w.options.Debounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.options.Debounce)
		}
		if fingerprint, err := fileFingerprint(path); err == nil {
			w.known[path] = fingerprint
		} else {
			delete(w.known, path)
		}
		timerC = timer.C
	}
	flush := func() {
		var paths, removed []string
		for path, isRemoved := range pending {
			if isRemoved {
				removed = append(removed, path)
			} else {
				paths = append(paths, path)
			}
			delete(pending, path)
		}
		sort.Strings(paths)
		sort.Strings(removed)
		if len(paths) > 0 || len(removed) > 0 {
			select {
			case w.events <- Change{Paths: paths, RemovedPaths: removed}:
			case <-w.done:
			}
		}
		timerC = nil
	}

	for {
		select {
		case <-w.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = w.addTree(event.Name)
					continue
				}
			}
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				w.forgetTree(event.Name)
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove) {
				queue(event.Name)
			}
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		case <-timerC:
			flush()
		case <-rescan.C:
			current, err := w.scanFiles(true)
			if err != nil {
				return
			}
			for path, fingerprint := range current {
				if w.known[path] != fingerprint {
					queue(path)
				}
			}
			for path := range w.known {
				if _, exists := current[path]; !exists {
					queue(path)
				}
			}
			w.known = current
		}
	}
}

func (w *Watcher) scanFiles(addWatches bool) (map[string]string, error) {
	files := make(map[string]string)
	for _, root := range w.roots {
		err := walkTreeContext(w.ctx, root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if addWatches && !w.watched[path] {
					if err := w.watcher.Add(path); err == nil {
						w.watched[path] = true
					}
				}
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}
			if fingerprint, err := fileFingerprint(path); err == nil {
				files[path] = fingerprint
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

const walkBatchEntries = 128

func walkTreeContext(ctx context.Context, root string, visit fs.WalkDirFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		return visit(root, nil, err)
	}
	return walkEntryContext(ctx, root, fs.FileInfoToDirEntry(info), visit)
}

func walkEntryContext(ctx context.Context, path string, entry fs.DirEntry, visit fs.WalkDirFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := visit(path, entry, nil); err != nil {
		if errors.Is(err, fs.SkipDir) && entry.IsDir() {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !entry.IsDir() {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return visit(path, entry, err)
	}
	defer func() { _ = directory.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(walkBatchEntries)
		for _, child := range entries {
			if err := walkEntryContext(ctx, filepath.Join(path, child.Name()), child, visit); err != nil {
				if errors.Is(err, fs.SkipDir) {
					continue
				}
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		}
		if readErr != nil {
			return visit(path, entry, readErr)
		}
	}
}

func (w *Watcher) forgetTree(root string) {
	for path := range w.watched {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			delete(w.watched, path)
		}
	}
}
