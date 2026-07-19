package codex

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSourceDiscoverReturnsRolloutFiles(t *testing.T) {
	root := filepath.Join("testdata", "sessions")
	source := NewSource(testParser(), []string{root})

	got, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	want := []string{
		filepath.Join(root, "2026", "01", "02", "rollout-deltas-only.jsonl"),
		filepath.Join(root, "2026", "01", "02", "rollout-session-main.jsonl"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %v, want %v", got, want)
	}
}

func TestDefaultRootsUsesCodexHome(t *testing.T) {
	got := DefaultRoots("unused", "codex-home")
	want := []string{filepath.Join("codex-home", "sessions")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRoots() = %v, want %v", got, want)
	}
}

func TestDefaultRootsUsesUserCodexDirectory(t *testing.T) {
	home := filepath.Join("fictional", "home")
	got := DefaultRoots(home, "")
	want := []string{filepath.Join(home, ".codex", "sessions")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRoots() = %v, want %v", got, want)
	}
}
