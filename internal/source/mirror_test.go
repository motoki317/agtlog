package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
)

func TestBuildSessionSnapshotCollapsesExactMirrorsBeforeLinking(t *testing.T) {
	directory := t.TempDir()
	content := []byte("shared fictional rollout\n")
	newSession := func(name string) *model.Session {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return &model.Session{
			ID: "thread-shared", Agent: model.AgentCodex, Path: path, SourceSize: int64(len(content)),
			Events: []model.Event{{RecordRef: model.RecordRef{Path: path}}},
		}
	}
	first := newSession("a.jsonl")
	middle := newSession("m.jsonl")
	last := newSession("z.jsonl")
	orders := [][]*model.Session{
		{last, middle, first},
		{middle, first, last},
		{first, last, middle},
	}
	for _, order := range orders {
		sessions, err := buildSessionSnapshotContext(context.Background(), order)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 || sessions[0].Path != first.Path {
			t.Fatalf("snapshot sessions = %#v, want lexical-path mirror winner", sessions)
		}
	}
}

func TestBuildSessionSnapshotKeepsSameIdentityWithDifferentBytes(t *testing.T) {
	directory := t.TempDir()
	newSession := func(name, content string) *model.Session {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return &model.Session{
			ID: "thread-shared", Agent: model.AgentCodex, Path: path, SourceSize: int64(len(content)), Title: "Same parsed state",
		}
	}
	first := newSession("a.jsonl", "assistant text alpha\n")
	second := newSession("z.jsonl", "assistant text bravo\n")

	sessions, err := buildSessionSnapshotContext(context.Background(), []*model.Session{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("snapshot sessions = %#v, want distinct source bytes retained", sessions)
	}
}
