package codex

import (
	"context"
	"os"
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

func TestSourceDiscoverIgnoresSymlinkedRolloutFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "rollout-target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "rollout-linked.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := NewSource(testParser(), []string{root}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	want := []string{target}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %v, want %v", got, want)
	}
}
