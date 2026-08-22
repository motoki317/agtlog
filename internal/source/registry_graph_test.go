package source

import (
	"context"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

func TestBuildSessionSnapshotCopiesInputsBeforeLinking(t *testing.T) {
	parentUpdated := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	childUpdated := parentUpdated.Add(time.Minute)
	parentPath := "/fictional/follow/root.jsonl"
	announced := &model.Session{ID: "thread-child", Agent: model.AgentCodex, Path: parentPath + "#scout"}
	missing := &model.Session{
		ID: "thread-missing", Agent: model.AgentCodex, Path: "/fictional/follow/missing.jsonl", AgentPath: "/root/missing",
	}
	parent := &model.Session{
		ID: "thread-root", Agent: model.AgentCodex, Path: parentPath, UpdatedAt: parentUpdated,
		Subagents: []*model.Session{announced, missing},
	}
	child := &model.Session{
		ID: "thread-child", ParentID: parent.ID, Agent: model.AgentCodex,
		Path: "/fictional/follow/child.jsonl", UpdatedAt: childUpdated,
	}

	var previous *model.Session
	for range 2 {
		snapshot, err := buildSessionSnapshotContext(context.Background(), []*model.Session{parent, child})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot) != 1 || snapshot[0] == parent || len(snapshot[0].Subagents) != 2 {
			t.Fatalf("snapshot graph = %#v, want one copied root with two children", snapshot)
		}
		if !snapshot[0].UpdatedAt.Equal(childUpdated) {
			t.Fatalf("snapshot UpdatedAt = %v, want child roll-up %v", snapshot[0].UpdatedAt, childUpdated)
		}
		children := make(map[string]*model.Session, len(snapshot[0].Subagents))
		for _, snapshotChild := range snapshot[0].Subagents {
			children[snapshotChild.ID] = snapshotChild
		}
		if children[child.ID] == nil || children[child.ID].Path != child.Path {
			t.Fatalf("linked child = %#v, want parsed sidecar", children[child.ID])
		}
		if children[missing.ID] == nil || children[missing.ID].Path != parentPath+"#missing" {
			t.Fatalf("missing child path = %#v, want a copied placeholder path", children[missing.ID])
		}
		if previous != nil && (snapshot[0] == previous || snapshot[0].Subagents[0] == previous.Subagents[0]) {
			t.Fatal("repeated snapshot reused a graph node from the previous linked copy")
		}
		previous = snapshot[0]
	}

	if !parent.UpdatedAt.Equal(parentUpdated) || len(parent.Subagents) != 2 || parent.Subagents[0] != announced || parent.Subagents[1] != missing {
		t.Fatalf("linking mutated indexed parent = %#v", parent)
	}
	if missing.Path != "/fictional/follow/missing.jsonl" {
		t.Fatalf("linking mutated indexed placeholder path = %q", missing.Path)
	}
}

func TestLinkSessionGraphsChildDrivenLinksInStableOrder(t *testing.T) {
	parent := &model.Session{ID: "thread-root", Agent: model.AgentCodex, Path: "/fictional/moon/root.jsonl"}
	children := []*model.Session{
		{ID: "thread-zeta", ParentID: parent.ID, Agent: model.AgentCodex, Path: "/fictional/moon/zeta.jsonl", StartedAt: time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)},
		{ID: "thread-beta", ParentID: parent.ID, Agent: model.AgentCodex, Path: "/fictional/moon/beta.jsonl", StartedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)},
		{ID: "thread-alpha", ParentID: parent.ID, Agent: model.AgentCodex, Path: "/fictional/moon/alpha.jsonl", StartedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)},
	}

	linked := linkSessionGraphs([]*model.Session{children[0], children[1], parent, children[2]})

	if len(parent.Subagents) != 3 {
		t.Fatalf("child-driven count = %d, want 3", len(parent.Subagents))
	}
	got := []string{parent.Subagents[0].ID, parent.Subagents[1].ID, parent.Subagents[2].ID}
	if got[0] != "thread-alpha" || got[1] != "thread-beta" || got[2] != "thread-zeta" {
		t.Fatalf("child-driven order = %v, want alpha, beta, zeta", got)
	}
	for _, child := range children {
		if !linked[child] {
			t.Fatalf("child %q was not marked linked", child.ID)
		}
	}
}

func TestLinkSessionGraphsKeepsRepeatedAgentPathSidecars(t *testing.T) {
	parent := &model.Session{ID: "thread-orbit-root", Agent: model.AgentCodex, Path: "/fictional/orbit/root.jsonl"}
	announced := &model.Session{
		ID:        "thread-feedback-second",
		Agent:     model.AgentCodex,
		Path:      "/fictional/orbit/root.jsonl#orbit_review",
		Title:     "orbit_review",
		AgentPath: "/root/orbit_review",
	}
	first := &model.Session{
		ID:        "thread-feedback-first",
		ParentID:  parent.ID,
		Agent:     model.AgentCodex,
		Path:      "/fictional/orbit/first.jsonl",
		Title:     "orbit_review",
		AgentPath: "/root/orbit_review",
		StartedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
	}
	second := &model.Session{
		ID:        "thread-feedback-second",
		ParentID:  parent.ID,
		Agent:     model.AgentCodex,
		Path:      "/fictional/orbit/second.jsonl",
		Title:     "orbit_review",
		AgentPath: "/root/orbit_review",
		StartedAt: time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC),
	}
	parent.Subagents = []*model.Session{announced}

	linked := linkSessionGraphs([]*model.Session{parent, second, first})

	if len(parent.Subagents) != 2 {
		t.Fatalf("reused agent path children = %d, want two", len(parent.Subagents))
	}
	for _, want := range []*model.Session{first, second} {
		count := 0
		for _, got := range parent.Subagents {
			if got == want {
				count++
			}
		}
		if count != 1 || !linked[want] {
			t.Fatalf("sidecar %q occurs %d times and linked=%v, want once and linked", want.ID, count, linked[want])
		}
	}
}

