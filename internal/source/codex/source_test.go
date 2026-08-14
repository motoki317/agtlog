package codex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// tempRoot returns a temporary directory with symlinks resolved so it matches
// the canonical form NewSource stores. On macOS t.TempDir lives under a
// /var → /private/var symlink that Discover resolves, so the raw path would
// never equal the discovered one.
func tempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSourceCacheFingerprintDelegatesToParser(t *testing.T) {
	parser := testParser()
	if got, want := NewSource(parser, nil).CacheFingerprint(), parser.CacheFingerprint(); got != want {
		t.Fatalf("CacheFingerprint() = %q, want %q", got, want)
	}
}

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
	root := tempRoot(t)
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
