package source

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
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
	sources []Source
	options Options
}

type DiscoveryDiagnostic struct {
	Agent model.AgentKind
	Path  string
	Err   error
}

func NewRegistry(sources []Source, options Options) *Registry {
	var roots []string
	for _, adapter := range sources {
		roots = append(roots, adapter.Roots()...)
	}
	if options.CacheDir != "" {
		if resolved, ok := ResolveCacheDir(options.CacheDir, roots); ok {
			options.CacheDir = resolved
		} else {
			options.CacheDir = ""
		}
	}
	registry := &Registry{sources: sources, options: options}
	registry.sweepStaleCacheTemps(time.Now())
	return registry
}

func (r *Registry) Discover(ctx context.Context) ([]*model.Session, error) {
	sessions, _, err := r.DiscoverWithDiagnostics(ctx)
	return sessions, err
}

func (r *Registry) DiscoverWithDiagnostics(ctx context.Context) ([]*model.Session, []DiscoveryDiagnostic, error) {
	type job struct {
		source Source
		path   string
	}
	type result struct {
		session     *model.Session
		diagnostics []DiscoveryDiagnostic
	}
	var pending []job
	for _, adapter := range r.sources {
		paths, err := adapter.Discover(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, path := range paths {
			pending = append(pending, job{source: adapter, path: path})
		}
	}
	if r.options.Progress != nil {
		r.options.Progress(0, len(pending))
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
				session, diagnostics, err := r.discoverSession(item.source, item.path)
				if err != nil {
					diagnostics = append(diagnostics, DiscoveryDiagnostic{Agent: item.source.Agent(), Path: item.path, Err: err})
					results <- result{diagnostics: diagnostics}
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
	completed := 0
	for item := range results {
		completed++
		if r.options.Progress != nil {
			r.options.Progress(completed, len(pending))
		}
		diagnostics = append(diagnostics, item.diagnostics...)
		if item.session != nil {
			allSessions = append(allSessions, item.session)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Agent != diagnostics[j].Agent {
			return diagnostics[i].Agent < diagnostics[j].Agent
		}
		return diagnostics[i].Path < diagnostics[j].Path
	})
	linkedChildren := linkSessionGraphs(allSessions)
	sessions := make([]*model.Session, 0, len(allSessions))
	for _, session := range allSessions {
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
	AttributeOwnership(sessions)
	return sessions, diagnostics, nil
}

func linkSessionGraphs(sessions []*model.Session) map[*model.Session]bool {
	byParentAndID := make(map[string]*model.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && session.ParentID != "" && session.ID != "" {
			key := string(session.Agent) + "\x00" + session.ParentID + "\x00" + session.ID
			byParentAndID[key] = session
		}
	}
	linkedChildren := make(map[*model.Session]bool)
	var link func(*model.Session, map[*model.Session]bool)
	link = func(session *model.Session, visiting map[*model.Session]bool) {
		if session == nil || visiting[session] {
			return
		}
		visiting[session] = true
		defer delete(visiting, session)
		for index, child := range session.Subagents {
			key := string(session.Agent) + "\x00" + session.ID + "\x00" + child.ID
			if actual := byParentAndID[key]; actual != nil && actual != session {
				session.Subagents[index] = actual
				child = actual
				linkedChildren[actual] = true
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
			link(child, visiting)
			if child.UpdatedAt.After(session.UpdatedAt) {
				session.UpdatedAt = child.UpdatedAt
			}
		}
	}
	for _, session := range sessions {
		link(session, make(map[*model.Session]bool))
	}
	return linkedChildren
}