func TestLinkSessionGraphsDoesNotDuplicateOrReparent(t *testing.T) {
	t.Run("already linked", func(t *testing.T) {
		parent := &model.Session{ID: "thread-root", Agent: model.AgentCodex, Path: "/fictional/atlas/root.jsonl"}
		child := &model.Session{ID: "thread-child", ParentID: parent.ID, Agent: model.AgentCodex, Path: "/fictional/atlas/child.jsonl"}
		parent.Subagents = []*model.Session{child}

		linked := linkSessionGraphs([]*model.Session{parent, child})

		if len(parent.Subagents) != 1 || parent.Subagents[0] != child || !linked[child] {
			t.Fatalf("already-linked graph = %#v/%v, want one linked child", parent.Subagents, linked)
		}
	})

	t.Run("does not reparent", func(t *testing.T) {
		announcedParent := &model.Session{ID: "thread-announced", Agent: model.AgentCodex, Path: "/fictional/atlas/announced.jsonl"}
		parentID := &model.Session{ID: "thread-declared", Agent: model.AgentCodex, Path: "/fictional/atlas/declared.jsonl"}
		child := &model.Session{ID: "thread-child", ParentID: parentID.ID, Agent: model.AgentCodex, Path: "/fictional/atlas/child.jsonl"}
		announcedParent.Subagents = []*model.Session{child}

		linkSessionGraphs([]*model.Session{announcedParent, parentID, child})

		if len(announcedParent.Subagents) != 1 || announcedParent.Subagents[0] != child || len(parentID.Subagents) != 0 {
			t.Fatalf("reparented graph = announced %v, declared %v; want child to stay announced", announcedParent.Subagents, parentID.Subagents)
		}
	})

	t.Run("matches the same agent only", func(t *testing.T) {
		parent := &model.Session{ID: "thread-root", Agent: model.AgentCodex, Path: "/fictional/atlas/codex-root.jsonl"}
		otherAgentParent := &model.Session{ID: parent.ID, Agent: model.AgentClaude, Path: "/fictional/atlas/claude-root.jsonl"}
		child := &model.Session{ID: "thread-child", ParentID: parent.ID, Agent: model.AgentClaude, Path: "/fictional/atlas/claude-child.jsonl"}

		linkSessionGraphs([]*model.Session{parent, otherAgentParent, child})

		if len(parent.Subagents) != 0 || len(otherAgentParent.Subagents) != 1 || otherAgentParent.Subagents[0] != child {
			t.Fatalf("cross-agent graph = codex %v, claude %v; want only same-agent parent", parent.Subagents, otherAgentParent.Subagents)
		}
	})

	t.Run("leaves ambiguous child identities unlinked", func(t *testing.T) {
		parent := &model.Session{ID: "thread-root", Agent: model.AgentCodex, Path: "/fictional/atlas/root.jsonl"}
		first := &model.Session{ID: "thread-child", ParentID: parent.ID, Agent: model.AgentCodex, Path: "/fictional/atlas/first.jsonl"}
		second := &model.Session{ID: first.ID, ParentID: parent.ID, Agent: model.AgentCodex, Path: "/fictional/atlas/second.jsonl"}

		linked := linkSessionGraphs([]*model.Session{parent, first, second})

		if len(parent.Subagents) != 0 || linked[first] || linked[second] {
			t.Fatalf("ambiguous child graph = parent %v, linked %v; want both children unlinked", parent.Subagents, linked)
		}
	})
}

func TestLinkSessionGraphsRejectsChildDrivenCycle(t *testing.T) {
	first := &model.Session{ID: "thread-cycle-a", ParentID: "thread-cycle-b", Agent: model.AgentCodex, Path: "/fictional/constellation/a.jsonl"}
	second := &model.Session{ID: "thread-cycle-b", ParentID: "thread-cycle-a", Agent: model.AgentCodex, Path: "/fictional/constellation/b.jsonl"}

	linked := linkSessionGraphs([]*model.Session{second, first})

	if len(first.Subagents)+len(second.Subagents) != 1 {
		t.Fatalf("cyclic graph edges = %d, want one accepted edge", len(first.Subagents)+len(second.Subagents))
	}
	if linked[first] == linked[second] {
		t.Fatalf("cyclic graph linked map = %v, want exactly one child linked", linked)
	}
	root := first
	if linked[first] {
		root = second
	}
	if root == nil || len(root.Subagents) != 1 || root.Subagents[0].Subagents != nil {
		t.Fatalf("cyclic graph = first %v, second %v; want acyclic one-level tree", first.Subagents, second.Subagents)
	}

	first = &model.Session{ID: "thread-existing-a", Agent: model.AgentCodex, Path: "/fictional/constellation/existing-a.jsonl"}
	second = &model.Session{ID: "thread-existing-b", Agent: model.AgentCodex, Path: "/fictional/constellation/existing-b.jsonl"}
	first.Subagents = []*model.Session{second}
	second.Subagents = []*model.Session{first}
	linkSessionGraphs([]*model.Session{first, second})
	if len(first.Subagents) != 1 || len(second.Subagents) != 0 || first.Subagents[0] != second {
		t.Fatalf("existing cyclic graph = first %v, second %v; want rejected back-edge", first.Subagents, second.Subagents)
	}
}
