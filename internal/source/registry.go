package source

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/motoki317/agtlog/internal/model"
)

func (r *Registry) LoadDetail(ctx context.Context, session *model.Session) error {
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
			return loader.LoadEvents(ctx, session)
		}
	}
	return errors.New("detail loader unavailable")
}

type detailLoader interface {
	LoadEvents(context.Context, *model.Session) error
}

type Options struct {
	Workers  int
	CacheDir string
}

type Registry struct {
	sources []Source
	options Options
}

func NewRegistry(sources []Source, options Options) *Registry {
	return &Registry{sources: sources, options: options}
}

func (r *Registry) Discover(ctx context.Context) ([]*model.Session, error) {
	type job struct {
		source Source
		path   string
	}
	var pending []job
	for _, adapter := range r.sources {
		paths, err := adapter.Discover(ctx)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			pending = append(pending, job{source: adapter, path: path})
		}
	}

	workers := r.options.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, max(1, len(pending)))
	jobs := make(chan job, len(pending))
	results := make(chan *model.Session, len(pending))
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
				if session, err := r.discoverSession(item.source, item.path); err == nil {
					results <- session
				}
			}
		}()
	}
	group.Wait()
	close(results)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allSessions := make([]*model.Session, 0, len(results))
	for session := range results {
		allSessions = append(allSessions, session)
	}
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
	return sessions, nil
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
