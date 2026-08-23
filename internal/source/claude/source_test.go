package claude

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

func TestSourceFingerprintExcludesPricingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := NewSource(NewParser(cost.NewCalculator(cost.Table{"model-a": {Input: 1}})), nil)
	second := NewSource(NewParser(cost.NewCalculator(cost.Table{"model-a": {Input: 2}})), nil)

	firstFingerprint, err := first.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := second.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("pricing changed source fingerprint from %q to %q", firstFingerprint, secondFingerprint)
	}
}

func TestSourceFingerprintAndAffectedPathStopForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := NewSource(testParser(), []string{filepath.Join("fictional", "projects")})
	if _, err := source.FingerprintContext(ctx, filepath.Join("fictional", "session.jsonl")); !errors.Is(err, context.Canceled) {
		t.Fatalf("FingerprintContext() error = %v, want context canceled", err)
	}
	if _, err := source.AffectedPathContext(ctx, filepath.Join("fictional", "agent-scout.jsonl")); !errors.Is(err, context.Canceled) {
		t.Fatalf("AffectedPathContext() error = %v, want context canceled", err)
	}
}

func TestSourceDiscoverReturnsTopLevelSessions(t *testing.T) {
	root := filepath.Join("testdata")
	source := NewSource(testParser(), []string{root})

	got, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	want := []string{filepath.Join(root, "project-alpha", "session-main.jsonl")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %v, want %v", got, want)
	}
}

func TestRootsTreatsClaudeConfigDirAsOneDirectory(t *testing.T) {
	got := Roots("unused", "config-a,config-b", nil)
	want := []string{filepath.Join("config-a,config-b", "projects")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestRootsAppendsAndDeduplicatesClaudeHomes(t *testing.T) {
	got := Roots("unused", "config-a", []string{"config-b", "config-a"})
	want := []string{filepath.Join("config-a", "projects"), filepath.Join("config-b", "projects")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestRootsDeduplicatesAbsoluteAndRelativeClaudeHomes(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o700); err != nil {
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
	want := []string{filepath.Join(home, "projects")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestSourceDiscoverDeduplicatesOverlappingRoots(t *testing.T) {
	root := filepath.Join("testdata")
	source := NewSource(testParser(), []string{root, root})

	got, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	want := []string{filepath.Join(root, "project-alpha", "session-main.jsonl")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %v, want %v", got, want)
	}
}

func TestSourceDiscoverIgnoresSymlinkedSessionFiles(t *testing.T) {
	root := tempRoot(t)
	target := filepath.Join(root, "session-target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "session-linked.jsonl")
	if err := os.Symlink(target, link); err != nil {
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

func TestRootsIncludesBothDefaultClaudeHomes(t *testing.T) {
	home := filepath.Join("fictional", "home")
	got := Roots(home, "", nil)
	want := []string{
		filepath.Join(home, ".config", "claude", "projects"),
		filepath.Join(home, ".claude", "projects"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestRootsAppendsExtraAfterDefaultClaudeHomes(t *testing.T) {
	home := filepath.Join("fictional", "home")
	extra := filepath.Join("fictional", "archive")
	got := Roots(home, "", []string{extra})
	want := []string{
		filepath.Join(home, ".config", "claude", "projects"),
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(extra, "projects"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %v, want %v", got, want)
	}
}

func TestAffectedPathMapsSubagentToParent(t *testing.T) {
	root := filepath.Join("fictional", "projects")
	path := filepath.Join(root, "project-alpha", "session-main", "subagents", "agent-scout.jsonl")
	source := NewSource(testParser(), []string{root})

	want := filepath.Join(root, "project-alpha", "session-main.jsonl")
	if got := source.AffectedPath(path); got != want {
		t.Fatalf("AffectedPath() = %q, want %q", got, want)
	}
}

func TestSourceLinksLegacyAgentFileToParent(t *testing.T) {
	root := tempRoot(t)
	project := filepath.Join(root, "project-legacy")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(project, "session-legacy.jsonl")
	parent := `{"type":"user","timestamp":"2026-01-02T03:00:00Z","sessionId":"session-legacy","message":{"content":"Parent"}}` + "\n"
	if err := os.WriteFile(parentPath, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(project, "agent-scout.jsonl")
	agent := `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","sessionId":"session-legacy","agentId":"scout","isSidechain":true,"requestId":"request-scout","message":{"id":"message-scout","model":"claude-opus-4-8","usage":{"input_tokens":5}}}` + "\n"
	if err := os.WriteFile(agentPath, []byte(agent), 0o600); err != nil {
		t.Fatal(err)
	}
	source := NewSource(testParser(), []string{root})

	paths, err := source.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !reflect.DeepEqual(paths, []string{parentPath}) {
		t.Fatalf("Discover() = %v, want only parent %v", paths, parentPath)
	}
	session, err := source.Parse(parentPath)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(session.Subagents) != 1 || session.Subagents[0].ID != "scout" || session.TotalUsage().InputTokens != 5 {
		t.Fatalf("Parse() = %#v, want linked scout with five tokens", session)
	}
	if got := source.AffectedPath(agentPath); got != parentPath {
		t.Fatalf("AffectedPath() = %q, want %q", got, parentPath)
	}
}
