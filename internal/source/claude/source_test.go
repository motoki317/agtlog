package claude

import (
	"context"
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
