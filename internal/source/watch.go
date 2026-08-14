package source

import (
	"context"
	"errors"
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
	Paths        []string
	RemovedPaths []string
	Sessions     []*model.Session
}

type Follower struct {
	watcher *Watcher
	updates chan SessionUpdate
	done    chan struct{}
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
	once    sync.Once
	group   sync.WaitGroup
}

func NewWatcher(roots []string, options WatchOptions) (*Watcher, error) {
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
	watcher := &Watcher{
		watcher: native,
		options: options,
		roots:   append([]string(nil), roots...),
		known:   make(map[string]string),
		watched: make(map[string]bool),
		events:  make(chan Change, 16),
		done:    make(chan struct{}),
	}
	watcher.known = watcher.scanFiles(true)
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
	watcher, err := NewWatcher(roots, options)
	if err != nil {
		return nil, err
	}
	follower := &Follower{
		watcher: watcher,
		updates: make(chan SessionUpdate, 16),
		done:    make(chan struct{}),
	}
	follower.group.Add(1)
	go func() {
		defer follower.group.Done()
		defer close(follower.updates)
		for {
			select {
			case <-ctx.Done():
				return
			case <-follower.done:
				return
			case change, ok := <-watcher.Events():
				if !ok {
					return
				}
				changed := append([]string(nil), change.Paths...)
				changed = append(changed, r.removedParentPaths(change.RemovedPaths)...)
				sessions := r.refresh(ctx, changed)
				needsSnapshot := r.pathsUseAgent(change.RemovedPaths, model.AgentCodex)
				for _, session := range sessions {
					needsSnapshot = needsSnapshot || session.Agent == model.AgentCodex
				}
				if needsSnapshot {
					if snapshot, discoverErr := r.Discover(ctx); discoverErr == nil {
						sessions = snapshot
					} else {
						sessions = nil
					}
				}
				removed := r.topLevelRemoved(change.RemovedPaths)
				if len(sessions) == 0 && len(removed) == 0 {
					continue
				}
				select {
				case follower.updates <- SessionUpdate{Paths: change.Paths, RemovedPaths: removed, Sessions: sessions}:
				case <-ctx.Done():
					return
				case <-follower.done:
					return
				}
			}
		}
	}()
	return follower, nil
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

func (r *Registry) topLevelRemoved(paths []string) []string {
	var removed []string
	for _, path := range paths {
		for _, adapter := range r.sources {
			if !insideAnyRoot(path, adapter.Roots()) {
				continue
			}
			affected := path
			if mapper, ok := adapter.(affectedPathMapper); ok {
				affected = mapper.AffectedPath(path)
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

func (r *Registry) removedParentPaths(paths []string) []string {
	seen := make(map[string]bool)
	var parents []string
	for _, path := range paths {
		for _, adapter := range r.sources {
			if !insideAnyRoot(path, adapter.Roots()) {
				continue
			}
			if mapper, ok := adapter.(affectedPathMapper); ok {
				affected := mapper.AffectedPath(path)
				if affected != path && !seen[affected] {
					parents = append(parents, affected)
					seen[affected] = true
				}
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
		close(f.done)
		err = f.watcher.Close()
		f.group.Wait()
	})
	return err
}

type affectedPathMapper interface {
	AffectedPath(string) string
}

func (r *Registry) refresh(ctx context.Context, changedPaths []string) []*model.Session {
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
			affectedPath := changedPath
			if mapper, ok := adapter.(affectedPathMapper); ok {
				affectedPath = mapper.AffectedPath(changedPath)
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
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		// Full re-parse per change; add offset-incremental parsing if large-session refresh lags.
		before, fingerprintErr := sourceFingerprint(target.adapter, target.path)
		session, _, err := r.parseAndCache(target.adapter, target.path, before, fingerprintErr == nil)
		if err != nil {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
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
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
			current := w.scanFiles(true)
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

func (w *Watcher) scanFiles(addWatches bool) map[string]string {
	files := make(map[string]string)
	for _, root := range w.roots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
	}
	return files
}

func (w *Watcher) forgetTree(root string) {
	for path := range w.watched {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			delete(w.watched, path)
		}
	}
}
