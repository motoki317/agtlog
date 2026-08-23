package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

type benchmarkFollowSource struct {
	roots []string
	paths []string
}

func (source benchmarkFollowSource) Agent() model.AgentKind { return model.AgentClaude }
func (source benchmarkFollowSource) Roots() []string {
	return append([]string(nil), source.roots...)
}
func (benchmarkFollowSource) CacheFingerprint() string { return "benchmark-follow-v1" }
func (source benchmarkFollowSource) Discover(context.Context) ([]string, error) {
	return append([]string(nil), source.paths...), nil
}
func (benchmarkFollowSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return &model.Session{ID: filepath.Base(path), Agent: model.AgentClaude, Path: path}, nil
}
func (benchmarkFollowSource) Reprice(*model.Session) {}

func BenchmarkFollowerNonMirroredClaude(b *testing.B) {
	firstRoot := b.TempDir()
	secondRoot := b.TempDir()
	changedPath := filepath.Join(firstRoot, "changed.jsonl")
	unrelatedPath := filepath.Join(secondRoot, "unrelated.jsonl")
	for _, path := range []string{changedPath, unrelatedPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	registry := NewRegistry([]Source{benchmarkFollowSource{
		roots: []string{firstRoot, secondRoot}, paths: []string{changedPath, unrelatedPath},
	}}, Options{Workers: 1})
	if _, err := registry.Discover(context.Background()); err != nil {
		b.Fatal(err)
	}
	follower, err := registry.Follow(context.Background(), WatchOptions{Debounce: time.Hour, RescanInterval: time.Hour})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = follower.Close() }()

	b.ResetTimer()
	for range b.N {
		follower.watcher.events <- Change{Paths: []string{changedPath}}
		if update := <-follower.Updates(); len(update.Sessions) != 1 {
			b.Fatalf("sessions = %d, want 1", len(update.Sessions))
		}
	}
}
