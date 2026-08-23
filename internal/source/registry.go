package source

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

var (
	ErrRecordChanged = errors.New("record source changed")
	ErrRecordRead    = errors.New("record read failed")
)

func ReadRecord(ctx context.Context, ref model.RecordRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Path == "" || ref.Offset < 0 || ref.Length <= 0 || ref.Length > jsonl.MaxLineBytes {
		return nil, fmt.Errorf("%w: invalid record reference", ErrRecordRead)
	}
	info, err := os.Lstat(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRecordRead, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: source path is not a regular file", ErrRecordRead)
	}
	file, err := os.Open(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRecordRead, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRecordRead, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: source path is not a regular file", ErrRecordRead)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, ErrRecordChanged
	}
	record := make([]byte, int(ref.Length))
	if _, err := file.ReadAt(record, ref.Offset); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRecordRead, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sha256.Sum256(record) != ref.Digest {
		return nil, ErrRecordChanged
	}
	return record, nil
}

func (r *Registry) LoadDetail(ctx context.Context, session *model.Session) error {
	return r.loadDetail(ctx, session, false)
}

func (r *Registry) LoadNodeDetail(ctx context.Context, session *model.Session) error {
	return r.loadDetail(ctx, session, true)
}

func (r *Registry) loadDetail(ctx context.Context, session *model.Session, nodeOnly bool) error {
	if session == nil {
		return errors.New("session is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := session.Path
	if separator := strings.IndexByte(path, '#'); separator >= 0 {
		path = path[:separator]
	}
	for _, adapter := range r.sources {
		loader, ok := adapter.(detailLoader)
		if !ok || adapter.Agent() != session.Agent {
			continue
		}
		if insideAnyRoot(path, adapter.Roots()) {
			if nodeOnly {
				if nodeLoader, supported := adapter.(nodeDetailLoader); supported {
					return nodeLoader.LoadNodeEvents(ctx, session)
				}
			}
			return loader.LoadEvents(ctx, session)
		}
	}
	return errors.New("detail loader unavailable")
}

type detailLoader interface {
	LoadEvents(context.Context, *model.Session) error
}

type nodeDetailLoader interface {
	LoadNodeEvents(context.Context, *model.Session) error
}

func (r *Registry) ReleaseDetail(session *model.Session) {
	var release func(*model.Session)
	release = func(current *model.Session) {
		if current == nil {
			return
		}
		current.Events = nil
		for _, child := range current.Subagents {
			release(child)
		}
	}
	release(session)
}

type Options struct {
	Workers  int
	CacheDir string
	Progress func(completed, total int)
}

type Registry struct {
	sources       []Source
	options       Options
	followMirrors *mirrorFollowState
}

type DiscoveryDiagnostic struct {
	Agent model.AgentKind
	Path  string
	Err   error
}

func NewRegistry(sources []Source, options Options) *Registry {
	registry := &Registry{sources: sources, options: options, followMirrors: &mirrorFollowState{}}
	if options.CacheDir != "" {
		if resolved, ok := ResolveCacheDir(options.CacheDir, registry.Roots()); ok {
			options.CacheDir = resolved
		} else {
			options.CacheDir = ""
		}
	}
	registry.options = options
	registry.sweepStaleCacheTemps(time.Now())
	return registry
}

func (r *Registry) Roots() []string {
	var roots []string
	seen := make(map[string]bool)
	for _, adapter := range r.sources {
		for _, root := range adapter.Roots() {
			root = filepath.Clean(root)
			if !seen[root] {
				seen[root] = true
				roots = append(roots, root)
			}
		}
	}
	return roots
}

func (r *Registry) WithDiscoveryProgress(progress func(completed, total int)) *Registry {
	copy := *r
	copy.options.Progress = progress
	return &copy
}

func (r *Registry) Discover(ctx context.Context) ([]*model.Session, error) {
	sessions, _, err := r.DiscoverWithDiagnostics(ctx)
	return sessions, err
}

func (r *Registry) DiscoverWithDiagnostics(ctx context.Context) ([]*model.Session, []DiscoveryDiagnostic, error) {
	return r.discoverWithDiagnostics(ctx)
}

func (r *Registry) discoverWithDiagnostics(ctx context.Context) ([]*model.Session, []DiscoveryDiagnostic, error) {
	allSessions, diagnostics, _, err := r.discoverSessionsWithDiagnostics(ctx)
	if err != nil {
		return nil, nil, err
	}
	sessions, mirrors, err := buildSessionSnapshotWithCandidatesContext(ctx, allSessions)
	if err != nil {
		return nil, nil, err
	}
	if r.followMirrors != nil {
		r.followMirrors.publish(allSessions, sessions, mirrors)
	}
	return sessions, diagnostics, nil
}

func (r *Registry) discoverSessionsWithDiagnostics(ctx context.Context) ([]*model.Session, []DiscoveryDiagnostic, []string, error) {
	type job struct {
		source Source
		path   string
	}
	type result struct {
		session     *model.Session
		diagnostics []DiscoveryDiagnostic
		failedPath  string
	}
	var pending []job
	for _, adapter := range r.sources {
		paths, err := adapter.Discover(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, path := range paths {
			pending = append(pending, job{source: adapter, path: path})
		}
	}
	if r.options.Progress != nil {
		r.options.Progress(0, len(pending))
	}
	cacheRoot, _ := r.openOrCreateCacheRoot()
	if cacheRoot != nil {
		defer func() { _ = cacheRoot.Close() }()
	}

	workers := r.options.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, max(1, len(pending)))
	jobs := make(chan job, len(pending))
	results := make(chan result, len(pending))
	for _, item := range pending {
		jobs <- item
	}
	close(jobs)

	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for item := range jobs {
				if ctx.Err() != nil {
					return
				}
				session, diagnostics, err := r.discoverSessionWithCacheContext(ctx, cacheRoot, item.source, item.path)
				if err != nil {
					diagnostics = append(diagnostics, DiscoveryDiagnostic{Agent: item.source.Agent(), Path: item.path, Err: err})
					results <- result{diagnostics: diagnostics, failedPath: item.path}
					continue
				}
				results <- result{session: session, diagnostics: diagnostics}
			}
		}()
	}
	go func() {
		group.Wait()
		close(results)
	}()

	allSessions := make([]*model.Session, 0, len(pending))
	diagnostics := make([]DiscoveryDiagnostic, 0)
	failedPaths := make([]string, 0)
	completed := 0
	for item := range results {
		completed++
		if r.options.Progress != nil {
			r.options.Progress(completed, len(pending))
		}
		diagnostics = append(diagnostics, item.diagnostics...)
		if item.failedPath != "" {
			failedPaths = append(failedPaths, item.failedPath)
		}
		if item.session != nil {
			allSessions = append(allSessions, item.session)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Agent != diagnostics[j].Agent {
			return diagnostics[i].Agent < diagnostics[j].Agent
		}
		return diagnostics[i].Path < diagnostics[j].Path
	})
	sort.Strings(failedPaths)
	return allSessions, diagnostics, failedPaths, nil
}

