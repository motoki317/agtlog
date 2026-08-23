package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/motoki317/agtlog/internal/cost"
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

func TestSourceFingerprintExcludesPricingPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := NewSource(NewParser(cost.NewCalculator(cost.Table{"fallback-a": {Input: 1}}), "fallback-a"), nil)
	second := NewSource(NewParser(cost.NewCalculator(cost.Table{"fallback-b": {Input: 2}}), "fallback-b"), nil)

	firstFingerprint, err := first.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := second.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("pricing policy changed source fingerprint from %q to %q", firstFingerprint, secondFingerprint)
	}
}

func TestSourceFingerprintStopsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := NewSource(testParser(), []string{filepath.Join("fictional", "sessions")})
	if _, err := source.FingerprintContext(ctx, filepath.Join("fictional", "rollout.jsonl")); !errors.Is(err, context.Canceled) {
		t.Fatalf("FingerprintContext() error = %v, want context canceled", err)
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

func TestRootsAppendsAndDeduplicatesCodexHomes(t *testing.T) {
	got := Roots("unused", "codex-home", []string{"archive-home", "codex-home"})
	want := []string{filepath.Join("codex-home", "sessions"), filepath.Join("archive-home", "sessions")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestRootsDeduplicatesAbsoluteAndRelativeCodexHomes(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(workingDirectory, home)
	if err != nil {
		t.Fatal(err)
	}
	got := Roots("unused", home, []string{relative})
	want := []string{filepath.Join(home, "sessions")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestRootsUsesDefaultCodexHome(t *testing.T) {
	home := filepath.Join("fictional", "home")
	got := Roots(home, "", nil)
	want := []string{filepath.Join(home, ".codex", "sessions")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestRootsAppendsExtraAfterDefaultCodexHome(t *testing.T) {
	home := filepath.Join("fictional", "home")
	extra := filepath.Join("fictional", "archive")
	got := Roots(home, "", []string{extra})
	want := []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(extra, "sessions"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
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
