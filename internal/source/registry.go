package source

import (
	"context"
	"runtime"
	"sort"
	"sync"

	"github.com/motoki317/agtlog/internal/model"
)

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

	sessions := make([]*model.Session, 0, len(results))
	for session := range results {
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