func buildSessionSnapshotContext(ctx context.Context, parsedSessions []*model.Session) ([]*model.Session, error) {
	sessions, _, err := buildSessionSnapshotWithCandidatesContext(ctx, parsedSessions)
	return sessions, err
}

func buildSessionSnapshotWithCandidatesContext(ctx context.Context, parsedSessions []*model.Session) ([]*model.Session, mirrorCandidates, error) {
	parsedSessions, mirrors, err := collapseMirroredSessionsWithCandidatesContext(ctx, parsedSessions)
	if err != nil {
		return nil, mirrorCandidates{}, err
	}
	allSessions := make([]*model.Session, 0, len(parsedSessions))
	for _, session := range parsedSessions {
		copied, err := copySessionTreeContext(ctx, session)
		if err != nil {
			return nil, mirrorCandidates{}, err
		}
		allSessions = append(allSessions, copied)
	}
	linkedChildren, err := linkSessionGraphsContext(ctx, allSessions)
	if err != nil {
		return nil, mirrorCandidates{}, err
	}
	sessions := make([]*model.Session, 0, len(allSessions))
	for _, session := range allSessions {
		if err := ctx.Err(); err != nil {
			return nil, mirrorCandidates{}, err
		}
		if linkedChildren[session] {
			continue
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
		}
		if sessions[i].Agent != sessions[j].Agent {
			return sessions[i].Agent < sessions[j].Agent
		}
		return sessions[i].ID < sessions[j].ID
	})
	if err := attributeOwnershipContext(ctx, sessions); err != nil {
		return nil, mirrorCandidates{}, err
	}
	return sessions, mirrors, nil
}

func copySessionTreeContext(ctx context.Context, session *model.Session) (*model.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	copy := *session
	if len(session.Subagents) == 0 {
		return &copy, nil
	}
	copy.Subagents = make([]*model.Session, 0, len(session.Subagents))
	for _, child := range session.Subagents {
		copiedChild, err := copySessionTreeContext(ctx, child)
		if err != nil {
			return nil, err
		}
		copy.Subagents = append(copy.Subagents, copiedChild)
	}
	return &copy, nil
}

