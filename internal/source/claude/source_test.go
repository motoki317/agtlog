package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func TestDefaultRootsUsesClaudeConfigDirs(t *testing.T) {
	got := DefaultRoots("unused", "config-a, config-b")
	want := []string{filepath.Join("config-a", "projects"), filepath.Join("config-b", "projects")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRoots() = %v, want %v", got, want)
	}
}

func TestDefaultRootsDeduplicatesClaudeConfigDirs(t *testing.T) {
	got := DefaultRoots("unused", "config-a, config-a")
	want := []string{filepath.Join("config-a", "projects")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRoots() = %v, want %v", got, want)
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
	root := t.TempDir()
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

func TestDefaultRootsIncludesBothClaudeHomes(t *testing.T) {
	home := filepath.Join("fictional", "home")
	got := DefaultRoots(home, "")
	want := []string{
		filepath.Join(home, ".config", "claude", "projects"),
		filepath.Join(home, ".claude", "projects"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRoots() = %v, want %v", got, want)
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
	root := t.TempDir()
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