func linkSessionGraphs(sessions []*model.Session) map[*model.Session]bool {
	linked, _ := linkSessionGraphsContext(context.Background(), sessions)
	return linked
}

func linkSessionGraphsContext(ctx context.Context, sessions []*model.Session) (map[*model.Session]bool, error) {
	// Newer Codex sidecars carry ParentID without a parent-side spawn announcement,
	// so graph ownership must be recoverable from parsed sessions alone.
	byParentAndID := make(map[string]*model.Session, len(sessions))
	ambiguousParentAndID := make(map[string]bool)
	byAgentAndID := make(map[string][]*model.Session, len(sessions))
	parsed := make(map[*model.Session]bool, len(sessions))
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if session == nil {
			continue
		}
		parsed[session] = true
		if session.ID != "" {
			key := string(session.Agent) + "\x00" + session.ID
			byAgentAndID[key] = append(byAgentAndID[key], session)
		}
		if session.ParentID != "" && session.ID != "" {
			key := string(session.Agent) + "\x00" + session.ParentID + "\x00" + session.ID
			if !ambiguousParentAndID[key] {
				if existing := byParentAndID[key]; existing != nil && existing != session {
					delete(byParentAndID, key)
					ambiguousParentAndID[key] = true
				} else {
					byParentAndID[key] = session
				}
			}
		}
	}
	linkedChildren := make(map[*model.Session]bool)
	parentOf := make(map[*model.Session]*model.Session)
	canLink := func(parent, child *model.Session) bool {
		if parent == nil || child == nil || parent == child {
			return false
		}
		if !parsed[child] {
			return true
		}
		if owner := parentOf[child]; owner != nil && owner != parent {
			return false
		}
		for current := parent; current != nil; current = parentOf[current] {
			if current == child {
				return false
			}
		}
		if parentOf[child] == nil {
			parentOf[child] = parent
			linkedChildren[child] = true
		}
		return true
	}
	var link func(*model.Session, map[*model.Session]bool) error
	link = func(session *model.Session, visiting map[*model.Session]bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if session == nil || visiting[session] {
			return nil
		}
		visiting[session] = true
		defer delete(visiting, session)
		for index := 0; index < len(session.Subagents); {
			child := session.Subagents[index]
			key := string(session.Agent) + "\x00" + session.ID + "\x00" + child.ID
			if actual := byParentAndID[key]; actual != nil && actual != session {
				if canLink(session, actual) {
					session.Subagents[index] = actual
					child = actual
				}
			} else if child.Path != "" && !strings.Contains(child.Path, "#") {
				info, err := os.Lstat(child.Path)
				if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					basePath := strings.SplitN(session.Path, "#", 2)[0]
					name := strings.TrimPrefix(child.AgentPath, "/root/")
					if name == "" {
						name = child.ID
					}
					child.Path = basePath + "#" + name
				}
			}
			if parsed[child] {
				if owner := parentOf[child]; owner == nil {
					if !canLink(session, child) {
						session.Subagents = append(session.Subagents[:index], session.Subagents[index+1:]...)
						continue
					}
				} else if owner != session {
					session.Subagents = append(session.Subagents[:index], session.Subagents[index+1:]...)
					continue
				}
			}
			if err := link(child, visiting); err != nil {
				return err
			}
			if child.UpdatedAt.After(session.UpdatedAt) {
				session.UpdatedAt = child.UpdatedAt
			}
			index++
		}
		return nil
	}
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := link(session, make(map[*model.Session]bool)); err != nil {
			return nil, err
		}
	}
	childDriven := make([]*model.Session, 0, len(sessions))
	for _, child := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := ""
		if child != nil && child.ParentID != "" && child.ID != "" {
			key = string(child.Agent) + "\x00" + child.ParentID + "\x00" + child.ID
		}
		if key != "" && !ambiguousParentAndID[key] {
			childDriven = append(childDriven, child)
		}
	}
	sort.SliceStable(childDriven, func(i, j int) bool {
		if !childDriven[i].StartedAt.Equal(childDriven[j].StartedAt) {
			return childDriven[i].StartedAt.Before(childDriven[j].StartedAt)
		}
		return childDriven[i].ID < childDriven[j].ID
	})
	for _, child := range childDriven {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parents := byAgentAndID[string(child.Agent)+"\x00"+child.ParentID]
		if len(parents) != 1 || parentOf[child] != nil {
			continue
		}
		parent := parents[0]
		if !canLink(parent, child) {
			continue
		}
		parent.Subagents = append(parent.Subagents, child)
		for current := parent; current != nil; current = parentOf[current] {
			if child.UpdatedAt.After(current.UpdatedAt) {
				current.UpdatedAt = child.UpdatedAt
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return linkedChildren, nil
}
